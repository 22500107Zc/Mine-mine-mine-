package saas

import (
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Execution intelligence: every figure below is computed from the company's
// own activity log (saas_activity_events), replayed into work items. Nothing
// is estimated from outside data or invented when history is missing; views
// say so instead.

// Scope is the persistent analysis scope of the Command Deck
type Scope struct {
	Preset   string // today 7d 30d 90d 6m 12m ytd all custom
	From, To time.Time
	FromDay  string // custom range inputs (YYYY-MM-DD)
	ToDay    string

	Module     string // workflow (pipeline module)
	Status     string // current status
	Category   string // case / approval / record type
	Priority   string
	Department uint64
	Team       uint64
	Assignee   uint64
}

// Window is a half-open time range [From, To)
type Window struct{ From, To time.Time }

func (w Window) Days() float64       { return w.To.Sub(w.From).Hours() / 24 }
func (w Window) Has(t time.Time) bool { return !t.Before(w.From) && t.Before(w.To) }

// overlap returns the part of [a, b) inside the window
func (w Window) overlap(a, b time.Time) time.Duration {
	if a.Before(w.From) {
		a = w.From
	}
	if b.After(w.To) {
		b = w.To
	}
	if b.Before(a) {
		return 0
	}
	return b.Sub(a)
}

type scopePreset struct{ Key, Label string }

var scopePresets = []scopePreset{
	{"today", "Today"}, {"7d", "7 days"}, {"30d", "30 days"}, {"90d", "90 days"},
	{"6m", "6 months"}, {"12m", "12 months"}, {"ytd", "Year to date"}, {"all", "All time"}, {"custom", "Custom range"},
}

// ParseIntelScope reads the scope bar from query parameters
func ParseIntelScope(q url.Values, now time.Time, earliest time.Time) Scope {
	now = now.UTC()
	s := Scope{Preset: q.Get("range")}

	// older links used period=30|90|365
	switch q.Get("period") {
	case "30":
		s.Preset = "30d"
	case "90":
		s.Preset = "90d"
	case "365":
		s.Preset = "12m"
	}

	today := dayOf(now)
	switch s.Preset {
	case "today":
		s.From = today
	case "7d":
		s.From = today.AddDate(0, 0, -6)
	case "90d":
		s.From = today.AddDate(0, 0, -89)
	case "6m":
		s.From = today.AddDate(0, -6, 0)
	case "12m":
		s.From = today.AddDate(-1, 0, 0)
	case "ytd":
		s.From = time.Date(now.Year(), 1, 1, 0, 0, 0, 0, time.UTC)
	case "all":
		s.From = dayOf(earliest)
		if earliest.IsZero() || s.From.After(today) {
			s.From = today.AddDate(0, 0, -29)
		}
	case "custom":
		from, err1 := time.Parse("2006-01-02", q.Get("from"))
		to, err2 := time.Parse("2006-01-02", q.Get("to"))
		if err1 == nil && err2 == nil && !to.Before(from) {
			s.From, s.FromDay, s.ToDay = from, q.Get("from"), q.Get("to")
			s.To = to.AddDate(0, 0, 1)
			break
		}
		s.Preset = "30d"
		s.From = today.AddDate(0, 0, -29)
	default:
		s.Preset = "30d"
		s.From = today.AddDate(0, 0, -29)
	}
	if s.To.IsZero() || s.To.After(now) {
		s.To = now
	}
	if s.FromDay == "" {
		s.FromDay = s.From.Format("2006-01-02")
		s.ToDay = s.To.Add(-time.Second).Format("2006-01-02")
	}

	if stageFor(q.Get("workflow")) != nil {
		s.Module = q.Get("workflow")
	}
	s.Status = clip(q.Get("status"), 80)
	s.Category = clip(q.Get("type"), 80)
	s.Priority = clip(q.Get("priority"), 40)
	s.Department, _ = strconv.ParseUint(q.Get("department"), 10, 64)
	s.Team, _ = strconv.ParseUint(q.Get("team"), 10, 64)
	s.Assignee, _ = strconv.ParseUint(q.Get("assignee"), 10, 64)
	return s
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n]
	}
	return s
}

// Window returns the scope's analysis window
func (s Scope) Window() Window { return Window{s.From, s.To} }

// Previous returns the comparable window just before the scope
func (s Scope) Previous() Window {
	d := s.To.Sub(s.From)
	return Window{s.From.Add(-d), s.From}
}

// Label describes the date range
func (s Scope) Label() string {
	for _, p := range scopePresets {
		if p.Key == s.Preset && p.Key != "custom" {
			if p.Key == "today" || p.Key == "ytd" || p.Key == "all" {
				return p.Label
			}
			return "Last " + p.Label
		}
	}
	return s.From.Format("Jan 2, 2006") + " – " + s.To.Add(-time.Second).Format("Jan 2, 2006")
}

// Filtered reports whether any record filter (beyond dates) is active
func (s Scope) Filtered() bool {
	return s.Module != "" || s.Status != "" || s.Category != "" || s.Priority != "" || s.Department > 0 || s.Team > 0 || s.Assignee > 0
}

// Values encodes the scope for links
func (s Scope) Values() url.Values {
	v := url.Values{}
	v.Set("range", s.Preset)
	if s.Preset == "custom" {
		v.Set("from", s.FromDay)
		v.Set("to", s.ToDay)
	}
	set := func(k, val string) {
		if val != "" {
			v.Set(k, val)
		}
	}
	set("workflow", s.Module)
	set("status", s.Status)
	set("type", s.Category)
	set("priority", s.Priority)
	if s.Department > 0 {
		v.Set("department", strconv.FormatUint(s.Department, 10))
	}
	if s.Team > 0 {
		v.Set("team", strconv.FormatUint(s.Team, 10))
	}
	if s.Assignee > 0 {
		v.Set("assignee", strconv.FormatUint(s.Assignee, 10))
	}
	return v
}

// Query returns the encoded scope
func (s Scope) Query() string { return s.Values().Encode() }

// URL builds a link to path carrying the scope plus extra key/value pairs
// (an empty value removes the key)
func (s Scope) URL(path string, kv ...string) string {
	v := s.Values()
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] == "" {
			v.Del(kv[i])
		} else {
			v.Set(kv[i], kv[i+1])
		}
	}
	return path + "?" + v.Encode()
}

// match reports whether a work item is inside the scope filters
func (s Scope) match(it *WorkItem) bool {
	if s.Module != "" && it.Module != s.Module {
		return false
	}
	if s.Status != "" && statusName(it.Status) != s.Status {
		return false
	}
	if s.Category != "" && it.Category != s.Category {
		return false
	}
	if s.Priority != "" && it.Priority != s.Priority {
		return false
	}
	if s.Department > 0 && it.Department != s.Department {
		return false
	}
	if s.Team > 0 && it.Team != s.Team {
		return false
	}
	if s.Assignee > 0 && it.Assignee != s.Assignee {
		return false
	}
	return true
}

// Lookups resolve names for display
type Lookups struct {
	Departments       map[uint64]string
	Teams             map[uint64]string
	TeamDepartment    map[uint64]uint64
	DepartmentManager map[uint64]uint64
	People            map[uint64]string
	RecordPages       map[string]uint64
	Slug              string
}

func (lk Lookups) person(id uint64) string {
	if id == 0 {
		return "Unassigned"
	}
	if n := lk.People[id]; n != "" {
		return n
	}
	return "Former team member"
}

func (lk Lookups) department(id uint64) string {
	if id == 0 {
		return "No department"
	}
	if n := lk.Departments[id]; n != "" {
		return n
	}
	return "Removed department"
}

func (lk Lookups) team(id uint64) string {
	if id == 0 {
		return "No team"
	}
	if n := lk.Teams[id]; n != "" {
		return n
	}
	return "Removed team"
}

// Intel is the execution-intelligence model of one company for one scope
type Intel struct {
	Now      time.Time
	Scope    Scope
	W, Prev  Window
	Earliest time.Time

	// All holds every reconstructed record (any module, deleted included);
	// Items the pipeline records inside the scope filters (not deleted)
	All   []*WorkItem
	Items []*WorkItem
	byID  map[uint64]*WorkItem

	// Events is the company's activity log, oldest first
	Events []ActivityEvent

	Targets map[string]float64
	Lk      Lookups

	calc      map[*WorkItem]*itemCalc
	customers map[uint64]string
	cases     map[uint64]string
	primary   map[uint64][2]uint64 // person → [team, department] they mostly work in
}

// itemCalc caches per-item results that depend on targets and "now"
type itemCalc struct {
	slaApplicable bool
	slaMet        bool
	breachAt      *time.Time // first moment a target was exceeded
	breachStage   string     // "" = whole workflow
	breachOver    time.Duration
}

// BuildIntel builds the model from reconstructed records (see intelCache)
func BuildIntel(all []*WorkItem, events []ActivityEvent, now time.Time, scope Scope, targets map[string]float64, lk Lookups) *Intel {
	in := &Intel{Now: now.UTC(), Scope: scope, W: scope.Window(), Prev: scope.Previous(), All: all, Events: events,
		Targets: targets, Lk: lk, byID: map[uint64]*WorkItem{}, calc: map[*WorkItem]*itemCalc{},
		customers: map[uint64]string{}, cases: map[uint64]string{}}
	if in.Targets == nil {
		in.Targets = map[string]float64{}
	}
	if len(events) > 0 {
		in.Earliest = events[0].OccurredAt
	}

	teamCount := map[uint64]map[uint64]int{}
	deptCount := map[uint64]map[uint64]int{}
	for _, it := range all {
		in.byID[it.ID] = it
		switch it.Module {
		case "Customer":
			if !it.Deleted {
				in.customers[it.ID] = it.Title
			}
		case "Case":
			in.cases[it.ID] = it.Title
		}
		if it.stage == nil || it.Deleted {
			continue
		}
		if it.Assignee > 0 {
			if teamCount[it.Assignee] == nil {
				teamCount[it.Assignee], deptCount[it.Assignee] = map[uint64]int{}, map[uint64]int{}
			}
			teamCount[it.Assignee][it.Team]++
			deptCount[it.Assignee][it.Department]++
		}
		in.calc[it] = in.computeSLA(it)
		if scope.match(it) {
			in.Items = append(in.Items, it)
		}
	}

	in.primary = map[uint64][2]uint64{}
	for p := range teamCount {
		in.primary[p] = [2]uint64{mode(teamCount[p]), mode(deptCount[p])}
	}
	return in
}

func mode(m map[uint64]int) uint64 {
	var best uint64
	n := -1
	for k, v := range m {
		if v > n || (v == n && k < best) {
			best, n = k, v
		}
	}
	return best
}

// computeSLA checks an item against its workflow and stage targets
func (in *Intel) computeSLA(it *WorkItem) *itemCalc {
	c := &itemCalc{}
	wf := in.Targets[it.Module]
	var earliest *time.Time
	mark := func(at time.Time, stage string, over time.Duration) {
		if earliest == nil || at.Before(*earliest) {
			t := at
			earliest = &t
			c.breachStage = stage
			c.breachOver = over
		}
	}

	if wf > 0 {
		c.slaApplicable = true
		limit := time.Duration(wf * float64(time.Hour))
		var took time.Duration
		switch {
		case it.CompletedAt != nil:
			took = it.CompletedAt.Sub(it.CreatedAt)
		default:
			took = in.Now.Sub(it.CreatedAt)
		}
		if took > limit {
			mark(it.CreatedAt.Add(limit), "", took-limit)
		}
	}

	for _, sg := range it.Segments {
		t := in.Targets[targetKey(it.Module, sg.Status)]
		if t <= 0 || sg.Kind == "done" || sg.Kind == "failed" {
			continue
		}
		c.slaApplicable = true
		limit := time.Duration(t * float64(time.Hour))
		if d := sg.Dur(in.Now); d > limit {
			mark(sg.Start.Add(limit), sg.Status, d-limit)
		}
	}

	c.breachAt = earliest
	c.slaMet = c.slaApplicable && earliest == nil
	return c
}

func (in *Intel) sla(it *WorkItem) *itemCalc {
	if c := in.calc[it]; c != nil {
		return c
	}
	return &itemCalc{}
}

// Record returns one record of this company (any module)
func (in *Intel) Record(id uint64) *WorkItem { return in.byID[id] }

// CustomerName resolves a customer record title
func (in *Intel) CustomerName(id uint64) string {
	if id == 0 {
		return ""
	}
	if n := in.customers[id]; n != "" {
		return n
	}
	return "Customer #" + strconv.FormatUint(id, 10)
}

// CaseName resolves a case title
func (in *Intel) CaseName(id uint64) string {
	if n := in.cases[id]; n != "" {
		return n
	}
	return "Case #" + strconv.FormatUint(id, 10)
}

// HasData reports whether the scope holds any tracked work
func (in *Intel) HasData() bool { return len(in.Items) > 0 }

// modules returns the workflows present in scope, in pipeline order
func (in *Intel) modules() []*stageDef {
	seen := map[string]bool{}
	for _, it := range in.Items {
		seen[it.Module] = true
	}
	var out []*stageDef
	for i := range pipelineStages {
		if seen[pipelineStages[i].Module] {
			out = append(out, &pipelineStages[i])
		}
	}
	return out
}

// scopedEvents returns the events of records inside the scope filters
func (in *Intel) scopedEvents() []ActivityEvent {
	if !in.Scope.Filtered() {
		return in.Events
	}
	keep := map[uint64]bool{}
	for _, it := range in.Items {
		keep[it.ID] = true
	}
	var out []ActivityEvent
	for _, e := range in.Events {
		if keep[e.RecordID] {
			out = append(out, e)
		}
	}
	return out
}

// ---------------------------------------------------------------------
// Cache: reconstructing a year of history per page view is wasteful, so
// records are rebuilt only when the company's activity log changes (every
// change appends an event, so the newest event id identifies the state).

type intelCacheEntry struct {
	version int64
	built   time.Time
	events  []ActivityEvent
	items   []*WorkItem
}

type intelCache struct {
	sync.Mutex
	m map[uint64]*intelCacheEntry
}

// cacheTTL bounds how long open work is measured against a stale "now"
const intelCacheTTL = 5 * time.Minute

func (c *intelCache) get(companyID uint64, version int64, now time.Time) *intelCacheEntry {
	c.Lock()
	defer c.Unlock()
	e := c.m[companyID]
	if e == nil || e.version != version || now.Sub(e.built) > intelCacheTTL || now.Before(e.built) {
		return nil
	}
	return e
}

func (c *intelCache) put(companyID uint64, e *intelCacheEntry) {
	c.Lock()
	defer c.Unlock()
	if c.m == nil {
		c.m = map[uint64]*intelCacheEntry{}
	}
	if len(c.m) >= 256 {
		// drop the oldest entry
		var oldest uint64
		var at time.Time
		for id, v := range c.m {
			if at.IsZero() || v.built.Before(at) {
				oldest, at = id, v.built
			}
		}
		delete(c.m, oldest)
	}
	c.m[companyID] = e
}

func (c *intelCache) invalidate(companyID uint64) {
	c.Lock()
	defer c.Unlock()
	delete(c.m, companyID)
}

// sortItems orders records newest first
func sortItems(ii []*WorkItem) {
	sort.SliceStable(ii, func(a, b int) bool { return ii[a].CreatedAt.After(ii[b].CreatedAt) })
}

// NewIntel replays events (oldest first) and builds the model; used where
// no cache applies (tests, one-off evaluations)
func NewIntel(events []ActivityEvent, now time.Time, scope Scope, targets map[string]float64, lk Lookups) *Intel {
	return BuildIntel(reconstruct(events, now.UTC(), lk.Departments), events, now, scope, targets, lk)
}
