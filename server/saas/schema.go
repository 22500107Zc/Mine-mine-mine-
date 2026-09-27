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

	`CREATE TABLE IF NOT EXISTS saas_founders (
		id              BIGINT      PRIMARY KEY,
		username        TEXT        NOT NULL,
		password_hash   TEXT        NOT NULL,
		failed_attempts INTEGER     NOT NULL DEFAULT 0,
		locked_until    TIMESTAMPTZ NULL,
		last_login_at   TIMESTAMPTZ NULL,
		created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS saas_founders_username_uq ON saas_founders (LOWER(username))`,

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
