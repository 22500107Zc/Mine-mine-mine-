package saas

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

// DeckScope narrows the analysis (Pipeline, Outcomes, Process, Organization)
type DeckScope struct {
	Days       int    // 30, 90 or 365
	Module     string // "" = all workflows
	Department uint64 // 0 = all departments
}

var scopePeriods = []struct {
	Days  int
	Label string
}{{30, "last 30 days"}, {90, "last 90 days"}, {365, "last 12 months"}}

// ParseScope reads scope query parameters, defaulting to 12 months
func ParseScope(period, workflow, dept string) DeckScope {
	s := DeckScope{Days: 365}
	if d, err := strconv.Atoi(period); err == nil && (d == 30 || d == 90 || d == 365) {
		s.Days = d
	}
	if stageFor(workflow) != nil {
		s.Module = workflow
	}
	s.Department, _ = strconv.ParseUint(dept, 10, 64)
	return s
}

// FilterEvents keeps the events of records created within the period that
// match the workflow and department
func FilterEvents(events []ActivityEvent, s DeckScope, now time.Time) []ActivityEvent {
	type rec struct {
		created time.Time
		module  string
		dept    uint64
	}
	recs := map[uint64]*rec{}
	for _, e := range events {
		r := recs[e.RecordID]
		if r == nil {
			r = &rec{created: e.OccurredAt, module: e.Module}
			recs[e.RecordID] = r
		}
		if e.DepartmentID > 0 {
			r.dept = e.DepartmentID
		}
	}

	since := now.AddDate(0, 0, -s.Days)
	var out []ActivityEvent
	for _, e := range events {
		r := recs[e.RecordID]
		if r.created.Before(since) {
			continue
		}
		if s.Module != "" && r.module != s.Module {
			continue
		}
		if s.Department > 0 && r.dept != s.Department {
			continue
		}
		out = append(out, e)
	}
	return out
}

// ---------------------------------------------------------------------
// Pipeline & Bottlenecks

type PipelineView struct {
	AvgCycleH       float64
	HasCycle        bool
	WaitShare       float64
	HasTime         bool
	Breaching       int
	TargetsSet      int
	OpenNow         int
	Statuses        []StatusStat // when one workflow is selected
	TargetOf        map[string]float64
	OverTarget      map[string]bool
	CompletedInScop int
}

type StatusStat struct {
	Status    string
	Kind      string // wait | work
	AvgH      float64
	Share     float64
	Items     int
	Now       int
	Bar       float64
	totalTime time.Duration
}

// Pipeline summarizes the scoped items against the company's targets
// (target cycle hours per workflow)
func Pipeline(d *Deck, items []*WorkItem, targets map[string]float64) PipelineView {
	v := PipelineView{TargetOf: targets, OverTarget: map[string]bool{}}

	var cycle float64
	var wait, work time.Duration
	for _, it := range items {
		wait += it.Wait
		work += it.Work
		if it.CompletedAt != nil && !it.Failed {
			cycle += it.CompletedAt.Sub(it.CreatedAt).Hours()
			v.CompletedInScop++
		}
		if it.CompletedAt == nil && !it.Failed {
			v.OpenNow++
		}
	}
	if v.CompletedInScop > 0 {
		v.HasCycle = true
		v.AvgCycleH = cycle / float64(v.CompletedInScop)
	}
	if t := wait + work; t > 0 {
		v.HasTime = true
		v.WaitShare = float64(wait) / float64(t) * 100
	}

	for _, st := range d.Stages {
		target, ok := targets[st.Module]
		if !ok || target <= 0 {
			continue
		}
		v.TargetsSet++
		if st.Completed > 0 && st.AvgCycleH > target {
			v.Breaching++
			v.OverTarget[st.Module] = true
		}
	}
	return v
}

// StatusBreakdown shows time per status for one workflow
func StatusBreakdown(items []*WorkItem, module string) []StatusStat {
	st := stageFor(module)
	if st == nil {
		return nil
	}

	by := map[string]*StatusStat{}
	var total time.Duration
	for _, it := range items {
		if it.Module != module {
			continue
		}
		for status, dur := range it.StatusTime {
			s := by[status]
			if s == nil {
				kind := "work"
				if st.Wait[status] || status == "No status" {
					kind = "wait"
				}
				s = &StatusStat{Status: status, Kind: kind}
				by[status] = s
			}
			s.totalTime += dur
			s.Items++
			total += dur
		}
		if it.CompletedAt == nil && !it.Failed {
			s := by[statusName(it.Status)]
			if s != nil {
				s.Now++
			}
		}
	}

	var out []StatusStat
	max := 0.0
	for _, s := range by {
		s.AvgH = s.totalTime.Hours() / float64(s.Items)
		if total > 0 {
			s.Share = float64(s.totalTime) / float64(total) * 100
		}
		if s.Share > max {
			max = s.Share
		}
		out = append(out, *s)
	}
	for i := range out {
		if max > 0 {
			out[i].Bar = out[i].Share / max * 100
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Share > out[j].Share })
	return out
}

// ---------------------------------------------------------------------
// Process Review

type ProcessView struct {
	Module      string
	Label       string
	Items       int
	AvgSteps    float64
	Transitions []TransitionStat
	Paths       []PathStat
	Skipped     int
	SkipShare   float64
	Reopened    int
	ReopenShare float64
}

type TransitionStat struct {
	From, To string
	Count    int
	Share    float64 // of exits from From
	Loop     bool    // leaves a finished state
	Bar      float64
}

type PathStat struct {
	Path      string
	Count     int
	Share     float64
	AvgCycleH float64
	HasCycle  bool
}

// Process reviews how items of one workflow actually move between statuses
func Process(items []*WorkItem, module string) *ProcessView {
	st := stageFor(module)
	if st == nil {
		return nil
	}
	v := &ProcessView{Module: module, Label: st.Label}

	trans := map[[2]string]int{}
	exits := map[string]int{}
	type pathAgg struct {
		n     int
		cycle float64
		done  int
	}
	paths := map[string]*pathAgg{}
	steps := 0

	for _, it := range items {
		if it.Module != module || len(it.Path) == 0 {
			continue
		}
		v.Items++
		steps += len(it.Transitions)
		for _, t := range it.Transitions {
			trans[t]++
			exits[t[0]]++
		}

		p := strings.Join(it.Path, " → ")
		pa := paths[p]
		if pa == nil {
			pa = &pathAgg{}
			paths[p] = pa
		}
		pa.n++
		if it.CompletedAt != nil && !it.Failed {
			pa.done++
			pa.cycle += it.CompletedAt.Sub(it.CreatedAt).Hours()
		}

		if it.Reopens > 0 {
			v.Reopened++
		}

		// finished without ever being actively worked
		if it.CompletedAt != nil && !it.Failed && len(st.Work) > 0 && it.Work == 0 {
			v.Skipped++
		}
	}
	if v.Items == 0 {
		return v
	}

	v.AvgSteps = float64(steps) / float64(v.Items)
	v.SkipShare = float64(v.Skipped) / float64(v.Items) * 100
	v.ReopenShare = float64(v.Reopened) / float64(v.Items) * 100

	max := 0
	for t, n := range trans {
		if n > max {
			max = n
		}
		v.Transitions = append(v.Transitions, TransitionStat{
			From: t[0], To: t[1], Count: n,
			Share: float64(n) / float64(exits[t[0]]) * 100,
			Loop:  (st.Done[t[0]] || st.Failed[t[0]]) && !st.Done[t[1]] && !st.Failed[t[1]],
		})
	}
	for i := range v.Transitions {
		v.Transitions[i].Bar = float64(v.Transitions[i].Count) / float64(max) * 100
	}
	sort.Slice(v.Transitions, func(i, j int) bool { return v.Transitions[i].Count > v.Transitions[j].Count })

	for p, pa := range paths {
		ps := PathStat{Path: p, Count: pa.n, Share: float64(pa.n) / float64(v.Items) * 100}
		if pa.done > 0 {
			ps.HasCycle, ps.AvgCycleH = true, pa.cycle/float64(pa.done)
		}
		v.Paths = append(v.Paths, ps)
	}
	sort.Slice(v.Paths, func(i, j int) bool { return v.Paths[i].Count > v.Paths[j].Count })
	if len(v.Paths) > 6 {
		v.Paths = v.Paths[:6]
	}
	return v
}

// ---------------------------------------------------------------------
// Organization Frame

type OrgView struct {
	People        []PersonLoad
	Departments   []DeptLoad
	Unassigned    int
	TopShare      float64 // share of items handled by the busiest 20% of people
	TopCount      int
	Concentration bool
}

type PersonLoad struct {
	UserID       uint64
	Name         string
	Items        int
	Open         int
	Completed30  int
	AvgCycleH    float64
	HasCycle     bool
	Share        float64
	cycle        float64
	completedAll int
}

type DeptLoad struct {
	Name      string
	Items     int
	Open      int
	AvgCycleH float64
	HasCycle  bool
	People    int
	cycle     float64
	done      int
	people    map[uint64]bool
}

// Organization shows who carries the work (by assignee) and how it is spread
func Organization(items []*WorkItem, now time.Time, names map[uint64]string, departments map[uint64]string) OrgView {
	var v OrgView
	people := map[uint64]*PersonLoad{}
	depts := map[string]*DeptLoad{}
	total := 0

	for _, it := range items {
		total++
		dn := departments[it.Department]
		if dn == "" {
			dn = "No department"
		}
		dl := depts[dn]
		if dl == nil {
			dl = &DeptLoad{Name: dn, people: map[uint64]bool{}}
			depts[dn] = dl
		}
		dl.Items++

		done := it.CompletedAt != nil && !it.Failed
		open := it.CompletedAt == nil && !it.Failed
		if open {
			dl.Open++
		}
		if done {
			dl.done++
			dl.cycle += it.CompletedAt.Sub(it.CreatedAt).Hours()
		}

		if it.Assignee == 0 {
			v.Unassigned++
			continue
		}
		dl.people[it.Assignee] = true

		p := people[it.Assignee]
		if p == nil {
			p = &PersonLoad{UserID: it.Assignee, Name: names[it.Assignee]}
			if p.Name == "" {
				p.Name = "Former team member"
			}
			people[it.Assignee] = p
		}
		p.Items++
		if open {
			p.Open++
		}
		if done {
			p.completedAll++
			p.cycle += it.CompletedAt.Sub(it.CreatedAt).Hours()
			if now.Sub(*it.CompletedAt) < 30*24*time.Hour {
				p.Completed30++
			}
		}
	}

	assigned := total - v.Unassigned
	for _, p := range people {
		if p.completedAll > 0 {
			p.HasCycle, p.AvgCycleH = true, p.cycle/float64(p.completedAll)
		}
		if assigned > 0 {
			p.Share = float64(p.Items) / float64(assigned) * 100
		}
		v.People = append(v.People, *p)
	}
	sort.Slice(v.People, func(i, j int) bool { return v.People[i].Items > v.People[j].Items })

	if n := len(v.People); n >= 5 && assigned > 0 {
		top := (n + 4) / 5
		sum := 0
		for _, p := range v.People[:top] {
			sum += p.Items
		}
		v.TopCount = top
		v.TopShare = float64(sum) / float64(assigned) * 100
		v.Concentration = v.TopShare >= 50
	}

	for _, dl := range depts {
		if dl.done > 0 {
			dl.HasCycle, dl.AvgCycleH = true, dl.cycle/float64(dl.done)
		}
		dl.People = len(dl.people)
		v.Departments = append(v.Departments, *dl)
	}
	sort.Slice(v.Departments, func(i, j int) bool { return v.Departments[i].Items > v.Departments[j].Items })
	return v
}

// ---------------------------------------------------------------------
// Outcomes & Effects

type OutcomesView struct {
	Months      []MonthStat
	MaxDone     int
	WaitEffect  *Effect
	DueEffect   *Effect
	ReworkEffct *Effect
}

type MonthStat struct {
	Label      string
	Created    int
	Completed  int
	Failed     int
	AvgCycleH  float64
	HasCycle   bool
	ReworkRate float64
	Bar        float64
	cycle      float64
	reopened   int
}

// Effect compares the average completion time of two groups of items
type Effect struct {
	Label     string
	WithH     float64
	WithoutH  float64
	WithN     int
	WithoutN  int
	DeltaPct  float64
	DescWith  string
	DescWithO string
}

func Outcomes(items []*WorkItem, now time.Time) OutcomesView {
	var v OutcomesView
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -11, 0)
	idx := map[string]int{}
	for i := 0; i < 12; i++ {
		m := start.AddDate(0, i, 0)
		idx[m.Format("2006-01")] = i
		v.Months = append(v.Months, MonthStat{Label: m.Format("Jan")})
	}

	for _, it := range items {
		if i, ok := idx[it.CreatedAt.UTC().Format("2006-01")]; ok {
			v.Months[i].Created++
			if it.Reopens > 0 {
				v.Months[i].reopened++
			}
		}
		if it.CompletedAt == nil {
			continue
		}
		if i, ok := idx[it.CompletedAt.UTC().Format("2006-01")]; ok {
			if it.Failed {
				v.Months[i].Failed++
			} else {
				v.Months[i].Completed++
				v.Months[i].cycle += it.CompletedAt.Sub(it.CreatedAt).Hours()
			}
		}
	}

	for i := range v.Months {
		m := &v.Months[i]
		if m.Completed > 0 {
			m.HasCycle, m.AvgCycleH = true, m.cycle/float64(m.Completed)
		}
		if m.Created > 0 {
			m.ReworkRate = float64(m.reopened) / float64(m.Created) * 100
		}
		if m.Completed > v.MaxDone {
			v.MaxDone = m.Completed
		}
	}
	for i := range v.Months {
		if v.MaxDone > 0 {
			v.Months[i].Bar = float64(v.Months[i].Completed) / float64(v.MaxDone) * 100
		}
	}

	v.WaitEffect = effect(items, "Waiting", "mostly waiting (over half of their time)", "mostly worked",
		func(it *WorkItem) bool { return it.Wait > it.Work })
	v.DueEffect = effect(items, "Missed due dates", "that missed their due date", "that met it",
		func(it *WorkItem) bool { return it.DueAt != nil && it.Breached })
	v.ReworkEffct = effect(items, "Rework", "that were reopened", "that were not",
		func(it *WorkItem) bool { return it.Reopens > 0 })
	return v
}

func effect(items []*WorkItem, label, with, without string, in func(*WorkItem) bool) *Effect {
	var a, b []float64
	for _, it := range items {
		if it.CompletedAt == nil || it.Failed {
			continue
		}
		c := it.CompletedAt.Sub(it.CreatedAt).Hours()
		if in(it) {
			a = append(a, c)
		} else {
			b = append(b, c)
		}
	}
	if len(a) < 5 || len(b) < 5 {
		return nil
	}
	e := &Effect{Label: label, WithH: mean(a), WithoutH: mean(b), WithN: len(a), WithoutN: len(b), DescWith: with, DescWithO: without}
	if e.WithoutH > 0 {
		e.DeltaPct = (e.WithH - e.WithoutH) / e.WithoutH * 100
	}
	return e
}

// ---------------------------------------------------------------------
// Intervention tests

var testMetrics = map[string]struct {
	Label        string
	Unit         string
	HigherBetter bool
}{
	"cycle":      {"Average completion time", "h", false},
	"wait":       {"Average waiting time", "h", false},
	"rework":     {"Reopen rate", "%", false},
	"breach":     {"Missed due dates", "%", false},
	"throughput": {"Completed per day", "/day", true},
}

// MetricValue measures a metric over items finished in [from, to)
func MetricValue(items []*WorkItem, module, metric string, from, to time.Time) (float64, int) {
	var vals []float64
	n, hits := 0, 0
	for _, it := range items {
		if module != "" && it.Module != module {
			continue
		}
		if it.CompletedAt == nil || it.CompletedAt.Before(from) || !it.CompletedAt.Before(to) {
			continue
		}
		n++
		switch metric {
		case "cycle":
			if !it.Failed {
				vals = append(vals, it.CompletedAt.Sub(it.CreatedAt).Hours())
			}
		case "wait":
			vals = append(vals, it.Wait.Hours())
		case "rework":
			if it.Reopens > 0 {
				hits++
			}
		case "breach":
			if it.Breached {
				hits++
			}
		case "throughput":
			if !it.Failed {
				hits++
			}
		}
	}

	switch metric {
	case "cycle", "wait":
		return mean(vals), len(vals)
	case "rework", "breach":
		if n == 0 {
			return 0, 0
		}
		return float64(hits) / float64(n) * 100, n
	case "throughput":
		days := to.Sub(from).Hours() / 24
		if days <= 0 {
			return 0, 0
		}
		return float64(hits) / days, n
	}
	return 0, 0
}
