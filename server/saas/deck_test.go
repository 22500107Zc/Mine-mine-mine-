package saas

import (
	"math"
	"strings"
	"testing"
	"time"
)

var deckNow = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

func daysAgo(d int) time.Time { return deckNow.AddDate(0, 0, -d) }

func ev(id uint64, module, kind, from, to string, at time.Time) ActivityEvent {
	return ActivityEvent{RecordID: id, Module: module, Kind: kind, FromStatus: from, ToStatus: to, OccurredAt: at, Title: module + " item"}
}

func near(a, b float64) bool { return math.Abs(a-b) < 0.01 }

// A small, hand-computed history
func fixtureEvents() []ActivityEvent {
	return []ActivityEvent{
		ev(4, "Case", ActivityCreated, "", "New", daysAgo(20)),
		ev(4, "Case", ActivityStatus, "New", "Open", daysAgo(19)),
		ev(4, "Case", ActivityStatus, "Open", "Resolved", daysAgo(18)),
		ev(4, "Case", ActivityStatus, "Resolved", "Open", daysAgo(17)),
		ev(4, "Case", ActivityStatus, "Open", "Closed", daysAgo(16)),
		ev(1, "Task", ActivityCreated, "", "Open", daysAgo(10)),
		ev(1, "Task", ActivityStatus, "Open", "In Progress", daysAgo(9)),
		ev(1, "Task", ActivityStatus, "In Progress", "Done", daysAgo(8)),
		ev(2, "Task", ActivityCreated, "", "Open", daysAgo(5)),
		ev(3, "Approval", ActivityCreated, "", "Pending", daysAgo(3)),
		ev(3, "Approval", ActivityStatus, "Pending", "Rejected", daysAgo(2)),
	}
}

func TestDeckKPIsFromMeasuredActivity(t *testing.T) {
	d := BuildDeck(fixtureEvents(), deckNow, nil)
	k := d.KPI

	if d.TotalEvents != 11 || k.Act7 != 3 || k.Prev7 != 3 {
		t.Fatalf("activity counts: total=%d act7=%d prev7=%d", d.TotalEvents, k.Act7, k.Prev7)
	}
	if k.Items != 4 || k.Completed != 2 || k.Failed != 1 || k.OpenNow != 1 {
		t.Fatalf("items: %+v", k)
	}
	if !near(k.CompletionRate, 50) || !near(k.AvgCompletionHours, 72) {
		t.Fatalf("completion rate %.2f avg %.2f", k.CompletionRate, k.AvgCompletionHours)
	}
	if k.CurrentStreak != 0 || k.LongestStrk != 5 {
		t.Fatalf("streaks current=%d longest=%d", k.CurrentStreak, k.LongestStrk)
	}
	if !near(k.Throughput, 2.0/30) {
		t.Fatalf("throughput %.4f", k.Throughput)
	}

	var task, cases *StageStat
	for i := range d.Stages {
		switch d.Stages[i].Module {
		case "Task":
			task = &d.Stages[i]
		case "Case":
			cases = &d.Stages[i]
		}
	}
	// Task 1: 24h wait + 24h work; Task 2: open and waiting for 5 days
	if task == nil || task.Passes != 2 || !near(task.AvgWaitH, 72) || !near(task.AvgWorkH, 12) {
		t.Fatalf("task stage: %+v", task)
	}
	// Case: 24h new, 24h open, reopened, 24h open again
	if cases == nil || !near(cases.AvgWaitH, 24) || !near(cases.AvgWorkH, 48) || !near(cases.ReworkRate, 100) {
		t.Fatalf("case stage: %+v", cases)
	}

	// too little data: no findings or recommendations are invented
	if d.Enough || d.Findings != nil || d.Recs != nil {
		t.Fatal("findings must not be shown without enough data")
	}
}

func TestDeckHeatmap(t *testing.T) {
	d := BuildDeck(fixtureEvents(), deckNow, nil)
	h := d.Heatmap

	if len(h.Weeks) != 53 {
		t.Fatalf("weeks: %d", len(h.Weeks))
	}

	total, future := 0, 0
	for _, w := range h.Weeks {
		if len(w) != 7 || w[0].Date.Weekday() != time.Monday {
			t.Fatal("weeks must be Monday-first columns of 7 days")
		}
		for _, dd := range w {
			total += dd.Count
			if dd.Future {
				future++
				if dd.Date.Before(dayOf(deckNow)) || dd.Date.Equal(dayOf(deckNow)) {
					t.Fatal("past days marked as future")
				}
			}
		}
	}
	if total != 11 || h.EventsCount != 11 || h.Peak != 1 {
		t.Fatalf("heatmap totals %d/%d peak %d", total, h.EventsCount, h.Peak)
	}
	if !h.HasWeeks || h.BestWeek.Count < h.LowestWeek.Count {
		t.Fatal("best/lowest week")
	}
	// time in active work: Task 1 24h + Case 48h = 3 days
	if !near(h.WorkedDays, 3) {
		t.Fatalf("worked days %.2f", h.WorkedDays)
	}
}

// Approvals wait far longer than tasks: the deck must say so, as a
// measured fact, and recommend testing a faster approval path first
func TestDeckFindsConstraintAndRecommends(t *testing.T) {
	var ee []ActivityEvent
	id := uint64(100)
	for i := 0; i < 30; i++ {
		id++
		start := daysAgo(60 - i)
		ee = append(ee,
			ev(id, "Approval", ActivityCreated, "", "Pending", start),
			ev(id, "Approval", ActivityStatus, "Pending", "Approved", start.Add(time.Duration(40+i%10)*time.Hour)))
		id++
		ee = append(ee,
			ev(id, "Task", ActivityCreated, "", "Open", start),
			ev(id, "Task", ActivityStatus, "Open", "In Progress", start.Add(time.Hour)),
			ev(id, "Task", ActivityStatus, "In Progress", "Done", start.Add(3*time.Hour)))
	}
	sortEvents(ee)

	d := BuildDeck(ee, deckNow, nil)
	if !d.Enough || d.Stages[0].Module != "Approval" {
		t.Fatalf("approvals must be the top stage: %+v", d.Stages)
	}
	if !near(d.Stages[0].WaitShare, 100) {
		t.Fatalf("approval wait share %.1f", d.Stages[0].WaitShare)
	}

	f := d.Findings[0]
	if f.Kind != "measured" || f.Title != "Approvals is the primary constraint" || !strings.Contains(f.Detail, "30 items") {
		t.Fatalf("primary finding: %+v", f)
	}

	if len(d.Recs) == 0 || !strings.Contains(d.Recs[0].Title, "approval") || d.Recs[0].Rank != 1 {
		t.Fatalf("recommendations: %+v", d.Recs)
	}
	if c := d.Recs[0].Confidence; c < 30 || c > 90 {
		t.Fatalf("confidence out of bounds: %d", c)
	}
	if d.Recs[0].Basis != "measured" || d.Recs[0].HowToTest == "" || d.Recs[0].Evidence == "" {
		t.Fatal("recommendations must carry evidence and a way to test them")
	}
}

// Quickly worked items are reopened more often: shown as a correlation,
// never as a cause
func TestDeckReworkCorrelation(t *testing.T) {
	var ee []ActivityEvent
	id := uint64(500)
	for i := 0; i < 40; i++ {
		id++
		start := daysAgo(50 - i)
		work := 10 * time.Hour
		reopen := false
		if i%2 == 0 {
			work, reopen = time.Hour, i%4 == 0
		}
		ee = append(ee,
			ev(id, "Task", ActivityCreated, "", "Open", start),
			ev(id, "Task", ActivityStatus, "Open", "In Progress", start.Add(time.Hour)),
			ev(id, "Task", ActivityStatus, "In Progress", "Done", start.Add(time.Hour+work)))
		if reopen {
			ee = append(ee,
				ev(id, "Task", ActivityStatus, "Done", "In Progress", start.Add(time.Hour+work+time.Minute)),
				ev(id, "Task", ActivityStatus, "In Progress", "Done", start.Add(time.Hour+work+2*time.Minute)))
		}
	}
	sortEvents(ee)

	d := BuildDeck(ee, deckNow, nil)
	var corr *Finding
	for i := range d.Findings {
		if strings.Contains(d.Findings[i].Title, "rework") {
			corr = &d.Findings[i]
		}
	}
	if corr == nil || corr.Kind != "correlation" || !strings.Contains(corr.Detail, "not a demonstrated cause") {
		t.Fatalf("rework correlation missing: %+v", d.Findings)
	}

	found := false
	for _, r := range d.Recs {
		if strings.Contains(r.Title, "checklist") && r.Basis == "correlation" {
			found = true
		}
	}
	if !found {
		t.Fatalf("checklist recommendation missing: %+v", d.Recs)
	}
}

func TestDeckEmptyAndDeleted(t *testing.T) {
	d := BuildDeck(nil, deckNow, nil)
	if d.HasData || d.KPI.Items != 0 || len(d.Heatmap.Weeks) != 53 {
		t.Fatal("empty deck")
	}

	ee := append(fixtureEvents(), ev(2, "Task", ActivityDeleted, "", "", daysAgo(1)))
	d = BuildDeck(ee, deckNow, nil)
	if d.KPI.Items != 3 || d.KPI.OpenNow != 0 {
		t.Fatalf("deleted records must leave the pipeline: %+v", d.KPI)
	}
}

func TestPearson(t *testing.T) {
	if r := pearson([]float64{1, 2, 3, 4}, []float64{8, 6, 4, 2}); !near(r, -1) {
		t.Fatalf("r=%.3f", r)
	}
	if r := pearson([]float64{1, 1, 1}, []float64{1, 2, 3}); r != 0 {
		t.Fatal("constant series has no correlation")
	}
}

func sortEvents(ee []ActivityEvent) {
	for i := 1; i < len(ee); i++ {
		for j := i; j > 0 && ee[j].OccurredAt.Before(ee[j-1].OccurredAt); j-- {
			ee[j], ee[j-1] = ee[j-1], ee[j]
		}
	}
}
