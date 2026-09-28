package saas

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
)

// ConfigProblem is a startup configuration finding
type ConfigProblem struct {
	Fatal   bool
	Setting string
	Message string
}

// ValidateEnvironment checks deployment configuration at startup. It must run
// after ApplyEnvAliases. Only names of settings are reported, never values.
func ValidateEnvironment() []ConfigProblem {
	var (
		out        []ConfigProblem
		production = !strings.EqualFold(env("ENVIRONMENT", "production"), "dev") &&
			!strings.EqualFold(env("ENVIRONMENT", "production"), "development") &&
			!strings.EqualFold(env("ENVIRONMENT", "production"), "test")
		fatal = func(setting, msg string) { out = append(out, ConfigProblem{production, setting, msg}) }
		warn  = func(setting, msg string) { out = append(out, ConfigProblem{false, setting, msg}) }
	)

	if !envBool("SAAS_ENABLED", true) {
		return nil
	}

	dsn := env("DB_DSN", "")
	switch {
	case dsn == "":
		fatal("DATABASE_URL", "is required (PostgreSQL connection string, e.g. postgres://user:password@host:5432/culpos)")
	case !strings.HasPrefix(dsn, "postgres://") && !strings.HasPrefix(dsn, "postgresql://") && !strings.HasPrefix(dsn, "postgres+debug://"):
		fatal("DATABASE_URL", "must be a PostgreSQL connection string (postgres://...)")
	}

	app := env("APP_URL", env("PUBLIC_APP_URL", ""))
	if app == "" {
		fatal("APP_URL", "is required (public URL of CulpOS, e.g. https://app.example.com)")
	} else if u, err := url.Parse(app); err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		fatal("APP_URL", "must be an absolute http(s) URL")
	} else if u.Scheme != "https" && production {
		warn("APP_URL", "should use https in production so that secure cookies are enabled")
	}

	for _, s := range []struct{ name, alias string }{{"AUTH_JWT_SECRET", "JWT_SECRET"}, {"AUTH_CSRF_SECRET", "CSRF_SECRET"}} {
		if v := os.Getenv(s.name); len(v) < 32 {
			fatal(s.alias, "is required and must be at least 32 characters (generate with: openssl rand -hex 32)")
		}
	}

	if len(os.Getenv("SAAS_SECRET")) < 32 {
		warn("SESSION_SECRET", "is not set; checkout links are signed with JWT_SECRET instead")
	}

	if os.Getenv("FOUNDER_BOOTSTRAP_PASSWORD") == "" {
		warn("FOUNDER_BOOTSTRAP_PASSWORD", "is not set; the Founder account is only created when this is provided")
	}

	var missing []string
	for _, k := range []string{"STRIPE_SECRET_KEY", "STRIPE_WEBHOOK_SECRET", "STRIPE_PRICE_ID"} {
		if env(k, "") == "" {
			missing = append(missing, k)
		}
	}

	if len(missing) > 0 {
		warn(strings.Join(missing, ", "), "not set; paid signup and billing are unavailable until configured")
	}

	if env("SMTP_HOST", "") == "" {
		warn("SMTP_HOST", "not set; invitations, password resets and billing emails cannot be delivered")
	}

	if env("SUPPORT_EMAIL", "") == "" {
		warn("SUPPORT_EMAIL", "not set; customers are directed to reply to CulpOS emails for support")
	}

	return out
}

// ReportConfigProblems prints findings and returns true when startup must stop
func ReportConfigProblems(w io.Writer, pp []ConfigProblem) bool {
	stop := false
	for _, p := range pp {
		level := "WARNING"
		if p.Fatal {
			level = "ERROR"
			stop = true
		}

		_, _ = fmt.Fprintf(w, "CulpOS configuration %s: %s %s\n", level, p.Setting, p.Message)
	}

	if stop {
		_, _ = fmt.Fprintln(w, "CulpOS cannot start until the configuration errors above are fixed. See .env.example.")
	}

	return stop
}
