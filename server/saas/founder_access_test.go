package saas

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

var (
	inputRE = regexp.MustCompile(`(?is)<input\b[^>]*>`)
	tagRE   = regexp.MustCompile(`(?s)<[^>]*>`)
)

// The Founder signs in with a password only: no username, no email, no
// account selector
func TestFounderAccessPageIsPasswordOnly(t *testing.T) {
	env := newTestEnv(t)
	env.svc.cfg.SecureCookies = false
	_ = env.svc.BootstrapFounder(context.Background())
	srv, cl := founderServer(t, env)

	rsp, err := cl.Get(srv.URL + "/founder")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rsp.Body)
	html := string(b)

	var visibleInputs []string
	for _, in := range inputRE.FindAllString(html, -1) {
		if !strings.Contains(in, `type="hidden"`) {
			visibleInputs = append(visibleInputs, in)
		}
	}
	if len(visibleInputs) != 1 || !strings.Contains(visibleInputs[0], `type="password"`) || !strings.Contains(visibleInputs[0], `name="password"`) {
		t.Fatalf("Founder login must have exactly one input, a password field: %v", visibleInputs)
	}

	if strings.Contains(html, "<select") || strings.Contains(html, "<textarea") {
		t.Fatal("Founder login must not offer any selector")
	}

	lower := strings.ToLower(html)
	for _, banned := range []string{"username", "email", "e-mail", `autocomplete="username"`} {
		if strings.Contains(lower, banned) {
			t.Fatalf("Founder login must not mention %q", banned)
		}
	}

	// visible text is exactly the brand, heading, label and button
	body := html[strings.Index(html, "<body"):]
	text := strings.Join(strings.Fields(tagRE.ReplaceAllString(body, " ")), " ")
	if text != "Founder Access Password Sign In" || !strings.Contains(body, `alt="St.Cloud~OS"`) {
		t.Fatalf("unexpected Founder login content: %q", text)
	}

	if strings.Contains(html, "test-founder-passphrase-1") {
		t.Fatal("the Founder password must never appear in HTML")
	}

	// password alone signs in and opens the dashboard
	tok := getCSRF(t, cl, srv.URL+"/founder")
	rsp, _ = cl.PostForm(srv.URL+"/founder", url.Values{"csrf": {tok}, "password": {"test-founder-passphrase-1"}})
	if rsp.StatusCode != http.StatusSeeOther || rsp.Header.Get("Location") != "/founder/dashboard" {
		t.Fatalf("password sign in failed: %d %s", rsp.StatusCode, rsp.Header.Get("Location"))
	}

	var cookie *http.Cookie
	for _, c := range rsp.Cookies() {
		if c.Name == founderCookie {
			cookie = c
		}
	}
	if cookie == nil || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/founder" {
		t.Fatalf("founder session cookie must be HttpOnly, SameSite=Strict and scoped to /founder: %+v", cookie)
	}

	rsp, _ = cl.Get(srv.URL + "/founder/dashboard")
	if rsp.StatusCode != http.StatusOK {
		t.Fatalf("dashboard not reachable after sign in: %d", rsp.StatusCode)
	}
}

func TestFounderLoginFailureIsGeneric(t *testing.T) {
	for _, initialized := range []bool{true, false} {
		env := newTestEnv(t)
		env.svc.cfg.SecureCookies = false
		if initialized {
			_ = env.svc.BootstrapFounder(context.Background())
		}
		srv, cl := founderServer(t, env)

		tok := getCSRF(t, cl, srv.URL+"/founder")
		rsp, _ := cl.PostForm(srv.URL+"/founder", url.Values{"csrf": {tok}, "password": {"not-the-founder-password"}})
		b, _ := io.ReadAll(rsp.Body)
		body := string(b)

		// the same response whether or not a Founder exists
		if rsp.StatusCode != http.StatusUnauthorized || !strings.Contains(body, "Sign in failed.") {
			t.Fatalf("initialized=%v: expected generic failure, got %d", initialized, rsp.StatusCode)
		}
		for _, leak := range []string{"$2a$", "bcrypt", "hash", "locked", "not found", "exist"} {
			if strings.Contains(strings.ToLower(body), strings.ToLower(leak)) {
				t.Fatalf("failure response reveals %q", leak)
			}
		}
		if strings.Contains(body, "not-the-founder-password") {
			t.Fatal("submitted password echoed back")
		}
	}
}

// Only one Founder can exist
func TestSingleFounderIdentity(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	if err := env.svc.BootstrapFounder(ctx); err != nil {
		t.Fatal(err)
	}

	if err := env.svc.repo.CreateFounder(ctx, &Founder{ID: 99, PasswordHash: "x"}); err == nil {
		t.Fatal("a second Founder must be rejected by the database")
	}

	var cols int
	_ = env.db.QueryRow(`SELECT COUNT(*) FROM information_schema.columns WHERE table_name = 'saas_founders' AND column_name IN ('username', 'email')`).Scan(&cols)
	if cols != 0 {
		t.Fatal("the Founder must not have a username or email")
	}
}

// Databases from the earlier username-based design are migrated to the single
// password-only Founder, keeping the Founder that signed in most recently
func TestFounderMigrationFromUsernameSchema(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	for _, q := range []string{
		`DROP TABLE saas_founder_sessions`,
		`DROP TABLE saas_founders`,
		`CREATE TABLE saas_founders (
			id BIGINT PRIMARY KEY, username TEXT NOT NULL, password_hash TEXT NOT NULL,
			failed_attempts INTEGER NOT NULL DEFAULT 0, locked_until TIMESTAMPTZ NULL, last_login_at TIMESTAMPTZ NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW())`,
		`CREATE UNIQUE INDEX saas_founders_username_uq ON saas_founders (LOWER(username))`,
		`CREATE TABLE saas_founder_sessions (
			token_hash TEXT PRIMARY KEY, founder_id BIGINT NOT NULL REFERENCES saas_founders (id), csrf_token TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), expires_at TIMESTAMPTZ NOT NULL,
			last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), ip TEXT NOT NULL DEFAULT '', user_agent TEXT NOT NULL DEFAULT '')`,
		`INSERT INTO saas_founders (id, username, password_hash, last_login_at, created_at) VALUES
			(1, 'founder', 'hash-old', NOW() - INTERVAL '10 days', NOW() - INTERVAL '30 days'),
			(2, 'owner',   'hash-new', NOW() - INTERVAL '1 hour',  NOW() - INTERVAL '5 days')`,
		`INSERT INTO saas_founder_sessions (token_hash, founder_id, csrf_token, expires_at) VALUES
			('t1', 1, 'c', NOW() + INTERVAL '1 hour'), ('t2', 2, 'c', NOW() + INTERVAL '1 hour')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}

	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	// migrations are re-run on every boot
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}

	f, err := NewRepo(db).Founder(ctx)
	if err != nil || f.ID != 2 || f.PasswordHash != "hash-new" {
		t.Fatalf("expected the most recently active Founder to remain: %+v %v", f, err)
	}

	var founders, sessions, cols int
	_ = db.QueryRow(`SELECT COUNT(*) FROM saas_founders`).Scan(&founders)
	_ = db.QueryRow(`SELECT COUNT(*) FROM saas_founder_sessions`).Scan(&sessions)
	_ = db.QueryRow(`SELECT COUNT(*) FROM information_schema.columns WHERE table_name = 'saas_founders' AND column_name = 'username'`).Scan(&cols)
	if founders != 1 || sessions != 1 || cols != 0 {
		t.Fatalf("migration incomplete: founders=%d sessions=%d usernameColumn=%d", founders, sessions, cols)
	}
}

func TestFounderBootstrapNeedsOnlyPassword(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	env.svc.cfg.FounderBootstrapPassword = ""
	_ = env.svc.BootstrapFounder(ctx)
	if _, err := env.svc.repo.Founder(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatal("no Founder without FOUNDER_BOOTSTRAP_PASSWORD")
	}
	if _, _, err := env.svc.FounderLogin(ctx, "anything-at-all", "", ""); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatal("sign in must fail generically before the Founder is initialized")
	}

	env.svc.cfg.FounderBootstrapPassword = "another-founder-secret"
	if err := env.svc.BootstrapFounder(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := env.svc.FounderLogin(ctx, "another-founder-secret", "", ""); err != nil {
		t.Fatalf("Founder must sign in with the bootstrap password alone: %v", err)
	}

	// a changed secret does not silently replace the stored password
	env.svc.cfg.FounderBootstrapPassword = "changed-secret-value"
	_ = env.svc.BootstrapFounder(ctx)
	if _, _, err := env.svc.FounderLogin(ctx, "changed-secret-value", "", ""); err == nil {
		t.Fatal("the stored password must only be replaced by an explicit reset")
	}
}
