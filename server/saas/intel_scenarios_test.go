package saas_test

import (
	"math"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/cortezaproject/corteza/server/saas"
	"github.com/cortezaproject/corteza/server/saas/fixture"
)

var scnNow = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

func scope(q string) saas.Scope {
	v, _ := url.ParseQuery(q)
	return saas.ParseIntelScope(v, scnNow, time.Time{})
}

func lookups(o fixture.Org) saas.Lookups {
	people := map[uint64]string{}
	for _, mm := range o.Members {
		for _, p := range mm {
			people[p] = "Person " + string(rune('A'+p%26))
		}
	}
	return saas.Lookups{Departments: o.Departments, Teams: o.Teams, TeamDepartment: o.TeamDept, People: people}
}

func history(days int) ([]saas.ActivityEvent, fixture.Org) {
	org := fixture.DefaultOrg(nil)
	return convert(fixture.Generate(fixture.Options{Now: scnNow, Days: days, Seed: 42, Org: org})), org
}

func convert(ee []fixture.Event) []saas.ActivityEvent {
	out := make([]saas.ActivityEvent, len(ee))
	for i, e := range ee {
		out[i] = saas.ActivityEvent{OccurredAt: e.OccurredAt, Module: e.Module, RecordID: e.RecordID, Title: e.Title, Kind: e.Kind,
			FromStatus: e.FromStatus, ToStatus: e.ToStatus, ActorID: e.ActorID, AssigneeID: e.AssigneeID, DepartmentID: e.DepartmentID,
			TeamID: e.TeamID, Category: e.Category, Priority: e.Priority, CustomerID: e.CustomerID, CaseID: e.CaseID, DueAt: e.DueAt, Source: e.Source}
	}
	return out
}

var scnTargets = map[string]float64{"Task": 48, "Approval": 36, "Approval|Pending": 24, "Case|Open": 24, "OperationsRecord|In Review": 24}

func TestIntelEmptyCompany(t *testing.T) {
	in := saas.NewIntel(nil, scnNow, scope("range=30d"), nil, saas.Lookups{})
	if in.HasData() {
		t.Fatal("empty company has no data")
	}
	for _, k := range in.KPIs() {
		if k.Key == "cycle_median" && k.HasValue {
			t.Fatal("no cycle time without completed work")
		}
		if k.Key == "sla" && k.HasValue {
			t.Fatal("no SLA compliance without targets")
		}
	}
	if len(in.Recommendations()) != 0 || len(in.WhatChanged()) != 0 || len(in.Summary()) != 0 {
		t.Fatal("no insights may be produced without history")
	}
	if c := in.Coverage(); c.Records != 0 || c.Events != 0 {
		t.Fatalf("coverage: %+v", c)
	}
	if rs := in.RecordSet(url.Values{"set": {"wip"}}); rs.Total != 0 {
		t.Fatal("no records")
	}
}

func TestIntelSevenDays(t *testing.T) {
	ee, org := history(7)
	in := saas.NewIntel(ee, scnNow, scope("range=7d"), scnTargets, lookups(org))
	if !in.HasData() {
		t.Fatal("a week of history")
	}
	m := in.Measure(in.W)
	if m.Started == 0 || m.Completed == 0 || m.WIP == 0 {
		t.Fatalf("measures: %+v", m)
	}
	for _, r := range in.Recommendations() {
		if r.Strength.Level == "High" {
			t.Fatalf("a week of history cannot give high evidence: %+v", r.Strength)
		}
	}
}

func TestIntelThirtyDaysAndYear(t *testing.T) {
	ee, org := history(400)
	lk := lookups(org)

	for _, q := range []string{"range=30d", "range=90d", "range=12m", "range=ytd", "range=all"} {
		in := saas.NewIntel(ee, scnNow, scope(q), scnTargets, lk)
		m := in.Measure(in.W)
		if m.Completed == 0 || m.Cycle.N != m.Completed {
			t.Fatalf("%s: completed %d cycle n %d", q, m.Completed, m.Cycle.N)
		}
		if !(m.Cycle.Median <= m.Cycle.P90 && m.Cycle.P90 <= m.Cycle.P95 && m.Cycle.P95 <= m.Cycle.Max) {
			t.Fatalf("%s: percentiles out of order: %+v", q, m.Cycle)
		}
		if m.SLAApplicable == 0 || m.SLAMet+m.SLABreaches != m.SLAApplicable {
			t.Fatalf("%s: SLA arithmetic: %+v", q, m)
		}
		if m.Handoffs == 0 || m.ReworkItems == 0 || m.Blocked < 0 {
			t.Fatalf("%s: handoffs %d rework %d", q, m.Handoffs, m.ReworkItems)
		}

		// every KPI with a population resolves to exactly those records
		for set, want := range map[string]int{"completed": m.Completed, "started": m.Started, "wip": m.WIP, "blocked": m.Blocked,
			"rework": m.ReworkItems, "aging": m.Aging, "overdue": m.Overdue, "failed": m.Failed, "open_cases": m.OpenCases,
			"sla": m.SLAApplicable, "sla_breached": m.SLABreaches + m.OpenBreaching} {
			rs := in.RecordSet(url.Values{"set": {set}})
			if rs.Total != want {
				t.Fatalf("%s: set %s has %d records, KPI says %d", q, set, rs.Total, want)
			}
		}
	}

	in := saas.NewIntel(ee, scnNow, scope("range=30d"), scnTargets, lk)
	kk := in.KPIs()
	if len(kk) < 20 {
		t.Fatalf("overview must carry the full KPI set: %d", len(kk))
	}
	for _, k := range kk {
		if len(k.Series) == 0 || (k.HasValue && k.Value == "—") {
			t.Fatalf("kpi %s: %+v", k.Key, k)
		}
		if k.Key == "sla" && (k.Prev == "" || k.Delta == "") {
			t.Fatalf("SLA must compare with the previous period: %+v", k)
		}
	}

	// stage analytics with severity components
	stages := in.Stages()
	if len(stages) != 4 {
		t.Fatalf("four workflows: %d", len(stages))
	}
	top := in.TopBottleneck(stages)
	if top == nil || len(top.Components) == 0 || top.Band == "" {
		t.Fatalf("bottleneck must show its components: %+v", top)
	}
	ap := in.StagesOf("Approval")
	pending := ap.Row("Pending")
	if pending == nil || !pending.HasTarget || pending.Breaches == 0 || pending.Time.N == 0 {
		t.Fatalf("approval pending stage: %+v", pending)
	}
	var loop bool
	for _, e := range ap.Edges {
		if e.From == "Approved" && e.To == "Pending" && e.Backward {
			loop = true
		}
	}
	if !loop {
		t.Fatal("repeat approvals must appear as a backward edge on the map")
	}

	// SLA view
	rows, breaches := in.SLA()
	if len(rows) < 4 || len(breaches) == 0 {
		t.Fatalf("sla rows %d breaches %d", len(rows), len(breaches))
	}
	for _, r := range rows {
		if r.Items > 0 && math.Abs(r.Compliance+r.BreachRate-100) > 1e-6 {
			t.Fatalf("compliance + breach rate must be 100: %+v", r)
		}
	}

	// aging, throughput, capacity, handoffs, rework, process
	ag := in.Aging("age", "department")
	sum := 0
	for _, r := range ag.Rows {
		sum += r.Total
	}
	if sum != ag.Total || ag.Total != in.Measure(in.W).WIP {
		t.Fatalf("aging totals %d/%d", sum, ag.Total)
	}
	tp := in.Throughput("day", "team")
	if len(tp.Rows) < 29 || tp.Totals.Completed != in.Measure(in.W).Completed {
		t.Fatalf("throughput rows %d completed %d", len(tp.Rows), tp.Totals.Completed)
	}
	cp := in.Capacity()
	if len(cp.People) == 0 || len(cp.Teams) != 4 || len(cp.Departments) != 3 {
		t.Fatalf("capacity: %d people %d teams %d depts", len(cp.People), len(cp.Teams), len(cp.Departments))
	}
	hv := in.Handoffs("team")
	if hv.Total == 0 || len(hv.Rows) == 0 || len(hv.Stages) == 0 {
		t.Fatalf("handoffs: %+v", hv.Total)
	}
	rw := in.Rework()
	var paths []string
	for _, p := range rw.Paths {
		paths = append(paths, p.Path)
	}
	joined := strings.Join(paths, "|")
	if !strings.Contains(joined, "Approved → Pending → Approved") || !strings.Contains(joined, "In Review → Open → In Review") {
		t.Fatalf("rework paths: %v", paths)
	}
	pr := in.Process("Task")
	if pr.Items == 0 || len(pr.Paths) == 0 || pr.Fastest == nil || pr.HistMax == 0 || len(pr.Contribution) == 0 {
		t.Fatalf("process review: %+v", pr.Items)
	}

	// what changed: approvals slowed down in the last 30 days
	var approvalSlower, reviewFaster bool
	for _, c := range in.WhatChanged() {
		if strings.Contains(c.Label, "Approvals · Pending stage time") && c.Dir == "up" && !c.Good {
			approvalSlower = true
		}
		if strings.Contains(c.Label, "Tasks · Waiting stage time") && c.Dir == "down" && c.Good {
			reviewFaster = true
		}
	}
	if !approvalSlower || !reviewFaster {
		t.Fatalf("what changed missed the regression (%v) or the improvement (%v)", approvalSlower, reviewFaster)
	}
	why := in.WhyCycle(in.W, in.Prev)
	if !why.Enough || len(why.Drivers) == 0 {
		t.Fatal("why must decompose the cycle change")
	}
	total := 0.0
	for _, d := range why.Drivers {
		total += d.DeltaH
	}
	if math.Abs(total-(why.Cur-why.Prev)) > 0.5 {
		t.Fatalf("stage contributions (%.2f) must add up to the cycle change (%.2f)", total, why.Cur-why.Prev)
	}
	if len(in.Summary()) < 3 {
		t.Fatal("management summary")
	}
	recs := in.Recommendations()
	if len(recs) == 0 || len(recs[0].Evidence) < 2 || recs[0].Action == "" || recs[0].Records == "" || len(recs[0].Strength.Reasons) == 0 {
		t.Fatalf("recommendations need evidence, action and records: %+v", recs)
	}
	for _, r := range recs {
		if strings.Contains(strings.ToLower(r.Action), "improve workflow") {
			t.Fatal("recommendations must be specific")
		}
	}

	// month report, comparison, coverage
	mr := in.Month(scnNow)
	if !mr.Has || len(mr.Rows) == 0 || len(mr.Months) < 12 || len(mr.Rows[0].Series) != 12 {
		t.Fatalf("month report: %+v", mr.Label)
	}
	cur, prev, _ := saas.CompareWindows("month", scnNow)
	if len(in.Compare(cur, prev)) == 0 {
		t.Fatal("comparison")
	}
	if c := in.Coverage(); c.FullHistory < 99 || c.Team < 99 {
		t.Fatalf("fixture history is complete: %+v", c)
	}

	// activity history drilldowns
	for _, v := range []string{"year", "month", "week", "day"} {
		h := in.ActivityHistory(v, "all", scnNow.AddDate(0, 0, -3), nil, "/command/activity", nil)
		if h.Total == 0 {
			t.Fatalf("%s view is empty", v)
		}
		if v == "day" && len(h.Events) == 0 {
			t.Fatal("day view must list events")
		}
	}
	if h := in.ActivityHistory("year", "approvals", scnNow, nil, "/command/activity", nil); h.Total == 0 || h.Total >= in.ActivityHistory("year", "all", scnNow, nil, "/command/activity", nil).Total {
		t.Fatal("activity filters must narrow the graph")
	}
}

func TestIntelScopeFilters(t *testing.T) {
	ee, org := history(120)
	lk := lookups(org)
	all := saas.NewIntel(ee, scnNow, scope("range=90d"), scnTargets, lk)
	ops := saas.NewIntel(ee, scnNow, scope("range=90d&department=9001"), scnTargets, lk)
	fin := saas.NewIntel(ee, scnNow, scope("range=90d&department=9002"), scnTargets, lk)
	a, o, f := all.Measure(all.W), ops.Measure(ops.W), fin.Measure(fin.W)
	if o.Completed == 0 || f.Completed == 0 || o.Completed+f.Completed >= a.Completed {
		t.Fatalf("department filters: all %d ops %d finance %d", a.Completed, o.Completed, f.Completed)
	}
	appr := saas.NewIntel(ee, scnNow, scope("range=90d&workflow=Approval&type=Purchase"), scnTargets, lk)
	for _, it := range appr.Items {
		if it.Module != "Approval" || it.Category != "Purchase" {
			t.Fatalf("filter leak: %s %s", it.Module, it.Category)
		}
	}
	custom := saas.NewIntel(ee, scnNow, scope("range=custom&from=2026-08-01&to=2026-08-31"), scnTargets, lk)
	if custom.W.From.Format("2006-01-02") != "2026-08-01" || custom.W.To.Format("2006-01-02") != "2026-09-01" {
		t.Fatalf("custom range: %v", custom.W)
	}
}

// a small hand-built history with known answers
func TestIntelExactMeasurements(t *testing.T) {
	t0 := scnNow.Add(-10 * 24 * time.Hour)
	h := func(n float64) time.Time { return t0.Add(time.Duration(n * float64(time.Hour))) }
	ev := func(at time.Time, kind, from, to string, assignee, actor uint64) saas.ActivityEvent {
		return saas.ActivityEvent{OccurredAt: at, Module: "Approval", RecordID: 1, Title: "PO 1", Kind: kind, FromStatus: from, ToStatus: to,
			AssigneeID: assignee, ActorID: actor, TeamID: 7, DepartmentID: 8, Source: "live"}
	}
	events := []saas.ActivityEvent{
		ev(h(0), saas.ActivityCreated, "", "Pending", 11, 10),
		ev(h(4), saas.ActivityUpdated, "", "Pending", 12, 11),         // handoff 11 → 12
		ev(h(10), saas.ActivityStatus, "Pending", "Approved", 12, 12), // picked up after 6h
		ev(h(30), saas.ActivityStatus, "Approved", "Pending", 12, 12), // reopened: loop
		ev(h(36), saas.ActivityStatus, "Pending", "Approved", 12, 12), // loop closed after 6h
	}
	in := saas.NewIntel(events, scnNow, scope("range=30d"), map[string]float64{"Approval|Pending": 8}, saas.Lookups{})
	it := in.Record(1)
	if it == nil || len(it.Segments) != 4 || len(it.Handoffs()) != 1 || len(it.Loops) != 1 {
		t.Fatalf("reconstruction: %+v", it)
	}
	if w := it.Handoffs()[0].Wait(scnNow); w != 6*time.Hour {
		t.Fatalf("handoff wait %v", w)
	}
	if !it.Handoffs()[0].Rework {
		t.Fatal("the loop followed the handoff")
	}
	if d := it.Loops[0].Dur(scnNow); d != 6*time.Hour || it.Loops[0].From != "Approved" || it.Loops[0].To != "Pending" {
		t.Fatalf("loop: %+v %v", it.Loops[0], d)
	}
	if c := it.Cycle(); c != 36*time.Hour {
		t.Fatalf("cycle %v", c)
	}
	m := in.Measure(in.W)
	if m.Completed != 1 || m.ReworkItems != 1 || m.SLAApplicable != 1 || m.SLABreaches != 1 || m.Handoffs != 1 || m.HandoffWait.Median != 6 {
		t.Fatalf("measures: %+v", m)
	}
	ri, ok := in.RecordIntel(1)
	if !ok || ri.SLA != "Breached" || ri.Handoffs != 1 || ri.Loops != 1 {
		t.Fatalf("record intel: %+v", ri)
	}
	var kinds []string
	for _, e := range ri.Timeline {
		kinds = append(kinds, e.Kind)
	}
	if got := strings.Join(kinds, ","); got != "created,assigned,handoff,sla,completed,reopened,completed" {
		t.Fatalf("timeline: %s", got)
	}
	if _, ok := in.RecordIntel(999); ok {
		t.Fatal("unknown record")
	}
}

func TestGoalsAndInterventions(t *testing.T) {
	ee, org := history(200)
	in := saas.NewIntel(ee, scnNow, scope("range=30d"), scnTargets, lookups(org))

	start := scnNow.AddDate(0, 0, -25)
	base, n := in.MetricIn("stage", "Task", "Waiting", saas.Window{From: start.AddDate(0, 0, -30), To: start})
	if n < 20 {
		t.Fatalf("baseline sample %d", n)
	}
	target := scnNow.AddDate(0, 0, 30)
	g := &saas.Goal{ID: 1, Title: "Review wait under 6h", Metric: "stage", Module: "Task", Stage: "Waiting", Target: 6, Baseline: base, StartAt: start, TargetAt: &target}
	gv := in.EvaluateGoal(g)
	if gv.Current >= base || gv.Status == "Off track" || gv.CurrentN == 0 || len(gv.Series) == 0 {
		t.Fatalf("goal: current %.1f baseline %.1f status %s", gv.Current, base, gv.Status)
	}
	if len(gv.Drivers) == 0 || gv.Confidence.Level == "" || len(gv.Confidence.Reasons) == 0 {
		t.Fatal("goal needs drivers and an explained confidence")
	}

	iv := &saas.Intervention{ID: 1, Title: "Review pairing", Module: "Task", Stage: "Waiting", Metric: "stage", StartedAt: start, BaselineDays: 30, EvalDays: 30}
	tv := in.EvaluateTest(iv)
	if tv.Verdict != "Associated improvement" || tv.BeforeN < 20 || tv.AfterN < 20 || tv.After >= tv.Before {
		t.Fatalf("intervention: %s before %.1f (%d) after %.1f (%d)", tv.Verdict, tv.Before, tv.BeforeN, tv.After, tv.AfterN)
	}
	if strings.Contains(strings.ToLower(tv.VerdictNote()), "caused") {
		t.Fatal("interventions must not claim causation")
	}
	slow := &saas.Intervention{ID: 2, Title: "New approval form", Module: "Approval", Stage: "Pending", Metric: "stage", StartedAt: scnNow.AddDate(0, 0, -29), BaselineDays: 30, EvalDays: 30}
	if v := in.EvaluateTest(slow); v.Verdict != "Associated worsening" {
		t.Fatalf("approval slowdown: %s", v.Verdict)
	}
}
