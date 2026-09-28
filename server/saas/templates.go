package saas

import (
	"embed"
	"html/template"
	"strings"
	"time"
)

//go:embed assets/templates/*.html
var templateFS embed.FS

//go:embed assets/static
var staticFS embed.FS

func parseTemplates(b Brand) (*template.Template, error) {
	return template.New("").Funcs(template.FuncMap{
		"brand": func() Brand { return b },
		"money": FormatCents,
		"date": func(t any) string {
			switch v := t.(type) {
			case *time.Time:
				if v == nil {
					return "—"
				}
				return v.Format("Jan 2, 2006")
			case time.Time:
				if v.IsZero() {
					return "—"
				}
				return v.Format("Jan 2, 2006")
			}
			return "—"
		},
		"datetime": func(t any) string {
			switch v := t.(type) {
			case *time.Time:
				if v == nil {
					return "—"
				}
				return v.UTC().Format("Jan 2, 2006 15:04 UTC")
			case time.Time:
				if v.IsZero() {
					return "—"
				}
				return v.UTC().Format("Jan 2, 2006 15:04 UTC")
			}
			return "—"
		},
		"subPill": func(s SubscriptionStatus) string {
			switch s {
			case SubActive:
				return "ok"
			case SubPastDue, SubIncomplete, SubTrialing, "":
				return "warn"
			case SubCanceled, SubUnpaid, SubIncompleteExpired, SubPaused:
				return "bad"
			}
			return "neutral"
		},
		"companyPill": func(s CompanyStatus) string {
			switch s {
			case CompanyActive:
				return "ok"
			case CompanyDisabled:
				return "bad"
			}
			return "warn"
		},
		"orDash": func(s string) string {
			if strings.TrimSpace(s) == "" {
				return "—"
			}
			return s
		},
		"upper": strings.ToUpper,
		"actionLabel": func(a string) string {
			if l, ok := actionLabels[a]; ok {
				return l
			}
			return a
		},
		"add": func(a, b int) int { return a + b },
		"dict": func(kv ...any) map[string]any {
			m := make(map[string]any, len(kv)/2)
			for i := 0; i+1 < len(kv); i += 2 {
				if k, ok := kv[i].(string); ok {
					m[k] = kv[i+1]
				}
			}
			return m
		},
		"sub": func(a, b int) int { return a - b },
	}).ParseFS(templateFS, "assets/templates/*.html")
}

// actionLabels are human readable names for audit actions
var actionLabels = map[string]string{
	"company.signup":               "Company signed up",
	"company.provision":            "Workspace provisioned",
	"company.provision.retry":      "Provisioning retried",
	"company.disable":              "Company disabled",
	"company.enable":               "Company enabled",
	"company.profile.update":       "Company profile updated",
	"company.member.invite":        "User invited",
	"company.member.role":          "User role changed",
	"company.member.remove":        "User removed",
	"company.billing.sync":         "Billing refreshed from Stripe",
	"billing.subscription.changed": "Subscription changed",
	"billing.subscription.cancel":  "Cancellation scheduled",
	"billing.subscription.resume":  "Subscription resumed",
	"billing.payment.succeeded":    "Payment received",
	"billing.payment.failed":       "Payment failed",
	"billing.portal.open":          "Billing portal opened",
	"billing.checkout":             "Checkout started",
	"founder.login":                "Founder sign-in",
	"founder.logout":               "Founder sign-out",
	"founder.bootstrap":            "Founder account created",
	"founder.password.change":      "Founder password changed",
	"founder.password.reset":       "Founder password reset",
	"founder.csrf":                 "Blocked request (CSRF)",
	"user.disable":                 "User disabled",
	"user.enable":                  "User enabled",
	"user.reset-access":            "User access reset",
	"onboarding.complete":          "Setup completed",
	"onboarding.customer.create":   "First customer added",
	"onboarding.task.create":       "First task created",
	"api.permissions":              "Blocked permission change",
}
