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
// Aging

var agingBuckets = []struct {
	Key   string
	Label string
	MaxH  float64
}{
	{"lt1d", "< 1 day", 24}, {"1-3d", "1–3 days", 72}, {"3-7d", "3–7 days", 168},
	{"7-14d", "7–14 days", 336}, {"14-30d", "14–30 days", 720}, {"30d+", "30+ days", math.Inf(1)},
}

type AgingView struct {
	Basis   string // age | stage
	Dim     string // stage | department | team | assignee | process | type
	Buckets []string
	Keys    []string
	Rows    []AgingRow
	Totals  []int
	Total   int
	AtRisk  []AtRisk
}

type AgingRow struct {
	Name   string
	Key    string
	Counts []int
	Total  int
	Links  []string
}

// AtRisk is open work already older than most completed work of its kind
type AtRisk struct {
	Item       *WorkItem
	AgeH       float64
	Percentile float64 // share of completed items that finished faster
	TargetH    float64
	Level      string // Past target | High | Elevated | Rising
	Stage      string
	Assignee   string
	Samples    int
}

func agingBucket(h float64) int {
	for i, b := range agingBuckets {
		if h < b.MaxH {
			return i
		}
	}
	return len(agingBuckets) - 1
}

// Aging groups open work by how long it has been open (or in its stage)
func (in *Intel) Aging(basis, dim string) AgingView {
	if basis != "stage" {
		basis = "age"
	}
	switch dim {
	case "stage", "department", "team", "assignee", "process", "type":
	default:
		dim = "stage"
	}
	v := AgingView{Basis: basis, Dim: dim, Totals: make([]int, len(agingBuckets))}
	for _, b := range agingBuckets {
		v.Buckets = append(v.Buckets, b.Label)
		v.Keys = append(v.Keys, b.Key)
	}

	end := in.W.To
	rows := map[string]*AgingRow{}
	var order []string

	// completed cycle times per workflow, for the at-risk comparison
	hist := map[string][]float64{}
	for _, it := range in.All {
		if it.stage != nil && !it.Deleted && it.Done() && it.CompletedAt.Before(end) {
			hist[it.Module] = append(hist[it.Module], it.Cycle().Hours())
		}
	}
	for k := range hist {
		sort.Float64s(hist[k])
	}

	for _, it := range in.Items {
		if !it.OpenAt(end) {
			continue
		}
		sg, _ := it.StatusAt(end.Add(-time.Nanosecond))
		age := end.Sub(it.CreatedAt).Hours()
		if basis == "stage" && !sg.Start.IsZero() {
			age = end.Sub(sg.Start).Hours()
		}
		b := agingBucket(age)

		var name, key string
		switch dim {
		case "stage":
			name, key = it.stage.Label+" · "+sg.Status, it.Module+"|"+sg.Status
		case "department":
			name, key = in.Lk.department(it.Department), strconv.FormatUint(it.Department, 10)
		case "team":
			name, key = in.Lk.team(it.Team), strconv.FormatUint(it.Team, 10)
		case "assignee":
			name, key = in.Lk.person(it.Assignee), strconv.FormatUint(it.Assignee, 10)
		case "process":
			name, key = it.stage.Label, it.Module
		case "type":
			name, key = orStr(it.Category, "No type"), it.Category
		}
		r := rows[key]
		if r == nil {
			r = &AgingRow{Name: name, Key: key, Counts: make([]int, len(agingBuckets))}
			rows[key] = r
			order = append(order, key)
		}
		r.Counts[b]++
		r.Total++
		v.Totals[b]++
		v.Total++

		// at risk: older than most completed items of the same workflow
		cycles := hist[it.Module]
		if len(cycles) >= 10 {
			fullAge := end.Sub(it.CreatedAt).Hours()
			p := float64(sort.SearchFloat64s(cycles, fullAge)) / float64(len(cycles)) * 100
			target := in.Targets[it.Module]
			level := ""
			switch {
			case target > 0 && fullAge > target:
				level = "Past target"
			case p >= 90:
				level = "High"
			case p >= 75:
				level = "Elevated"
			case p >= 50 && target > 0 && fullAge > 0.75*target:
				level = "Rising"
			}
			if level != "" {
				v.AtRisk = append(v.AtRisk, AtRisk{Item: it, AgeH: fullAge, Percentile: p, TargetH: target, Level: level,
					Stage: sg.Status, Assignee: in.Lk.person(it.Assignee), Samples: len(cycles)})
			}
		}
	}

	sort.Slice(order, func(i, j int) bool { return rows[order[i]].Total > rows[order[j]].Total })
	for _, k := range order {
		r := rows[k]
		for _, bk := range v.Keys {
			r.Links = append(r.Links, in.Scope.URL("/command/records", "set", "aging_bucket", "bucket", bk, "basis", basis, "dim", dim, "key", r.Key))
		}
		v.Rows = append(v.Rows, *r)
	}
	sort.SliceStable(v.AtRisk, func(i, j int) bool {
		li, lj := riskRank(v.AtRisk[i].Level), riskRank(v.AtRisk[j].Level)
		if li != lj {
			return li > lj
		}
		return v.AtRisk[i].Percentile > v.AtRisk[j].Percentile
	})
	if len(v.AtRisk) > 50 {
		v.AtRisk = v.AtRisk[:50]
	}
	return v
}

func riskRank(l string) int {
	switch l {
	case "Past target":
		return 4
	case "High":
		return 3
	case "Elevated":
		return 2
	}
	return 1
}

// agingKey returns the aging dimension key of an item (for record lists)
func (in *Intel) agingKey(it *WorkItem, dim string) string {
	switch dim {
	case "department":
		return strconv.FormatUint(it.Department, 10)
	case "team":
		return strconv.FormatUint(it.Team, 10)
	case "assignee":
		return strconv.FormatUint(it.Assignee, 10)
	case "process":
		return it.Module
	case "type":
		return it.Category
	}
	sg, _ := it.StatusAt(in.W.To.Add(-time.Nanosecond))
	return it.Module + "|" + sg.Status
}

// ---------------------------------------------------------------------
// Throughput

type ThroughputView struct {
	Unit    string
	Rows    []ThroughputRow
	Max     int
	MaxWIP  int
	Totals  ThroughputRow
	ByDim   []DimChange
	DimName string
}

type ThroughputRow struct {
	Label              string
	From               time.Time
	Started, Completed int
	Net                int
	Backlog            int
	CompletionRatio    float64
	MovingAvg          float64
	Change             string
	Link               string
}

// DimChange compares one group between the window and the previous one
type DimChange struct {
	Name      string
	Cur, Prev int
	Delta     string
	Dir       string
	Link      string
}

// Throughput reports started and completed work per interval
func (in *Intel) Throughput(unit, dim string) ThroughputView {
	var buckets []Window
	w := in.W
	switch unit {
	case "day", "week", "month":
		buckets = bucketsBy(w, unit)
	default:
		buckets, unit = Buckets(w)
		if unit == "hour" {
			buckets, unit = bucketsBy(w, "day"), "day"
		}
	}
	v := ThroughputView{Unit: unit}
	var completed []int
	for i, b := range buckets {
		r := ThroughputRow{From: b.From, Label: bucketLabel(b.From, unit)}
		for _, it := range in.Items {
			if b.Has(it.CreatedAt) {
				r.Started++
			}
			if it.Done() && b.Has(*it.CompletedAt) {
				r.Completed++
			}
			if it.OpenAt(b.To) {
				r.Backlog++
			}
		}
		r.Net = r.Started - r.Completed
		if r.Started > 0 {
			r.CompletionRatio = float64(r.Completed) / float64(r.Started) * 100
		}
		completed = append(completed, r.Completed)
		n := 0
		for k := len(completed) - 1; k >= 0 && k >= len(completed)-3; k-- {
			r.MovingAvg += float64(completed[k])
			n++
		}
		r.MovingAvg /= float64(n)
		if i > 0 {
			r.Change, _ = deltaText(float64(r.Completed), float64(completed[i-1]), "n")
		}
		r.Link = in.Scope.URL("/command/records", "set", "completed", "range", "custom",
			"from", b.From.Format("2006-01-02"), "to", b.To.Add(-time.Second).Format("2006-01-02"))
		if r.Started > v.Max {
			v.Max = r.Started
		}
		if r.Completed > v.Max {
			v.Max = r.Completed
		}
		if r.Backlog > v.MaxWIP {
			v.MaxWIP = r.Backlog
		}
		v.Totals.Started += r.Started
		v.Totals.Completed += r.Completed
		v.Rows = append(v.Rows, r)
	}
	v.Totals.Net = v.Totals.Started - v.Totals.Completed
	if v.Totals.Started > 0 {
		v.Totals.CompletionRatio = float64(v.Totals.Completed) / float64(v.Totals.Started) * 100
	}

	v.DimName, v.ByDim = in.completedBy(dim)
	return v
}

// completedBy compares completions per group with the previous period
func (in *Intel) completedBy(dim string) (string, []DimChange) {
	key := func(it *WorkItem) (string, string) {
		switch dim {
		case "department":
			return in.Lk.department(it.Department), "department=" + strconv.FormatUint(it.Department, 10)
		case "team":
			return in.Lk.team(it.Team), "team=" + strconv.FormatUint(it.Team, 10)
		case "assignee":
			return in.Lk.person(it.Assignee), "assignee=" + strconv.FormatUint(it.Assignee, 10)
		case "type":
			return orStr(it.Category, "No type"), "type=" + it.Category
		}
		return it.stage.Label, "workflow=" + it.Module
	}
	name := map[string]string{"department": "Department", "team": "Team", "assignee": "Assignee", "type": "Type"}[dim]
	if name == "" {
		name = "Workflow"
	}
	cur, prev := map[string]int{}, map[string]int{}
	links := map[string]string{}
	for _, it := range in.Items {
		if !it.Done() {
			continue
		}
		k, l := key(it)
		links[k] = l
		switch {
		case in.W.Has(*it.CompletedAt):
			cur[k]++
		case in.Prev.Has(*it.CompletedAt):
			prev[k]++
		}
	}
	var out []DimChange
	for k, l := range links {
		if cur[k] == 0 && prev[k] == 0 {
			continue
		}
		d, dir := deltaText(float64(cur[k]), float64(prev[k]), "n")
		kv := strings.SplitN(l, "=", 2)
		out = append(out, DimChange{Name: k, Cur: cur[k], Prev: prev[k], Delta: d, Dir: dir,
			Link: in.Scope.URL("/command/records", "set", "completed", kv[0], kv[1])})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Cur > out[j].Cur })
	return name, out
}

func bucketsBy(w Window, unit string) []Window {
	var out []Window
	cur := dayOf(w.From)
	step := func(t time.Time) time.Time { return t.AddDate(0, 0, 1) }
	switch unit {
	case "week":
		cur = cur.AddDate(0, 0, -((int(cur.Weekday()) + 6) % 7))
		step = func(t time.Time) time.Time { return t.AddDate(0, 0, 7) }
	case "month":
		cur = time.Date(cur.Year(), cur.Month(), 1, 0, 0, 0, 0, time.UTC)
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
	return out
}

func bucketLabel(t time.Time, unit string) string {
	switch unit {
	case "hour":
		return t.Format("15:04")
	case "week":
		return "Wk " + t.Format("Jan 2")
	case "month":
		return t.Format("Jan 2006")
	}
	return t.Format("Mon Jan 2")
}

// ---------------------------------------------------------------------
// Capacity / load

type LoadRow struct {
	ID        uint64
	Name      string
	Link      string
	Assigned  int
	Active    int
	Overdue   int
	Completed int
	Cycle     Stats
	SLA       float64
	HasSLA    bool
	WIPShare  float64
	Incoming7 int
	Done7     int
	Blocked   int
	Aging     int
}

type CapacityView struct {
	People, Teams, Departments []LoadRow
	TotalActive                int
	Unassigned                 int
	TopShare                   float64
	TopCount                   int
}

// Capacity shows how work is distributed across people, teams and departments
func (in *Intel) Capacity() CapacityView {
	var v CapacityView
	end := in.W.To
	week := Window{end.AddDate(0, 0, -7), end}

	type acc struct {
		row         LoadRow
		cycles      []float64
		slaN, slaOK int
	}
	groups := map[string]map[uint64]*acc{"person": {}, "team": {}, "department": {}}
	get := func(kind string, id uint64) *acc {
		a := groups[kind][id]
		if a == nil {
			a = &acc{row: LoadRow{ID: id}}
			switch kind {
			case "person":
				a.row.Name = in.Lk.person(id)
				a.row.Link = in.Scope.URL(fmt.Sprintf("/command/person/%d", id))
			case "team":
				a.row.Name = in.Lk.team(id)
				a.row.Link = in.Scope.URL(fmt.Sprintf("/command/team/%d", id))
			default:
				a.row.Name = in.Lk.department(id)
				a.row.Link = in.Scope.URL(fmt.Sprintf("/command/department/%d", id))
			}
			groups[kind][id] = a
		}
		return a
	}

	for _, it := range in.Items {
		activeInWindow := it.CreatedAt.Before(end) && (it.CompletedAt == nil || !it.CompletedAt.Before(in.W.From))
		if !activeInWindow {
			continue
		}
		open := it.OpenAt(end)
		if open {
			v.TotalActive++
			if it.Assignee == 0 {
				v.Unassigned++
			}
		}
		for kind, id := range map[string]uint64{"person": it.Assignee, "team": it.Team, "department": it.Department} {
			if kind == "person" && id == 0 {
				continue
			}
			a := get(kind, id)
			a.row.Assigned++
			if open {
				a.row.Active++
				if it.DueAt != nil && end.After(it.DueAt.Add(24*time.Hour)) {
					a.row.Overdue++
				}
				if sg, ok := it.StatusAt(end.Add(-time.Nanosecond)); ok && sg.Kind == "blocked" {
					a.row.Blocked++
				}
				if end.Sub(it.CreatedAt) > agingThreshold {
					a.row.Aging++
				}
			}
			if it.Done() && in.W.Has(*it.CompletedAt) {
				a.row.Completed++
				a.cycles = append(a.cycles, it.Cycle().Hours())
				if c := in.sla(it); c.slaApplicable {
					a.slaN++
					if c.breachAt == nil || c.breachAt.After(*it.CompletedAt) {
						a.slaOK++
					}
				}
			}
			if it.Done() && week.Has(*it.CompletedAt) {
				a.row.Done7++
			}
		}
		for _, as := range it.Assignments {
			if week.Has(as.At) && as.To > 0 {
				get("person", as.To).row.Incoming7++
			}
		}
	}

	for kind, m := range groups {
		var rows []LoadRow
		for _, a := range m {
			a.row.Cycle = statsOf(a.cycles)
			if a.slaN > 0 {
				a.row.HasSLA, a.row.SLA = true, float64(a.slaOK)/float64(a.slaN)*100
			}
			if v.TotalActive > 0 {
				a.row.WIPShare = float64(a.row.Active) / float64(v.TotalActive) * 100
			}
			rows = append(rows, a.row)
		}
		sort.Slice(rows, func(i, j int) bool {
			if rows[i].Active != rows[j].Active {
				return rows[i].Active > rows[j].Active
			}
			return rows[i].Name < rows[j].Name
		})
		switch kind {
		case "person":
			v.People = rows
		case "team":
			v.Teams = rows
		default:
			v.Departments = rows
		}
	}

	if n := len(v.People); n >= 5 && v.TotalActive > 0 {
		k := (n + 4) / 5
		sum := 0
		for _, p := range v.People[:k] {
			sum += p.Active
		}
		v.TopCount = k
		if d := v.TotalActive - v.Unassigned; d > 0 {
			v.TopShare = float64(sum) / float64(d) * 100
		}
	}
	return v
}

// ---------------------------------------------------------------------
// Handoffs

type HandoffRow struct {
	From, To       string
	FromID, ToID   uint64
	Count          int
	Wait           Stats
	BreachRate     float64
	BaseBreachRate float64
	ShareOfDelay   float64
	ReworkAfter    float64
	Link           string
}

type StageHandoff struct {
	Module, Label string
	From, To      string
	Count         int
	Queue         Stats // time in the destination before the next move
	Link          string
}

type HandoffView struct {
	By        string // person | team | department
	Rows      []HandoffRow
	Stages    []StageHandoff
	Total     int
	TotalWait float64
	Cycle     float64
	Share     float64
	Wait      Stats
}

// Handoffs analyzes changes of owner and stage-to-stage transitions
func (in *Intel) Handoffs(by string) HandoffView {
	switch by {
	case "team", "department":
	default:
		by = "person"
	}
	v := HandoffView{By: by}
	w := in.W

	group := func(person uint64) (uint64, string) {
		switch by {
		case "team":
			t := in.primary[person][0]
			return t, in.Lk.team(t)
		case "department":
			d := in.primary[person][1]
			return d, in.Lk.department(d)
		}
		return person, in.Lk.person(person)
	}

	type acc struct {
		row              HandoffRow
		waits            []float64
		breached, rework int
		items            map[uint64]bool
	}
	rows := map[[2]uint64]*acc{}
	var all []float64
	var withSLA, withBreach, baseSLA, baseBreach int
	handed := map[uint64]bool{}

	for _, it := range in.Items {
		c := in.sla(it)
		if it.Done() && w.Has(*it.CompletedAt) {
			v.Cycle += it.Cycle().Hours()
		}
		for _, h := range it.Handoffs() {
			if !w.Has(h.At) {
				continue
			}
			handed[it.ID] = true
			fid, fname := group(h.From)
			tid, tname := group(h.To)
			k := [2]uint64{fid, tid}
			a := rows[k]
			if a == nil {
				a = &acc{row: HandoffRow{From: fname, To: tname, FromID: fid, ToID: tid}, items: map[uint64]bool{}}
				rows[k] = a
			}
			wait := h.Wait(in.Now).Hours()
			a.row.Count++
			a.waits = append(a.waits, wait)
			all = append(all, wait)
			v.Total++
			v.TotalWait += wait
			a.items[it.ID] = true
			if c.breachAt != nil {
				a.breached++
			}
			if h.Rework {
				a.rework++
			}
		}
	}
	for _, it := range in.Items {
		c := in.sla(it)
		if !c.slaApplicable {
			continue
		}
		if handed[it.ID] {
			withSLA++
			if c.breachAt != nil {
				withBreach++
			}
		} else {
			baseSLA++
			if c.breachAt != nil {
				baseBreach++
			}
		}
	}

	base := 0.0
	if baseSLA > 0 {
		base = float64(baseBreach) / float64(baseSLA) * 100
	}
	for _, a := range rows {
		a.row.Wait = statsOf(a.waits)
		if a.row.Count > 0 {
			a.row.BreachRate = float64(a.breached) / float64(a.row.Count) * 100
			a.row.ReworkAfter = float64(a.rework) / float64(a.row.Count) * 100
		}
		a.row.BaseBreachRate = base
		if v.Cycle > 0 {
			a.row.ShareOfDelay = a.row.Wait.Avg * float64(a.row.Count) / v.Cycle * 100
		}
		a.row.Link = in.Scope.URL("/command/records", "set", "handoffs", "by", by,
			"hfrom", strconv.FormatUint(a.row.FromID, 10), "hto", strconv.FormatUint(a.row.ToID, 10))
		v.Rows = append(v.Rows, a.row)
	}
	sort.Slice(v.Rows, func(i, j int) bool {
		return v.Rows[i].Wait.Avg*float64(v.Rows[i].Count) > v.Rows[j].Wait.Avg*float64(v.Rows[j].Count)
	})
	v.Wait = statsOf(all)
	if v.Cycle > 0 {
		v.Share = v.TotalWait / v.Cycle * 100
	}

	// stage transitions
	for _, ps := range in.Stages() {
		for _, e := range ps.Edges {
			if e.Backward || ps.Row(e.To) == nil || ps.Row(e.To).Terminal {
				continue
			}
			sh := StageHandoff{Module: ps.Module, Label: ps.Label, From: e.From, To: e.To, Count: e.Count,
				Link: in.Scope.URL("/command/records", "set", "transition", "workflow", ps.Module, "tfrom", e.From, "tto", e.To)}
			var q []float64
			for _, it := range in.Items {
				if it.Module != ps.Module {
					continue
				}
				for i := 1; i < len(it.Segments); i++ {
					prev, sg := it.Segments[i-1], it.Segments[i]
					if prev.Status == e.From && sg.Status == e.To && w.Has(sg.Start) {
						q = append(q, sg.Dur(in.Now).Hours())
					}
				}
			}
			sh.Queue = statsOf(q)
			v.Stages = append(v.Stages, sh)
		}
	}
	sort.Slice(v.Stages, func(i, j int) bool {
		return v.Stages[i].Queue.Median*float64(v.Stages[i].Count) > v.Stages[j].Queue.Median*float64(v.Stages[j].Count)
	})
	return v
}

// ---------------------------------------------------------------------
// Rework

type LoopPath struct {
	Module, Label string
	Path          string
	Count         int
	Extra         Stats
	ShareOfDelay  float64
	Items         int
	Link          string
}

type ReworkView struct {
	Reopened, Backward     int
	RepeatApprovals        int
	RepeatReviews          int
	RepeatCorrections      int
	ReworkItems, Completed int
	Rate                   float64
	LostH                  float64
	CycleH                 float64
	Paths                  []LoopPath
	ByProcess              []NameCount
	ByTeam                 []NameCount
	ByPerson               []NameCount
	Extra                  Stats
}

// Rework analyzes backward movement and the time it cost
func (in *Intel) Rework() ReworkView {
	var v ReworkView
	w := in.W
	type acc struct {
		lp    LoopPath
		extra []float64
		items map[uint64]bool
	}
	paths := map[string]*acc{}
	proc, teams, people := map[string]int{}, map[uint64]int{}, map[uint64]int{}
	var extra []float64

	for _, it := range in.Items {
		if it.Done() && w.Has(*it.CompletedAt) {
			v.Completed++
			v.CycleH += it.Cycle().Hours()
			if len(it.Loops) > 0 {
				v.ReworkItems++
			}
		}
		for _, l := range it.Loops {
			if !w.Has(l.At) {
				continue
			}
			v.Backward++
			if it.stage.terminal(statusOrEmpty(l.From)) {
				v.Reopened++
			}
			switch it.Module {
			case "Approval":
				v.RepeatApprovals++
			case "OperationsRecord":
				v.RepeatReviews++
			case "Case":
				v.RepeatCorrections++
			}
			d := l.Dur(in.Now).Hours()
			v.LostH += d
			extra = append(extra, d)
			label := strings.Join(l.Path, " → ")
			k := it.Module + "|" + label
			a := paths[k]
			if a == nil {
				a = &acc{lp: LoopPath{Module: it.Module, Label: it.stage.Label, Path: label}, items: map[uint64]bool{}}
				paths[k] = a
			}
			a.lp.Count++
			a.extra = append(a.extra, d)
			a.items[it.ID] = true
			proc[it.stage.Label]++
			teams[it.Team]++
			sg, _ := it.StatusAt(l.At.Add(-time.Nanosecond))
			who := sg.Assignee
			if who == 0 {
				who = it.Assignee
			}
			people[who]++
		}
	}
	if v.Completed > 0 {
		v.Rate = float64(v.ReworkItems) / float64(v.Completed) * 100
	}
	v.Extra = statsOf(extra)
	for _, a := range paths {
		a.lp.Extra = statsOf(a.extra)
		a.lp.Items = len(a.items)
		if v.CycleH > 0 {
			a.lp.ShareOfDelay = sum(a.extra) / v.CycleH * 100
		}
		a.lp.Link = in.Scope.URL("/command/records", "set", "loop", "workflow", a.lp.Module, "loop", a.lp.Path)
		v.Paths = append(v.Paths, a.lp)
	}
	sort.Slice(v.Paths, func(i, j int) bool { return v.Paths[i].Count > v.Paths[j].Count })
	for name, c := range proc {
		v.ByProcess = append(v.ByProcess, NameCount{Name: name, Count: c})
	}
	sort.Slice(v.ByProcess, func(i, j int) bool { return v.ByProcess[i].Count > v.ByProcess[j].Count })
	v.ByTeam = in.topNames(teams, 6, "team")
	v.ByPerson = in.topNames(people, 6, "person")
	return v
}

func sum(v []float64) float64 {
	s := 0.0
	for _, x := range v {
		s += x
	}
	return s
}

// ---------------------------------------------------------------------
// Process review (paths)

type PathRow struct {
	Path   string
	Count  int
	Share  float64
	Cycle  Stats
	Done   int
	Rework bool
	Open   int
	Link   string
}

type ProcessReview struct {
	Module, Label   string
	Items           int
	Completed       int
	CompletionRate  float64
	Paths           []PathRow
	Fastest         *PathRow
	Slowest         *PathRow
	ReworkPaths     []PathRow
	Stalled         int
	StalledLink     string
	Skipped         int
	SkippedLink     string
	LoopsPerItem    float64
	HandoffsPerItem float64
	Variance        []StageVariance
	Histogram       []HistBin
	HistMax         int
	Contribution    []NameShare
	Stages          ProcessStages
}

type StageVariance struct {
	Status string
	Mean   float64
	SD     float64
	CV     float64
	N      int
}

type HistBin struct {
	Label string
	Count int
	Link  string
}

type NameShare struct {
	Name  string
	Share float64
	Hours float64
}

var histBins = []struct {
	Label string
	MaxH  float64
}{{"< 4h", 4}, {"4–8h", 8}, {"8–24h", 24}, {"1–2d", 48}, {"2–4d", 96}, {"4–7d", 168}, {"1–2w", 336}, {"2w+", math.Inf(1)}}

// stalledAfter is how long without any activity makes open work "stalled"
const stalledAfter = 30 * 24 * time.Hour

// Process reviews how one workflow actually behaves
func (in *Intel) Process(module string) ProcessReview {
	st := stageFor(module)
	pr := ProcessReview{Module: module}
	if st == nil {
		return pr
	}
	pr.Label = st.Label
	w := in.W

	type acc struct {
		row    PathRow
		cycles []float64
	}
	paths := map[string]*acc{}
	var loops, handoffs int
	stageTimes := map[string][]float64{}
	pr.Histogram = make([]HistBin, len(histBins))
	for i, b := range histBins {
		pr.Histogram[i].Label = b.Label
	}
	contrib := map[string]float64{}
	var contribTotal float64

	for _, it := range in.Items {
		if it.Module != module {
			continue
		}
		inScope := (it.Done() && w.Has(*it.CompletedAt)) || (it.OpenAt(w.To) && it.CreatedAt.Before(w.To)) || w.Has(it.CreatedAt)
		if !inScope {
			continue
		}
		pr.Items++
		loops += len(it.Loops)
		handoffs += len(it.Handoffs())
		p := strings.Join(it.Path, " → ")
		a := paths[p]
		if a == nil {
			a = &acc{row: PathRow{Path: p}}
			paths[p] = a
		}
		a.row.Count++
		if len(it.Loops) > 0 {
			a.row.Rework = true
		}
		if it.Done() && w.Has(*it.CompletedAt) {
			pr.Completed++
			a.row.Done++
			c := it.Cycle().Hours()
			a.cycles = append(a.cycles, c)
			for i, b := range histBins {
				if c < b.MaxH {
					pr.Histogram[i].Count++
					break
				}
			}
			for _, sg := range it.Segments {
				if sg.Kind == "done" || sg.Kind == "failed" {
					continue
				}
				d := sg.Dur(in.Now).Hours()
				stageTimes[sg.Status] = append(stageTimes[sg.Status], d)
				contrib[sg.Status] += d
				contribTotal += d
			}
			if len(st.Work) > 0 && it.Work == 0 {
				pr.Skipped++
			}
		}
		if it.OpenAt(w.To) {
			a.row.Open++
			if w.To.Sub(it.LastEventAt) > stalledAfter {
				pr.Stalled++
			}
		}
	}
	if pr.Items == 0 {
		return pr
	}
	pr.CompletionRate = float64(pr.Completed) / float64(pr.Items) * 100
	pr.LoopsPerItem = float64(loops) / float64(pr.Items)
	pr.HandoffsPerItem = float64(handoffs) / float64(pr.Items)
	pr.StalledLink = in.Scope.URL("/command/records", "set", "stalled", "workflow", module)
	pr.SkippedLink = in.Scope.URL("/command/records", "set", "skipped", "workflow", module)

	for _, a := range paths {
		a.row.Cycle = statsOf(a.cycles)
		a.row.Share = float64(a.row.Count) / float64(pr.Items) * 100
		a.row.Link = in.Scope.URL("/command/records", "set", "path", "workflow", module, "path", a.row.Path)
		pr.Paths = append(pr.Paths, a.row)
	}
	sort.Slice(pr.Paths, func(i, j int) bool { return pr.Paths[i].Count > pr.Paths[j].Count })

	minCommon := int(math.Max(3, math.Ceil(0.05*float64(pr.Items))))
	for i := range pr.Paths {
		p := &pr.Paths[i]
		if p.Cycle.N < minCommon {
			continue
		}
		if pr.Fastest == nil || p.Cycle.Median < pr.Fastest.Cycle.Median {
			cp := *p
			pr.Fastest = &cp
		}
		if pr.Slowest == nil || p.Cycle.Median > pr.Slowest.Cycle.Median {
			cp := *p
			pr.Slowest = &cp
		}
	}
	if pr.Fastest != nil && pr.Slowest != nil && pr.Fastest.Path == pr.Slowest.Path {
		pr.Slowest = nil
	}
	for _, p := range pr.Paths {
		if p.Rework {
			pr.ReworkPaths = append(pr.ReworkPaths, p)
		}
	}
	if len(pr.Paths) > 10 {
		pr.Paths = pr.Paths[:10]
	}
	if len(pr.ReworkPaths) > 6 {
		pr.ReworkPaths = pr.ReworkPaths[:6]
	}

	for status, vals := range stageTimes {
		m := mean(vals)
		var sq float64
		for _, x := range vals {
			sq += (x - m) * (x - m)
		}
		sd := math.Sqrt(sq / float64(len(vals)))
		sv := StageVariance{Status: status, Mean: m, SD: sd, N: len(vals)}
		if m > 0 {
			sv.CV = sd / m
		}
		pr.Variance = append(pr.Variance, sv)
	}
	sort.Slice(pr.Variance, func(i, j int) bool { return pr.Variance[i].CV > pr.Variance[j].CV })

	for status, h := range contrib {
		if contribTotal > 0 {
			pr.Contribution = append(pr.Contribution, NameShare{Name: status, Hours: h, Share: h / contribTotal * 100})
		}
	}
	sort.Slice(pr.Contribution, func(i, j int) bool { return pr.Contribution[i].Share > pr.Contribution[j].Share })

	for i := range pr.Histogram {
		if pr.Histogram[i].Count > pr.HistMax {
			pr.HistMax = pr.Histogram[i].Count
		}
		pr.Histogram[i].Link = in.Scope.URL("/command/records", "set", "cycle_bin", "workflow", module, "bin", strconv.Itoa(i))
	}
	pr.Stages = in.StagesOf(module)
	return pr
}

// BusiestModule is the workflow with the most records in scope
func (in *Intel) BusiestModule() string {
	counts := map[string]int{}
	for _, it := range in.Items {
		counts[it.Module]++
	}
	best := ""
	for _, st := range pipelineStages {
		if best == "" || counts[st.Module] > counts[best] {
			best = st.Module
		}
	}
	return best
}
