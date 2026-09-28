package app

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/cortezaproject/corteza/server/pkg/eventbus"
	"github.com/cortezaproject/corteza/server/pkg/id"
	"github.com/cortezaproject/corteza/server/pkg/options"
	"github.com/cortezaproject/corteza/server/pkg/rbac"
	"github.com/cortezaproject/corteza/server/pkg/version"
	"github.com/cortezaproject/corteza/server/saas"
	"github.com/cortezaproject/corteza/server/saas/integration"
	"github.com/cortezaproject/corteza/server/store/adapters/rdbms"
	sysService "github.com/cortezaproject/corteza/server/system/service"
	"github.com/jmoiron/sqlx"
	"go.uber.org/zap"
)

// initSaaS boots the St.Cloud~OS commercial layer: schema, Founder bootstrap,
// Stripe, subscription gating and tenant isolation.
func (app *CortezaApp) initSaaS(ctx context.Context) error {
	cfg := saas.LoadConfig()
	if !cfg.Enabled {
		app.Log.Warn("St.Cloud~OS commercial layer disabled (SAAS_ENABLED=false)")
		return nil
	}

	db, err := saasDB(app)
	if err != nil {
		if app.Opt.Environment.IsProduction() && !testing.Testing() {
			return err
		}

		// test harnesses and local tooling may run on other stores;
		// production always requires PostgreSQL
		app.Log.Warn("St.Cloud~OS commercial layer disabled outside production", zap.Error(err))
		return nil
	}

	if err = saas.Migrate(ctx, db); err != nil {
		return err
	}

	platform := &integration.Platform{
		Store:                 app.Store,
		Log:                   app.Log.Named("culpos.platform"),
		Rbac:                  rbac.Global(),
		Invite:                sysService.DefaultAuth.GenerateInviteToken,
		AuthBaseURL:           app.Opt.Auth.BaseURL,
		WorkspaceTemplateSlug: options.EnvString("WORKSPACE_TEMPLATE_SLUG", "culpos-workspace-template"),
	}

	mailer := integration.Mailer{
		FromAddress: options.EnvString("MAIL_FROM", ""),
		FromName:    options.EnvString("MAIL_FROM_NAME", saas.DefaultProductName),
	}

	secret := []byte(options.EnvString("SAAS_SECRET", app.Opt.Auth.Secret))

	svc, err := saas.NewService(cfg, saas.NewRepo(db), saas.NewStripeClient(cfg.StripeSecretKey, cfg.StripeAPIBase), platform, mailer, app.Log, secret, id.Next)
	if err != nil {
		return err
	}

	// Commercial policy: no free self-service signup (companies are created
	// through paid checkout), branded transactional email
	enforced := map[string]interface{}{
		"auth.internal.signup.enabled":                 false,
		"auth.internal.password-reset.enabled":         true,
		"auth.mail.from-name":                          options.EnvString("MAIL_FROM_NAME", saas.DefaultProductName),
		"auth.internal.send-user-invite-email.enabled": true,
	}

	if from := options.EnvString("MAIL_FROM", ""); from != "" {
		enforced["auth.mail.from-address"] = from
	}

	if cfg.Brand.PublicAppURL != "" {
		enforced["general.mail.logo"] = cfg.Brand.URL("/stcloud/static/stcloud-email-logo.png")
	}

	// St.Cloud~OS look for the web applications and sign-in pages (platform managed)
	themes, customCSS := saas.WebappTheme()
	enforced["ui.studio.themes"] = themes
	enforced["ui.studio.custom-css"] = customCSS

	// Default product logos for the web applications (only when not customized).
	// Earlier built-in defaults are replaced so existing installs pick up the
	// current brand; a logo an administrator uploaded is left alone.
	if l := sysService.CurrentSettings.UI.MainLogo; saas.IsBuiltinLogo(l) {
		enforced["ui.main-logo"] = saas.DefaultMainLogo
	}

	if l := sysService.CurrentSettings.UI.IconLogo; saas.IsBuiltinLogo(l) {
		enforced["ui.icon-logo"] = saas.DefaultIconLogo
	}

	for k, v := range enforced {
		if err = updateSetting(ctx, k, v); err != nil {
			app.Log.Warn("could not apply St.Cloud~OS setting", zap.String("key", k), zap.Error(err))
		}
	}

	// Content stored by versions before the product rename (email templates,
	// workspace pages, labels) is brought over to the current name
	platform.MigrateBrand(ctx, saas.LegacyProductName, cfg.Brand.ProductName, saas.LegacyProductDescription, cfg.Brand.ProductDescription)

	// Command Deck: measure every workspace record change per company
	integration.RegisterActivityHook(eventbus.Service(), svc)

	if err = svc.BootstrapFounder(ctx); err != nil {
		return fmt.Errorf("founder bootstrap failed: %w", err)
	}

	saas.Version = version.Version
	saas.LoadLicenseText("LICENSE", "../LICENSE", "/corteza/LICENSE", "/culpos/LICENSE")
	saas.MailConfigured = func() bool {
		return len(sysService.CurrentSettings.SMTP.Servers) > 0 && sysService.CurrentSettings.SMTP.Servers[0].Host != ""
	}

	if !cfg.StripeConfigured() {
		app.Log.Warn("Stripe is not configured (STRIPE_SECRET_KEY, STRIPE_WEBHOOK_SECRET, STRIPE_PRICE_ID); paid signup is unavailable")
	}

	app.SaaS = svc
	app.Log.Info("St.Cloud~OS commercial layer ready", zap.String("product", cfg.Brand.ProductName), zap.String("price", cfg.Brand.PricePerInterval()))
	return nil
}

// saasDB returns the PostgreSQL connection of the primary store
func saasDB(app *CortezaApp) (*sql.DB, error) {
	rs, ok := app.Store.(*rdbms.Store)
	if !ok {
		return nil, fmt.Errorf("St.Cloud~OS requires PostgreSQL (DB_DSN / DATABASE_URL)")
	}

	sx, ok := rs.DB.(*sqlx.DB)
	if !ok {
		return nil, fmt.Errorf("St.Cloud~OS requires a PostgreSQL connection pool")
	}

	if sx.DriverName() != "postgres" && sx.DriverName() != "postgres+debug" {
		return nil, fmt.Errorf("St.Cloud~OS requires PostgreSQL, got %q", sx.DriverName())
	}

	return sx.DB, nil
}
