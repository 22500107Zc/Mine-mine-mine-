package saas

import (
	"context"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
)

type customerCtx struct {
	UserID   uint64
	Company  *Company
	Member   *Member
	Decision AccessDecision
}

// customer resolves the signed-in application user and their company.
// Redirects to sign in when there is no session; platform staff without a
// company get a 404 on company pages.
func (svc *Service) customer(w http.ResponseWriter, r *http.Request) (*customerCtx, bool) {
	var uid uint64
	if svc.sessionUser != nil {
		uid = svc.sessionUser(r)
	}

	if uid == 0 {
		http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
		return nil, false
	}

	c, m, d, err := svc.AccessForUser(r.Context(), uid)
	if err != nil {
		svc.internalError(w, r, err)
		return nil, false
	}

	if c == nil || m == nil {
		svc.renderError(w, r, http.StatusNotFound)
		return nil, false
	}

	if d.Level == AccessNone {
		http.Redirect(w, r, "/account/disabled", http.StatusSeeOther)
		return nil, false
	}

	return &customerCtx{UserID: uid, Company: c, Member: m, Decision: d}, true
}

func (svc *Service) customerPage(cc *customerCtx, title, section string) pageData {
	d := pageData{
		"Title":      title,
		"Nav":        "customer",
		"Section":    section,
		"Company":    cc.Company,
		"Decision":   cc.Decision,
		"CanManage":  cc.Member.Role.CanManageMembers() && cc.Decision.Level == AccessFull,
		"CanBilling": cc.Member.Role.CanViewBilling(),
		"NoIndex":    true,
	}

	if cc.Decision.Level == AccessFull && cc.Company.ProvisioningStatus == ProvProvisioned {
		d["Workspace"] = WorkspacePath(cc.Company)
	}

	return d
}

// WorkspacePath returns the path of the company workspace in the web application
func WorkspacePath(c *Company) string {
	return "/compose/ns/" + c.Slug + "/pages"
}

func (svc *Service) billingPage(w http.ResponseWriter, r *http.Request) {
	cc, ok := svc.customer(w, r)
	if !ok {
		return
	}

	if !cc.Member.Role.CanViewBilling() {
		svc.renderError(w, r, http.StatusForbidden)
		return
	}

	payments, err := svc.repo.Payments(r.Context(), cc.Company.ID, "", 12)
	if err != nil {
		svc.internalError(w, r, err)
		return
	}

	d := svc.customerPage(cc, "Billing", "billing")
	d["Payments"] = payments
	d["CanManageSub"] = cc.Member.Role.CanManageSubscription()
	d["NeedsCheckout"] = cc.Company.StripeSubscriptionID == "" ||
		cc.Decision.Reason == "canceled" ||
		cc.Company.SubscriptionStatus == SubIncompleteExpired
	svc.render(w, r, http.StatusOK, "billing", d)
}

func (svc *Service) billingAction(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil || !svc.validCSRF(r) {
		svc.renderError(w, r, http.StatusForbidden)
		return
	}

	cc, ok := svc.customer(w, r)
	if !ok {
		return
	}

	if !cc.Member.Role.CanManageSubscription() {
		svc.audit(r.Context(), userActor(cc.UserID, cc.Member.Role, cc.Company.ID, clientIP(r)).
			with("billing."+chi.URLParam(r, "action"), cc.Company.Name, ResultDenied, nil))
		svc.renderError(w, r, http.StatusForbidden)
		return
	}

	var (
		ctx   = r.Context()
		c     = cc.Company
		actor = userActor(cc.UserID, cc.Member.Role, c.ID, clientIP(r))
	)

	switch chi.URLParam(r, "action") {
	case "checkout":
		url, err := svc.StartCheckout(ctx, c)
		if err != nil {
			svc.flashErr(w, err)
			break
		}
		http.Redirect(w, r, url, http.StatusSeeOther)
		return

	case "portal":
		if c.StripeCustomerID == "" {
			svc.setFlash(w, "error", "No billing account exists yet. Complete checkout first.")
			break
		}

		url, err := svc.stripe.CreatePortalSession(ctx, c.StripeCustomerID, svc.cfg.Brand.URL("/billing"))
		if err != nil {
			svc.log.Error("failed to create billing portal session", zap.Error(err))
			svc.setFlash(w, "error", "Billing management is temporarily unavailable. Please try again.")
			break
		}

		svc.audit(ctx, actor.with("billing.portal.open", c.Name, ResultSuccess, nil))
		http.Redirect(w, r, url, http.StatusSeeOther)
		return

	case "cancel", "resume":
		cancel := chi.URLParam(r, "action") == "cancel"
		if err := svc.setCancelAtPeriodEnd(ctx, c, cancel); err != nil {
			svc.log.Error("failed to change subscription cancellation", zap.Error(err))
			svc.setFlash(w, "error", "We could not update your subscription. Please try again.")
			svc.audit(ctx, actor.with("billing.subscription."+chi.URLParam(r, "action"), c.Name, ResultFailure, nil))
			break
		}

		svc.audit(ctx, actor.with("billing.subscription."+chi.URLParam(r, "action"), c.Name, ResultSuccess, nil))
		if cancel {
			svc.setFlash(w, "success", "Your subscription will end at the close of the current billing period.")
		} else {
			svc.setFlash(w, "success", "Your subscription will continue.")
		}

	default:
		svc.renderError(w, r, http.StatusNotFound)
		return
	}

	http.Redirect(w, r, "/billing", http.StatusSeeOther)
}

func (svc *Service) setCancelAtPeriodEnd(ctx context.Context, c *Company, cancel bool) error {
	if c.StripeSubscriptionID == "" {
		return userErr("There is no subscription to change.")
	}

	sub, err := svc.stripe.SetCancelAtPeriodEnd(ctx, c.StripeSubscriptionID, cancel)
	if err != nil {
		return err
	}

	// Persist immediately; the customer.subscription.updated webhook confirms it
	return svc.applySubscription(ctx, c, sub, "")
}

func (svc *Service) companyPage(w http.ResponseWriter, r *http.Request) {
	cc, ok := svc.customer(w, r)
	if !ok {
		return
	}

	members, err := svc.MembersWithInfo(r.Context(), cc.Company.ID)
	if err != nil {
		svc.internalError(w, r, err)
		return
	}

	var assignable []CompanyRole
	for _, role := range []CompanyRole{RoleAdministrator, RoleManager, RoleEmployee} {
		if cc.Member.Role.CanAssign(role) {
			assignable = append(assignable, role)
		}
	}

	d := svc.customerPage(cc, "Company Admin", "company")
	d["Members"] = members
	d["Me"] = cc.Member
	d["AssignableRoles"] = assignable
	svc.render(w, r, http.StatusOK, "company", d)
}

func (svc *Service) companyInvite(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil || !svc.validCSRF(r) {
		svc.renderError(w, r, http.StatusForbidden)
		return
	}

	cc, ok := svc.customer(w, r)
	if !ok {
		return
	}

	role, valid := ParseCompanyRole(r.PostFormValue("role"))
	if !valid {
		svc.setFlash(w, "error", "Choose a valid role.")
	} else if err := svc.Invite(r.Context(), cc.UserID, r.PostFormValue("email"), r.PostFormValue("name"), role, clientIP(r)); err != nil {
		svc.flashErr(w, err)
	} else {
		svc.setFlash(w, "success", "Invitation sent.")
	}

	http.Redirect(w, r, "/company", http.StatusSeeOther)
}

func (svc *Service) companyMemberAction(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil || !svc.validCSRF(r) {
		svc.renderError(w, r, http.StatusForbidden)
		return
	}

	cc, ok := svc.customer(w, r)
	if !ok {
		return
	}

	target, err := strconv.ParseUint(chi.URLParam(r, "userID"), 10, 64)
	if err != nil {
		svc.renderError(w, r, http.StatusNotFound)
		return
	}

	switch chi.URLParam(r, "action") {
	case "role":
		role, valid := ParseCompanyRole(r.PostFormValue("role"))
		if !valid {
			err = ErrNotAllowed
		} else {
			err = svc.ChangeRole(r.Context(), cc.UserID, target, role, clientIP(r))
		}
		if err == nil {
			svc.setFlash(w, "success", "Role updated.")
		}
	case "remove":
		if err = svc.RemoveMember(r.Context(), cc.UserID, target, clientIP(r)); err == nil {
			svc.setFlash(w, "success", "User removed from the company.")
		}
	default:
		svc.renderError(w, r, http.StatusNotFound)
		return
	}

	if err != nil {
		svc.flashErr(w, err)
	}

	http.Redirect(w, r, "/company", http.StatusSeeOther)
}

func (svc *Service) flashErr(w http.ResponseWriter, err error) {
	if msg, ok := isUserError(err); ok {
		svc.setFlash(w, "error", msg)
		return
	}

	svc.log.Error("customer action failed", zap.Error(err))
	svc.setFlash(w, "error", "Something went wrong. Please try again.")
}
