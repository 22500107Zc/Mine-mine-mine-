package saas

import (
	"embed"
	"fmt"
	"html/template"
	"math"
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
		"sub":         func(a, b int) int { return a - b },
		"spark":       sparkline,
		"hr":          fmtHours,
		"pmap":        processMap,
		"bars":        barChart,
		"line":        lineChart,
		"fmetric":     fmtMetric,
		"pct0":        func(v float64) string { return fmt.Sprintf("%.0f%%", v) },
		"f1":          func(v float64) string { return fmt.Sprintf("%.1f", v) },
		"def":         func(key string) string { return metricDef(key).Definition },
		"defLabel":    func(key string) string { return metricDef(key).Label },
		"scopeHidden": scopeHidden,
		"band":        bandClass,
		"statusClass": func(s string) string {
			switch s {
			case "good", "Achieved", "On track", "Associated improvement", "High", "met", "on time", "resolved":
				return "ok"
			case "warn", "At risk", "Moderate", "Rising", "Elevated", "in_review", "reopened", "Collecting data", "Measuring":
				return "warn"
			case "bad", "Off track", "Associated worsening", "Past target", "breached", "open breach", "open":
				return "bad"
			}
			return "neutral"
		},
		"issueLabel": func(s string) string {
			return map[string]string{"open": "Open", "in_review": "In review", "resolved": "Resolved", "reopened": "Reopened"}[s]
		},
		"barPct": func(v, max float64) string {
			if max <= 0 {
				return "0"
			}
			return fmt.Sprintf("%.1f", v/max*100)
		},
		"barPctI": func(v, max int) string {
			if max <= 0 {
				return "0"
			}
			return fmt.Sprintf("%.1f", float64(v)/float64(max)*100)
		},
		"absH": func(v float64) string {
			if v < 0 {
				return "−" + fmtHours(-v)
			}
			return "+" + fmtHours(v)
		},
		"signed": func(v float64, unit string) string {
			s := "+"
			if v < 0 {
				s, v = "−", -v
			}
			return s + fmtMetric(v, unit)
		},
		"heatLevel": func(c, mx int) int {
			if c == 0 || mx == 0 {
				return 0
			}
			r := float64(c) / float64(mx)
			switch {
			case r <= .25:
				return 1
			case r <= .5:
				return 2
			case r <= .75:
				return 3
			}
			return 4
		},
		"hourRange": func() []int {
			out := make([]int, 24)
			for i := range out {
				out[i] = i
			}
			return out
		},
		"wdName":        func(i int) string { return []string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"}[i] },
		"u64":           func(v uint64) string { return fmt.Sprint(v) },
		"join":          strings.Join,
		"issueStatuses": func() []string { return issueStatuses },
		"list":          func(ss ...string) []string { return ss },
		"mod":           func(a, b int) int { return a % b },
		"subf":          func(a, b float64) float64 { return a - b },
		"absf":          math.Abs,
		"deref": func(p *float64) float64 {
			if p == nil {
				return 0
			}
			return *p
		},
		"waitShare": func(wait, work float64) float64 {
			if wait+work <= 0 {
				return 0
			}
			return wait / (wait + work) * 100
		},
		"maxAbs": func(dd []Driver) float64 {
			m := 0.0
			for _, d := range dd {
				m = math.Max(m, math.Abs(d.DeltaH))
			}
			return m
		},
		"topRow": func(ps ProcessStages) *StageRow {
			var best *StageRow
			for i := range ps.Rows {
				r := &ps.Rows[i]
				if r.Enough && (best == nil || r.Severity > best.Severity) {
					best = r
				}
			}
			return best
		},
		"testMetricFor": func(m string) string {
			if _, ok := goalMetric(m); ok {
				return m
			}
			return "cycle_median"
		},
		"actLink": func(a ActivityHistory, view string) string {
			return activityURL(a, view, a.Filter)
		},
		"actFilter": func(a ActivityHistory, filter string) string {
			return activityURL(a, a.View, filter)
		},
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
