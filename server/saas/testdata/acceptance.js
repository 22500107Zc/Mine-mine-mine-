// CulpOS end-to-end acceptance run (real browser) against a running instance.
//
// Prerequisites: CulpOS started with STRIPE_API_BASE pointing at
// stripe_mock.py and SMTP delivered to a local catcher that writes one .eml
// file per message into MAIL.
//
//   B=http://localhost:18080 MOCK=http://localhost:18181 MAIL=/tmp/mail SHOTS=/tmp/shots //   PGURL=postgres://... FOUNDER_PW=... node acceptance.js
//
// Optional: PLAYWRIGHT (module path), CHROMIUM (browser executable)
const { chromium } = require(process.env.PLAYWRIGHT || 'playwright');
const fs = require('fs');
const { execSync } = require('child_process');

const B = process.env.B || 'http://localhost:18080';
const MOCK = process.env.MOCK || 'http://localhost:18181';
const SHOTS = process.env.SHOTS, MAIL = process.env.MAIL, PGURL = process.env.PGURL;
const FOUNDER_PW = process.env.FOUNDER_PW;
const bad = /corteza|planet ?crust|low-code|no-code|lorem|placeholder|supabase|vercel/i;
const results = [];
let shotN = 0;

function ok(step, pass, detail = '') {
  results.push({ step, pass: !!pass, detail });
  console.log(`${pass ? 'PASS' : 'FAIL'}  ${step}${detail ? '  — ' + detail : ''}`);
}
async function snap(page, label) {
  const n = String(++shotN).padStart(2, '0');
  await page.screenshot({ path: `${SHOTS}/${n}-${label}.png`, fullPage: true }).catch(() => {});
  const txt = await page.evaluate(() => document.body.innerText + ' ' + document.title).catch(() => '');
  const m = txt.match(bad);
  if (m) ok(`no upstream/placeholder text on ${label}`, false, m[0]);
  return txt;
}
const sql = q => execSync(`psql "${PGURL}" -tA -c "${q.replace(/"/g, '\\"')}"`).toString().trim();
const sleep = ms => new Promise(r => setTimeout(r, ms));
function mails(to) {
  return fs.readdirSync(MAIL).filter(f => f.endsWith(`-${to}.eml`)).sort().map(f => {
    const raw = fs.readFileSync(`${MAIL}/${f}`, 'utf8');
    const subj = (raw.match(/^Subject: (.*)$/mi) || [])[1] || '';
    const body = raw.replace(/=\r?\n/g, '').replace(/=([0-9A-F]{2})/g, (_, h) => String.fromCharCode(parseInt(h, 16)));
    return { f, subj: decodeSubj(subj), body };
  });
}
function decodeSubj(s) {
  return s.replace(/=\?utf-8\?([qb])\?(.*?)\?=/gi, (_, enc, t) => enc.toLowerCase() === 'b'
    ? Buffer.from(t, 'base64').toString('utf8')
    : decodeURIComponent(t.replace(/_/g, ' ').replace(/%/g, '%25').replace(/=([0-9A-F]{2})/gi, '%$1')));
}
async function waitMail(to, re, ms = 20000) {
  const end = Date.now() + ms;
  while (Date.now() < end) {
    const m = mails(to).filter(x => re.test(x.subj));
    if (m.length) return m[m.length - 1];
    await sleep(500);
  }
  return null;
}
async function post(page, sel) {
  await Promise.all([page.waitForNavigation({ waitUntil: 'load' }), page.click(sel)]);
}

async function signup(b, co, first, email, pw) {
  const ctx = await b.newContext({ viewport: { width: 1366, height: 900 } });
  const page = await ctx.newPage();
  page.on('dialog', d => d.accept());
  await page.goto(B + '/signup');
  const t = await snap(page, `signup-${first}`);
  ok(`${co}: signup shows $333.88/month`, /\$333\.88/.test(t) && /month/i.test(t));
  await page.fill('#companyName', co);
  await page.fill('#firstName', first); await page.fill('#lastName', 'Owner');
  await page.fill('#email', email); await page.fill('#password', pw);
  const terms = await page.$('input[name=acceptTerms], #acceptTerms, input[type=checkbox]');
  if (terms) await terms.check();
  await post(page, 'button[type=submit]');
  ok(`${co}: server redirected to hosted checkout`, page.url().startsWith(MOCK + '/checkout/cs_'), page.url());
  const ct = await snap(page, `checkout-${first}`);
  ok(`${co}: checkout charges $333.88 per month`, ct.includes('$333.88 per month'));
  const before = sql(`select count(*) from saas_companies where name='${co}' and provisioning_status='provisioned'`);
  ok(`${co}: not provisioned before payment`, before === '0');
  await post(page, '#pay');
  for (let i = 0; i < 15 && !(await page.content()).includes('Your workspace is ready'); i++) await page.waitForTimeout(1000).then(() => page.reload());
  const done = await snap(page, `signup-complete-${first}`);
  ok(`${co}: webhook activated company`, done.includes('Your workspace is ready'));
  ok(`${co}: company active in database`, sql(`select subscription_status||'/'||provisioning_status from saas_companies where name='${co}'`) === 'active/provisioned');
  await page.click('text=Sign In to CulpOS'); await page.waitForLoadState('networkidle');
  await page.fill('input[name=email]', email); await page.fill('input[name=password]', pw);
  await post(page, '[data-test-id=button-login]');
  await page.waitForTimeout(1500);
  return { ctx, page };
}

(async () => {
  for (const f of fs.readdirSync(MAIL)) fs.unlinkSync(`${MAIL}/${f}`);
  const b = await chromium.launch(process.env.CHROMIUM ? { executablePath: process.env.CHROMIUM } : {});

  // ---------- public surface ----------
  {
    const h = await (await fetch(B + '/health')).json();
    ok('health endpoint reports ok without secrets', h.status === 'ok' && h.database === 'ok' && !JSON.stringify(h).match(/sk_|whsec_|postgres:\/\//), JSON.stringify(h));
    const ctx = await b.newContext(); const page = await ctx.newPage();
    for (const p of ['/legal/terms', '/legal/privacy', '/legal/acceptable-use', '/legal/billing', '/legal/cancellation', '/legal/refunds', '/legal/open-source', '/support']) {
      const r = await page.goto(B + p);
      const t = await page.evaluate(() => document.body.innerText);
      ok(`public page ${p}`, r.status() === 200 && /Terms/.test(t) && /Open Source Notices/.test(t) && /Culp Industries/.test(t));
    }
    await snap(page, 'legal-terms');
    await page.goto(B + '/'); await page.waitForLoadState('networkidle');
    ok('anonymous / goes to sign-in', /\/auth\/login/.test(page.url()), page.url());
    await snap(page, 'login');
    await ctx.close();
  }

  // ---------- company A signup + onboarding ----------
  const A = await signup(b, 'Acme Operations', 'Ada', 'owner@acme.test', 'Str0ngPassw0rd!');
  {
    const page = A.page;
    ok('owner lands in onboarding after first sign-in', page.url().includes('/welcome'), page.url());
    ok('welcome email sent', !!(await waitMail('owner@acme.test', /welcome/i)));
    await snap(page, 'onboarding-1-profile');
    await page.fill('#p-industry', 'Logistics'); await page.fill('#p-phone', '+1 512 555 0100'); await page.fill('#p-website', 'https://acme.test');
    await post(page, 'text=Save and continue');
    ok('onboarding: profile saved', page.url().includes('step=2') && sql(`select profile_industry from saas_companies where name='Acme Operations'`) === 'Logistics');
    await page.fill('#i-email', 'eve@acme.test'); await page.fill('#i-name', 'Eve Employee'); await page.selectOption('#i-role', 'employee');
    await post(page, 'text=Send invitation');
    const inv = await snap(page, 'onboarding-2-invited');
    ok('onboarding: invitation sent', inv.includes('Invitation sent') && inv.includes('eve@acme.test'));
    await page.click('a:has-text("Continue")'); await page.waitForLoadState('load');
    await page.fill('#c-name', 'Northwind Traders'); await page.fill('#c-email', 'buyer@northwind.test');
    await post(page, 'text=Add customer');
    ok('onboarding: first customer created', (await page.content()).includes('Customer added'));
    await page.fill('#t-title', 'Call Northwind about Q4 order');
    await post(page, 'text=Create task');
    ok('onboarding: first task created', (await page.content()).includes('Task created'));
    await snap(page, 'onboarding-5-finish');
    await post(page, 'text=Go to my Dashboard');
    await page.waitForURL(/\/compose\/ns\/.+\/pages/, { timeout: 30000 }).catch(() => {});
    await page.waitForTimeout(4000);
    const dash = await snap(page, 'owner-dashboard');
    ok('owner sees Dashboard with workspace navigation', ['Dashboard', 'Customers', 'Tasks'].every(x => dash.includes(x)), page.url());
    ok('onboarding completed recorded', sql(`select onboarding_completed_at is not null from saas_companies where name='Acme Operations'`) === 't');

    // Customers page via UI shows onboarding customer, then create one through the app UI.
    await page.click('nav >> text=Customers').catch(() => page.click('text=Customers'));
    await page.waitForTimeout(3500);
    const cust = await snap(page, 'customers-list');
    ok('Customers list shows onboarding customer', cust.includes('Northwind Traders'));
    await page.click('[data-test-id=button-add-record]');
    await page.waitForTimeout(2500);
    const name = page.getByLabel('Name', { exact: true });
    if (await name.count()) await name.first().fill('Contoso Freight');
    else await page.locator('main input[type=text]').first().fill('Contoso Freight');
    await snap(page, 'customer-create-form');
    await page.click('[data-test-id=button-save]');
    await page.waitForTimeout(3000);
    await snap(page, 'customer-created');
    const nsA = sql(`select namespace_id from saas_companies where name='Acme Operations'`);
    const n = sql(`select count(*) from compose_record r join compose_module m on m.id=r.rel_module where m.rel_namespace=${nsA} and m.handle='Customer' and r.deleted_at is null`);
    ok('owner created a customer through the app UI', n === '2', `customers=${n}`);
    await page.click('nav >> text=Tasks').catch(() => page.click('text=Tasks'));
    await page.waitForTimeout(3500);
    ok('Tasks list shows onboarding task', (await snap(page, 'tasks-list')).includes('Call Northwind'));

    // Command Deck: built from the company's own recorded activity
    await page.goto(B + '/');
    await page.waitForTimeout(3000);
    ok('launcher offers the Command Deck to the owner', (await page.evaluate(() => document.body.innerText)).includes('Command Deck'));
    await page.goto(B + '/command');
    const deck = await snap(page, 'command-deck');
    ok('Command Deck shows all sections', ['Activity 7d', 'Completion rate', 'What is happening', 'Where is it happening',
      'Why might it be happening', 'What is it affecting', 'What should we test next'].every(x => deck.toLowerCase().includes(x.toLowerCase())));
    const evCount = Number(((deck.match(/([\d,]+) events \/ 12 months/i) || [])[1] || '0').replace(/,/g, ''));
    ok('Command Deck counts the recorded activity', evCount >= 3, String(evCount));
    ok('Command Deck shows no demo data', !/demo/i.test(deck));
    await page.goto(B + '/command/activity?day=' + new Date().toISOString().slice(0, 10));
    const day = await snap(page, 'command-deck-day');
    ok('Activity Graph day lists records created in the app', ['Northwind Traders', 'Contoso Freight', 'Call Northwind about Q4 order'].every(x => day.includes(x)));
    const recLink = await page.locator('a[href*="/record/"]').first().getAttribute('href').catch(() => null);
    ok('day records link into the workspace', !!recLink && recLink.includes('/compose/ns/'), recLink || '');
    for (const [p, label] of [['/command/pipeline', 'Pipeline & bottlenecks'], ['/command/goals', 'Goal Intelligence']]) {
      await page.goto(B + p);
      ok(`Command Deck tab ${p}`, (await snap(page, p.split('/').pop())).toLowerCase().includes(label.toLowerCase()));
    }

    await page.goto(B + '/billing');
    const bill = await snap(page, 'billing-active');
    ok('/billing: active, $333.88, next billing date, Manage Billing', /Active/.test(bill) && bill.includes('$333.88') && /Next billing date/i.test(bill) && bill.includes('Manage Billing'));
    await post(page, 'button:has-text("Manage Billing")');
    ok('Manage Billing opens server-created portal session', page.url().startsWith(MOCK + '/portal/cus_'), page.url());
    await post(page, '#back');
    ok('portal returns to /billing', page.url() === B + '/billing', page.url());
  }

  // ---------- employee accepts invitation ----------
  let E;
  {
    const m = await waitMail('eve@acme.test', /invit/i);
    const link = m && (m.body.match(/https?:\/\/[^"'\s<>]+/g) || []).find(u => u.startsWith(B) && /invit|token/i.test(u));
    ok('invitation email delivered with accept link', !!link, m ? m.subj : 'no mail');
    const ctx = await b.newContext({ viewport: { width: 1366, height: 900 } });
    const page = await ctx.newPage();
    await page.goto(link.replace(/&amp;/g, '&')); await page.waitForLoadState('networkidle');
    await snap(page, 'invite-accept');
    await page.fill('input[name=password]', 'EvePassw0rd!23');
    const cp = await page.$('input[name=confirmPassword]'); if (cp) await cp.fill('EvePassw0rd!23');
    await post(page, 'button[type=submit]');
    await page.goto(B + '/'); await page.waitForTimeout(3000);
    const home = await snap(page, 'employee-home');
    ok('employee home shows Workspace', home.includes('Workspace'));
    ok('employee launcher hides owner-only apps', !home.includes('Command Deck') && !home.includes('Billing'));
    await page.goto(B + '/compose/'); await page.waitForURL(/\/compose\/ns\/.+\/pages/, { timeout: 30000 }).catch(() => {});
    await page.waitForTimeout(4000);
    const t = await snap(page, 'employee-dashboard');
    ok('employee accepted invite and sees company workspace', ['Dashboard', 'Customers', 'Tasks'].every(x => t.includes(x)), page.url());
    await page.goto(B + '/billing');
    ok('employee cannot manage billing', !(await page.content()).includes('Cancel Subscription'));
    const deckRsp = await page.goto(B + '/command');
    ok('employee cannot open the Command Deck', deckRsp.status() === 403, String(deckRsp.status()));
    await page.goto(B + '/founder/dashboard');
    ok('employee cannot reach Founder dashboard', /\/founder$|\/founder\?|\/founder\/login/.test(page.url()) || !(await page.content()).includes('Monthly Recurring'), page.url());
    E = { ctx, page };
  }

  // ---------- company B ----------
  const Bc = await signup(b, 'Beta Logistics', 'Ben', 'owner@beta.test', 'Str0ngPassw0rd!');
  let tokB;
  {
    const page = Bc.page;
    page.on('request', r => { const a = r.headers()['authorization']; if (a && a.startsWith('Bearer ')) tokB = a; });
    await post(page, 'text=Skip setup and go to my Dashboard');
    await page.waitForTimeout(5000);
    ok('company B skipped onboarding', sql(`select onboarding_completed_at is not null from saas_companies where name='Beta Logistics'`) === 't');
  }

  // ---------- cross-company isolation ----------
  {
    let tokA;
    const page = A.page;
    page.on('request', r => { const a = r.headers()['authorization']; if (a && a.startsWith('Bearer ')) tokA = a; });
    await page.goto(B + '/'); await page.waitForTimeout(5000);
    const nsB = sql(`select namespace_id from saas_companies where name='Beta Logistics'`);
    const slugB = sql(`select slug from saas_companies where name='Beta Logistics'`);
    const modB = sql(`select id from compose_module where rel_namespace=${nsB} and handle='Customer'`);
    const api = (tok, path, opt = {}) => fetch(B + '/api' + path, { ...opt, headers: { Authorization: tok, 'Content-Type': 'application/json', ...(opt.headers || {}) } });
    const created = await (await api(tokB, `/compose/namespace/${nsB}/module/${modB}/record/`, { method: 'POST', body: JSON.stringify({ values: [{ name: 'Name', value: 'Beta Confidential Client' }] }) })).json().catch(() => ({}));
    const recB = created.response && created.response.recordID;
    ok('company B created its own record via API', !!recB);
    const probes = [
      ['GET record', `/compose/namespace/${nsB}/module/${modB}/record/${recB}`, {}],
      ['GET list', `/compose/namespace/${nsB}/module/${modB}/record/`, {}],
      ['POST update', `/compose/namespace/${nsB}/module/${modB}/record/${recB}`, { method: 'POST', body: JSON.stringify({ values: [{ name: 'Name', value: 'pwned' }] }) }],
      ['DELETE', `/compose/namespace/${nsB}/module/${modB}/record/${recB}`, { method: 'DELETE' }],
      ['export', `/compose/namespace/${nsB}/module/${modB}/record/export.csv?fields=Name`, {}],
      ['namespace', `/compose/namespace/${nsB}`, {}],
    ];
    for (const [label, path, opt] of probes) {
      const r = await api(tokA, path, opt); const body = await r.text();
      const leaked = body.includes('Beta Confidential') || body.includes('pwned');
      const denied = r.status >= 400 || /"error"/.test(body);
      ok(`isolation: A → B ${label} denied`, denied && !leaked, `${r.status} ${body.slice(0, 90)}`);
    }
    const stillThere = sql(`select count(*) from compose_record where id=${recB} and deleted_at is null`);
    ok('isolation: B record intact after A attacks', stillThere === '1');
    const users = await (await api(tokA, '/system/users/')).text();
    ok('isolation: A user list excludes B users', !users.includes('owner@beta.test'));
    await page.goto(`${B}/compose/ns/${slugB}/pages`); await page.waitForTimeout(4000);
    const t = await snap(page, 'cross-company-ui-attempt');
    ok('isolation: A cannot open B workspace in UI', !t.includes('Beta Confidential') && !t.includes('Beta Logistics'));
  }

  // ---------- Founder ----------
  const F = await b.newContext({ viewport: { width: 1366, height: 900 } });
  const fp = await F.newPage(); fp.on('dialog', d => d.accept());
  {
    // Founder Access is password-only: no username, no email, no selector
    const identityInputs = 'input[type=email], input[type=text], input[name*=user i], input[name*=email i], input[autocomplete=username], input[autocomplete=email], select, textarea';
    await fp.goto(B + '/founder');
    await snap(fp, 'founder-access');
    const loginText = (await fp.evaluate(() => document.body.innerText)).replace(/\s+/g, ' ').trim();
    const inputs = fp.locator('input:not([type=hidden])');
    ok('Founder Access shows exactly CulpOS / Founder Access / Password / Sign In',
      loginText === 'CulpOS Founder Access Password Sign In', loginText);
    ok('Founder Access has exactly one input: Password',
      (await inputs.count()) === 1 && (await inputs.first().getAttribute('type')) === 'password' &&
      (await fp.getByLabel('Password', { exact: true }).count()) === 1);
    ok('Founder Access has no username or email input', (await fp.locator(identityInputs).count()) === 0);
    await fp.fill('#password', FOUNDER_PW);
    await post(fp, 'button:has-text("Sign In")');
    ok('Founder password alone opens /founder/dashboard', fp.url() === B + '/founder/dashboard', fp.url());
    const d = await snap(fp, 'founder-dashboard');

    // an invalid password fails generically (separate browser, no session)
    const X = await b.newContext(); const xp = await X.newPage();
    await xp.goto(B + '/founder');
    await xp.fill('#password', 'not-the-founder-password');
    await post(xp, 'button:has-text("Sign In")');
    const failed = await xp.evaluate(() => document.body.innerText);
    ok('Founder invalid password fails generically', xp.url() === B + '/founder' && failed.includes('Sign in failed.') &&
      !/hash|locked|exist|not found|username|email/i.test(failed));
    ok('no username or email input after a failed attempt', (await xp.locator(identityInputs).count()) === 0 &&
      (await xp.locator('input:not([type=hidden])').count()) === 1);
    await X.close();

    ok('Founder dashboard: both companies, MRR $667.76', d.includes('Acme Operations') && d.includes('Beta Logistics') && d.includes('$667.76'));
    await post(fp, 'a:has-text("Acme Operations")');
    const c = await snap(fp, 'founder-company');
    ok('Founder company detail: users, subscription, Stripe IDs, audit', c.includes('eve@acme.test') && /cus_/.test(c) && /sub_/.test(c) && /Audit/i.test(c));
    await post(fp, 'button:has-text("Disable Company")');
    await snap(fp, 'founder-company-disabled');
    await A.page.goto(B + '/'); await A.page.waitForTimeout(3000);
    const blocked = await snap(A.page, 'owner-when-disabled');
    ok('disabled company: owner loses access', /unavailable|disabled|suspended/i.test(blocked) && !blocked.includes('Northwind'), A.page.url());
    await E.page.goto(B + '/'); await E.page.waitForTimeout(3000);
    ok('disabled company: employee loses access', !(await E.page.evaluate(() => document.body.innerText)).includes('Northwind'));
    ok('account disabled email sent', !!(await waitMail('owner@acme.test', /unavailable|disabled|suspend/i)));
    await post(fp, 'button:has-text("Enable Company")');
    await A.page.goto(B + '/compose/'); await A.page.waitForURL(/\/compose\/ns\/.+\/pages/, { timeout: 30000 }).catch(() => {});
    await A.page.waitForTimeout(4000);
    const back = await snap(A.page, 'owner-after-enable');
    ok('re-enabled company: access returns', back.includes('Dashboard') && back.includes('Customers'), A.page.url());
  }

  // ---------- payment failure ----------
  const subA = sql(`select stripe_subscription_id from saas_companies where name='Acme Operations'`);
  {
    await fetch(`${MOCK}/__fail?sub=${subA}`, { method: 'POST' });
    await sleep(1000);
    await A.page.goto(B + '/billing');
    const t = await snap(A.page, 'billing-payment-failed');
    ok('payment failure: past due + Payment Problem shown', /past due/i.test(t) && /Fix Payment|payment/i.test(t));
    ok('payment failed email sent', !!(await waitMail('owner@acme.test', /payment.*(failed|problem|unsuccessful)/i)));
    await fetch(`${MOCK}/__recover?sub=${subA}`, { method: 'POST' });
    await sleep(1000);
    await A.page.reload();
    ok('payment recovered: active again', /Active/.test(await A.page.evaluate(() => document.body.innerText)) && sql(`select subscription_status from saas_companies where name='Acme Operations'`) === 'active');
  }

  // ---------- cancel / resume / cancellation ----------
  {
    const page = A.page;
    await page.goto(B + '/billing');
    await post(page, 'button:has-text("Cancel Subscription")');
    await sleep(1000); await page.goto(B + '/billing');
    const t = await snap(page, 'billing-cancel-scheduled');
    ok('cancel at period end shown with access date', /set to end on/i.test(t) && t.includes('Resume Subscription'));
    await post(page, 'button:has-text("Resume Subscription")');
    await sleep(1000); await page.goto(B + '/billing');
    ok('resume subscription', (await page.content()).includes('Cancel Subscription'));
    ok('subscription resumed email sent', !!(await waitMail('owner@acme.test', /will continue|resumed/i)));
    await post(page, 'button:has-text("Cancel Subscription")');
    await fetch(`${MOCK}/__delete?sub=${subA}`, { method: 'POST' });
    await sleep(1000);
    await page.goto(B + '/'); await page.waitForTimeout(3000);
    const c = await snap(page, 'owner-after-cancellation');
    ok('canceled: workspace blocked, billing-only access', /\/billing/.test(page.url()) || /reactivate|subscription/i.test(c), page.url());
    ok('subscription canceled email sent', !!(await waitMail('owner@acme.test', /cancel/i)));
    const nsA = sql(`select namespace_id from saas_companies where name='Acme Operations'`);
    const recs = sql(`select count(*) from compose_record r join compose_module m on m.id=r.rel_module where m.rel_namespace=${nsA} and r.deleted_at is null`);
    const members = sql(`select count(*) from saas_company_members m join saas_companies c on c.id=m.company_id where c.name='Acme Operations'`);
    ok('canceled: company data retained', Number(recs) >= 3 && Number(members) === 2, `records=${recs} members=${members}`);
    await fp.goto(B + '/founder/dashboard');
    const d = await snap(fp, 'founder-after-cancel');
    ok('Founder sees cancellation and updated MRR $333.88', d.includes('$333.88') && /cancel/i.test(d));
    const r = await fetch(B + '/api/system/users/', { headers: { Authorization: 'Bearer invalid' } });
    ok('API rejects invalid token', r.status === 401 || r.status === 403, String(r.status));
  }

  // ---------- responsive smoke ----------
  for (const [vp, tag] of [[{ width: 390, height: 844 }, 'mobile'], [{ width: 820, height: 1180 }, 'tablet']]) {
    const ctx = await b.newContext({ viewport: vp }); const page = await ctx.newPage();
    for (const p of ['/signup', '/legal/terms', '/founder']) {
      await page.goto(B + p);
      const h = await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth + 1);
      ok(`${tag} ${p} no horizontal scroll`, !h);
    }
    await snap(page, `${tag}-founder-login`);
    await ctx.close();
  }

  const subjects = fs.readdirSync(MAIL).map(f => decodeSubj((fs.readFileSync(`${MAIL}/${f}`, 'utf8').match(/^Subject: (.*)$/mi) || [])[1] || ''));
  console.log('\nEmails delivered:\n  ' + [...new Set(subjects)].join('\n  '));
  await b.close();
  const failed = results.filter(r => !r.pass);
  console.log(`\n${results.length - failed.length}/${results.length} acceptance checks passed`);
  process.exit(failed.length ? 1 : 0);
})().catch(e => { console.error(e); process.exit(2); });
