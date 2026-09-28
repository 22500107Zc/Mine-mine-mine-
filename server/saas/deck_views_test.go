package saas

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestDeckViewsFromFixture(t *testing.T) {
	d := BuildDeck(fixtureEvents(), deckNow, nil)

	// Pipeline: completed Task 1 (48h) and Case 4 (96h); open Task 2
	p := Pipeline(d, d.Tracked, map[string]float64{"Case": 50, "Task": 100})
	if !p.HasCycle || !near(p.AvgCycleH, 72) || p.OpenNow != 1 {
		t.Fatalf("pipeline: %+v", p)
	}
	if p.TargetsSet != 2 || p.Breaching != 1 || !p.OverTarget["Case"] || p.OverTarget["Task"] {
		t.Fatalf("targets: %+v", p)
	}

	// status breakdown for tasks: Open 24h + 120h, In Progress 24h
	ss := StatusBreakdown(d.Tracked, "Task")
	byStatus := map[string]StatusStat{}
	for _, s := range ss {
		byStatus[s.Status] = s
	}
	if byStatus["Open"].Kind != "wait" || !near(byStatus["Open"].AvgH, 72) || byStatus["Open"].Now != 1 || byStatus["In Progress"].Kind != "work" {
		t.Fatalf("status breakdown: %+v", ss)
	}

	// process: the case was reopened once
	pv := Process(d.Tracked, "Case")
	if pv.Items != 1 || pv.Reopened != 1 || len(pv.Paths) != 1 || pv.Paths[0].Path != "New → Open → Resolved → Open → Closed" {
		t.Fatalf("process: %+v", pv)
	}
	loops := 0
	for _, tr := range pv.Transitions {
		if tr.Loop {
			loops++
			if tr.From != "Resolved" || tr.To != "Open" {
				t.Fatalf("wrong loop: %+v", tr)
			}
		}
	}
	if loops != 1 {
		t.Fatal("reopen must be reported as a loop")
	}

	// scope: only tasks, only the last 7 days
	scoped := BuildDeck(FilterEvents(fixtureEvents(), DeckScope{Days: 7, Module: "Task"}, deckNow), deckNow, nil)
	if scoped.KPI.Items != 1 || scoped.Tracked[0].ID != 2 {
		t.Fatalf("scope filter: %+v", scoped.KPI)
	}
	if s := ParseScope("90", "Approval", "12"); s.Days != 90 || s.Module != "Approval" || s.Department != 12 {
		t.Fatalf("parse scope: %+v", s)
	}
	if s := ParseScope("7", "Payroll", "x"); s.Days != 365 || s.Module != "" || s.Department != 0 {
		t.Fatal("invalid scope values must fall back to defaults")
	}

	// intervention metric over a window
	v, n := MetricValue(d.Tracked, "", "cycle", daysAgo(30), deckNow)
	if n != 2 || !near(v, 72) {
		t.Fatalf("metric cycle %.1f n=%d", v, n)
	}
	v, n = MetricValue(d.Tracked, "", "rework", daysAgo(30), deckNow)
	if n != 3 || !near(v, 100.0/3) {
		t.Fatalf("metric rework %.1f n=%d", v, n)
	}
}

func TestOrganizationAndOutcomes(t *testing.T) {
	var ee []ActivityEvent
	id := uint64(1)
	for i := 0; i < 20; i++ {
		person := uint64(1)
		if i%4 == 0 {
			person = uint64(2 + i%3)
		}
		start := daysAgo(40 - i)
		e := ev(id, "Task", ActivityCreated, "", "Open", start)
		e.AssigneeID = person
		e.DepartmentID = 7
		ee = append(ee, e, ev(id, "Task", ActivityStatus, "Open", "Done", start.Add(24*time.Hour)))
		id++
	}

	d := BuildDeck(ee, deckNow, map[uint64]string{7: "Operations"})
	o := Organization(d.Tracked, deckNow, map[uint64]string{1: "Ada Owner"}, map[uint64]string{7: "Operations"})
	if len(o.People) != 4 || o.People[0].Name != "Ada Owner" || o.People[0].Items != 15 || !near(o.People[0].Share, 75) {
		t.Fatalf("people: %+v", o.People)
	}
	if o.People[1].Name != "Former team member" {
		t.Fatal("unknown assignees must not expose identifiers")
	}
	if len(o.Departments) != 1 || o.Departments[0].Name != "Operations" || o.Departments[0].People != 4 {
		t.Fatalf("departments: %+v", o.Departments)
	}

	out := Outcomes(d.Tracked, deckNow)
	total := 0
	for _, m := range out.Months {
		total += m.Completed
	}
	if len(out.Months) != 12 || total != 20 || out.MaxDone == 0 {
		t.Fatalf("outcomes: %d months, %d completed", len(out.Months), total)
	}
}

func TestCommandDeckTabsAndActions(t *testing.T) {
	env := newTestEnv(t)
	env.svc.cfg.SecureCookies = false
	env.svc.cfg.Brand.SupportEmail = "support@culpos.test"
	ctx := context.Background()

	a := env.paidCompany(t, "Acme", "owner@acme.test")
	b := env.paidCompany(t, "Beta", "owner@beta.test")
	_ = env.svc.FinishOnboarding(ctx, a.OwnerUserID, "")
	now := time.Now().UTC()
	for i := uint64(0); i < 12; i++ {
		env.svc.RecordActivity(ctx, a.NamespaceID, ActivityEvent{Module: "Task", RecordID: 1000 + i, Title: fmt.Sprint("Task ", i), Kind: ActivityCreated, ToStatus: "Open", OccurredAt: now.Add(-72 * time.Hour)})
		env.svc.RecordActivity(ctx, a.NamespaceID, ActivityEvent{Module: "Task", RecordID: 1000 + i, Kind: ActivityStatus, FromStatus: "Open", ToStatus: "Done", OccurredAt: now.Add(-time.Hour)})
	}

	srv, cl := founderServer(t, env)
	get := func(path string) (int, string) {
		rsp, err := cl.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(rsp.Body)
		return rsp.StatusCode, string(b)
	}
	post := func(path string, v url.Values) *http.Response {
		_, body := get("/command/goals")
		v.Set("csrf", csrfRE.FindStringSubmatch(body)[1])
		rsp, err := cl.PostForm(srv.URL+path, v)
		if err != nil {
			t.Fatal(err)
		}
		return rsp
	}

	env.user = a.OwnerUserID
	env.svc.cache = newAccessCache(0)

	for _, tab := range deckTabs {
		code, body := get(tab.Path)
		if code != http.StatusOK || !strings.Contains(body, "<h1>"+escaped(tab.Title)+"</h1>") || !strings.Contains(body, escaped(tab.Question)) {
			t.Fatalf("%s: %d", tab.Path, code)
		}
	}

	code, body := get("/command/pipeline?period=90&workflow=Task")
	if code != http.StatusOK || !strings.Contains(body, "Avg end-to-end cycle") || !strings.Contains(body, "Stages breaching SLA") || !strings.Contains(body, "no targets set") {
		t.Fatalf("pipeline scope: %d", code)
	}

	// targets: owners set them, then the pipeline reports the breach
	cl.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if rsp := post("/command/actions/targets", url.Values{"target_Task": {"24"}}); rsp.StatusCode != http.StatusSeeOther {
		t.Fatalf("targets: %d", rsp.StatusCode)
	}
	if tg, _ := env.svc.repo.DeckTargets(ctx, a.ID); tg["Task"] != 24 {
		t.Fatalf("target not stored: %v", tg)
	}
	_, body = get("/command/pipeline")
	if !strings.Contains(body, "over their target cycle") {
		t.Fatal("a 71h average against a 24h target must breach")
	}
	if rsp := post("/command/actions/targets", url.Values{"target_Task": {"-3"}}); rsp.StatusCode != http.StatusSeeOther {
		t.Fatal("invalid target must be refused with a message")
	}

	// intervention tests
	post("/command/actions/tests", url.Values{"title": {"Daily stand-up"}, "metric": {"cycle"}, "module": {"Task"}})
	ivs, _ := env.svc.repo.Interventions(ctx, a.ID)
	if len(ivs) != 1 || ivs[0].BaselineN != 12 || !near(ivs[0].Baseline, 71) {
		t.Fatalf("intervention baseline: %+v", ivs)
	}
	_, body = get("/command/tests")
	if !strings.Contains(body, "Daily stand-up") || !strings.Contains(body, "Collecting data") {
		t.Fatal("running test not listed")
	}

	// another company cannot end it
	env.user = b.OwnerUserID
	env.svc.cache = newAccessCache(0)
	post("/command/actions/end-test", url.Values{"id": {fmt.Sprint(ivs[0].ID)}})
	if ivs, _ = env.svc.repo.Interventions(ctx, a.ID); ivs[0].EndedAt != nil {
		t.Fatal("a company ended another company's test")
	}
	if code, body = get("/command/tests"); strings.Contains(body, "Daily stand-up") {
		t.Fatal("tests leaked across companies")
	}
	env.user = a.OwnerUserID
	env.svc.cache = newAccessCache(0)
	post("/command/actions/end-test", url.Values{"id": {fmt.Sprint(ivs[0].ID)}})
	if ivs, _ = env.svc.repo.Interventions(ctx, a.ID); ivs[0].EndedAt == nil {
		t.Fatal("owner could not end the test")
	}

	// managers use the deck but cannot set targets
	mgr, _, _ := env.platform.InviteUser(ctx, a, "mia@acme.test", "Mia", RoleManager)
	_ = env.svc.repo.AddMember(ctx, &Member{CompanyID: a.ID, UserID: mgr, Role: RoleManager})
	env.user = mgr
	env.svc.cache = newAccessCache(0)
	if code, _ = get("/command/access"); code != http.StatusOK {
		t.Fatalf("manager deck: %d", code)
	}
	if rsp := post("/command/actions/targets", url.Values{"target_Task": {"1"}}); rsp.StatusCode != http.StatusForbidden {
		t.Fatalf("manager must not set targets: %d", rsp.StatusCode)
	}

	// any member can report an issue from Support; the Founder sees it
	eve, _, _ := env.platform.InviteUser(ctx, a, "eve@acme.test", "Eve", RoleEmployee)
	_ = env.svc.repo.AddMember(ctx, &Member{CompanyID: a.ID, UserID: eve, Role: RoleEmployee})
	env.user = eve
	env.svc.cache = newAccessCache(0)
	_, body = get("/support")
	if !strings.Contains(body, "Report an issue") {
		t.Fatal("support page must offer issue reporting to members")
	}
	tok := csrfRE.FindStringSubmatch(body)[1]
	rsp, _ := cl.PostForm(srv.URL+"/support/report", url.Values{"csrf": {tok}, "category": {"Data looks wrong"}, "summary": {"Export is <empty>"}, "details": {"Steps: open Customers"}})
	if rsp.StatusCode != http.StatusSeeOther {
		t.Fatalf("report: %d", rsp.StatusCode)
	}
	reports, _ := env.svc.repo.IssueReports(ctx, a.ID, 10)
	if len(reports) != 1 || reports[0].UserID != eve || reports[0].CompanyName != "Acme" {
		t.Fatalf("report not stored: %+v", reports)
	}
	if env.mail.count("support@culpos.test|") != 1 || env.mail.count("Export is &lt;empty&gt;") != 1 {
		t.Fatal("support must be notified, with the report escaped")
	}
	rsp, _ = cl.PostForm(srv.URL+"/support/report", url.Values{"csrf": {tok}, "category": {"Hack"}, "summary": {"x"}})
	if n, _ := env.svc.repo.IssueReports(ctx, a.ID, 10); len(n) != 1 {
		t.Fatal("invalid report accepted")
	}

	_ = env.svc.BootstrapFounder(ctx)
	ftok := getCSRF(t, cl, srv.URL+"/founder")
	_, _ = cl.PostForm(srv.URL+"/founder", url.Values{"csrf": {ftok}, "password": {"test-founder-passphrase-1"}})
	if code, body = get("/founder/issues"); code != http.StatusOK || !strings.Contains(body, "Export is &lt;empty&gt;") || !strings.Contains(body, "eve@acme.test") {
		t.Fatalf("founder issues: %d", code)
	}
}

// escaped mirrors html/template escaping of static tab text
func escaped(s string) string {
	return strings.NewReplacer("&", "&amp;", "'", "&#39;", "·", "·").Replace(s)
}
