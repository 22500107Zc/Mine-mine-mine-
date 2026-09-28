package saas

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// Stats summarizes a set of durations (hours)
type Stats struct {
	N                                    int
	Avg, Median, P75, P90, P95, Min, Max float64
}

func statsOf(v []float64) Stats {
	s := Stats{N: len(v)}
	if s.N == 0 {
		return s
	}
	c := append([]float64(nil), v...)
	sort.Float64s(c)
	s.Avg = mean(c)
	s.Median = median(c)
	s.P75 = percentile(c, 0.75)
	s.P90 = percentile(c, 0.90)
	s.P95 = percentile(c, 0.95)
	s.Min, s.Max = c[0], c[len(c)-1]
	return s
}

// Measures are the company's execution figures for one window
type Measures struct {
	W                        Window
	Started, Completed       int
	Failed                   int
	Throughput               float64 // completed per day
	WIP                      int     // open at the end of the window
	Cycle                    Stats   // created → completed, items completed in the window
	SLAApplicable, SLAMet    int
	SLACompliance            float64
	SLABreaches              int // completed in the window over a target
	OpenBreaching            int // open at the end, already over a target
	Blocked                  int
	ReworkItems              int
	ReworkRate               float64 // of items completed in the window
	Handoffs                 int
	HandoffWait              Stats
	Aging                    int // open for more than 7 days at the end
	Overdue                  int // open past their due date at the end
	CreatedN, CreatedDone    int
	CompletionRate           float64 // share of items created in the window that are completed
	FailureRate              float64 // share of finished items that failed (rejected)
	OpenCases, OpenTasks     int
	PendingApprovals         int
	OpenRecords              int
	ActivePeople, EventCount int
	WaitH, WorkH             float64 // time accumulated inside the window
	ReworkH                  float64 // time lost to loops started in the window
}

// agingThreshold defines "aging work"
const agingThreshold = 7 * 24 * time.Hour

// Measure computes the figures for a window over the scoped records
func (in *Intel) Measure(w Window) Measures {
	m := Measures{W: w}
	var cycles, waits []float64
	end := w.To

	for _, it := range in.Items {
		if w.Has(it.CreatedAt) {
			m.Started++
			m.CreatedN++
			if it.Done() && !it.CompletedAt.After(end) {
				m.CreatedDone++
			}
		}

		if it.CompletedAt != nil && w.Has(*it.CompletedAt) {
			if it.Failed {
				m.Failed++
			} else {
				m.Completed++
				cycles = append(cycles, it.Cycle().Hours())
				if len(it.Loops) > 0 {
					m.ReworkItems++
				}
			}
			c := in.sla(it)
			if c.slaApplicable {
				m.SLAApplicable++
				if c.breachAt == nil || c.breachAt.After(*it.CompletedAt) {
					m.SLAMet++
				} else {
					m.SLABreaches++
				}
			}
		}

		if it.OpenAt(end) {
			m.WIP++
			switch it.Module {
			case "Case":
				m.OpenCases++
			case "Task":
				m.OpenTasks++
			case "Approval":
				m.PendingApprovals++
			case "OperationsRecord":
				m.OpenRecords++
			}
			if sg, ok := it.StatusAt(end.Add(-time.Nanosecond)); ok && sg.Kind == "blocked" {
				m.Blocked++
			}
			if end.Sub(it.CreatedAt) > agingThreshold {
				m.Aging++
			}
			if it.DueAt != nil && end.After(it.DueAt.Add(24*time.Hour)) {
				m.Overdue++
			}
			if c := in.sla(it); c.breachAt != nil && c.breachAt.Before(end) {
				m.OpenBreaching++
			}
		}

		for _, a := range it.Assignments {
			if a.From > 0 && w.Has(a.At) {
				m.Handoffs++
				waits = append(waits, a.Wait(minTime(in.Now, end)).Hours())
			}
		}

		for _, sg := range it.Segments {
			e := in.Now
			if sg.End != nil {
				e = *sg.End
			}
			d := w.overlap(sg.Start, e).Hours()
			switch sg.Kind {
			case "wait", "blocked":
				m.WaitH += d
			case "work":
				m.WorkH += d
			}
		}
		for _, l := range it.Loops {
			if w.Has(l.At) {
				m.ReworkH += l.Dur(in.Now).Hours()
			}
		}
	}

	m.Cycle = statsOf(cycles)
	m.HandoffWait = statsOf(waits)
	if d := w.Days(); d > 0 {
		m.Throughput = float64(m.Completed) / d
	}
	if m.SLAApplicable > 0 {
		m.SLACompliance = float64(m.SLAMet) / float64(m.SLAApplicable) * 100
	}
	if m.Completed > 0 {
		m.ReworkRate = float64(m.ReworkItems) / float64(m.Completed) * 100
	}
	if m.CreatedN > 0 {
		m.CompletionRate = float64(m.CreatedDone) / float64(m.CreatedN) * 100
	}
	if f := m.Completed + m.Failed; f > 0 {
		m.FailureRate = float64(m.Failed) / float64(f) * 100
	}

	people := map[uint64]bool{}
	for _, e := range in.scopedEvents() {
		if w.Has(e.OccurredAt) {
			m.EventCount++
			if e.ActorID > 0 {
				people[e.ActorID] = true
			}
		}
	}
	m.ActivePeople = len(people)
	return m
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// Buckets splits a window into chart intervals: hours for a day, days up to
// a month, weeks up to ~6 months, months beyond
func Buckets(w Window) ([]Window, string) {
	days := w.Days()
	var (
		out  []Window
		step func(time.Time) time.Time
		unit string
		cur  time.Time
	)
	switch {
	case days <= 1.01:
		unit, cur = "hour", w.From.Truncate(time.Hour)
		step = func(t time.Time) time.Time { return t.Add(time.Hour) }
	case days <= 31.5:
		unit, cur = "day", dayOf(w.From)
		step = func(t time.Time) time.Time { return t.AddDate(0, 0, 1) }
	case days <= 190:
		unit = "week"
		cur = dayOf(w.From)
		cur = cur.AddDate(0, 0, -((int(cur.Weekday()) + 6) % 7))
		step = func(t time.Time) time.Time { return t.AddDate(0, 0, 7) }
	default:
		unit = "month"
		cur = time.Date(w.From.Year(), w.From.Month(), 1, 0, 0, 0, 0, time.UTC)
		step = func(t time.Time) time.Time { return t.AddDate(0, 1, 0) }
	}
	for cur.Before(w.To) && len(out) < 400 {
		next := step(cur)
		b := Window{cur, next}
		if b.From.Before(w.From) {
			b.From = w.From
		}
		if b.To.After(w.To) {
			b.To = w.To
		}
		out = append(out, b)
		cur = next
	}
	return out, unit
}

// ---------------------------------------------------------------------
// KPI cards

// KPI is one overview card
type KPI struct {
	Key      string
	Label    string
	Value    string
	Prev     string
	Delta    string // "+12.0%" / "-3 pts" / ""
	Dir      string // up | down | flat
	Status   string // good | warn | bad | neutral
	Note     string
	Series   []float64
	Link     string
	Def      string // definition key
	HasValue bool
}

type kpiSpec struct {
	key, label, def, set string
	unit                 string // h | % | n | /d
	higherBetter         *bool  // nil = neutral
	value                func(m Measures) (float64, bool)
	note                 func(m Measures) string
}

var (
	higher = func() *bool { b := true; return &b }()
	lower  = func() *bool { b := false; return &b }()
)

var kpiSpecs = []kpiSpec{
	{"wip", "Total active work", "wip", "wip", "n", nil, func(m Measures) (float64, bool) { return float64(m.WIP), true }, nil},
	{"completed", "Completed this period", "completed", "completed", "n", higher, func(m Measures) (float64, bool) { return float64(m.Completed), true }, nil},
	{"started", "New work this period", "started", "started", "n", nil, func(m Measures) (float64, bool) { return float64(m.Started), true }, nil},
	{"throughput", "Throughput", "throughput", "completed", "/d", higher, func(m Measures) (float64, bool) { return m.Throughput, true }, nil},
	{"cycle_avg", "Avg cycle time", "cycle", "completed", "h", lower, func(m Measures) (float64, bool) { return m.Cycle.Avg, m.Cycle.N > 0 }, func(m Measures) string { return fmt.Sprintf("%d completed", m.Cycle.N) }},
	{"cycle_median", "Median cycle time", "cycle_median", "completed", "h", lower, func(m Measures) (float64, bool) { return m.Cycle.Median, m.Cycle.N > 0 }, nil},
	{"cycle_p90", "P90 cycle time", "cycle_p90", "completed", "h", lower, func(m Measures) (float64, bool) { return m.Cycle.P90, m.Cycle.N > 0 }, nil},
	{"cycle_p95", "P95 cycle time", "cycle_p95", "completed", "h", lower, func(m Measures) (float64, bool) { return m.Cycle.P95, m.Cycle.N > 0 }, nil},
	{"sla", "SLA compliance", "sla", "sla", "%", higher, func(m Measures) (float64, bool) { return m.SLACompliance, m.SLAApplicable > 0 },
		func(m Measures) string {
			if m.SLAApplicable == 0 {
				return "no SLA targets apply"
			}
			return fmt.Sprintf("%d of %d within target", m.SLAMet, m.SLAApplicable)
		}},
	{"breaches", "SLA breaches", "breaches", "sla_breached", "n", lower, func(m Measures) (float64, bool) {
		return float64(m.SLABreaches + m.OpenBreaching), m.SLAApplicable > 0 || m.OpenBreaching > 0
	},
		func(m Measures) string {
			return fmt.Sprintf("%d completed late · %d open past target", m.SLABreaches, m.OpenBreaching)
		}},
	{"blocked", "Blocked work", "blocked", "blocked", "n", lower, func(m Measures) (float64, bool) { return float64(m.Blocked), true }, nil},
	{"rework", "Rework rate", "rework", "rework", "%", lower, func(m Measures) (float64, bool) { return m.ReworkRate, m.Completed > 0 },
		func(m Measures) string {
			return fmt.Sprintf("%d of %d completed went backward", m.ReworkItems, m.Completed)
		}},
	{"handoff", "Handoff delay", "handoff", "handoffs", "h", lower, func(m Measures) (float64, bool) { return m.HandoffWait.Median, m.HandoffWait.N > 0 },
		func(m Measures) string { return fmt.Sprintf("median · %d handoffs", m.HandoffWait.N) }},
	{"aging", "Aging work", "aging", "aging", "n", lower, func(m Measures) (float64, bool) { return float64(m.Aging), true }, func(Measures) string { return "open more than 7 days" }},
	{"overdue", "Overdue work", "overdue", "overdue", "n", lower, func(m Measures) (float64, bool) { return float64(m.Overdue), true }, func(Measures) string { return "open past due date" }},
	{"completion", "Process completion rate", "completion", "started", "%", higher, func(m Measures) (float64, bool) { return m.CompletionRate, m.CreatedN > 0 },
		func(m Measures) string { return fmt.Sprintf("%d of %d new items finished", m.CreatedDone, m.CreatedN) }},
	{"failure", "Workflow failure rate", "failure", "failed", "%", lower, func(m Measures) (float64, bool) { return m.FailureRate, m.Completed+m.Failed > 0 },
		func(m Measures) string { return fmt.Sprintf("%d rejected", m.Failed) }},
	{"open_cases", "Open cases", "open_cases", "open_cases", "n", nil, func(m Measures) (float64, bool) { return float64(m.OpenCases), true }, nil},
	{"open_tasks", "Open tasks", "open_tasks", "open_tasks", "n", nil, func(m Measures) (float64, bool) { return float64(m.OpenTasks), true }, nil},
	{"pending_approvals", "Pending approvals", "pending_approvals", "pending_approvals", "n", nil, func(m Measures) (float64, bool) { return float64(m.PendingApprovals), true }, nil},
	{"team", "Team activity", "team", "", "n", higher, func(m Measures) (float64, bool) { return float64(m.ActivePeople), true },
		func(m Measures) string { return fmt.Sprintf("people · %s events", formatInt(m.EventCount)) }},
}

func fmtValue(v float64, unit string) string {
	switch unit {
	case "h":
		return fmtHours(v)
	case "%":
		return fmt.Sprintf("%.1f%%", v)
	case "/d":
		return fmt.Sprintf("%.1f/day", v)
	}
	return formatInt(int(math.Round(v)))
}

// fmtHours renders hours as minutes, hours or days
func fmtHours(h float64) string {
	switch {
	case h < 1:
		return fmt.Sprintf("%.0fm", h*60)
	case h < 48:
		return fmt.Sprintf("%.1fh", h)
	}
	return fmt.Sprintf("%.1fd", h/24)
}

// KPIs builds the overview cards for the scope, with the previous comparable
// period and a series across the window
func (in *Intel) KPIs() []KPI {
	cur, prev := in.Measure(in.W), in.Measure(in.Prev)
	buckets, _ := Buckets(in.W)
	series := make([]Measures, len(buckets))
	for i, b := range buckets {
		series[i] = in.Measure(b)
	}

	var out []KPI
	for _, sp := range kpiSpecs {
		v, ok := sp.value(cur)
		pv, pok := sp.value(prev)
		k := KPI{Key: sp.key, Label: sp.label, Def: sp.def, HasValue: ok}
		if ok {
			k.Value = fmtValue(v, sp.unit)
		} else {
			k.Value = "—"
		}
		if sp.set != "" {
			k.Link = in.Scope.URL("/command/records", "set", sp.set)
		}
		if sp.note != nil {
			k.Note = sp.note(cur)
		}
		k.Dir, k.Delta, k.Status = "flat", "", "neutral"
		if ok && pok {
			k.Prev = fmtValue(pv, sp.unit)
			k.Delta, k.Dir = deltaText(v, pv, sp.unit)
			k.Status = judge(v, pv, sp.unit, sp.higherBetter)
		}
		for _, b := range series {
			bv, bok := sp.value(b)
			if !bok {
				bv = math.NaN()
			}
			k.Series = append(k.Series, bv)
		}
		out = append(out, k)
	}
	return out
}

// deltaText describes a change; percentages change in points
func deltaText(v, pv float64, unit string) (string, string) {
	dir := "flat"
	switch {
	case v > pv+1e-9:
		dir = "up"
	case v < pv-1e-9:
		dir = "down"
	}
	if unit == "%" {
		return fmt.Sprintf("%+.1f pts", v-pv), dir
	}
	if pv == 0 {
		if v == 0 {
			return "no change", dir
		}
		return "new", dir
	}
	return fmt.Sprintf("%+.1f%%", (v-pv)/math.Abs(pv)*100), dir
}

// judge compares a value with the previous one for a metric with a known
// good direction (a change under 5% is steady)
func judge(v, pv float64, unit string, higherBetter *bool) string {
	if higherBetter == nil {
		return "neutral"
	}
	var rel float64
	switch {
	case unit == "%":
		rel = (v - pv) / 100
	case pv != 0:
		rel = (v - pv) / math.Abs(pv)
	case v != 0:
		rel = 1
	}
	if !*higherBetter {
		rel = -rel
	}
	switch {
	case rel >= 0.05:
		return "good"
	case rel <= -0.25:
		return "bad"
	case rel <= -0.05:
		return "warn"
	}
	return "neutral"
}

// ---------------------------------------------------------------------
// Definitions: every metric can be traced back to how it is computed

type MetricDef struct {
	Key, Label, Definition string
}

var metricDefs = []MetricDef{
	{"wip", "Work in progress (WIP)", "Records in a tracked workflow (tasks, cases, approvals, operations records) that exist and have not reached a finished state (done, closed, approved or rejected) at the end of the period."},
	{"completed", "Completed", "Records that reached a successful finished state inside the period. Rejected approvals count as failed, not completed."},
	{"started", "New work", "Records created inside the period."},
	{"throughput", "Throughput", "Completed records divided by the number of days in the period."},
	{"cycle", "Cycle time", "Time between a record being created and reaching a finished state, for records completed in the period. Average, median, P90 and P95 are computed over those records."},
	{"cycle_median", "Median cycle time", "Half of the records completed in the period took less than this, half took longer."},
	{"cycle_p90", "P90 cycle time", "90% of the records completed in the period finished within this time."},
	{"cycle_p95", "P95 cycle time", "95% of the records completed in the period finished within this time."},
	{"sla", "SLA compliance", "Share of records completed in the period that met every SLA target configured for them: the workflow target (created to completed) and each stage target (time spent in that status). Records without any configured target are not counted."},
	{"breaches", "SLA breaches", "Records completed in the period after exceeding a target, plus open records that have already exceeded one."},
	{"blocked", "Blocked work", "Open records whose current status is a blocked or waiting-on-others state (task: Blocked or Waiting; case: Pending)."},
	{"rework", "Rework rate", "Share of records completed in the period that moved backward at least once: reopened after finishing, or returned to an earlier stage (for example Approved back to Pending, or Resolved back to Open)."},
	{"handoff", "Handoff delay", "Time between a change of owner (assignee) and the next step: the next status change, or the first action by the new owner. Median over handoffs in the period."},
	{"aging", "Aging work", "Open records created more than 7 days before the end of the period."},
	{"overdue", "Overdue work", "Open records past their due date."},
	{"completion", "Process completion rate", "Share of records created in the period that have already reached a successful finished state."},
	{"failure", "Workflow failure rate", "Share of records finished in the period that ended in a failed state (a rejected approval)."},
	{"open_cases", "Open cases", "Cases not resolved or closed at the end of the period."},
	{"open_tasks", "Open tasks", "Tasks not done at the end of the period."},
	{"pending_approvals", "Pending approvals", "Approvals not yet approved or rejected at the end of the period."},
	{"team", "Team activity", "Distinct people who changed a workspace record in the period, and the number of recorded changes."},
	{"stage_time", "Time in stage", "Time a record spent in one status before moving on. Measured on stage visits that ended in the period; a record returning to a status is a new visit."},
	{"stage_sla", "Stage SLA compliance", "Share of stage visits that ended in the period within that stage's target."},
	{"wait_share", "Waiting share", "Share of time spent in waiting or blocked states (queue time) rather than in working states."},
	{"handoff_share", "Share of process delay", "Total handoff waiting time divided by the total cycle time of records completed in the period."},
	{"severity", "Bottleneck severity", "A 0–100 ranking of stages built from measured components only: share of workflow time spent in the stage, share of open work sitting in it, aging, target breaches, rework re-entries and backlog growth. The components are always shown next to the score."},
	{"loop", "Rework loop", "A backward move and the time it took the record to regain the position it had lost (or finish). The loop time is the additional time the backward move cost."},
	{"evidence", "Evidence strength", "How much measured history supports a statement: sample size, days of history, how consistently the pattern held week to week, and how complete the records' histories are. High, Moderate or Low — never a probability."},
	{"coverage", "Data coverage", "How complete the recorded history is. Records that existed before activity tracking started have only a snapshot of their state, so their stage history is partial."},
}

func metricDef(key string) MetricDef {
	for _, d := range metricDefs {
		if d.Key == key {
			return d
		}
	}
	return MetricDef{Key: key}
}
