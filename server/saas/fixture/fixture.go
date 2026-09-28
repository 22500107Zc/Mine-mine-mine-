// Package fixture generates synthetic operational history for tests and for
// local/demo environments only. The CulpOS server never imports it: it is
// used by Go tests and by the culpos-fixture development command, and it
// must never be run against a production company.
package fixture

import (
	"fmt"
	"math"
	"math/rand"
	"sort"
	"time"

	"github.com/cortezaproject/corteza/server/saas"
)

// Org describes the organization the history is generated for
type Org struct {
	Departments map[uint64]string
	Teams       map[uint64]string
	TeamDept    map[uint64]uint64
	// People per team (user IDs)
	Members map[uint64][]uint64
}

// DefaultOrg is a small company: three departments, four teams, nine people
func DefaultOrg(people []uint64) Org {
	o := Org{
		Departments: map[uint64]string{9001: "Operations", 9002: "Finance", 9003: "Customer Service"},
		Teams:       map[uint64]string{9101: "Intake", 9102: "Review", 9103: "Approvals Desk", 9104: "Support"},
		TeamDept:    map[uint64]uint64{9101: 9001, 9102: 9001, 9103: 9002, 9104: 9003},
		Members:     map[uint64][]uint64{},
	}
	if len(people) == 0 {
		people = []uint64{101, 102, 103, 104, 105, 106, 107, 108, 109}
	}
	teams := []uint64{9101, 9102, 9103, 9104}
	for i, p := range people {
		t := teams[i%len(teams)]
		o.Members[t] = append(o.Members[t], p)
	}
	return o
}

// Options control the generated history
type Options struct {
	Now      time.Time
	Days     int     // length of history
	Volume   float64 // 1.0 ≈ 24 new work items per weekday
	Seed     int64
	FirstID  uint64
	Org      Org
	Customers int
}

type gen struct {
	o      Options
	r      *rand.Rand
	next   uint64
	events []saas.ActivityEvent
}

// Generate returns events oldest first
func Generate(o Options) []saas.ActivityEvent {
	if o.Volume == 0 {
		o.Volume = 1
	}
	if o.FirstID == 0 {
		o.FirstID = 5_000_000
	}
	if o.Customers == 0 {
		o.Customers = 30
	}
	g := &gen{o: o, r: rand.New(rand.NewSource(o.Seed)), next: o.FirstID}
	start := dayOf(o.Now).AddDate(0, 0, -o.Days)

	var customers []uint64
	for i := 0; i < o.Customers; i++ {
		id := g.id()
		customers = append(customers, id)
		at := start.Add(time.Duration(g.r.Intn(72)) * time.Hour).Add(-96 * time.Hour)
		g.emit(saas.ActivityEvent{OccurredAt: at, Module: "Customer", RecordID: id, Title: customerNames[i%len(customerNames)] + suffix(i), Kind: saas.ActivityCreated,
			ToStatus: "Active", CustomerID: id, AssigneeID: g.member(9104), ActorID: g.member(9101)})
	}

	for d := 0; d < o.Days; d++ {
		day := start.AddDate(0, 0, d)
		wd := day.Weekday()
		weekend := wd == time.Saturday || wd == time.Sunday
		scale := o.Volume
		if weekend {
			scale *= 0.12
		}
		// recent regression: approvals slow down in the last 30 days;
		// review gets faster after a process change 25 days ago
		recent := o.Now.Sub(day) < 30*24*time.Hour
		improved := o.Now.Sub(day) < 25*24*time.Hour

		for i := 0; i < g.poisson(10*scale); i++ {
			g.task(day, customers, improved)
		}
		for i := 0; i < g.poisson(4*scale); i++ {
			g.caseFlow(day, customers)
		}
		for i := 0; i < g.poisson(6*scale); i++ {
			g.approval(day, recent)
		}
		for i := 0; i < g.poisson(4*scale); i++ {
			g.record(day, customers, improved)
		}
		for i := 0; i < g.poisson(1.5*scale); i++ {
			id := g.id()
			g.emit(saas.ActivityEvent{OccurredAt: g.workTime(day), Module: "Document", RecordID: id, Title: fmt.Sprintf("%s %d", docNames[g.r.Intn(len(docNames))], id%10000),
				Kind: saas.ActivityCreated, Category: docNames[g.r.Intn(len(docNames))], CustomerID: customers[g.r.Intn(len(customers))], ActorID: g.anyone()})
		}
	}

	// nothing can happen after "now"
	var out []saas.ActivityEvent
	for _, e := range g.events {
		if !e.OccurredAt.After(o.Now) {
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].OccurredAt.Before(out[j].OccurredAt) })
	return out
}

func dayOf(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func suffix(i int) string {
	if i < len(customerNames) {
		return ""
	}
	return fmt.Sprintf(" %d", i/len(customerNames)+1)
}

func (g *gen) id() uint64 { g.next++; return g.next }

func (g *gen) emit(e saas.ActivityEvent) {
	if e.Source == "" {
		e.Source = "live"
	}
	g.events = append(g.events, e)
}

func (g *gen) poisson(lambda float64) int {
	l, k, p := math.Exp(-lambda), 0, 1.0
	for {
		p *= g.r.Float64()
		if p <= l {
			return k
		}
		k++
	}
}

// hours draws a log-normal duration around a median (hours)
func (g *gen) hours(median, spread float64) time.Duration {
	h := median * math.Exp(g.r.NormFloat64()*spread)
	return time.Duration(math.Max(0.1, h) * float64(time.Hour))
}

// workTime is a moment during business hours of the day (UTC)
func (g *gen) workTime(day time.Time) time.Time {
	return day.Add(time.Duration(8*60+g.r.Intn(9*60)) * time.Minute)
}

func (g *gen) member(team uint64) uint64 {
	mm := g.o.Org.Members[team]
	if len(mm) == 0 {
		return 0
	}
	// the first member of each team carries more work
	if g.r.Float64() < 0.45 {
		return mm[0]
	}
	return mm[g.r.Intn(len(mm))]
}

func (g *gen) anyone() uint64 {
	var all []uint64
	for _, mm := range g.o.Org.Members {
		all = append(all, mm...)
	}
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
	if len(all) == 0 {
		return 0
	}
	return all[g.r.Intn(len(all))]
}

type rec struct {
	g    *gen
	base saas.ActivityEvent
	at   time.Time
	st   string
	who  uint64
}

func (g *gen) start(module, title string, at time.Time, status string, team uint64, extra func(*saas.ActivityEvent)) *rec {
	id := g.id()
	e := saas.ActivityEvent{Module: module, RecordID: id, Title: title, TeamID: team, DepartmentID: g.o.Org.TeamDept[team], AssigneeID: g.member(team)}
	if extra != nil {
		extra(&e)
	}
	c := e
	c.OccurredAt, c.Kind, c.ToStatus, c.ActorID = at, saas.ActivityCreated, status, g.member(9101)
	g.emit(c)
	return &rec{g: g, base: e, at: at, st: status, who: e.AssigneeID}
}

func (r *rec) move(after time.Duration, to string) {
	r.at = r.at.Add(after)
	e := r.base
	e.Title = ""
	e.OccurredAt, e.Kind, e.FromStatus, e.ToStatus, e.ActorID, e.AssigneeID = r.at, saas.ActivityStatus, r.st, to, r.who, r.who
	r.g.emit(e)
	r.st = to
}

// handoff changes the owner (to a member of another team)
func (r *rec) handoff(after time.Duration, team uint64) {
	r.at = r.at.Add(after)
	to := r.g.member(team)
	e := r.base
	e.Title = ""
	e.OccurredAt, e.Kind, e.ToStatus, e.ActorID, e.AssigneeID, e.TeamID = r.at, saas.ActivityUpdated, r.st, r.who, to, team
	e.DepartmentID = r.g.o.Org.TeamDept[team]
	r.g.emit(e)
	r.who = to
	r.base.AssigneeID, r.base.TeamID, r.base.DepartmentID = to, team, e.DepartmentID
}

func (g *gen) task(day time.Time, customers []uint64, improved bool) {
	prio := []string{"Low", "Normal", "Normal", "High", "Urgent"}[g.r.Intn(5)]
	due := day.AddDate(0, 0, 2+g.r.Intn(6))
	r := g.start("Task", fmt.Sprintf("%s #%d", taskNames[g.r.Intn(len(taskNames))], g.next%100000), g.workTime(day), "Open", 9101, func(e *saas.ActivityEvent) {
		e.Priority, e.DueAt, e.CustomerID = prio, &due, customers[g.r.Intn(len(customers))]
	})
	r.move(g.hours(5, 0.9), "In Progress")
	if g.r.Float64() < 0.18 {
		r.move(g.hours(3, 0.7), "Blocked")
		r.move(g.hours(14, 0.8), "In Progress")
	}
	if g.r.Float64() < 0.45 {
		// review by a second team
		r.handoff(g.hours(4, 0.6), 9102)
		review := 9.0
		if improved {
			review = 4.0
		}
		r.move(g.hours(1, 0.5), "Waiting")
		r.move(g.hours(review, 0.8), "In Progress")
	}
	r.move(g.hours(6, 0.8), "Done")
	if g.r.Float64() < 0.1 {
		r.move(g.hours(20, 0.8), "In Progress")
		r.move(g.hours(6, 0.7), "Done")
	}
}

func (g *gen) caseFlow(day time.Time, customers []uint64) {
	cust := customers[g.r.Intn(len(customers))]
	due := day.AddDate(0, 0, 3+g.r.Intn(5))
	r := g.start("Case", fmt.Sprintf("%s — %s", caseNames[g.r.Intn(len(caseNames))], customerNames[int(cust)%len(customerNames)]), g.workTime(day), "New", 9104, func(e *saas.ActivityEvent) {
		e.Category = []string{"Question", "Problem", "Incident", "Request", "Complaint"}[g.r.Intn(5)]
		e.Priority, e.DueAt, e.CustomerID = []string{"Low", "Normal", "High", "Urgent"}[g.r.Intn(4)], &due, cust
	})
	r.move(g.hours(3, 0.8), "Open")
	if g.r.Float64() < 0.35 {
		r.move(g.hours(6, 0.7), "Pending")
		r.move(g.hours(30, 0.8), "Open")
	}
	r.move(g.hours(14, 0.8), "Resolved")
	if g.r.Float64() < 0.14 {
		r.move(g.hours(26, 0.7), "Open")
		r.move(g.hours(10, 0.7), "Resolved")
	}
	r.move(g.hours(20, 0.6), "Closed")

	// follow-up task linked to the case
	if g.r.Float64() < 0.4 {
		caseID := r.base.RecordID
		t := g.start("Task", "Follow up: "+r.base.Title, r.at.Add(-g.hours(8, 0.5)), "Open", 9104, func(e *saas.ActivityEvent) {
			e.CaseID, e.CustomerID, e.Priority = caseID, cust, "Normal"
		})
		t.move(g.hours(4, 0.6), "In Progress")
		t.move(g.hours(5, 0.6), "Done")
	}
}

func (g *gen) approval(day time.Time, slower bool) {
	r := g.start("Approval", fmt.Sprintf("%s request #%d", approvalNames[g.r.Intn(len(approvalNames))], g.next%100000), g.workTime(day), "Pending", 9103, func(e *saas.ActivityEvent) {
		e.Category = approvalNames[g.r.Intn(len(approvalNames))]
	})
	wait := 14.0
	if slower {
		wait = 30.0
	}
	if g.r.Float64() < 0.3 {
		r.handoff(g.hours(6, 0.7), 9103)
	}
	final := "Approved"
	if g.r.Float64() < 0.12 {
		final = "Rejected"
	}
	r.move(g.hours(wait, 0.9), final)
	if final == "Approved" && g.r.Float64() < 0.09 {
		r.move(g.hours(18, 0.6), "Pending")
		r.move(g.hours(12, 0.7), "Approved")
	}
}

func (g *gen) record(day time.Time, customers []uint64, improved bool) {
	r := g.start("OperationsRecord", fmt.Sprintf("%s %d", recordNames[g.r.Intn(len(recordNames))], g.next%100000), g.workTime(day), "Open", 9102, func(e *saas.ActivityEvent) {
		e.Category = []string{"Operations", "Finance", "Compliance", "Facilities", "HR"}[g.r.Intn(5)]
		e.CustomerID = customers[g.r.Intn(len(customers))]
	})
	r.move(g.hours(10, 0.8), "In Review")
	back := 0.22
	if improved {
		back = 0.12
	}
	if g.r.Float64() < back {
		r.move(g.hours(8, 0.6), "Open")
		r.move(g.hours(12, 0.7), "In Review")
	}
	r.move(g.hours(16, 0.8), "Closed")
}

var (
	customerNames = []string{"Northwind Traders", "Blue Harbor Logistics", "Cedar Health", "Summit Foods", "Atlas Freight", "Lumen Energy", "Pioneer Retail", "Harborview Clinics", "Keystone Builders", "Riverstone Bank", "Orion Media", "Evergreen Farms"}
	taskNames     = []string{"Prepare shipment", "Update vendor file", "Reconcile invoice", "Schedule inspection", "Order replacement", "Onboard account", "Verify documents", "Draft proposal"}
	caseNames     = []string{"Delivery delayed", "Billing question", "Damaged goods", "Access request", "Contract change", "Service outage"}
	approvalNames = []string{"Purchase", "Expense", "Contract", "Time Off"}
	recordNames   = []string{"Compliance check", "Facility report", "Audit sample", "Quality review", "Vendor assessment"}
	docNames      = []string{"Contract", "Invoice", "Policy", "Proposal", "Report"}
)
