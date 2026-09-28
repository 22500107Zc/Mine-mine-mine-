package saas

import (
	"fmt"
	"html"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

type deckTab struct {
	Key, Label, Path, Title, Question string
}

// deckTabs are the Command Deck views, in navigation order
var deckTabs = []deckTab{
	{"deck", "Command Deck", "/command", "Command Deck", "What is happening · where · why might it · what it affects · what to test next"},
	{"activity", "Activity Graph", "/command/activity", "Activity Graph", "When does work happen?"},
	{"pipeline", "Pipeline & Bottlenecks", "/command/pipeline", "Pipeline & Bottlenecks", "Where does work stop moving?"},
	{"outcomes", "Outcomes & Effects", "/command/outcomes", "Outcomes & Effects", "What is it affecting?"},
	{"process", "Process Review", "/command/process", "Process Review", "How does work actually move?"},
	{"organization", "Organization Frame", "/command/organization", "Organization Frame", "Who carries the work?"},
	{"goals", "Goal Intelligence", "/command/goals", "Goal Intelligence", "What should we aim for, and what should we test?"},
	{"tests", "Intervention Tests", "/command/tests", "Intervention Tests", "Did the change work?"},
	{"access", "Org & Access", "/command/access", "Org & Access", "Who can see and change what?"},
	{"report", "Report an Issue", "/command/report", "Report an Issue", "Something wrong or missing? Tell us."},
}

// deckLookback covers the 12-month graph plus a 30-day comparison baseline
const deckLookback = 400 * 24 * time.Hour

var issueCategories = []string{"Something is broken", "Data looks wrong", "Missing feature", "Billing", "Access or sign-in", "Other"}

// CanUseCommandDeck reports whether the role sees company-wide analytics
func (r CompanyRole) CanUseCommandDeck() bool {
	return r == RoleOwner || r == RoleAdministrator || r == RoleManager
}

type deckActivityRow struct {
	At     time.Time
	Module string
	Title  string
	What   string
	Link   string
}

type workflowOption struct{ Module, Label string }

type interventionView struct {
	*Intervention
	MetricLabel  string
	Unit         string
	WorkflowName string
	Current      float64
	CurrentN     int
	Days         int
	ChangePct    float64
	Verdict      string // collecting | improved | worse | unclear
}

type accessRow struct {
	Capability                      string
	Owner, Admin, Manager, Employee bool
}

var accessMatrix = []accessRow{
	{"Workspace: customers, cases, tasks, approvals, records", true, true, true, true},
	{"Command Deck and analytics", true, true, true, false},
	{"Set goals and start intervention tests", true, true, true, false},
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

func (svc *Service) commandDeck(tab string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cc, ok := svc.deckContext(w, r)
		if !ok {
			return
		}

		ctx := r.Context()
		svc.ensureActivityBaseline(ctx, cc.Company)

		now := svc.now()
		events, err := svc.repo.Activity(ctx, cc.Company.ID, now.Add(-deckLookback))
		if err != nil {
			svc.internalError(w, r, err)
			return
		}

		lk, err := svc.platform.WorkspaceLookups(ctx, cc.Company)
		if err != nil {
			lk = WorkspaceLookup{}
		}

		targets, _ := svc.repo.DeckTargets(ctx, cc.Company.ID)
		deck := BuildDeck(events, now, lk.Departments)

		d := svc.customerPage(cc, "Command Deck", "command")
		d["Deck"] = deck
		d["Tab"] = tab
		d["Tabs"] = deckTabs
		d["MainClass"] = "deck-main"
		d["Workflows"] = workflows()
		d["Targets"] = targets
		d["CanSetGoals"] = cc.Member.Role.CanManageMembers()
		for _, t := range deckTabs {
			if t.Key == tab {
				d["TabInfo"] = t
				d["Title"] = t.Title
			}
		}

		// scoped analysis for the diagnostic tabs
		scope := ParseScope(r.URL.Query().Get("period"), r.URL.Query().Get("workflow"), r.URL.Query().Get("department"))
		scoped := deck
		if tab == "pipeline" || tab == "outcomes" || tab == "process" || tab == "organization" {
			if tab == "process" && scope.Module == "" {
				scope.Module = busiestWorkflow(deck)
			}
			scoped = BuildDeck(FilterEvents(events, scope, now), now, lk.Departments)
		}
		d["Scope"] = scope
		d["Scoped"] = scoped
		d["Periods"] = scopePeriods
		d["Departments"] = sortedDepartments(lk.Departments)

		switch tab {
		case "activity":
			day, _ := time.Parse("2006-01-02", r.URL.Query().Get("day"))
			d["Day"] = day
			d["DayRows"] = svc.deckDayRows(cc.Company, events, day, lk)

		case "pipeline":
			d["Pipeline"] = Pipeline(scoped, scoped.Tracked, targets)
			if scope.Module != "" {
				d["Statuses"] = StatusBreakdown(scoped.Tracked, scope.Module)
			}

		case "outcomes":
			d["Outcomes"] = Outcomes(scoped.Tracked, now)

		case "process":
			d["Process"] = Process(scoped.Tracked, scope.Module)

		case "organization":
			ids := map[uint64]bool{}
			for _, it := range scoped.Tracked {
				if it.Assignee > 0 {
					ids[it.Assignee] = true
				}
			}
			d["Org"] = Organization(scoped.Tracked, now, svc.userNames(r, ids), lk.Departments)

		case "goals":
			d["Pipeline"] = Pipeline(deck, deck.Tracked, targets)

		case "tests":
			d["Tests"] = svc.interventionViews(r, cc.Company.ID, deck, now)
			d["Metrics"] = testMetricOptions()

		case "access":
			members, _ := svc.MembersWithInfo(ctx, cc.Company.ID)
			last := map[uint64]time.Time{}
			for _, e := range events {
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

func busiestWorkflow(d *Deck) string {
	counts := map[string]int{}
	for _, it := range d.Tracked {
		counts[it.Module]++
	}
	best := pipelineStages[0].Module
	for _, st := range pipelineStages {
		if counts[st.Module] > counts[best] {
			best = st.Module
		}
	}
	return best
}

func sortedDepartments(m map[uint64]string) []workflowOption {
	var out []workflowOption
	for id, name := range m {
		out = append(out, workflowOption{strconv.FormatUint(id, 10), name})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out
}

func (svc *Service) userNames(r *http.Request, ids map[uint64]bool) map[uint64]string {
	var list []uint64
	for id := range ids {
		list = append(list, id)
	}
	out := map[uint64]string{}
	if len(list) == 0 {
		return out
	}
	uu, err := svc.platform.Users(r.Context(), list...)
	if err != nil {
		return out
	}
	for id, u := range uu {
		out[id] = orStr(u.Name, u.Email)
	}
	return out
}

func testMetricOptions() []workflowOption {
	var out []workflowOption
	for _, k := range []string{"cycle", "wait", "rework", "breach", "throughput"} {
		out = append(out, workflowOption{k, testMetrics[k].Label})
	}
	return out
}

func (svc *Service) interventionViews(r *http.Request, companyID uint64, d *Deck, now time.Time) []interventionView {
	ivs, _ := svc.repo.Interventions(r.Context(), companyID)
	var out []interventionView
	for _, iv := range ivs {
		m := testMetrics[iv.Metric]
		end := now
		if iv.EndedAt != nil {
			end = *iv.EndedAt
		}

		v := interventionView{Intervention: iv, MetricLabel: m.Label, Unit: m.Unit, WorkflowName: "All workflows"}
		if st := stageFor(iv.Module); st != nil {
			v.WorkflowName = st.Label
		}
		v.Days = int(end.Sub(iv.StartedAt).Hours() / 24)
		v.Current, v.CurrentN = MetricValue(d.Tracked, iv.Module, iv.Metric, iv.StartedAt, end)

		switch {
		case v.Days < 14 || v.CurrentN < 5 || iv.BaselineN < 5:
			v.Verdict = "collecting"
		case iv.Baseline == 0:
			v.Verdict = "unclear"
		default:
			v.ChangePct = (v.Current - iv.Baseline) / iv.Baseline * 100
			better := v.ChangePct <= -10
			worse := v.ChangePct >= 10
			if m.HigherBetter {
				better, worse = v.ChangePct >= 10, v.ChangePct <= -10
			}
			switch {
			case better:
				v.Verdict = "improved"
			case worse:
				v.Verdict = "worse"
			default:
				v.Verdict = "unclear"
			}
		}
		out = append(out, v)
	}
	return out
}

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

	switch chi.URLParam(r, "action") {
	case "targets":
		if !cc.Member.Role.CanManageMembers() {
			svc.renderError(w, r, http.StatusForbidden)
			return
		}
		for _, st := range pipelineStages {
			raw := strings.TrimSpace(r.PostFormValue("target_" + st.Module))
			hours := 0.0
			if raw != "" {
				v, err := strconv.ParseFloat(raw, 64)
				if err != nil || v < 0 || v > 10000 {
					svc.flashErr(w, userErr("Targets must be a number of hours between 0 and 10,000."))
					http.Redirect(w, r, "/command/goals", http.StatusSeeOther)
					return
				}
				hours = v
			}
			if err := svc.repo.SetDeckTarget(ctx, cc.Company.ID, st.Module, hours, cc.UserID); err != nil {
				svc.internalError(w, r, err)
				return
			}
		}
		svc.audit(ctx, userActor(cc.UserID, cc.Member.Role, cc.Company.ID, ip).with("deck.targets", "", ResultSuccess, nil))
		svc.setFlash(w, "success", "Targets saved.")
		http.Redirect(w, r, "/command/goals", http.StatusSeeOther)

	case "tests":
		title := strings.TrimSpace(r.PostFormValue("title"))
		module := r.PostFormValue("module")
		metric := r.PostFormValue("metric")
		if _, ok := testMetrics[metric]; !ok || len(title) < 3 || len(title) > 200 || (module != "" && stageFor(module) == nil) {
			svc.flashErr(w, userErr("Give the test a name (3–200 characters) and choose what to measure."))
			http.Redirect(w, r, "/command/tests", http.StatusSeeOther)
			return
		}

		events, err := svc.repo.Activity(ctx, cc.Company.ID, now.Add(-deckLookback))
		if err != nil {
			svc.internalError(w, r, err)
			return
		}
		deck := BuildDeck(events, now, nil)
		base, n := MetricValue(deck.Tracked, module, metric, now.AddDate(0, 0, -28), now)

		iv := &Intervention{CompanyID: cc.Company.ID, Title: title, Module: module, Metric: metric, Baseline: base, BaselineN: n, StartedAt: now, CreatedBy: cc.UserID}
		if err = svc.repo.CreateIntervention(ctx, iv); err != nil {
			svc.internalError(w, r, err)
			return
		}
		svc.audit(ctx, userActor(cc.UserID, cc.Member.Role, cc.Company.ID, ip).with("deck.test.start", title, ResultSuccess, map[string]string{"metric": metric}))
		svc.setFlash(w, "success", "Test started. The baseline is the last 28 days; results appear after two weeks.")
		http.Redirect(w, r, "/command/tests", http.StatusSeeOther)

	case "end-test":
		id, _ := strconv.ParseInt(r.PostFormValue("id"), 10, 64)
		ended, err := svc.repo.EndIntervention(ctx, cc.Company.ID, id, now)
		if err != nil {
			svc.internalError(w, r, err)
			return
		}
		if ended {
			svc.audit(ctx, userActor(cc.UserID, cc.Member.Role, cc.Company.ID, ip).with("deck.test.end", strconv.FormatInt(id, 10), ResultSuccess, nil))
			svc.setFlash(w, "success", "Test ended. Its result is frozen at today’s measurement.")
		}
		http.Redirect(w, r, "/command/tests", http.StatusSeeOther)

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

// deckDayRows lists the records behind one day of the graph (or the most
// recent activity when no day is selected)
func (svc *Service) deckDayRows(c *Company, events []ActivityEvent, day time.Time, lk WorkspaceLookup) []deckActivityRow {
	var rows []deckActivityRow
	deleted := map[uint64]bool{}
	for _, e := range events {
		if e.Kind == ActivityDeleted {
			deleted[e.RecordID] = true
		}
	}

	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		if !day.IsZero() && !dayOf(e.OccurredAt).Equal(day) {
			continue
		}

		row := deckActivityRow{At: e.OccurredAt, Module: orStr(moduleLabels[e.Module], e.Module), Title: orStr(e.Title, "(untitled)")}
		switch e.Kind {
		case ActivityCreated:
			row.What = "Created"
			if e.ToStatus != "" {
				row.What += " · " + e.ToStatus
			}
		case ActivityStatus:
			row.What = fmt.Sprintf("%s → %s", orStr(e.FromStatus, "—"), orStr(e.ToStatus, "—"))
		case ActivityDeleted:
			row.What, row.Title = "Deleted", "(deleted record)"
		default:
			row.What = "Updated"
		}

		if page := lk.RecordPages[e.Module]; page > 0 && !deleted[e.RecordID] {
			row.Link = fmt.Sprintf("/compose/ns/%s/pages/%d/record/%d", c.Slug, page, e.RecordID)
		}

		rows = append(rows, row)
		if len(rows) >= 200 || (day.IsZero() && len(rows) >= 25) {
			break
		}
	}

	sort.SliceStable(rows, func(i, j int) bool { return rows[i].At.After(rows[j].At) })
	return rows
}
