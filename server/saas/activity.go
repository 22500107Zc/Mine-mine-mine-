package saas

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// Activity kinds recorded for workspace records
const (
	ActivityCreated = "created"
	ActivityStatus  = "status"
	ActivityUpdated = "updated"
	ActivityDeleted = "deleted"
)

// ActivityEvent is one change to a record in a company workspace. The
// Command Deck is computed from these events only.
type ActivityEvent struct {
	ID           int64
	CompanyID    uint64
	OccurredAt   time.Time
	Module       string
	RecordID     uint64
	Title        string
	Kind         string
	FromStatus   string
	ToStatus     string
	ActorID      uint64
	AssigneeID   uint64
	DepartmentID uint64
	DueAt        *time.Time
	Source       string
}

// namespace → company cache for the activity hook (records are saved often)
type nsCache struct {
	sync.RWMutex
	m map[uint64]uint64
}

// RecordActivity stores a workspace record change. It is called for every
// record save, so it never fails the save: problems are only logged.
func (svc *Service) RecordActivity(ctx context.Context, namespaceID uint64, ev ActivityEvent) {
	companyID, ok := svc.companyForNamespace(ctx, namespaceID)
	if !ok {
		return
	}

	ev.CompanyID = companyID
	if ev.OccurredAt.IsZero() {
		ev.OccurredAt = svc.now()
	}
	if ev.Source == "" {
		ev.Source = "live"
	}
	if len(ev.Title) > 200 {
		ev.Title = ev.Title[:200]
	}

	if err := svc.repo.InsertActivity(ctx, &ev); err != nil {
		svc.log.Warn("activity not recorded", zap.Error(err))
		return
	}

	// deleted records keep their history for metrics, but not their content
	if ev.Kind == ActivityDeleted {
		_ = svc.repo.ScrubActivityTitles(ctx, companyID, ev.RecordID)
	}
}

func (svc *Service) companyForNamespace(ctx context.Context, nsID uint64) (uint64, bool) {
	if nsID == 0 {
		return 0, false
	}

	svc.nsCompanies.RLock()
	id, ok := svc.nsCompanies.m[nsID]
	svc.nsCompanies.RUnlock()
	if ok {
		return id, id > 0
	}

	c, err := svc.repo.CompanyByNamespace(ctx, nsID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return 0, false
	}

	if c != nil {
		id = c.ID
	}

	svc.nsCompanies.Lock()
	if svc.nsCompanies.m == nil {
		svc.nsCompanies.m = map[uint64]uint64{}
	}
	svc.nsCompanies.m[nsID] = id
	svc.nsCompanies.Unlock()
	return id, id > 0
}

// ensureActivityBaseline seeds the activity log from the current workspace
// once, so companies that existed before the Command Deck see their data
func (svc *Service) ensureActivityBaseline(ctx context.Context, c *Company) {
	if c.ActivityBackfilledAt != nil || c.NamespaceID == 0 {
		return
	}

	ee, err := svc.platform.WorkspaceSnapshot(ctx, c)
	if err != nil {
		svc.log.Warn("activity baseline failed", zap.Uint64("companyID", c.ID), zap.Error(err))
		return
	}

	known, err := svc.repo.ActivityRecordIDs(ctx, c.ID)
	if err != nil {
		return
	}

	for _, e := range ee {
		if known[e.RecordID] {
			continue
		}
		e.CompanyID = c.ID
		e.Source = "baseline"
		_ = svc.repo.InsertActivity(ctx, &e)
	}

	now := svc.now()
	_ = svc.repo.MarkActivityBackfilled(ctx, c.ID, now)
	c.ActivityBackfilledAt = &now
}

// Repository ------------------------------------------------------------

func (r *Repo) InsertActivity(ctx context.Context, e *ActivityEvent) error {
	return r.db.QueryRowContext(ctx, `INSERT INTO saas_activity_events
		(company_id, occurred_at, module, record_id, title, kind, from_status, to_status, actor_id, assignee_id, department_id, due_at, source)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13) RETURNING id`,
		e.CompanyID, e.OccurredAt, e.Module, e.RecordID, e.Title, e.Kind, e.FromStatus, e.ToStatus,
		e.ActorID, e.AssigneeID, e.DepartmentID, e.DueAt, orStr(e.Source, "live"),
	).Scan(&e.ID)
}

// Activity returns a company's events since the given time, oldest first
func (r *Repo) Activity(ctx context.Context, companyID uint64, since time.Time) ([]ActivityEvent, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, company_id, occurred_at, module, record_id, title, kind, from_status, to_status,
			actor_id, assignee_id, department_id, due_at, source
		FROM saas_activity_events WHERE company_id = $1 AND occurred_at >= $2 ORDER BY occurred_at, id`, companyID, since)
	if err != nil {
		return nil, err
	}

	defer rows.Close()
	var out []ActivityEvent
	for rows.Next() {
		var (
			e   ActivityEvent
			due sql.NullTime
		)
		if err = rows.Scan(&e.ID, &e.CompanyID, &e.OccurredAt, &e.Module, &e.RecordID, &e.Title, &e.Kind, &e.FromStatus, &e.ToStatus,
			&e.ActorID, &e.AssigneeID, &e.DepartmentID, &due, &e.Source); err != nil {
			return nil, err
		}
		e.DueAt = nullTime(due)
		out = append(out, e)
	}

	return out, rows.Err()
}

func (r *Repo) ActivityRecordIDs(ctx context.Context, companyID uint64) (map[uint64]bool, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT DISTINCT record_id FROM saas_activity_events WHERE company_id = $1`, companyID)
	if err != nil {
		return nil, err
	}

	defer rows.Close()
	out := map[uint64]bool{}
	for rows.Next() {
		var id uint64
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}

	return out, rows.Err()
}

func (r *Repo) ScrubActivityTitles(ctx context.Context, companyID, recordID uint64) error {
	_, err := r.db.ExecContext(ctx, `UPDATE saas_activity_events SET title = '' WHERE company_id = $1 AND record_id = $2`, companyID, recordID)
	return err
}

func (r *Repo) MarkActivityBackfilled(ctx context.Context, companyID uint64, at time.Time) error {
	_, err := r.db.ExecContext(ctx, `UPDATE saas_companies SET activity_backfilled_at = $2 WHERE id = $1`, companyID, at)
	return err
}

func orStr(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}
