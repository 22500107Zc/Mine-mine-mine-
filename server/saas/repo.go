package saas

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type (
	// Repo is the PostgreSQL persistence for the commercial layer.
	// All queries are parameterized.
	Repo struct {
		db *sql.DB
	}

	CompanyFilter struct {
		Query  string
		Status string
		Limit  int
		Offset int
	}

	Metrics struct {
		TotalCompanies          int
		ActiveCompanies         int
		ActiveSubscriptions     int
		PastDueSubscriptions    int
		CanceledSubscriptions   int
		UnpaidSubscriptions     int
		IncompleteSubscriptions int
		DisabledCompanies       int
		TotalUsers              int
		NewCompaniesThisMonth   int
		NewCompaniesThisWeek    int
		MRRCents                int64
	}
)

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
)

func NewRepo(db *sql.DB) *Repo {
	return &Repo{db: db}
}

func (r *Repo) DB() *sql.DB { return r.db }

func (r *Repo) Ping(ctx context.Context) error {
	return r.db.PingContext(ctx)
}

const companyColumns = `c.id, c.name, c.slug, c.status, c.owner_user_id, c.owner_email, c.owner_name,
	c.namespace_id, c.role_owner_id, c.role_admin_id, c.role_manager_id, c.role_employee_id,
	COALESCE(c.stripe_customer_id, ''), COALESCE(c.stripe_subscription_id, ''), c.stripe_price_id,
	c.subscription_status, c.billing_period_start, c.billing_period_end, c.cancel_at_period_end,
	c.canceled_at, c.provisioning_status, c.provisioning_error, c.provisioned_at, c.last_activity_at,
	c.payment_method_summary, c.profile_website, c.profile_phone, c.profile_address, c.profile_industry,
	c.onboarding_completed_at, c.created_at, c.updated_at,
	(SELECT COUNT(*) FROM saas_company_members m WHERE m.company_id = c.id)`

type scanner interface {
	Scan(dest ...any) error
}

func scanCompany(s scanner) (*Company, error) {
	var (
		c                                                        = &Company{}
		status, subStatus, provStatus                            string
		periodStart, periodEnd, canceledAt, provAt, actAt, onbAt sql.NullTime
	)

	err := s.Scan(
		&c.ID, &c.Name, &c.Slug, &status, &c.OwnerUserID, &c.OwnerEmail, &c.OwnerName,
		&c.NamespaceID, &c.RoleOwnerID, &c.RoleAdminID, &c.RoleManagerID, &c.RoleEmployeeID,
		&c.StripeCustomerID, &c.StripeSubscriptionID, &c.StripePriceID,
		&subStatus, &periodStart, &periodEnd, &c.CancelAtPeriodEnd,
		&canceledAt, &provStatus, &c.ProvisioningError, &provAt, &actAt,
		&c.PaymentMethodSummary, &c.Website, &c.Phone, &c.Address, &c.Industry,
		&onbAt, &c.CreatedAt, &c.UpdatedAt,
		&c.UserCount,
	)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}

	if err != nil {
		return nil, err
	}

	c.Status = CompanyStatus(status)
	c.SubscriptionStatus = SubscriptionStatus(subStatus)
	c.ProvisioningStatus = ProvisioningStatus(provStatus)
	c.BillingPeriodStart = nullTime(periodStart)
	c.BillingPeriodEnd = nullTime(periodEnd)
	c.CanceledAt = nullTime(canceledAt)
	c.ProvisionedAt = nullTime(provAt)
	c.LastActivityAt = nullTime(actAt)
	c.OnboardingDoneAt = nullTime(onbAt)
	return c, nil
}

func nullTime(t sql.NullTime) *time.Time {
	if !t.Valid {
		return nil
	}

	v := t.Time
	return &v
}

func nullString(s string) any {
	if s == "" {
		return nil
	}

	return s
}

// CreateCompany inserts a new (pending) company
func (r *Repo) CreateCompany(ctx context.Context, c *Company) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO saas_companies
		(id, name, slug, status, owner_user_id, owner_email, owner_name, stripe_price_id,
		 subscription_status, provisioning_status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $11)`,
		c.ID, c.Name, c.Slug, string(c.Status), c.OwnerUserID, c.OwnerEmail, c.OwnerName,
		c.StripePriceID, string(c.SubscriptionStatus), string(c.ProvisioningStatus), c.CreatedAt,
	)

	if err != nil && strings.Contains(err.Error(), "duplicate key") {
		return ErrConflict
	}

	return err
}

func (r *Repo) companyWhere(ctx context.Context, where string, args ...any) (*Company, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+companyColumns+` FROM saas_companies c WHERE `+where, args...)
	return scanCompany(row)
}

func (r *Repo) CompanyByID(ctx context.Context, id uint64) (*Company, error) {
	return r.companyWhere(ctx, "c.id = $1", id)
}

func (r *Repo) CompanyBySlug(ctx context.Context, slug string) (*Company, error) {
	return r.companyWhere(ctx, "c.slug = $1", slug)
}

func (r *Repo) CompanyByStripeCustomer(ctx context.Context, customerID string) (*Company, error) {
	return r.companyWhere(ctx, "c.stripe_customer_id = $1", customerID)
}

func (r *Repo) CompanyByStripeSubscription(ctx context.Context, subID string) (*Company, error) {
	return r.companyWhere(ctx, "c.stripe_subscription_id = $1", subID)
}

func (r *Repo) CompanyByNamespace(ctx context.Context, nsID uint64) (*Company, error) {
	return r.companyWhere(ctx, "c.namespace_id = $1", nsID)
}

// CompanyByUser returns the company the user belongs to
func (r *Repo) CompanyByUser(ctx context.Context, userID uint64) (*Company, *Member, error) {
	m, err := r.MemberByUser(ctx, userID)
	if err != nil {
		return nil, nil, err
	}

	c, err := r.CompanyByID(ctx, m.CompanyID)
	if err != nil {
		return nil, nil, err
	}

	return c, m, nil
}

// SearchCompanies lists companies newest first
func (r *Repo) SearchCompanies(ctx context.Context, f CompanyFilter) ([]*Company, error) {
	var (
		conds = []string{"TRUE"}
		args  []any
	)

	if q := strings.TrimSpace(f.Query); q != "" {
		args = append(args, "%"+strings.ToLower(q)+"%")
		n := len(args)
		conds = append(conds, fmt.Sprintf("(LOWER(c.name) LIKE $%d OR LOWER(c.owner_email) LIKE $%d OR LOWER(c.slug) LIKE $%d OR COALESCE(c.stripe_customer_id,'') LIKE $%d)", n, n, n, n))
	}

	switch f.Status {
	case "":
	case string(CompanyDisabled), string(CompanyActive), string(CompanyPending):
		args = append(args, f.Status)
		conds = append(conds, fmt.Sprintf("c.status = $%d", len(args)))
	default:
		args = append(args, f.Status)
		conds = append(conds, fmt.Sprintf("c.subscription_status = $%d", len(args)))
	}

	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}

	args = append(args, f.Limit, f.Offset)
	q := `SELECT ` + companyColumns + ` FROM saas_companies c WHERE ` + strings.Join(conds, " AND ") +
		fmt.Sprintf(" ORDER BY c.created_at DESC, c.id DESC LIMIT $%d OFFSET $%d", len(args)-1, len(args))

	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}

	defer rows.Close()

	out := make([]*Company, 0, f.Limit)
	for rows.Next() {
		c, err := scanCompany(rows)
		if err != nil {
			return nil, err
		}

		out = append(out, c)
	}

	return out, rows.Err()
}

// SetStripeCustomer associates a Stripe customer with a company
func (r *Repo) SetStripeCustomer(ctx context.Context, companyID uint64, customerID string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE saas_companies SET stripe_customer_id = $2, updated_at = NOW() WHERE id = $1`,
		companyID, nullString(customerID))
	return err
}

// SubscriptionUpdate carries subscription state from Stripe
type SubscriptionUpdate struct {
	CustomerID         string
	SubscriptionID     string
	PriceID            string
	Status             SubscriptionStatus
	PeriodStart        *time.Time
	PeriodEnd          *time.Time
	CancelAtPeriodEnd  bool
	CanceledAt         *time.Time
	PaymentMethodBrief string
}

// ApplySubscription persists subscription state
func (r *Repo) ApplySubscription(ctx context.Context, companyID uint64, u SubscriptionUpdate) error {
	_, err := r.db.ExecContext(ctx, `UPDATE saas_companies SET
			stripe_customer_id     = COALESCE($2, stripe_customer_id),
			stripe_subscription_id = COALESCE($3, stripe_subscription_id),
			stripe_price_id        = CASE WHEN $4 = '' THEN stripe_price_id ELSE $4 END,
			subscription_status    = $5,
			billing_period_start   = COALESCE($6, billing_period_start),
			billing_period_end     = COALESCE($7, billing_period_end),
			cancel_at_period_end   = $8,
			canceled_at            = $9,
			payment_method_summary = CASE WHEN $10 = '' THEN payment_method_summary ELSE $10 END,
			updated_at             = NOW()
		WHERE id = $1`,
		companyID, nullString(u.CustomerID), nullString(u.SubscriptionID), u.PriceID, string(u.Status),
		u.PeriodStart, u.PeriodEnd, u.CancelAtPeriodEnd, u.CanceledAt, u.PaymentMethodBrief,
	)

	return err
}

// SetSubscriptionStatus updates only the status
func (r *Repo) SetSubscriptionStatus(ctx context.Context, companyID uint64, st SubscriptionStatus) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE saas_companies SET subscription_status = $2, updated_at = NOW() WHERE id = $1`,
		companyID, string(st))
	return err
}

// ClaimProvisioning atomically moves a company into the provisioning state.
//
// Returns false when another worker (or a duplicate webhook) already claimed
// or completed provisioning — this is what makes provisioning idempotent.
func (r *Repo) ClaimProvisioning(ctx context.Context, companyID uint64) (bool, error) {
	res, err := r.db.ExecContext(ctx, `UPDATE saas_companies
		SET provisioning_status = $2, updated_at = NOW()
		WHERE id = $1 AND provisioning_status IN ($3, $4)`,
		companyID, string(ProvProvisioning), string(ProvAwaitingPayment), string(ProvFailed))
	if err != nil {
		return false, err
	}

	n, err := res.RowsAffected()
	return n == 1, err
}

// ProvisioningResult stores the result of provisioning
type ProvisioningResult struct {
	NamespaceID    uint64
	RoleOwnerID    uint64
	RoleAdminID    uint64
	RoleManagerID  uint64
	RoleEmployeeID uint64
}

func (r *Repo) CompleteProvisioning(ctx context.Context, companyID uint64, p ProvisioningResult) error {
	_, err := r.db.ExecContext(ctx, `UPDATE saas_companies SET
			namespace_id = $2, role_owner_id = $3, role_admin_id = $4, role_manager_id = $5, role_employee_id = $6,
			provisioning_status = $7, provisioning_error = '', provisioned_at = NOW(),
			status = CASE WHEN status = $8 THEN $9 ELSE status END,
			updated_at = NOW()
		WHERE id = $1`,
		companyID, p.NamespaceID, p.RoleOwnerID, p.RoleAdminID, p.RoleManagerID, p.RoleEmployeeID,
		string(ProvProvisioned), string(CompanyPending), string(CompanyActive))
	return err
}

func (r *Repo) FailProvisioning(ctx context.Context, companyID uint64, msg string) error {
	if len(msg) > 500 {
		msg = msg[:500]
	}

	_, err := r.db.ExecContext(ctx, `UPDATE saas_companies SET provisioning_status = $2, provisioning_error = $3, updated_at = NOW() WHERE id = $1`,
		companyID, string(ProvFailed), msg)
	return err
}

// SetCompanyStatus enables/disables a company (Founder operation)
func (r *Repo) SetCompanyStatus(ctx context.Context, companyID uint64, st CompanyStatus) error {
	res, err := r.db.ExecContext(ctx, `UPDATE saas_companies SET status = $2, updated_at = NOW() WHERE id = $1`, companyID, string(st))
	if err != nil {
		return err
	}

	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}

	return nil
}

// CompanyProfile holds editable company profile fields
type CompanyProfile struct {
	Name     string
	Website  string
	Phone    string
	Address  string
	Industry string
}

func (r *Repo) UpdateCompanyProfile(ctx context.Context, companyID uint64, p CompanyProfile) error {
	_, err := r.db.ExecContext(ctx, `UPDATE saas_companies SET name = $2, profile_website = $3, profile_phone = $4,
		profile_address = $5, profile_industry = $6, updated_at = NOW() WHERE id = $1`,
		companyID, p.Name, p.Website, p.Phone, p.Address, p.Industry)
	return err
}

func (r *Repo) CompleteOnboarding(ctx context.Context, companyID uint64, at time.Time) error {
	_, err := r.db.ExecContext(ctx, `UPDATE saas_companies SET onboarding_completed_at = COALESCE(onboarding_completed_at, $2), updated_at = NOW() WHERE id = $1`, companyID, at)
	return err
}

// TouchActivity records last activity (throttled by caller)
func (r *Repo) TouchActivity(ctx context.Context, companyID uint64, at time.Time) error {
	_, err := r.db.ExecContext(ctx, `UPDATE saas_companies SET last_activity_at = $2 WHERE id = $1`, companyID, at)
	return err
}

// Members ------------------------------------------------------------------

func (r *Repo) AddMember(ctx context.Context, m *Member) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO saas_company_members (company_id, user_id, role, invited_by, created_at)
		VALUES ($1, $2, $3, $4, $5)`, m.CompanyID, m.UserID, string(m.Role), m.InvitedBy, m.CreatedAt)
	if err != nil && strings.Contains(err.Error(), "duplicate key") {
		return ErrConflict
	}

	return err
}

func (r *Repo) UpdateMemberRole(ctx context.Context, companyID, userID uint64, role CompanyRole) error {
	res, err := r.db.ExecContext(ctx, `UPDATE saas_company_members SET role = $3 WHERE company_id = $1 AND user_id = $2`,
		companyID, userID, string(role))
	if err != nil {
		return err
	}

	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}

	return nil
}

func (r *Repo) RemoveMember(ctx context.Context, companyID, userID uint64) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM saas_company_members WHERE company_id = $1 AND user_id = $2`, companyID, userID)
	if err != nil {
		return err
	}

	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}

	return nil
}

func scanMember(s scanner) (*Member, error) {
	var (
		m    = &Member{}
		role string
	)

	err := s.Scan(&m.CompanyID, &m.UserID, &role, &m.InvitedBy, &m.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}

	m.Role = CompanyRole(role)
	return m, err
}

func (r *Repo) MemberByUser(ctx context.Context, userID uint64) (*Member, error) {
	return scanMember(r.db.QueryRowContext(ctx,
		`SELECT company_id, user_id, role, invited_by, created_at FROM saas_company_members WHERE user_id = $1`, userID))
}

func (r *Repo) Members(ctx context.Context, companyID uint64) ([]*Member, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT company_id, user_id, role, invited_by, created_at FROM saas_company_members WHERE company_id = $1 ORDER BY created_at, user_id`, companyID)
	if err != nil {
		return nil, err
	}

	defer rows.Close()
	var out []*Member
	for rows.Next() {
		m, err := scanMember(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}

	return out, rows.Err()
}

func (r *Repo) AllMembers(ctx context.Context, limit, offset int) ([]*Member, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}

	rows, err := r.db.QueryContext(ctx,
		`SELECT company_id, user_id, role, invited_by, created_at FROM saas_company_members ORDER BY created_at DESC LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, err
	}

	defer rows.Close()
	var out []*Member
	for rows.Next() {
		m, err := scanMember(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}

	return out, rows.Err()
}

// Metrics -----------------------------------------------------------------

func (r *Repo) Metrics(ctx context.Context, now time.Time, priceCents int64) (*Metrics, error) {
	var (
		m          = &Metrics{}
		monthStart = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
		weekStart  = now.AddDate(0, 0, -7)
	)

	err := r.db.QueryRowContext(ctx, `SELECT
			COUNT(*),
			COUNT(*) FILTER (WHERE status = 'active'),
			COUNT(*) FILTER (WHERE subscription_status = 'active'),
			COUNT(*) FILTER (WHERE subscription_status = 'past_due'),
			COUNT(*) FILTER (WHERE subscription_status = 'canceled'),
			COUNT(*) FILTER (WHERE subscription_status = 'unpaid'),
			COUNT(*) FILTER (WHERE subscription_status IN ('incomplete', 'incomplete_expired', '')),
			COUNT(*) FILTER (WHERE status = 'disabled'),
			COUNT(*) FILTER (WHERE created_at >= $1),
			COUNT(*) FILTER (WHERE created_at >= $2),
			COUNT(*) FILTER (WHERE subscription_status IN ('active', 'past_due'))
		FROM saas_companies`, monthStart, weekStart).Scan(
		&m.TotalCompanies, &m.ActiveCompanies, &m.ActiveSubscriptions, &m.PastDueSubscriptions,
		&m.CanceledSubscriptions, &m.UnpaidSubscriptions, &m.IncompleteSubscriptions, &m.DisabledCompanies,
		&m.NewCompaniesThisMonth, &m.NewCompaniesThisWeek, &m.MRRCents,
	)

	if err != nil {
		return nil, err
	}

	// MRR = number of billable subscriptions × plan price
	// (past_due subscriptions are still billable, Stripe retries the charge)
	m.MRRCents = m.MRRCents * priceCents

	if err = r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM saas_company_members`).Scan(&m.TotalUsers); err != nil {
		return nil, err
	}

	return m, nil
}

// Founders ----------------------------------------------------------------

func scanFounder(s scanner) (*Founder, error) {
	var (
		f            = &Founder{}
		locked, last sql.NullTime
	)

	err := s.Scan(&f.ID, &f.PasswordHash, &f.FailedAttempts, &locked, &last, &f.CreatedAt, &f.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}

	f.LockedUntil = nullTime(locked)
	f.LastLoginAt = nullTime(last)
	return f, err
}

const founderColumns = `id, password_hash, failed_attempts, locked_until, last_login_at, created_at, updated_at`

// Founder returns the platform Founder (there is at most one)
func (r *Repo) Founder(ctx context.Context) (*Founder, error) {
	return scanFounder(r.db.QueryRowContext(ctx, `SELECT `+founderColumns+` FROM saas_founders ORDER BY created_at LIMIT 1`))
}

func (r *Repo) FounderByID(ctx context.Context, id uint64) (*Founder, error) {
	return scanFounder(r.db.QueryRowContext(ctx, `SELECT `+founderColumns+` FROM saas_founders WHERE id = $1`, id))
}

func (r *Repo) CreateFounder(ctx context.Context, f *Founder) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO saas_founders (id, password_hash, created_at, updated_at) VALUES ($1, $2, NOW(), NOW())`,
		f.ID, f.PasswordHash)
	return err
}

func (r *Repo) SetFounderPassword(ctx context.Context, id uint64, hash string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE saas_founders SET password_hash = $2, failed_attempts = 0, locked_until = NULL, updated_at = NOW() WHERE id = $1`, id, hash)
	return err
}

func (r *Repo) FounderLoginFailed(ctx context.Context, id uint64, maxAttempts int, lockout time.Duration, now time.Time) error {
	_, err := r.db.ExecContext(ctx, `UPDATE saas_founders SET
			failed_attempts = failed_attempts + 1,
			locked_until = CASE WHEN failed_attempts + 1 >= $2 THEN $3::timestamptz ELSE locked_until END,
			updated_at = NOW()
		WHERE id = $1`, id, maxAttempts, now.Add(lockout))
	return err
}

func (r *Repo) FounderLoginSucceeded(ctx context.Context, id uint64, now time.Time) error {
	_, err := r.db.ExecContext(ctx, `UPDATE saas_founders SET failed_attempts = 0, locked_until = NULL, last_login_at = $2, updated_at = NOW() WHERE id = $1`, id, now)
	return err
}

func (r *Repo) CreateFounderSession(ctx context.Context, s *FounderSession) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO saas_founder_sessions (token_hash, founder_id, csrf_token, created_at, expires_at, last_seen_at, ip, user_agent)
		VALUES ($1, $2, $3, $4, $5, $4, $6, $7)`,
		s.TokenHash, s.FounderID, s.CSRFToken, s.CreatedAt, s.ExpiresAt, s.IP, s.UserAgent)
	return err
}

func (r *Repo) FounderSession(ctx context.Context, tokenHash string) (*FounderSession, error) {
	s := &FounderSession{}
	err := r.db.QueryRowContext(ctx, `SELECT token_hash, founder_id, csrf_token, created_at, expires_at, last_seen_at, ip, user_agent
		FROM saas_founder_sessions WHERE token_hash = $1`, tokenHash).Scan(
		&s.TokenHash, &s.FounderID, &s.CSRFToken, &s.CreatedAt, &s.ExpiresAt, &s.LastSeenAt, &s.IP, &s.UserAgent)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}

	return s, err
}

func (r *Repo) TouchFounderSession(ctx context.Context, tokenHash string, at time.Time) error {
	_, err := r.db.ExecContext(ctx, `UPDATE saas_founder_sessions SET last_seen_at = $2 WHERE token_hash = $1`, tokenHash, at)
	return err
}

func (r *Repo) DeleteFounderSession(ctx context.Context, tokenHash string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM saas_founder_sessions WHERE token_hash = $1`, tokenHash)
	return err
}

func (r *Repo) DeleteFounderSessions(ctx context.Context, founderID uint64) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM saas_founder_sessions WHERE founder_id = $1`, founderID)
	return err
}

func (r *Repo) DeleteExpiredFounderSessions(ctx context.Context, now time.Time) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM saas_founder_sessions WHERE expires_at < $1`, now)
	return err
}

// Stripe events & payments -----------------------------------------------

// RecordStripeEvent inserts an event; returns false when the event was already
// processed (duplicate delivery). Events stuck in "failed" may be retried.
func (r *Repo) RecordStripeEvent(ctx context.Context, id, typ string) (bool, error) {
	res, err := r.db.ExecContext(ctx, `INSERT INTO saas_stripe_events (id, type, status, received_at)
		VALUES ($1, $2, 'received', NOW())
		ON CONFLICT (id) DO UPDATE SET status = 'received', error = '', received_at = NOW()
		WHERE saas_stripe_events.status = 'failed'`, id, typ)
	if err != nil {
		return false, err
	}

	n, err := res.RowsAffected()
	return n == 1, err
}

func (r *Repo) FinishStripeEvent(ctx context.Context, id string, companyID uint64, procErr error) error {
	var (
		status = "processed"
		msg    string
	)

	if procErr != nil {
		status = "failed"
		msg = procErr.Error()
		if len(msg) > 500 {
			msg = msg[:500]
		}
	}

	_, err := r.db.ExecContext(ctx, `UPDATE saas_stripe_events SET status = $2, error = $3, company_id = $4, processed_at = NOW() WHERE id = $1`,
		id, status, msg, companyID)
	return err
}

func (r *Repo) RecentStripeEvents(ctx context.Context, companyID uint64, limit int) ([]*StripeEventRecord, error) {
	q := `SELECT id, type, status, error, company_id, received_at, processed_at FROM saas_stripe_events`
	args := []any{limit}
	if companyID > 0 {
		q += ` WHERE company_id = $2`
		args = append(args, companyID)
	}

	rows, err := r.db.QueryContext(ctx, q+` ORDER BY received_at DESC LIMIT $1`, args...)
	if err != nil {
		return nil, err
	}

	defer rows.Close()
	var out []*StripeEventRecord
	for rows.Next() {
		var (
			e  = &StripeEventRecord{}
			pa sql.NullTime
		)
		if err = rows.Scan(&e.ID, &e.Type, &e.Status, &e.Error, &e.CompanyID, &e.ReceivedAt, &pa); err != nil {
			return nil, err
		}
		e.ProcessedAt = nullTime(pa)
		out = append(out, e)
	}

	return out, rows.Err()
}

// LastStripeEventAt returns when the last webhook was received and processed
func (r *Repo) LastStripeEventAt(ctx context.Context) (last *time.Time, failed int, err error) {
	var t sql.NullTime
	err = r.db.QueryRowContext(ctx, `SELECT MAX(received_at), COUNT(*) FILTER (WHERE status = 'failed') FROM saas_stripe_events`).Scan(&t, &failed)
	return nullTime(t), failed, err
}

func (r *Repo) UpsertPayment(ctx context.Context, p *Payment) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO saas_payments (id, company_id, amount_cents, currency, status, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (id) DO UPDATE SET status = EXCLUDED.status, amount_cents = EXCLUDED.amount_cents, company_id = EXCLUDED.company_id`,
		p.ID, p.CompanyID, p.AmountCents, p.Currency, p.Status, p.CreatedAt)
	return err
}

func (r *Repo) Payments(ctx context.Context, companyID uint64, status string, limit int) ([]*Payment, error) {
	var (
		conds = []string{"TRUE"}
		args  = []any{limit}
	)

	if companyID > 0 {
		args = append(args, companyID)
		conds = append(conds, fmt.Sprintf("p.company_id = $%d", len(args)))
	}

	if status != "" {
		args = append(args, status)
		conds = append(conds, fmt.Sprintf("p.status = $%d", len(args)))
	}

	rows, err := r.db.QueryContext(ctx, `SELECT p.id, p.company_id, COALESCE(c.name, ''), p.amount_cents, p.currency, p.status, p.created_at
		FROM saas_payments p LEFT JOIN saas_companies c ON c.id = p.company_id
		WHERE `+strings.Join(conds, " AND ")+` ORDER BY p.created_at DESC LIMIT $1`, args...)
	if err != nil {
		return nil, err
	}

	defer rows.Close()
	var out []*Payment
	for rows.Next() {
		p := &Payment{}
		if err = rows.Scan(&p.ID, &p.CompanyID, &p.CompanyName, &p.AmountCents, &p.Currency, &p.Status, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}

	return out, rows.Err()
}

// Audit -------------------------------------------------------------------

func (r *Repo) InsertAudit(ctx context.Context, e *AuditEntry) error {
	meta, err := json.Marshal(e.Metadata)
	if err != nil || e.Metadata == nil {
		meta = []byte("{}")
	}

	_, err = r.db.ExecContext(ctx, `INSERT INTO saas_audit_log (occurred_at, actor_type, actor_id, actor_label, role, company_id, action, target, result, ip, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		e.OccurredAt, e.ActorType, e.ActorID, e.ActorLabel, e.Role, e.CompanyID, e.Action, e.Target, e.Result, e.IP, string(meta))
	return err
}

func (r *Repo) Audit(ctx context.Context, companyID uint64, actionPrefix string, limit, offset int) ([]*AuditEntry, error) {
	var (
		conds = []string{"TRUE"}
		args  = []any{limit, offset}
	)

	if companyID > 0 {
		args = append(args, companyID)
		conds = append(conds, fmt.Sprintf("company_id = $%d", len(args)))
	}

	if actionPrefix != "" {
		args = append(args, actionPrefix+"%")
		conds = append(conds, fmt.Sprintf("action LIKE $%d", len(args)))
	}

	rows, err := r.db.QueryContext(ctx, `SELECT id, occurred_at, actor_type, actor_id, actor_label, role, company_id, action, target, result, ip, metadata
		FROM saas_audit_log WHERE `+strings.Join(conds, " AND ")+` ORDER BY occurred_at DESC, id DESC LIMIT $1 OFFSET $2`, args...)
	if err != nil {
		return nil, err
	}

	defer rows.Close()
	var out []*AuditEntry
	for rows.Next() {
		var (
			e    = &AuditEntry{}
			meta []byte
		)
		if err = rows.Scan(&e.ID, &e.OccurredAt, &e.ActorType, &e.ActorID, &e.ActorLabel, &e.Role, &e.CompanyID, &e.Action, &e.Target, &e.Result, &e.IP, &meta); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(meta, &e.Metadata)
		out = append(out, e)
	}

	return out, rows.Err()
}
