# Contributing to St.Cloud\~OS

St.Cloud\~OS is developed by Culp Industries.

## Workflow

1. Create a branch from the default branch.
2. Keep changes focused; include tests for new behaviour.
3. Run the checks before opening a pull request:
   ```bash
   cd server && go vet -mod=vendor ./saas/... && go test -mod=vendor ./saas/... ./auth/...
   cd client/web/<app> && yarn lint && yarn test
   ```
4. Never commit secrets (API keys, passwords, `.env` files).

## Guidelines

- Customer-facing text must use the St.Cloud\~OS brand (see `server/saas/brand.go`).
- Keep tenant isolation and subscription checks server-side (`server/saas/gate.go`).
- Preserve license and attribution notices (`LICENSE`, `NOTICE`, `DCO`).

## Developer Certificate of Origin

Contributions are accepted under the terms in the [DCO](DCO) file.
