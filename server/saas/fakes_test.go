package saas

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"golang.org/x/crypto/bcrypt"
)

var idSeq uint64 = uint64(time.Now().UnixNano()) / 1000

func nextTestID() uint64 { return atomic.AddUint64(&idSeq, 1) }

// fakePlatform is an in-memory implementation of Platform
type fakePlatform struct {
	mu         sync.Mutex
	users      map[uint64]*fakeUser
	provisions int
	revoked    map[uint64]int
	resets     []string
	roleOf     map[uint64]CompanyRole
	records    map[uint64][]string
}

type fakeUser struct {
	UserInfo
	hash string
}

func newFakePlatform() *fakePlatform {
	return &fakePlatform{users: map[uint64]*fakeUser{}, revoked: map[uint64]int{}, roleOf: map[uint64]CompanyRole{}}
}

func (p *fakePlatform) UserExists(_ context.Context, email string) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, u := range p.users {
		if strings.EqualFold(u.Email, email) {
			return true, nil
		}
	}
	return false, nil
}

func (p *fakePlatform) CheckPasswordStrength(pw string) bool { return len(pw) >= 8 }

func (p *fakePlatform) CreatePendingOwner(_ context.Context, email, name, password string) (uint64, error) {
	h, _ := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	id := nextTestID()
	p.mu.Lock()
	p.users[id] = &fakeUser{UserInfo{ID: id, Email: email, Name: name, Suspended: true}, string(h)}
	p.mu.Unlock()
	return id, nil
}

func (p *fakePlatform) ProvisionCompany(_ context.Context, c *Company) (ProvisioningResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.provisions++
	p.users[c.OwnerUserID].Suspended = false
	p.roleOf[c.OwnerUserID] = RoleOwner
	return ProvisioningResult{NamespaceID: nextTestID(), RoleOwnerID: nextTestID(), RoleAdminID: nextTestID(), RoleManagerID: nextTestID(), RoleEmployeeID: nextTestID()}, nil
}

func (p *fakePlatform) InviteUser(_ context.Context, c *Company, email, name string, role CompanyRole) (uint64, string, error) {
	id := nextTestID()
	p.mu.Lock()
	p.users[id] = &fakeUser{UserInfo: UserInfo{ID: id, Email: email, Name: name}}
	p.roleOf[id] = role
	p.mu.Unlock()
	return id, "https://app.culpos.example/auth/accept-invite?token=t", nil
}

func (p *fakePlatform) SetMemberRole(_ context.Context, _ *Company, uid uint64, _, to CompanyRole) error {
	p.mu.Lock()
	p.roleOf[uid] = to
	p.mu.Unlock()
	return nil
}

func (p *fakePlatform) RemoveUser(_ context.Context, _ *Company, uid uint64, _ CompanyRole) error {
	return p.SuspendUser(context.Background(), uid)
}

func (p *fakePlatform) SuspendUser(_ context.Context, uid uint64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if u, ok := p.users[uid]; ok {
		u.Suspended = true
	}
	return nil
}

func (p *fakePlatform) UnsuspendUser(_ context.Context, uid uint64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if u, ok := p.users[uid]; ok {
		u.Suspended = false
	}
	return nil
}

func (p *fakePlatform) RevokeSessions(_ context.Context, uid uint64) error {
	p.mu.Lock()
	p.revoked[uid]++
	p.mu.Unlock()
	return nil
}

func (p *fakePlatform) SendPasswordReset(_ context.Context, email string) error {
	p.mu.Lock()
	p.resets = append(p.resets, email)
	p.mu.Unlock()
	return nil
}

func (p *fakePlatform) Users(_ context.Context, ids ...uint64) (map[uint64]UserInfo, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := map[uint64]UserInfo{}
	for _, id := range ids {
		if u, ok := p.users[id]; ok {
			out[id] = u.UserInfo
		}
	}
	return out, nil
}

func (p *fakePlatform) userByEmail(email string) *fakeUser {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, u := range p.users {
		if u.Email == email {
			return u
		}
	}
	return nil
}

// fakeStripe is an in-memory Stripe API
type fakeStripe struct {
	mu        sync.Mutex
	customers int
	sessions  []CheckoutParams
	subs      map[string]*StripeSubscription
}

func newFakeStripe() *fakeStripe { return &fakeStripe{subs: map[string]*StripeSubscription{}} }

func (s *fakeStripe) CreateCustomer(context.Context, CustomerParams) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.customers++
	return fmt.Sprintf("cus_test_%d", nextTestID()), nil
}

func (s *fakeStripe) CreateCheckoutSession(_ context.Context, p CheckoutParams) (*CheckoutSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions = append(s.sessions, p)
	return &CheckoutSession{ID: "cs_test", URL: "https://checkout.stripe.test/cs_test"}, nil
}

func (s *fakeStripe) CreatePortalSession(_ context.Context, customerID, _ string) (string, error) {
	return "https://billing.stripe.test/" + customerID, nil
}

func (s *fakeStripe) GetSubscription(_ context.Context, id string) (*StripeSubscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sub, ok := s.subs[id]; ok {
		cp := *sub
		return &cp, nil
	}
	return nil, fmt.Errorf("no such subscription")
}

func (s *fakeStripe) SetCancelAtPeriodEnd(_ context.Context, id string, cancel bool) (*StripeSubscription, error) {
	s.mu.Lock()
	sub := s.subs[id]
	sub.CancelAtPeriodEnd = cancel
	s.mu.Unlock()
	return s.GetSubscription(context.Background(), id)
}

func (s *fakeStripe) putSub(id, customer, status, price string, end time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub := &StripeSubscription{ID: id, Customer: []byte(`"` + customer + `"`), Status: status,
		CurrentPeriodStart: time.Now().Add(-time.Hour).Unix(), CurrentPeriodEnd: end.Unix()}
	sub.Items.Data = append(sub.Items.Data, struct {
		CurrentPeriodStart int64 `json:"current_period_start"`
		CurrentPeriodEnd   int64 `json:"current_period_end"`
		Price              struct {
			ID string `json:"id"`
		} `json:"price"`
	}{})
	sub.Items.Data[0].Price.ID = price
	s.subs[id] = sub
}

type fakeMailer struct {
	mu   sync.Mutex
	sent []string
}

func (m *fakeMailer) Send(_ context.Context, to, subject, body string) error {
	m.mu.Lock()
	m.sent = append(m.sent, to+"|"+subject+"|"+body)
	m.mu.Unlock()
	return nil
}

func (m *fakeMailer) count(substr string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, s := range m.sent {
		if strings.Contains(s, substr) {
			n++
		}
	}
	return n
}

const testWebhookSecret = "whsec_unit_test"

func testConfig() Config {
	return Config{
		Enabled: true,
		Brand: Brand{ProductName: "CulpOS", CompanyName: "Culp Industries", ProductDescription: "Business Operations System",
			Tagline: DefaultTagline, PriceCents: 33388, PriceDisplay: "$333.88", Currency: "usd", BillingInterval: "month",
			SupportEmail: "support@culpos.example", AppURL: "https://app.culpos.example", PublicAppURL: "https://app.culpos.example"},
		StripeSecretKey: "sk_test", StripeWebhookSecret: testWebhookSecret, StripePriceID: "price_culpos",
		FounderBootstrapUsername: "founder", FounderBootstrapPassword: "test-founder-passphrase-1",
		FounderSessionIdleTTL: 30 * time.Minute, FounderSessionAbsoluteTTL: 8 * time.Hour,
		FounderMaxFailedLogins: 5, FounderLockoutDuration: 15 * time.Minute,
		SecureCookies: true, WebhookTolerance: 5 * time.Minute, Production: true,
	}
}

// testDB connects to CULPOS_TEST_DATABASE_URL and resets the saas schema
func testDB(t *testing.T) *sql.DB {
	dsn := os.Getenv("CULPOS_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("CULPOS_TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}

	for _, tbl := range []string{"saas_audit_log", "saas_payments", "saas_stripe_events", "saas_founder_sessions", "saas_founders", "saas_company_members", "saas_companies"} {
		if _, err = db.Exec("DROP TABLE IF EXISTS " + tbl + " CASCADE"); err != nil {
			t.Fatal(err)
		}
	}

	if err = Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = db.Close() })
	return db
}

type testEnv struct {
	svc      *Service
	platform *fakePlatform
	stripe   *fakeStripe
	mail     *fakeMailer
	db       *sql.DB
	user     uint64 // simulated signed-in application user
}

func newTestService(t *testing.T, db *sql.DB) *Service {
	var repo *Repo
	if db != nil {
		repo = NewRepo(db)
	}

	svc, err := NewService(testConfig(), repo, newFakeStripe(), newFakePlatform(), &fakeMailer{}, nil, []byte(strings.Repeat("k", 32)), nextTestID)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func newTestEnv(t *testing.T) *testEnv {
	db := testDB(t)
	env := &testEnv{platform: newFakePlatform(), stripe: newFakeStripe(), mail: &fakeMailer{}, db: db}
	svc, err := NewService(testConfig(), NewRepo(db), env.stripe, env.platform, env.mail, nil, []byte(strings.Repeat("k", 32)), nextTestID)
	if err != nil {
		t.Fatal(err)
	}

	svc.SetSessionUserResolver(func(*http.Request) uint64 { return env.user })
	env.svc = svc
	return env
}

// webhook signs and delivers an event to the service
func (e *testEnv) webhook(t *testing.T, id, typ string, obj any) error {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"id": id, "type": typ, "created": time.Now().Unix(), "data": map[string]any{"object": obj}})
	return e.svc.HandleWebhook(context.Background(), b, SignWebhookPayload(b, testWebhookSecret, time.Now()))
}

// paidCompany runs signup and a successful checkout, returns the company
func (e *testEnv) paidCompany(t *testing.T, name, email string) *Company {
	t.Helper()
	ctx := context.Background()
	_, c, err := e.svc.Signup(ctx, SignupInput{CompanyName: name, FirstName: "Ada", LastName: "Owner", Email: email, Password: "Str0ngPassw0rd!"})
	if err != nil {
		t.Fatalf("signup: %v", err)
	}

	c, _ = e.svc.repo.CompanyByID(ctx, c.ID)
	sub := "sub_" + fmt.Sprint(c.ID)
	e.stripe.putSub(sub, c.StripeCustomerID, "active", "price_culpos", time.Now().Add(30*24*time.Hour))

	if err = e.webhook(t, "evt_cs_"+fmt.Sprint(c.ID), "checkout.session.completed", map[string]any{
		"id": "cs_1", "mode": "subscription", "status": "complete", "payment_status": "paid",
		"client_reference_id": fmt.Sprint(c.ID), "customer": c.StripeCustomerID, "subscription": sub,
		"metadata": map[string]string{"company_id": fmt.Sprint(c.ID)},
	}); err != nil {
		t.Fatalf("checkout webhook: %v", err)
	}

	c, _ = e.svc.repo.CompanyByID(ctx, c.ID)
	return c
}

func (p *fakePlatform) CreateRecord(_ context.Context, c *Company, userID uint64, role CompanyRole, module string, values map[string]string) (uint64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.records == nil {
		p.records = map[uint64][]string{}
	}
	p.records[c.ID] = append(p.records[c.ID], module+":"+values["Name"]+values["Title"])
	return nextTestID(), nil
}

func (p *fakePlatform) RenameWorkspace(context.Context, *Company, string) error { return nil }
