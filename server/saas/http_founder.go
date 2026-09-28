package saas

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/httprate"
)

type (
	founderCtxKey struct{}

	founderAuth struct {
		founder *Founder
		session *FounderSession
	}

	// FounderUserRow is a user as displayed in the Founder console
	FounderUserRow struct {
		UserID      uint64
		Name        string
		Email       string
		CompanyID   uint64
		CompanyName string
		Role        CompanyRole
		Suspended   bool
		CreatedAt   time.Time
	}
)

// MailConfigured reports whether outgoing email is configured (set by the app)
var MailConfigured = func() bool { return false }

func (svc *Service) mountFounder(r chi.Router) {
	r.Get("/founder", svc.founderLoginForm)
	r.With(httprate.LimitByIP(10, time.Minute)).Post("/founder", svc.founderLoginProc)

	r.Group(func(r chi.Router) {
		r.Use(svc.founderOnly)

		r.Get("/founder/dashboard", svc.founderDashboard)
		r.Get("/founder/companies", svc.founderCompanies)
		r.Get("/founder/companies/{companyID}", svc.founderCompany)
		r.Post("/founder/companies/{companyID}/{action}", svc.founderCompanyAction)
		r.Get("/founder/users", svc.founderUsers)
		r.Post("/founder/users/{userID}/{action}", svc.founderUserAction)
		r.Get("/founder/billing", svc.founderBilling)
		r.Get("/founder/audit", svc.founderAudit)
		r.Get("/founder/system", svc.founderSystem)
		r.Get("/founder/account", svc.founderAccount)
		r.Post("/founder/account/password", svc.founderPassword)
		r.Post("/founder/logout", svc.founderLogout)
	})
}

func (svc *Service) setFounderCookie(w http.ResponseWriter, token string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     founderCookie,
		Value:    token,
		Path:     "/founder",
		HttpOnly: true,
		Secure:   svc.cfg.SecureCookies,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   maxAge,
	})
}

func (svc *Service) founderLoginForm(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(founderCookie); err == nil {
		if _, _, err = svc.FounderSession(r.Context(), c.Value); err == nil {
			http.Redirect(w, r, "/founder/dashboard", http.StatusSeeOther)
			return
		}
	}

	svc.render(w, r, http.StatusOK, "founder-login", pageData{"Title": "Founder", "Nav": "founder", "MainClass": "narrow", "NoIndex": true})
}

func (svc *Service) founderLoginProc(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := r.ParseForm(); err != nil || !svc.validCSRF(r) {
		svc.render(w, r, http.StatusForbidden, "founder-login", pageData{"Title": "Founder", "Nav": "founder", "MainClass": "narrow", "NoIndex": true,
			"Error": "Your session expired. Please try again."})
		return
	}

	token, ses, err := svc.FounderLogin(r.Context(), r.PostFormValue("username"), r.PostFormValue("password"), clientIP(r), r.UserAgent())
	if err != nil {
		if !errors.Is(err, ErrInvalidCredentials) {
			svc.log.Error("founder login error")
		}

		// one generic message for every failure mode
		svc.render(w, r, http.StatusUnauthorized, "founder-login", pageData{"Title": "Founder", "Nav": "founder", "MainClass": "narrow", "NoIndex": true,
			"Error": "Invalid username or password."})
		return
	}

	svc.setFounderCookie(w, token, int(time.Until(ses.ExpiresAt).Seconds()))
	http.Redirect(w, r, "/founder/dashboard", http.StatusSeeOther)
}

// founderOnly enforces a valid, unexpired Founder session server-side and
// CSRF tokens bound to that session for every state-changing request
func (svc *Service) founderOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(founderCookie)
		if err != nil {
			http.Redirect(w, r, "/founder", http.StatusSeeOther)
			return
		}

		f, ses, err := svc.FounderSession(r.Context(), c.Value)
		if err != nil {
			svc.setFounderCookie(w, "", -1)
			http.Redirect(w, r, "/founder", http.StatusSeeOther)
			return
		}

		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
			if err = r.ParseForm(); err != nil || !ses.ValidCSRF(r.PostFormValue("csrf")) {
				svc.audit(r.Context(), founderActor(f, clientIP(r)).with("founder.csrf", r.URL.Path, ResultDenied, nil))
				svc.renderError(w, r, http.StatusForbidden)
				return
			}
		}

		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), founderCtxKey{}, &founderAuth{f, ses})))
	})
}

func founderFrom(r *http.Request) *founderAuth {
	fa, _ := r.Context().Value(founderCtxKey{}).(*founderAuth)
	return fa
}

func (svc *Service) founderPage(r *http.Request, title, section string) pageData {
	fa := founderFrom(r)
	return pageData{
		"Title":   title,
		"Nav":     "founder",
		"Section": section,
		"Founder": fa.founder,
		"Session": fa.session,
		"CSRF":    fa.session.CSRFToken,
		"NoIndex": true,
		"Now":     svc.now(),
	}
}

func (svc *Service) founderDashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m, err := svc.repo.Metrics(ctx, svc.now(), svc.cfg.Brand.PriceCents)
	if err != nil {
		svc.internalError(w, r, err)
		return
	}

	recent, _ := svc.repo.SearchCompanies(ctx, CompanyFilter{Limit: 10})
	canceled, _ := svc.repo.SearchCompanies(ctx, CompanyFilter{Status: string(SubCanceled), Limit: 10})
	payments, _ := svc.repo.Payments(ctx, 0, "paid", 10)
	failed, _ := svc.repo.Payments(ctx, 0, "failed", 10)
	audit, _ := svc.repo.Audit(ctx, 0, "", 12, 0)
	audit = svc.labelAudit(ctx, audit)

	// companies set to cancel at period end are "cancellations" too
	pending, _ := svc.repo.SearchCompanies(ctx, CompanyFilter{Status: string(SubActive), Limit: 200})
	listed := map[uint64]bool{}
	for _, c := range canceled {
		listed[c.ID] = true
	}
	for _, c := range pending {
		if c.CancelAtPeriodEnd && c.SubscriptionStatus == SubActive && !listed[c.ID] {
			listed[c.ID] = true
			canceled = append(canceled, c)
		}
	}

	d := svc.founderPage(r, "Founder", "dashboard")
	d["M"] = m
	d["Recent"] = recent
	d["Canceled"] = canceled
	d["Payments"] = payments
	d["Failed"] = failed
	d["Audit"] = audit
	svc.systemStatus(ctx, d)
	svc.render(w, r, http.StatusOK, "founder-dashboard", d)
}

func (svc *Service) systemStatus(ctx context.Context, d pageData) {
	d["Version"] = Version
	d["DBOK"] = svc.repo.Ping(ctx) == nil
	d["StripeConfigured"] = svc.cfg.StripeConfigured()
	d["StripeMissing"] = svc.cfg.StripeMissing()
	d["MailConfigured"] = MailConfigured()
	last, failed, _ := svc.repo.LastStripeEventAt(ctx)
	d["LastWebhook"] = last
	d["WebhookFailures"] = failed
}

func pageParam(r *http.Request) int {
	p, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if p < 1 {
		p = 1
	}

	return p
}

func (svc *Service) founderCompanies(w http.ResponseWriter, r *http.Request) {
	const perPage = 50
	var (
		page   = pageParam(r)
		query  = strings.TrimSpace(r.URL.Query().Get("q"))
		status = r.URL.Query().Get("status")
	)

	if len(query) > 100 {
		query = query[:100]
	}

	cc, err := svc.repo.SearchCompanies(r.Context(), CompanyFilter{Query: query, Status: status, Limit: perPage + 1, Offset: (page - 1) * perPage})
	if err != nil {
		svc.internalError(w, r, err)
		return
	}

	hasMore := len(cc) > perPage
	if hasMore {
		cc = cc[:perPage]
	}

	d := svc.founderPage(r, "Founder · Companies", "companies")
	d["Companies"] = cc
	d["Query"] = query
	d["Status"] = status
	d["Page"] = page
	d["HasMore"] = hasMore
	d["Statuses"] = []string{"active", "pending", "disabled", "past_due", "canceled", "unpaid", "incomplete", "incomplete_expired"}
	svc.render(w, r, http.StatusOK, "founder-companies", d)
}

func (svc *Service) founderCompanyByParam(w http.ResponseWriter, r *http.Request) (*Company, bool) {
	id, err := strconv.ParseUint(chi.URLParam(r, "companyID"), 10, 64)
	if err != nil {
		svc.renderError(w, r, http.StatusNotFound)
		return nil, false
	}

	c, err := svc.repo.CompanyByID(r.Context(), id)
	if errors.Is(err, ErrNotFound) {
		svc.renderError(w, r, http.StatusNotFound)
		return nil, false
	}

	if err != nil {
		svc.internalError(w, r, err)
		return nil, false
	}

	return c, true
}

func (svc *Service) founderCompany(w http.ResponseWriter, r *http.Request) {
	c, ok := svc.founderCompanyByParam(w, r)
	if !ok {
		return
	}

	ctx := r.Context()
	members, err := svc.MembersWithInfo(ctx, c.ID)
	if err != nil {
		svc.internalError(w, r, err)
		return
	}

	rows := make([]FounderUserRow, 0, len(members))
	for _, m := range members {
		rows = append(rows, FounderUserRow{UserID: m.UserID, Name: m.Name, Email: m.Email, CompanyID: c.ID, CompanyName: c.Name, Role: m.Role, Suspended: m.Suspended, CreatedAt: m.CreatedAt})
	}

	payments, _ := svc.repo.Payments(ctx, c.ID, "", 24)
	events, _ := svc.repo.RecentStripeEvents(ctx, c.ID, 25)
	audit, _ := svc.repo.Audit(ctx, c.ID, "", 25, 0)
	audit = svc.labelAudit(ctx, audit)

	dec := Evaluate(c, svc.now())
	label := map[AccessLevel]string{AccessFull: "Full access", AccessBillingOnly: "Billing & recovery only", AccessNone: "Disabled"}[dec.Level]

	roleCounts := map[CompanyRole]int{}
	for _, m := range members {
		roleCounts[m.Role]++
	}

	d := svc.founderPage(r, "Founder · "+c.Name, "companies")
	d["Company"] = c
	d["RoleCounts"] = []struct {
		Role  CompanyRole
		Count int
	}{{RoleOwner, roleCounts[RoleOwner]}, {RoleAdministrator, roleCounts[RoleAdministrator]}, {RoleManager, roleCounts[RoleManager]}, {RoleEmployee, roleCounts[RoleEmployee]}}
	d["Workspace"] = WorkspacePath(c)
	d["Users"] = rows
	d["Payments"] = payments
	d["Events"] = events
	d["Audit"] = audit
	d["AccessLabel"] = label
	svc.render(w, r, http.StatusOK, "founder-company", d)
}

func (svc *Service) founderCompanyAction(w http.ResponseWriter, r *http.Request) {
	c, ok := svc.founderCompanyByParam(w, r)
	if !ok {
		return
	}

	var (
		ctx = r.Context()
		fa  = founderFrom(r)
		ip  = clientIP(r)
		err error
	)

	switch chi.URLParam(r, "action") {
	case "disable":
		err = svc.SetCompanyEnabled(ctx, fa.founder, c.ID, false, ip)
		if err == nil {
			svc.setFlash(w, "success", "Company disabled.")
		}
	case "enable":
		err = svc.SetCompanyEnabled(ctx, fa.founder, c.ID, true, ip)
		if err == nil {
			svc.setFlash(w, "success", "Company enabled.")
		}
	case "provision":
		err = svc.Provision(ctx, c.ID)
		svc.audit(ctx, founderActor(fa.founder, ip).with("company.provision.retry", c.Name, resultOf(err), nil))
		if err == nil {
			svc.setFlash(w, "success", "Provisioning completed.")
		}
	case "sync":
		err = svc.SyncFromStripe(ctx, c)
		svc.audit(ctx, founderActor(fa.founder, ip).with("company.billing.sync", c.Name, resultOf(err), nil))
		if err == nil {
			svc.setFlash(w, "success", "Subscription refreshed from Stripe.")
		}
	default:
		svc.renderError(w, r, http.StatusNotFound)
		return
	}

	if err != nil {
		svc.log.Error("founder company action failed")
		svc.setFlash(w, "error", "The action could not be completed. See the audit log for details.")
	}

	http.Redirect(w, r, "/founder/companies/"+strconv.FormatUint(c.ID, 10), http.StatusSeeOther)
}

// SyncFromStripe refreshes subscription state directly from the Stripe API
func (svc *Service) SyncFromStripe(ctx context.Context, c *Company) error {
	if c.StripeSubscriptionID == "" {
		return userErr("No subscription on record.")
	}

	sub, err := svc.stripe.GetSubscription(ctx, c.StripeSubscriptionID)
	if err != nil {
		return err
	}

	return svc.applySubscription(ctx, c, sub, "")
}

func resultOf(err error) string {
	if err != nil {
		return ResultFailure
	}

	return ResultSuccess
}

func (svc *Service) founderUsers(w http.ResponseWriter, r *http.Request) {
	const perPage = 100
	ctx := r.Context()
	page := pageParam(r)

	mm, err := svc.repo.AllMembers(ctx, perPage+1, (page-1)*perPage)
	if err != nil {
		svc.internalError(w, r, err)
		return
	}

	hasMore := len(mm) > perPage
	if hasMore {
		mm = mm[:perPage]
	}

	ids := make([]uint64, len(mm))
	for i, m := range mm {
		ids[i] = m.UserID
	}

	info, err := svc.platform.Users(ctx, ids...)
	if err != nil {
		svc.internalError(w, r, err)
		return
	}

	names := map[uint64]string{}
	rows := make([]FounderUserRow, 0, len(mm))
	for _, m := range mm {
		if _, ok := names[m.CompanyID]; !ok {
			if c, err := svc.repo.CompanyByID(ctx, m.CompanyID); err == nil {
				names[m.CompanyID] = c.Name
			}
		}

		u := info[m.UserID]
		rows = append(rows, FounderUserRow{UserID: m.UserID, Name: u.Name, Email: u.Email, CompanyID: m.CompanyID, CompanyName: names[m.CompanyID], Role: m.Role, Suspended: u.Suspended, CreatedAt: m.CreatedAt})
	}

	d := svc.founderPage(r, "Founder · Users", "users")
	d["Users"] = rows
	d["Page"] = page
	d["HasMore"] = hasMore
	svc.render(w, r, http.StatusOK, "founder-users", d)
}

func (svc *Service) founderUserAction(w http.ResponseWriter, r *http.Request) {
	uid, err := strconv.ParseUint(chi.URLParam(r, "userID"), 10, 64)
	if err != nil {
		svc.renderError(w, r, http.StatusNotFound)
		return
	}

	// Founder user management is limited to company users
	m, err := svc.repo.MemberByUser(r.Context(), uid)
	if err != nil {
		svc.renderError(w, r, http.StatusNotFound)
		return
	}

	var (
		fa = founderFrom(r)
		ip = clientIP(r)
	)

	switch chi.URLParam(r, "action") {
	case "disable":
		err = svc.SetUserEnabled(r.Context(), fa.founder, uid, false, ip)
	case "enable":
		err = svc.SetUserEnabled(r.Context(), fa.founder, uid, true, ip)
	case "reset":
		err = svc.ResetUserAccess(r.Context(), fa.founder, uid, ip)
	default:
		svc.renderError(w, r, http.StatusNotFound)
		return
	}

	svc.cache.invalidateUser(uid)
	if err != nil {
		svc.setFlash(w, "error", "The action could not be completed.")
	} else {
		svc.setFlash(w, "success", "User updated.")
	}

	// only redirect back within the Founder console (no open redirects)
	back := localPath(r.Referer(), r.Host, "")
	if !strings.HasPrefix(back, "/founder/") {
		back = "/founder/companies/" + strconv.FormatUint(m.CompanyID, 10)
	}

	http.Redirect(w, r, back, http.StatusSeeOther)
}

func (svc *Service) founderBilling(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m, err := svc.repo.Metrics(ctx, svc.now(), svc.cfg.Brand.PriceCents)
	if err != nil {
		svc.internalError(w, r, err)
		return
	}

	payments, _ := svc.repo.Payments(ctx, 0, "paid", 50)
	failed, _ := svc.repo.Payments(ctx, 0, "failed", 50)
	pastDue, _ := svc.repo.SearchCompanies(ctx, CompanyFilter{Status: string(SubPastDue), Limit: 100})
	unpaid, _ := svc.repo.SearchCompanies(ctx, CompanyFilter{Status: string(SubUnpaid), Limit: 100})
	events, _ := svc.repo.RecentStripeEvents(ctx, 0, 50)

	d := svc.founderPage(r, "Founder · Billing", "billing")
	d["M"] = m
	d["Payments"] = payments
	d["Failed"] = failed
	d["AtRisk"] = append(pastDue, unpaid...)
	d["Events"] = events
	svc.render(w, r, http.StatusOK, "founder-billing", d)
}

func (svc *Service) founderAudit(w http.ResponseWriter, r *http.Request) {
	const perPage = 100
	var (
		q         = r.URL.Query()
		page      = pageParam(r)
		action    = strings.TrimSpace(q.Get("action"))
		companyID uint64
	)

	if len(action) > 60 {
		action = action[:60]
	}

	if v := q.Get("company"); v != "" {
		companyID, _ = strconv.ParseUint(v, 10, 64)
	}

	ee, err := svc.repo.Audit(r.Context(), companyID, action, perPage+1, (page-1)*perPage)
	if err != nil {
		svc.internalError(w, r, err)
		return
	}

	hasMore := len(ee) > perPage
	if hasMore {
		ee = ee[:perPage]
	}

	d := svc.founderPage(r, "Founder · Audit Log", "audit")
	d["Audit"] = svc.labelAudit(r.Context(), ee)
	d["Action"] = action
	d["CompanyID"] = companyID
	d["Page"] = page
	d["HasMore"] = hasMore
	if companyID == 0 {
		d["CompanyID"] = ""
	}
	svc.render(w, r, http.StatusOK, "founder-audit", d)
}

func (svc *Service) founderSystem(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	events, _ := svc.repo.RecentStripeEvents(ctx, 0, 25)
	all, _ := svc.repo.Audit(ctx, 0, "", 200, 0)

	var errs []*AuditEntry
	for _, e := range all {
		if e.Result == ResultFailure && e.ActorType != ActorFounder && e.ActorType != ActorPublic {
			errs = append(errs, e)
		}
	}

	d := svc.founderPage(r, "Founder · System", "system")
	d["Events"] = events
	d["Errors"] = svc.labelAudit(ctx, errs)
	svc.systemStatus(ctx, d)
	svc.render(w, r, http.StatusOK, "founder-system", d)
}

func (svc *Service) founderAccount(w http.ResponseWriter, r *http.Request) {
	svc.render(w, r, http.StatusOK, "founder-account", svc.founderPage(r, "Founder · Account", "account"))
}

func (svc *Service) founderPassword(w http.ResponseWriter, r *http.Request) {
	fa := founderFrom(r)
	err := svc.FounderChangePassword(r.Context(), fa.founder, r.PostFormValue("current"), r.PostFormValue("next"), clientIP(r))
	if err != nil {
		d := svc.founderPage(r, "Founder · Account", "account")
		if msg, ok := isUserError(err); ok {
			d["Error"] = msg
		} else {
			d["Error"] = "Password could not be changed."
		}
		svc.render(w, r, http.StatusUnprocessableEntity, "founder-account", d)
		return
	}

	// all sessions were revoked; sign in again with the new password
	svc.setFounderCookie(w, "", -1)
	http.Redirect(w, r, "/founder", http.StatusSeeOther)
}

func (svc *Service) founderLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(founderCookie); err == nil {
		svc.FounderLogout(r.Context(), c.Value, clientIP(r))
	}

	svc.setFounderCookie(w, "", -1)
	http.Redirect(w, r, "/founder", http.StatusSeeOther)
}
