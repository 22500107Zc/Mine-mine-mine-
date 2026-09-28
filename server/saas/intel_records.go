package saas

import (
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// RecordRow is one record in an evidence list
type RecordRow struct {
	Item       *WorkItem
	Link       string
	Module     string
	Status     string
	Department string
	Team       string
	Assignee   string
	Customer   string
	Cycle      string
	Age        string
	SLA        string // met | breached | open breach | —
	Loops      int
	Handoffs   int
	Detail     string // set-specific context (stage time, handoff wait…)
}

type RecordSet struct {
	Key        string
	Title      string
	Definition string
	Rows       []RecordRow
	Total      int
	Truncated  bool
}

var recordSetTitles = map[string][2]string{
	"wip":               {"Work in progress", "wip"},
	"completed":         {"Completed in period", "completed"},
	"started":           {"New work in period", "started"},
	"failed":            {"Failed (rejected) in period", "failure"},
	"sla":               {"SLA-applicable records completed in period", "sla"},
	"sla_breached":      {"SLA breaches", "breaches"},
	"blocked":           {"Blocked work", "blocked"},
	"rework":            {"Completed records with rework", "rework"},
	"handoffs":          {"Records with handoffs in period", "handoff"},
	"aging":             {"Aging work (open more than 7 days)", "aging"},
	"aging_bucket":      {"Open work in aging bucket", "aging"},
	"overdue":           {"Overdue work", "overdue"},
	"open_cases":        {"Open cases", "open_cases"},
	"open_tasks":        {"Open tasks", "open_tasks"},
	"pending_approvals": {"Pending approvals", "pending_approvals"},
	"in_stage":          {"Stage visits in period", "stage_time"},
	"transition":        {"Stage transitions in period", "stage_time"},
	"loop":              {"Records with this rework loop", "loop"},
	"path":              {"Records that followed this path", "stage_time"},
	"stalled":           {"Stalled open work (no activity for 30+ days)", "wip"},
	"skipped":           {"Completed without a working stage", "stage_time"},
	"cycle_bin":         {"Completed records in cycle-time range", "cycle"},
	"day":               {"Records with activity on this day", "team"},
}

// RecordSet resolves a drilldown population to the exact records behind it
func (in *Intel) RecordSet(q url.Values) RecordSet {
	key := q.Get("set")
	t, ok := recordSetTitles[key]
	if !ok {
		key, t = "wip", recordSetTitles["wip"]
	}
	rs := RecordSet{Key: key, Title: t[0], Definition: metricDef(t[1]).Definition}
	w, end := in.W, in.W.To
	module, stage := q.Get("workflow"), q.Get("stage")

	detail := map[*WorkItem]string{}
	pick := func(it *WorkItem) bool {
		switch key {
		case "wip":
			return it.OpenAt(end)
		case "completed":
			return it.Done() && w.Has(*it.CompletedAt)
		case "started":
			return w.Has(it.CreatedAt)
		case "failed":
			return it.CompletedAt != nil && it.Failed && w.Has(*it.CompletedAt)
		case "sla":
			return it.CompletedAt != nil && w.Has(*it.CompletedAt) && in.sla(it).slaApplicable
		case "sla_breached":
			c := in.sla(it)
			if c.breachAt == nil || c.breachAt.After(end) {
				return false
			}
			if stage != "" && c.breachStage != stage {
				// a record can breach several stages; check this one
				for _, sg := range it.Segments {
					if sg.Status == stage {
						tgt := in.Targets[targetKey(it.Module, stage)]
						if tgt > 0 && sg.Dur(in.Now).Hours() > tgt {
							return (sg.End == nil && it.OpenAt(end)) || (sg.End != nil && w.Has(*sg.End))
						}
					}
				}
				return false
			}
			if stage == "" && q.Has("stage") && c.breachStage != "" {
				return false
			}
			detail[it] = fmt.Sprintf("over by %s", fmtHours(c.breachOver.Hours()))
			return (it.CompletedAt != nil && w.Has(*it.CompletedAt)) || it.OpenAt(end)
		case "blocked":
			sg, ok := it.StatusAt(end.Add(-time.Nanosecond))
			return it.OpenAt(end) && ok && sg.Kind == "blocked"
		case "rework":
			return it.Done() && w.Has(*it.CompletedAt) && len(it.Loops) > 0
		case "handoffs":
			for _, h := range it.Handoffs() {
				if !w.Has(h.At) {
					continue
				}
				if q.Get("hfrom") != "" {
					f, t := in.groupOf(h.From, q.Get("by")), in.groupOf(h.To, q.Get("by"))
					if strconv.FormatUint(f, 10) != q.Get("hfrom") || strconv.FormatUint(t, 10) != q.Get("hto") {
						continue
					}
				}
				detail[it] = fmt.Sprintf("%s → %s · waited %s", in.Lk.person(h.From), in.Lk.person(h.To), fmtHours(h.Wait(in.Now).Hours()))
				return true
			}
			return false
		case "aging":
			return it.OpenAt(end) && end.Sub(it.CreatedAt) > agingThreshold
		case "aging_bucket":
			if !it.OpenAt(end) {
				return false
			}
			sg, _ := it.StatusAt(end.Add(-time.Nanosecond))
			age := end.Sub(it.CreatedAt).Hours()
			if q.Get("basis") == "stage" && !sg.Start.IsZero() {
				age = end.Sub(sg.Start).Hours()
			}
			b := agingBuckets[agingBucket(age)].Key
			return b == q.Get("bucket") && in.agingKey(it, q.Get("dim")) == q.Get("key")
		case "overdue":
			return it.OpenAt(end) && it.DueAt != nil && end.After(it.DueAt.Add(24*time.Hour))
		case "open_cases":
			return it.Module == "Case" && it.OpenAt(end)
		case "open_tasks":
			return it.Module == "Task" && it.OpenAt(end)
		case "pending_approvals":
			return it.Module == "Approval" && it.OpenAt(end)
		case "in_stage":
			for _, sg := range it.Segments {
				if sg.Status == stage && ((sg.End != nil && w.Has(*sg.End)) || (sg.End == nil && it.OpenAt(end))) {
					detail[it] = "in " + stage + " for " + fmtHours(sg.Dur(in.Now).Hours())
					return true
				}
			}
			return false
		case "transition":
			for i := 1; i < len(it.Segments); i++ {
				if it.Segments[i-1].Status == q.Get("tfrom") && it.Segments[i].Status == q.Get("tto") && w.Has(it.Segments[i].Start) {
					detail[it] = "queued " + fmtHours(it.Segments[i].Dur(in.Now).Hours())
					return true
				}
			}
			return false
		case "loop":
			for _, l := range it.Loops {
				if w.Has(l.At) && strings.Join(l.Path, " → ") == q.Get("loop") {
					detail[it] = "loop cost " + fmtHours(l.Dur(in.Now).Hours())
					return true
				}
			}
			return false
		case "path":
			return strings.Join(it.Path, " → ") == q.Get("path") &&
				((it.Done() && w.Has(*it.CompletedAt)) || it.OpenAt(end) || w.Has(it.CreatedAt))
		case "stalled":
			return it.OpenAt(end) && end.Sub(it.LastEventAt) > stalledAfter
		case "skipped":
			return it.Done() && w.Has(*it.CompletedAt) && len(it.stage.Work) > 0 && it.Work == 0
		case "cycle_bin":
			b, _ := strconv.Atoi(q.Get("bin"))
			if b < 0 || b >= len(histBins) || !it.Done() || !w.Has(*it.CompletedAt) {
				return false
			}
			c := it.Cycle().Hours()
			lo := 0.0
			if b > 0 {
				lo = histBins[b-1].MaxH
			}
			return c >= lo && c < histBins[b].MaxH
		case "day":
			d, err := time.Parse("2006-01-02", q.Get("date"))
			if err != nil {
				return false
			}
			day := Window{d, d.AddDate(0, 0, 1)}
			for _, e := range in.Events {
				if e.RecordID == it.ID && day.Has(e.OccurredAt) {
					return true
				}
			}
			return false
		}
		return false
	}

	var picked []*WorkItem
	for _, it := range in.Items {
		if module != "" && it.Module != module {
			continue
		}
		if pick(it) {
			picked = append(picked, it)
		}
	}
	sortItems(picked)
	rs.Total = len(picked)
	if len(picked) > 500 {
		picked, rs.Truncated = picked[:500], true
	}
	for _, it := range picked {
		r := in.row(it)
		r.Detail = detail[it]
		rs.Rows = append(rs.Rows, r)
	}
	if st := stageFor(module); st != nil {
		rs.Title += " · " + st.Label
		if stage != "" {
			rs.Title += " · " + stage
		}
	}
	return rs
}

func (in *Intel) groupOf(person uint64, by string) uint64 {
	switch by {
	case "team":
		return in.primary[person][0]
	case "department":
		return in.primary[person][1]
	}
	return person
}

func (in *Intel) row(it *WorkItem) RecordRow {
	r := RecordRow{Item: it, Link: fmt.Sprintf("/command/record/%d", it.ID), Module: it.stage.Label, Status: statusName(it.Status),
		Department: in.Lk.department(it.Department), Team: in.Lk.team(it.Team), Assignee: in.Lk.person(it.Assignee),
		Customer: in.CustomerName(it.Customer), Loops: len(it.Loops), Handoffs: len(it.Handoffs()), Cycle: "—", SLA: "—"}
	if it.CompletedAt != nil {
		r.Cycle = fmtHours(it.Cycle().Hours())
	} else {
		r.Age = fmtHours(in.Now.Sub(it.CreatedAt).Hours())
	}
	if c := in.sla(it); c.slaApplicable {
		switch {
		case c.breachAt == nil:
			r.SLA = "met"
			if it.CompletedAt == nil {
				r.SLA = "on time"
			}
		case it.CompletedAt == nil:
			r.SLA = "open breach"
		default:
			r.SLA = "breached"
		}
	}
	return r
}

// ---------------------------------------------------------------------
// Record intelligence

type PathStep struct {
	Status   string
	Kind     string
	Start    time.Time
	Dur      string
	Hours    float64
	Assignee string
	Reentry  bool
	Open     bool
	Target   string
	Breached bool
}

type TimelineEntry struct {
	At    time.Time
	Kind  string // created | assigned | handoff | status | sla | completed | reopened | updated | deleted
	Text  string
	Who   string
	Alert bool
}

type RelatedRec struct {
	Module, Title, Status string
	Link                  string
}

type RecordIntel struct {
	Item       *WorkItem
	Module     string
	Status     string
	Kind       string
	Owner      string
	Department string
	Team       string
	Customer   string
	CaseTitle  string
	Created    time.Time
	Age        string
	Cycle      string
	Active     string
	Wait       string
	Blocked    string
	Handoff    string
	Rework     string
	Handoffs   int
	Loops      int
	SLA        string
	SLADetail  string
	Path       []PathStep
	MaxHours   float64
	Timeline   []TimelineEntry
	Related    []RelatedRec
	Partial    bool
	Workspace  string
}

// Record builds the intelligence view of one record of this company
func (in *Intel) RecordIntel(id uint64) (*RecordIntel, bool) {
	it := in.byID[id]
	if it == nil {
		return nil, false
	}
	ri := &RecordIntel{Item: it, Module: orStr(moduleLabels[it.Module], it.Module), Status: statusName(it.Status), Owner: in.Lk.person(it.Assignee),
		Department: in.Lk.department(it.Department), Team: in.Lk.team(it.Team), Customer: in.CustomerName(it.Customer),
		Created: it.CreatedAt, Partial: it.Partial, Handoffs: len(it.Handoffs()), Loops: len(it.Loops)}
	if it.Case > 0 && it.Module != "Case" {
		ri.CaseTitle = in.CaseName(it.Case)
	}
	if page := in.Lk.RecordPages[it.Module]; page > 0 && !it.Deleted && in.Lk.Slug != "" {
		ri.Workspace = fmt.Sprintf("/compose/ns/%s/pages/%d/record/%d", in.Lk.Slug, page, it.ID)
	}
	if it.stage != nil {
		ri.Kind = it.stage.kindOf(it.Status)
	}
	end := in.Now
	if it.CompletedAt != nil {
		end = *it.CompletedAt
		ri.Cycle = fmtHours(it.Cycle().Hours())
	} else {
		ri.Age = fmtHours(in.Now.Sub(it.CreatedAt).Hours())
	}
	_ = end

	var active, wait, blocked, handoff float64
	for _, sg := range it.Segments {
		d := sg.Dur(in.Now).Hours()
		step := PathStep{Status: sg.Status, Kind: sg.Kind, Start: sg.Start, Hours: d, Assignee: in.Lk.person(sg.Assignee), Reentry: sg.Reentry, Open: sg.End == nil && it.CompletedAt == nil}
		if sg.Kind != "done" && sg.Kind != "failed" {
			step.Dur = fmtHours(d)
			if t := in.Targets[targetKey(it.Module, sg.Status)]; t > 0 {
				step.Target = fmtHours(t)
				step.Breached = d > t
			}
		}
		switch sg.Kind {
		case "work":
			active += d
		case "wait":
			wait += d
		case "blocked":
			blocked += d
			wait += d
		}
		if d > ri.MaxHours && sg.Kind != "done" && sg.Kind != "failed" {
			ri.MaxHours = d
		}
		ri.Path = append(ri.Path, step)
	}
	for _, h := range it.Handoffs() {
		handoff += h.Wait(in.Now).Hours()
	}
	ri.Active, ri.Wait, ri.Blocked = fmtHours(active), fmtHours(wait), fmtHours(blocked)
	ri.Handoff, ri.Rework = fmtHours(handoff), fmtHours(it.ReworkTime(in.Now).Hours())

	c := in.sla(it)
	switch {
	case !c.slaApplicable:
		ri.SLA, ri.SLADetail = "No target", "No SLA target is configured for this workflow or its stages."
	case c.breachAt == nil:
		ri.SLA, ri.SLADetail = "Met", "Within every configured target."
		if it.CompletedAt == nil {
			ri.SLA = "On time so far"
		}
	default:
		where := "the workflow target"
		if c.breachStage != "" {
			where = "the " + c.breachStage + " stage target"
		}
		ri.SLA = "Breached"
		ri.SLADetail = fmt.Sprintf("Exceeded %s on %s, by %s.", where, c.breachAt.UTC().Format("Jan 2, 2006 15:04 UTC"), fmtHours(c.breachOver.Hours()))
	}

	// timeline from the raw events, plus computed SLA breach moments
	var assignee uint64
	for _, e := range in.Events {
		if e.RecordID != id {
			continue
		}
		who := in.Lk.person(e.ActorID)
		if e.ActorID == 0 {
			who = "System"
		}
		switch e.Kind {
		case ActivityCreated:
			txt := "Created"
			if e.ToStatus != "" {
				txt += " in " + e.ToStatus
			}
			if e.Source == "baseline" {
				txt += " (history before tracking began is not available)"
			}
			ri.Timeline = append(ri.Timeline, TimelineEntry{At: e.OccurredAt, Kind: "created", Text: txt, Who: who})
		case ActivityStatus:
			k, txt := "status", fmt.Sprintf("Moved from %s to %s", orStr(e.FromStatus, "no status"), orStr(e.ToStatus, "no status"))
			if it.stage != nil {
				switch {
				case it.stage.Done[e.ToStatus]:
					k, txt = "completed", "Completed · "+e.ToStatus
				case it.stage.Failed[e.ToStatus]:
					k, txt = "completed", "Finished as "+e.ToStatus
				case it.stage.terminal(e.FromStatus):
					k, txt = "reopened", fmt.Sprintf("Reopened: %s → %s", e.FromStatus, orStr(e.ToStatus, "no status"))
				case it.stage.rankOf(e.ToStatus) < it.stage.rankOf(e.FromStatus):
					k, txt = "reopened", fmt.Sprintf("Returned to %s from %s", e.ToStatus, e.FromStatus)
				}
			}
			ri.Timeline = append(ri.Timeline, TimelineEntry{At: e.OccurredAt, Kind: k, Text: txt, Who: who, Alert: k == "reopened"})
		case ActivityDeleted:
			ri.Timeline = append(ri.Timeline, TimelineEntry{At: e.OccurredAt, Kind: "deleted", Text: "Deleted", Who: who, Alert: true})
		default:
			if e.AssigneeID == 0 || e.AssigneeID == assignee {
				ri.Timeline = append(ri.Timeline, TimelineEntry{At: e.OccurredAt, Kind: "updated", Text: "Updated", Who: who})
			}
		}
		if e.AssigneeID > 0 && e.AssigneeID != assignee {
			txt := "Assigned to " + in.Lk.person(e.AssigneeID)
			k := "assigned"
			if assignee > 0 {
				txt = fmt.Sprintf("Handed off from %s to %s", in.Lk.person(assignee), in.Lk.person(e.AssigneeID))
				k = "handoff"
			}
			ri.Timeline = append(ri.Timeline, TimelineEntry{At: e.OccurredAt, Kind: k, Text: txt, Who: who})
			assignee = e.AssigneeID
		}
	}
	if wf := in.Targets[it.Module]; wf > 0 {
		at := it.CreatedAt.Add(hoursDur(wf))
		if (it.CompletedAt != nil && it.CompletedAt.After(at)) || (it.CompletedAt == nil && in.Now.After(at)) {
			ri.Timeline = append(ri.Timeline, TimelineEntry{At: at, Kind: "sla", Text: fmt.Sprintf("SLA breached — workflow target of %s exceeded", fmtHours(wf)), Alert: true})
		}
	}
	for _, sg := range it.Segments {
		t := in.Targets[targetKey(it.Module, sg.Status)]
		if t <= 0 || sg.Kind == "done" || sg.Kind == "failed" {
			continue
		}
		if sg.Dur(in.Now).Hours() > t {
			ri.Timeline = append(ri.Timeline, TimelineEntry{At: sg.Start.Add(hoursDur(t)), Kind: "sla", Text: fmt.Sprintf("SLA breached — %s target of %s exceeded", sg.Status, fmtHours(t)), Alert: true})
		}
	}
	sort.SliceStable(ri.Timeline, func(i, j int) bool { return ri.Timeline[i].At.Before(ri.Timeline[j].At) })

	// related records, from the links captured in the history
	rel := func(o *WorkItem) {
		if o == nil || o.ID == it.ID || o.Deleted || len(ri.Related) >= 40 {
			return
		}
		ri.Related = append(ri.Related, RelatedRec{Module: orStr(moduleLabels[o.Module], o.Module), Title: orStr(o.Title, "(untitled)"),
			Status: statusName(o.Status), Link: fmt.Sprintf("/command/record/%d", o.ID)})
	}
	if it.Case > 0 {
		rel(in.byID[it.Case])
	}
	if it.Customer > 0 && it.Module != "Customer" {
		rel(in.byID[it.Customer])
	}
	for _, o := range in.All {
		switch {
		case it.Module == "Case" && o.Case == it.ID:
			rel(o)
		case it.Module == "Customer" && o.Customer == it.ID:
			rel(o)
		case it.Case > 0 && o.Case == it.Case && o.Module != "Case":
			rel(o)
		}
	}
	return ri, true
}
