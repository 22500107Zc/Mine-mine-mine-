package saas

import (
	"embed"
	"fmt"
	"html/template"
	"mime"
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
		"upper":           strings.ToUpper,
		"issueCategories": func() []string { return issueCategories },
		"lower":           strings.ToLower,
		"effects": func(ee ...*Effect) []*Effect {
			var out []*Effect
			for _, e := range ee {
				if e != nil {
					out = append(out, e)
				}
			}
			return out
		},
		"stageStat": func(d *Deck, module string) *StageStat {
			for i := range d.Stages {
				if d.Stages[i].Module == module {
					return &d.Stages[i]
				}
			}
			return nil
		},
		"metricValue": func(v float64, unit string) string {
			switch unit {
			case "h":
				if v >= 48 {
					return fmt.Sprintf("%.1fd", v/24)
				}
				return fmt.Sprintf("%.1fh", v)
			case "%":
				return fmt.Sprintf("%.1f%%", v)
			}
			return fmt.Sprintf("%.2f%s", v, unit)
		},
		"dur": func(h float64) string {
			if h >= 48 {
				return fmt.Sprintf("%.1fd", h/24)
			}
			return fmt.Sprintf("%.1fh", h)
		},
		"hrs":  func(v float64) string { return fmt.Sprintf("%.1fh", v) },
		"pct1": func(v float64) string { return fmt.Sprintf("%.1f%%", v) },
		"num":  formatInt,
		"change": func(cur, prev float64) template.HTML {
			if prev <= 0 {
				return template.HTML(`<span class="muted">no baseline</span>`)
			}
			d := (cur - prev) / prev * 100
			if d >= 0 {
				return template.HTML(fmt.Sprintf(`<span class="up">▲ +%.1f%%</span>`, d))
			}
			return template.HTML(fmt.Sprintf(`<span class="down">▼ %.1f%%</span>`, d))
		},
		"isoDay": func(t time.Time) string { return t.Format("2006-01-02") },
		"float":  func(i int) float64 { return float64(i) },
		"inc":    func(i int) int { return i + 1 },
		"days":   func(h float64) float64 { return h / 24 },
		"seq": func(n int) []int {
			out := make([]int, n)
			for i := range out {
				out[i] = i
			}
			return out
		},
		"moduleLabel": func(m string) string {
			if l, ok := moduleLabels[m]; ok {
				return l
			}
			return m
		},
		"recsTop": func(rr []Recommendation, n int) []Recommendation {
			if len(rr) > n {
				return rr[:n]
			}
			return rr
		},
		"weekday": func(i int) string { return []string{"M", "", "W", "", "F", "", "S"}[i] },
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

// formatInt renders 8146 as 8,146
func formatInt(n int) string {
	s := fmt.Sprintf("%d", n)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

func init() {
	// bundled web fonts (IBM Plex, SIL Open Font License)
	_ = mime.AddExtensionType(".woff2", "font/woff2")
}
