package saas

import (
	"context"
	"database/sql"
	"time"
)

type (
	Intervention struct {
		ID           int64
		CompanyID    uint64
		Title        string
		Module       string
		Stage        string
		Metric       string
		Baseline     float64 // measured over the baseline window when the test started
		BaselineN    int
		BaselineDays int
		EvalDays     int
		OwnerID      uint64
		Notes        string
		StartedAt    time.Time
		EndedAt      *time.Time
		Result       *float64 // frozen when the test is ended
		ResultN      *int
		CreatedBy    uint64
	}

	Goal struct {
		ID        int64
		CompanyID uint64
		Title     string
		Metric    string
		Module    string
		Stage     string
		Target    float64
		Baseline  float64
		BaselineN int
		StartAt   time.Time
		TargetAt  *time.Time
		Status    string // active | achieved | closed
		CreatedBy uint64
		CreatedAt time.Time
		ClosedAt  *time.Time
	}

	IssueReport struct {
		ID             int64
		CompanyID      uint64
		UserID         uint64
		Category       string
		Summary        string
		Details        string
		Page           string
		Status         string // open | in_review | resolved | reopened
		ResolutionNote string
		CreatedAt      time.Time
		UpdatedAt      *time.Time
		CompanyName    string
		UserEmail      string
	}

	// DeckUsageDay is Command Deck use by a company on one day
	DeckUsageDay struct {
		Day   time.Time
		Views int
		Users int
	}

	// CompanyDeckUsage totals Command Deck use per company
	CompanyDeckUsage struct {
		CompanyID uint64
		Views     int
		Users     int
		LastDay   time.Time
	}
)

// issueStatuses are the review states of a reported issue
var issueStatuses = []string{"open", "in_review", "resolved", "reopened"}

func validIssueStatus(s string) bool {
	for _, v := range issueStatuses {
		if v == s {
			return true
		}
	}
	return false
}

// targetKey keys a stage target inside the targets map ("Module|Stage");
// workflow targets use the module alone
func targetKey(module, stage string) string {
	if stage == "" {
		return module
	}
	return module + "|" + stage
}

// DeckTargets returns the company's SLA targets in hours, keyed by
// workflow ("Task") and by workflow stage ("Task|In Progress")
func (r *Repo) DeckTargets(ctx context.Context, companyID uint64) (map[string]float64, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT module, stage, cycle_hours FROM saas_deck_targets WHERE company_id = $1`, companyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]float64{}
	for rows.Next() {
		var (
			m, st string
			h     float64
		)
		if err = rows.Scan(&m, &st, &h); err != nil {
			return nil, err
		}
		out[targetKey(m, st)] = h
	}
	return out, rows.Err()
}

// SetDeckTarget stores a workflow target; zero hours removes it
func (r *Repo) SetDeckTarget(ctx context.Context, companyID uint64, module string, hours float64, by uint64) error {
	return r.SetStageTarget(ctx, companyID, module, "", hours, by)
}

// SetStageTarget stores a target for one stage of a workflow ("" = the whole
// workflow); zero hours removes it
func (r *Repo) SetStageTarget(ctx context.Context, companyID uint64, module, stage string, hours float64, by uint64) error {
	if hours <= 0 {
		_, err := r.db.ExecContext(ctx, `DELETE FROM saas_deck_targets WHERE company_id = $1 AND module = $2 AND stage = $3`, companyID, module, stage)
		return err
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO saas_deck_targets (company_id, module, stage, cycle_hours, updated_by, updated_at)
		VALUES ($1, $2, $3, $4, $5, NOW())
		ON CONFLICT (company_id, module, stage) DO UPDATE SET cycle_hours = $4, updated_by = $5, updated_at = NOW()`,
		companyID, module, stage, hours, by)
	return err
}

// Interventions ---------------------------------------------------------

const interventionColumns = `id, company_id, title, module, stage, metric, baseline, baseline_n, baseline_days, eval_days,
	owner_id, notes, started_at, ended_at, result, result_n, created_by`

func scanIntervention(s scanner) (*Intervention, error) {
	var (
		iv      = &Intervention{}
		ended   sql.NullTime
		result  sql.NullFloat64
		resultN sql.NullInt64
	)
	if err := s.Scan(&iv.ID, &iv.CompanyID, &iv.Title, &iv.Module, &iv.Stage, &iv.Metric, &iv.Baseline, &iv.BaselineN, &iv.BaselineDays, &iv.EvalDays,
		&iv.OwnerID, &iv.Notes, &iv.StartedAt, &ended, &result, &resultN, &iv.CreatedBy); err != nil {
		return nil, err
	}
	iv.EndedAt = nullTime(ended)
	if result.Valid {
		v := result.Float64
		iv.Result = &v
	}
	if resultN.Valid {
		n := int(resultN.Int64)
		iv.ResultN = &n
	}
	return iv, nil
}

func (r *Repo) Interventions(ctx context.Context, companyID uint64) ([]*Intervention, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+interventionColumns+`
		FROM saas_deck_interventions WHERE company_id = $1 ORDER BY started_at DESC, id DESC LIMIT 200`, companyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Intervention
	for rows.Next() {
		iv, err := scanIntervention(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, iv)
	}
	return out, rows.Err()
}

// InterventionByID returns one test of the given company only
func (r *Repo) InterventionByID(ctx context.Context, companyID uint64, id int64) (*Intervention, error) {
	iv, err := scanIntervention(r.db.QueryRowContext(ctx, `SELECT `+interventionColumns+`
		FROM saas_deck_interventions WHERE company_id = $1 AND id = $2`, companyID, id))
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	return iv, err
}

func (r *Repo) CreateIntervention(ctx context.Context, iv *Intervention) error {
	if iv.BaselineDays <= 0 {
		iv.BaselineDays = 28
	}
	if iv.EvalDays <= 0 {
		iv.EvalDays = 28
	}
	return r.db.QueryRowContext(ctx, `INSERT INTO saas_deck_interventions
		(company_id, title, module, stage, metric, baseline, baseline_n, baseline_days, eval_days, owner_id, notes, started_at, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13) RETURNING id`,
		iv.CompanyID, iv.Title, iv.Module, iv.Stage, iv.Metric, iv.Baseline, iv.BaselineN, iv.BaselineDays, iv.EvalDays,
		iv.OwnerID, iv.Notes, iv.StartedAt, iv.CreatedBy).Scan(&iv.ID)
}

// EndIntervention closes a running test of the given company only, freezing
// the measured result
func (r *Repo) EndIntervention(ctx context.Context, companyID uint64, id int64, at time.Time, result ...float64) (bool, error) {
	var (
		res sql.Result
		err error
	)
	if len(result) == 2 {
		res, err = r.db.ExecContext(ctx, `UPDATE saas_deck_interventions SET ended_at = $3, result = $4, result_n = $5
			WHERE company_id = $1 AND id = $2 AND ended_at IS NULL`, companyID, id, at, result[0], int(result[1]))
	} else {
		res, err = r.db.ExecContext(ctx, `UPDATE saas_deck_interventions SET ended_at = $3 WHERE company_id = $1 AND id = $2 AND ended_at IS NULL`, companyID, id, at)
	}
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// SetInterventionNotes updates the notes of one test of the given company
func (r *Repo) SetInterventionNotes(ctx context.Context, companyID uint64, id int64, notes string) (bool, error) {
	res, err := r.db.ExecContext(ctx, `UPDATE saas_deck_interventions SET notes = $3 WHERE company_id = $1 AND id = $2`, companyID, id, notes)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// Goals -----------------------------------------------------------------

const goalColumns = `id, company_id, title, metric, module, stage, target, baseline, baseline_n, start_at, target_at, status, created_by, created_at, closed_at`

func scanGoal(s scanner) (*Goal, error) {
	var (
		g              = &Goal{}
		targetAt, clAt sql.NullTime
	)
	if err := s.Scan(&g.ID, &g.CompanyID, &g.Title, &g.Metric, &g.Module, &g.Stage, &g.Target, &g.Baseline, &g.BaselineN,
		&g.StartAt, &targetAt, &g.Status, &g.CreatedBy, &g.CreatedAt, &clAt); err != nil {
		return nil, err
	}
	g.TargetAt = nullTime(targetAt)
	g.ClosedAt = nullTime(clAt)
	return g, nil
}

func (r *Repo) Goals(ctx context.Context, companyID uint64) ([]*Goal, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+goalColumns+` FROM saas_goals WHERE company_id = $1 ORDER BY (status = 'active') DESC, created_at DESC, id DESC LIMIT 200`, companyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Goal
	for rows.Next() {
		g, err := scanGoal(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// GoalByID returns one goal of the given company only
func (r *Repo) GoalByID(ctx context.Context, companyID uint64, id int64) (*Goal, error) {
	g, err := scanGoal(r.db.QueryRowContext(ctx, `SELECT `+goalColumns+` FROM saas_goals WHERE company_id = $1 AND id = $2`, companyID, id))
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	return g, err
}

func (r *Repo) CreateGoal(ctx context.Context, g *Goal) error {
	if g.Status == "" {
		g.Status = "active"
	}
	return r.db.QueryRowContext(ctx, `INSERT INTO saas_goals (company_id, title, metric, module, stage, target, baseline, baseline_n, start_at, target_at, status, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12) RETURNING id, created_at`,
		g.CompanyID, g.Title, g.Metric, g.Module, g.Stage, g.Target, g.Baseline, g.BaselineN, g.StartAt, g.TargetAt, g.Status, g.CreatedBy).Scan(&g.ID, &g.CreatedAt)
}

// SetGoalStatus changes the status of one goal of the given company only
func (r *Repo) SetGoalStatus(ctx context.Context, companyID uint64, id int64, status string, at time.Time) (bool, error) {
	var closed any
	if status != "active" {
		closed = at
	}
	res, err := r.db.ExecContext(ctx, `UPDATE saas_goals SET status = $3, closed_at = $4 WHERE company_id = $1 AND id = $2`, companyID, id, status, closed)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// Issue reports ---------------------------------------------------------

func (r *Repo) CreateIssueReport(ctx context.Context, ir *IssueReport) error {
	return r.db.QueryRowContext(ctx, `INSERT INTO saas_issue_reports (company_id, user_id, category, summary, details, page)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id, created_at`,
		ir.CompanyID, ir.UserID, ir.Category, ir.Summary, ir.Details, ir.Page).Scan(&ir.ID, &ir.CreatedAt)
}

const issueColumns = `i.id, i.company_id, i.user_id, i.category, i.summary, i.details, i.page, i.status, i.resolution_note,
	i.created_at, i.updated_at, COALESCE(c.name, '')`

func scanIssue(s scanner) (*IssueReport, error) {
	var (
		ir  = &IssueReport{}
		upd sql.NullTime
	)
	if err := s.Scan(&ir.ID, &ir.CompanyID, &ir.UserID, &ir.Category, &ir.Summary, &ir.Details, &ir.Page, &ir.Status, &ir.ResolutionNote,
		&ir.CreatedAt, &upd, &ir.CompanyName); err != nil {
		return nil, err
	}
	ir.UpdatedAt = nullTime(upd)
	return ir, nil
}

// IssueReports lists reports of one company (companyID > 0) or of all
// companies (Founder only), optionally in one status
func (r *Repo) IssueReports(ctx context.Context, companyID uint64, limit int, status ...string) ([]*IssueReport, error) {
	q := `SELECT ` + issueColumns + ` FROM saas_issue_reports i LEFT JOIN saas_companies c ON c.id = i.company_id WHERE TRUE`
	args := []any{limit}
	if companyID > 0 {
		args = append(args, companyID)
		q += ` AND i.company_id = $2`
	}
	if len(status) == 1 && status[0] != "" {
		args = append(args, status[0])
		q += ` AND i.status = $` + itoa(len(args))
	}
	rows, err := r.db.QueryContext(ctx, q+` ORDER BY i.created_at DESC, i.id DESC LIMIT $1`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*IssueReport
	for rows.Next() {
		ir, err := scanIssue(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ir)
	}
	return out, rows.Err()
}

// IssueByID returns one report (Founder)
func (r *Repo) IssueByID(ctx context.Context, id int64) (*IssueReport, error) {
	ir, err := scanIssue(r.db.QueryRowContext(ctx, `SELECT `+issueColumns+` FROM saas_issue_reports i
		LEFT JOIN saas_companies c ON c.id = i.company_id WHERE i.id = $1`, id))
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	return ir, err
}

// IssueCounts counts reports per status (companyID 0 = all companies)
func (r *Repo) IssueCounts(ctx context.Context, companyID uint64) (map[string]int, error) {
	q := `SELECT status, COUNT(*) FROM saas_issue_reports`
	var args []any
	if companyID > 0 {
		q += ` WHERE company_id = $1`
		args = append(args, companyID)
	}
	rows, err := r.db.QueryContext(ctx, q+` GROUP BY status`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var (
			s string
			n int
		)
		if err = rows.Scan(&s, &n); err != nil {
			return nil, err
		}
		out[s] = n
	}
	return out, rows.Err()
}

// SetIssueStatus moves a report to a review state, with an optional note
func (r *Repo) SetIssueStatus(ctx context.Context, id int64, status string, note ...string) error {
	if len(note) == 1 {
		_, err := r.db.ExecContext(ctx, `UPDATE saas_issue_reports SET status = $2, resolution_note = $3, updated_at = NOW() WHERE id = $1`, id, status, note[0])
		return err
	}
	_, err := r.db.ExecContext(ctx, `UPDATE saas_issue_reports SET status = $2, updated_at = NOW() WHERE id = $1`, id, status)
	return err
}

// Command Deck usage ----------------------------------------------------

// RecordDeckView counts one Command Deck page view
func (r *Repo) RecordDeckView(ctx context.Context, companyID, userID uint64, at time.Time) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO saas_deck_usage (company_id, user_id, day, views) VALUES ($1, $2, $3, 1)
		ON CONFLICT (company_id, day, user_id) DO UPDATE SET views = saas_deck_usage.views + 1`, companyID, userID, dayOf(at))
	return err
}

// DeckUsage returns daily Command Deck use of one company since a day
func (r *Repo) DeckUsage(ctx context.Context, companyID uint64, since time.Time) ([]DeckUsageDay, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT day, SUM(views), COUNT(DISTINCT user_id) FROM saas_deck_usage
		WHERE company_id = $1 AND day >= $2 GROUP BY day ORDER BY day`, companyID, dayOf(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeckUsageDay
	for rows.Next() {
		var d DeckUsageDay
		if err = rows.Scan(&d.Day, &d.Views, &d.Users); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// DeckUsageByCompany totals Command Deck use per company since a day
func (r *Repo) DeckUsageByCompany(ctx context.Context, since time.Time) (map[uint64]CompanyDeckUsage, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT company_id, SUM(views), COUNT(DISTINCT user_id), MAX(day) FROM saas_deck_usage
		WHERE day >= $1 GROUP BY company_id`, dayOf(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uint64]CompanyDeckUsage{}
	for rows.Next() {
		var u CompanyDeckUsage
		if err = rows.Scan(&u.CompanyID, &u.Views, &u.Users, &u.LastDay); err != nil {
			return nil, err
		}
		out[u.CompanyID] = u
	}
	return out, rows.Err()
}
