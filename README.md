# CulpOS

**Business Operations System** — by Culp Industries

CulpOS is one operating system for running a company's internal operations:
customers, contacts, records, cases, tasks, approvals and workflows, documents,
reports, departments and team management — in a single, isolated company
workspace.

- **Plan:** CulpOS — **$333.88 USD / month** per company (one subscription = one company account)
- No free plan, no free trial, no public demo account
- Payments: Stripe (Checkout, Customer Portal, verified webhooks)
- Database: PostgreSQL

---

## Contents

1. [What the product does](#what-the-product-does)
2. [Architecture](#architecture)
3. [Development setup](#development-setup)
4. [PostgreSQL setup](#postgresql-setup)
5. [Environment setup](#environment-setup)
6. [Stripe configuration](#stripe-configuration)
7. [Founder bootstrap](#founder-bootstrap)
8. [Email configuration](#email-configuration)
9. [Production deployment](#production-deployment)
10. [Backup and restore](#backup-and-restore)
11. [Testing](#testing)
12. [Build commands](#build-commands)
13. [Open Source Notices](#open-source-notices)

---

## What the product does

| Area | Where |
| --- | --- |
| Paid company signup (Company → Plan → Stripe Checkout → activation) | `/signup` |
| Sign in / forgot password / accept invitation | `/auth/login` |
| Company workspace (Dashboard, Customers, Contacts, Records, Cases, Tasks, Workflows/Approvals, Documents, Reports, Team, Departments, Settings, Billing, Company Admin) | `/` → Workspace |
| Company Admin (profile, users, roles, invitations) | `/company` |
| Billing (status, next billing date, payment method, Stripe portal, cancel) | `/billing` |
| Founder console (platform owner) | `/founder` → `/founder/dashboard` |
| Terms / Privacy / Support / Open Source Notices | `/legal/terms`, `/legal/privacy`, `/support`, `/legal/open-source` |
| Stripe webhook endpoint | `POST /stripe/webhook` |

**Roles.** `Founder` (platform) → `Company Owner` → `Administrator` → `Manager` → `Employee`.
Founder is a platform account stored separately from company users; no API or
company role can create or grant it.

**Isolation.** Each company gets its own workspace, its own roles and access
rules scoped to that workspace only. A server-side API gate additionally
rejects any request from a company user that targets another company's
workspace, users, roles or platform administration endpoints (responses are
indistinguishable from "not found").

**Subscription gating.** Access is evaluated server-side on every API request
and before any access token is issued:

| Stripe status | Access |
| --- | --- |
| `active` | full |
| `past_due` | full + billing warning and recovery flow |
| `canceled` | full until the paid period ends, then billing/recovery only |
| `unpaid`, `incomplete`, `incomplete_expired`, `paused`, unknown | billing/recovery only |
| company disabled by Founder | neutral "access unavailable" message |

Customer data is never deleted because of billing state.

## Architecture

```
server/                 Go application server (API, sign-in, provisioning)
server/saas/            CulpOS commercial layer: brand config, companies, Stripe,
                        webhooks, subscription gating, tenant isolation, Founder console,
                        signup/billing/company pages, branded email
server/saas/integration Adapter to users/roles/access-control/workspace services
server/provision/       First-boot configuration, incl. 500_culpos_workspace (workspace template)
client/web/*            Web applications (workspace, configuration studio, ...)
lib/js, lib/vue         Shared front-end libraries
locale/en               UI strings
```

The central brand configuration lives in `server/saas/brand.go` and is driven by
environment variables (`PRODUCT_NAME`, `COMPANY_NAME`, `PRODUCT_DESCRIPTION`,
`SUPPORT_EMAIL`, `APP_URL`, `PUBLIC_APP_URL`). The price is fixed at 33388 cents
(`PRICE_CENTS`) and must match the Stripe price configured in `STRIPE_PRICE_ID`.

Internal technical identifiers (Go module path, RBAC resource prefixes,
`window.CortezaAPI` config globals, npm package names) intentionally keep their
upstream names; they are never shown to customers.

## Development setup

Requirements: Go 1.24+, Node 22 + Yarn 1, PostgreSQL 14+ (16 recommended).

```bash
# 1. Front-end libraries and apps
cd lib/js  && yarn && yarn link && yarn build
cd ../vue  && yarn && yarn link @cortezaproject/corteza-js && yarn build && yarn link
cd ../../client/web/one && yarn && yarn cdeps && yarn build   # repeat for compose, admin, ...

# 2. Embed language files into the server
cd ../../../server/pkg/locale && make src/en

# 3. Run the server
cd ../.. && cp ../.env.example .env   # edit values
go run -mod=vendor ./cmd/corteza serve-api
```

For local development without webapps set `HTTP_WEBAPP_ENABLED=false`
(the API is then served from `/`).

## PostgreSQL setup

```sql
CREATE USER culpos WITH PASSWORD 'change-me';
CREATE DATABASE culpos OWNER culpos;
```

```
DATABASE_URL=postgres://culpos:change-me@db-host:5432/culpos?sslmode=require
```

All schema (application tables and the CulpOS `saas_*` tables) is created and
migrated automatically at startup. CulpOS refuses to start its commercial layer
on any database other than PostgreSQL.

## Environment setup

Copy `.env.example` to `.env` (or configure the same keys in your platform's
secret manager). Required in production:

| Variable | Purpose |
| --- | --- |
| `APP_URL` / `PUBLIC_APP_URL` | Public https URL of CulpOS. All email links, Stripe return URLs, canonical/OpenGraph URLs derive from it. |
| `DATABASE_URL` | PostgreSQL connection string |
| `FOUNDER_BOOTSTRAP_USERNAME`, `FOUNDER_BOOTSTRAP_PASSWORD` | Founder account (first boot) |
| `STRIPE_SECRET_KEY`, `STRIPE_PUBLISHABLE_KEY`, `STRIPE_WEBHOOK_SECRET`, `STRIPE_PRICE_ID` | Billing |
| `SMTP_HOST`, `SMTP_PORT`, `SMTP_USERNAME`, `SMTP_PASSWORD`, `MAIL_FROM`, `MAIL_FROM_NAME` | Email |
| `JWT_SECRET`, `CSRF_SECRET`, `SESSION_SECRET` | Signing secrets (`openssl rand -hex 32`) |
| `SUPPORT_EMAIL` | Shown on Support page, footers and emails |

Secure, HTTP-only cookies are enabled automatically when `APP_URL` starts with `https://`.

## Stripe configuration

1. In Stripe create a **Product** named `CulpOS` with one **recurring Price**:
   `33388` USD cents, interval `month`. Put the price ID into `STRIPE_PRICE_ID`.
2. Set `STRIPE_SECRET_KEY` and `STRIPE_PUBLISHABLE_KEY` (the secret key and the
   webhook secret are only ever used server-side).
3. Create a webhook endpoint `https://<APP_URL>/stripe/webhook` with the events:
   `checkout.session.completed`, `checkout.session.async_payment_succeeded`,
   `checkout.session.async_payment_failed`, `customer.subscription.created`,
   `customer.subscription.updated`, `customer.subscription.deleted`,
   `customer.subscription.paused`, `customer.subscription.resumed`,
   `invoice.paid`, `invoice.payment_failed`, `invoice.payment_action_required`,
   `payment_method.attached`, `customer.updated`.
   Put its signing secret into `STRIPE_WEBHOOK_SECRET`.
4. Enable the **Customer Portal** (payment method updates, invoice history,
   cancellation) in the Stripe dashboard.

Flow: `/signup` creates a pending company and a *suspended* owner account →
Stripe Checkout → the signature-verified webhook fetches the subscription from
the Stripe API → the company is provisioned exactly once (idempotent claim +
event de-duplication) → the owner can sign in. Reaching the success URL never
grants access by itself.

## Founder bootstrap

On boot, if no Founder with `FOUNDER_BOOTSTRAP_USERNAME` exists and
`FOUNDER_BOOTSTRAP_PASSWORD` is set, CulpOS creates it with a bcrypt hash
(cost 12). The plaintext is never stored or logged. Sign in at `/founder`,
then change the password at `/founder/account`. To reset a lost Founder
password set a new `FOUNDER_BOOTSTRAP_PASSWORD` together with
`FOUNDER_BOOTSTRAP_FORCE_RESET=true` for one restart (all Founder sessions are
revoked), then remove the flag.

Founder sessions: random 256-bit token (only its SHA-256 is stored),
HTTP-only / SameSite=Strict / Secure cookie scoped to `/founder`, 30-minute
idle and 8-hour absolute expiry, server-side logout, per-session CSRF tokens,
per-IP rate limiting and account lockout after repeated failures, generic
failure messages, full audit logging.

## Email configuration

Set `SMTP_HOST`, `SMTP_PORT`, `SMTP_USERNAME`, `SMTP_PASSWORD`, `MAIL_FROM`
and `MAIL_FROM_NAME` (defaults to `CulpOS`). CulpOS sends branded welcome,
invitation (with company and inviter), payment received, payment failed,
cancellation, password reset, email confirmation and sign-in code emails. All
links derive from `APP_URL`.

## Production deployment

```bash
cp .env.example .env        # fill in values / use your secret manager
docker compose up -d --build
```

The root `Dockerfile` builds the web applications and the server from source
into a non-root runtime image listening on `:8080`. Put an https reverse proxy
or load balancer in front of it and set `APP_URL` to the public https URL.
Health check: `GET /healthcheck`.

Production checklist:
- `ENVIRONMENT=production` (default in the image; hides debug details)
- web console disabled (`HTTP_SERVER_WEB_CONSOLE_ENABLED=false`, image default)
- all secrets from secret storage, `APP_URL` is https
- Stripe live keys + webhook configured, SMTP verified

## Backup and restore

Backup (PostgreSQL holds all company data, billing state and audit logs;
`/data` holds uploaded files):

```bash
pg_dump --format=custom --no-owner "$DATABASE_URL" > culpos-$(date +%F).dump
tar czf culpos-files-$(date +%F).tgz -C /var/lib/docker/volumes/culpos_culpos-data/_data .
```

Restore:

```bash
createdb culpos_restore
pg_restore --no-owner --dbname "postgres://…/culpos_restore" culpos-YYYY-MM-DD.dump
tar xzf culpos-files-YYYY-MM-DD.tgz -C <data volume>
# point DATABASE_URL at the restored database and restart CulpOS
```

Test restores regularly. Stripe remains the source of truth for billing; after
a restore use **Founder → Company → Refresh from Stripe** for affected companies.

## Testing

```bash
cd server
# CulpOS commercial layer (unit + PostgreSQL integration tests)
CULPOS_TEST_DATABASE_URL=postgres://culpos:culpos@localhost:5432/culpos_test?sslmode=disable \
  go test -mod=vendor ./saas/...
go test -mod=vendor ./auth/...
```

`server/saas/testdata/stripe_mock.py` is a local Stripe API mock that emits
correctly signed webhooks for end-to-end testing (development only).

## Build commands

```bash
# server binary
cd server && go build -mod=vendor -o build/culpos-server ./cmd/corteza
# web applications
cd client/web/<app> && yarn build
# container image
docker build -t culpos .
```

## Open Source Notices

CulpOS includes open source software. It is derived from the Corteza project,
licensed under the Apache License, Version 2.0. See [LICENSE](LICENSE),
[NOTICE](NOTICE) and the in-app page `/legal/open-source`. CulpOS branding and
original CulpOS functionality are © Culp Industries.
