package saas

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

func TestLegalAndSupportPages(t *testing.T) {
	env := newTestEnv(t)
	env.svc.cfg.SecureCookies = false
	srv, cl := founderServer(t, env)

	pages := map[string]string{
		"/legal":                "Legal",
		"/legal/terms":          "Terms of Service",
		"/legal/privacy":        "Privacy Policy",
		"/legal/acceptable-use": "Acceptable Use Policy",
		"/legal/billing":        "Subscription and Billing Policy",
		"/legal/cancellation":   "Cancellation Policy",
		"/legal/refunds":        "Refund Policy",
		"/legal/open-source":    "Open Source Notices",
		"/support":              "Support",
	}

	for path, title := range pages {
		rsp, err := cl.Get(srv.URL + path)
		if err != nil || rsp.StatusCode != 200 {
			t.Fatalf("%s: %v %d", path, err, rsp.StatusCode)
		}
		b, _ := io.ReadAll(rsp.Body)
		body := string(b)

		if !strings.Contains(body, "<title>CulpOS | "+title+"</title>") {
			t.Errorf("%s: missing branded title", path)
		}

		for _, bad := range []string{"TBD", "Lorem", "legal review", "counsel", "[INSERT", "coming soon", "placeholder paragraph", "To be completed"} {
			if strings.Contains(strings.ToLower(body), strings.ToLower(bad)) {
				t.Errorf("%s: contains placeholder text %q", path, bad)
			}
		}

		for _, link := range []string{"/legal/terms", "/legal/privacy", "/legal/acceptable-use", "/legal/billing", "/legal/cancellation", "/legal/refunds", "/legal/open-source", "/support"} {
			if !strings.Contains(body, `href="`+link+`"`) {
				t.Errorf("%s: footer missing %s", path, link)
			}
		}

		if path != "/legal/open-source" && strings.Contains(body, "Corteza") {
			t.Errorf("%s: upstream name on a customer page", path)
		}
	}

	rsp, _ := cl.Get(srv.URL + "/legal/terms")
	b, _ := io.ReadAll(rsp.Body)
	for _, want := range []string{"$333.88", "Culp Industries", "Stripe", "Limitation of Liability", "Governing Law", "Indemnification", "support@culpos.example"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("terms missing %q", want)
		}
	}

	rsp, _ = cl.Get(srv.URL + "/legal/privacy")
	b, _ = io.ReadAll(rsp.Body)
	if !strings.Contains(string(b), "not received or stored by CulpOS") {
		t.Error("privacy policy must state that card numbers are handled by Stripe")
	}

	rsp, _ = cl.Get(srv.URL + "/legal/open-source")
	b, _ = io.ReadAll(rsp.Body)
	if !strings.Contains(string(b), "Apache License") {
		t.Error("open source notices must keep the Apache-2.0 attribution")
	}
}

func TestSupportWithoutEmailHasNoPlaceholder(t *testing.T) {
	env := newTestEnv(t)
	env.svc.cfg.Brand.SupportEmail = ""
	t.Setenv("MAIL_FROM", "")
	env.svc.tpl, _ = parseTemplates(env.svc.cfg.Brand)
	rec := httptest.NewRecorder()
	env.svc.supportPage(rec, httptest.NewRequest("GET", "/support", nil))
	body := rec.Body.String()
	if strings.Contains(body, "mailto:\"") || strings.Contains(body, "mailto:?") || !strings.Contains(body, "Reply to any email") {
		t.Fatal("support page without SUPPORT_EMAIL must show a generic contact message")
	}
}

func TestSignupUnavailableWithoutStripe(t *testing.T) {
	env := newTestEnv(t)
	env.svc.cfg.StripePriceID = ""
	rec := httptest.NewRecorder()
	env.svc.signupForm(rec, httptest.NewRequest("GET", "/signup", nil))
	if !strings.Contains(rec.Body.String(), "temporarily unavailable") || !strings.Contains(rec.Body.String(), "disabled>") {
		t.Fatal("signup must be disabled with a friendly message when billing is not configured")
	}

	_, _, err := env.svc.Signup(context.Background(), SignupInput{CompanyName: "Acme", FirstName: "A", LastName: "B", Email: "a@b.test", Password: "Str0ngPassw0rd!"})
	if msg, ok := isUserError(err); !ok || !strings.Contains(msg, "unavailable") {
		t.Fatal("signup must be refused when billing is not configured")
	}
}

func TestOnboardingFlow(t *testing.T) {
	env := newTestEnv(t)
	env.svc.cfg.SecureCookies = false
	ctx := context.Background()
	c := env.paidCompany(t, "Acme", "owner@acme.test")

	if got := env.svc.AuthGuard(ctx, c.OwnerUserID); got != "/welcome" {
		t.Fatalf("new owner should be guided to onboarding, got %q", got)
	}

	srv, cl := founderServer(t, env)
	env.user = c.OwnerUserID

	rsp, _ := cl.Get(srv.URL + "/welcome")
	b, _ := io.ReadAll(rsp.Body)
	for _, step := range onboardingSteps {
		if !strings.Contains(string(b), step) {
			t.Fatalf("welcome page missing step %q", step)
		}
	}
	csrf := csrfRE.FindStringSubmatch(string(b))[1]

	post := func(action string, v url.Values) *http.Response {
		v.Set("csrf", csrf)
		r, _ := cl.PostForm(srv.URL+"/welcome/"+action, v)
		return r
	}

	post("profile", url.Values{"name": {"Acme Holdings"}, "industry": {"Logistics"}, "website": {"acme.example"}})
	cc, _ := env.svc.repo.CompanyByID(ctx, c.ID)
	if cc.Name != "Acme Holdings" || cc.Industry != "Logistics" || cc.Website != "https://acme.example" {
		t.Fatalf("profile not saved: %+v", cc)
	}

	post("invite", url.Values{"email": {"eve@acme.test"}, "name": {"Eve"}, "role": {"employee"}})
	if env.platform.userByEmail("eve@acme.test") == nil {
		t.Fatal("onboarding invite failed")
	}

	post("customer", url.Values{"name": {"First Customer"}, "email": {"buyer@first.test"}})
	post("task", url.Values{"title": {"Call First Customer"}, "dueDate": {"2026-10-01"}})
	recs := strings.Join(env.platform.records[c.ID], ",")
	if !strings.Contains(recs, "Customer:First Customer") || !strings.Contains(recs, "Task:Call First Customer") {
		t.Fatalf("records not created: %s", recs)
	}

	// invalid inputs are rejected
	if err := env.svc.AddFirstRecord(ctx, c.OwnerUserID, "task", map[string]string{"Title": "x", "DueDate": "tomorrow"}, ""); err == nil {
		t.Fatal("bad due date accepted")
	}
	if err := env.svc.AddFirstRecord(ctx, c.OwnerUserID, "invoice", map[string]string{}, ""); err == nil {
		t.Fatal("unknown record kind accepted")
	}

	// employees cannot finish onboarding for the company
	eve := env.platform.userByEmail("eve@acme.test")
	if err := env.svc.FinishOnboarding(ctx, eve.ID, ""); err == nil {
		t.Fatal("employee must not complete company onboarding")
	}

	cl.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if rsp := post("finish", url.Values{}); rsp.Header.Get("Location") != WorkspacePath(cc) {
		t.Fatalf("finishing setup should open the workspace Dashboard, got %q", rsp.Header.Get("Location"))
	}
	env.svc.cache = newAccessCache(0)
	if got := env.svc.AuthGuard(ctx, c.OwnerUserID); got != "" {
		t.Fatalf("after onboarding owner should enter the app, got %q", got)
	}
}

func TestHealthEndpoint(t *testing.T) {
	env := newTestEnv(t)
	rec := httptest.NewRecorder()
	env.svc.healthHandler(rec, httptest.NewRequest("GET", "/health", nil))
	if rec.Code != 200 {
		t.Fatalf("health: %d", rec.Code)
	}

	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["status"] != "ok" || body["database"] != "ok" || body["billing"] != "configured" {
		t.Fatalf("unexpected health: %v", body)
	}

	raw := rec.Body.String()
	for _, secret := range []string{"sk_test", testWebhookSecret, "price_culpos", "postgres://", os.Getenv("CULPOS_TEST_DATABASE_URL")} {
		if secret != "" && strings.Contains(raw, secret) {
			t.Fatalf("health endpoint leaks %q", secret)
		}
	}
}

func TestValidateEnvironment(t *testing.T) {
	for _, k := range []string{"DB_DSN", "APP_URL", "PUBLIC_APP_URL", "AUTH_JWT_SECRET", "AUTH_CSRF_SECRET", "SAAS_SECRET", "STRIPE_SECRET_KEY", "STRIPE_WEBHOOK_SECRET", "STRIPE_PRICE_ID", "SMTP_HOST", "SUPPORT_EMAIL", "FOUNDER_BOOTSTRAP_PASSWORD", "SAAS_ENABLED"} {
		t.Setenv(k, "")
	}
	t.Setenv("ENVIRONMENT", "production")

	var sb strings.Builder
	if !ReportConfigProblems(&sb, ValidateEnvironment()) {
		t.Fatal("missing database/app URL/secrets must stop startup in production")
	}
	for _, want := range []string{"DATABASE_URL", "APP_URL", "JWT_SECRET", "CSRF_SECRET"} {
		if !strings.Contains(sb.String(), want) {
			t.Errorf("missing report for %s", want)
		}
	}

	t.Setenv("DB_DSN", "sqlite3://file.db")
	if !ReportConfigProblems(io.Discard, ValidateEnvironment()) {
		t.Fatal("non-PostgreSQL database must be rejected")
	}

	t.Setenv("DB_DSN", "postgres://u:secretpw@db:5432/culpos")
	t.Setenv("APP_URL", "https://app.culpos.example")
	t.Setenv("AUTH_JWT_SECRET", strings.Repeat("a", 64))
	t.Setenv("AUTH_CSRF_SECRET", strings.Repeat("b", 64))
	sb.Reset()
	if ReportConfigProblems(&sb, ValidateEnvironment()) {
		t.Fatalf("valid configuration rejected: %s", sb.String())
	}
	if !strings.Contains(sb.String(), "STRIPE_SECRET_KEY") || !strings.Contains(sb.String(), "SMTP_HOST") {
		t.Fatal("missing Stripe/SMTP should be reported as warnings")
	}
	if strings.Contains(sb.String(), "secretpw") {
		t.Fatal("configuration report must never print values")
	}
}

func TestLifecycleEmails(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	_ = env.svc.BootstrapFounder(ctx)
	f, _ := env.svc.repo.FounderByUsername(ctx, "founder")
	c := env.paidCompany(t, "Acme", "owner@acme.test")

	// schedule cancellation then resume
	if err := env.svc.setCancelAtPeriodEnd(ctx, c, true); err != nil {
		t.Fatal(err)
	}
	c, _ = env.svc.repo.CompanyByID(ctx, c.ID)
	if err := env.svc.setCancelAtPeriodEnd(ctx, c, false); err != nil {
		t.Fatal(err)
	}
	if env.mail.count("set to cancel") != 1 || env.mail.count("will continue") != 1 {
		t.Fatal("cancellation/resume emails not sent")
	}

	_ = env.svc.SetCompanyEnabled(ctx, f, c.ID, false, "")
	_ = env.svc.SetCompanyEnabled(ctx, f, c.ID, true, "")
	if env.mail.count("access is unavailable") != 1 || env.mail.count("access has been restored") != 1 {
		t.Fatal("account disabled/restored emails not sent")
	}

	if err := env.svc.ResetUserAccess(ctx, f, c.OwnerUserID, ""); err != nil {
		t.Fatal(err)
	}
	if env.mail.count("Security notice") != 1 || len(env.platform.resets) != 1 {
		t.Fatal("security notice / reset not sent")
	}

	for _, e := range env.mail.sent {
		if strings.Contains(e, "Corteza") || strings.Contains(e, "localhost") {
			t.Fatalf("email not properly branded: %.200s", e)
		}
	}
}

// TestDirectObjectReferenceMatrix exercises every HTTP method against another
// company's objects through the API gate and the company/billing pages
func TestDirectObjectReferenceMatrix(t *testing.T) {
	env := newTestEnv(t)
	env.svc.cfg.SecureCookies = false
	ctx := context.Background()
	a := env.paidCompany(t, "Acme", "owner@acme.test")
	b := env.paidCompany(t, "Beta", "owner@beta.test")
	gateCompanies = []*Company{a, b}
	_ = env.svc.Invite(ctx, b.OwnerUserID, "emp@beta.test", "", RoleEmployee, "")
	bEmp := env.platform.userByEmail("emp@beta.test")

	h := gateServer(env, a.OwnerUserID)
	methods := []string{"GET", "POST", "PUT", "PATCH", "DELETE"}
	targets := []string{
		fmt.Sprintf("/api/compose/namespace/%d", b.NamespaceID),
		fmt.Sprintf("/api/compose/namespace/%d/module/", b.NamespaceID),
		fmt.Sprintf("/api/compose/namespace/%d/module/11/record/22", b.NamespaceID),
		fmt.Sprintf("/api/compose/namespace/%d/module/11/record/", b.NamespaceID),
		fmt.Sprintf("/api/compose/namespace/%d/module/11/record/export.csv", b.NamespaceID),
		fmt.Sprintf("/api/compose/namespace/%d/page/", b.NamespaceID),
		fmt.Sprintf("/api/compose/namespace/%d/chart/", b.NamespaceID),
		fmt.Sprintf("/api/compose/namespace/%d/attachment/record/5/original/x.pdf", b.NamespaceID),
		fmt.Sprintf("/api/system/users/%d", b.OwnerUserID),
		fmt.Sprintf("/api/system/users/%d", bEmp.ID),
		fmt.Sprintf("/api/system/users/%d/membership", b.OwnerUserID),
		fmt.Sprintf("/api/system/users/%d/password", b.OwnerUserID),
		fmt.Sprintf("/api/system/roles/%d", b.RoleOwnerID),
		fmt.Sprintf("/api/system/roles/%d/members", b.RoleOwnerID),
		fmt.Sprintf("/api/system/roles/%d/member/%d", b.RoleOwnerID, a.OwnerUserID),
		fmt.Sprintf("/api/compose/permissions/%d/rules", b.RoleEmployeeID),
		"/api/system/users/export/users.zip",
		"/api/automation/workflows/",
	}

	for _, target := range targets {
		for _, m := range methods {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(m, target, strings.NewReader(`{"values":[]}`)))
			if rec.Code != http.StatusNotFound && rec.Code != http.StatusForbidden {
				t.Errorf("%s %s: expected denial, got %d", m, target, rec.Code)
			}
		}
	}

	// company management pages cannot target another company's users
	srv, cl := founderServer(t, env)
	env.user = a.OwnerUserID
	rsp, _ := cl.Get(srv.URL + "/company")
	body, _ := io.ReadAll(rsp.Body)
	csrf := csrfRE.FindStringSubmatch(string(body))[1]
	if strings.Contains(string(body), "owner@beta.test") || strings.Contains(string(body), "emp@beta.test") {
		t.Fatal("company admin shows another company's users")
	}

	for _, action := range []string{"role", "remove"} {
		for _, uid := range []uint64{b.OwnerUserID, bEmp.ID} {
			_, _ = cl.PostForm(fmt.Sprintf("%s/company/members/%d/%s", srv.URL, uid, action), url.Values{"csrf": {csrf}, "role": {"manager"}})
		}
	}

	if m, _ := env.svc.repo.MemberByUser(ctx, bEmp.ID); m == nil || m.CompanyID != b.ID || m.Role != RoleEmployee {
		t.Fatal("company A modified a company B member")
	}
	if env.platform.userByEmail("emp@beta.test").Suspended {
		t.Fatal("company A removed a company B member")
	}

	// billing actions only ever affect the signed-in user's company
	_, _ = cl.PostForm(srv.URL+"/billing/cancel", url.Values{"csrf": {csrf}})
	bb, _ := env.svc.repo.CompanyByID(ctx, b.ID)
	if bb.CancelAtPeriodEnd {
		t.Fatal("company A canceled company B's subscription")
	}

	// signed checkout references cannot be forged or reused for another company
	ref := env.svc.signedRef(a.ID)
	forged := fmt.Sprint(b.ID) + ref[strings.Index(ref, "."):]
	if _, ok := env.svc.parseRef(forged); ok {
		t.Fatal("forged checkout reference accepted")
	}

	// Founder routes require a founder session
	for _, p := range []string{fmt.Sprintf("/founder/companies/%d", b.ID), "/founder/users"} {
		rsp, _ := cl.Get(srv.URL + p)
		if rsp.StatusCode != http.StatusSeeOther {
			t.Fatalf("%s reachable without founder session", p)
		}
	}
	rsp, _ = cl.PostForm(fmt.Sprintf("%s/founder/companies/%d/disable", srv.URL, b.ID), url.Values{"csrf": {csrf}})
	if bb, _ := env.svc.repo.CompanyByID(ctx, b.ID); bb.Status == CompanyDisabled || rsp.StatusCode != http.StatusSeeOther {
		t.Fatal("company user disabled another company through founder route")
	}

}
