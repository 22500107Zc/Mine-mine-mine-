package saas

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/cortezaproject/corteza/server/pkg/auth"
	"github.com/go-chi/chi/v5"
)

func TestSignupRequiresPaymentThenProvisions(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	url, c, err := env.svc.Signup(ctx, SignupInput{CompanyName: "Acme", FirstName: "Ada", LastName: "Owner", Email: "owner@acme.test", Password: "Str0ngPassw0rd!"})
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(url, "https://checkout.stripe.test/") {
		t.Fatalf("expected Stripe Checkout URL, got %s", url)
	}

	sess := env.stripe.sessions[0]
	if sess.PriceID != "price_culpos" || !strings.HasPrefix(sess.SuccessURL, "https://app.culpos.example/signup/complete") {
		t.Fatalf("unexpected checkout params: %+v", sess)
	}

	// before payment: owner suspended, no access
	if u := env.platform.userByEmail("owner@acme.test"); u == nil || !u.Suspended {
		t.Fatal("owner must be suspended until payment is verified")
	}

	_, _, d, _ := env.svc.AccessForUser(ctx, c.OwnerUserID)
	if d.Level == AccessFull {
		t.Fatal("unpaid company must not have access")
	}

	if env.svc.AuthGuard(ctx, c.OwnerUserID) != "/billing" {
		t.Fatal("unpaid owner must be routed to billing")
	}

	// the success redirect alone never activates anything
	ref := regexp.MustCompile(`ref=([^&]+)`).FindStringSubmatch(sess.SuccessURL)[1]
	rec := httptest.NewRecorder()
	env.svc.signupComplete(rec, httptest.NewRequest("GET", "/signup/complete?ref="+ref, nil))
	if !strings.Contains(rec.Body.String(), "Confirming your payment") {
		t.Fatal("success page must wait for server-side confirmation")
	}

	c2 := env.paidCompany(t, "Beta", "owner@beta.test")
	if c2.ProvisioningStatus != ProvProvisioned || c2.Status != CompanyActive || c2.SubscriptionStatus != SubActive || c2.NamespaceID == 0 {
		t.Fatalf("company not provisioned: %+v", c2)
	}

	if c2.StripeSubscriptionID == "" || c2.StripeCustomerID == "" || c2.BillingPeriodEnd == nil {
		t.Fatal("stripe identifiers not persisted")
	}

	if u := env.platform.userByEmail("owner@beta.test"); u.Suspended {
		t.Fatal("owner not activated")
	}

	if env.mail.count("Welcome to CulpOS") != 1 {
		t.Fatal("welcome email not sent")
	}
}

func TestDuplicateWebhookDoesNotReprovision(t *testing.T) {
	env := newTestEnv(t)
	c := env.paidCompany(t, "Acme", "owner@acme.test")

	obj := map[string]any{"id": "cs_1", "mode": "subscription", "status": "complete", "payment_status": "paid",
		"client_reference_id": fmt.Sprint(c.ID), "customer": c.StripeCustomerID, "subscription": c.StripeSubscriptionID}

	// same event delivered again
	if err := env.webhook(t, "evt_cs_"+fmt.Sprint(c.ID), "checkout.session.completed", obj); err != nil {
		t.Fatal(err)
	}

	// a different event for the same checkout
	if err := env.webhook(t, "evt_other", "checkout.session.completed", obj); err != nil {
		t.Fatal(err)
	}

	if env.platform.provisions != 1 {
		t.Fatalf("company provisioned %d times", env.platform.provisions)
	}

	var n int
	_ = env.db.QueryRow(`SELECT COUNT(*) FROM saas_companies`).Scan(&n)
	if n != 1 {
		t.Fatalf("duplicate companies: %d", n)
	}
}

func TestWebhookRejectsInvalidSignature(t *testing.T) {
	env := newTestEnv(t)
	body := []byte(`{"id":"evt_forged","type":"checkout.session.completed","data":{"object":{}}}`)

	for _, hdr := range []string{"", "t=1,v1=00", SignWebhookPayload(body, "whsec_wrong", time.Now()), SignWebhookPayload(body, testWebhookSecret, time.Now().Add(-time.Hour))} {
		if err := env.svc.HandleWebhook(context.Background(), body, hdr); !errors.Is(err, ErrInvalidSignature) {
			t.Fatalf("header %q: expected invalid signature, got %v", hdr, err)
		}
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/stripe/webhook", strings.NewReader(string(body)))
	req.Header.Set("Stripe-Signature", "t=1,v1=00")
	env.svc.webhookHandler(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}

	var n int
	_ = env.db.QueryRow(`SELECT COUNT(*) FROM saas_stripe_events`).Scan(&n)
	if n != 0 {
		t.Fatal("forged event must not be recorded")
	}
}

func TestSubscriptionLifecycle(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	c := env.paidCompany(t, "Acme", "owner@acme.test")

	// payment failure → past_due, warning, data kept, email sent
	env.stripe.putSub(c.StripeSubscriptionID, c.StripeCustomerID, "past_due", "price_culpos", time.Now().Add(24*time.Hour))
	if err := env.webhook(t, "evt_fail", "invoice.payment_failed", map[string]any{"id": "in_1", "customer": c.StripeCustomerID, "subscription": c.StripeSubscriptionID, "amount_due": 33388, "currency": "usd"}); err != nil {
		t.Fatal(err)
	}

	env.svc.cache = newAccessCache(0)
	_, _, d, _ := env.svc.AccessForUser(ctx, c.OwnerUserID)
	if d.Level != AccessFull || d.Warning == "" {
		t.Fatalf("past due should warn but keep access: %+v", d)
	}

	if env.mail.count("payment failed") != 1 {
		t.Fatal("payment failure email not sent")
	}

	failed, _ := env.svc.repo.Payments(ctx, c.ID, "failed", 10)
	if len(failed) != 1 || failed[0].AmountCents != 33388 {
		t.Fatal("failed payment not recorded")
	}

	// unpaid → billing only
	env.stripe.putSub(c.StripeSubscriptionID, c.StripeCustomerID, "unpaid", "price_culpos", time.Now().Add(24*time.Hour))
	_ = env.webhook(t, "evt_unpaid", "customer.subscription.updated", env.stripe.subs[c.StripeSubscriptionID])
	_, _, d, _ = env.svc.AccessForUser(ctx, c.OwnerUserID)
	if d.Level != AccessBillingOnly {
		t.Fatal("unpaid must restrict to billing")
	}

	// recovered
	env.stripe.putSub(c.StripeSubscriptionID, c.StripeCustomerID, "active", "price_culpos", time.Now().Add(30*24*time.Hour))
	_ = env.webhook(t, "evt_paid", "invoice.paid", map[string]any{"id": "in_2", "customer": c.StripeCustomerID, "subscription": c.StripeSubscriptionID, "amount_paid": 33388, "currency": "usd"})
	_, _, d, _ = env.svc.AccessForUser(ctx, c.OwnerUserID)
	if d.Level != AccessFull {
		t.Fatal("recovered subscription must restore access")
	}

	// cancel at period end keeps access
	c, _ = env.svc.repo.CompanyByID(ctx, c.ID)
	if err := env.svc.setCancelAtPeriodEnd(ctx, c, true); err != nil {
		t.Fatal(err)
	}
	c, _ = env.svc.repo.CompanyByID(ctx, c.ID)
	_, _, d, _ = env.svc.AccessForUser(ctx, c.OwnerUserID)
	if !c.CancelAtPeriodEnd || d.Level != AccessFull {
		t.Fatal("cancel at period end should keep access until the end of the period")
	}

	// subscription deleted after period end → billing only, data kept
	env.stripe.putSub(c.StripeSubscriptionID, c.StripeCustomerID, "canceled", "price_culpos", time.Now().Add(-time.Minute))
	sub := env.stripe.subs[c.StripeSubscriptionID]
	if err := env.webhook(t, "evt_del", "customer.subscription.deleted", sub); err != nil {
		t.Fatal(err)
	}
	c, _ = env.svc.repo.CompanyByID(ctx, c.ID)
	_, _, d, _ = env.svc.AccessForUser(ctx, c.OwnerUserID)
	if c.SubscriptionStatus != SubCanceled || d.Level != AccessBillingOnly {
		t.Fatalf("canceled subscription must lock operational access: %s %v", c.SubscriptionStatus, d)
	}

	if m, _ := env.svc.repo.Members(ctx, c.ID); len(m) != 1 {
		t.Fatal("company data must be retained after cancellation")
	}

	if env.mail.count("subscription has ended") != 1 {
		t.Fatal("cancellation email not sent")
	}
}

func TestWebhookIgnoresForeignPrice(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	_, c, _ := env.svc.Signup(ctx, SignupInput{CompanyName: "Cheap", FirstName: "A", LastName: "B", Email: "a@cheap.test", Password: "Str0ngPassw0rd!"})
	c, _ = env.svc.repo.CompanyByID(ctx, c.ID)
	env.stripe.putSub("sub_cheap", c.StripeCustomerID, "active", "price_other", time.Now().Add(time.Hour))
	_ = env.webhook(t, "evt_cheap", "checkout.session.completed", map[string]any{"mode": "subscription", "status": "complete", "payment_status": "paid",
		"client_reference_id": fmt.Sprint(c.ID), "customer": c.StripeCustomerID, "subscription": "sub_cheap"})
	c, _ = env.svc.repo.CompanyByID(ctx, c.ID)
	if c.ProvisioningStatus == ProvProvisioned {
		t.Fatal("subscription to a different price must not provision a company")
	}
}

func TestFounderBootstrapAndLogin(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	if err := env.svc.BootstrapFounder(ctx); err != nil {
		t.Fatal(err)
	}

	var hash string
	_ = env.db.QueryRow(`SELECT password_hash FROM saas_founders WHERE username = 'founder'`).Scan(&hash)
	if hash == "" || strings.Contains(hash, "test-founder-passphrase-1") || !strings.HasPrefix(hash, "$2a$") {
		t.Fatal("founder password must be stored as a bcrypt hash only")
	}

	// bootstrapping again must not reset the password
	if err := env.svc.BootstrapFounder(ctx); err != nil {
		t.Fatal(err)
	}

	var n int
	_ = env.db.QueryRow(`SELECT COUNT(*) FROM saas_founders`).Scan(&n)
	if n != 1 {
		t.Fatal("duplicate founders")
	}

	tok, ses, err := env.svc.FounderLogin(ctx, "founder", "test-founder-passphrase-1", "10.0.0.1", "test")
	if err != nil || tok == "" || ses.CSRFToken == "" {
		t.Fatalf("founder login failed: %v", err)
	}

	// token stored only as hash
	var stored string
	_ = env.db.QueryRow(`SELECT token_hash FROM saas_founder_sessions LIMIT 1`).Scan(&stored)
	if stored == tok || stored != hashToken(tok) {
		t.Fatal("session token must be stored hashed")
	}

	if _, _, err = env.svc.FounderSession(ctx, tok); err != nil {
		t.Fatal("valid session rejected")
	}

	// wrong password and unknown user yield the same generic error
	_, _, e1 := env.svc.FounderLogin(ctx, "founder", "wrong", "10.0.0.1", "test")
	_, _, e2 := env.svc.FounderLogin(ctx, "nobody", "wrong", "10.0.0.1", "test")
	if !errors.Is(e1, ErrInvalidCredentials) || !errors.Is(e2, ErrInvalidCredentials) || e1.Error() != e2.Error() {
		t.Fatal("failures must be generic")
	}

	// logout invalidates server-side
	env.svc.FounderLogout(ctx, tok, "10.0.0.1")
	if _, _, err = env.svc.FounderSession(ctx, tok); err == nil {
		t.Fatal("session valid after logout")
	}

	// lockout after repeated failures, even with the right password afterwards
	for i := 0; i < 5; i++ {
		_, _, _ = env.svc.FounderLogin(ctx, "founder", "bad", "10.0.0.1", "test")
	}
	if _, _, err = env.svc.FounderLogin(ctx, "founder", "test-founder-passphrase-1", "10.0.0.1", "test"); err == nil {
		t.Fatal("locked founder must not be able to sign in")
	}

	// audit events recorded, without secrets
	var logins int
	_ = env.db.QueryRow(`SELECT COUNT(*) FROM saas_audit_log WHERE action = 'founder.login'`).Scan(&logins)
	if logins < 7 {
		t.Fatalf("founder logins not audited: %d", logins)
	}

	var leaked int
	_ = env.db.QueryRow(`SELECT COUNT(*) FROM saas_audit_log WHERE metadata::text LIKE '%test-founder-passphrase-1%' OR target LIKE '%test-founder-passphrase-1%'`).Scan(&leaked)
	if leaked != 0 {
		t.Fatal("password leaked into the audit log")
	}
}

func TestFounderSessionExpiry(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	_ = env.svc.BootstrapFounder(ctx)
	tok, _, err := env.svc.FounderLogin(ctx, "founder", "test-founder-passphrase-1", "", "")
	if err != nil {
		t.Fatal(err)
	}

	real := env.svc.now
	env.svc.now = func() time.Time { return real().Add(31 * time.Minute) }
	if _, _, err = env.svc.FounderSession(ctx, tok); err == nil {
		t.Fatal("idle session must expire")
	}
}

// founderClient performs the founder HTTP flow through the real router
func founderServer(t *testing.T, env *testEnv) (*httptest.Server, *http.Client) {
	r := chi.NewRouter()
	env.svc.MountRoutes(r)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	jar, _ := cookiejar.New(nil)
	cl := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return srv, cl
}

var csrfRE = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

func getCSRF(t *testing.T, cl *http.Client, u string) string {
	rsp, err := cl.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rsp.Body)
	rsp.Body.Close()
	m := csrfRE.FindSubmatch(b)
	if m == nil {
		t.Fatalf("no csrf token on %s", u)
	}
	return string(m[1])
}

func TestFounderHTTPFlow(t *testing.T) {
	env := newTestEnv(t)
	env.svc.cfg.SecureCookies = false // httptest serves plain http
	_ = env.svc.BootstrapFounder(context.Background())
	c := env.paidCompany(t, "Acme", "owner@acme.test")
	srv, cl := founderServer(t, env)

	// unauthenticated → login page; dashboard protected
	rsp, _ := cl.Get(srv.URL + "/founder/dashboard")
	if rsp.StatusCode != http.StatusSeeOther || rsp.Header.Get("Location") != "/founder" {
		t.Fatalf("dashboard must require founder session, got %d", rsp.StatusCode)
	}

	// login page branding
	rsp, _ = cl.Get(srv.URL + "/founder")
	b, _ := io.ReadAll(rsp.Body)
	for _, want := range []string{"<title>CulpOS | Founder</title>", "Username", "Password", "Sign In"} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("founder login page missing %q", want)
		}
	}

	// POST without CSRF is rejected
	rsp, _ = cl.PostForm(srv.URL+"/founder", url.Values{"username": {"founder"}, "password": {"test-founder-passphrase-1"}})
	if rsp.StatusCode != http.StatusForbidden {
		t.Fatalf("login without CSRF must fail, got %d", rsp.StatusCode)
	}

	// wrong password → generic message, 401
	tok := getCSRF(t, cl, srv.URL+"/founder")
	rsp, _ = cl.PostForm(srv.URL+"/founder", url.Values{"csrf": {tok}, "username": {"founder"}, "password": {"nope"}})
	b, _ = io.ReadAll(rsp.Body)
	if rsp.StatusCode != http.StatusUnauthorized || !strings.Contains(string(b), "Invalid username or password.") {
		t.Fatal("expected generic login failure")
	}

	// success → cookie flags + redirect
	rsp, _ = cl.PostForm(srv.URL+"/founder", url.Values{"csrf": {tok}, "username": {"founder"}, "password": {"test-founder-passphrase-1"}})
	if rsp.StatusCode != http.StatusSeeOther || rsp.Header.Get("Location") != "/founder/dashboard" {
		t.Fatalf("login failed: %d", rsp.StatusCode)
	}

	sc := rsp.Header.Get("Set-Cookie")
	if !strings.Contains(sc, "HttpOnly") || !strings.Contains(sc, "SameSite=Strict") || !strings.Contains(sc, "Path=/founder") {
		t.Fatalf("insecure founder cookie: %s", sc)
	}

	rsp, _ = cl.Get(srv.URL + "/founder/dashboard")
	b, _ = io.ReadAll(rsp.Body)
	body := string(b)
	for _, want := range []string{"Monthly Recurring Revenue", "$333.88", "Acme", c.StripeCustomerID, c.StripeSubscriptionID, "Stripe Webhook", "Database"} {
		if !strings.Contains(body, want) {
			t.Fatalf("dashboard missing %q", want)
		}
	}

	csrf := csrfRE.FindStringSubmatch(body)[1]

	// disable without CSRF → 403
	rsp, _ = cl.PostForm(fmt.Sprintf("%s/founder/companies/%d/disable", srv.URL, c.ID), nil)
	if rsp.StatusCode != http.StatusForbidden {
		t.Fatalf("CSRF not enforced: %d", rsp.StatusCode)
	}

	// disable / enable
	rsp, _ = cl.PostForm(fmt.Sprintf("%s/founder/companies/%d/disable", srv.URL, c.ID), url.Values{"csrf": {csrf}})
	env.svc.cache = newAccessCache(0)
	if _, _, d, _ := env.svc.AccessForUser(context.Background(), c.OwnerUserID); d.Level != AccessNone {
		t.Fatal("disabled company still has access")
	}

	if env.svc.AuthGuard(context.Background(), c.OwnerUserID) != "/account/disabled" {
		t.Fatal("disabled company must see the neutral disabled page")
	}

	rsp, _ = cl.PostForm(fmt.Sprintf("%s/founder/companies/%d/enable", srv.URL, c.ID), url.Values{"csrf": {csrf}})
	if _, _, d, _ := env.svc.AccessForUser(context.Background(), c.OwnerUserID); d.Level != AccessFull {
		t.Fatal("re-enabled company must have access")
	}

	for _, p := range []string{"companies", "users", "billing", "audit", "system", "account", fmt.Sprintf("companies/%d", c.ID)} {
		rsp, _ = cl.Get(srv.URL + "/founder/" + p)
		if rsp.StatusCode != http.StatusOK {
			t.Fatalf("/founder/%s: %d", p, rsp.StatusCode)
		}
	}

	// logout invalidates
	rsp, _ = cl.PostForm(srv.URL+"/founder/logout", url.Values{"csrf": {csrf}})
	rsp, _ = cl.Get(srv.URL + "/founder/dashboard")
	if rsp.StatusCode != http.StatusSeeOther {
		t.Fatal("dashboard accessible after logout")
	}

	var actions []string
	rows, _ := env.db.Query(`SELECT action FROM saas_audit_log WHERE actor_type = 'founder' ORDER BY id`)
	for rows.Next() {
		var a string
		_ = rows.Scan(&a)
		actions = append(actions, a)
	}
	joined := strings.Join(actions, ",")
	for _, want := range []string{"founder.login", "company.disable", "company.enable", "founder.logout", "founder.csrf"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("audit missing %s: %s", want, joined)
		}
	}
}

func TestNormalUserDeniedFounderRoutes(t *testing.T) {
	env := newTestEnv(t)
	c := env.paidCompany(t, "Acme", "owner@acme.test")
	env.user = c.OwnerUserID // signed in as company owner
	srv, cl := founderServer(t, env)

	for _, p := range []string{"/founder/dashboard", "/founder/companies", "/founder/users"} {
		rsp, _ := cl.Get(srv.URL + p)
		if rsp.StatusCode != http.StatusSeeOther || rsp.Header.Get("Location") != "/founder" {
			t.Fatalf("%s accessible to a company user: %d", p, rsp.StatusCode)
		}
	}

	// forged cookie
	req, _ := http.NewRequest("GET", srv.URL+"/founder/dashboard", nil)
	req.AddCookie(&http.Cookie{Name: founderCookie, Value: "forged"})
	rsp, _ := cl.Do(req)
	if rsp.StatusCode != http.StatusSeeOther {
		t.Fatal("forged founder cookie accepted")
	}
}

func TestCompanyMembersInvitationsAndRoles(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	a := env.paidCompany(t, "Acme", "owner@acme.test")
	b := env.paidCompany(t, "Beta", "owner@beta.test")

	// owner invites an administrator; invitation email is branded and company-specific
	if err := env.svc.Invite(ctx, a.OwnerUserID, "admin@acme.test", "Ann Admin", RoleAdministrator, ""); err != nil {
		t.Fatal(err)
	}
	if env.mail.count("invited to join Acme on CulpOS") != 1 || env.mail.count("Accept Invitation") != 1 {
		t.Fatal("invitation email missing")
	}

	admin := env.platform.userByEmail("admin@acme.test")

	// nobody can invite an owner or a founder
	if err := env.svc.Invite(ctx, a.OwnerUserID, "x@acme.test", "", RoleOwner, ""); err == nil {
		t.Fatal("owner role must not be assignable")
	}

	// administrator can invite employee/manager but not administrator
	if err := env.svc.Invite(ctx, admin.ID, "emp@acme.test", "", RoleEmployee, ""); err != nil {
		t.Fatal(err)
	}
	if err := env.svc.Invite(ctx, admin.ID, "adm2@acme.test", "", RoleAdministrator, ""); err == nil {
		t.Fatal("administrator must not create peers")
	}

	emp := env.platform.userByEmail("emp@acme.test")

	// employee cannot invite or change roles (escalation)
	if err := env.svc.Invite(ctx, emp.ID, "x2@acme.test", "", RoleEmployee, ""); err == nil {
		t.Fatal("employee must not invite")
	}
	if err := env.svc.ChangeRole(ctx, emp.ID, emp.ID, RoleAdministrator, ""); err == nil {
		t.Fatal("self-escalation must fail")
	}
	if err := env.svc.ChangeRole(ctx, admin.ID, admin.ID, RoleAdministrator, ""); err == nil {
		t.Fatal("self role change must fail")
	}
	if err := env.svc.ChangeRole(ctx, admin.ID, a.OwnerUserID, RoleEmployee, ""); err == nil {
		t.Fatal("administrator must not demote the owner")
	}

	// cross-company management is indistinguishable from not allowed
	if err := env.svc.RemoveMember(ctx, a.OwnerUserID, b.OwnerUserID, ""); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("cross-company removal: %v", err)
	}
	if err := env.svc.ChangeRole(ctx, a.OwnerUserID, b.OwnerUserID, RoleEmployee, ""); !errors.Is(err, ErrNotAllowed) {
		t.Fatal("cross-company role change must fail")
	}

	// owner promotes employee to manager, then removes them
	if err := env.svc.ChangeRole(ctx, a.OwnerUserID, emp.ID, RoleManager, ""); err != nil {
		t.Fatal(err)
	}
	if err := env.svc.RemoveMember(ctx, a.OwnerUserID, emp.ID, ""); err != nil {
		t.Fatal(err)
	}
	if !env.platform.userByEmail("emp@acme.test").Suspended || env.platform.revoked[emp.ID] == 0 {
		t.Fatal("removed member must be suspended and signed out")
	}

	// owner cannot be removed
	if err := env.svc.RemoveMember(ctx, a.OwnerUserID, a.OwnerUserID, ""); err == nil {
		t.Fatal("owner must not be removable")
	}

	// invited users never pay individually: still exactly two checkout sessions (one per company)
	if len(env.stripe.sessions) != 2 {
		t.Fatalf("unexpected checkout sessions: %d", len(env.stripe.sessions))
	}
}

// gateServer wraps a fake API handler with the gate and a fake identity
func gateServer(env *testEnv, userID uint64) http.Handler {
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/compose/namespace/":
			_, _ = w.Write([]byte(`{"response":{"set":[{"namespaceID":"` + fmt.Sprint(idsOf(env)[0]) + `"},{"namespaceID":"` + fmt.Sprint(idsOf(env)[1]) + `"}]}}`))
		default:
			_, _ = w.Write([]byte(`{"response":{"ok":true}}`))
		}
	})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := auth.SetIdentityToContext(r.Context(), auth.Authenticated(userID))
		env.svc.APIGate("/api")(api).ServeHTTP(w, r.WithContext(ctx))
	})
}

var gateCompanies []*Company

func idsOf(*testEnv) []uint64 {
	return []uint64{gateCompanies[0].NamespaceID, gateCompanies[1].NamespaceID}
}

func TestCrossCompanyIsolationAtAPIGate(t *testing.T) {
	env := newTestEnv(t)
	a := env.paidCompany(t, "Acme", "owner@acme.test")
	b := env.paidCompany(t, "Beta", "owner@beta.test")
	gateCompanies = []*Company{a, b}

	h := gateServer(env, a.OwnerUserID)
	do := func(method, path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(`{}`)))
		return rec
	}

	// own workspace works
	if rec := do("GET", fmt.Sprintf("/api/compose/namespace/%d/module/1/record/", a.NamespaceID)); rec.Code != 200 {
		t.Fatalf("own workspace denied: %d", rec.Code)
	}

	// every attempt against company B fails
	for _, p := range []struct{ m, path string }{
		{"GET", fmt.Sprintf("/api/compose/namespace/%d/module/1/record/2", b.NamespaceID)},
		{"GET", fmt.Sprintf("/api/compose/namespace/%d/module/1/record/", b.NamespaceID)},
		{"POST", fmt.Sprintf("/api/compose/namespace/%d/module/1/record/2", b.NamespaceID)},
		{"DELETE", fmt.Sprintf("/api/compose/namespace/%d/module/1/record/2", b.NamespaceID)},
		{"GET", fmt.Sprintf("/api/compose/namespace/%d/module/1/record/export.csv", b.NamespaceID)},
		{"GET", fmt.Sprintf("/api/system/users/%d", b.OwnerUserID)},
		{"GET", fmt.Sprintf("/api/system/roles/%d", b.RoleOwnerID)},
		{"PATCH", fmt.Sprintf("/api/compose/permissions/%d/rules", b.RoleOwnerID)},
	} {
		if rec := do(p.m, p.path); rec.Code != http.StatusNotFound {
			t.Errorf("%s %s: expected 404, got %d (%s)", p.m, p.path, rec.Code, rec.Body.String())
		}
	}

	// namespace list only includes company A
	rec := do("GET", "/api/compose/namespace/")
	if strings.Contains(rec.Body.String(), fmt.Sprint(b.NamespaceID)) || !strings.Contains(rec.Body.String(), fmt.Sprint(a.NamespaceID)) {
		t.Fatalf("namespace list leaks: %s", rec.Body.String())
	}

	// disabled company is blocked entirely; unpaid restricted to billing
	_ = env.svc.repo.SetCompanyStatus(context.Background(), a.ID, CompanyDisabled)
	env.svc.cache = newAccessCache(0)
	if rec := do("GET", fmt.Sprintf("/api/compose/namespace/%d/module/1/record/", a.NamespaceID)); rec.Code != http.StatusForbidden {
		t.Fatalf("disabled company must be blocked: %d", rec.Code)
	} else if rec.Header().Get("X-CulpOS-Access") != "disabled" {
		t.Fatal("disabled response must tell the web app where to send the user")
	}

	_ = env.svc.repo.SetCompanyStatus(context.Background(), a.ID, CompanyActive)
	_ = env.svc.repo.SetSubscriptionStatus(context.Background(), a.ID, SubUnpaid)
	if rec := do("GET", fmt.Sprintf("/api/compose/namespace/%d/module/1/record/", a.NamespaceID)); rec.Code != http.StatusPaymentRequired {
		t.Fatalf("unpaid company must get 402: %d", rec.Code)
	} else if rec.Header().Get("X-CulpOS-Access") != "billing" {
		t.Fatal("unpaid response must send the web app to billing")
	}
	if rec := do("GET", "/api/system/auth/check"); rec.Code != 200 {
		t.Fatal("account recovery endpoints must stay reachable")
	}

	// platform staff (no company) unaffected
	staff := gateServer(env, 424242)
	rec = httptest.NewRecorder()
	staff.ServeHTTP(rec, httptest.NewRequest("GET", fmt.Sprintf("/api/compose/namespace/%d/module/1/record/", b.NamespaceID), nil))
	if rec.Code != 200 {
		t.Fatal("platform staff must not be gated")
	}
}

func TestBillingAndCompanyPages(t *testing.T) {
	env := newTestEnv(t)
	env.svc.cfg.SecureCookies = false
	c := env.paidCompany(t, "Acme", "owner@acme.test")
	srv, cl := founderServer(t, env)

	// anonymous → sign in
	rsp, _ := cl.Get(srv.URL + "/billing")
	if rsp.StatusCode != http.StatusSeeOther || rsp.Header.Get("Location") != "/auth/login" {
		t.Fatal("billing must require sign in")
	}

	env.user = c.OwnerUserID
	rsp, _ = cl.Get(srv.URL + "/billing")
	b, _ := io.ReadAll(rsp.Body)
	body := string(b)
	for _, want := range []string{"<title>CulpOS | Billing</title>", "$333.88", "/month", "Active", "Next billing date", "Manage Billing", "Cancel Subscription"} {
		if !strings.Contains(body, want) {
			t.Fatalf("billing page missing %q", want)
		}
	}

	csrf := csrfRE.FindStringSubmatch(body)[1]
	rsp, _ = cl.PostForm(srv.URL+"/billing/portal", url.Values{"csrf": {csrf}})
	if rsp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(rsp.Header.Get("Location"), "https://billing.stripe.test/") {
		t.Fatalf("billing portal redirect failed: %d %s", rsp.StatusCode, rsp.Header.Get("Location"))
	}

	rsp, _ = cl.PostForm(srv.URL+"/billing/cancel", url.Values{"csrf": {"wrong"}})
	if rsp.StatusCode != http.StatusForbidden {
		t.Fatal("billing actions must enforce CSRF")
	}

	rsp, _ = cl.PostForm(srv.URL+"/billing/cancel", url.Values{"csrf": {csrf}})
	cc, _ := env.svc.repo.CompanyByID(context.Background(), c.ID)
	if !cc.CancelAtPeriodEnd {
		t.Fatal("cancel did not apply")
	}

	rsp, _ = cl.Get(srv.URL + "/company")
	b, _ = io.ReadAll(rsp.Body)
	if !strings.Contains(string(b), "Company Admin") || !strings.Contains(string(b), "owner@acme.test") {
		t.Fatal("company admin page broken")
	}

	// employees cannot see billing or manage the subscription
	_ = env.svc.Invite(context.Background(), c.OwnerUserID, "emp@acme.test", "", RoleEmployee, "")
	env.user = env.platform.userByEmail("emp@acme.test").ID
	rsp, _ = cl.Get(srv.URL + "/billing")
	if rsp.StatusCode != http.StatusForbidden {
		t.Fatalf("employee billing access: %d", rsp.StatusCode)
	}
}
