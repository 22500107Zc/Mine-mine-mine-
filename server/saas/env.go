package saas

import (
	"net"
	"net/mail"
	"net/url"
	"os"
	"strings"
)

// envAliases maps St.Cloud~OS deployment variables to the variables the
// underlying application reads. Existing values are never overwritten.
var envAliases = [][2]string{
	{"DATABASE_URL", "DB_DSN"},
	{"SMTP_USERNAME", "SMTP_USER"},
	{"SMTP_PASSWORD", "SMTP_PASS"},
	{"JWT_SECRET", "AUTH_JWT_SECRET"},
	{"CSRF_SECRET", "AUTH_CSRF_SECRET"},
	{"SESSION_SECRET", "SAAS_SECRET"},
	{"AUTH_URL", "AUTH_BASE_URL"},
}

// ApplyEnvAliases must run before application options are loaded
func ApplyEnvAliases() {
	for _, a := range envAliases {
		setDefault(a[1], os.Getenv(a[0]))
	}

	// MAIL_FROM + MAIL_FROM_NAME → SMTP_FROM (`"St.Cloud~OS" <noreply@...>`)
	if from := strings.TrimSpace(os.Getenv("MAIL_FROM")); from != "" {
		name := env("MAIL_FROM_NAME", DefaultProductName)
		setDefault("SMTP_FROM", (&mail.Address{Name: name, Address: from}).String())
	}

	// APP_URL drives the public host name and TLS awareness
	if app := env("APP_URL", env("PUBLIC_APP_URL", "")); app != "" {
		if u, err := url.Parse(app); err == nil && u.Host != "" {
			setDefault("DOMAIN", u.Host)

			// The sign-in session must reach /billing and /company, not only /auth
			setDefault("AUTH_SESSION_COOKIE_PATH", "/")
			if _, set := os.LookupEnv("AUTH_SESSION_COOKIE_DOMAIN"); !set {
				host := u.Hostname()
				if host == "localhost" || net.ParseIP(host) != nil {
					// host-only cookie
					host = ""
				}
				_ = os.Setenv("AUTH_SESSION_COOKIE_DOMAIN", host)
			}
			if u.Scheme == "https" {
				setDefault("HTTP_SSL_TERMINATED", "true")
				setDefault("AUTH_SESSION_COOKIE_SECURE", "true")
			}

			setDefault("AUTH_BASE_URL", strings.TrimRight(app, "/")+"/auth")
			setDefault("AUTH_EXTERNAL_REDIRECT_URL", strings.TrimRight(app, "/")+"/auth/external/{provider}/callback")
		}
	}
}

func setDefault(key, val string) {
	if val == "" {
		return
	}

	if cur, ok := os.LookupEnv(key); ok && cur != "" {
		return
	}

	_ = os.Setenv(key, val)
}
