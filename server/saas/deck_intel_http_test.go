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

	"github.com/cortezaproject/corteza/server/saas/fixture"
)

// seedHistory writes generated history for a company (tests only)
func seedHistory(t *testing.T, env *testEnv, c *Company, ee []fixture.Event) {
	t.Helper()
	const chunk = 500
	for i := 0; i < len(ee); i += chunk {
		end := i + chunk
		if end > len(ee) {
			end = len(ee)
		}
		var (
			vals []string
			args []any
		)
		for _, e := range ee[i:end] {
			n := len(args)
			vals = append(vals, fmt.Sprintf("($%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d)",
				n+1, n+2, n+3, n+4, n+5, n+6, n+7, n+8, n+9, n+10, n+11, n+12, n+13, n+14, n+15, n+16, n+17, n+18))
			args = append(args, c.ID, e.OccurredAt, e.Module, e.RecordID, e.Title, e.Kind, e.FromStatus, e.ToStatus, e.ActorID, e.AssigneeID,
				e.DepartmentID, e.DueAt, "live", e.TeamID, e.Category, e.Priority, e.CustomerID, e.CaseID)
		}
		if _, err := env.db.Exec(`INSERT INTO saas_activity_events (company_id, occurred_at, module, record_id, title, kind, from_status, to_status,
			actor_id, assignee_id, department_id, due_at, source, team_id, category, priority, customer_id, case_id) VALUES `+strings.Join(vals, ","), args...); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	_ = env.svc.repo.MarkActivityBackfilled(context.Background(), c.ID, now)
	c.ActivityBackfilledAt = &now
	env.svc.intelCache.invalidate(c.ID)
}

func TestCommandDeckPopulatedViewsAndIsolation(t *testing.T) {
	env := newTestEnv(t)
	env.svc.cfg.SecureCookies = false
	ctx := context.Background()

	a := env.paidCompany(t, "Acme", "owner@acme.test")
	b := env.paidCompany(t, "Beta", "owner@beta.test")
	_ = env.svc.FinishOnboarding(ctx, a.OwnerUserID, "")

	var people []uint64
	for i := 0; i < 9; i++ {
		id, _, err := env.platform.InviteUser(ctx, a, fmt.Sprintf("p%d@acme.test", i), fmt.Sprintf("Person %d", i), RoleEmployee)
		if err != nil {
			t.Fatal(err)
		}
		_ = env.svc.repo.AddMember(ctx, &Member{CompanyID: a.ID, UserID: id, Role: RoleEmployee})
		people = append(people, id)
	}
	org := fixture.DefaultOrg(people)
	now := env.svc.now()
	seedHistory(t, env, a, fixture.Generate(fixture.Options{Now: now, Days: 120, Seed: 7, Org: org, FirstID: 70_000_000}))
	env.platform.mu.Lock()
	env.platform.lookups = map[uint64]WorkspaceLookup{a.ID: {Departments: org.Departments, Teams: org.Teams, TeamDepartment: org.TeamDept,
		DepartmentManager: map[uint64]uint64{9001: people[0]}, RecordPages: map[string]uint64{"Task": 7001}}}
	env.platform.mu.Unlock()
	for k, v := range map[string]float64{"Task": 48, "Approval|Pending": 24, "Case|Open": 24} {
		m, s := splitTargetKey(k)
		_ = env.svc.repo.SetStageTarget(ctx, a.ID, m, s, v, a.OwnerUserID)
	}

	srv, cl := founderServer(t, env)
	cl.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	get := func(path string) (int, string) {
		rsp, err := cl.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(rsp.Body)
		return rsp.StatusCode, string(body)
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

	// every view renders with deep content
	want := map[string][]string{
		"/command":                                       {"Operations summary", "Total active work", "P95 cycle time", "SLA compliance", "Handoff delay", "Where work is stuck", "What management should investigate", "DATA COVERAGE", "<svg class=\"spark"},
		"/command/activity":                              {"Year", "operational events", "heat-cell"},
		"/command/activity?view=month":                   {"cal-d"},
		"/command/activity?view=week":                    {"class=\"hours\""},
		"/command/activity?view=day":                     {"Events"},
		"/command/pipeline":                              {"Tasks · stages", "P95", "Net WIP", "Handoff delay", "Severity"},
		"/command/map?map=Approval":                      {"class=\"pmap\"", "Connections", "rework loop"},
		"/command/sla":                                   {"Every SLA", "Breached records", "Max breach", "SLA targets"},
		"/command/aging":                                 {"open items by stage", "At risk of missing SLA"},
		"/command/aging?dim=assignee":                    {"open items by assignee"},
		"/command/throughput?unit=week":                  {"class=\"bars\"", "Moving avg"},
		"/command/outcomes":                              {"management report", "3-mo avg", "Major bottlenecks"},
		"/command/process?map=Task":                      {"Execution paths", "Fastest common path", "Cycle-time distribution", "Stage variance"},
		"/command/handoffs?by=team":                      {"Ownership handoffs", "Stage handoffs", "Share of delay"},
		"/command/rework":                                {"Top rework paths", "Time lost to rework"},
		"/command/organization":                          {"Departments", "Operations", "Process ownership"},
		"/command/capacity":                              {"not a performance ranking", "Person 0"},
		"/command/goals":                                 {"New goal", "SLA targets"},
		"/command/tests":                                 {"New intervention"},
		"/command/recommendations":                       {"Recommendation:", "evidence"},
		"/command/changes":                               {"Why did cycle time change?", "Period comparison"},
		"/command/changes?compare=quarter":               {"This quarter vs previous quarter"},
		"/command/definitions":                           {"SLA compliance", "Bottleneck severity"},
		"/command/department/9001":                       {"Operations", "Monthly trend", "Top bottlenecks", "Activity history"},
		"/command/team/9103":                             {"Approvals Desk"},
		"/command/stage?workflow=Approval&stage=Pending": {"Severity components", "Arrives from", "Records in this stage"},
		"/command/records?set=sla_breached":              {"SLA breaches", "records"},
		"/command/pipeline?range=7d":                     {"Last 7 days"},
		"/command/pipeline?range=custom&from=2026-01-01&to=2026-01-31": {"Custom"},
		"/command/pipeline?department=9002&workflow=Approval":          {"Department: Finance", "Workflow: Approvals"},
	}
	for path, ww := range want {
		code, body := get(path)
		if code != http.StatusOK {
			t.Fatalf("%s: %d", path, code)
		}
		for _, w := range ww {
			if !strings.Contains(body, w) {
				t.Fatalf("%s: missing %q", path, w)
			}
		}
		if strings.Contains(strings.ToLower(body), "lorem") || strings.Contains(body, "Beta Secret") {
			t.Fatalf("%s: placeholder or foreign data", path)
		}
	}
	code, body := get(fmt.Sprintf("/command/person/%d", people[0]))
	if code != http.StatusOK || !strings.Contains(body, "does not rate people") {
		t.Fatalf("person detail: %d", code)
	}

	// every record set resolves
	for set := range recordSetTitles {
		if code, _ := get("/command/records?set=" + set); code != http.StatusOK {
			t.Fatalf("set %s: %d", set, code)
		}
	}

	// record intelligence, full page and drawer partial
	in, err := env.svc.loadIntel(ctx, a, url.Values{})
	if err != nil {
		t.Fatal(err)
	}
	var rec *WorkItem
	for _, it := range in.Items {
		if len(it.Loops) > 0 && len(it.Handoffs()) > 0 {
			rec = it
			break
		}
	}
	if rec == nil {
		t.Fatal("fixture has loops and handoffs")
	}
	code, body = get(fmt.Sprintf("/command/record/%d", rec.ID))
	if code != http.StatusOK || !strings.Contains(body, "Timeline") || !strings.Contains(body, "Handed off") || !strings.Contains(body, "Rework loops") {
		t.Fatalf("record page: %d", code)
	}
	code, body = get(fmt.Sprintf("/command/record/%d?partial=1", rec.ID))
	if code != http.StatusOK || strings.Contains(body, "<html") || !strings.Contains(body, "Timeline") {
		t.Fatalf("record drawer: %d", code)
	}

	// goals and intervention tests with windows, owner and notes
	if rsp := post("/command/actions/goals", url.Values{"title": {"Approvals under a day"}, "metric": {"stage"}, "module": {"Approval"}, "stage": {"Pending"},
		"target": {"24"}, "target_date": {now.AddDate(0, 2, 0).Format("2006-01-02")}}); rsp.StatusCode != http.StatusSeeOther {
		t.Fatalf("goal: %d", rsp.StatusCode)
	}
	goals, _ := env.svc.repo.Goals(ctx, a.ID)
	if len(goals) != 1 || goals[0].BaselineN == 0 || goals[0].Stage != "Pending" {
		t.Fatalf("goal stored: %+v", goals)
	}
	code, body = get(fmt.Sprintf("/command/goals/%d", goals[0].ID))
	if code != http.StatusOK || !strings.Contains(body, "Goal drivers") || !strings.Contains(body, "Confidence in the projection") {
		t.Fatalf("goal detail: %d", code)
	}
	if rsp := post("/command/actions/goals", url.Values{"title": {"x"}, "metric": {"sla"}, "target": {"140"}}); rsp.StatusCode != http.StatusSeeOther {
		t.Fatal("invalid goal must be refused with a message")
	}
	if g, _ := env.svc.repo.Goals(ctx, a.ID); len(g) != 1 {
		t.Fatal("invalid goal stored")
	}

	rsp := post("/command/actions/tests", url.Values{"title": {"Route approvals to available managers"}, "metric": {"stage"}, "module": {"Approval"}, "stage": {"Pending"},
		"start": {now.AddDate(0, 0, -20).Format("2006-01-02")}, "baseline_days": {"30"}, "eval_days": {"30"}, "owner": {fmt.Sprint(people[2])}, "notes": {"Pilot in Finance"}})
	if rsp.StatusCode != http.StatusSeeOther {
		t.Fatalf("test: %d", rsp.StatusCode)
	}
	ivs, _ := env.svc.repo.Interventions(ctx, a.ID)
	if len(ivs) != 1 || ivs[0].OwnerID != people[2] || ivs[0].BaselineDays != 30 || ivs[0].BaselineN == 0 || ivs[0].Notes != "Pilot in Finance" {
		t.Fatalf("test stored: %+v", ivs)
	}
	code, body = get(fmt.Sprintf("/command/tests/%d", ivs[0].ID))
	if code != http.StatusOK || !strings.Contains(body, "Before") || !strings.Contains(body, "Evidence strength") || !strings.Contains(body, "Person 2") {
		t.Fatalf("test detail: %d", code)
	}
	if strings.Contains(body, "caused") {
		t.Fatal("no causal claims")
	}
	post("/command/actions/end-test", url.Values{"id": {fmt.Sprint(ivs[0].ID)}})
	if ivs, _ = env.svc.repo.Interventions(ctx, a.ID); ivs[0].EndedAt == nil || ivs[0].Result == nil {
		t.Fatal("ending freezes the result")
	}

	// per-stage SLA targets through the form
	post("/command/actions/targets", url.Values{"target_OperationsRecord|In Review": {"12"}, "target_Task|Bogus": {"5"}})
	tg, _ := env.svc.repo.DeckTargets(ctx, a.ID)
	if tg["OperationsRecord|In Review"] != 12 || tg["Task|Bogus"] != 0 {
		t.Fatalf("stage targets: %v", tg)
	}

	// deck usage is counted for the Founder
	if u, _ := env.svc.repo.DeckUsage(ctx, a.ID, now.AddDate(0, 0, -1)); len(u) == 0 || u[0].Views < 10 {
		t.Fatalf("deck usage: %+v", u)
	}

	// ---------------------------------------------------------------
	// Company B can neither reach nor infer anything of Company A
	recs := in.Recommendations()
	env.user = b.OwnerUserID
	env.svc.cache = newAccessCache(0)
	for _, p := range []string{
		fmt.Sprintf("/command/record/%d", rec.ID),
		fmt.Sprintf("/command/record/%d?partial=1", rec.ID),
		"/command/department/9001",
		"/command/team/9101",
		fmt.Sprintf("/command/person/%d", people[0]),
		fmt.Sprintf("/command/goals/%d", goals[0].ID),
		fmt.Sprintf("/command/tests/%d", ivs[0].ID),
	} {
		if code, _ := get(p); code != http.StatusNotFound {
			t.Fatalf("company B reached %s: %d", p, code)
		}
	}
	if len(recs) > 0 {
		if code, _ := get("/command/recommendations/" + recs[0].Key); code != http.StatusNotFound {
			t.Fatal("company B reached a recommendation derived from company A")
		}
	}
	markers := []string{rec.Title, "Approvals under a day", "Route approvals to available managers", "Customer Service", "Approvals Desk", "Person 0"}
	for path := range want {
		if strings.HasPrefix(path, "/command/department") || strings.HasPrefix(path, "/command/team") {
			continue
		}
		_, body := get(path)
		for _, m := range markers {
			if strings.Contains(body, m) {
				t.Fatalf("company B sees %q on %s", m, path)
			}
		}
	}
	for set := range recordSetTitles {
		_, body := get("/command/records?set=" + set)
		if strings.Contains(body, "data-drawer") || strings.Contains(body, "/command/record/") {
			t.Fatalf("company B record set %s is not empty", set)
		}
	}
	bin, _ := env.svc.loadIntel(ctx, b, url.Values{"range": {"all"}})
	if m := bin.Measure(bin.W); m.Started+m.Completed+m.WIP+m.EventCount != 0 {
		t.Fatalf("company B counts include company A: %+v", m)
	}
	post("/command/actions/end-test", url.Values{"id": {fmt.Sprint(ivs[0].ID)}})
	post("/command/actions/goal-status", url.Values{"id": {fmt.Sprint(goals[0].ID)}, "status": {"closed"}})
	post("/command/actions/test-notes", url.Values{"id": {fmt.Sprint(ivs[0].ID)}, "notes": {"hijack"}})
	if g, _ := env.svc.repo.GoalByID(ctx, a.ID, goals[0].ID); g.Status != "active" {
		t.Fatal("company B changed company A's goal")
	}
	if iv, _ := env.svc.repo.InterventionByID(ctx, a.ID, ivs[0].ID); iv.Notes != "Pilot in Finance" {
		t.Fatal("company B changed company A's intervention notes")
	}
}

func TestFounderExecutionIntelligence(t *testing.T) {
	env := newTestEnv(t)
	env.svc.cfg.SecureCookies = false
	ctx := context.Background()

	a := env.paidCompany(t, "Acme", "owner@acme.test")
	b := env.paidCompany(t, "Beta", "owner@beta.test")
	org := fixture.DefaultOrg(nil)
	now := env.svc.now()
	seedHistory(t, env, a, fixture.Generate(fixture.Options{Now: now, Days: 60, Seed: 3, Org: org, FirstID: 80_000_000}))
	ir := &IssueReport{CompanyID: a.ID, UserID: a.OwnerUserID, Category: "Data looks wrong", Summary: "Totals differ", Page: "/command/pipeline"}
	if err := env.svc.repo.CreateIssueReport(ctx, ir); err != nil {
		t.Fatal(err)
	}
	_ = env.svc.repo.RecordDeckView(ctx, a.ID, a.OwnerUserID, now)

	srv, cl := founderServer(t, env)
	cl.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	get := func(path string) (int, string) {
		rsp, err := cl.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(rsp.Body)
		return rsp.StatusCode, string(body)
	}

	// company users cannot reach any Founder view
	env.user = a.OwnerUserID
	env.svc.cache = newAccessCache(0)
	for _, p := range []string{"/founder/dashboard", fmt.Sprintf("/founder/companies/%d", b.ID), "/founder/issues"} {
		if code, body := get(p); code == http.StatusOK && strings.Contains(body, "Platform Usage") {
			t.Fatalf("company user reached %s", p)
		}
	}

	_ = env.svc.BootstrapFounder(ctx)
	ftok := getCSRF(t, cl, srv.URL+"/founder")
	_, _ = cl.PostForm(srv.URL+"/founder", url.Values{"csrf": {ftok}, "password": {"test-founder-passphrase-1"}})

	code, body := get("/founder/dashboard")
	if code != http.StatusOK || !strings.Contains(body, "Platform Usage") || !strings.Contains(body, "Command Deck adoption") || !strings.Contains(body, "Acme") {
		t.Fatalf("founder dashboard: %d", code)
	}
	code, body = get(fmt.Sprintf("/founder/companies/%d?date=%s", a.ID, now.AddDate(0, 0, -3).Format("2006-01-02")))
	if code != http.StatusOK || !strings.Contains(body, "Company Intelligence") || !strings.Contains(body, "Operational Usage") || !strings.Contains(body, "heat-cell") || !strings.Contains(body, "Totals differ") {
		t.Fatalf("founder company intelligence: %d", code)
	}

	// issue lifecycle: open → in review → resolved (with note) → reopened
	tok := csrfRE.FindStringSubmatch(body)[1]
	for _, st := range []string{"in_review", "resolved", "reopened"} {
		v := url.Values{"csrf": {tok}}
		if st == "resolved" {
			v.Set("note", "Fixed the export totals")
		}
		rsp, _ := cl.PostForm(srv.URL+fmt.Sprintf("/founder/issues/%d/%s", ir.ID, st), v)
		if rsp.StatusCode != http.StatusSeeOther {
			t.Fatalf("issue %s: %d", st, rsp.StatusCode)
		}
		got, _ := env.svc.repo.IssueByID(ctx, ir.ID)
		if got.Status != st {
			t.Fatalf("issue status %s, want %s", got.Status, st)
		}
	}
	got, _ := env.svc.repo.IssueByID(ctx, ir.ID)
	if got.ResolutionNote != "Fixed the export totals" {
		t.Fatalf("resolution note: %q", got.ResolutionNote)
	}
	if rsp, _ := cl.PostForm(srv.URL+fmt.Sprintf("/founder/issues/%d/deleted", ir.ID), url.Values{"csrf": {tok}}); rsp.StatusCode != http.StatusNotFound {
		t.Fatal("unknown issue states are refused")
	}
	code, body = get("/founder/issues?status=reopened")
	if code != http.StatusOK || !strings.Contains(body, "Totals differ") || !strings.Contains(body, "Reopened · 1") {
		t.Fatalf("issue inbox filter: %d", code)
	}
	if _, body = get("/founder/issues?status=resolved"); strings.Contains(body, "Totals differ") {
		t.Fatal("status filter")
	}
}
