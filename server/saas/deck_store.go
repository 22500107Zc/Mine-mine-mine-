package saas

import (
	"context"
	"database/sql"
	"time"
)

type (
	Intervention struct {
		ID        int64
		CompanyID uint64
		Title     string
		Module    string
		Metric    string
		Baseline  float64
		BaselineN int
		StartedAt time.Time
		EndedAt   *time.Time
		CreatedBy uint64
	}

	IssueReport struct {
		ID          int64
		CompanyID   uint64
		UserID      uint64
		Category    string
		Summary     string
		Details     string
		Page        string
		Status      string
		CreatedAt   time.Time
		CompanyName string
		UserEmail   string
	}
)

func (r *Repo) DeckTargets(ctx context.Context, companyID uint64) (map[string]float64, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT module, cycle_hours FROM saas_deck_targets WHERE company_id = $1`, companyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]float64{}
	for rows.Next() {
		var (
			m string
			h float64
		)
		if err = rows.Scan(&m, &h); err != nil {
			return nil, err
		}
		out[m] = h
	}
	return out, rows.Err()
}

// SetDeckTarget stores a target; zero hours removes it
func (r *Repo) SetDeckTarget(ctx context.Context, companyID uint64, module string, hours float64, by uint64) error {
	if hours <= 0 {
		_, err := r.db.ExecContext(ctx, `DELETE FROM saas_deck_targets WHERE company_id = $1 AND module = $2`, companyID, module)
		return err
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO saas_deck_targets (company_id, module, cycle_hours, updated_by, updated_at)
		VALUES ($1, $2, $3, $4, NOW())
		ON CONFLICT (company_id, module) DO UPDATE SET cycle_hours = $3, updated_by = $4, updated_at = NOW()`,
		companyID, module, hours, by)
	return err
}

func (r *Repo) Interventions(ctx context.Context, companyID uint64) ([]*Intervention, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, company_id, title, module, metric, baseline, baseline_n, started_at, ended_at, created_by
		FROM saas_deck_interventions WHERE company_id = $1 ORDER BY started_at DESC, id DESC LIMIT 100`, companyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Intervention
	for rows.Next() {
		var (
			iv    = &Intervention{}
			ended sql.NullTime
		)
		if err = rows.Scan(&iv.ID, &iv.CompanyID, &iv.Title, &iv.Module, &iv.Metric, &iv.Baseline, &iv.BaselineN, &iv.StartedAt, &ended, &iv.CreatedBy); err != nil {
			return nil, err
		}
		iv.EndedAt = nullTime(ended)
		out = append(out, iv)
	}
	return out, rows.Err()
}

func (r *Repo) CreateIntervention(ctx context.Context, iv *Intervention) error {
	return r.db.QueryRowContext(ctx, `INSERT INTO saas_deck_interventions (company_id, title, module, metric, baseline, baseline_n, started_at, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id`,
		iv.CompanyID, iv.Title, iv.Module, iv.Metric, iv.Baseline, iv.BaselineN, iv.StartedAt, iv.CreatedBy).Scan(&iv.ID)
}

// EndIntervention closes a running test of the given company only
func (r *Repo) EndIntervention(ctx context.Context, companyID uint64, id int64, at time.Time) (bool, error) {
	res, err := r.db.ExecContext(ctx, `UPDATE saas_deck_interventions SET ended_at = $3 WHERE company_id = $1 AND id = $2 AND ended_at IS NULL`, companyID, id, at)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (r *Repo) CreateIssueReport(ctx context.Context, ir *IssueReport) error {
	return r.db.QueryRowContext(ctx, `INSERT INTO saas_issue_reports (company_id, user_id, category, summary, details, page)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id, created_at`,
		ir.CompanyID, ir.UserID, ir.Category, ir.Summary, ir.Details, ir.Page).Scan(&ir.ID, &ir.CreatedAt)
}

func (r *Repo) IssueReports(ctx context.Context, companyID uint64, limit int) ([]*IssueReport, error) {
	q := `SELECT i.id, i.company_id, i.user_id, i.category, i.summary, i.details, i.page, i.status, i.created_at, COALESCE(c.name, '')
		FROM saas_issue_reports i LEFT JOIN saas_companies c ON c.id = i.company_id`
	args := []any{limit}
	if companyID > 0 {
		q += ` WHERE i.company_id = $2`
		args = append(args, companyID)
	}
	rows, err := r.db.QueryContext(ctx, q+` ORDER BY i.created_at DESC, i.id DESC LIMIT $1`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*IssueReport
	for rows.Next() {
		ir := &IssueReport{}
		if err = rows.Scan(&ir.ID, &ir.CompanyID, &ir.UserID, &ir.Category, &ir.Summary, &ir.Details, &ir.Page, &ir.Status, &ir.CreatedAt, &ir.CompanyName); err != nil {
			return nil, err
		}
		out = append(out, ir)
	}
	return out, rows.Err()
}

func (r *Repo) SetIssueStatus(ctx context.Context, id int64, status string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE saas_issue_reports SET status = $2 WHERE id = $1`, id, status)
	return err
}
