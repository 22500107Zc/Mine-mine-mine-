package saas

import (
	"context"
	"database/sql"
	"fmt"
)

// schema holds idempotent PostgreSQL DDL for the CulpOS commercial layer.
//
// Statements are applied in order on every boot; each one must be safe to
// re-run (IF NOT EXISTS).
var schema = []string{
	`CREATE TABLE IF NOT EXISTS saas_companies (
		id                     BIGINT       PRIMARY KEY,
		name                   TEXT         NOT NULL,
		slug                   TEXT         NOT NULL,
		status                 TEXT         NOT NULL DEFAULT 'pending',
		owner_user_id          BIGINT       NOT NULL DEFAULT 0,
		owner_email            TEXT         NOT NULL,
		owner_name             TEXT         NOT NULL DEFAULT '',
		namespace_id           BIGINT       NOT NULL DEFAULT 0,
		role_owner_id          BIGINT       NOT NULL DEFAULT 0,
		role_admin_id          BIGINT       NOT NULL DEFAULT 0,
		role_manager_id        BIGINT       NOT NULL DEFAULT 0,
		role_employee_id       BIGINT       NOT NULL DEFAULT 0,
		stripe_customer_id     TEXT         NULL,
		stripe_subscription_id TEXT         NULL,
		stripe_price_id        TEXT         NOT NULL DEFAULT '',
		subscription_status    TEXT         NOT NULL DEFAULT '',
		billing_period_start   TIMESTAMPTZ  NULL,
		billing_period_end     TIMESTAMPTZ  NULL,
		cancel_at_period_end   BOOLEAN      NOT NULL DEFAULT FALSE,
		canceled_at            TIMESTAMPTZ  NULL,
		provisioning_status    TEXT         NOT NULL DEFAULT 'awaiting_payment',
		provisioning_error     TEXT         NOT NULL DEFAULT '',
		provisioned_at         TIMESTAMPTZ  NULL,
		last_activity_at       TIMESTAMPTZ  NULL,
		payment_method_summary TEXT         NOT NULL DEFAULT '',
		created_at             TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
		updated_at             TIMESTAMPTZ  NOT NULL DEFAULT NOW()
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS saas_companies_slug_uq ON saas_companies (slug)`,
	`ALTER TABLE saas_companies ADD COLUMN IF NOT EXISTS profile_website TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE saas_companies ADD COLUMN IF NOT EXISTS profile_phone TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE saas_companies ADD COLUMN IF NOT EXISTS profile_address TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE saas_companies ADD COLUMN IF NOT EXISTS profile_industry TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE saas_companies ADD COLUMN IF NOT EXISTS onboarding_completed_at TIMESTAMPTZ NULL`,
	`CREATE UNIQUE INDEX IF NOT EXISTS saas_companies_stripe_customer_uq ON saas_companies (stripe_customer_id) WHERE stripe_customer_id IS NOT NULL`,
	`CREATE UNIQUE INDEX IF NOT EXISTS saas_companies_stripe_subscription_uq ON saas_companies (stripe_subscription_id) WHERE stripe_subscription_id IS NOT NULL`,
	`CREATE INDEX IF NOT EXISTS saas_companies_created_at_idx ON saas_companies (created_at DESC)`,
	`CREATE INDEX IF NOT EXISTS saas_companies_namespace_idx ON saas_companies (namespace_id)`,

	`CREATE TABLE IF NOT EXISTS saas_company_members (
		company_id  BIGINT      NOT NULL REFERENCES saas_companies (id),
		user_id     BIGINT      NOT NULL,
		role        TEXT        NOT NULL,
		invited_by  BIGINT      NOT NULL DEFAULT 0,
		created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		PRIMARY KEY (company_id, user_id)
	)`,
	// A user belongs to exactly one company; this is the root of tenant isolation
	`CREATE UNIQUE INDEX IF NOT EXISTS saas_company_members_user_uq ON saas_company_members (user_id)`,

	// exactly one platform Founder; identified by password only
	`CREATE TABLE IF NOT EXISTS saas_founders (
		id              BIGINT      PRIMARY KEY,
		password_hash   TEXT        NOT NULL,
		failed_attempts INTEGER     NOT NULL DEFAULT 0,
		locked_until    TIMESTAMPTZ NULL,
		last_login_at   TIMESTAMPTZ NULL,
		created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`,

	`CREATE TABLE IF NOT EXISTS saas_founder_sessions (
		token_hash   TEXT        PRIMARY KEY,
		founder_id   BIGINT      NOT NULL REFERENCES saas_founders (id),
		csrf_token   TEXT        NOT NULL,
		created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		expires_at   TIMESTAMPTZ NOT NULL,
		last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		ip           TEXT        NOT NULL DEFAULT '',
		user_agent   TEXT        NOT NULL DEFAULT ''
	)`,
	`CREATE INDEX IF NOT EXISTS saas_founder_sessions_expires_idx ON saas_founder_sessions (expires_at)`,

	// Databases created before the Founder became password-only may hold a
	// username column and more than one Founder row: keep the Founder that
	// signed in most recently, drop the username and allow a single row.
	`DELETE FROM saas_founder_sessions WHERE founder_id <> (
		SELECT id FROM saas_founders ORDER BY last_login_at DESC NULLS LAST, created_at DESC, id DESC LIMIT 1)`,
	`DELETE FROM saas_founders WHERE id <> (
		SELECT id FROM saas_founders ORDER BY last_login_at DESC NULLS LAST, created_at DESC, id DESC LIMIT 1)`,
	`DROP INDEX IF EXISTS saas_founders_username_uq`,
	`ALTER TABLE saas_founders DROP COLUMN IF EXISTS username`,
	`CREATE UNIQUE INDEX IF NOT EXISTS saas_founders_single_uq ON saas_founders ((TRUE))`,

	`CREATE TABLE IF NOT EXISTS saas_stripe_events (
		id           TEXT        PRIMARY KEY,
		type         TEXT        NOT NULL,
		status       TEXT        NOT NULL DEFAULT 'received',
		error        TEXT        NOT NULL DEFAULT '',
		company_id   BIGINT      NOT NULL DEFAULT 0,
		received_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		processed_at TIMESTAMPTZ NULL
	)`,
	`CREATE INDEX IF NOT EXISTS saas_stripe_events_received_idx ON saas_stripe_events (received_at DESC)`,

	`CREATE TABLE IF NOT EXISTS saas_payments (
		id           TEXT        PRIMARY KEY,
		company_id   BIGINT      NOT NULL DEFAULT 0,
		amount_cents BIGINT      NOT NULL DEFAULT 0,
		currency     TEXT        NOT NULL DEFAULT 'usd',
		status       TEXT        NOT NULL,
		created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`,
	`CREATE INDEX IF NOT EXISTS saas_payments_created_idx ON saas_payments (created_at DESC)`,

	`CREATE TABLE IF NOT EXISTS saas_audit_log (
		id          BIGSERIAL   PRIMARY KEY,
		occurred_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		actor_type  TEXT        NOT NULL,
		actor_id    TEXT        NOT NULL DEFAULT '',
		actor_label TEXT        NOT NULL DEFAULT '',
		role        TEXT        NOT NULL DEFAULT '',
		company_id  BIGINT      NOT NULL DEFAULT 0,
		action      TEXT        NOT NULL,
		target      TEXT        NOT NULL DEFAULT '',
		result      TEXT        NOT NULL DEFAULT '',
		ip          TEXT        NOT NULL DEFAULT '',
		metadata    JSONB       NOT NULL DEFAULT '{}'::jsonb
	)`,
	`CREATE INDEX IF NOT EXISTS saas_audit_log_occurred_idx ON saas_audit_log (occurred_at DESC)`,
	`CREATE INDEX IF NOT EXISTS saas_audit_log_company_idx ON saas_audit_log (company_id, occurred_at DESC)`,

	// Command Deck: every change to a workspace record, per company
	`CREATE TABLE IF NOT EXISTS saas_activity_events (
		id            BIGSERIAL   PRIMARY KEY,
		company_id    BIGINT      NOT NULL,
		occurred_at   TIMESTAMPTZ NOT NULL,
		module        TEXT        NOT NULL,
		record_id     BIGINT      NOT NULL,
		title         TEXT        NOT NULL DEFAULT '',
		kind          TEXT        NOT NULL,
		from_status   TEXT        NOT NULL DEFAULT '',
		to_status     TEXT        NOT NULL DEFAULT '',
		actor_id      BIGINT      NOT NULL DEFAULT 0,
		assignee_id   BIGINT      NOT NULL DEFAULT 0,
		department_id BIGINT      NOT NULL DEFAULT 0,
		due_at        TIMESTAMPTZ NULL,
		source        TEXT        NOT NULL DEFAULT 'live'
	)`,
	`CREATE INDEX IF NOT EXISTS saas_activity_company_idx ON saas_activity_events (company_id, occurred_at)`,
	`CREATE INDEX IF NOT EXISTS saas_activity_record_idx ON saas_activity_events (company_id, record_id)`,
	`ALTER TABLE saas_companies ADD COLUMN IF NOT EXISTS activity_backfilled_at TIMESTAMPTZ NULL`,

	// Command Deck goals: target completion time per workflow
	`CREATE TABLE IF NOT EXISTS saas_deck_targets (
		company_id  BIGINT      NOT NULL,
		module      TEXT        NOT NULL,
		cycle_hours NUMERIC     NOT NULL,
		updated_by  BIGINT      NOT NULL DEFAULT 0,
		updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		PRIMARY KEY (company_id, module)
	)`,

	// Command Deck intervention tests (before/after measurement)
	`CREATE TABLE IF NOT EXISTS saas_deck_interventions (
		id          BIGSERIAL   PRIMARY KEY,
		company_id  BIGINT      NOT NULL,
		title       TEXT        NOT NULL,
		module      TEXT        NOT NULL DEFAULT '',
		metric      TEXT        NOT NULL,
		baseline    NUMERIC     NOT NULL DEFAULT 0,
		baseline_n  INTEGER     NOT NULL DEFAULT 0,
		started_at  TIMESTAMPTZ NOT NULL,
		ended_at    TIMESTAMPTZ NULL,
		created_by  BIGINT      NOT NULL DEFAULT 0
	)`,
	`CREATE INDEX IF NOT EXISTS saas_deck_interventions_company_idx ON saas_deck_interventions (company_id, started_at DESC)`,

	// Issues reported by company users to platform support
	`CREATE TABLE IF NOT EXISTS saas_issue_reports (
		id          BIGSERIAL   PRIMARY KEY,
		company_id  BIGINT      NOT NULL,
		user_id     BIGINT      NOT NULL,
		category    TEXT        NOT NULL,
		summary     TEXT        NOT NULL,
		details     TEXT        NOT NULL DEFAULT '',
		page        TEXT        NOT NULL DEFAULT '',
		status      TEXT        NOT NULL DEFAULT 'open',
		created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`,
	`CREATE INDEX IF NOT EXISTS saas_issue_reports_created_idx ON saas_issue_reports (created_at DESC)`,
	`CREATE INDEX IF NOT EXISTS saas_issue_reports_company_idx ON saas_issue_reports (company_id, created_at DESC)`,

	// Command Deck rows always belong to an existing company; added
	// idempotently and NOT VALID so upgrades never fail on rows already stored
	companyKey("saas_activity_events"),
	companyKey("saas_deck_targets"),
	companyKey("saas_deck_interventions"),
	companyKey("saas_issue_reports"),

	// Execution intelligence: richer event history (team, type, priority,
	// the customer and case a record belongs to)
	`ALTER TABLE saas_activity_events ADD COLUMN IF NOT EXISTS team_id     BIGINT NOT NULL DEFAULT 0`,
	`ALTER TABLE saas_activity_events ADD COLUMN IF NOT EXISTS category    TEXT   NOT NULL DEFAULT ''`,
	`ALTER TABLE saas_activity_events ADD COLUMN IF NOT EXISTS priority    TEXT   NOT NULL DEFAULT ''`,
	`ALTER TABLE saas_activity_events ADD COLUMN IF NOT EXISTS customer_id BIGINT NOT NULL DEFAULT 0`,
	`ALTER TABLE saas_activity_events ADD COLUMN IF NOT EXISTS case_id     BIGINT NOT NULL DEFAULT 0`,
	`CREATE INDEX IF NOT EXISTS saas_activity_company_id_idx ON saas_activity_events (company_id, id)`,

	// SLA targets per workflow ('' stage) and per stage of a workflow
	`ALTER TABLE saas_deck_targets ADD COLUMN IF NOT EXISTS stage TEXT NOT NULL DEFAULT ''`,
	`DO $$ BEGIN
		IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'saas_deck_targets_pkey' AND array_length(conkey, 1) = 2) THEN
			ALTER TABLE saas_deck_targets DROP CONSTRAINT saas_deck_targets_pkey;
			ALTER TABLE saas_deck_targets ADD CONSTRAINT saas_deck_targets_pkey PRIMARY KEY (company_id, module, stage);
		END IF;
	END $$`,

	// Goals measured against the company's own history
	`CREATE TABLE IF NOT EXISTS saas_goals (
		id          BIGSERIAL   PRIMARY KEY,
		company_id  BIGINT      NOT NULL,
		title       TEXT        NOT NULL,
		metric      TEXT        NOT NULL,
		module      TEXT        NOT NULL DEFAULT '',
		stage       TEXT        NOT NULL DEFAULT '',
		target      NUMERIC     NOT NULL,
		baseline    NUMERIC     NOT NULL DEFAULT 0,
		baseline_n  INTEGER     NOT NULL DEFAULT 0,
		start_at    TIMESTAMPTZ NOT NULL,
		target_at   TIMESTAMPTZ NULL,
		status      TEXT        NOT NULL DEFAULT 'active',
		created_by  BIGINT      NOT NULL DEFAULT 0,
		created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		closed_at   TIMESTAMPTZ NULL
	)`,
	`CREATE INDEX IF NOT EXISTS saas_goals_company_idx ON saas_goals (company_id, created_at DESC)`,
	companyKey("saas_goals"),

	// Intervention tests: owner, stage, measurement windows, notes, frozen result
	`ALTER TABLE saas_deck_interventions ADD COLUMN IF NOT EXISTS stage         TEXT    NOT NULL DEFAULT ''`,
	`ALTER TABLE saas_deck_interventions ADD COLUMN IF NOT EXISTS owner_id      BIGINT  NOT NULL DEFAULT 0`,
	`ALTER TABLE saas_deck_interventions ADD COLUMN IF NOT EXISTS baseline_days INTEGER NOT NULL DEFAULT 28`,
	`ALTER TABLE saas_deck_interventions ADD COLUMN IF NOT EXISTS eval_days     INTEGER NOT NULL DEFAULT 28`,
	`ALTER TABLE saas_deck_interventions ADD COLUMN IF NOT EXISTS notes         TEXT    NOT NULL DEFAULT ''`,
	`ALTER TABLE saas_deck_interventions ADD COLUMN IF NOT EXISTS result        NUMERIC NULL`,
	`ALTER TABLE saas_deck_interventions ADD COLUMN IF NOT EXISTS result_n      INTEGER NULL`,

	// Issue inbox: review states and a resolution note
	`ALTER TABLE saas_issue_reports ADD COLUMN IF NOT EXISTS resolution_note TEXT        NOT NULL DEFAULT ''`,
	`ALTER TABLE saas_issue_reports ADD COLUMN IF NOT EXISTS updated_at      TIMESTAMPTZ NULL`,
	`CREATE INDEX IF NOT EXISTS saas_issue_reports_status_idx ON saas_issue_reports (status, created_at DESC)`,

	// Command Deck usage per company, user and day (for the Founder)
	`CREATE TABLE IF NOT EXISTS saas_deck_usage (
		company_id BIGINT NOT NULL,
		user_id    BIGINT NOT NULL,
		day        DATE   NOT NULL,
		views      INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (company_id, day, user_id)
	)`,
	companyKey("saas_deck_usage"),
}

// companyKey adds a foreign key from table.company_id to saas_companies once
func companyKey(table string) string {
	return fmt.Sprintf(`DO $$ BEGIN
		IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = '%[1]s_company_fk') THEN
			ALTER TABLE %[1]s ADD CONSTRAINT %[1]s_company_fk FOREIGN KEY (company_id) REFERENCES saas_companies (id) NOT VALID;
		END IF;
	END $$`, table)
}

// Migrate applies the schema
func Migrate(ctx context.Context, db *sql.DB) error {
	for i, stmt := range schema {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("saas schema migration step %d failed: %w", i, err)
		}
	}

	return nil
}
