package saas

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestBrandDefaults(t *testing.T) {
	for _, k := range []string{"PRODUCT_NAME", "COMPANY_NAME", "PRICE_CENTS", "PRICE_DISPLAY", "BILLING_INTERVAL"} {
		t.Setenv(k, "")
	}

	b := LoadBrand()
	if b.ProductName != "St.Cloud~OS" || b.CompanyName != "Culp Industries" || b.ProductDescription != "Business Execution Intelligence OS" {
		t.Fatalf("unexpected brand: %+v", b)
	}

	if b.PriceCents != 33388 || b.PriceDisplay != "$333.88" || b.PricePerInterval() != "$333.88/month" {
		t.Fatalf("unexpected price: %d %s %s", b.PriceCents, b.PriceDisplay, b.PricePerInterval())
	}

	if b.PageTitle("Billing") != "St.Cloud~OS | Billing" || b.PageTitle("") != "St.Cloud~OS" {
		t.Fatal("unexpected page title format")
	}
}

func TestFormatCents(t *testing.T) {
	cases := map[int64]string{33388: "$333.88", 0: "$0.00", 5: "$0.05", 100000: "$1,000.00", 66776: "$667.76", 123456789: "$1,234,567.89"}
	for in, out := range cases {
		if got := FormatCents(in); got != out {
			t.Errorf("FormatCents(%d) = %s, want %s", in, got, out)
		}
	}
}

func TestEvaluateSubscriptionStates(t *testing.T) {
	now := time.Now()
	future, past := now.Add(24*time.Hour), now.Add(-24*time.Hour)

	base := func(st SubscriptionStatus) *Company {
		return &Company{Status: CompanyActive, ProvisioningStatus: ProvProvisioned, SubscriptionStatus: st}
	}

	cases := []struct {
		name string
		c    *Company
		want AccessLevel
	}{
		{"active", base(SubActive), AccessFull},
		{"past due keeps access with warning", base(SubPastDue), AccessFull},
		{"canceled within paid period", func() *Company { c := base(SubCanceled); c.BillingPeriodEnd = &future; return c }(), AccessFull},
		{"canceled after period", func() *Company { c := base(SubCanceled); c.BillingPeriodEnd = &past; return c }(), AccessBillingOnly},
		{"unpaid", base(SubUnpaid), AccessBillingOnly},
		{"incomplete", base(SubIncomplete), AccessBillingOnly},
		{"incomplete expired", base(SubIncompleteExpired), AccessBillingOnly},
		{"unknown status never grants access", base("something-new"), AccessBillingOnly},
		{"trialing without period", base(SubTrialing), AccessBillingOnly},
		{"not provisioned", func() *Company { c := base(SubActive); c.ProvisioningStatus = ProvAwaitingPayment; return c }(), AccessBillingOnly},
		{"disabled by founder", func() *Company { c := base(SubActive); c.Status = CompanyDisabled; return c }(), AccessNone},
		{"nil company", nil, AccessNone},
	}

	for _, tc := range cases {
		if got := Evaluate(tc.c, now).Level; got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}

	if Evaluate(base(SubPastDue), now).Warning == "" {
		t.Error("past due should carry a billing warning")
	}
}

func TestNormalizeSubscriptionStatus(t *testing.T) {
	if NormalizeSubscriptionStatus("ACTIVE") != SubActive || NormalizeSubscriptionStatus("weird") != SubIncomplete {
		t.Fatal("normalization failed")
	}
}

func TestRoleHierarchyAndEscalation(t *testing.T) {
	if _, ok := ParseCompanyRole("founder"); ok {
		t.Fatal("founder must not be a company role")
	}

	if _, ok := ParseCompanyRole("admin"); ok {
		t.Fatal("unknown role must be rejected")
	}

	cases := []struct {
		actor, target CompanyRole
		want          bool
	}{
		{RoleOwner, RoleAdministrator, true},
		{RoleOwner, RoleEmployee, true},
		{RoleOwner, RoleOwner, false},
		{RoleAdministrator, RoleManager, true},
		{RoleAdministrator, RoleAdministrator, false},
		{RoleAdministrator, RoleOwner, false},
		{RoleManager, RoleEmployee, false},
		{RoleEmployee, RoleEmployee, false},
		{RoleOwner, CompanyRole("founder"), false},
	}

	for _, tc := range cases {
		if got := tc.actor.CanAssign(tc.target); got != tc.want {
			t.Errorf("%s assigning %s: got %v want %v", tc.actor, tc.target, got, tc.want)
		}
	}

	if !RoleOwner.CanManageSubscription() || RoleAdministrator.CanManageSubscription() {
		t.Error("only the owner manages the subscription")
	}

	if RoleManager.CanManageMembers() || RoleEmployee.CanViewBilling() {
		t.Error("manager/employee privileges too broad")
	}
}

func TestWebhookSignature(t *testing.T) {
	var (
		secret  = "whsec_test"
		payload = []byte(`{"id":"evt_1","type":"invoice.paid"}`)
		now     = time.Now()
		hdr     = SignWebhookPayload(payload, secret, now)
	)

	if err := VerifyWebhookSignature(payload, hdr, secret, 5*time.Minute, now); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}

	if VerifyWebhookSignature([]byte(`{"id":"evt_1","type":"x"}`), hdr, secret, 5*time.Minute, now) == nil {
		t.Fatal("tampered payload accepted")
	}

	if VerifyWebhookSignature(payload, hdr, "whsec_other", 5*time.Minute, now) == nil {
		t.Fatal("wrong secret accepted")
	}

	if VerifyWebhookSignature(payload, hdr, secret, 5*time.Minute, now.Add(10*time.Minute)) == nil {
		t.Fatal("replayed (expired) signature accepted")
	}

	for _, bad := range []string{"", "t=abc,v1=00", "v1=00", "t=123"} {
		if VerifyWebhookSignature(payload, bad, secret, 5*time.Minute, now) == nil {
			t.Fatalf("malformed header %q accepted", bad)
		}
	}

	if VerifyWebhookSignature(payload, hdr, "", 5*time.Minute, now) == nil {
		t.Fatal("empty secret accepted")
	}
}

func TestStripeSubscriptionMapping(t *testing.T) {
	raw := `{"id":"sub_1","customer":"cus_1","status":"past_due","current_period_start":1700000000,"current_period_end":1702592000,
		"cancel_at_period_end":true,"items":{"data":[{"price":{"id":"price_x"}}]},
		"default_payment_method":{"type":"card","card":{"brand":"visa","last4":"4242"}}}`
	s := &StripeSubscription{}
	if err := json.Unmarshal([]byte(raw), s); err != nil {
		t.Fatal(err)
	}

	u := s.ToUpdate()
	if u.CustomerID != "cus_1" || u.SubscriptionID != "sub_1" || u.PriceID != "price_x" || u.Status != SubPastDue || !u.CancelAtPeriodEnd {
		t.Fatalf("bad mapping: %+v", u)
	}

	if u.PeriodEnd == nil || u.PeriodEnd.Unix() != 1702592000 || u.PaymentMethodBrief != "Visa ending in 4242" {
		t.Fatalf("bad period/payment method: %+v", u)
	}

	// newer API versions keep periods on items; expanded customer objects
	raw2 := `{"id":"sub_2","customer":{"id":"cus_2"},"status":"active","items":{"data":[{"current_period_start":1,"current_period_end":2,"price":{"id":"p"}}]}}`
	s2 := &StripeSubscription{}
	_ = json.Unmarshal([]byte(raw2), s2)
	if s2.CustomerID() != "cus_2" {
		t.Fatal("expanded customer not handled")
	}
	if _, end := s2.Period(); end == nil || end.Unix() != 2 {
		t.Fatal("item period fallback not handled")
	}
}

func testCompany() *Company {
	return &Company{ID: 10, NamespaceID: 111, RoleOwnerID: 1, RoleAdminID: 2, RoleManagerID: 3, RoleEmployeeID: 4, Status: CompanyActive, ProvisioningStatus: ProvProvisioned, SubscriptionStatus: SubActive}
}

func TestGateDecisions(t *testing.T) {
	c := testCompany()
	owner := &Member{CompanyID: 10, UserID: 500, Role: RoleOwner}
	emp := &Member{CompanyID: 10, UserID: 501, Role: RoleEmployee}

	cases := []struct {
		m      *Member
		method string
		path   string
		want   gateDecision
	}{
		{emp, "GET", "/compose/namespace/", gateFilterNamespaces},
		{emp, "POST", "/compose/namespace/", gateDeny},
		{emp, "GET", "/compose/namespace/111/module/5/record/", gateAllow},
		{emp, "POST", "/compose/namespace/111/module/5/record/7", gateAllow},
		{emp, "GET", "/compose/namespace/222/module/5/record/", gateDeny},
		{emp, "GET", "/compose/namespace/222/module/5/record/export.csv", gateDeny},
		{emp, "POST", "/compose/namespace/222/module/5/record/7", gateDeny},
		{owner, "DELETE", "/compose/namespace/111", gateDeny},
		{owner, "POST", "/compose/namespace/111/clone", gateDeny},
		{emp, "POST", "/compose/namespace/import", gateDeny},
		{emp, "GET", "/system/users/", gateFilterUsers},
		{emp, "GET", "/system/users/500", gateFilterUsers},
		{emp, "PUT", "/system/users/500", gateDeny},
		{emp, "PUT", "/system/users/501", gateAllow},
		{emp, "POST", "/system/users/500/suspend", gateDeny},
		{emp, "GET", "/system/roles/", gateFilterRoles},
		{emp, "GET", "/system/roles/1", gateAllow},
		{emp, "GET", "/system/roles/99", gateDeny},
		{emp, "POST", "/system/roles/", gateDeny},
		{owner, "GET", "/system/settings/", gateDeny},
		{owner, "GET", "/system/settings/current", gateAllow},
		{owner, "GET", "/system/actionlog/", gateDeny},
		{owner, "GET", "/system/dal/connections/", gateDeny},
		{owner, "POST", "/system/auth/impersonate", gateDeny},
		{owner, "GET", "/system/auth/clients/", gateDeny},
		{owner, "GET", "/automation/workflows/", gateDeny},
		{owner, "GET", "/system/application/", gateFilterApps},
		{owner, "PATCH", "/compose/permissions/2/rules", gateValidateRules},
		{owner, "PATCH", "/compose/permissions/99/rules", gateDeny},
		{emp, "PATCH", "/compose/permissions/4/rules", gateDeny},
		{emp, "GET", "/federation/nodes/", gateDeny},
		{emp, "GET", "/websocket/", gateAllow},
	}

	for _, tc := range cases {
		gr := &gateRequest{method: tc.method, path: tc.path, userID: tc.m.UserID, company: c, member: tc.m}
		if got := decide(gr); got != tc.want {
			t.Errorf("%s %s %s: got %d want %d", tc.m.Role, tc.method, tc.path, got, tc.want)
		}
	}
}

func TestValidRulesBody(t *testing.T) {
	mk := func(body string) *http.Request {
		return httptest.NewRequest("PATCH", "/compose/permissions/2/rules", strings.NewReader(body))
	}

	if !validRulesBody(mk(`{"rules":[{"resource":"corteza::compose:module/111/*","operation":"read","access":"allow"}]}`), 111) {
		t.Fatal("own namespace rule rejected")
	}

	for _, body := range []string{
		`{"rules":[{"resource":"corteza::compose:namespace/*","operation":"read","access":"allow"}]}`,
		`{"rules":[{"resource":"corteza::compose:module/222/*","operation":"read","access":"allow"}]}`,
		`{"rules":[{"resource":"corteza::system:user/*","operation":"read","access":"allow"}]}`,
		`{"rules":[{"resource":"corteza::compose/","operation":"namespace.create","access":"allow"}]}`,
		`not json`,
	} {
		if validRulesBody(mk(body), 111) {
			t.Errorf("escalating rules accepted: %s", body)
		}
	}
}

func TestFilterPayload(t *testing.T) {
	raw := []byte(`{"response":{"filter":{},"set":[{"namespaceID":"111","slug":"mine"},{"namespaceID":"222","slug":"theirs"}]}}`)
	out, ok := filterPayload(raw, "namespaceID", map[uint64]bool{111: true})
	if !ok || bytes.Contains(out, []byte("theirs")) || !bytes.Contains(out, []byte("mine")) {
		t.Fatalf("filtering failed: %s", out)
	}

	single := []byte(`{"response":{"userID":"9","email":"x@other.test"}}`)
	if _, ok := filterPayload(single, "userID", map[uint64]bool{1: true}); ok {
		t.Fatal("foreign single resource must be rejected")
	}
}

func TestRestrictQueryIDs(t *testing.T) {
	r := httptest.NewRequest("GET", "/system/users/?userID=5&userID=9", nil)
	restrictQueryIDs(r, "userID", map[uint64]bool{5: true, 6: true})
	if got := r.URL.Query()["userID[]"]; len(got) != 1 || got[0] != "5" {
		t.Fatalf("unexpected ids: %v", got)
	}

	r = httptest.NewRequest("GET", "/system/users/?userID=9", nil)
	restrictQueryIDs(r, "userID", map[uint64]bool{5: true})
	if got := r.URL.Query()["userID[]"]; len(got) != 1 || got[0] != "1" {
		t.Fatalf("foreign-only query must resolve to an empty set: %v", got)
	}
}

func TestEnvAliases(t *testing.T) {
	for _, k := range []string{"DB_DSN", "SMTP_USER", "SMTP_PASS", "SMTP_FROM", "DOMAIN", "AUTH_BASE_URL", "AUTH_SESSION_COOKIE_PATH", "AUTH_SESSION_COOKIE_DOMAIN", "HTTP_SSL_TERMINATED", "AUTH_SESSION_COOKIE_SECURE", "AUTH_EXTERNAL_REDIRECT_URL"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}

	t.Setenv("DATABASE_URL", "postgres://u:p@h/db")
	t.Setenv("SMTP_USERNAME", "mailer")
	t.Setenv("SMTP_PASSWORD", "pw")
	t.Setenv("MAIL_FROM", "noreply@culpos.example")
	t.Setenv("MAIL_FROM_NAME", "")
	t.Setenv("APP_URL", "https://app.culpos.example")

	ApplyEnvAliases()

	checks := map[string]string{
		"DB_DSN":                     "postgres://u:p@h/db",
		"SMTP_USER":                  "mailer",
		"SMTP_PASS":                  "pw",
		"SMTP_FROM":                  `"St.Cloud~OS" <noreply@culpos.example>`,
		"DOMAIN":                     "app.culpos.example",
		"AUTH_BASE_URL":              "https://app.culpos.example/auth",
		"AUTH_SESSION_COOKIE_PATH":   "/",
		"AUTH_SESSION_COOKIE_DOMAIN": "app.culpos.example",
		"HTTP_SSL_TERMINATED":        "true",
		"AUTH_SESSION_COOKIE_SECURE": "true",
	}

	for k, v := range checks {
		if got := os.Getenv(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
}

func TestTemplatesAreBranded(t *testing.T) {
	svc := newTestService(t, nil)

	pages := map[string]pageData{
		"signup":          {"Title": "Create Company", "Form": map[string]string{}},
		"founder-login":   {"Title": "Founder", "Nav": "founder"},
		"legal-terms":     {"Title": "Terms of Service"},
		"legal-privacy":   {"Title": "Privacy Policy"},
		"status":          {"Title": "Page not found", "Heading": "Page not found", "Code": 404},
		"signup-complete": {"Title": "Welcome", "Company": &Company{Name: "Acme"}},
	}

	for name, d := range pages {
		rec := httptest.NewRecorder()
		svc.render(rec, httptest.NewRequest("GET", "/x", nil), 200, name, d)
		body := rec.Body.String()

		if !strings.Contains(body, "St.Cloud~OS") {
			t.Errorf("%s: missing St.Cloud~OS branding", name)
		}

		for _, bad := range []string{"Corteza", "corteza", "Planet Crust", "planetcrust", "vercel", "low-code", "no-code", LegacyProductName, "culpos-logo", "culpos-mark", "/culpos/static"} {
			if strings.Contains(body, bad) {
				t.Errorf("%s: contains upstream term %q", name, bad)
			}
		}
	}

	rec := httptest.NewRecorder()
	svc.render(rec, httptest.NewRequest("GET", "/signup", nil), 200, "signup", pageData{"Title": "Create Company", "Form": map[string]string{}})
	body := rec.Body.String()
	if !strings.Contains(body, "$333.88") || !strings.Contains(body, "/month") || !strings.Contains(body, "<title>St.Cloud~OS | Create Company</title>") {
		t.Error("signup must show $333.88/month and a branded title")
	}

	for _, e := range []string{"welcome", "invite", "payment-success", "payment-failed", "subscription-canceled"} {
		html, err := svc.renderEmail(e, map[string]any{"Company": "Acme", "URL": "https://app.culpos.example/x", "Amount": "$333.88", "Name": "Ada", "Role": "Employee", "Inviter": "Owner"})
		if err != nil {
			t.Fatalf("email %s: %v", e, err)
		}
		if !strings.Contains(html, "St.Cloud~OS") || strings.Contains(html, "Corteza") || strings.Contains(html, "localhost") || strings.Contains(html, LegacyProductName) || !strings.Contains(html, "/stcloud/static/stcloud-email-logo.png") {
			t.Errorf("email %s not properly branded", e)
		}
	}
}

func TestBuiltinLogoDetection(t *testing.T) {
	for _, v := range []string{"", "/assets/logo.svg", "/culpos/static/culpos-logo-light.svg", "/culpos/static/culpos-mark.svg", DefaultMainLogo, DefaultIconLogo} {
		if !IsBuiltinLogo(v) {
			t.Errorf("%q must be treated as a built-in logo", v)
		}
	}

	// a logo an administrator uploaded is never replaced
	if IsBuiltinLogo("/api/system/attachment/settings/123/original/logo.png") {
		t.Error("uploaded logos must be preserved")
	}
}
