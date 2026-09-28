package saas

import (
	"context"
	"errors"
	"fmt"
	"html"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// deckTab is one Command Deck view
type deckTab struct {
	Key, Label, Path, Title, Question string
	Group                             string // pipeline | process | org (sub-navigation)
	Sub                               string // label inside its group
}

// deckTabs are the Command Deck views, in navigation order
var deckTabs = []deckTab{
	{"deck", "Command Deck", "/command", "Command Deck", "What is happening · where · why · what it affects · what to do next", "", ""},
	{"activity", "Activity Graph", "/command/activity", "Activity History", "When does work happen, and what exactly happened?", "", ""},
	{"pipeline", "Pipeline & Bottlenecks", "/command/pipeline", "Pipeline & Bottlenecks", "Where does work stop moving?", "pipeline", "Stages"},
	{"map", "Bottleneck Map", "/command/map", "Bottleneck Map", "How does work flow between stages, and where does it pile up?", "pipeline", "Bottleneck Map"},
	{"sla", "SLA", "/command/sla", "Stages Breaching SLA", "Which targets are missed, where, by whom and by how much?", "pipeline", "SLA"},
	{"aging", "Aging", "/command/aging", "Aging Intelligence", "What has been open too long, and what is at risk?", "pipeline", "Aging"},
	{"throughput", "Throughput", "/command/throughput", "Throughput", "Is work finishing as fast as it arrives?", "pipeline", "Throughput"},
	{"outcomes", "Outcomes & Effects", "/command/outcomes", "Monthly Outcomes", "What did the month deliver, and how does it compare?", "", ""},
	{"process", "Process Review", "/command/process", "Process Review", "How does work actually move?", "process", "Paths"},
	{"handoffs", "Handoffs", "/command/handoffs", "Handoff Intelligence", "Where does work wait between owners and stages?", "process", "Handoffs"},
	{"rework", "Rework", "/command/rework", "Rework Intelligence", "What comes back, and what does it cost?", "process", "Rework"},
	{"organization", "Organization Frame", "/command/organization", "Organization Frame", "Who carries the work, and how does the company operate?", "org", "Structure"},
	{"capacity", "Capacity", "/command/capacity", "Capacity & Load", "How is work distributed across people, teams and departments?", "org", "Capacity"},
	{"goals", "Goal Intelligence", "/command/goals", "Goal Intelligence", "What are we aiming for, and are we getting there?", "", ""},
	{"tests", "Intervention Tests", "/command/tests", "Intervention Tests", "Did the change work?", "", ""},
	{"recommendations", "Recommendations", "/command/recommendations", "Recommendations", "What should management investigate next?", "", ""},
	{"changes", "What Changed", "/command/changes", "What Changed", "What moved, why, and compared with when?", "", ""},
	{"access", "Org & Access", "/command/access", "Org & Access", "Who can see and change what?", "", ""},
	{"report", "Report an Issue", "/command/report", "Report an Issue", "Something wrong or missing? Tell us.", "", ""},
}

// detail pages and the tab they belong to
var deckPages = map[string]deckTab{
	"stage":      {"stage", "", "/command/stage", "Stage Detail", "What happens inside this stage?", "pipeline", ""},
	"records":    {"records", "", "/command/records", "Evidence", "The exact records behind the number.", "", ""},
	"record":     {"record", "", "/command/record", "Record Intelligence", "What happened to this record, step by step?", "", ""},
	"department": {"department", "", "/command/department", "Department Detail", "How does this department execute?", "org", ""},
	"team":       {"team", "", "/command/team", "Team Detail", "How does this team execute?", "org", ""},
	"person":     {"person", "", "/command/person", "Workload Detail", "What is this person carrying? (workload, not a performance rating)", "org", ""},
	"goal":       {"goal", "", "/command/goals", "Goal Detail", "Is this goal on track, and what is driving it?", "", ""},
	"test":       {"test", "", "/command/tests", "Intervention Detail", "What changed after this intervention?", "", ""},
	"rec":        {"rec", "", "/command/recommendations", "Recommendation", "Why CulpOS suggests this, with the evidence.", "", ""},
	"defs":       {"defs", "", "/command/definitions", "Metric Definitions", "How every number is calculated.", "", ""},
}

var tabParent = map[string]string{"stage": "pipeline", "department": "organization", "team": "organization", "person": "capacity",
	"goal": "goals", "test": "tests", "rec": "recommendations", "records": "", "record": "", "defs": "deck"}

var issueCategories = []string{"Something is broken", "Data looks wrong", "Missing feature", "Billing", "Access or sign-in", "Other"}

// CanUseCommandDeck reports whether the role sees company-wide analytics
func (r CompanyRole) CanUseCommandDeck() bool {
	return r == RoleOwner || r == RoleAdministrator || r == RoleManager
}

type workflowOption struct{ Module, Label string }

type accessRow struct {
	Capability                      string
	Owner, Admin, Manager, Employee bool
}

var accessMatrix = []accessRow{
	{"Workspace: customers, cases, tasks, approvals, records", true, true, true, true},
	{"Command Deck, analytics and record drilldowns", true, true, true, false},
	{"Start intervention tests and set goals", true, true, true, false},
	{"Set SLA targets", true, true, false, false},
	{"Invite, remove and change team members", true, true, false, false},
	{"View billing and invoices", true, true, false, false},
	{"Change or cancel the subscription", true, false, false, false},
	{"Report an issue to support", true, true, true, true},
}

func workflows() []workflowOption {
	out := make([]workflowOption, 0, len(pipelineStages))
	for _, st := range pipelineStages {
		out = append(out, workflowOption{st.Module, st.Label})
	}
	return out
}

func (svc *Service) deckContext(w http.ResponseWriter, r *http.Request) (*customerCtx, bool) {
	cc, ok := svc.customer(w, r)
	if !ok {
		return nil, false
	}

	if cc.Decision.Level != AccessFull {
		http.Redirect(w, r, "/billing", http.StatusSeeOther)
		return nil, false
	}

	if !cc.Member.Role.CanUseCommandDeck() {
		svc.renderError(w, r, http.StatusForbidden)
		return nil, false
	}

	return cc, true
}

// loadIntel builds the execution-intelligence model of the company for the
// request's scope, reusing reconstructed history while the log is unchanged
func (svc *Service) loadIntel(ctx context.Context, c *Company, q url.Values) (*Intel, error) {
	svc.ensureActivityBaseline(ctx, c)
	now := svc.now().UTC()

	version, err := svc.repo.ActivityVersion(ctx, c.ID)
	if err != nil {
		return nil, err
	}
	lk, err := svc.platform.WorkspaceLookups(ctx, c)
	if err != nil {
		lk = WorkspaceLookup{}
	}

	entry := svc.intelCache.get(c.ID, version, now)
	if entry == nil {
		events, err := svc.repo.Activity(ctx, c.ID, time.Time{})
		if err != nil {
			return nil, err
		}
		entry = &intelCacheEntry{version: version, built: now, events: events, items: reconstruct(events, now, lk.Departments)}
		entry.names = svc.namesFor(ctx, events)
		svc.intelCache.put(c.ID, entry)
	}

	targets, err := svc.repo.DeckTargets(ctx, c.ID)
	if err != nil {
		return nil, err
	}
	var earliest time.Time
	if len(entry.events) > 0 {
		earliest = entry.events[0].OccurredAt
	}
	scope := ParseIntelScope(q, now, earliest)
	people := entry.names
	if lk.DepartmentManager != nil {
		for _, m := range lk.DepartmentManager {
			if _, ok := people[m]; !ok {
				people = svc.withNames(ctx, people, m)
			}
		}
	}
	return BuildIntel(entry.items, entry.events, entry.built, scope, targets, Lookups{
		Departments: lk.Departments, Teams: lk.Teams, TeamDepartment: lk.TeamDepartment, DepartmentManager: lk.DepartmentManager,
		People: people, RecordPages: lk.RecordPages, Slug: c.Slug,
	}), nil
}

// namesFor resolves every person who appears in the history
func (svc *Service) namesFor(ctx context.Context, events []ActivityEvent) map[uint64]string {
	ids := map[uint64]bool{}
	for _, e := range events {
		if e.ActorID > 0 {
			ids[e.ActorID] = true
		}
		if e.AssigneeID > 0 {
			ids[e.AssigneeID] = true
		}
	}
	var list []uint64
	for id := range ids {
		list = append(list, id)
	}
	out := map[uint64]string{}
	if len(list) == 0 {
		return out
	}
	uu, err := svc.platform.Users(ctx, list...)
	if err != nil {
		return out
	}
	for id, u := range uu {
		out[id] = orStr(u.Name, u.Email)
	}
	return out
}

func (svc *Service) withNames(ctx context.Context, m map[uint64]string, ids ...uint64) map[uint64]string {
	out := make(map[uint64]string, len(m)+len(ids))
	for k, v := range m {
		out[k] = v
	}
	if uu, err := svc.platform.Users(ctx, ids...); err == nil {
		for id, u := range uu {
			out[id] = orStr(u.Name, u.Email)
		}
	}
	return out
}

// scopeOption lists the values the scope bar offers
type scopeOptions struct {
	Presets     []scopePreset
	Workflows   []workflowOption
	Statuses    []workflowOption
	Categories  []string
	Priorities  []string
	Departments []workflowOption
	Teams       []workflowOption
	People      []workflowOption
}

func (in *Intel) scopeOptions() scopeOptions {
	o := scopeOptions{Presets: scopePresets, Workflows: workflows()}
	seenS, seenC, seenP := map[string]bool{}, map[string]bool{}, map[string]bool{}
	people := map[uint64]bool{}
	for _, st := range pipelineStages {
		if in.Scope.Module != "" && st.Module != in.Scope.Module {
			continue
		}
		for _, s := range st.Order {
			if !seenS[s] {
				seenS[s] = true
				o.Statuses = append(o.Statuses, workflowOption{s, s})
			}
		}
	}
	for _, it := range in.All {
		if it.stage == nil || it.Deleted {
			continue
		}
		if it.Category != "" && !seenC[it.Category] && (in.Scope.Module == "" || it.Module == in.Scope.Module) {
			seenC[it.Category] = true
			o.Categories = append(o.Categories, it.Category)
		}
		if it.Priority != "" && !seenP[it.Priority] {
			seenP[it.Priority] = true
			o.Priorities = append(o.Priorities, it.Priority)
		}
		if it.Assignee > 0 {
			people[it.Assignee] = true
		}
	}
	sort.Strings(o.Categories)
	sort.Strings(o.Priorities)
	for id, n := range in.Lk.Departments {
		o.Departments = append(o.Departments, workflowOption{strconv.FormatUint(id, 10), n})
	}
	for id, n := range in.Lk.Teams {
		o.Teams = append(o.Teams, workflowOption{strconv.FormatUint(id, 10), n})
	}
	for id := range people {
		o.People = append(o.People, workflowOption{strconv.FormatUint(id, 10), in.Lk.person(id)})
	}
	for _, l := range [][]workflowOption{o.Departments, o.Teams, o.People} {
		sort.Slice(l, func(i, j int) bool { return l[i].Label < l[j].Label })
	}
	return o
}

// scopeChip is one active filter shown above every view
type scopeChip struct{ Label, Clear string }

func (in *Intel) scopeChips(path string) []scopeChip {
	s := in.Scope
	var out []scopeChip
	add := func(label, key string) { out = append(out, scopeChip{label, s.URL(path, key, "")}) }
	if st := stageFor(s.Module); st != nil {
		add("Workflow: "+st.Label, "workflow")
	}
	if s.Status != "" {
		add("Status: "+s.Status, "status")
	}
	if s.Category != "" {
		add("Type: "+s.Category, "type")
	}
	if s.Priority != "" {
		add("Priority: "+s.Priority, "priority")
	}
	if s.Department > 0 {
		add("Department: "+in.Lk.department(s.Department), "department")
	}
	if s.Team > 0 {
		add("Team: "+in.Lk.team(s.Team), "team")
	}
	if s.Assignee > 0 {
		add("Assignee: "+in.Lk.person(s.Assignee), "assignee")
	}
	return out
}

func tabNav(active string) ([]deckTab, []deckTab) {
	group := ""
	for _, t := range deckTabs {
		if t.Key == active {
			group = t.Group
		}
	}
	var main, sub []deckTab
	seen := map[string]bool{}
	for _, t := range deckTabs {
		if t.Group != "" {
			if t.Group == group {
				sub = append(sub, t)
			}
			if seen[t.Group] {
				continue
			}
			seen[t.Group] = true
		}
		main = append(main, t)
	}
	return main, sub
}

// deckView renders one Command Deck view
func (svc *Service) deckView(view string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cc, ok := svc.deckContext(w, r)
		if !ok {
			return
		}
		ctx := r.Context()
		q := r.URL.Query()

		in, err := svc.loadIntel(ctx, cc.Company, q)
		if err != nil {
			svc.internalError(w, r, err)
			return
		}
		_ = svc.repo.RecordDeckView(ctx, cc.Company.ID, cc.UserID, svc.now())

		info, isTab := deckTab{}, false
		for _, t := range deckTabs {
			if t.Key == view {
				info, isTab = t, true
			}
		}
		if !isTab {
			info = deckPages[view]
		}
		active := view
		if p, ok := tabParent[view]; ok {
			active = p
		}
		main, sub := tabNav(active)
		if g := info.Group; g != "" && len(sub) == 0 {
			for _, t := range deckTabs {
				if t.Group == g {
					sub = append(sub, t)
				}
			}
		}

		d := svc.customerPage(cc, info.Title, "command")
		d["View"] = view
		d["Tab"] = active
		d["TabInfo"] = info
		d["Tabs"] = main
		d["SubTabs"] = sub
		d["In"] = in
		d["Scope"] = in.Scope
		d["ScopeOpts"] = in.scopeOptions()
		d["Chips"] = in.scopeChips(r.URL.Path)
		d["MainClass"] = "deck-main"
		d["CanSetTargets"] = cc.Member.Role.CanManageMembers()
		d["Title"] = info.Title
		d["Coverage"] = in.Coverage()
		d["Metrics"] = goalMetrics
		d["Workflows"] = workflows()
		extra := url.Values{}
		for _, k := range []string{"map", "unit", "dim", "basis", "by", "compare", "month", "view", "date", "filter", "set", "stage", "bucket", "key", "hfrom", "hto", "tfrom", "tto", "loop", "path", "bin"} {
			if v := q.Get(k); v != "" {
				extra.Set(k, v)
			}
		}
		d["ExtraQuery"] = extra

		switch view {
		case "deck":
			d["KPIs"] = in.KPIs()
			d["Summary"] = in.Summary()
			recs := in.Recommendations()
			if len(recs) > 3 {
				recs = recs[:3]
			}
			d["Recs"] = recs
			stages := in.Stages()
			d["Stages"] = stages
			d["Top"] = in.TopBottleneck(stages)
			ch := in.WhatChanged()
			if len(ch) > 6 {
				ch = ch[:6]
			}
			d["Changes"] = ch
			d["Heat"] = in.ActivityHistory("year", "all", in.Now, svc.adminEntries(ctx, cc.Company.ID, in), "/command/activity", nil)
			tests, _ := svc.repo.Interventions(ctx, cc.Company.ID)
			active := 0
			for _, t := range tests {
				if t.EndedAt == nil {
					active++
				}
			}
			d["ActiveTests"] = active

		case "activity":
			date, _ := time.Parse("2006-01-02", q.Get("date"))
			if q.Get("day") != "" {
				date, _ = time.Parse("2006-01-02", q.Get("day"))
				if q.Get("view") == "" {
					q.Set("view", "day")
				}
			}
			d["Act"] = in.ActivityHistory(q.Get("view"), q.Get("filter"), date, svc.adminEntries(ctx, cc.Company.ID, in), "/command/activity", in.Scope.Values())

		case "pipeline":
			d["Stages"] = in.Stages()
			d["M"] = in.Measure(in.W)

		case "map":
			module := in.Scope.Module
			if module == "" {
				module = orStr(q.Get("map"), in.BusiestModule())
			}
			d["MapModule"] = module
			d["Map"] = in.StagesOf(module)

		case "stage":
			module, status := q.Get("workflow"), q.Get("stage")
			ps := in.StagesOf(module)
			row := ps.Row(status)
			if row == nil {
				svc.renderError(w, r, http.StatusNotFound)
				return
			}
			d["PS"] = ps
			d["Row"] = row
			d["StageRecords"] = in.RecordSet(url.Values{"set": {"in_stage"}, "workflow": {module}, "stage": {status}})
			var into, out []Edge
			for _, e := range ps.Edges {
				if e.To == status {
					into = append(into, e)
				}
				if e.From == status {
					out = append(out, e)
				}
			}
			d["Into"], d["Out"] = into, out

		case "sla":
			d["SLAForm"] = in.slaForm()
			rows, breaches := in.SLA()
			d["SLARows"] = rows
			if len(breaches) > 200 {
				breaches = breaches[:200]
			}
			d["Breaches"] = breaches
			d["M"] = in.Measure(in.W)
			d["Targets"] = in.Targets

		case "aging":
			ag := in.Aging(q.Get("basis"), q.Get("dim"))
			d["Aging"] = ag
			mx := 0
			for _, row := range ag.Rows {
				for _, c := range row.Counts {
					if c > mx {
						mx = c
					}
				}
			}
			d["AgingMax"] = mx

		case "throughput":
			d["TP"] = in.Throughput(q.Get("unit"), q.Get("dim"))

		case "outcomes":
			month, err := time.Parse("2006-01", q.Get("month"))
			if err != nil {
				month = in.Now
			}
			d["Month"] = in.Month(month)
			tests, _ := svc.repo.Interventions(ctx, cc.Company.ID)
			goals, _ := svc.repo.Goals(ctx, cc.Company.ID)
			mw := Window{time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, time.UTC), time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0)}
			var tv []TestView
			for _, t := range tests {
				if t.StartedAt.Before(mw.To) && (t.EndedAt == nil || !t.EndedAt.Before(mw.From)) {
					tv = append(tv, in.EvaluateTest(t))
				}
			}
			var gv []GoalView
			for _, g := range goals {
				if g.Status == "active" || (g.ClosedAt != nil && mw.Has(*g.ClosedAt)) {
					gv = append(gv, in.EvaluateGoal(g))
				}
			}
			d["MonthTests"], d["MonthGoals"] = tv, gv
			recs := in.Recommendations()
			if len(recs) > 3 {
				recs = recs[:3]
			}
			d["Recs"] = recs

		case "process":
			module := in.Scope.Module
			if module == "" {
				module = orStr(q.Get("map"), in.BusiestModule())
			}
			d["MapModule"] = module
			d["PR"] = in.Process(module)

		case "handoffs":
			d["HO"] = in.Handoffs(q.Get("by"))
			if q.Get("tfrom") != "" {
				d["Transition"] = in.RecordSet(url.Values{"set": {"transition"}, "workflow": {q.Get("workflow")}, "tfrom": {q.Get("tfrom")}, "tto": {q.Get("tto")}})
				d["TFrom"], d["TTo"] = q.Get("tfrom"), q.Get("tto")
			}

		case "rework":
			d["RW"] = in.Rework()

		case "organization":
			cp := in.Capacity()
			d["Cap"] = cp
			d["Managers"] = in.Lk.DepartmentManager
			d["Ownership"] = in.processOwnership()

		case "capacity":
			d["Cap"] = in.Capacity()

		case "department", "team", "person":
			id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 64)
			if err != nil {
				svc.renderError(w, r, http.StatusNotFound)
				return
			}
			sq := url.Values{}
			for k, v := range q {
				sq[k] = v
			}
			label := ""
			switch view {
			case "department":
				if id > 0 {
					if _, ok := in.Lk.Departments[id]; !ok && !in.hasDepartment(id) {
						svc.renderError(w, r, http.StatusNotFound)
						return
					}
				}
				sq.Set("department", strconv.FormatUint(id, 10))
				label = in.Lk.department(id)
				d["Manager"] = in.Lk.person(in.Lk.DepartmentManager[id])
				d["HasManager"] = in.Lk.DepartmentManager[id] > 0
			case "team":
				if _, ok := in.Lk.Teams[id]; !ok && !in.hasTeam(id) {
					svc.renderError(w, r, http.StatusNotFound)
					return
				}
				sq.Set("team", strconv.FormatUint(id, 10))
				label = in.Lk.team(id)
			case "person":
				if !in.hasPerson(id) {
					svc.renderError(w, r, http.StatusNotFound)
					return
				}
				sq.Set("assignee", strconv.FormatUint(id, 10))
				label = in.Lk.person(id)
			}
			sub, err := svc.loadIntel(ctx, cc.Company, sq)
			if err != nil {
				svc.internalError(w, r, err)
				return
			}
			d["Sub"] = sub
			d["SubLabel"] = label
			d["SubID"] = id
			d["SubM"] = sub.Measure(sub.W)
			d["SubKPIs"] = sub.KPIs()
			stages := sub.Stages()
			var sev []StageRow
			for _, ps := range stages {
				for _, row := range ps.Rows {
					if row.Enough {
						sev = append(sev, row)
					}
				}
			}
			sort.Slice(sev, func(i, j int) bool { return sev[i].Severity > sev[j].Severity })
			if len(sev) > 5 {
				sev = sev[:5]
			}
			d["SubStages"] = sev
			d["SubHeat"] = sub.ActivityHistory("year", "all", sub.Now, nil, "/command/activity", sub.Scope.Values())
			d["SubMonths"] = sub.monthTrend(12)
			d["SubChanges"] = firstN(sub.WhatChanged(), 6)
			d["SubRecs"] = sub.Recommendations()
			d["SubHO"] = sub.Handoffs("person")
			d["SubCap"] = sub.Capacity()
			d["SubProcesses"] = sub.processMix()
			d["Title"] = label

		case "goals":
			goals, _ := svc.repo.Goals(ctx, cc.Company.ID)
			var gv []GoalView
			for _, g := range goals {
				gv = append(gv, in.EvaluateGoal(g))
			}
			d["Goals"] = gv
			d["SLAForm"] = in.slaForm()

		case "goal":
			id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
			g, err := svc.repo.GoalByID(ctx, cc.Company.ID, id)
			if errors.Is(err, ErrNotFound) {
				svc.renderError(w, r, http.StatusNotFound)
				return
			} else if err != nil {
				svc.internalError(w, r, err)
				return
			}
			d["Goal"] = in.EvaluateGoal(g)
			d["Title"] = g.Title

		case "tests":
			tests, _ := svc.repo.Interventions(ctx, cc.Company.ID)
			var tv []TestView
			for _, t := range tests {
				tv = append(tv, in.EvaluateTest(t))
			}
			d["Tests"] = tv
			d["Owners"] = in.scopeOptions().People

		case "test":
			id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
			t, err := svc.repo.InterventionByID(ctx, cc.Company.ID, id)
			if errors.Is(err, ErrNotFound) {
				svc.renderError(w, r, http.StatusNotFound)
				return
			} else if err != nil {
				svc.internalError(w, r, err)
				return
			}
			d["Test"] = in.EvaluateTest(t)
			d["Title"] = t.Title

		case "recommendations":
			d["Recs"] = in.Recommendations()

		case "rec":
			key := chi.URLParam(r, "key")
			var found *Rec
			for _, rc := range in.Recommendations() {
				if rc.Key == key {
					rc := rc
					found = &rc
				}
			}
			if found == nil {
				svc.renderError(w, r, http.StatusNotFound)
				return
			}
			d["Rec"] = found
			d["Title"] = found.Issue

		case "changes":
			cmp := q.Get("compare")
			cur, prev, cmp := CompareWindows(cmp, in.Now)
			d["Compare"] = cmp
			d["CompareOpts"] = comparePresets
			d["CompareRows"] = in.Compare(cur, prev)
			d["CmpCur"], d["CmpPrev"] = cur, prev
			d["Changes"] = in.WhatChanged()
			d["Why"] = in.WhyCycle(in.W, in.Prev)

		case "records":
			d["RS"] = in.RecordSet(q)

		case "record":
			id, _ := strconv.ParseUint(chi.URLParam(r, "id"), 10, 64)
			ri, ok := in.RecordIntel(id)
			if !ok {
				svc.renderError(w, r, http.StatusNotFound)
				return
			}
			d["RI"] = ri
			d["Title"] = orStr(ri.Item.Title, "Record")
			if q.Get("partial") == "1" {
				w.Header().Set("Cache-Control", "no-store")
				svc.render(w, r, http.StatusOK, "record-intel-partial", d)
				return
			}

		case "defs":
			d["Defs"] = metricDefs

		case "access":
			members, _ := svc.MembersWithInfo(ctx, cc.Company.ID)
			last := map[uint64]time.Time{}
			for _, e := range in.Events {
				if e.ActorID > 0 && e.OccurredAt.After(last[e.ActorID]) {
					last[e.ActorID] = e.OccurredAt
				}
			}
			d["Members"] = members
			d["LastActive"] = last
			d["Matrix"] = accessMatrix

		case "report":
			d["Categories"] = issueCategories
			d["Reports"], _ = svc.repo.IssueReports(ctx, cc.Company.ID, 10)
		}

		w.Header().Set("Cache-Control", "no-store")
		svc.render(w, r, http.StatusOK, "command-deck", d)
	}
}

func firstN(cc []Change, n int) []Change {
	if len(cc) > n {
		return cc[:n]
	}
	return cc
}

func (in *Intel) hasDepartment(id uint64) bool {
	for _, it := range in.All {
		if it.Department == id {
			return true
		}
	}
	return false
}

func (in *Intel) hasTeam(id uint64) bool {
	for _, it := range in.All {
		if it.Team == id {
			return true
		}
	}
	return false
}

func (in *Intel) hasPerson(id uint64) bool {
	if id == 0 {
		return false
	}
	for _, it := range in.All {
		if it.Assignee == id || it.People[id] {
			return true
		}
	}
	return false
}

// MonthPoint is one month of a detail page's trend
type MonthPoint struct {
	Label     string
	Completed int
	Cycle     float64
	HasCycle  bool
	Bar       float64
}

func (in *Intel) monthTrend(n int) []MonthPoint {
	first := time.Date(in.Now.Year(), in.Now.Month(), 1, 0, 0, 0, 0, time.UTC)
	var out []MonthPoint
	mx := 0
	for k := n - 1; k >= 0; k-- {
		s := first.AddDate(0, -k, 0)
		m := in.Measure(Window{s, minTime(s.AddDate(0, 1, 0), in.Now)})
		p := MonthPoint{Label: s.Format("Jan"), Completed: m.Completed, Cycle: m.Cycle.Median, HasCycle: m.Cycle.N > 0}
		if p.Completed > mx {
			mx = p.Completed
		}
		out = append(out, p)
	}
	for i := range out {
		if mx > 0 {
			out[i].Bar = float64(out[i].Completed) / float64(mx) * 100
		}
	}
	return out
}

func (in *Intel) processMix() []NameCount {
	m := map[string]int{}
	for _, it := range in.Items {
		if it.OpenAt(in.W.To) || (it.CompletedAt != nil && in.W.Has(*it.CompletedAt)) {
			m[it.stage.Label]++
		}
	}
	var out []NameCount
	for k, v := range m {
		out = append(out, NameCount{Name: k, Count: v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	return out
}

// Ownership is who holds most open work of each workflow
type Ownership struct {
	Workflow   string
	Department string
	DeptShare  float64
	Team       string
	TeamShare  float64
	Open       int
}

func (in *Intel) processOwnership() []Ownership {
	var out []Ownership
	for _, st := range in.modules() {
		depts, teams := map[uint64]float64{}, map[uint64]float64{}
		open := 0
		for _, it := range in.Items {
			if it.Module == st.Module && it.OpenAt(in.W.To) {
				open++
				depts[it.Department]++
				teams[it.Team]++
			}
		}
		if open == 0 {
			continue
		}
		o := Ownership{Workflow: st.Label, Open: open}
		id, sh := top(depts)
		o.Department, o.DeptShare = in.Lk.department(id), sh
		id, sh = top(teams)
		o.Team, o.TeamShare = in.Lk.team(id), sh
		out = append(out, o)
	}
	return out
}

// SLAField is one input of the SLA target form
type SLAField struct {
	Name, Label string
	Value       string
	Stage       bool
}

type SLAGroup struct {
	Label  string
	Fields []SLAField
}

func (in *Intel) slaForm() []SLAGroup {
	var out []SLAGroup
	for _, st := range pipelineStages {
		g := SLAGroup{Label: st.Label}
		val := func(k string) string {
			if v := in.Targets[k]; v > 0 {
				return strconv.FormatFloat(v, 'f', -1, 64)
			}
			return ""
		}
		g.Fields = append(g.Fields, SLAField{"target_" + st.Module, "Whole " + lower1(st.Label) + " (created → finished)", val(st.Module), false})
		for _, s := range st.Order {
			if st.terminal(s) {
				continue
			}
			g.Fields = append(g.Fields, SLAField{"target_" + st.Module + "|" + s, s, val(targetKey(st.Module, s)), true})
		}
		out = append(out, g)
	}
	return out
}

// adminEntries turns the company's audit log into activity entries
func (svc *Service) adminEntries(ctx context.Context, companyID uint64, in *Intel) []AdminEntry {
	if in.Scope.Filtered() {
		return nil
	}
	ee, err := svc.repo.Audit(ctx, companyID, "", 2000, 0)
	if err != nil {
		return nil
	}
	var out []AdminEntry
	for _, e := range ee {
		if strings.HasPrefix(e.Action, "founder.") {
			continue
		}
		label := e.Action
		if l, ok := actionLabels[e.Action]; ok {
			label = l
		}
		out = append(out, AdminEntry{At: e.OccurredAt, Label: label, Actor: orStr(e.ActorLabel, "—")})
	}
	return out
}

// ---------------------------------------------------------------------
// Actions

// deckAction handles Command Deck form posts
func (svc *Service) deckAction(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil || !svc.validCSRF(r) {
		svc.renderError(w, r, http.StatusForbidden)
		return
	}

	cc, ok := svc.deckContext(w, r)
	if !ok {
		return
	}

	ctx := r.Context()
	now := svc.now()
	ip := clientIP(r)
	actor := userActor(cc.UserID, cc.Member.Role, cc.Company.ID, ip)
	back := func(def string) string {
		if b := localPath(r.PostFormValue("back"), r.Host, ""); strings.HasPrefix(b, "/command") {
			return b
		}
		return def
	}

	switch chi.URLParam(r, "action") {
	case "targets":
		if !cc.Member.Role.CanManageMembers() {
			svc.renderError(w, r, http.StatusForbidden)
			return
		}
		type change struct {
			module, stage string
			hours         float64
		}
		var changes []change
		for key, vv := range r.PostForm {
			if !strings.HasPrefix(key, "target_") || len(vv) == 0 {
				continue
			}
			module, stage := splitTargetKey(strings.TrimPrefix(key, "target_"))
			st := stageFor(module)
			if st == nil || (stage != "" && !contains(st.Order, stage)) {
				continue
			}
			raw := strings.TrimSpace(vv[0])
			hours := 0.0
			if raw != "" {
				v, err := strconv.ParseFloat(raw, 64)
				if err != nil || v < 0 || v > 10000 || math.IsNaN(v) {
					svc.flashErr(w, userErr("Targets must be a number of hours between 0 and 10,000."))
					http.Redirect(w, r, back("/command/goals"), http.StatusSeeOther)
					return
				}
				hours = v
			}
			changes = append(changes, change{module, stage, hours})
		}
		for _, c := range changes {
			if err := svc.repo.SetStageTarget(ctx, cc.Company.ID, c.module, c.stage, c.hours, cc.UserID); err != nil {
				svc.internalError(w, r, err)
				return
			}
		}
		svc.audit(ctx, actor.with("deck.targets", "", ResultSuccess, nil))
		svc.setFlash(w, "success", "SLA targets saved.")
		http.Redirect(w, r, back("/command/goals"), http.StatusSeeOther)

	case "tests":
		title := strings.TrimSpace(r.PostFormValue("title"))
		module, stage, metric := r.PostFormValue("module"), strings.TrimSpace(r.PostFormValue("stage")), r.PostFormValue("metric")
		m, okm := goalMetric(metric)
		bdays, _ := strconv.Atoi(orStr(r.PostFormValue("baseline_days"), "30"))
		edays, _ := strconv.Atoi(orStr(r.PostFormValue("eval_days"), "30"))
		owner, _ := strconv.ParseUint(r.PostFormValue("owner"), 10, 64)
		notes := strings.TrimSpace(r.PostFormValue("notes"))
		startAt := now
		if s := r.PostFormValue("start"); s != "" {
			if t, err := time.Parse("2006-01-02", s); err == nil && !t.After(now) && now.Sub(t) < 366*24*time.Hour {
				startAt = t
			}
		}
		valid := okm && len(title) >= 3 && len(title) <= 200 && (module == "" || stageFor(module) != nil) &&
			bdays >= 7 && bdays <= 180 && edays >= 7 && edays <= 180 && len(notes) <= 2000
		if valid && m.NeedsStage {
			valid = stageFor(module) != nil && contains(stageFor(module).Order, stage)
		}
		if !valid {
			svc.flashErr(w, userErr("Give the test a name (3–200 characters), choose what to measure (a stage metric needs a workflow and stage), and windows of 7–180 days."))
			http.Redirect(w, r, back("/command/tests"), http.StatusSeeOther)
			return
		}
		if !m.NeedsStage {
			stage = ""
		}
		in, err := svc.loadIntel(ctx, cc.Company, url.Values{})
		if err != nil {
			svc.internalError(w, r, err)
			return
		}
		base, n := in.MetricIn(metric, module, stage, Window{startAt.AddDate(0, 0, -bdays), startAt})
		iv := &Intervention{CompanyID: cc.Company.ID, Title: title, Module: module, Stage: stage, Metric: metric, Baseline: base, BaselineN: n,
			BaselineDays: bdays, EvalDays: edays, OwnerID: owner, Notes: notes, StartedAt: startAt, CreatedBy: cc.UserID}
		if err = svc.repo.CreateIntervention(ctx, iv); err != nil {
			svc.internalError(w, r, err)
			return
		}
		svc.audit(ctx, actor.with("deck.test.start", title, ResultSuccess, map[string]string{"metric": metric}))
		svc.setFlash(w, "success", fmt.Sprintf("Test started. Baseline: the %d days before it started; results appear after a week.", bdays))
		http.Redirect(w, r, fmt.Sprintf("/command/tests/%d", iv.ID), http.StatusSeeOther)

	case "end-test":
		id, _ := strconv.ParseInt(r.PostFormValue("id"), 10, 64)
		iv, err := svc.repo.InterventionByID(ctx, cc.Company.ID, id)
		if errors.Is(err, ErrNotFound) {
			http.Redirect(w, r, "/command/tests", http.StatusSeeOther)
			return
		} else if err != nil {
			svc.internalError(w, r, err)
			return
		}
		in, err := svc.loadIntel(ctx, cc.Company, url.Values{})
		if err != nil {
			svc.internalError(w, r, err)
			return
		}
		tv := in.EvaluateTest(iv)
		ended, err := svc.repo.EndIntervention(ctx, cc.Company.ID, id, now, tv.After, float64(tv.AfterN))
		if err != nil {
			svc.internalError(w, r, err)
			return
		}
		if ended {
			svc.audit(ctx, actor.with("deck.test.end", strconv.FormatInt(id, 10), ResultSuccess, nil))
			svc.setFlash(w, "success", "Test ended. Its result is frozen at today’s measurement.")
		}
		http.Redirect(w, r, back("/command/tests"), http.StatusSeeOther)

	case "test-notes":
		id, _ := strconv.ParseInt(r.PostFormValue("id"), 10, 64)
		notes := strings.TrimSpace(r.PostFormValue("notes"))
		if len(notes) > 2000 {
			notes = notes[:2000]
		}
		if ok, err := svc.repo.SetInterventionNotes(ctx, cc.Company.ID, id, notes); err != nil {
			svc.internalError(w, r, err)
			return
		} else if ok {
			svc.setFlash(w, "success", "Notes saved.")
		}
		http.Redirect(w, r, fmt.Sprintf("/command/tests/%d", id), http.StatusSeeOther)

	case "goals":
		title := strings.TrimSpace(r.PostFormValue("title"))
		metric, module, stage := r.PostFormValue("metric"), r.PostFormValue("module"), strings.TrimSpace(r.PostFormValue("stage"))
		m, okm := goalMetric(metric)
		target, err := strconv.ParseFloat(strings.TrimSpace(r.PostFormValue("target")), 64)
		var targetAt *time.Time
		if s := r.PostFormValue("target_date"); s != "" {
			if t, err := time.Parse("2006-01-02", s); err == nil && t.After(now) {
				targetAt = &t
			}
		}
		valid := okm && metric != "cycle" && err == nil && target >= 0 && target <= 1e6 && len(title) >= 3 && len(title) <= 200 &&
			(module == "" || stageFor(module) != nil) && (m.Unit != "%" || target <= 100)
		if valid && m.NeedsStage {
			valid = stageFor(module) != nil && contains(stageFor(module).Order, stage)
		}
		if !valid {
			svc.flashErr(w, userErr("Give the goal a name, a metric and a numeric target (a stage metric needs a workflow and stage; percentages 0–100)."))
			http.Redirect(w, r, "/command/goals", http.StatusSeeOther)
			return
		}
		if !m.NeedsStage {
			stage = ""
		}
		in, err := svc.loadIntel(ctx, cc.Company, url.Values{})
		if err != nil {
			svc.internalError(w, r, err)
			return
		}
		base, n := in.MetricIn(metric, module, stage, Window{now.AddDate(0, 0, -goalWindowDays), now})
		g := &Goal{CompanyID: cc.Company.ID, Title: title, Metric: metric, Module: module, Stage: stage, Target: target,
			Baseline: base, BaselineN: n, StartAt: now, TargetAt: targetAt, CreatedBy: cc.UserID}
		if err = svc.repo.CreateGoal(ctx, g); err != nil {
			svc.internalError(w, r, err)
			return
		}
		svc.audit(ctx, actor.with("deck.goal.create", title, ResultSuccess, map[string]string{"metric": metric}))
		svc.setFlash(w, "success", "Goal created. Its baseline is the last 30 days.")
		http.Redirect(w, r, fmt.Sprintf("/command/goals/%d", g.ID), http.StatusSeeOther)

	case "goal-status":
		id, _ := strconv.ParseInt(r.PostFormValue("id"), 10, 64)
		status := r.PostFormValue("status")
		if status != "active" && status != "achieved" && status != "closed" {
			svc.renderError(w, r, http.StatusBadRequest)
			return
		}
		if ok, err := svc.repo.SetGoalStatus(ctx, cc.Company.ID, id, status, now); err != nil {
			svc.internalError(w, r, err)
			return
		} else if ok {
			svc.audit(ctx, actor.with("deck.goal."+status, strconv.FormatInt(id, 10), ResultSuccess, nil))
			svc.setFlash(w, "success", "Goal updated.")
		}
		http.Redirect(w, r, fmt.Sprintf("/command/goals/%d", id), http.StatusSeeOther)

	default:
		svc.renderError(w, r, http.StatusNotFound)
	}
}

// reportIssue records an issue from any company member and notifies support
func (svc *Service) reportIssue(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil || !svc.validCSRF(r) {
		svc.renderError(w, r, http.StatusForbidden)
		return
	}

	cc, ok := svc.customer(w, r)
	if !ok {
		return
	}

	back := localPath(r.Referer(), r.Host, "/support")
	if !strings.HasPrefix(back, "/command/report") {
		back = "/support"
	}

	category := r.PostFormValue("category")
	summary := strings.TrimSpace(r.PostFormValue("summary"))
	details := strings.TrimSpace(r.PostFormValue("details"))
	valid := false
	for _, c := range issueCategories {
		valid = valid || c == category
	}
	if !valid || len(summary) < 5 || len(summary) > 200 || len(details) > 4000 {
		svc.flashErr(w, userErr("Choose a category and describe the issue in 5–200 characters (details up to 4,000)."))
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}

	ir := &IssueReport{CompanyID: cc.Company.ID, UserID: cc.UserID, Category: category, Summary: summary, Details: details,
		Page: localPath(r.PostFormValue("page"), r.Host, "")}
	if len(ir.Page) > 300 {
		ir.Page = ir.Page[:300]
	}
	if err := svc.repo.CreateIssueReport(r.Context(), ir); err != nil {
		svc.internalError(w, r, err)
		return
	}

	svc.audit(r.Context(), userActor(cc.UserID, cc.Member.Role, cc.Company.ID, clientIP(r)).with("issue.reported", strconv.FormatInt(ir.ID, 10), ResultSuccess, map[string]string{"category": category}))

	if to := svc.cfg.Brand.ContactEmail(); to != "" && svc.mailer != nil {
		body := fmt.Sprintf(`<p><strong>Issue #%d</strong> from %s</p><p><strong>Category:</strong> %s</p><p><strong>Summary:</strong> %s</p><p>%s</p><p class="muted">Reported by user %d · page %s</p>`,
			ir.ID, html.EscapeString(cc.Company.Name), html.EscapeString(category), html.EscapeString(summary),
			strings.ReplaceAll(html.EscapeString(details), "\n", "<br>"), cc.UserID, html.EscapeString(orStr(ir.Page, "—")))
		if err := svc.mailer.Send(r.Context(), to, fmt.Sprintf("%s issue #%d: %s", svc.cfg.Brand.ProductName, ir.ID, summary), body); err != nil {
			svc.log.Warn("issue report email failed")
		}
	}

	svc.setFlash(w, "success", fmt.Sprintf("Thanks — issue #%d was sent to %s support.", ir.ID, svc.cfg.Brand.CompanyName))
	http.Redirect(w, r, back, http.StatusSeeOther)
}
