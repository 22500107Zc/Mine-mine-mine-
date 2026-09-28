<picture>
  <source media="(prefers-color-scheme: dark)" srcset="server/saas/assets/static/stcloud-logo-light.svg">
  <img src="server/saas/assets/static/stcloud-logo.svg" alt="St.Cloud~OS" height="48">
</picture>

# St.Cloud\~OS

**Business Execution Intelligence OS** — by Culp Industries

**Live:** [https://st-cloud-os.vercel.app](https://st-cloud-os.vercel.app)

St.Cloud\~OS is one operating system for running a company's internal operations:
customers, contacts, records, cases, tasks, approvals, documents, reports,
departments and team management — in a single, isolated company workspace.

- **Plan:** St.Cloud\~OS — **$333.88 USD / month** per company (one subscription = one company account)
- No free plan, no trial, no public demo account
- Payments: Stripe (hosted Checkout and Customer Portal, signature-verified webhooks)
- Database: PostgreSQL

---

## What happens automatically

Operators do not provision companies, roles, workspaces or subscriptions by
hand. St.Cloud\~OS does this itself:

1. A customer opens `/signup`, enters the company and owner details and clicks
   **Continue to Secure Checkout**. The server creates the Stripe Checkout
   Session and redirects the browser to Stripe.
2. After payment Stripe calls `POST /stripe/webhook`. St.Cloud\~OS verifies the
   signature, reads the subscription from the Stripe API, and provisions the
   company exactly once: company roles (Owner, Administrator, Manager, Employee),
   an isolated workspace with Dashboard, Customers, Contacts, Records, Cases,
   Tasks, Workflows/Approvals, Documents, Reports, Team, Departments, Settings,
   Billing and Company Admin, access rules, and the owner account. A welcome
   email is sent.
3. The owner signs in and is guided through a short setup (company profile,
   invite team, first customer, first task). Invited team members are included
   in the company subscription.
4. Every change to a workspace record is measured (status, owner, department,
   team, type, priority, customer, linked case). Owners, administrators and
   managers get the **Command Deck** at `/command` — an execution
   intelligence layer built only from the company's own recorded history.
   A persistent scope bar (date presets or a custom range, process, status,
   type, priority, department, team, assignee) applies to every view, and
   every number drills down to the exact records behind it:
   - **Command Deck** – management summary, 21 KPI cards (WIP, throughput,
     cycle time avg/median/P90/P95, SLA compliance and breaches, blocked,
     rework, handoff delay, aging, overdue, completion and failure rates…)
     with the previous comparable period and a trend line, where work is
     stuck, what changed, activity history and recommendations
   - **Activity Graph** – year → month → week → day → event, with filters
   - **Pipeline & Bottlenecks** – per-stage WIP, entries/exits, time-in-stage
     percentiles, SLA, aging, blocked, rework, fallout, flow rates, handoff
     delay, owners, trend and a component-based severity score; plus the
     **Bottleneck Map**, stage detail, **SLA** intelligence (per workflow and
     per stage targets, breached records), **Aging** (buckets, at-risk work
     compared with completed history) and **Throughput**
   - **Outcomes & Effects** – monthly management report versus the previous
     month, the 3-month average and 12 months of history
   - **Process Review** – execution paths, fastest/slowest/rework paths,
     stalled and skipped work, variance, distribution; **Handoffs** (owner and
     stage) and **Rework** (loops and the time they cost)
   - **Organization Frame** / **Capacity** – departments, teams, people
     (workload, not performance ratings) with detail pages
   - **Goal Intelligence** – goals with baseline, trend, projection,
     explained confidence and observed drivers; SLA targets
   - **Intervention Tests** – before/after windows, sample sizes, evidence
     strength and notes; results are associations, never claimed causes
   - **Recommendations** and **What Changed** (with the drivers of cycle-time
     change and period comparisons)
   - **Record intelligence** – any record's path, timeline (including SLA
     breach moments), handoffs, loops and related records
   - **Definitions** (`/command/definitions`) and a data-coverage line on
     every view: how each number is calculated and how complete the history is
   - **Org & Access** and **Report an Issue** (the Founder works reports
     through open → in review → resolved → reopened at `/founder/issues`)

   New companies see explicit "not enough history yet" states — nothing is
   estimated or invented. Synthetic history exists only in tests and in the
   `server/saas/fixture` generator, whose development command refuses to run
   unless `CULPOS_FIXTURE_CONFIRM=test-environment` is set and marks every
   event `source = 'fixture'`.
5. Subscription changes (payment failures, recovery, cancellation at period
   end, resumption, cancellation) arrive through webhooks and are enforced on
   the server for every request. Customer data is never deleted because of
   billing state.

| Customer surface | Path |
| --- | --- |
| Sign in / forgot password / accept invitation | `/auth/login` |
| Paid signup | `/signup` |
| Workspace | `/` |
| Command Deck (execution intelligence, drilldowns, goals, interventions) | `/command` |
| Company Admin (profile, users, roles, invitations) | `/company` |
| Billing (status, next billing date, manage billing, cancel/resume) | `/billing` |
| Support | `/support` |
| Legal (terms, privacy, acceptable use, billing, cancellation, refunds, open source) | `/legal` |

| Platform surface | Path |
| --- | --- |
| Founder sign-in | `/founder` |
| Founder dashboard (companies, MRR, payments, activity, system health) | `/founder/dashboard` |
| Health check (JSON, no secrets) | `/health` |
| Stripe webhook | `POST /stripe/webhook` |

## External setup (the only manual steps)

### 1. PostgreSQL

Create a database and user, and set `DATABASE_URL`:

```
DATABASE_URL=postgres://culpos:<password>@<host>:5432/culpos?sslmode=require
```

The schema is created and migrated automatically on startup.

### 2. Environment

Copy `.env.example` to `.env` (or put the same keys in your platform's secret
manager) and fill in:

| Variable | Purpose |
| --- | --- |
| `APP_URL` | Public https URL of St.Cloud\~OS (e.g. `https://app.example.com`) |
| `DATABASE_URL` | PostgreSQL connection string |
| `JWT_SECRET`, `CSRF_SECRET`, `SESSION_SECRET` | Random secrets, `openssl rand -hex 32` each |
| `FOUNDER_BOOTSTRAP_PASSWORD` | Founder password, set on first start (the only Founder credential) |
| `STRIPE_SECRET_KEY`, `STRIPE_PUBLISHABLE_KEY`, `STRIPE_WEBHOOK_SECRET`, `STRIPE_PRICE_ID` | Billing |
| `SMTP_HOST`, `SMTP_PORT`, `SMTP_USERNAME`, `SMTP_PASSWORD`, `MAIL_FROM`, `MAIL_FROM_NAME` | Email |
| `SUPPORT_EMAIL` | Shown on Support, legal pages and emails |
| `LEGAL_GOVERNING_LAW` (optional) | Governing law clause, e.g. `the State of Texas, United States` |

St.Cloud\~OS validates this configuration at startup and prints a clear message for
anything missing (values are never printed). Missing database, `APP_URL` or
secrets stop startup; missing Stripe or SMTP settings are reported and shown to
the Founder until provided.

### 3. Stripe

In your Stripe account:

1. Create the product **St.Cloud\~OS** with a recurring price of **$333.88 USD per
   month** and put its price ID in `STRIPE_PRICE_ID`.
2. Add a webhook endpoint `https://<APP_URL>/stripe/webhook` for these events:
   `checkout.session.completed`, `checkout.session.async_payment_succeeded`,
   `checkout.session.async_payment_failed`, `customer.subscription.created`,
   `customer.subscription.updated`, `customer.subscription.deleted`,
   `customer.subscription.paused`, `customer.subscription.resumed`,
   `invoice.paid`, `invoice.payment_failed`, `invoice.payment_action_required`,
   `payment_method.attached`, `customer.updated`.
   Put the endpoint's signing secret in `STRIPE_WEBHOOK_SECRET`.
3. Enable the Customer Portal (payment method updates, invoices).

Nothing else is configured in Stripe or in St.Cloud\~OS: checkout sessions, portal
sessions and subscription state are handled by the server.

### 4. Email

Provide SMTP credentials (`SMTP_*`, `MAIL_FROM`). `MAIL_FROM_NAME` defaults to
`St.Cloud~OS`. All links in emails are built from `APP_URL`.

### 5. Domain and HTTPS

Point your domain at the host and terminate TLS in front of St.Cloud\~OS (reverse
proxy or load balancer). With an `https://` `APP_URL`, cookies are marked
Secure and HSTS is sent automatically.

## Deploy

```bash
cp .env.example .env          # fill in the values above
docker compose up -d --build  # PostgreSQL + St.Cloud~OS on port 8080
```

The `Dockerfile` builds the web applications and the server from source into a
non-root image. The container reports health at `GET /health`.

Builds behind a TLS-intercepting proxy can pass that proxy's CA without baking
it into the image:

```bash
docker build --secret id=build_ca,src=/path/to/proxy-ca.crt -t culpos .
```

### Vercel

St.Cloud\~OS also runs as a single Vercel container (Vercel Functions with a
custom image). `Dockerfile.vercel` is the same build as the `Dockerfile`; Vercel
detects it and routes all traffic — web applications, `/auth`, `/founder`,
`/command`, `/billing`, `/company`, `/support`, `/health`, `/stripe/webhook` and
static assets — to the container.

The container is stateless: PostgreSQL is external (`DATABASE_URL`, use a
direct, non-pooled connection string) and nothing is kept on the container's
disk. In the Vercel project settings set `PORT=8080`, the variables from
[Environment](#2-environment) (secrets as *Sensitive*), and `APP_URL` /
`PUBLIC_APP_URL` to the production domain. Point the Stripe webhook at
`<APP_URL>/stripe/webhook`. Keep Vercel Authentication limited to preview
deployments so customers and Stripe can reach production.

### Founder access

There is exactly one Founder, and the Founder signs in with a password only —
there is no Founder username or email. Put the initial password in your secret
store as `FOUNDER_BOOTSTRAP_PASSWORD`; on first start St.Cloud\~OS stores only its
bcrypt hash. Sign in at `/founder` and change the password at
`/founder/account`.

To recover a lost Founder password, set a new `FOUNDER_BOOTSTRAP_PASSWORD`
together with `FOUNDER_BOOTSTRAP_FORCE_RESET=true` for one restart, then remove
the flag.

## Backup and restore

PostgreSQL holds all company data, billing state and audit history; the `/data`
volume holds uploaded files.

```bash
# backup
pg_dump --format=custom --no-owner "$DATABASE_URL" > culpos-$(date +%F).dump
docker run --rm -v culpos_culpos-data:/data -v "$PWD":/backup debian:bookworm-slim \
  tar czf /backup/culpos-files-$(date +%F).tgz -C /data .

# restore
createdb culpos_restore
pg_restore --no-owner --dbname "postgres://…/culpos_restore" culpos-YYYY-MM-DD.dump
docker run --rm -v culpos_culpos-data:/data -v "$PWD":/backup debian:bookworm-slim \
  tar xzf /backup/culpos-files-YYYY-MM-DD.tgz -C /data
```

Stripe remains the source of truth for billing; after a restore use
**Founder → Company → Refresh from Stripe** for affected companies.

## Development

Requirements: Go 1.24+, Node 22 + Yarn 1, PostgreSQL 14+.

```bash
# front-end libraries and apps
cd lib/js  && yarn && yarn link && yarn build
cd ../vue  && yarn && yarn link @cortezaproject/corteza-js && yarn build && yarn link
cd ../../client/web/one && yarn && yarn cdeps && yarn build   # likewise compose, admin, ...

# embed language files and run the server
cd ../../../server/pkg/locale && make src/en
cd ../.. && go run -mod=vendor ./cmd/corteza serve-api
```

`server/saas/testdata/stripe_mock.py` is a local Stripe API stand-in that emits
correctly signed webhooks, and `smtp_catcher.py` stores outgoing email as
files; both are for development and end-to-end testing only.

## Testing

```bash
cd server
CULPOS_TEST_DATABASE_URL=postgres://culpos:culpos@localhost:5432/culpos_test?sslmode=disable \
  go test -mod=vendor ./saas/...     # billing, webhooks, Founder, isolation, onboarding, legal pages
go test -mod=vendor ./...
```

End-to-end acceptance in a real browser (signup → checkout → webhook
activation → onboarding → team invitation → tenant isolation → Founder
disable/enable → payment failure → cancellation), against a running instance
configured with the Stripe test double and SMTP catcher:

```bash
node server/saas/testdata/acceptance.js   # see the header for required variables
```

## Open Source Notices

St.Cloud\~OS includes open source software, including software derived from the
Corteza project under the Apache License, Version 2.0. See [LICENSE](LICENSE),
[NOTICE](NOTICE) and `/legal/open-source`. St.Cloud\~OS branding and original St.Cloud\~OS
functionality are © Culp Industries.
