package saas

import (
	"fmt"
	"math"
	"net/url"
	"sort"
	"strings"
	"time"
)

// AdminEntry is an administrative change of the company (audit log)
type AdminEntry struct {
	At    time.Time
	Label string
	Actor string
}

type ActDay struct {
	Date   time.Time
	Count  int
	Level  int
	Future bool
	Out    bool // outside the displayed month
	Tip    string
	Link   string
}

type ActEvent struct {
	At       time.Time
	Category string
	Module   string
	Title    string
	What     string
	Actor    string
	Link     string
}

type HourBar struct {
	Hour  int
	Count int
	Pct   float64
}

type ActCount struct {
	Label string
	Count int
}

type ActivityHistory struct {
	View, Filter string
	Date         time.Time
	Title        string
	PrevLink     string
	NextLink     string
	UpLink       string
	UpLabel      string

	Weeks  [][]ActDay // year: 53 columns of 7 days; month: rows of 7 days
	Months []HeatMonth
	Hours  [7][24]int // week view
	HourMx int
	Days   []ActDay // week view: the 7 days
	Bars   []HourBar
	Events []ActEvent

	Total, Peak, ActiveDays int
	Totals                  []ActCount
	Filters                 []workflowOption
	Base                    string
	extra                   url.Values
}

// activityURL links to another zoom level or filter of the same graph
func activityURL(a ActivityHistory, view, filter string) string {
	q := url.Values{}
	for k, vv := range a.extra {
		q[k] = vv
	}
	q.Set("view", view)
	q.Set("date", a.Date.Format("2006-01-02"))
	q.Del("filter")
	if filter != "" && filter != "all" {
		q.Set("filter", filter)
	}
	return a.Base + "?" + q.Encode()
}

var activityFilters = []workflowOption{
	{"all", "All activity"}, {"tasks", "Tasks"}, {"cases", "Cases"}, {"records", "Records"}, {"workflows", "Status changes"},
	{"approvals", "Approvals"}, {"documents", "Documents"}, {"customers", "Customer activity"}, {"admin", "Administrative"},
}

func eventCategory(e ActivityEvent) []string {
	var cc []string
	switch e.Module {
	case "Task":
		cc = append(cc, "tasks")
	case "Case":
		cc = append(cc, "cases")
	case "OperationsRecord":
		cc = append(cc, "records")
	case "Approval":
		cc = append(cc, "approvals")
	case "Document":
		cc = append(cc, "documents")
	case "Customer", "Contact":
		cc = append(cc, "customers")
	}
	if e.Kind == ActivityStatus {
		cc = append(cc, "workflows")
	}
	return cc
}

type dayAgg struct {
	events, created, tasksDone, approvals, casesClosed, breaches, admin int
}

// ActivityHistory builds the drillable activity graph: year → month → week
// → day → event. base is the page the graph links back to (for reuse on
// department, team, person and Founder pages); scope links are kept.
func (in *Intel) ActivityHistory(view, filter string, date time.Time, admin []AdminEntry, base string, extra url.Values) ActivityHistory {
	if !contains([]string{"year", "month", "week", "day"}, view) {
		view = "year"
	}
	valid := false
	for _, f := range activityFilters {
		valid = valid || f.Module == filter
	}
	if !valid {
		filter = "all"
	}
	today := dayOf(in.Now)
	if date.IsZero() || date.After(today) {
		date = today
	}
	date = dayOf(date)
	h := ActivityHistory{View: view, Filter: filter, Date: date, Filters: activityFilters, Base: base, extra: extra}

	link := func(v string, d time.Time) string {
		q := url.Values{}
		for k, vv := range extra {
			q[k] = vv
		}
		q.Set("view", v)
		q.Set("date", d.Format("2006-01-02"))
		if filter != "all" {
			q.Set("filter", filter)
		}
		return base + "?" + q.Encode()
	}

	// per-day aggregates
	days := map[time.Time]*dayAgg{}
	agg := func(d time.Time) *dayAgg {
		a := days[d]
		if a == nil {
			a = &dayAgg{}
			days[d] = a
		}
		return a
	}
	want := func(e ActivityEvent) bool {
		if filter == "all" {
			return true
		}
		return contains(eventCategory(e), filter)
	}
	events := in.scopedEvents()
	for _, e := range events {
		if !want(e) {
			continue
		}
		a := agg(dayOf(e.OccurredAt))
		a.events++
		switch {
		case e.Kind == ActivityCreated:
			a.created++
		case e.Kind == ActivityStatus && e.Module == "Task" && e.ToStatus == "Done":
			a.tasksDone++
		case e.Kind == ActivityStatus && e.Module == "Approval" && (e.ToStatus == "Approved" || e.ToStatus == "Rejected"):
			a.approvals++
		case e.Kind == ActivityStatus && e.Module == "Case" && (e.ToStatus == "Closed" || e.ToStatus == "Resolved"):
			a.casesClosed++
		}
	}
	if filter == "all" || filter == "admin" {
		for _, ad := range admin {
			a := agg(dayOf(ad.At))
			a.events++
			a.admin++
		}
	}
	if filter == "all" {
		for _, it := range in.Items {
			if c := in.sla(it); c.breachAt != nil {
				agg(dayOf(*c.breachAt)).breaches++
			}
		}
	}

	tip := func(d time.Time) string {
		a := days[d]
		if a == nil || a.events == 0 {
			return d.Format("Monday, Jan 2, 2006") + "\nNo recorded activity"
		}
		var b strings.Builder
		b.WriteString(d.Format("Monday, Jan 2, 2006"))
		fmt.Fprintf(&b, "\n%s", plural(a.events, "operational event", "operational events"))
		for _, x := range []struct {
			n    int
			l, p string
		}{{a.tasksDone, "task completed", "tasks completed"}, {a.created, "record created", "records created"},
			{a.approvals, "approval decided", "approvals decided"}, {a.casesClosed, "case closed", "cases closed"},
			{a.breaches, "SLA breach", "SLA breaches"}, {a.admin, "administrative change", "administrative changes"}} {
			if x.n > 0 {
				fmt.Fprintf(&b, "\n%s", plural(x.n, x.l, x.p))
			}
		}
		return b.String()
	}

	levels := func(counts []int) func(int) int {
		var nz []int
		for _, c := range counts {
			if c > 0 {
				nz = append(nz, c)
			}
		}
		sort.Ints(nz)
		q := func(p float64) int {
			if len(nz) == 0 {
				return 0
			}
			return nz[int(math.Min(float64(len(nz)-1), math.Floor(p*float64(len(nz)))))]
		}
		q1, q2, q3 := q(0.25), q(0.5), q(0.75)
		return func(c int) int {
			switch {
			case c == 0:
				return 0
			case c <= q1:
				return 1
			case c <= q2:
				return 2
			case c <= q3:
				return 3
			}
			return 4
		}
	}

	cell := func(d time.Time) ActDay {
		c := 0
		if a := days[d]; a != nil {
			c = a.events
		}
		return ActDay{Date: d, Count: c, Future: d.After(today), Tip: tip(d), Link: link("day", d)}
	}

	var shown []time.Time
	switch view {
	case "year":
		end := date
		offset := (int(end.Weekday()) + 6) % 7
		start := end.AddDate(0, 0, -offset-52*7)
		h.Title = fmt.Sprintf("%s – %s", start.Format("Jan 2, 2006"), end.Format("Jan 2, 2006"))
		h.PrevLink, h.NextLink = link("year", end.AddDate(-1, 0, 0)), ""
		if end.AddDate(1, 0, 0).Before(today.AddDate(0, 0, 1)) {
			h.NextLink = link("year", end.AddDate(1, 0, 0))
		}
		lastMonth := -1
		for w := 0; w < 53; w++ {
			col := make([]ActDay, 7)
			for i := 0; i < 7; i++ {
				d := start.AddDate(0, 0, w*7+i)
				col[i] = cell(d)
				shown = append(shown, d)
				if i == 0 && d.Day() <= 7 && int(d.Month()) != lastMonth {
					h.Months = append(h.Months, HeatMonth{Label: d.Format("Jan"), Col: w})
					lastMonth = int(d.Month())
				}
			}
			h.Weeks = append(h.Weeks, col)
		}
		for i := range h.Months {
			m := h.Months[i]
			first := h.Weeks[m.Col][0].Date
			h.Months[i].Label = first.Format("Jan")
		}

	case "month":
		first := time.Date(date.Year(), date.Month(), 1, 0, 0, 0, 0, time.UTC)
		h.Title = first.Format("January 2006")
		h.PrevLink = link("month", first.AddDate(0, -1, 0))
		if next := first.AddDate(0, 1, 0); !next.After(today) {
			h.NextLink = link("month", next)
		}
		h.UpLink, h.UpLabel = link("year", date), "Year"
		start := first.AddDate(0, 0, -((int(first.Weekday()) + 6) % 7))
		for d := start; d.Before(first.AddDate(0, 1, 0)); d = d.AddDate(0, 0, 7) {
			row := make([]ActDay, 7)
			for i := 0; i < 7; i++ {
				dd := d.AddDate(0, 0, i)
				row[i] = cell(dd)
				row[i].Out = dd.Month() != first.Month()
				if !row[i].Out {
					shown = append(shown, dd)
				}
				row[i].Link = link("day", dd)
			}
			h.Weeks = append(h.Weeks, row)
		}

	case "week":
		start := date.AddDate(0, 0, -((int(date.Weekday()) + 6) % 7))
		h.Title = fmt.Sprintf("Week of %s", start.Format("Jan 2, 2006"))
		h.PrevLink = link("week", start.AddDate(0, 0, -7))
		if next := start.AddDate(0, 0, 7); !next.After(today) {
			h.NextLink = link("week", next)
		}
		h.UpLink, h.UpLabel = link("month", date), "Month"
		for i := 0; i < 7; i++ {
			d := start.AddDate(0, 0, i)
			h.Days = append(h.Days, cell(d))
			shown = append(shown, d)
		}
		w := Window{start, start.AddDate(0, 0, 7)}
		for _, e := range events {
			if w.Has(e.OccurredAt) && want(e) {
				wd := (int(e.OccurredAt.Weekday()) + 6) % 7
				h.Hours[wd][e.OccurredAt.UTC().Hour()]++
			}
		}
		for _, ad := range admin {
			if w.Has(ad.At) && (filter == "all" || filter == "admin") {
				wd := (int(ad.At.Weekday()) + 6) % 7
				h.Hours[wd][ad.At.UTC().Hour()]++
			}
		}
		for d := range h.Hours {
			for hr := range h.Hours[d] {
				if h.Hours[d][hr] > h.HourMx {
					h.HourMx = h.Hours[d][hr]
				}
			}
		}

	case "day":
		h.Title = date.Format("Monday, January 2, 2006")
		h.PrevLink = link("day", date.AddDate(0, 0, -1))
		if next := date.AddDate(0, 0, 1); !next.After(today) {
			h.NextLink = link("day", next)
		}
		h.UpLink, h.UpLabel = link("week", date), "Week"
		shown = append(shown, date)
		w := Window{date, date.AddDate(0, 0, 1)}
		var bars [24]int
		for i := len(events) - 1; i >= 0; i-- {
			e := events[i]
			if !w.Has(e.OccurredAt) || !want(e) {
				continue
			}
			bars[e.OccurredAt.UTC().Hour()]++
			if len(h.Events) < 500 {
				h.Events = append(h.Events, in.actEvent(e))
			}
		}
		if filter == "all" || filter == "admin" {
			for _, ad := range admin {
				if w.Has(ad.At) {
					bars[ad.At.UTC().Hour()]++
					h.Events = append(h.Events, ActEvent{At: ad.At, Category: "admin", Module: "Administration", Title: ad.Label, What: "Administrative change", Actor: ad.Actor})
				}
			}
		}
		sort.SliceStable(h.Events, func(i, j int) bool { return h.Events[i].At.After(h.Events[j].At) })
		mx := 0
		for _, b := range bars {
			if b > mx {
				mx = b
			}
		}
		for hr, b := range bars {
			p := 0.0
			if mx > 0 {
				p = float64(b) / float64(mx) * 100
			}
			h.Bars = append(h.Bars, HourBar{hr, b, p})
		}
	}

	var counts []int
	for _, d := range shown {
		if a := days[d]; a != nil {
			counts = append(counts, a.events)
		}
	}
	lv := levels(counts)
	apply := func(dd []ActDay) {
		for i := range dd {
			dd[i].Level = lv(dd[i].Count)
		}
	}
	for i := range h.Weeks {
		apply(h.Weeks[i])
	}
	apply(h.Days)

	var tot dayAgg
	for _, d := range shown {
		if a := days[d]; a != nil {
			h.Total += a.events
			if a.events > h.Peak {
				h.Peak = a.events
			}
			if a.events > 0 {
				h.ActiveDays++
			}
			tot.created += a.created
			tot.tasksDone += a.tasksDone
			tot.approvals += a.approvals
			tot.casesClosed += a.casesClosed
			tot.breaches += a.breaches
			tot.admin += a.admin
		}
	}
	h.Totals = []ActCount{{"Events", h.Total}, {"Records created", tot.created}, {"Tasks completed", tot.tasksDone},
		{"Approvals decided", tot.approvals}, {"Cases closed", tot.casesClosed}, {"SLA breaches", tot.breaches}, {"Administrative", tot.admin}}
	return h
}

func (in *Intel) actEvent(e ActivityEvent) ActEvent {
	ev := ActEvent{At: e.OccurredAt, Module: orStr(moduleLabels[e.Module], e.Module), Title: orStr(e.Title, "(untitled)"), Actor: in.Lk.person(e.ActorID)}
	if e.ActorID == 0 {
		ev.Actor = "—"
	}
	if cc := eventCategory(e); len(cc) > 0 {
		ev.Category = cc[0]
	}
	switch e.Kind {
	case ActivityCreated:
		ev.What = "Created"
		if e.ToStatus != "" {
			ev.What += " · " + e.ToStatus
		}
	case ActivityStatus:
		ev.What = fmt.Sprintf("%s → %s", orStr(e.FromStatus, "—"), orStr(e.ToStatus, "—"))
	case ActivityDeleted:
		ev.What, ev.Title = "Deleted", "(deleted record)"
	default:
		ev.What = "Updated"
	}
	if it := in.byID[e.RecordID]; it != nil && !it.Deleted {
		ev.Link = fmt.Sprintf("/command/record/%d", e.RecordID)
	}
	return ev
}

// Heat builds the compact 53-week grid used on detail pages
func (in *Intel) Heat(base string, extra url.Values) ActivityHistory {
	return in.ActivityHistory("year", "all", in.Now, nil, base, extra)
}
