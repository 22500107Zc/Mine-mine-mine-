package saas

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// Founder execution intelligence: platform usage and per-company adoption.
// The Founder is the only platform-wide identity; company users never reach
// these queries.

type PlatformDay struct {
	Day       time.Time
	Events    int
	Companies int
}

// CompanyUsage is one company's activity over a window
type CompanyUsage struct {
	CompanyID   uint64
	Name        string
	Status      SubscriptionStatus
	Events      int
	ActiveUsers int
	LastEvent   *time.Time
	DeckViews   int
	DeckUsers   int
	OpenIssues  int
	Series      []float64
}

// PlatformActivity returns platform-wide events and active companies per day
func (r *Repo) PlatformActivity(ctx context.Context, since time.Time) ([]PlatformDay, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT date_trunc('day', occurred_at AT TIME ZONE 'UTC'), COUNT(*), COUNT(DISTINCT company_id)
		FROM saas_activity_events WHERE occurred_at >= $1 GROUP BY 1 ORDER BY 1`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PlatformDay
	for rows.Next() {
		var d PlatformDay
		if err = rows.Scan(&d.Day, &d.Events, &d.Companies); err != nil {
			return nil, err
		}
		d.Day = dayOf(d.Day)
		out = append(out, d)
	}
	return out, rows.Err()
}

// CompanyUsageSince aggregates activity per company since a moment
func (r *Repo) CompanyUsageSince(ctx context.Context, since time.Time) (map[uint64]*CompanyUsage, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT company_id, COUNT(*), COUNT(DISTINCT NULLIF(actor_id, 0)), MAX(occurred_at)
		FROM saas_activity_events WHERE occurred_at >= $1 GROUP BY company_id`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uint64]*CompanyUsage{}
	for rows.Next() {
		var (
			u    = &CompanyUsage{}
			last sql.NullTime
		)
		if err = rows.Scan(&u.CompanyID, &u.Events, &u.ActiveUsers, &last); err != nil {
			return nil, err
		}
		u.LastEvent = nullTime(last)
		out[u.CompanyID] = u
	}
	return out, rows.Err()
}

// CompanyDailyEvents counts one company's events per day
func (r *Repo) CompanyDailyEvents(ctx context.Context, companyID uint64, since time.Time) (map[time.Time]int, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT date_trunc('day', occurred_at AT TIME ZONE 'UTC'), COUNT(*) FROM saas_activity_events
		WHERE company_id = $1 AND occurred_at >= $2 GROUP BY 1`, companyID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[time.Time]int{}
	for rows.Next() {
		var (
			d time.Time
			n int
		)
		if err = rows.Scan(&d, &n); err != nil {
			return nil, err
		}
		out[dayOf(d)] = n
	}
	return out, rows.Err()
}

// ModuleCount is created/changed records of one kind
type ModuleCount struct {
	Module     string
	Created30  int
	CreatedAll int
	Changes30  int
}

// CompanyModuleCounts counts records created and changed per module
func (r *Repo) CompanyModuleCounts(ctx context.Context, companyID uint64, since time.Time) ([]ModuleCount, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT module,
			COUNT(*) FILTER (WHERE kind = 'created' AND occurred_at >= $2),
			COUNT(*) FILTER (WHERE kind = 'created'),
			COUNT(*) FILTER (WHERE occurred_at >= $2)
		FROM saas_activity_events WHERE company_id = $1 GROUP BY module ORDER BY 3 DESC`, companyID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ModuleCount
	for rows.Next() {
		var m ModuleCount
		if err = rows.Scan(&m.Module, &m.Created30, &m.CreatedAll, &m.Changes30); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// OpenIssueCounts counts open or reopened reports per company
func (r *Repo) OpenIssueCounts(ctx context.Context) (map[uint64]int, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT company_id, COUNT(*) FROM saas_issue_reports WHERE status IN ('open', 'in_review', 'reopened') GROUP BY company_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uint64]int{}
	for rows.Next() {
		var (
			id uint64
			n  int
		)
		if err = rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------

type platformIntel struct {
	Active7, Active30   int
	Events30, EventsP30 int
	DeckViews30         int
	DeckCompanies30     int
	OpenIssues          int
	Companies           []*CompanyUsage
	EventSeries         []float64
	CompanySeries       []float64
	From                time.Time
}

func (svc *Service) platformIntel(ctx context.Context) platformIntel {
	now := svc.now().UTC()
	var p platformIntel
	p.From = dayOf(now).AddDate(0, 0, -89)
	days, _ := svc.repo.PlatformActivity(ctx, p.From)
	byDay := map[time.Time]PlatformDay{}
	for _, d := range days {
		byDay[d.Day] = d
	}
	for d := p.From; !d.After(dayOf(now)); d = d.AddDate(0, 0, 1) {
		p.EventSeries = append(p.EventSeries, float64(byDay[d].Events))
		p.CompanySeries = append(p.CompanySeries, float64(byDay[d].Companies))
	}

	u7, _ := svc.repo.CompanyUsageSince(ctx, now.AddDate(0, 0, -7))
	u30, _ := svc.repo.CompanyUsageSince(ctx, now.AddDate(0, 0, -30))
	p.Active7, p.Active30 = len(u7), len(u30)
	for _, u := range u30 {
		p.Events30 += u.Events
	}
	prev, _ := svc.repo.CompanyUsageSince(ctx, now.AddDate(0, 0, -60))
	for id, u := range prev {
		p.EventsP30 += u.Events
		if c := u30[id]; c != nil {
			p.EventsP30 -= c.Events
		}
	}

	deck, _ := svc.repo.DeckUsageByCompany(ctx, now.AddDate(0, 0, -30))
	for _, d := range deck {
		p.DeckViews30 += d.Views
		p.DeckCompanies30++
	}
	issues, _ := svc.repo.OpenIssueCounts(ctx)
	for _, n := range issues {
		p.OpenIssues += n
	}

	cc, _ := svc.repo.SearchCompanies(ctx, CompanyFilter{Limit: 500})
	for _, c := range cc {
		u := u30[c.ID]
		if u == nil {
			u = &CompanyUsage{CompanyID: c.ID}
		}
		u.Name, u.Status = c.Name, c.SubscriptionStatus
		if d, ok := deck[c.ID]; ok {
			u.DeckViews, u.DeckUsers = d.Views, d.Users
		}
		u.OpenIssues = issues[c.ID]
		p.Companies = append(p.Companies, u)
	}
	sort.SliceStable(p.Companies, func(i, j int) bool { return p.Companies[i].Events > p.Companies[j].Events })
	if len(p.Companies) > 25 {
		p.Companies = p.Companies[:25]
	}
	return p
}

type founderCompanyIntel struct {
	Heat        ActivityHistory
	M           Measures
	Modules     []ModuleCount
	DeckDays    []DeckUsageDay
	DeckViews   int
	DeckUsers   int
	SignIns     []signIn
	Issues      []*IssueReport
	Day         time.Time
	DayCounts   []ModuleCount
	HasActivity bool
}

type signIn struct {
	Name, Email string
	At          time.Time
}

// founderCompanyIntel summarizes how one company uses St.Cloud~OS (counts only;
// the Founder does not browse the company's record contents here)
func (svc *Service) founderCompanyIntel(r *http.Request, c *Company, members []*Member) founderCompanyIntel {
	ctx := r.Context()
	now := svc.now().UTC()
	var fi founderCompanyIntel

	in, err := svc.loadIntel(ctx, c, url.Values{"range": {"30d"}})
	if err == nil {
		fi.M = in.Measure(in.W)
		fi.HasActivity = len(in.Events) > 0
		fi.Heat = in.ActivityHistory("year", "all", now, nil, fmt.Sprintf("/founder/companies/%d", c.ID), nil)
		// the Founder's graph links to a per-day count summary
		for i := range fi.Heat.Weeks {
			for k := range fi.Heat.Weeks[i] {
				d := fi.Heat.Weeks[i][k]
				fi.Heat.Weeks[i][k].Link = fmt.Sprintf("/founder/companies/%d?date=%s#activity", c.ID, d.Date.Format("2006-01-02"))
			}
		}
	}

	fi.Modules, _ = svc.repo.CompanyModuleCounts(ctx, c.ID, now.AddDate(0, 0, -30))
	fi.DeckDays, _ = svc.repo.DeckUsage(ctx, c.ID, now.AddDate(0, 0, -30))
	for _, d := range fi.DeckDays {
		fi.DeckViews += d.Views
		if d.Users > fi.DeckUsers {
			fi.DeckUsers = d.Users
		}
	}
	fi.Issues, _ = svc.repo.IssueReports(ctx, c.ID, 20)

	var ids []uint64
	byID := map[uint64]*Member{}
	for _, m := range members {
		ids = append(ids, m.UserID)
		byID[m.UserID] = m
	}
	if last, err := svc.platform.LastSignIns(ctx, ids...); err == nil {
		for id, at := range last {
			if at.IsZero() {
				continue
			}
			fi.SignIns = append(fi.SignIns, signIn{Name: byID[id].Name, Email: byID[id].Email, At: at})
		}
		sort.Slice(fi.SignIns, func(i, j int) bool { return fi.SignIns[i].At.After(fi.SignIns[j].At) })
	}

	if d, err := time.Parse("2006-01-02", r.URL.Query().Get("date")); err == nil {
		fi.Day = d
		counts := map[string]*ModuleCount{}
		var order []string
		if in != nil {
			for _, e := range in.Events {
				if dayOf(e.OccurredAt).Equal(d) {
					mc := counts[e.Module]
					if mc == nil {
						mc = &ModuleCount{Module: e.Module}
						counts[e.Module] = mc
						order = append(order, e.Module)
					}
					mc.Changes30++
					if e.Kind == ActivityCreated {
						mc.Created30++
					}
				}
			}
		}
		for _, k := range order {
			fi.DayCounts = append(fi.DayCounts, *counts[k])
		}
	}
	return fi
}

// founderIssueUpdate moves a report through open → in review → resolved
// (→ reopened) with an optional resolution note
func (svc *Service) founderIssueUpdate(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "issueID"), 10, 64)
	status := chi.URLParam(r, "status")
	if !validIssueStatus(status) {
		svc.renderError(w, r, http.StatusNotFound)
		return
	}
	if _, err := svc.repo.IssueByID(r.Context(), id); err != nil {
		svc.renderError(w, r, http.StatusNotFound)
		return
	}
	_ = r.ParseForm()
	note := strings.TrimSpace(r.PostFormValue("note"))
	if len(note) > 2000 {
		note = note[:2000]
	}
	var err error
	if r.PostForm.Has("note") {
		err = svc.repo.SetIssueStatus(r.Context(), id, status, note)
	} else {
		err = svc.repo.SetIssueStatus(r.Context(), id, status)
	}
	if err != nil {
		svc.internalError(w, r, err)
		return
	}

	fa := founderFrom(r)
	svc.audit(r.Context(), founderActor(fa.founder, clientIP(r)).with("founder.issue."+status, strconv.FormatInt(id, 10), ResultSuccess, nil))
	back := "/founder/issues"
	if f := r.URL.Query().Get("filter"); validIssueStatus(f) {
		back += "?status=" + f
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}
