package saas

import (
	"fmt"
	"net/http"
	"sort"
	"time"
)

// deckTabs are the Command Deck views
var deckTabs = []struct{ Key, Label, Path string }{
	{"deck", "Command Deck", "/command"},
	{"activity", "Activity Graph", "/command/activity"},
	{"pipeline", "Pipeline & Bottlenecks", "/command/pipeline"},
	{"goals", "Goal Intelligence", "/command/goals"},
}

// deckLookback covers the 12-month graph plus a 30-day comparison baseline
const deckLookback = 400 * 24 * time.Hour

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

func (svc *Service) commandDeck(tab string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cc, ok := svc.customer(w, r)
		if !ok {
			return
		}

		if cc.Decision.Level != AccessFull {
			http.Redirect(w, r, "/billing", http.StatusSeeOther)
			return
		}

		if !cc.Member.Role.CanUseCommandDeck() {
			svc.renderError(w, r, http.StatusForbidden)
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

		deck := BuildDeck(events, now, lk.Departments)

		d := svc.customerPage(cc, "Command Deck", "command")
		d["Deck"] = deck
		d["Tab"] = tab
		d["Tabs"] = deckTabs
		d["MainClass"] = "deck-main"

		if tab == "activity" {
			day, _ := time.Parse("2006-01-02", r.URL.Query().Get("day"))
			d["Day"] = day
			d["DayRows"] = svc.deckDayRows(cc.Company, events, day, now, lk)
		}

		w.Header().Set("Cache-Control", "no-store")
		svc.render(w, r, http.StatusOK, "command-deck", d)
	}
}

// deckDayRows lists the records behind one day of the graph (or the most
// recent activity when no day is selected)
func (svc *Service) deckDayRows(c *Company, events []ActivityEvent, day, now time.Time, lk WorkspaceLookup) []deckActivityRow {
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
		if len(rows) >= 200 {
			break
		}
		if day.IsZero() && len(rows) >= 25 {
			break
		}
	}

	sort.SliceStable(rows, func(i, j int) bool { return rows[i].At.After(rows[j].At) })
	return rows
}
