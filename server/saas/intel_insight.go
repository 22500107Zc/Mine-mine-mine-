package saas

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ---------------------------------------------------------------------
// Evidence strength: never a probability, always explained

type Evidence struct {
	Level   string // High | Moderate | Low
	Reasons []string
}

// evidenceOf grades how much measured history supports a statement
func evidenceOf(n int, days float64, consistent, weeks int, partialShare float64) Evidence {
	e := Evidence{Level: "Low"}
	e.Reasons = append(e.Reasons, plural(n, "measured record", "measured records"))
	e.Reasons = append(e.Reasons, fmt.Sprintf("%.0f days of history", days))
	if weeks > 0 {
		e.Reasons = append(e.Reasons, fmt.Sprintf("pattern held in %d of %d weeks", consistent, weeks))
	}
	if partialShare > 0.005 {
		e.Reasons = append(e.Reasons, fmt.Sprintf("%.0f%% of records have partial history", partialShare*100))
	}
	ratio := 1.0
	if weeks > 0 {
		ratio = float64(consistent) / float64(weeks)
	}
	switch {
	case n >= 100 && days >= 56 && ratio >= 0.7 && partialShare <= 0.2:
		e.Level = "High"
	case n >= 30 && days >= 21 && ratio >= 0.5:
		e.Level = "Moderate"
	}
	return e
}

func (in *Intel) partialShare() float64 {
	if len(in.Items) == 0 {
		return 0
	}
	n := 0
	for _, it := range in.Items {
		if it.Partial {
			n++
		}
	}
	return float64(n) / float64(len(in.Items))
}

// ---------------------------------------------------------------------
// Recommendations

type EvLine struct {
	Text string
	Link string
}

type Rec struct {
	Key      string
	Priority string // High | Medium | Low
	Issue    string
	Evidence []EvLine
	Metrics  []string
	Process  string
	Module   string
	Stage    string
	Records  string // link to affected records
	RecordsN int
	Window   string
	Action   string
	Expected string
	Strength Evidence
	Why      string
	Metric   string // intervention metric for "Start test"
	score    float64
}

// Recommendations derives specific, evidence-backed actions from the scope
func (in *Intel) Recommendations() []Rec {
	var out []Rec
	stages := in.Stages()
	cur := in.Measure(in.W)
	partial := in.partialShare()
	win := in.Scope.Label()

	// 1. the primary bottleneck
	if b := in.TopBottleneck(stages); b != nil && b.Severity >= 15 && b.Exited+b.WIP >= 10 {
		r := Rec{Key: "bottleneck-" + b.Module + "-" + b.Status, Process: b.Label, Module: b.Module, Stage: b.Status, Window: win, Metric: "stage"}
		r.Issue = fmt.Sprintf("%s · %s is the primary bottleneck", b.Label, b.Status)
		r.Evidence = append(r.Evidence, EvLine{Text: fmt.Sprintf("%.0f%% of %s workflow time is spent in %s.", b.TimeShare, lower1(b.Label), b.Status)})
		if b.Time.N > 0 {
			line := fmt.Sprintf("Median time in stage %s, P95 %s across %s.", fmtHours(b.Time.Median), fmtHours(b.Time.P95), plural(b.Time.N, "visit", "visits"))
			if b.HasTarget {
				line = fmt.Sprintf("P95 time in stage is %s against a %s target.", fmtHours(b.Time.P95), fmtHours(b.Target))
			}
			r.Evidence = append(r.Evidence, EvLine{Text: line})
		}
		if b.HasTarget && b.Breaches > 0 {
			r.Evidence = append(r.Evidence, EvLine{Text: fmt.Sprintf("%d of %d stage visits breached the target this period.", b.Breaches, b.Exited+b.WIP),
				Link: in.Scope.URL("/command/records", "set", "sla_breached", "workflow", b.Module, "stage", b.Status)})
		}
		if b.WIP > 0 {
			r.Evidence = append(r.Evidence, EvLine{Text: fmt.Sprintf("%d items are in %s now; %d of them are aging.", b.WIP, b.Status, b.Aging),
				Link: in.Scope.URL("/command/records", "set", "in_stage", "workflow", b.Module, "stage", b.Status)})
		}
		hs := in.handoffShareInto(b.Module, b.Status)
		if hs.n >= 5 {
			r.Evidence = append(r.Evidence, EvLine{Text: fmt.Sprintf("%.0f%% of the slow visits (over the stage median) began with a change of owner.", hs.share)})
		}
		h1, h2, net := in.accumulationHours(b.Module, b.Status)
		action := fmt.Sprintf("Review staffing and routing for %s %s", lower1(b.Label), b.Status)
		if net > 0 {
			action += fmt.Sprintf(" between %02d:00 and %02d:00 UTC, when arrivals outpace exits most (net +%d over the period)", h1, h2, net)
			r.Evidence = append(r.Evidence, EvLine{Text: fmt.Sprintf("Backlog accumulates most between %02d:00 and %02d:00 UTC (arrivals minus exits: +%d).", h1, h2, net)})
		}
		r.Action = action + "."
		if b.Owner != "" && b.OwnerShare >= 50 {
			r.Evidence = append(r.Evidence, EvLine{Text: fmt.Sprintf("%.0f%% of the time in this stage sat with one owner (%s).", b.OwnerShare, b.Owner)})
			r.Action += " Consider a second owner or a shared queue for this stage."
		}
		r.Expected = "Lower median and P95 time in " + b.Status + ", and fewer breaches."
		r.Metrics = []string{"Time in " + b.Status, "Stage SLA compliance", "Cycle time"}
		r.Records = in.Scope.URL("/command/records", "set", "in_stage", "workflow", b.Module, "stage", b.Status)
		r.RecordsN = b.Exited + b.WIP
		cons, weeks := consistencyOf(b.Trend, b.Time.Median, true)
		r.Strength = evidenceOf(b.Exited+b.WIP, in.W.Days(), cons, weeks, partial)
		r.Why = fmt.Sprintf("Its severity score (%.0f, %s) is the highest of all stages. Components: %s.", b.Severity, b.Band, componentsText(b.Components))
		r.score = b.Severity
		out = append(out, r)
	}

	// 2. rework loops
	rw := in.Rework()
	if len(rw.Paths) > 0 && rw.Paths[0].Count >= 5 {
		p := rw.Paths[0]
		r := Rec{Key: "loop-" + p.Module + "-" + slug(p.Path), Process: p.Label, Module: p.Module, Window: win, Metric: "rework"}
		r.Issue = fmt.Sprintf("Rework loop in %s: %s", lower1(p.Label), p.Path)
		r.Evidence = []EvLine{
			{Text: fmt.Sprintf("%d occurrences across %s.", p.Count, plural(p.Items, "record", "records")), Link: p.Link},
			{Text: fmt.Sprintf("Median additional time per loop: %s.", fmtHours(p.Extra.Median))},
		}
		if p.ShareOfDelay > 0 {
			r.Evidence = append(r.Evidence, EvLine{Text: fmt.Sprintf("Loops of this kind account for %.0f%% of total cycle time of work completed in the period.", p.ShareOfDelay)})
		}
		r.Evidence = append(r.Evidence, EvLine{Text: fmt.Sprintf("Overall rework rate %.1f%% (%d of %d completed).", rw.Rate, rw.ReworkItems, rw.Completed)})
		parts := strings.Split(p.Path, " → ")
		r.Action = fmt.Sprintf("Investigate why %s returns from %s to %s: add a completeness check before %s and review the reasons recorded on the returned items.",
			lower1(p.Label), parts[0], parts[min(1, len(parts)-1)], parts[0])
		r.Expected = "Fewer backward moves and a shorter cycle for the affected records."
		r.Metrics = []string{"Rework rate", "Time lost to rework", "Cycle time"}
		r.Records, r.RecordsN = p.Link, p.Items
		r.Strength = evidenceOf(p.Count, in.W.Days(), 0, 0, partial)
		r.Why = "Backward moves are measured directly from status history; each loop's cost is the time until the record regained its position."
		r.score = math.Min(60, p.ShareOfDelay*2+float64(p.Count)/2)
		out = append(out, r)
	}

	// 3. handoff delay
	hv := in.Handoffs("team")
	if len(hv.Rows) > 0 && hv.Rows[0].Count >= 5 && hv.Rows[0].Wait.Median >= 2 {
		h := hv.Rows[0]
		r := Rec{Key: "handoff-" + strconv.FormatUint(h.FromID, 10) + "-" + strconv.FormatUint(h.ToID, 10), Window: win, Metric: "handoff"}
		r.Issue = fmt.Sprintf("Handoffs from %s to %s are slow to be picked up", h.From, h.To)
		r.Evidence = []EvLine{
			{Text: fmt.Sprintf("%d handoffs; median wait %s, P90 %s.", h.Count, fmtHours(h.Wait.Median), fmtHours(h.Wait.P90)), Link: h.Link},
		}
		if h.ShareOfDelay >= 1 {
			r.Evidence = append(r.Evidence, EvLine{Text: fmt.Sprintf("Waiting after these handoffs equals %.0f%% of the total cycle time of completed work.", h.ShareOfDelay)})
		}
		if h.BreachRate > h.BaseBreachRate+5 {
			r.Evidence = append(r.Evidence, EvLine{Text: fmt.Sprintf("%.0f%% of handed-off records breached an SLA, versus %.0f%% of records without a handoff.", h.BreachRate, h.BaseBreachRate)})
		}
		r.Action = fmt.Sprintf("Agree a pickup expectation for work arriving at %s from %s and make new arrivals visible (a shared queue or notification).", h.To, h.From)
		r.Expected = "Shorter wait between handoff and the next step."
		r.Metrics = []string{"Handoff delay", "Cycle time"}
		r.Records, r.RecordsN = h.Link, h.Count
		r.Strength = evidenceOf(h.Count, in.W.Days(), 0, 0, partial)
		r.Why = "Waiting time is measured from the change of owner until the next status change or the new owner's first action."
		r.score = math.Min(55, h.ShareOfDelay*2+h.Wait.Median/2)
		out = append(out, r)
	}

	// 4. aging risk
	ag := in.Aging("age", "stage")
	risky := 0
	for _, a := range ag.AtRisk {
		if a.Level == "Past target" || a.Level == "High" {
			risky++
		}
	}
	if risky >= 3 {
		r := Rec{Key: "aging", Window: win, Metric: "cycle"}
		r.Issue = fmt.Sprintf("%d open items are older than 90%% of comparable completed work (or past target)", risky)
		r.Evidence = []EvLine{{Text: fmt.Sprintf("%d open items in total; %d aging more than 7 days.", ag.Total, cur.Aging),
			Link: in.Scope.URL("/command/aging")}}
		r.Action = "Triage the at-risk list: confirm each item is still needed, unblock or reassign it, and close what is no longer relevant."
		r.Expected = "Lower aging and fewer SLA breaches over the next period."
		r.Metrics = []string{"Aging work", "SLA breaches"}
		r.Records, r.RecordsN = in.Scope.URL("/command/aging"), risky
		r.Strength = evidenceOf(ag.Total, in.W.Days(), 0, 0, partial)
		r.Why = "Each item's age is compared with the cycle times of completed items of the same workflow."
		r.score = math.Min(40, float64(risky)*2)
		out = append(out, r)
	}

	// 5. growing backlog
	prev := in.Measure(in.Prev)
	if cur.Started >= 10 && cur.Started > cur.Completed && prev.WIP > 0 && float64(cur.WIP) > float64(prev.WIP)*1.15 {
		r := Rec{Key: "backlog", Window: win, Metric: "throughput"}
		r.Issue = fmt.Sprintf("Backlog grew %.0f%% this period", (float64(cur.WIP)-float64(prev.WIP))/float64(prev.WIP)*100)
		r.Evidence = []EvLine{
			{Text: fmt.Sprintf("%d new items versus %d completed.", cur.Started, cur.Completed), Link: in.Scope.URL("/command/throughput")},
			{Text: fmt.Sprintf("Open work went from %d to %d.", prev.WIP, cur.WIP)},
		}
		r.Action = "Compare intake with capacity by team on the Capacity view and decide what to defer, reassign or stop accepting."
		r.Expected = "Completions catching up with intake."
		r.Metrics = []string{"Throughput", "Work in progress"}
		r.Records, r.RecordsN = in.Scope.URL("/command/records", "set", "wip"), cur.WIP
		r.Strength = evidenceOf(cur.Started+cur.Completed, in.W.Days(), 0, 0, partial)
		r.Why = "Measured intake and completions over the same period."
		r.score = 25
		out = append(out, r)
	}

	// 6. concentrated load
	cp := in.Capacity()
	if cp.TopShare >= 50 && cp.TotalActive >= 20 {
		r := Rec{Key: "load", Window: win, Metric: "cycle"}
		r.Issue = fmt.Sprintf("%d people hold %.0f%% of assigned open work", cp.TopCount, cp.TopShare)
		r.Evidence = []EvLine{{Text: fmt.Sprintf("%d open items across %d assignees.", cp.TotalActive, len(cp.People)), Link: in.Scope.URL("/command/capacity")}}
		r.Action = "Review whether the distribution reflects roles and availability; rebalance new assignments if it does not."
		r.Expected = "More even load and less queueing behind a few people."
		r.Metrics = []string{"Work in progress", "Handoff delay"}
		r.Records, r.RecordsN = in.Scope.URL("/command/capacity"), cp.TotalActive
		r.Strength = evidenceOf(cp.TotalActive, in.W.Days(), 0, 0, partial)
		r.Why = "Measured from current assignments only; it does not account for part-time schedules or roles."
		r.score = 20
		out = append(out, r)
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].score > out[j].score })
	for i := range out {
		switch {
		case out[i].score >= 35 && out[i].Strength.Level != "Low":
			out[i].Priority = "High"
		case out[i].score >= 18:
			out[i].Priority = "Medium"
		default:
			out[i].Priority = "Low"
		}
	}
	return out
}

func componentsText(cc []SevComponent) string {
	var parts []string
	for _, c := range cc {
		parts = append(parts, c.Name+" "+c.Value)
	}
	if len(parts) == 0 {
		return "none above noise"
	}
	return strings.Join(parts, ", ")
}

func slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			if b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
				b.WriteByte('-')
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// consistencyOf counts how many weeks the stage stayed at or above its
// median (i.e. the problem persisted)
func consistencyOf(series []float64, ref float64, higherIsProblem bool) (int, int) {
	n, ok := 0, 0
	for _, v := range series {
		if math.IsNaN(v) {
			continue
		}
		n++
		if (higherIsProblem && v >= ref*0.9) || (!higherIsProblem && v <= ref*1.1) {
			ok++
		}
	}
	return ok, n
}

type shareN struct {
	share float64
	n     int
}

// handoffShareInto: of slow visits to a stage, how many began with a change of owner
func (in *Intel) handoffShareInto(module, status string) shareN {
	var durs []float64
	type visit struct {
		d       float64
		handoff bool
	}
	var vv []visit
	for _, it := range in.Items {
		if it.Module != module {
			continue
		}
		for _, sg := range it.Segments {
			if sg.Status != status || sg.End == nil || !in.W.Has(*sg.End) {
				continue
			}
			d := sg.End.Sub(sg.Start).Hours()
			h := false
			for _, a := range it.Handoffs() {
				if !a.At.Before(sg.Start.Add(-time.Minute)) && a.At.Before(*sg.End) {
					h = true
				}
			}
			durs = append(durs, d)
			vv = append(vv, visit{d, h})
		}
	}
	med := median(durs)
	slow, withH := 0, 0
	for _, v := range vv {
		if v.d > med {
			slow++
			if v.handoff {
				withH++
			}
		}
	}
	if slow == 0 {
		return shareN{}
	}
	return shareN{float64(withH) / float64(slow) * 100, slow}
}

// accumulationHours finds the 4-hour block (UTC) where arrivals into a stage
// most exceed exits
func (in *Intel) accumulationHours(module, status string) (int, int, int) {
	var net [24]int
	for _, it := range in.Items {
		if it.Module != module {
			continue
		}
		for _, sg := range it.Segments {
			if sg.Status != status {
				continue
			}
			if in.W.Has(sg.Start) {
				net[sg.Start.UTC().Hour()]++
			}
			if sg.End != nil && in.W.Has(*sg.End) {
				net[sg.End.UTC().Hour()]--
			}
		}
	}
	best, bestH := math.MinInt, 0
	for h := 0; h < 24; h++ {
		s := 0
		for k := 0; k < 4; k++ {
			s += net[(h+k)%24]
		}
		if s > best {
			best, bestH = s, h
		}
	}
	return bestH, (bestH + 4) % 24, best
}

// ---------------------------------------------------------------------
// What changed / Why

type Change struct {
	Label     string
	Dimension string
	Cur, Prev string
	Delta     string
	Dir       string
	Good      bool
	Neutral   bool
	Link      string
	magnitude float64
}

type changeMetric struct {
	name         string
	unit         string
	higherBetter *bool
	measure      func(items []*WorkItem, w Window) (float64, int)
	set          string
}

// WhatChanged surfaces meaningful changes between the window and the previous one
func (in *Intel) WhatChanged() []Change {
	type dim struct {
		name  string
		items []*WorkItem
		kv    []string
	}
	dims := []dim{{name: "All work", items: in.Items}}
	groups := map[string]*dim{}
	add := func(key, name string, it *WorkItem, kv ...string) {
		d := groups[key]
		if d == nil {
			d = &dim{name: name, kv: kv}
			groups[key] = d
		}
		d.items = append(d.items, it)
	}
	for _, it := range in.Items {
		add("m"+it.Module, it.stage.Label, it, "workflow", it.Module)
		if it.Department > 0 {
			add("d"+strconv.FormatUint(it.Department, 10), in.Lk.department(it.Department), it, "department", strconv.FormatUint(it.Department, 10))
		}
		if it.Team > 0 {
			add("t"+strconv.FormatUint(it.Team, 10), in.Lk.team(it.Team), it, "team", strconv.FormatUint(it.Team, 10))
		}
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		dims = append(dims, *groups[k])
	}

	metrics := []changeMetric{
		{"throughput", "n", higher, func(ii []*WorkItem, w Window) (float64, int) {
			n := 0
			for _, it := range ii {
				if it.Done() && w.Has(*it.CompletedAt) {
					n++
				}
			}
			return float64(n), n
		}, "completed"},
		{"median cycle time", "h", lower, func(ii []*WorkItem, w Window) (float64, int) {
			var v []float64
			for _, it := range ii {
				if it.Done() && w.Has(*it.CompletedAt) {
					v = append(v, it.Cycle().Hours())
				}
			}
			return median(v), len(v)
		}, "completed"},
		{"backlog", "n", lower, func(ii []*WorkItem, w Window) (float64, int) {
			n := 0
			for _, it := range ii {
				if it.OpenAt(w.To) {
					n++
				}
			}
			return float64(n), n
		}, "wip"},
		{"SLA breaches", "n", lower, func(ii []*WorkItem, w Window) (float64, int) {
			n, a := 0, 0
			for _, it := range ii {
				if it.CompletedAt != nil && w.Has(*it.CompletedAt) {
					c := in.sla(it)
					if c.slaApplicable {
						a++
						if c.breachAt != nil && !c.breachAt.After(*it.CompletedAt) {
							n++
						}
					}
				}
			}
			return float64(n), a
		}, "sla_breached"},
		{"rework rate", "%", lower, func(ii []*WorkItem, w Window) (float64, int) {
			n, r := 0, 0
			for _, it := range ii {
				if it.Done() && w.Has(*it.CompletedAt) {
					n++
					if len(it.Loops) > 0 {
						r++
					}
				}
			}
			if n == 0 {
				return 0, 0
			}
			return float64(r) / float64(n) * 100, n
		}, "rework"},
	}

	var out []Change
	for _, d := range dims {
		for _, m := range metrics {
			cv, cn := m.measure(d.items, in.W)
			pv, pn := m.measure(d.items, in.Prev)
			var rel float64
			switch m.unit {
			case "%":
				if cn < 10 || pn < 10 || math.Abs(cv-pv) < 5 {
					continue
				}
				rel = (cv - pv) / 100
			case "h":
				if cn < 5 || pn < 5 || pv == 0 {
					continue
				}
				rel = (cv - pv) / pv
			default:
				if (cv < 5 && pv < 5) || math.Abs(cv-pv) < 3 {
					continue
				}
				if pv == 0 {
					rel = 1
				} else {
					rel = (cv - pv) / pv
				}
			}
			if math.Abs(rel) < 0.15 {
				continue
			}
			delta, dir := deltaText(cv, pv, m.unit)
			if m.unit == "n" && pv > 0 {
				delta = fmt.Sprintf("%+d (%s)", int(cv-pv), delta)
			}
			good := (rel > 0) == *m.higherBetter
			label := d.name + " " + m.name
			if d.name == "All work" {
				label = strings.ToUpper(m.name[:1]) + m.name[1:]
			}
			kv := append([]string{"set", m.set}, d.kv...)
			out = append(out, Change{Label: label, Dimension: d.name, Cur: fmtValue(cv, m.unit), Prev: fmtValue(pv, m.unit),
				Delta: delta, Dir: dir, Good: good, Link: in.Scope.URL("/command/records", kv...),
				magnitude: math.Abs(rel) * math.Sqrt(float64(cn+pn))})
		}
	}

	// stage times
	for _, ps := range in.Stages() {
		prevIn := *in
		prevIn.W = in.Prev
		pp := prevIn.StagesOf(ps.Module)
		for _, r := range ps.Rows {
			if r.Terminal {
				continue
			}
			pr := pp.Row(r.Status)
			if pr == nil || r.Time.N < 5 || pr.Time.N < 5 || pr.Time.Median == 0 {
				continue
			}
			rel := (r.Time.Median - pr.Time.Median) / pr.Time.Median
			if math.Abs(rel) < 0.15 {
				continue
			}
			delta, dir := deltaText(r.Time.Median, pr.Time.Median, "h")
			out = append(out, Change{Label: fmt.Sprintf("%s · %s stage time", ps.Label, r.Status), Dimension: ps.Label,
				Cur: fmtHours(r.Time.Median), Prev: fmtHours(pr.Time.Median), Delta: delta, Dir: dir, Good: rel < 0,
				Link:      in.Scope.URL("/command/stage", "workflow", ps.Module, "stage", r.Status),
				magnitude: math.Abs(rel) * math.Sqrt(float64(r.Time.N+pr.Time.N))})
			if pr.WIP >= 5 || r.WIP >= 5 {
				if pr.WIP > 0 {
					br := float64(r.WIP-pr.WIP) / float64(pr.WIP)
					if math.Abs(br) >= 0.2 && absInt(r.WIP-pr.WIP) >= 3 {
						d2, dir2 := deltaText(float64(r.WIP), float64(pr.WIP), "n")
						out = append(out, Change{Label: fmt.Sprintf("%s · %s backlog", ps.Label, r.Status), Dimension: ps.Label,
							Cur: strconv.Itoa(r.WIP), Prev: strconv.Itoa(pr.WIP), Delta: d2, Dir: dir2, Good: br < 0,
							Link:      in.Scope.URL("/command/records", "set", "in_stage", "workflow", ps.Module, "stage", r.Status),
							magnitude: math.Abs(br) * math.Sqrt(float64(r.WIP+pr.WIP))})
					}
				}
			}
		}
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].magnitude > out[j].magnitude })
	if len(out) > 30 {
		out = out[:30]
	}
	return out
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// Driver is one observed contribution to a change
type Driver struct {
	Name   string
	DeltaH float64
	Cur    float64
	Prev   float64
	Link   string
	Lens   bool // an overlapping view (not part of the stage total)
}

type WhyView struct {
	Metric     string
	Cur, Prev  float64
	N, PrevN   int
	Drivers    []Driver
	Lenses     []Driver
	Throughput []DimChange
	Enough     bool
}

// WhyCycle decomposes the change in average cycle time between two windows
// into time per stage (these sum to the total change), plus overlapping
// lenses: handoff waiting, rework loops and weekend waiting
func (in *Intel) WhyCycle(cur, prev Window) WhyView {
	v := WhyView{Metric: "Average cycle time"}
	type side struct {
		n       int
		cycle   float64
		stage   map[string]float64
		handoff float64
		loops   float64
		weekend float64
	}
	collect := func(w Window) side {
		s := side{stage: map[string]float64{}}
		for _, it := range in.Items {
			if !it.Done() || !w.Has(*it.CompletedAt) {
				continue
			}
			s.n++
			s.cycle += it.Cycle().Hours()
			for _, sg := range it.Segments {
				if sg.Kind == "done" || sg.Kind == "failed" {
					continue
				}
				d := sg.Dur(in.Now).Hours()
				s.stage[it.stage.Label+" · "+sg.Status] += d
				if sg.Kind == "wait" || sg.Kind == "blocked" {
					s.weekend += weekendHours(sg.Start, *sg.End)
				}
			}
			for _, h := range it.Handoffs() {
				s.handoff += h.Wait(in.Now).Hours()
			}
			s.loops += it.ReworkTime(in.Now).Hours()
		}
		return s
	}
	a, b := collect(cur), collect(prev)
	v.N, v.PrevN = a.n, b.n
	if a.n < 5 || b.n < 5 {
		return v
	}
	v.Enough = true
	v.Cur, v.Prev = a.cycle/float64(a.n), b.cycle/float64(b.n)
	names := map[string]bool{}
	for k := range a.stage {
		names[k] = true
	}
	for k := range b.stage {
		names[k] = true
	}
	for k := range names {
		ca, cb := a.stage[k]/float64(a.n), b.stage[k]/float64(b.n)
		if math.Abs(ca-cb) < 0.05 {
			continue
		}
		v.Drivers = append(v.Drivers, Driver{Name: k, DeltaH: ca - cb, Cur: ca, Prev: cb})
	}
	sort.Slice(v.Drivers, func(i, j int) bool { return math.Abs(v.Drivers[i].DeltaH) > math.Abs(v.Drivers[j].DeltaH) })
	lens := func(name string, x, y float64) {
		ca, cb := x/float64(a.n), y/float64(b.n)
		if math.Abs(ca-cb) >= 0.05 {
			v.Lenses = append(v.Lenses, Driver{Name: name, DeltaH: ca - cb, Cur: ca, Prev: cb, Lens: true})
		}
	}
	lens("Waiting after handoffs", a.handoff, b.handoff)
	lens("Rework loops", a.loops, b.loops)
	lens("Weekend waiting", a.weekend, b.weekend)
	_, v.Throughput = in.completedBy("department")
	return v
}

// weekendHours counts Saturday and Sunday hours inside [a, b)
func weekendHours(a, b time.Time) float64 {
	if !b.After(a) {
		return 0
	}
	if b.Sub(a) > 400*24*time.Hour {
		return b.Sub(a).Hours() * 2 / 7
	}
	var h float64
	for t := a; t.Before(b); {
		next := dayOf(t).AddDate(0, 0, 1)
		if next.After(b) {
			next = b
		}
		if wd := t.Weekday(); wd == time.Saturday || wd == time.Sunday {
			h += next.Sub(t).Hours()
		}
		t = next
	}
	return h
}

// ---------------------------------------------------------------------
// Management summary

type Statement struct {
	Text string
	Link string
}

// Summary writes a short management summary; every sentence comes from a
// computed figure and links to its evidence
func (in *Intel) Summary() []Statement {
	cur, prev := in.Measure(in.W), in.Measure(in.Prev)
	var out []Statement
	label := strings.ToUpper(in.Scope.Label()[:1]) + in.Scope.Label()[1:]

	if cur.Completed+prev.Completed > 0 {
		if prev.Completed > 0 {
			ch := (float64(cur.Completed) - float64(prev.Completed)) / float64(prev.Completed) * 100
			verb := "increased"
			if ch < 0 {
				verb = "decreased"
			}
			if math.Abs(ch) < 2 {
				out = append(out, Statement{fmt.Sprintf("%s: throughput held steady at %d completed.", label, cur.Completed), in.Scope.URL("/command/throughput")})
			} else {
				out = append(out, Statement{fmt.Sprintf("%s: throughput %s %.0f%% (%d completed versus %d).", label, verb, math.Abs(ch), cur.Completed, prev.Completed), in.Scope.URL("/command/throughput")})
			}
		} else {
			out = append(out, Statement{fmt.Sprintf("%s: %d items completed.", label, cur.Completed), in.Scope.URL("/command/throughput")})
		}
	}
	if cur.Cycle.N >= 5 && prev.Cycle.N >= 5 {
		verb := "rose"
		if cur.Cycle.Median < prev.Cycle.Median {
			verb = "declined"
		}
		out = append(out, Statement{fmt.Sprintf("Median cycle time %s from %s to %s.", verb, fmtHours(prev.Cycle.Median), fmtHours(cur.Cycle.Median)),
			in.Scope.URL("/command/records", "set", "completed")})
	}
	if cur.SLAApplicable >= 5 && prev.SLAApplicable >= 5 {
		verb := "increased"
		if cur.SLACompliance < prev.SLACompliance {
			verb = "decreased"
		}
		out = append(out, Statement{fmt.Sprintf("SLA compliance %s from %.0f%% to %.0f%%.", verb, prev.SLACompliance, cur.SLACompliance), in.Scope.URL("/command/sla")})
	} else if cur.SLAApplicable > 0 {
		out = append(out, Statement{fmt.Sprintf("SLA compliance is %.0f%% (%d of %d).", cur.SLACompliance, cur.SLAMet, cur.SLAApplicable), in.Scope.URL("/command/sla")})
	}
	stages := in.Stages()
	if b := in.TopBottleneck(stages); b != nil {
		out = append(out, Statement{fmt.Sprintf("%s · %s is the largest bottleneck and holds %.0f%% of %s workflow time.", b.Label, b.Status, b.TimeShare, lower1(b.Label)),
			in.Scope.URL("/command/stage", "workflow", b.Module, "stage", b.Status)})
	}
	var best *Change
	for _, c := range in.WhatChanged() {
		c := c
		if c.Good && strings.Contains(c.Label, "stage time") && (best == nil || c.magnitude > best.magnitude) {
			best = &c
		}
	}
	if best != nil {
		out = append(out, Statement{fmt.Sprintf("The strongest improvement was %s: median %s, down from %s.", best.Label, best.Cur, best.Prev), best.Link})
	}
	if cur.Blocked > 0 {
		out = append(out, Statement{fmt.Sprintf("%s currently blocked.", plural(cur.Blocked, "item is", "items are")), in.Scope.URL("/command/records", "set", "blocked")})
	}
	return out
}

// ---------------------------------------------------------------------
// Period comparison

type CompareRow struct {
	Label     string
	Cur, Prev string
	Delta     string
	Dir       string
	Status    string
	Series    []float64
}

var comparePresets = []struct{ Key, Label string }{
	{"week", "This week vs last week"}, {"month", "This month vs last month"}, {"30d", "Last 30 days vs previous 30"},
	{"quarter", "This quarter vs previous quarter"}, {"year", "This year vs prior year"},
}

// CompareWindows returns the current and previous windows of a comparison
func CompareWindows(key string, now time.Time) (Window, Window, string) {
	now = now.UTC()
	today := dayOf(now)
	switch key {
	case "week":
		s := today.AddDate(0, 0, -((int(today.Weekday()) + 6) % 7))
		return Window{s, now}, Window{s.AddDate(0, 0, -7), now.AddDate(0, 0, -7)}, key
	case "month":
		s := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		ps := s.AddDate(0, -1, 0)
		pe := ps.Add(now.Sub(s))
		if pe.After(s) {
			pe = s
		}
		return Window{s, now}, Window{ps, pe}, key
	case "quarter":
		qm := time.Month((int(now.Month())-1)/3*3 + 1)
		s := time.Date(now.Year(), qm, 1, 0, 0, 0, 0, time.UTC)
		ps := s.AddDate(0, -3, 0)
		pe := ps.Add(now.Sub(s))
		if pe.After(s) {
			pe = s
		}
		return Window{s, now}, Window{ps, pe}, key
	case "year":
		s := time.Date(now.Year(), 1, 1, 0, 0, 0, 0, time.UTC)
		ps := s.AddDate(-1, 0, 0)
		return Window{s, now}, Window{ps, ps.Add(now.Sub(s))}, key
	}
	return Window{today.AddDate(0, 0, -29), now}, Window{today.AddDate(0, 0, -59), today.AddDate(0, 0, -29)}, "30d"
}

// Compare lists every overview measure for two windows with a series of the
// last 8 comparable periods
func (in *Intel) Compare(cur, prev Window) []CompareRow {
	a, b := in.Measure(cur), in.Measure(prev)
	span := cur.To.Sub(cur.From)
	var hist []Measures
	for k := 7; k >= 0; k-- {
		w := Window{cur.From.Add(-time.Duration(k) * span), cur.To.Add(-time.Duration(k) * span)}
		hist = append(hist, in.Measure(w))
	}
	var out []CompareRow
	for _, sp := range kpiSpecs {
		v, ok := sp.value(a)
		pv, pok := sp.value(b)
		r := CompareRow{Label: sp.label, Cur: "—", Prev: "—", Dir: "flat", Status: "neutral"}
		if ok {
			r.Cur = fmtValue(v, sp.unit)
		}
		if pok {
			r.Prev = fmtValue(pv, sp.unit)
		}
		if ok && pok {
			r.Delta, r.Dir = deltaText(v, pv, sp.unit)
			r.Status = judge(v, pv, sp.unit, sp.higherBetter)
		}
		for _, h := range hist {
			hv, hok := sp.value(h)
			if !hok {
				hv = math.NaN()
			}
			r.Series = append(r.Series, hv)
		}
		out = append(out, r)
	}
	return out
}

// ---------------------------------------------------------------------
// Data coverage

type Coverage struct {
	Records     int
	Events      int
	FullHistory float64
	Assignment  float64
	Department  float64
	Team        float64
	DueDates    float64
	HistoryDays float64
	Earliest    time.Time
	Live        int
	Partial     int
}

// Coverage reports how complete the history behind the scope is
func (in *Intel) Coverage() Coverage {
	c := Coverage{Records: len(in.Items), Earliest: in.Earliest}
	if !in.Earliest.IsZero() {
		c.HistoryDays = in.Now.Sub(in.Earliest).Hours() / 24
	}
	for _, e := range in.scopedEvents() {
		if in.W.Has(e.OccurredAt) {
			c.Events++
		}
	}
	if c.Records == 0 {
		return c
	}
	var full, asg, dep, team, due int
	for _, it := range in.Items {
		if !it.Partial {
			full++
		}
		if it.HadAssignee {
			asg++
		}
		if it.Department > 0 {
			dep++
		}
		if it.Team > 0 {
			team++
		}
		if it.DueAt != nil {
			due++
		}
	}
	n := float64(c.Records)
	c.Live, c.Partial = full, c.Records-full
	c.FullHistory = float64(full) / n * 100
	c.Assignment = float64(asg) / n * 100
	c.Department = float64(dep) / n * 100
	c.Team = float64(team) / n * 100
	c.DueDates = float64(due) / n * 100
	return c
}

// ---------------------------------------------------------------------
// Monthly outcomes

type MonthReport struct {
	Month     time.Time
	Label     string
	Has       bool
	Rows      []MonthRow
	Stages    []StageRow
	Changes   []Change
	Months    []MonthOption
	Documents int
	CasesOpen int
	CasesDone int
	TasksDone int
	Approvals int
}

type MonthRow struct {
	Label     string
	Cur, Prev string
	Avg3      string
	Delta     string
	Dir       string
	Status    string
	Series    []float64
}

type MonthOption struct {
	Key, Label string
	Selected   bool
}

// Month builds the management report of one calendar month
func (in *Intel) Month(month time.Time) MonthReport {
	m := time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, time.UTC)
	r := MonthReport{Month: m, Label: m.Format("January 2006")}
	cur := Window{m, minTime(m.AddDate(0, 1, 0), in.Now)}
	prev := Window{m.AddDate(0, -1, 0), m}

	first := time.Date(in.Now.Year(), in.Now.Month(), 1, 0, 0, 0, 0, time.UTC)
	for k := 0; k < 12; k++ {
		mm := first.AddDate(0, -k, 0)
		if !in.Earliest.IsZero() && mm.AddDate(0, 1, 0).Before(in.Earliest) {
			break
		}
		r.Months = append(r.Months, MonthOption{mm.Format("2006-01"), mm.Format("January 2006"), mm.Equal(m)})
	}

	a, b := in.Measure(cur), in.Measure(prev)
	r.Has = a.Started+a.Completed+a.WIP > 0
	var three []Measures
	for k := 1; k <= 3; k++ {
		s := m.AddDate(0, -k, 0)
		three = append(three, in.Measure(Window{s, s.AddDate(0, 1, 0)}))
	}
	var hist []Measures
	for k := 11; k >= 0; k-- {
		s := m.AddDate(0, -k, 0)
		hist = append(hist, in.Measure(Window{s, minTime(s.AddDate(0, 1, 0), in.Now)}))
	}

	for _, sp := range kpiSpecs {
		v, ok := sp.value(a)
		pv, pok := sp.value(b)
		row := MonthRow{Label: sp.label, Cur: "—", Prev: "—", Avg3: "—", Dir: "flat", Status: "neutral"}
		if ok {
			row.Cur = fmtValue(v, sp.unit)
		}
		if pok {
			row.Prev = fmtValue(pv, sp.unit)
		}
		if ok && pok {
			row.Delta, row.Dir = deltaText(v, pv, sp.unit)
			row.Status = judge(v, pv, sp.unit, sp.higherBetter)
		}
		var s float64
		n := 0
		for _, t := range three {
			if tv, tok := sp.value(t); tok {
				s += tv
				n++
			}
		}
		if n > 0 {
			row.Avg3 = fmtValue(s/float64(n), sp.unit)
		}
		for _, h := range hist {
			hv, hok := sp.value(h)
			if !hok {
				hv = math.NaN()
			}
			row.Series = append(row.Series, hv)
		}
		r.Rows = append(r.Rows, row)
	}

	mi := *in
	mi.W, mi.Prev = cur, prev
	for _, ps := range mi.Stages() {
		for _, sr := range ps.Rows {
			if sr.Enough && sr.Band != "Normal" {
				r.Stages = append(r.Stages, sr)
			}
		}
	}
	sort.Slice(r.Stages, func(i, j int) bool { return r.Stages[i].Severity > r.Stages[j].Severity })
	if len(r.Stages) > 5 {
		r.Stages = r.Stages[:5]
	}
	r.Changes = mi.WhatChanged()

	for _, e := range in.Events {
		if !cur.Has(e.OccurredAt) {
			continue
		}
		switch {
		case e.Module == "Document" && e.Kind == ActivityCreated:
			r.Documents++
		case e.Module == "Case" && e.Kind == ActivityCreated:
			r.CasesOpen++
		case e.Module == "Case" && e.Kind == ActivityStatus && (e.ToStatus == "Resolved" || e.ToStatus == "Closed"):
			r.CasesDone++
		case e.Module == "Task" && e.Kind == ActivityStatus && e.ToStatus == "Done":
			r.TasksDone++
		case e.Module == "Approval" && e.Kind == ActivityStatus && (e.ToStatus == "Approved" || e.ToStatus == "Rejected"):
			r.Approvals++
		}
	}
	return r
}
