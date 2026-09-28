package saas

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// StageRow is the deep analysis of one status of one workflow
type StageRow struct {
	Module, Label, Status, Kind string
	Rank                        int

	WIP, Entered, Exited, Completed int
	Time                            Stats // visits that ended in the window
	Target                          float64
	HasTarget                       bool
	Compliance                      float64
	Breaches                        int // visits over target (ended or still open)
	Aging                           int // open visits older than target (or the stage's P90)
	Blocked                         int
	Reentries                       int
	ReworkRate                      float64
	Fallout                         int // exits into a failed state
	DropRate                        float64
	InRate, OutRate                 float64 // per day
	NetWIP                          int
	HandoffWait                     Stats // handoffs made while in this stage
	Owner, Team                     string
	OwnerShare                      float64
	TimeShare                       float64 // share of the workflow's time in the window
	Trend                           []float64
	TrendDir                        string // rising | falling | steady | —

	Severity   float64
	Band       string // Critical | High | Elevated | Normal | —
	Components []SevComponent
	Terminal   bool
	Enough     bool
	timeH      float64
}

// SevComponent is one measured input of a severity score
type SevComponent struct {
	Name   string
	Value  string
	Points float64
}

// ProcessStages holds the stages of one workflow in process order
type ProcessStages struct {
	Module, Label string
	Rows          []StageRow
	Edges         []Edge
	Items         int
	TotalH        float64
}

// Edge is a movement between two statuses
type Edge struct {
	From, To  string
	Count     int
	Delay     Stats // time spent in From before this move
	Backward  bool
	Skip      bool
	Fallout   bool
	FromIndex int
	ToIndex   int
}

func (p ProcessStages) Row(status string) *StageRow {
	for i := range p.Rows {
		if p.Rows[i].Status == status {
			return &p.Rows[i]
		}
	}
	return nil
}

// Stages analyzes every workflow in scope
func (in *Intel) Stages() []ProcessStages {
	var out []ProcessStages
	for _, st := range in.modules() {
		out = append(out, in.StagesOf(st.Module))
	}
	return out
}

// StagesOf analyzes the statuses of one workflow over the scope window
func (in *Intel) StagesOf(module string) ProcessStages {
	st := stageFor(module)
	ps := ProcessStages{Module: module}
	if st == nil {
		return ps
	}
	ps.Label = st.Label
	w := in.W

	type agg struct {
		row       *StageRow
		times     []float64
		within    int
		ended     int
		handoffs  []float64
		owners    map[uint64]float64
		teams     map[uint64]float64
		weekTimes [8][]float64
	}
	rows := map[string]*agg{}
	get := func(status string) *agg {
		a := rows[status]
		if a == nil {
			a = &agg{row: &StageRow{Module: module, Label: st.Label, Status: status, Kind: st.kindOf(statusOrEmpty(status)),
				Rank: st.rankOf(statusOrEmpty(status))}, owners: map[uint64]float64{}, teams: map[uint64]float64{}}
			a.row.Terminal = a.row.Kind == "done" || a.row.Kind == "failed"
			if t := in.Targets[targetKey(module, status)]; t > 0 {
				a.row.Target, a.row.HasTarget = t, true
			}
			rows[status] = a
		}
		return a
	}
	for _, s := range st.Order {
		get(s)
	}

	edges := map[[2]string]*Edge{}
	var delays = map[[2]string][]float64{}
	weekStart := w.To.AddDate(0, 0, -56)
	var totalH float64

	for _, it := range in.Items {
		if it.Module != module {
			continue
		}
		counted := false
		for i, sg := range it.Segments {
			a := get(sg.Status)
			r := a.row
			end := in.Now
			if sg.End != nil {
				end = *sg.End
			}
			inWin := w.overlap(sg.Start, end).Hours()
			if !r.Terminal {
				r.timeH += inWin
				totalH += inWin
				if inWin > 0 {
					a.owners[sg.Assignee] += inWin
					a.teams[it.Team] += inWin
				}
			}
			if inWin > 0 || w.Has(sg.Start) {
				counted = true
			}

			if w.Has(sg.Start) {
				r.Entered++
				if sg.Reentry {
					r.Reentries++
				}
				if r.Kind == "done" {
					r.Completed++
				}
			}

			if sg.End != nil && !r.Terminal {
				d := sg.End.Sub(sg.Start).Hours()
				if w.Has(*sg.End) {
					r.Exited++
					a.times = append(a.times, d)
					a.ended++
					if r.HasTarget && d <= r.Target {
						a.within++
					}
					if r.HasTarget && d > r.Target {
						r.Breaches++
					}
					if i+1 < len(it.Segments) && it.Segments[i+1].Kind == "failed" {
						r.Fallout++
					}
				}
				if !sg.End.Before(weekStart) && sg.End.Before(w.To) {
					wk := int(sg.End.Sub(weekStart).Hours() / (24 * 7))
					if wk >= 0 && wk < 8 {
						a.weekTimes[wk] = append(a.weekTimes[wk], d)
					}
				}
			}

			// every move out of a status (finished states included: reopens)
			if i+1 < len(it.Segments) && w.Has(it.Segments[i+1].Start) {
				next := it.Segments[i+1]
				k := [2]string{sg.Status, next.Status}
				e := edges[k]
				if e == nil {
					e = &Edge{From: sg.Status, To: next.Status}
					edges[k] = e
				}
				e.Count++
				delays[k] = append(delays[k], next.Start.Sub(sg.Start).Hours())
			}

			// still in this stage at the end of the window
			if !r.Terminal && !sg.Start.After(w.To) && (sg.End == nil || sg.End.After(w.To)) && it.OpenAt(w.To) {
				r.WIP++
				if r.Kind == "blocked" {
					r.Blocked++
				}
				// aging is decided below, once the stage's percentiles are known
				if r.HasTarget && w.To.Sub(sg.Start).Hours() > r.Target {
					r.Breaches++
				}
			}
		}
		if counted {
			ps.Items++
		}

		for _, h := range it.Handoffs() {
			if w.Has(h.At) {
				a := get(h.Status)
				a.handoffs = append(a.handoffs, h.Wait(in.Now).Hours())
			}
		}
	}

	// aging needs each stage's own P90 when no target is set
	for _, it := range in.Items {
		if it.Module != module || !it.OpenAt(w.To) {
			continue
		}
		sg, ok := it.StatusAt(w.To.Add(-time.Nanosecond))
		if !ok {
			continue
		}
		a := rows[sg.Status]
		if a == nil || a.row.Terminal {
			continue
		}
		limit := a.row.Target
		if !a.row.HasTarget {
			limit = percentile(a.times, 0.9)
		}
		if limit > 0 && w.To.Sub(sg.Start).Hours() > limit {
			a.row.Aging++
		}
	}

	var maxSev float64
	for _, a := range rows {
		r := a.row
		r.Time = statsOf(a.times)
		r.HandoffWait = statsOf(a.handoffs)
		if r.HasTarget && a.ended > 0 {
			r.Compliance = float64(a.within) / float64(a.ended) * 100
		}
		if r.Entered > 0 {
			r.ReworkRate = float64(r.Reentries) / float64(r.Entered) * 100
		}
		if r.Exited > 0 {
			r.DropRate = float64(r.Fallout) / float64(r.Exited) * 100
		}
		if d := w.Days(); d > 0 {
			r.InRate = float64(r.Entered) / d
			r.OutRate = float64(r.Exited) / d
		}
		r.NetWIP = r.Entered - r.Exited
		if r.Terminal {
			r.NetWIP = 0
		}
		if totalH > 0 {
			r.TimeShare = r.timeH / totalH * 100
		}
		if id, share := top(a.owners); share > 0 {
			r.Owner = in.Lk.person(id)
			r.OwnerShare = share
		}
		if id, share := top(a.teams); share > 0 && id > 0 {
			r.Team = in.Lk.team(id)
		}
		for _, wk := range a.weekTimes {
			if len(wk) == 0 {
				r.Trend = append(r.Trend, math.NaN())
				continue
			}
			r.Trend = append(r.Trend, median(wk))
		}
		r.TrendDir = trendDir(r.Trend, false)
		r.Enough = !r.Terminal && r.Entered+r.WIP >= 5
		ps.TotalH += r.timeH
	}

	// severity from measured components
	totalWIP := 0
	for _, a := range rows {
		totalWIP += a.row.WIP
	}
	for _, a := range rows {
		r := a.row
		if !r.Enough {
			r.Band = "—"
			continue
		}
		var cc []SevComponent
		add := func(name, val string, pts float64) {
			if pts > 0.05 {
				cc = append(cc, SevComponent{name, val, pts})
			}
			r.Severity += pts
		}
		add("time share", fmt.Sprintf("%.0f%%", r.TimeShare), 30*r.TimeShare/100)
		if totalWIP > 0 {
			ws := float64(r.WIP) / float64(totalWIP) * 100
			add("open work", fmt.Sprintf("%.0f%% of WIP", ws), 15*ws/100)
		}
		if r.WIP > 0 {
			as := float64(r.Aging) / float64(r.WIP) * 100
			add("aging", fmt.Sprintf("%d aging", r.Aging), 15*as/100)
		}
		if n := r.Exited + r.WIP; r.HasTarget && n > 0 {
			br := float64(r.Breaches) / float64(n) * 100
			add("SLA breaches", fmt.Sprintf("%d (%.0f%%)", r.Breaches, br), 20*math.Min(1, br/100))
		}
		add("rework", fmt.Sprintf("%.0f%% re-entries", r.ReworkRate), 10*math.Min(1, r.ReworkRate/100))
		if r.Entered > 0 && r.NetWIP > 0 {
			g := float64(r.NetWIP) / float64(r.Entered) * 100
			add("backlog growth", fmt.Sprintf("+%d net", r.NetWIP), 10*math.Min(1, g/100))
		}
		sort.Slice(cc, func(i, j int) bool { return cc[i].Points > cc[j].Points })
		r.Components = cc
		r.Severity = math.Round(r.Severity)
		switch {
		case r.Severity >= 45:
			r.Band = "Critical"
		case r.Severity >= 30:
			r.Band = "High"
		case r.Severity >= 15:
			r.Band = "Elevated"
		default:
			r.Band = "Normal"
		}
		if r.Severity > maxSev {
			maxSev = r.Severity
		}
	}

	// rows in process order: known statuses first, then custom ones
	var list []*agg
	for _, a := range rows {
		if a.row.Entered+a.row.Exited+a.row.WIP == 0 && !contains(st.Order, a.row.Status) {
			continue
		}
		list = append(list, a)
	}
	sort.SliceStable(list, func(i, j int) bool {
		oi, oj := indexOf(st.Order, list[i].row.Status), indexOf(st.Order, list[j].row.Status)
		if oi < 0 {
			oi = 50 + list[i].row.Rank
		}
		if oj < 0 {
			oj = 50 + list[j].row.Rank
		}
		return oi < oj
	})
	idx := map[string]int{}
	for i, a := range list {
		ps.Rows = append(ps.Rows, *a.row)
		idx[a.row.Status] = i
	}

	for k, e := range edges {
		e.Delay = statsOf(delays[k])
		e.FromIndex, e.ToIndex = idx[e.From], idx[e.To]
		fr, tr := st.rankOf(statusOrEmpty(e.From)), st.rankOf(statusOrEmpty(e.To))
		if st.terminal(statusOrEmpty(e.From)) {
			fr++
		}
		e.Backward = tr < fr && !st.terminal(statusOrEmpty(e.To))
		e.Fallout = st.Failed[statusOrEmpty(e.To)]
		e.Skip = !e.Backward && !st.terminal(statusOrEmpty(e.To)) && tr-fr > 1
		ps.Edges = append(ps.Edges, *e)
	}
	sort.Slice(ps.Edges, func(i, j int) bool { return ps.Edges[i].Count > ps.Edges[j].Count })
	return ps
}

func statusOrEmpty(s string) string {
	if s == "No status" {
		return ""
	}
	return s
}

func contains(ss []string, s string) bool { return indexOf(ss, s) >= 0 }

func indexOf(ss []string, s string) int {
	for i, v := range ss {
		if v == s {
			return i
		}
	}
	return -1
}

// top returns the key with the largest share of the total
func top(m map[uint64]float64) (uint64, float64) {
	var (
		best       uint64
		max, total float64
	)
	for k, v := range m {
		total += v
		if v > max || (v == max && k < best) {
			best, max = k, v
		}
	}
	if total == 0 {
		return 0, 0
	}
	return best, max / total * 100
}

// trendDir compares the recent half of a series with the earlier half
func trendDir(series []float64, higherBetter bool) string {
	var a, b []float64
	half := len(series) / 2
	for i, v := range series {
		if math.IsNaN(v) {
			continue
		}
		if i < half {
			a = append(a, v)
		} else {
			b = append(b, v)
		}
	}
	if len(a) < 2 || len(b) < 2 {
		return "—"
	}
	ma, mb := median(a), median(b)
	if ma == 0 {
		return "—"
	}
	ch := (mb - ma) / ma
	switch {
	case ch > 0.1:
		return "rising"
	case ch < -0.1:
		return "falling"
	}
	return "steady"
}

// TopBottleneck returns the most severe stage across workflows
func (in *Intel) TopBottleneck(pp []ProcessStages) *StageRow {
	var best *StageRow
	for i := range pp {
		for j := range pp[i].Rows {
			r := &pp[i].Rows[j]
			if r.Enough && (best == nil || r.Severity > best.Severity) {
				best = r
			}
		}
	}
	return best
}

// ---------------------------------------------------------------------
// SLA intelligence

// SLARow analyzes one configured target
type SLARow struct {
	Module, Label, Stage   string // Stage "" = whole workflow
	Target                 float64
	Actual                 Stats
	Items, Breaches        int
	Compliance, BreachRate float64
	AvgOver, MaxOver       float64
	Trend                  []float64 // weekly compliance
	TrendDir               string
	Teams, People          []NameCount
	Link                   string
}

type NameCount struct {
	Name  string
	Count int
	Link  string
}

// Breach is one record that exceeded a target
type Breach struct {
	Item     *WorkItem
	Customer string
	Stage    string
	Entered  time.Time
	Deadline time.Time
	Finished *time.Time
	OverH    float64
	Assignee string
	Status   string
	Open     bool
}

// SLA analyzes every configured target over the window
func (in *Intel) SLA() ([]SLARow, []Breach) {
	var rows []SLARow
	var breaches []Breach
	w := in.W

	for i := range pipelineStages {
		st := &pipelineStages[i]
		keys := []string{""}
		for _, s := range st.Order {
			keys = append(keys, s)
		}
		for k := range in.Targets {
			if m, s := splitTargetKey(k); m == st.Module && s != "" && !contains(keys, s) {
				keys = append(keys, s)
			}
		}

		for _, stage := range keys {
			target := in.Targets[targetKey(st.Module, stage)]
			if target <= 0 {
				continue
			}
			row := SLARow{Module: st.Module, Label: st.Label, Stage: stage, Target: target,
				Link: in.Scope.URL("/command/records", "set", "sla_breached", "workflow", st.Module, "stage", stage)}
			var vals []float64
			teams, people := map[uint64]int{}, map[uint64]int{}
			weekStart := w.To.AddDate(0, 0, -56)
			var wkN, wkOK [8]int
			var overSum float64

			for _, it := range in.Items {
				if it.Module != st.Module {
					continue
				}
				if stage == "" {
					if it.CompletedAt != nil && w.Has(*it.CompletedAt) {
						d := it.Cycle().Hours()
						vals = append(vals, d)
						row.Items++
						ok := d <= target
						if !ok {
							row.Breaches++
							over := d - target
							overSum += over
							row.MaxOver = math.Max(row.MaxOver, over)
							teams[it.Team]++
							people[it.Assignee]++
							t := *it.CompletedAt
							breaches = append(breaches, Breach{Item: it, Customer: in.CustomerName(it.Customer), Stage: "Whole " + lower1(st.Label),
								Entered: it.CreatedAt, Deadline: it.CreatedAt.Add(hoursDur(target)), Finished: &t, OverH: over,
								Assignee: in.Lk.person(it.Assignee), Status: statusName(it.Status)})
						}
						if !it.CompletedAt.Before(weekStart) {
							wk := int(it.CompletedAt.Sub(weekStart).Hours() / 168)
							if wk >= 0 && wk < 8 {
								wkN[wk]++
								if ok {
									wkOK[wk]++
								}
							}
						}
					} else if it.OpenAt(w.To) && w.To.Sub(it.CreatedAt).Hours() > target {
						over := w.To.Sub(it.CreatedAt).Hours() - target
						row.Breaches++
						row.Items++
						teams[it.Team]++
						people[it.Assignee]++
						breaches = append(breaches, Breach{Item: it, Customer: in.CustomerName(it.Customer), Stage: "Whole " + lower1(st.Label),
							Entered: it.CreatedAt, Deadline: it.CreatedAt.Add(hoursDur(target)), OverH: over,
							Assignee: in.Lk.person(it.Assignee), Status: statusName(it.Status), Open: true})
					}
					continue
				}

				for _, sg := range it.Segments {
					if sg.Status != stage {
						continue
					}
					if sg.End != nil && w.Has(*sg.End) {
						d := sg.End.Sub(sg.Start).Hours()
						vals = append(vals, d)
						row.Items++
						ok := d <= target
						if !ok {
							row.Breaches++
							over := d - target
							overSum += over
							row.MaxOver = math.Max(row.MaxOver, over)
							teams[it.Team]++
							people[sg.Assignee]++
							t := *sg.End
							breaches = append(breaches, Breach{Item: it, Customer: in.CustomerName(it.Customer), Stage: stage,
								Entered: sg.Start, Deadline: sg.Start.Add(hoursDur(target)), Finished: &t, OverH: over,
								Assignee: in.Lk.person(sg.Assignee), Status: statusName(it.Status)})
						}
						if !sg.End.Before(weekStart) {
							wk := int(sg.End.Sub(weekStart).Hours() / 168)
							if wk >= 0 && wk < 8 {
								wkN[wk]++
								if ok {
									wkOK[wk]++
								}
							}
						}
					} else if sg.End == nil && it.OpenAt(w.To) && w.To.Sub(sg.Start).Hours() > target {
						over := w.To.Sub(sg.Start).Hours() - target
						row.Breaches++
						row.Items++
						teams[it.Team]++
						people[sg.Assignee]++
						breaches = append(breaches, Breach{Item: it, Customer: in.CustomerName(it.Customer), Stage: stage,
							Entered: sg.Start, Deadline: sg.Start.Add(hoursDur(target)), OverH: over,
							Assignee: in.Lk.person(sg.Assignee), Status: statusName(it.Status), Open: true})
					}
				}
			}

			row.Actual = statsOf(vals)
			if row.Items > 0 {
				row.BreachRate = float64(row.Breaches) / float64(row.Items) * 100
				row.Compliance = 100 - row.BreachRate
			}
			if row.Breaches > 0 {
				row.AvgOver = overSum / math.Max(1, float64(countClosed(breaches, st.Module, stage)))
			}
			for k := range wkN {
				if wkN[k] == 0 {
					row.Trend = append(row.Trend, math.NaN())
					continue
				}
				row.Trend = append(row.Trend, float64(wkOK[k])/float64(wkN[k])*100)
			}
			row.TrendDir = trendDir(row.Trend, true)
			row.Teams = in.topNames(teams, 3, "team")
			row.People = in.topNames(people, 3, "person")
			rows = append(rows, row)
		}
	}

	sort.SliceStable(breaches, func(i, j int) bool { return breaches[i].OverH > breaches[j].OverH })
	return rows, breaches
}

func countClosed(bb []Breach, module, stage string) int {
	n := 0
	for _, b := range bb {
		if b.Item.Module == module && !b.Open && (b.Stage == stage || (stage == "" && len(b.Stage) > 6 && b.Stage[:6] == "Whole ")) {
			n++
		}
	}
	return n
}

func splitTargetKey(k string) (string, string) {
	for i := 0; i < len(k); i++ {
		if k[i] == '|' {
			return k[:i], k[i+1:]
		}
	}
	return k, ""
}

func hoursDur(h float64) time.Duration { return time.Duration(h * float64(time.Hour)) }

func lower1(s string) string {
	if s == "" {
		return s
	}
	b := []byte(s)
	if b[0] >= 'A' && b[0] <= 'Z' {
		b[0] += 'a' - 'A'
	}
	return string(b)
}

func (in *Intel) topNames(m map[uint64]int, n int, kind string) []NameCount {
	var out []NameCount
	for id, c := range m {
		nc := NameCount{Count: c}
		switch kind {
		case "team":
			nc.Name = in.Lk.team(id)
			if id > 0 {
				nc.Link = in.Scope.URL("/command/team/" + fmt.Sprint(id))
			}
		case "department":
			nc.Name = in.Lk.department(id)
			nc.Link = in.Scope.URL("/command/department/" + fmt.Sprint(id))
		default:
			nc.Name = in.Lk.person(id)
			if id > 0 {
				nc.Link = in.Scope.URL("/command/person/" + fmt.Sprint(id))
			}
		}
		out = append(out, nc)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}
