package saas

import (
	"errors"
	"io"
	"net/http"

	"go.uber.org/zap"
)

func (svc *Service) signupForm(w http.ResponseWriter, r *http.Request) {
	svc.render(w, r, http.StatusOK, "signup", pageData{
		"Title":       "Create Company",
		"MainClass":   "narrow",
		"Form":        map[string]string{},
		"Unavailable": !svc.cfg.StripeConfigured(),
	})
}

func (svc *Service) signupProc(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil || !svc.validCSRF(r) {
		svc.renderError(w, r, http.StatusForbidden)
		return
	}

	form := map[string]string{
		"companyName": r.PostFormValue("companyName"),
		"firstName":   r.PostFormValue("firstName"),
		"lastName":    r.PostFormValue("lastName"),
		"email":       r.PostFormValue("email"),
	}

	url, _, err := svc.Signup(r.Context(), SignupInput{
		CompanyName: form["companyName"],
		FirstName:   form["firstName"],
		LastName:    form["lastName"],
		Email:       form["email"],
		Password:    r.PostFormValue("password"),
		IP:          clientIP(r),
	})

	if err != nil {
		msg, ok := isUserError(err)
		if !ok {
			svc.log.Error("signup failed", zap.Error(err))
			msg = "We could not create your company right now. Please try again."
		}

		svc.render(w, r, http.StatusUnprocessableEntity, "signup", pageData{
			"Title": "Create Company", "MainClass": "narrow", "Form": form, "Error": msg,
		})
		return
	}

	http.Redirect(w, r, url, http.StatusSeeOther)
}

func (svc *Service) companyFromRef(r *http.Request) (*Company, string, bool) {
	ref := r.FormValue("ref")
	id, ok := svc.parseRef(ref)
	if !ok {
		return nil, "", false
	}

	c, err := svc.repo.CompanyByID(r.Context(), id)
	if err != nil {
		return nil, "", false
	}

	return c, ref, true
}

func (svc *Service) checkoutForm(w http.ResponseWriter, r *http.Request) {
	c, ref, ok := svc.companyFromRef(r)
	if !ok {
		svc.renderError(w, r, http.StatusNotFound)
		return
	}

	if c.ProvisioningStatus == ProvProvisioned {
		http.Redirect(w, r, "/signup/complete?ref="+ref, http.StatusSeeOther)
		return
	}

	svc.render(w, r, http.StatusOK, "signup-checkout", pageData{"Title": "Checkout", "MainClass": "narrow", "Company": c, "Ref": ref, "NoIndex": true})
}

func (svc *Service) checkoutProc(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil || !svc.validCSRF(r) {
		svc.renderError(w, r, http.StatusForbidden)
		return
	}

	c, ref, ok := svc.companyFromRef(r)
	if !ok {
		svc.renderError(w, r, http.StatusNotFound)
		return
	}

	url, err := svc.StartCheckout(r.Context(), c)
	if err != nil {
		msg, ok := isUserError(err)
		if !ok {
			svc.log.Error("checkout failed", zap.Error(err))
			msg = "We could not start checkout. Please try again."
		}

		svc.render(w, r, http.StatusUnprocessableEntity, "signup-checkout", pageData{"Title": "Checkout", "MainClass": "narrow", "Company": c, "Ref": ref, "Error": msg, "NoIndex": true})
		return
	}

	http.Redirect(w, r, url, http.StatusSeeOther)
}

// signupComplete only displays status; it never grants access. Activation
// happens exclusively in the signature-verified webhook handler.
func (svc *Service) signupComplete(w http.ResponseWriter, r *http.Request) {
	c, _, ok := svc.companyFromRef(r)
	if !ok {
		svc.renderError(w, r, http.StatusNotFound)
		return
	}

	svc.render(w, r, http.StatusOK, "signup-complete", pageData{
		"Title":     "Welcome",
		"MainClass": "narrow",
		"Company":   c,
		"Ready":     c.ProvisioningStatus == ProvProvisioned && Evaluate(c, svc.now()).Level == AccessFull,
		"Failed":    c.ProvisioningStatus == ProvFailed,
		"NoIndex":   true,
	})
}

func (svc *Service) webhookHandler(w http.ResponseWriter, r *http.Request) {
	payload, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	err = svc.HandleWebhook(r.Context(), payload, r.Header.Get("Stripe-Signature"))
	switch {
	case err == nil:
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"received":true}`))
	case errors.Is(err, ErrInvalidSignature):
		http.Error(w, "invalid signature", http.StatusBadRequest)
	default:
		// generic message; details are in the server log and Founder console
		http.Error(w, "processing error", http.StatusInternalServerError)
	}
}
