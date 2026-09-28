package saas

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// GoalMetric is a measurable quantity a goal or an intervention test tracks
type GoalMetric struct {
	Key          string
	Label        string
	Unit         string // h | % | n | /mo
	HigherBetter bool
	NeedsStage   bool
}

var goalMetrics = []GoalMetric{
	{"cycle_median", "Median cycle time", "h", false, false},
	{"cycle_avg", "Average cycle time", "h", false, false},
	{"sla", "SLA compliance", "%", true, false},
	{"rework", "Rework rate", "%", false, false},
	{"backlog", "Open work (backlog)", "n", false, false},
	{"throughput", "Completed per 30 days", "n", true, false},
	{"stage", "Median time in a stage", "h", false, true},
	{"wait", "Average waiting time per item", "h", false, false},
	{"handoff", "Median handoff delay", "h", false, false},
	{"breach", "Missed due dates", "%", false, false},
}

func goalMetric(key string) (GoalMetric, bool) {
	for _, m := range goalMetrics {
		if m.Key == key {
			return m, true
		}
	}
	// intervention tests created before the deeper metrics existed
	switch key {
	case "cycle":
		return GoalMetric{"cycle", "Average completion time", "h", false, false}, true
	}
	return GoalMetric{}, false
}

// MetricIn measures a goal/test metric over a window for one workflow
// ("" = all) and stage; it returns the value and the sample size
func (in *Intel) MetricIn(metric, module, stage string, w Window) (float64, int) {
	var vals []float64
	n, hits := 0, 0
	for _, it := range in.Items {
		if module != "" && it.Module != module {
			continue
		}
		switch metric {
		case "backlog":
			if it.OpenAt(w.To) {
				hits++
			}
			continue
		case "stage":
			for _, sg := range it.Segments {
				if sg.Status == stage && sg.End != nil && w.Has(*sg.End) {
					vals = append(vals, sg.End.Sub(sg.Start).Hours())
				}
			}
			continue
		case "handoff":
			for _, h := range it.Handoffs() {
				if w.Has(h.At) {
					vals = append(vals, h.Wait(in.Now).Hours())
				}
			}
			continue
		}

		if it.CompletedAt == nil || !w.Has(*it.CompletedAt) {
			continue
		}
		n++
		switch metric {
		case "cycle_median", "cycle_avg", "cycle":
			if !it.Failed {
				vals = append(vals, it.Cycle().Hours())
			}
		case "wait":
			vals = append(vals, it.Wait.Hours())
		case "rework":
			if !it.Failed {
				if len(it.Loops) > 0 {
					hits++
				}
			} else {
				n--
			}
		case "breach":
			if it.Breached {
				hits++
			}
		case "throughput":
			if !it.Failed {
				hits++
			}
		case "sla":
			c := in.sla(it)
			if !c.slaApplicable {
				n--
				continue
			}
			if c.breachAt == nil || c.breachAt.After(*it.CompletedAt) {
				hits++
			}
		}
	}

	switch metric {
	case "cycle_median", "stage", "handoff":
		return median(vals), len(vals)
	case "cycle_avg", "cycle", "wait":
		return mean(vals), len(vals)
	case "rework", "breach", "sla":
		if n <= 0 {
			return 0, 0
		}
		return float64(hits) / float64(n) * 100, n
	case "throughput":
		d := w.Days()
		if d <= 0 {
			return 0, 0
		}
		return float64(hits) / d * 30, n
	case "backlog":
		return float64(hits), hits
	}
	return 0, 0
}

// ---------------------------------------------------------------------
// Goals

type GoalView struct {
	*Goal
	M            GoalMetric
	Workflow     string
	Current      float64
	CurrentN     int
	Delta        string // versus baseline
	Gap          string // to target
	Progress     float64
	Series       []float64
	SeriesLabels []string
	Trend        string
	Projected    float64
	HasProject   bool
	Status       string // Achieved | On track | At risk | Off track | Collecting data
	Confidence   Evidence
	Drivers      []Driver
	Lenses       []Driver
	DriverNote   string
	Link         string
}

const goalWindowDays = 30

// EvaluateGoal measures a goal against the company's history
func (in *Intel) EvaluateGoal(g *Goal) GoalView {
	m, _ := goalMetric(g.Metric)
	v := GoalView{Goal: g, M: m, Workflow: "All workflows", Link: fmt.Sprintf("/command/goals/%d", g.ID)}
	if st := stageFor(g.Module); st != nil {
		v.Workflow = st.Label
	}
	now := in.Now
	if g.ClosedAt != nil && g.ClosedAt.Before(now) {
		now = *g.ClosedAt
	}
	v.Current, v.CurrentN = in.MetricIn(g.Metric, g.Module, g.Stage, Window{now.AddDate(0, 0, -goalWindowDays), now})

	better := func(a, b float64) bool {
		if m.HigherBetter {
			return a >= b
		}
		return a <= b
	}
	if g.Baseline != 0 {
		v.Delta, _ = deltaText(v.Current, g.Baseline, unitOf(m))
	}
	if span := g.Target - g.Baseline; span != 0 {
		v.Progress = math.Max(0, math.Min(100, (v.Current-g.Baseline)/span*100))
	}
	v.Gap = fmtMetric(math.Abs(g.Target-v.Current), m.Unit)

	// weekly trailing-30-day values since 8 weeks before the start
	start := g.StartAt.AddDate(0, 0, -56)
	var xs, ys []float64
	for t := start; !t.After(now); t = t.AddDate(0, 0, 7) {
		val, n := in.MetricIn(g.Metric, g.Module, g.Stage, Window{t.AddDate(0, 0, -goalWindowDays), t})
		if n < 3 && g.Metric != "backlog" {
			v.Series = append(v.Series, math.NaN())
		} else {
			v.Series = append(v.Series, val)
			if !t.Before(g.StartAt) {
				xs = append(xs, t.Sub(g.StartAt).Hours()/24)
				ys = append(ys, val)
			}
		}
		v.SeriesLabels = append(v.SeriesLabels, t.Format("Jan 2"))
	}
	v.Trend = trendDir(v.Series, m.HigherBetter)

	slope, intercept, r2 := linreg(xs, ys)
	weeks := len(xs)
	if weeks >= 3 && g.TargetAt != nil {
		v.HasProject = true
		v.Projected = intercept + slope*g.TargetAt.Sub(g.StartAt).Hours()/24
		if m.Unit == "%" {
			v.Projected = math.Max(0, math.Min(100, v.Projected))
		}
	}

	switch {
	case v.CurrentN == 0 && g.Metric != "backlog":
		v.Status = "Collecting data"
	case better(v.Current, g.Target):
		v.Status = "Achieved"
	case weeks < 3:
		v.Status = "Collecting data"
	case v.HasProject && better(v.Projected, g.Target):
		v.Status = "On track"
	case (m.HigherBetter && slope > 0) || (!m.HigherBetter && slope < 0):
		v.Status = "At risk"
	default:
		v.Status = "Off track"
	}

	// confidence in the projection: history length, fit and sample size
	v.Confidence = Evidence{Level: "Low"}
	v.Confidence.Reasons = []string{
		fmt.Sprintf("%s since the goal started", plural(weeks, "weekly measurement", "weekly measurements")),
		fmt.Sprintf("%d records in the latest 30-day window", v.CurrentN),
	}
	if weeks >= 3 {
		v.Confidence.Reasons = append(v.Confidence.Reasons, fmt.Sprintf("trend fit R² %.2f", r2))
	}
	switch {
	case weeks >= 8 && r2 >= 0.5 && v.CurrentN >= 50:
		v.Confidence.Level = "High"
	case weeks >= 4 && r2 >= 0.25 && v.CurrentN >= 20:
		v.Confidence.Level = "Moderate"
	}

	// observed drivers: baseline window versus the latest window
	base := Window{g.StartAt.AddDate(0, 0, -goalWindowDays), g.StartAt}
	curW := Window{now.AddDate(0, 0, -goalWindowDays), now}
	switch g.Metric {
	case "cycle_median", "cycle_avg", "wait", "stage":
		why := in.WhyCycle(curW, base)
		if why.Enough {
			v.Drivers, v.Lenses = why.Drivers, why.Lenses
			v.DriverNote = fmt.Sprintf("Average time per completed item by stage, %d items now versus %d at the start.", why.N, why.PrevN)
		}
	default:
		v.Drivers = in.groupDrivers(g.Metric, g.Module, curW, base)
		v.DriverNote = "Change in the metric by workflow, department and team between the goal's baseline window and the latest 30 days."
	}
	if len(v.Drivers) > 8 {
		v.Drivers = v.Drivers[:8]
	}
	return v
}

// groupDrivers compares a metric per workflow, department and team
func (in *Intel) groupDrivers(metric, module string, cur, base Window) []Driver {
	var out []Driver
	for _, dim := range []string{"workflow", "department", "team"} {
		groups := map[string][]*WorkItem{}
		for _, it := range in.Items {
			if module != "" && it.Module != module {
				continue
			}
			var k string
			switch dim {
			case "workflow":
				k = it.stage.Label
			case "department":
				k = in.Lk.department(it.Department)
			default:
				k = in.Lk.team(it.Team)
			}
			groups[k] = append(groups[k], it)
		}
		if len(groups) < 2 {
			continue
		}
		for k, items := range groups {
			sub := *in
			sub.Items = items
			a, an := sub.MetricIn(metric, "", "", cur)
			b, bn := sub.MetricIn(metric, "", "", base)
			if an < 3 || bn < 3 || math.Abs(a-b) < 1e-9 {
				continue
			}
			out = append(out, Driver{Name: dim + ": " + k, DeltaH: a - b, Cur: a, Prev: b})
		}
	}
	sortDrivers(out)
	return out
}

func sortDrivers(dd []Driver) {
	for i := 1; i < len(dd); i++ {
		for j := i; j > 0 && math.Abs(dd[j].DeltaH) > math.Abs(dd[j-1].DeltaH); j-- {
			dd[j], dd[j-1] = dd[j-1], dd[j]
		}
	}
}

func unitOf(m GoalMetric) string {
	if m.Unit == "/mo" {
		return "n"
	}
	return m.Unit
}

func fmtMetric(v float64, unit string) string {
	switch unit {
	case "h":
		return fmtHours(v)
	case "%":
		return fmt.Sprintf("%.1f%%", v)
	}
	return fmt.Sprintf("%.0f", v)
}

// linreg fits y = a + b·x
func linreg(xs, ys []float64) (slope, intercept, r2 float64) {
	n := float64(len(xs))
	if n < 2 {
		return 0, mean(ys), 0
	}
	mx, my := mean(xs), mean(ys)
	var sxy, sxx, syy float64
	for i := range xs {
		sxy += (xs[i] - mx) * (ys[i] - my)
		sxx += (xs[i] - mx) * (xs[i] - mx)
		syy += (ys[i] - my) * (ys[i] - my)
	}
	if sxx == 0 {
		return 0, my, 0
	}
	slope = sxy / sxx
	intercept = my - slope*mx
	if syy > 0 {
		r2 = sxy * sxy / (sxx * syy)
	}
	return
}

// ---------------------------------------------------------------------
// Intervention tests

type TestView struct {
	*Intervention
	M                   GoalMetric
	Workflow            string
	Owner               string
	Before, After       float64
	BeforeN, AfterN     int
	BeforeMed, AfterMed float64
	HasMedian           bool
	Delta               string
	DeltaPct            float64
	Dir                 string
	Days                int
	EvalEnd             time.Time
	BaselineFrom        time.Time
	Status              string // Measuring | Evaluation complete | Ended
	Verdict             string // Collecting data | Associated improvement | Associated worsening | No clear change
	Strength            Evidence
	Series              []float64
	StartIndex          int
	Link                string
}

// EvaluateTest compares the baseline window before a test with the
// evaluation window after it started
func (in *Intel) EvaluateTest(iv *Intervention) TestView {
	metric := iv.Metric
	m, ok := goalMetric(metric)
	if !ok {
		m = GoalMetric{Key: metric, Label: metric, Unit: "n"}
	}
	v := TestView{Intervention: iv, M: m, Workflow: "All workflows", Owner: in.Lk.person(iv.OwnerID), Link: fmt.Sprintf("/command/tests/%d", iv.ID)}
	if iv.OwnerID == 0 {
		v.Owner = in.Lk.person(iv.CreatedBy)
	}
	if st := stageFor(iv.Module); st != nil {
		v.Workflow = st.Label
	}
	bd, ed := iv.BaselineDays, iv.EvalDays
	if bd <= 0 {
		bd = 28
	}
	if ed <= 0 {
		ed = 28
	}
	v.BaselineFrom = iv.StartedAt.AddDate(0, 0, -bd)
	v.EvalEnd = iv.StartedAt.AddDate(0, 0, ed)
	end := minTime(v.EvalEnd, in.Now)
	if iv.EndedAt != nil && iv.EndedAt.Before(end) {
		end = *iv.EndedAt
	}
	switch {
	case iv.EndedAt != nil:
		v.Status = "Ended"
	case !in.Now.Before(v.EvalEnd):
		v.Status = "Evaluation complete"
	default:
		v.Status = "Measuring"
	}
	v.Days = int(end.Sub(iv.StartedAt).Hours() / 24)

	before := Window{v.BaselineFrom, iv.StartedAt}
	after := Window{iv.StartedAt, end}
	v.Before, v.BeforeN = in.MetricIn(metric, iv.Module, iv.Stage, before)
	v.After, v.AfterN = in.MetricIn(metric, iv.Module, iv.Stage, after)
	if m.Unit == "h" && metric != "cycle_median" && metric != "stage" && metric != "handoff" {
		v.HasMedian = true
		v.BeforeMed, _ = in.MetricIn("cycle_median", iv.Module, iv.Stage, before)
		v.AfterMed, _ = in.MetricIn("cycle_median", iv.Module, iv.Stage, after)
		if metric == "wait" {
			v.HasMedian = false
		}
	}
	if v.Before != 0 {
		v.DeltaPct = (v.After - v.Before) / math.Abs(v.Before) * 100
	}
	v.Delta, v.Dir = deltaText(v.After, v.Before, unitOf(m))

	// weekly series across both windows
	weeks := 0
	for t := v.BaselineFrom; t.Before(end); t = t.AddDate(0, 0, 7) {
		we := minTime(t.AddDate(0, 0, 7), end)
		if !t.Before(iv.StartedAt) && v.StartIndex == 0 {
			v.StartIndex = len(v.Series)
		}
		val, n := in.MetricIn(metric, iv.Module, iv.Stage, Window{t, we})
		if n < 2 && metric != "backlog" {
			v.Series = append(v.Series, math.NaN())
		} else {
			v.Series = append(v.Series, val)
		}
		weeks++
	}

	// consistency: after-weeks better than the baseline value
	cons, total := 0, 0
	for i := v.StartIndex; i < len(v.Series); i++ {
		x := v.Series[i]
		if math.IsNaN(x) {
			continue
		}
		total++
		if (m.HigherBetter && x > v.Before) || (!m.HigherBetter && x < v.Before) {
			cons++
		}
	}
	n := v.BeforeN
	if v.AfterN < n {
		n = v.AfterN
	}
	v.Strength = evidenceOf(n, float64(bd+v.Days), cons, total, in.partialShare())
	v.Strength.Reasons = append([]string{fmt.Sprintf("%d records before, %d after", v.BeforeN, v.AfterN)}, v.Strength.Reasons[1:]...)

	improved := (m.HigherBetter && v.After > v.Before) || (!m.HigherBetter && v.After < v.Before)
	switch {
	case v.Days < 7 || v.AfterN < 5 || v.BeforeN < 5:
		v.Verdict = "Collecting data"
	case math.Abs(v.DeltaPct) < 10 && m.Unit != "%":
		v.Verdict = "No clear change"
	case m.Unit == "%" && math.Abs(v.After-v.Before) < 3:
		v.Verdict = "No clear change"
	case improved:
		v.Verdict = "Associated improvement"
	default:
		v.Verdict = "Associated worsening"
	}
	return v
}

// VerdictNote words the verdict without claiming causation
func (v TestView) VerdictNote() string {
	switch v.Verdict {
	case "Associated improvement":
		return fmt.Sprintf("Observed change: %s moved from %s to %s after the change started. This is an association over the measured windows; other changes in the same period may also contribute.",
			strings.ToLower(v.M.Label), fmtMetric(v.Before, v.M.Unit), fmtMetric(v.After, v.M.Unit))
	case "Associated worsening":
		return fmt.Sprintf("Observed change: %s moved from %s to %s after the change started — in the unfavorable direction. Review what else changed in the period before drawing conclusions.",
			strings.ToLower(v.M.Label), fmtMetric(v.Before, v.M.Unit), fmtMetric(v.After, v.M.Unit))
	case "No clear change":
		return "The difference between the windows is small relative to normal variation."
	}
	return "Results appear once both windows hold at least 5 records and the test has run for a week."
}
