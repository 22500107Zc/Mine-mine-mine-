package saas

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestCommandDeckPages(t *testing.T) {
	env := newTestEnv(t)
	env.svc.cfg.SecureCookies = false
	ctx := context.Background()

	a := env.paidCompany(t, "Acme", "owner@acme.test")
	b := env.paidCompany(t, "Beta", "owner@beta.test")
	_ = env.svc.FinishOnboarding(ctx, a.OwnerUserID, "")

	// existing workspace records are seeded once
	env.platform.mu.Lock()
	env.platform.snapshot = map[uint64][]ActivityEvent{a.ID: {
		{Module: "Customer", RecordID: 900, Title: "Seeded Customer", Kind: ActivityCreated, OccurredAt: time.Now().Add(-48 * time.Hour)},
	}}
	env.platform.mu.Unlock()

	// live activity through the hook, per namespace
	now := time.Now().UTC()
	env.svc.RecordActivity(ctx, a.NamespaceID, ActivityEvent{Module: "Task", RecordID: 901, Title: "Call Northwind", Kind: ActivityCreated, ToStatus: "Open", OccurredAt: now.Add(-time.Hour)})
	env.svc.RecordActivity(ctx, a.NamespaceID, ActivityEvent{Module: "Task", RecordID: 901, Title: "Call Northwind", Kind: ActivityStatus, FromStatus: "Open", ToStatus: "Done", OccurredAt: now})
	env.svc.RecordActivity(ctx, b.NamespaceID, ActivityEvent{Module: "Task", RecordID: 902, Title: "Beta Secret Plan", Kind: ActivityCreated, ToStatus: "Open", OccurredAt: now})
	env.svc.RecordActivity(ctx, 424242, ActivityEvent{Module: "Task", RecordID: 903, Title: "Not a company", Kind: ActivityCreated})

	srv, cl := founderServer(t, env)
	get := func(path string) (int, string) {
		rsp, err := cl.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(rsp.Body)
		return rsp.StatusCode, string(body)
	}

	env.user = a.OwnerUserID
	env.svc.cache = newAccessCache(0)
	code, body := get("/command")
	if code != http.StatusOK {
		t.Fatalf("owner deck: %d", code)
	}
	for _, want := range []string{"Command Deck", "Activity Graph", "Pipeline &amp; Bottlenecks", "Goal Intelligence", "What is happening",
		"Where is it happening", "Why might it be happening", "What is it affecting", "What should we test next", "3 events / 12 months"} {
		if !strings.Contains(body, want) {
			t.Fatalf("deck missing %q", want)
		}
	}
	if strings.Contains(body, "Beta Secret Plan") || strings.Contains(strings.ToLower(body), "demo") {
		t.Fatal("deck must only show the company's own, real data")
	}

	// seeding happens once
	_, _ = get("/command")
	var seeded int
	_ = env.db.QueryRow(`SELECT COUNT(*) FROM saas_activity_events WHERE company_id = $1 AND source = 'baseline'`, a.ID).Scan(&seeded)
	if seeded != 1 {
		t.Fatalf("baseline seeded %d times", seeded)
	}

	// day drill-down lists the day's records with links into the workspace
	code, body = get("/command/activity?day=" + now.Format("2006-01-02"))
	link := fmt.Sprintf("/compose/ns/%s/pages/7001/record/901", a.Slug)
	if code != http.StatusOK || !strings.Contains(body, "Call Northwind") || !strings.Contains(body, link) || !strings.Contains(body, "Open → Done") {
		t.Fatalf("day drill-down: %d", code)
	}
	if strings.Contains(body, "Beta Secret Plan") {
		t.Fatal("another company's activity leaked into the drill-down")
	}

	for _, p := range []string{"/command/pipeline", "/command/goals"} {
		if code, _ = get(p); code != http.StatusOK {
			t.Fatalf("%s: %d", p, code)
		}
	}

	// employees do not see company-wide analytics
	eveID, _, err := env.platform.InviteUser(ctx, a, "eve@acme.test", "Eve", RoleEmployee)
	if err != nil {
		t.Fatal(err)
	}
	if err = env.svc.repo.AddMember(ctx, &Member{CompanyID: a.ID, UserID: eveID, Role: RoleEmployee}); err != nil {
		t.Fatal(err)
	}
	env.user = eveID
	env.svc.cache = newAccessCache(0)
	if code, _ = get("/command"); code != http.StatusForbidden {
		t.Fatalf("employee must not open the Command Deck: %d", code)
	}

	// unpaid companies are sent to billing
	env.user = b.OwnerUserID
	_ = env.svc.repo.SetSubscriptionStatus(ctx, b.ID, SubUnpaid)
	env.svc.cache = newAccessCache(0)
	cl.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	rsp, _ := cl.Get(srv.URL + "/command")
	if rsp.StatusCode != http.StatusSeeOther || rsp.Header.Get("Location") != "/billing" {
		t.Fatalf("unpaid: %d %s", rsp.StatusCode, rsp.Header.Get("Location"))
	}

	// deleting a record keeps its metrics but removes its content
	env.svc.RecordActivity(ctx, a.NamespaceID, ActivityEvent{Module: "Task", RecordID: 901, Kind: ActivityDeleted})
	var titled int
	_ = env.db.QueryRow(`SELECT COUNT(*) FROM saas_activity_events WHERE record_id = 901 AND title <> ''`).Scan(&titled)
	if titled != 0 {
		t.Fatal("deleted record titles must be removed")
	}

	var orphan int
	_ = env.db.QueryRow(`SELECT COUNT(*) FROM saas_activity_events WHERE record_id = 903`).Scan(&orphan)
	if orphan != 0 {
		t.Fatal("activity outside company workspaces must not be recorded")
	}
}
