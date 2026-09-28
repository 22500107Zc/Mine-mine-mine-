package saas

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

var onboardingSteps = []string{"Company Profile", "Invite Team", "Add First Customer", "Create First Task", "Finish"}

func (svc *Service) welcomePage(w http.ResponseWriter, r *http.Request) {
	cc, ok := svc.customer(w, r)
	if !ok {
		return
	}

	if cc.Decision.Level != AccessFull {
		http.Redirect(w, r, "/billing", http.StatusSeeOther)
		return
	}

	if !cc.Member.Role.CanManageMembers() {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	step, _ := strconv.Atoi(r.URL.Query().Get("step"))
	if step < 1 || step > len(onboardingSteps) {
		step = 1
	}

	members, _ := svc.MembersWithInfo(r.Context(), cc.Company.ID)

	var assignable []CompanyRole
	for _, role := range []CompanyRole{RoleAdministrator, RoleManager, RoleEmployee} {
		if cc.Member.Role.CanAssign(role) {
			assignable = append(assignable, role)
		}
	}

	d := svc.customerPage(cc, "Welcome", "welcome")
	d["Step"] = step
	d["Steps"] = onboardingSteps
	d["Next"] = step + 1
	d["Members"] = members
	d["AssignableRoles"] = assignable
	d["MainClass"] = "medium"
	svc.render(w, r, http.StatusOK, "welcome", d)
}

func (svc *Service) welcomeAction(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil || !svc.validCSRF(r) {
		svc.renderError(w, r, http.StatusForbidden)
		return
	}

	cc, ok := svc.customer(w, r)
	if !ok {
		return
	}

	var (
		ctx  = r.Context()
		ip   = clientIP(r)
		err  error
		next string
	)

	switch chi.URLParam(r, "action") {
	case "profile":
		err = svc.UpdateCompanyProfile(ctx, cc.UserID, CompanyProfile{
			Name: r.PostFormValue("name"), Industry: r.PostFormValue("industry"), Website: r.PostFormValue("website"),
			Phone: r.PostFormValue("phone"), Address: r.PostFormValue("address"),
		}, ip)
		next = "/welcome?step=2"

	case "invite":
		role, valid := ParseCompanyRole(r.PostFormValue("role"))
		if !valid {
			err = userErr("Choose a valid role.")
		} else if err = svc.Invite(ctx, cc.UserID, r.PostFormValue("email"), r.PostFormValue("name"), role, ip); err == nil {
			svc.setFlash(w, "success", "Invitation sent. Invite more people or continue.")
		}
		next = "/welcome?step=2"

	case "customer":
		err = svc.AddFirstRecord(ctx, cc.UserID, "customer", map[string]string{
			"Name": r.PostFormValue("name"), "Email": r.PostFormValue("email"), "Phone": r.PostFormValue("phone"),
		}, ip)
		if err == nil {
			svc.setFlash(w, "success", "Customer added to your workspace.")
		}
		next = "/welcome?step=4"

	case "task":
		err = svc.AddFirstRecord(ctx, cc.UserID, "task", map[string]string{
			"Title": r.PostFormValue("title"), "DueDate": r.PostFormValue("dueDate"),
		}, ip)
		if err == nil {
			svc.setFlash(w, "success", "Task created and assigned to you.")
		}
		next = "/welcome?step=5"

	case "finish":
		err = svc.FinishOnboarding(ctx, cc.UserID, ip)
		next = WorkspacePath(cc.Company)

	default:
		svc.renderError(w, r, http.StatusNotFound)
		return
	}

	if err != nil {
		svc.flashErr(w, err)
		next = localPath(r.Referer(), r.Host, "/welcome")
		if !strings.HasPrefix(next, "/welcome") {
			next = "/welcome"
		}
	}

	http.Redirect(w, r, next, http.StatusSeeOther)
}
