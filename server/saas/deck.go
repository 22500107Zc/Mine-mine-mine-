package saas

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// The Command Deck answers, from a company's own workspace activity only:
// what is happening, where, why it might be happening, what it affects and
// what to test next. Measured facts and correlations are always labeled as
// such; nothing is shown when there is not enough data to support it.

// stageDef classifies the statuses of one kind of work
type stageDef struct {
	Module string
	Label  string
	Wait   map[string]bool
	Work   map[string]bool
	Done   map[string]bool
	Failed map[string]bool
}

func set(ss ...string) map[string]bool {
	m := map[string]bool{}
	for _, s := range ss {
		m[s] = true
	}
	return m
}

var pipelineStages = []stageDef{
	{Module: "Task", Label: "Tasks", Wait: set("", "Open", "Waiting"), Work: set("In Progress"), Done: set("Done"), Failed: set()},
	{Module: "Case", Label: "Cases", Wait: set("", "New", "Pending"), Work: set("Open"), Done: set("Resolved", "Closed"), Failed: set()},
	{Module: "Approval", Label: "Approvals", Wait: set("", "Pending"), Work: set(), Done: set("Approved"), Failed: set("Rejected")},
	{Module: "OperationsRecord", Label: "Records", Wait: set("", "Open"), Work: set("In Review"), Done: set("Closed"), Failed: set()},
}

// module labels for activity lists
var moduleLabels = map[string]string{
	"Task": "Task", "Case": "Case", "Approval": "Approval", "OperationsRecord": "Record",
	"Customer": "Customer", "Contact": "Contact", "Document": "Document", "Department": "Department",
}

func stageFor(module string) *stageDef {
	for i := range pipelineStages {
		if pipelineStages[i].Module == module {
			return &pipelineStages[i]
		}
	}
	return nil
}

// Minimum sample sizes before a finding is shown
const (
	minItemsForAnalysis = 10
	minDaysForAnomaly   = 14
	minItemsForCorr     = 20
)

type (
	Deck struct {
		Now         time.Time
		TotalEvents int
		HasData     bool
		Enough      bool // enough items for bottleneck analysis

		KPI      DeckKPI
		Heatmap  Heatmap
		Stages   []StageStat
		Findings []Finding
		Impact   ImpactTable
		Recs     []Recommendation

		OpenItems    []*WorkItem // oldest open first
		OverdueItems []*WorkItem
	}

	DeckKPI struct {
		Act7, Prev7                 int
		Act30, Prev30               int
		Items, Completed, Failed    int
		CompletionRate              float64
		CurrentStreak, LongestStrk  int
		AvgCompletionHours          float64
		Throughput, PrevThroughput  float64
		Created30, Completed30      int
		OpenNow, OverdueNow         int
		HasCompletion, HasBaseline7 bool
	}

	Heatmap struct {
		Weeks       [][]HeatDay // columns of 7 days, Monday first
		Months      []HeatMonth
		Peak        int
		BestWeek    WeekTotal
		LowestWeek  WeekTotal
		HasWeeks    bool
		WorkedDays  float64
		ActiveDays  int
		EventsCount int
	}

	HeatDay struct {
		Date   time.Time
		Count  int
		Level  int
		Future bool
	}

	HeatMonth struct {
		Label string
		Col   int
	}

	WeekTotal struct {
		Start time.Time
		Count int
	}

	StageStat struct {
		Module, Label       string
		Passes              int
		AvgWaitH, AvgWorkH  float64
		P90WaitH            float64
		WaitShare           float64 // share of this stage's time spent waiting
		Share               float64 // share of total cycle time across stages
		People              int
		BreachRate          float64
		ReworkRate          float64
		OpenNow             int
		Severity            float64
		Bar                 float64 // 0..100 relative to the most severe stage
		Completed, Failed   int
		AvgCycleH           float64
		totalWait, totalWrk time.Duration
	}

	Finding struct {
		Title    string
		Detail   string
		Kind     string // measured | correlation
		Dates    []time.Time
		Evidence string
	}

	ImpactTable struct {
		Dimension string // Department | Work type
		Rows      []ImpactRow
		Slowest   *ImpactRow
	}

	ImpactRow struct {
		Name       string
		Items      int
		AvgCycleH  float64
		ReworkRate float64
		Failed     int
		completed  int
	}

	Recommendation struct {
		Rank       int
		Title      string
		Expected   string
		Evidence   string
		HowToTest  string
		Confidence int
		Basis      string // measured | correlation
		score      float64
	}

	// WorkItem is one record reconstructed from its events
	WorkItem struct {
		Module, Title       string
		ID                  uint64
		Status              string
		CreatedAt           time.Time
		CompletedAt         *time.Time
		Failed, Deleted     bool
		Wait, Work          time.Duration
		Reopens             int
		DueAt               *time.Time
		Breached            bool
		Department          uint64
		People              map[uint64]bool
		passesWait          []time.Duration
		lastStatusChangeAt  time.Time
		completedDay        time.Time
		createdWeekday      time.Weekday
		stage               *stageDef
		AgeH                float64
		DepartmentName      string
		completionHandlerCt int
	}
)

func hours(d time.Duration) float64 { return d.Hours() }

func dayOf(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// BuildDeck computes the Command Deck from a company's activity events
// (oldest first). departments resolves department IDs to names.
func BuildDeck(events []ActivityEvent, now time.Time, departments map[uint64]string) *Deck {
	now = now.UTC()
	d := &Deck{Now: now, TotalEvents: len(events), HasData: len(events) > 0}

	items := reconstruct(events, now, departments)
	d.KPI = kpis(events, items, now)
	d.Heatmap = heatmap(events, items, now)

	var tracked []*WorkItem
	for _, it := range items {
		if it.stage != nil && !it.Deleted {
			tracked = append(tracked, it)
		}
	}

	d.Enough = len(tracked) >= minItemsForAnalysis
	d.Stages = stages(tracked)
	d.Impact = impact(tracked, departments)
	d.Findings = findings(d, tracked, now)
	d.Recs = recommendations(d, tracked)

	for _, it := range tracked {
		if it.CompletedAt == nil && !it.Failed {
			it.AgeH = now.Sub(it.CreatedAt).Hours()
			d.OpenItems = append(d.OpenItems, it)
			if it.DueAt != nil && now.After(*it.DueAt) {
				d.OverdueItems = append(d.OverdueItems, it)
			}
		}
	}
	sort.Slice(d.OpenItems, func(i, j int) bool { return d.OpenItems[i].CreatedAt.Before(d.OpenItems[j].CreatedAt) })
	sort.Slice(d.OverdueItems, func(i, j int) bool { return d.OverdueItems[i].DueAt.Before(*d.OverdueItems[j].DueAt) })
	if len(d.OpenItems) > 15 {
		d.OpenItems = d.OpenItems[:15]
	}
	if len(d.OverdueItems) > 15 {
		d.OverdueItems = d.OverdueItems[:15]
	}

	return d
}

// reconstruct replays events into work items with time spent per state
func reconstruct(events []ActivityEvent, now time.Time, departments map[uint64]string) []*WorkItem {
	byID := map[uint64]*WorkItem{}
	var order []*WorkItem

	for _, e := range events {
		it := byID[e.RecordID]
		if it == nil {
			it = &WorkItem{Module: e.Module, ID: e.RecordID, CreatedAt: e.OccurredAt, People: map[uint64]bool{},
				stage: stageFor(e.Module), lastStatusChangeAt: e.OccurredAt, createdWeekday: e.OccurredAt.Weekday()}
			byID[e.RecordID] = it
			order = append(order, it)
		}

		if e.Title != "" {
			it.Title = e.Title
		}
		if e.AssigneeID > 0 {
			it.People[e.AssigneeID] = true
		}
		if e.ActorID > 0 {
			it.People[e.ActorID] = true
		}
		if e.DepartmentID > 0 {
			it.Department = e.DepartmentID
		}
		if e.DueAt != nil {
			it.DueAt = e.DueAt
		}

		switch e.Kind {
		case ActivityDeleted:
			it.Deleted = true
			continue
		case ActivityCreated:
			it.Status = e.ToStatus
			it.lastStatusChangeAt = e.OccurredAt
			continue
		case ActivityStatus:
		default:
			continue
		}

		if it.stage == nil {
			it.Status = e.ToStatus
			continue
		}

		// close the interval spent in the previous status
		it.account(e.OccurredAt)

		wasTerminal := it.stage.Done[it.Status] || it.stage.Failed[it.Status]
		it.Status = e.ToStatus
		it.lastStatusChangeAt = e.OccurredAt

		switch {
		case it.stage.Done[e.ToStatus]:
			t := e.OccurredAt
			it.CompletedAt, it.Failed = &t, false
		case it.stage.Failed[e.ToStatus]:
			t := e.OccurredAt
			it.CompletedAt, it.Failed = &t, true
		default:
			if wasTerminal {
				it.Reopens++
				it.CompletedAt, it.Failed = nil, false
			}
		}
	}

	for _, it := range order {
		if it.stage != nil && it.CompletedAt == nil && !it.Deleted {
			it.account(now)
		}
		if it.CompletedAt != nil {
			it.completedDay = dayOf(*it.CompletedAt)
		}
		if it.DueAt != nil {
			due := it.DueAt.Add(24 * time.Hour) // due dates are whole days
			if it.CompletedAt != nil {
				it.Breached = it.CompletedAt.After(due)
			} else {
				it.Breached = now.After(due)
			}
		}
		it.DepartmentName = departments[it.Department]
	}

	return order
}

// account adds the time since the last status change to wait or work
func (it *WorkItem) account(until time.Time) {
	dur := until.Sub(it.lastStatusChangeAt)
	if dur <= 0 || it.stage == nil {
		return
	}

	switch {
	case it.stage.Done[it.Status] || it.stage.Failed[it.Status]:
	case it.stage.Wait[it.Status]:
		it.Wait += dur
		it.passesWait = append(it.passesWait, dur)
	default:
		// "work" statuses and any custom status the company added
		it.Work += dur
	}
}

func kpis(events []ActivityEvent, items []*WorkItem, now time.Time) DeckKPI {
	var k DeckKPI
	today := dayOf(now)

	days := map[time.Time]int{}
	for _, e := range events {
		age := now.Sub(e.OccurredAt)
		switch {
		case age < 7*24*time.Hour:
			k.Act7++
		case age < 14*24*time.Hour:
			k.Prev7++
		}
		switch {
		case age < 30*24*time.Hour:
			k.Act30++
		case age < 60*24*time.Hour:
			k.Prev30++
		}
		days[dayOf(e.OccurredAt)]++
	}
	k.HasBaseline7 = k.Prev7 > 0

	// streaks: consecutive days with activity (today may still be empty)
	cur := today
	if days[cur] == 0 {
		cur = cur.AddDate(0, 0, -1)
	}
	for days[cur] > 0 {
		k.CurrentStreak++
		cur = cur.AddDate(0, 0, -1)
	}

	var sorted []time.Time
	for dd := range days {
		sorted = append(sorted, dd)
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Before(sorted[j]) })
	run := 0
	for i, dd := range sorted {
		if i > 0 && dd.Sub(sorted[i-1]) == 24*time.Hour {
			run++
		} else {
			run = 1
		}
		if run > k.LongestStrk {
			k.LongestStrk = run
		}
	}

	var cycle float64
	var done30, donePrev30 int
	for _, it := range items {
		if it.stage == nil || it.Deleted {
			continue
		}
		k.Items++
		if now.Sub(it.CreatedAt) < 30*24*time.Hour {
			k.Created30++
		}
		switch {
		case it.CompletedAt != nil && it.Failed:
			k.Failed++
		case it.CompletedAt != nil:
			k.Completed++
			cycle += it.CompletedAt.Sub(it.CreatedAt).Hours()
			age := now.Sub(*it.CompletedAt)
			if age < 30*24*time.Hour {
				done30++
			} else if age < 60*24*time.Hour {
				donePrev30++
			}
		default:
			k.OpenNow++
			if it.Breached {
				k.OverdueNow++
			}
		}
	}

	if k.Items > 0 {
		k.CompletionRate = float64(k.Completed) / float64(k.Items) * 100
	}
	if k.Completed > 0 {
		k.HasCompletion = true
		k.AvgCompletionHours = cycle / float64(k.Completed)
	}
	k.Completed30 = done30
	k.Throughput = float64(done30) / 30
	k.PrevThroughput = float64(donePrev30) / 30
	return k
}

func heatmap(events []ActivityEvent, items []*WorkItem, now time.Time) Heatmap {
	var h Heatmap
	today := dayOf(now)

	// 53 columns ending with the current week, Monday first
	offset := (int(today.Weekday()) + 6) % 7
	start := today.AddDate(0, 0, -offset-52*7)

	counts := map[time.Time]int{}
	for _, e := range events {
		dd := dayOf(e.OccurredAt)
		if !dd.Before(start) && !dd.After(today) {
			counts[dd]++
			h.EventsCount++
		}
	}

	var nonzero []int
	for _, c := range counts {
		if c > 0 {
			nonzero = append(nonzero, c)
		}
		if c > h.Peak {
			h.Peak = c
		}
	}
	h.ActiveDays = len(nonzero)
	sort.Ints(nonzero)
	q := func(p float64) int {
		if len(nonzero) == 0 {
			return 0
		}
		return nonzero[int(math.Min(float64(len(nonzero)-1), math.Floor(p*float64(len(nonzero)))))]
	}
	q1, q2, q3 := q(0.25), q(0.5), q(0.75)
	level := func(c int) int {
		switch {
		case c == 0:
			return 0
		case c <= q1:
			return 1
		case c <= q2:
			return 2
		case c <= q3:
			return 3
		default:
			return 4
		}
	}

	var weeks []WeekTotal
	firstActive := -1
	lastMonth := -1
	for w := 0; w < 53; w++ {
		col := make([]HeatDay, 7)
		wt := WeekTotal{Start: start.AddDate(0, 0, w*7)}
		for i := 0; i < 7; i++ {
			dd := start.AddDate(0, 0, w*7+i)
			c := counts[dd]
			col[i] = HeatDay{Date: dd, Count: c, Level: level(c), Future: dd.After(today)}
			wt.Count += c
			if dd.Day() <= 7 && i == 0 && int(dd.Month()) != lastMonth {
				h.Months = append(h.Months, HeatMonth{Label: dd.Format("Jan"), Col: w})
				lastMonth = int(dd.Month())
			}
		}
		if wt.Count > 0 && firstActive < 0 {
			firstActive = w
		}
		h.Weeks = append(h.Weeks, col)
		weeks = append(weeks, wt)
	}

	if firstActive >= 0 {
		// weeks since activity started, excluding the current partial week
		span := weeks[firstActive:]
		if len(span) > 1 {
			span = span[:len(span)-1]
		}
		h.BestWeek, h.LowestWeek = span[0], span[0]
		for _, wt := range span {
			if wt.Count > h.BestWeek.Count {
				h.BestWeek = wt
			}
			if wt.Count < h.LowestWeek.Count {
				h.LowestWeek = wt
			}
		}
		h.HasWeeks = true
	}

	var worked time.Duration
	for _, it := range items {
		worked += it.Work
	}
	h.WorkedDays = worked.Hours() / 24
	return h
}

func percentile(vals []float64, p float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	s := append([]float64(nil), vals...)
	sort.Float64s(s)
	idx := int(math.Ceil(p*float64(len(s)))) - 1
	if idx < 0 {
		idx = 0
	}
	return s[idx]
}

func stages(items []*WorkItem) []StageStat {
	by := map[string]*StageStat{}
	waits := map[string][]float64{}
	people := map[string]map[uint64]bool{}
	var cycles = map[string][]float64{}
	var breached, reopened = map[string]int{}, map[string]int{}

	for _, it := range items {
		st := by[it.Module]
		if st == nil {
			st = &StageStat{Module: it.Module, Label: it.stage.Label}
			by[it.Module] = st
			people[it.Module] = map[uint64]bool{}
		}
		st.Passes++
		st.totalWait += it.Wait
		st.totalWrk += it.Work
		waits[it.Module] = append(waits[it.Module], it.Wait.Hours())
		for p := range it.People {
			people[it.Module][p] = true
		}
		if it.Breached {
			breached[it.Module]++
		}
		if it.Reopens > 0 {
			reopened[it.Module]++
		}
		switch {
		case it.CompletedAt != nil && it.Failed:
			st.Failed++
		case it.CompletedAt != nil:
			st.Completed++
			cycles[it.Module] = append(cycles[it.Module], it.CompletedAt.Sub(it.CreatedAt).Hours())
		default:
			st.OpenNow++
		}
	}

	var total time.Duration
	for _, st := range by {
		total += st.totalWait + st.totalWrk
	}

	var out []StageStat
	maxSev := 0.0
	for m, st := range by {
		n := float64(st.Passes)
		st.AvgWaitH = st.totalWait.Hours() / n
		st.AvgWorkH = st.totalWrk.Hours() / n
		st.P90WaitH = percentile(waits[m], 0.9)
		if t := st.totalWait + st.totalWrk; t > 0 {
			st.WaitShare = float64(st.totalWait) / float64(t) * 100
		}
		if total > 0 {
			st.Share = float64(st.totalWait+st.totalWrk) / float64(total) * 100
		}
		st.People = len(people[m])
		st.BreachRate = float64(breached[m]) / n * 100
		st.ReworkRate = float64(reopened[m]) / n * 100
		if len(cycles[m]) > 0 {
			sum := 0.0
			for _, c := range cycles[m] {
				sum += c
			}
			st.AvgCycleH = sum / float64(len(cycles[m]))
		}
		// severity: share of cycle time, plus target breach and rework
		st.Severity = st.Share + 0.5*st.BreachRate + 0.5*st.ReworkRate
		if st.Severity > maxSev {
			maxSev = st.Severity
		}
		out = append(out, *st)
	}

	for i := range out {
		if maxSev > 0 {
			out[i].Bar = out[i].Severity / maxSev * 100
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Severity > out[j].Severity })
	return out
}

func impact(items []*WorkItem, departments map[uint64]string) ImpactTable {
	t := ImpactTable{Dimension: "Department"}

	useDept := false
	for _, it := range items {
		if it.Department > 0 {
			useDept = true
			break
		}
	}
	if !useDept {
		t.Dimension = "Work type"
	}

	rows := map[string]*ImpactRow{}
	cycle := map[string]float64{}
	reopened := map[string]int{}
	for _, it := range items {
		name := it.stage.Label
		if useDept {
			name = departments[it.Department]
			if name == "" {
				name = "No department"
			}
		}
		r := rows[name]
		if r == nil {
			r = &ImpactRow{Name: name}
			rows[name] = r
		}
		r.Items++
		if it.Reopens > 0 {
			reopened[name]++
		}
		if it.CompletedAt != nil && it.Failed {
			r.Failed++
		} else if it.CompletedAt != nil {
			r.completed++
			cycle[name] += it.CompletedAt.Sub(it.CreatedAt).Hours()
		}
	}

	for name, r := range rows {
		if r.completed > 0 {
			r.AvgCycleH = cycle[name] / float64(r.completed)
		}
		r.ReworkRate = float64(reopened[name]) / float64(r.Items) * 100
		t.Rows = append(t.Rows, *r)
	}
	sort.Slice(t.Rows, func(i, j int) bool { return t.Rows[i].Name < t.Rows[j].Name })

	for i := range t.Rows {
		if t.Rows[i].completed > 0 && (t.Slowest == nil || t.Rows[i].AvgCycleH > t.Slowest.AvgCycleH) {
			t.Slowest = &t.Rows[i]
		}
	}
	return t
}

// pearson returns the correlation coefficient of two equal-length series
func pearson(x, y []float64) float64 {
	n := float64(len(x))
	if n < 3 {
		return 0
	}
	var sx, sy, sxx, syy, sxy float64
	for i := range x {
		sx += x[i]
		sy += y[i]
		sxx += x[i] * x[i]
		syy += y[i] * y[i]
		sxy += x[i] * y[i]
	}
	den := math.Sqrt((n*sxx - sx*sx) * (n*syy - sy*sy))
	if den == 0 {
		return 0
	}
	return (n*sxy - sx*sy) / den
}

func handledBy(people int) string {
	if people == 0 {
		return ""
	}
	return ", handled by " + plural(people, "person", "people")
}

func pct(v float64) string { return fmt.Sprintf("%.0f%%", v) }

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

type corrResult struct {
	stage           *StageStat
	r               float64
	days            int
	lowWait, hiWait float64
	peakWeekday     time.Weekday
	peakOK          bool
}

type reworkResult struct {
	stage    string
	fastRate float64
	slowRate float64
	n        int
}

func findings(d *Deck, items []*WorkItem, now time.Time) []Finding {
	var ff []Finding
	if !d.Enough || len(d.Stages) == 0 {
		return nil
	}

	top := d.Stages[0]
	if top.Passes >= 5 && top.totalWait+top.totalWrk > 0 {
		ff = append(ff, Finding{
			Kind:  "measured",
			Title: top.Label + " is the primary constraint",
			Detail: fmt.Sprintf("%s of its cycle time is waiting, not being worked. p90 wait %.1fh across %s%s.",
				pct(top.WaitShare), top.P90WaitH, plural(top.Passes, "item", "items"), handledBy(top.People)),
		})
	}

	// unusual slowdowns: days whose average completion time is > mean + 2σ
	byDay := map[time.Time][]float64{}
	for _, it := range items {
		if it.CompletedAt != nil && !it.Failed {
			byDay[it.completedDay] = append(byDay[it.completedDay], it.CompletedAt.Sub(it.CreatedAt).Hours())
		}
	}
	if len(byDay) >= minDaysForAnomaly {
		type dayAvg struct {
			d time.Time
			v float64
		}
		var avgs []dayAvg
		var sum, sq float64
		for dd, vv := range byDay {
			s := 0.0
			for _, v := range vv {
				s += v
			}
			a := s / float64(len(vv))
			avgs = append(avgs, dayAvg{dd, a})
			sum += a
			sq += a * a
		}
		n := float64(len(avgs))
		mean := sum / n
		sd := math.Sqrt(math.Max(0, sq/n-mean*mean))
		var flagged []time.Time
		for _, a := range avgs {
			if sd > 0 && a.v > mean+2*sd {
				flagged = append(flagged, a.d)
			}
		}
		sort.Slice(flagged, func(i, j int) bool { return flagged[i].After(flagged[j]) })
		if len(flagged) > 5 {
			flagged = flagged[:5]
		}
		if len(flagged) > 0 {
			ff = append(ff, Finding{Kind: "measured", Title: "Unusual slowdowns flagged", Dates: flagged,
				Detail: fmt.Sprintf("Days whose average completion time was more than two standard deviations above the %.1fh daily average.", mean)})
		}
	}

	// backlog direction over the last 30 days
	if k := d.KPI; k.Created30 > 0 || k.Completed30 > 0 {
		diff := k.Created30 - k.Completed30
		switch {
		case diff > 0:
			ff = append(ff, Finding{Kind: "measured", Title: "Backlog is growing",
				Detail: fmt.Sprintf("In the last 30 days %s created and %s completed; %d more arrived than were finished.",
					plural(k.Created30, "item was", "items were"), plural(k.Completed30, "was", "were"), diff)})
		case diff < 0:
			ff = append(ff, Finding{Kind: "measured", Title: "Backlog is shrinking",
				Detail: fmt.Sprintf("In the last 30 days %s completed and %s created.",
					plural(k.Completed30, "item was", "items were"), plural(k.Created30, "was", "were"))})
		}
	}

	if d.KPI.OverdueNow > 0 {
		ff = append(ff, Finding{Kind: "measured", Title: "Open work is past due",
			Detail: fmt.Sprintf("%s are open beyond their due date.", plural(d.KPI.OverdueNow, "item", "items"))})
	}

	if rw := reworkCorrelation(items); rw != nil {
		ff = append(ff, Finding{Kind: "correlation",
			Title: "Faster " + rw.stage + " work may be trading time for rework",
			Detail: fmt.Sprintf("Items worked faster than the median were reopened %.0f%% of the time, versus %.0f%% for slower items (%d items). This is a correlation, not a demonstrated cause.",
				rw.fastRate, rw.slowRate, rw.n)})
	}

	if hc := handlerCorrelation(d, items); hc != nil {
		ff = append(ff, Finding{Kind: "correlation",
			Title: hc.stage.Label + " capacity appears concentrated in a small group",
			Detail: fmt.Sprintf("Waiting time rises on days when fewer distinct people handled %s (r = %.2f over %d days). Staffing records were not available to confirm.",
				hc.stage.Label, hc.r, hc.days)})
	}

	if wd, share, n := intakePeak(items); n > 0 {
		ff = append(ff, Finding{Kind: "correlation",
			Title:  "Intake peaks on " + wd.String() + "s",
			Detail: fmt.Sprintf("%.0f%% of new work (%d items) arrives on %ss, against an even share of 14%%.", share, n, wd)})
	}

	return ff
}

// reworkCorrelation compares reopen rates of faster and slower worked items
func reworkCorrelation(items []*WorkItem) *reworkResult {
	byStage := map[string][]*WorkItem{}
	for _, it := range items {
		if it.CompletedAt != nil && it.Work > 0 {
			byStage[it.stage.Label] = append(byStage[it.stage.Label], it)
		}
	}

	var best *reworkResult
	for label, ii := range byStage {
		if len(ii) < minItemsForCorr {
			continue
		}
		var ww []float64
		for _, it := range ii {
			ww = append(ww, it.Work.Hours())
		}
		med := median(ww)
		var fast, slow, fastRe, slowRe int
		for _, it := range ii {
			if it.Work.Hours() < med {
				fast++
				if it.Reopens > 0 {
					fastRe++
				}
			} else {
				slow++
				if it.Reopens > 0 {
					slowRe++
				}
			}
		}
		if fast < 10 || slow < 10 {
			continue
		}
		fr, sr := float64(fastRe)/float64(fast)*100, float64(slowRe)/float64(slow)*100
		if fr-sr >= 5 && (best == nil || fr-sr > best.fastRate-best.slowRate) {
			best = &reworkResult{stage: label, fastRate: fr, slowRate: sr, n: len(ii)}
		}
	}
	return best
}

// handlerCorrelation relates daily waiting time of the top stage to the
// number of distinct people who handled it that day
func handlerCorrelation(d *Deck, items []*WorkItem) *corrResult {
	if len(d.Stages) == 0 {
		return nil
	}
	top := d.Stages[0]

	type day struct {
		wait   []float64
		people map[uint64]bool
	}
	days := map[time.Time]*day{}
	for _, it := range items {
		if it.Module != top.Module || it.CompletedAt == nil {
			continue
		}
		dd := days[it.completedDay]
		if dd == nil {
			dd = &day{people: map[uint64]bool{}}
			days[it.completedDay] = dd
		}
		dd.wait = append(dd.wait, it.Wait.Hours())
		for p := range it.People {
			dd.people[p] = true
		}
	}
	if len(days) < minDaysForAnomaly {
		return nil
	}

	var xs, ys []float64
	for _, dd := range days {
		s := 0.0
		for _, w := range dd.wait {
			s += w
		}
		xs = append(xs, float64(len(dd.people)))
		ys = append(ys, s/float64(len(dd.wait)))
	}
	r := pearson(xs, ys)
	if r > -0.3 {
		return nil
	}

	med := percentile(xs, 0.5)
	var lo, hi []float64
	for i := range xs {
		if xs[i] <= med {
			lo = append(lo, ys[i])
		} else {
			hi = append(hi, ys[i])
		}
	}
	res := &corrResult{stage: &d.Stages[0], r: r, days: len(xs), lowWait: mean(lo), hiWait: mean(hi)}
	var n int
	res.peakWeekday, _, n = intakePeak(items)
	res.peakOK = n > 0
	return res
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	if len(s)%2 == 1 {
		return s[len(s)/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}

func mean(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := 0.0
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}

// intakePeak returns the weekday receiving a clearly outsized share of new work
func intakePeak(items []*WorkItem) (time.Weekday, float64, int) {
	if len(items) < 30 {
		return 0, 0, 0
	}
	var counts [7]int
	for _, it := range items {
		counts[it.createdWeekday]++
	}
	best := 0
	for i := range counts {
		if counts[i] > counts[best] {
			best = i
		}
	}
	share := float64(counts[best]) / float64(len(items)) * 100
	if share < 100.0/7*1.3 {
		return 0, 0, 0
	}
	return time.Weekday(best), share, counts[best]
}

// confidence grows with sample size and effect strength, capped honestly
func confidence(n int, strength float64) int {
	c := 30 + 12*math.Log10(float64(n)+1) + 25*math.Min(1, math.Max(0, strength))
	return int(math.Round(math.Min(90, math.Max(30, c))))
}

func rangeLabel(mid float64, unit string) string {
	lo, hi := mid*0.8, mid*1.2
	if hi < 1 {
		return "less than 1" + unit
	}
	return fmt.Sprintf("%.0f–%.0f%s", math.Max(1, math.Floor(lo)), math.Ceil(hi), unit)
}

func recommendations(d *Deck, items []*WorkItem) []Recommendation {
	if !d.Enough || len(d.Stages) == 0 {
		return nil
	}

	var rr []Recommendation
	top := d.Stages[0]

	// 1. halve waiting in the constraint
	if top.WaitShare >= 25 && top.Passes >= 5 {
		mid := top.Share * top.WaitShare / 100 * 0.5
		title := "Set a pickup target for waiting " + strings.ToLower(top.Label)
		if top.Module == "Approval" {
			title = "Add an approval delegate so pending approvals are decided within a day"
		}
		rr = append(rr, Recommendation{
			Title:      title,
			Expected:   "Estimated " + rangeLabel(mid, "%") + " reduction in end-to-end time if waiting in " + top.Label + " is halved.",
			Evidence:   fmt.Sprintf("%s holds %s of all cycle time and %s of it is waiting (p90 %.1fh).", top.Label, pct(top.Share), pct(top.WaitShare), top.P90WaitH),
			HowToTest:  fmt.Sprintf("Run for 3 weeks and compare the average wait in %s against today's %.1fh.", top.Label, top.AvgWaitH),
			Confidence: confidence(top.Passes, top.WaitShare/100),
			Basis:      "measured",
			score:      mid,
		})
	}

	// 2. capacity on peak days
	if hc := handlerCorrelation(d, items); hc != nil && hc.lowWait > hc.hiWait && hc.lowWait > 0 {
		mid := (hc.lowWait - hc.hiWait) / hc.lowWait * 100
		when := "on its busiest days"
		if hc.peakOK {
			when = "on " + hc.peakWeekday.String() + "s"
		}
		rr = append(rr, Recommendation{
			Title:      "Add a second person to " + hc.stage.Label + " " + when,
			Expected:   "Estimated " + rangeLabel(mid, "%") + " reduction in " + hc.stage.Label + " waiting time on those days.",
			Evidence:   fmt.Sprintf("Days handled by more people waited %.1fh on average versus %.1fh (r = %.2f, %d days).", hc.hiWait, hc.lowWait, hc.r, hc.days),
			HowToTest:  "Staff the extra person for 4 weeks and compare waiting time on those days with the weeks before.",
			Confidence: confidence(hc.days, -hc.r),
			Basis:      "correlation",
			score:      mid * 0.8,
		})
	}

	// 3. rework checklist
	if rw := reworkCorrelation(items); rw != nil {
		gap := rw.fastRate - rw.slowRate
		rr = append(rr, Recommendation{
			Title:      "Introduce a completion checklist before closing " + strings.ToLower(rw.stage),
			Expected:   "Estimated " + rangeLabel(gap/2, " point") + " reduction in rework rate.",
			Evidence:   fmt.Sprintf("Faster-worked %s were reopened %.0f%% of the time versus %.0f%% (%d items).", strings.ToLower(rw.stage), rw.fastRate, rw.slowRate, rw.n),
			HowToTest:  "Use the checklist for 4 weeks and compare the reopen rate with the current rate.",
			Confidence: confidence(rw.n, gap/20),
			Basis:      "correlation",
			score:      gap / 2,
		})
	}

	// 4. due-date breaches
	for _, st := range d.Stages {
		if st.BreachRate >= 15 && st.Passes >= 10 {
			mid := st.BreachRate / 3
			rr = append(rr, Recommendation{
				Title:      "Review overdue " + strings.ToLower(st.Label) + " weekly and reset unrealistic due dates",
				Expected:   "Estimated " + rangeLabel(mid, " point") + " reduction in missed due dates for " + st.Label + ".",
				Evidence:   fmt.Sprintf("%s of %s missed their due date.", pct(st.BreachRate), strings.ToLower(st.Label)),
				HowToTest:  "Hold the review for 4 weeks and compare the missed-due-date rate with the current rate.",
				Confidence: confidence(st.Passes, st.BreachRate/50),
				Basis:      "measured",
				score:      mid,
			})
			break
		}
	}

	sort.SliceStable(rr, func(i, j int) bool {
		return rr[i].score*float64(rr[i].Confidence) > rr[j].score*float64(rr[j].Confidence)
	})
	for i := range rr {
		rr[i].Rank = i + 1
	}
	return rr
}
