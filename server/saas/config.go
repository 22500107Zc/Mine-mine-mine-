package saas

import (
	"os"
	"strings"
	"time"
)

type (
	// Config holds all server-side SaaS configuration.
	//
	// Secrets (Stripe secret key, webhook secret, Founder bootstrap password)
	// live only here and are never rendered, logged or sent to the browser.
	Config struct {
		Enabled bool

		Brand Brand

		// Stripe
		StripeSecretKey      string
		StripePublishableKey string
		StripeWebhookSecret  string
		StripePriceID        string
		StripeAPIBase        string

		// Founder bootstrap
		FounderBootstrapPassword   string
		FounderBootstrapForceReset bool

		// Founder sessions
		FounderSessionIdleTTL     time.Duration
		FounderSessionAbsoluteTTL time.Duration
		FounderMaxFailedLogins    int
		FounderLockoutDuration    time.Duration

		// When true, cookies get the Secure flag.
		SecureCookies bool

		// Webhook timestamp tolerance
		WebhookTolerance time.Duration

		// When true, detailed errors are never rendered (production default)
		Production bool
	}
)

// LoadConfig reads configuration from the environment
func LoadConfig() Config {
	c := Config{
		Enabled: envBool("SAAS_ENABLED", true),
		Brand:   LoadBrand(),

		StripeSecretKey:      env("STRIPE_SECRET_KEY", ""),
		StripePublishableKey: env("STRIPE_PUBLISHABLE_KEY", ""),
		StripeWebhookSecret:  env("STRIPE_WEBHOOK_SECRET", ""),
		StripePriceID:        env("STRIPE_PRICE_ID", ""),
		StripeAPIBase:        strings.TrimRight(env("STRIPE_API_BASE", "https://api.stripe.com"), "/"),

		FounderBootstrapPassword:   os.Getenv("FOUNDER_BOOTSTRAP_PASSWORD"),
		FounderBootstrapForceReset: envBool("FOUNDER_BOOTSTRAP_FORCE_RESET", false),

		FounderSessionIdleTTL:     envDuration("FOUNDER_SESSION_IDLE_TTL", 30*time.Minute),
		FounderSessionAbsoluteTTL: envDuration("FOUNDER_SESSION_ABSOLUTE_TTL", 8*time.Hour),
		FounderMaxFailedLogins:    envInt("FOUNDER_MAX_FAILED_LOGINS", 5),
		FounderLockoutDuration:    envDuration("FOUNDER_LOCKOUT_DURATION", 15*time.Minute),

		WebhookTolerance: envDuration("STRIPE_WEBHOOK_TOLERANCE", 5*time.Minute),

		Production: !strings.EqualFold(env("ENVIRONMENT", "production"), "dev") &&
			!strings.EqualFold(env("ENVIRONMENT", "production"), "development") &&
			!strings.EqualFold(env("ENVIRONMENT", "production"), "test"),
	}

	c.SecureCookies = envBool("SECURE_COOKIES", !strings.HasPrefix(c.Brand.PublicAppURL, "http://"))
	return c
}

// StripeMissing lists missing server-side Stripe settings (names only, never values)
func (c Config) StripeMissing() []string {
	var out []string
	if c.StripeSecretKey == "" {
		out = append(out, "STRIPE_SECRET_KEY")
	}
	if c.StripeWebhookSecret == "" {
		out = append(out, "STRIPE_WEBHOOK_SECRET")
	}
	if c.StripePriceID == "" {
		out = append(out, "STRIPE_PRICE_ID")
	}
	return out
}

// StripeConfigured reports whether all server-side Stripe values are present
func (c Config) StripeConfigured() bool {
	return c.StripeSecretKey != "" && c.StripePriceID != "" && c.StripeWebhookSecret != ""
}

func envBool(key string, def bool) bool {
	switch strings.ToLower(env(key, "")) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}

	return def
}

func envInt(key string, def int) int {
	var i int
	if v := env(key, ""); v != "" {
		if _, err := fmtSscan(v, &i); err == nil {
			return i
		}
	}

	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if v := env(key, ""); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}

	return def
}
