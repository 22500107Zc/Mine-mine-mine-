package saas

import (
	"bytes"
	"crypto/subtle"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/httprate"
	"go.uber.org/zap"
)

type (
	httpRequest = *http.Request

	flash struct {
		Kind string
		Text string
	}

	pageData map[string]any
)

const (
	csrfCookie    = "culpos_csrf"
	founderCookie = "culpos_founder"
	flashCookie   = "culpos_flash"
)

var (
	// Version is set by the application for display in the Founder console
	Version = "dev"

	// LicenseText holds the Apache-2.0 license text displayed on the
	// Open Source Notices page (loaded from LICENSE at boot when available)
	LicenseText = ""
)

// MountRoutes mounts all CulpOS commercial routes
func (svc *Service) MountRoutes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(securityHeaders(svc.cfg.SecureCookies, svc.extraFormOrigins()))

		static, _ := fs.Sub(staticFS, "assets/static")
		r.Handle("/culpos/static/*", http.StripPrefix("/culpos/static/", cacheFor(24*time.Hour, http.FileServer(http.FS(static)))))

		// Stripe webhook: authenticated by signature, not by CSRF/session
		r.With(httprate.LimitByIP(600, time.Minute)).Post("/stripe/webhook", svc.webhookHandler)

		r.Group(func(r chi.Router) {
			r.Use(noStore)

			r.Get("/legal/terms", svc.staticPage("legal-terms", "Terms of Service", "/legal/terms"))
			r.Get("/legal/privacy", svc.staticPage("legal-privacy", "Privacy Policy", "/legal/privacy"))
			r.Get("/legal/open-source", svc.ossPage)
			r.Get("/support", svc.supportPage)

			r.Get("/account/disabled", svc.statusPage(http.StatusForbidden, "Access unavailable", "Access to this workspace is currently unavailable. Please contact your company administrator or support.", "", ""))
			r.Get("/account/unavailable", svc.statusPage(http.StatusServiceUnavailable, "Temporarily unavailable", "We could not verify your account right now. Please try again in a moment.", "/", "Try again"))
			r.Get("/subscription-required", svc.statusPage(http.StatusPaymentRequired, "Subscription required", "An active subscription is required to use this workspace. Your company data is retained.", "/billing", "Go to Billing"))
			r.Get("/payment-required", svc.statusPage(http.StatusPaymentRequired, "Payment required", "Your latest payment needs attention. Update your payment method to restore access.", "/billing", "Update Payment Method"))
			r.Get("/maintenance", svc.statusPage(http.StatusServiceUnavailable, "Scheduled maintenance", "We’re performing scheduled maintenance. Please check back shortly.", "/", "Retry"))

			// Paid signup
			r.Get("/signup", svc.signupForm)
			r.With(httprate.LimitByIP(20, time.Hour)).Post("/signup", svc.signupProc)
			r.Get("/signup/checkout", svc.checkoutForm)
			r.With(httprate.LimitByIP(30, time.Hour)).Post("/signup/checkout", svc.checkoutProc)
			r.Get("/signup/complete", svc.signupComplete)

			// Customer (company) pages — identity from the application sign-in session
			r.Get("/billing", svc.billingPage)
			r.With(httprate.LimitByIP(30, time.Minute)).Post("/billing/{action}", svc.billingAction)
			r.Get("/company", svc.companyPage)
			r.With(httprate.LimitByIP(60, time.Minute)).Post("/company/invite", svc.companyInvite)
			r.With(httprate.LimitByIP(60, time.Minute)).Post("/company/members/{userID}/{action}", svc.companyMemberAction)

			// Founder console
			svc.mountFounder(r)
		})
	})
}

// render executes a page template with common data
func (svc *Service) render(w http.ResponseWriter, r *http.Request, status int, name string, d pageData) {
	if d == nil {
		d = pageData{}
	}

	if _, ok := d["Path"]; !ok {
		d["Path"] = r.URL.Path
	}

	if _, ok := d["Nav"]; !ok {
		d["Nav"] = "public"
	}

	if _, ok := d["CSRF"]; !ok {
		d["CSRF"] = svc.csrfToken(w, r)
	}

	d["Year"] = svc.now().Year()
	d["Flash"] = svc.popFlash(w, r)

	buf := &bytes.Buffer{}
	if err := svc.tpl.ExecuteTemplate(buf, name, d); err != nil {
		svc.log.Error("template execution failed", zap.String("template", name), zap.Error(err))
		svc.renderError(w, r, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// renderError renders a sanitized error page (no stack traces, paths, SQL or env values)
func (svc *Service) renderError(w http.ResponseWriter, r *http.Request, status int) {
	var (
		heading = "Something went wrong"
		msg     = "An unexpected error occurred. Please try again. If the problem persists, contact support."
		d       = pageData{"Year": svc.now().Year(), "Nav": "public", "Path": r.URL.Path, "Code": status, "ActionURL": "/", "ActionLabel": "Go to " + svc.cfg.Brand.ProductName, "NoIndex": true}
	)

	switch status {
	case http.StatusNotFound:
		heading, msg = "Page not found", "The page you’re looking for doesn’t exist or has moved."
	case http.StatusForbidden:
		heading, msg = "Access denied", "You don’t have permission to view this page."
	case http.StatusTooManyRequests:
		heading, msg = "Too many requests", "Please wait a moment and try again."
	}

	d["Title"], d["Heading"], d["Message"] = heading, heading, msg

	buf := &bytes.Buffer{}
	if err := svc.tpl.ExecuteTemplate(buf, "status", d); err != nil {
		http.Error(w, heading, status)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

func (svc *Service) internalError(w http.ResponseWriter, r *http.Request, err error) {
	svc.log.Error("request failed", zap.String("path", r.URL.Path), zap.Error(err))
	svc.renderError(w, r, http.StatusInternalServerError)
}

func (svc *Service) staticPage(name, title, path string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		svc.render(w, r, http.StatusOK, name, pageData{"Title": title, "Path": path, "MainClass": "medium"})
	}
}

func (svc *Service) ossPage(w http.ResponseWriter, r *http.Request) {
	svc.render(w, r, http.StatusOK, "legal-oss", pageData{"Title": "Open Source Notices", "MainClass": "medium", "License": LicenseText})
}

func (svc *Service) supportPage(w http.ResponseWriter, r *http.Request) {
	msg := "Contact your company administrator for help with your workspace."
	if svc.cfg.Brand.SupportEmail != "" {
		msg = "Our team is here to help. Email " + svc.cfg.Brand.SupportEmail + " and include your company name."
	}

	d := pageData{"Title": "Support", "Heading": "Support", "Message": msg}
	if svc.cfg.Brand.SupportEmail != "" {
		d["ActionURL"], d["ActionLabel"] = "mailto:"+svc.cfg.Brand.SupportEmail, "Email Support"
	}

	svc.render(w, r, http.StatusOK, "status", d)
}

func (svc *Service) statusPage(status int, heading, msg, actionURL, actionLabel string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		svc.render(w, r, status, "status", pageData{
			"Title": heading, "Heading": heading, "Message": msg,
			"ActionURL": actionURL, "ActionLabel": actionLabel, "NoIndex": true,
		})
	}
}

// NotFound renders the branded 404 page
func (svc *Service) NotFound(w http.ResponseWriter, r *http.Request) {
	svc.renderError(w, r, http.StatusNotFound)
}

// CSRF (double submit cookie; cookie is HttpOnly + SameSite=Strict) -------

func (svc *Service) csrfToken(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie(csrfCookie); err == nil && len(c.Value) >= 32 && len(c.Value) < 100 {
		return c.Value
	}

	tok, err := randomToken(32)
	if err != nil {
		return ""
	}

	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookie,
		Value:    tok,
		Path:     "/",
		HttpOnly: true,
		Secure:   svc.cfg.SecureCookies,
		SameSite: http.SameSiteStrictMode,
	})

	// make it available to the current render
	r.AddCookie(&http.Cookie{Name: csrfCookie, Value: tok})
	return tok
}

func (svc *Service) validCSRF(r *http.Request) bool {
	c, err := r.Cookie(csrfCookie)
	if err != nil || len(c.Value) < 32 {
		return false
	}

	return subtle.ConstantTimeCompare([]byte(c.Value), []byte(r.PostFormValue("csrf"))) == 1
}

// Flash messages survive exactly one redirect --------------------------

func (svc *Service) setFlash(w http.ResponseWriter, kind, text string) {
	http.SetCookie(w, &http.Cookie{
		Name:     flashCookie,
		Value:    kind + ":" + encodeFlash(text),
		Path:     "/",
		HttpOnly: true,
		Secure:   svc.cfg.SecureCookies,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   60,
	})
}

func (svc *Service) popFlash(w http.ResponseWriter, r *http.Request) []flash {
	c, err := r.Cookie(flashCookie)
	if err != nil || c.Value == "" {
		return nil
	}

	http.SetCookie(w, &http.Cookie{Name: flashCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: svc.cfg.SecureCookies, SameSite: http.SameSiteStrictMode})

	kind, text := splitFlash(c.Value)
	switch kind {
	case "success", "error", "warning", "info":
	default:
		return nil
	}

	return []flash{{Kind: kind, Text: text}}
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}

	return host
}

// extraFormOrigins allows redirects to a non-default Stripe endpoint (local
// Stripe mocks in development); production uses Stripe's hosted pages only
func (svc *Service) extraFormOrigins() string {
	base := svc.cfg.StripeAPIBase
	if base == "" || strings.HasPrefix(base, "https://api.stripe.com") {
		return ""
	}

	return " " + base
}

func securityHeaders(secure bool, extraFormOrigins string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("X-Frame-Options", "DENY")
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("Referrer-Policy", "same-origin")
			h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; form-action 'self' https://checkout.stripe.com https://billing.stripe.com"+extraFormOrigins+"; frame-ancestors 'none'; base-uri 'self'")
			if secure {
				h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
			}
			next.ServeHTTP(w, r)
		})
	}
}

func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func cacheFor(d time.Duration, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age="+itoa(int(d.Seconds())))
		next.ServeHTTP(w, r)
	})
}

// LoadLicenseText tries to read the Apache-2.0 LICENSE shipped with the build
func LoadLicenseText(paths ...string) {
	for _, p := range paths {
		if b, err := os.ReadFile(p); err == nil && len(b) > 0 {
			LicenseText = string(b)
			return
		}
	}

	LicenseText = "Licensed under the Apache License, Version 2.0 (the \"License\"); you may not use this file except in compliance with the License. You may obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0"
}

func isUserError(err error) (string, bool) {
	var ue UserError
	if errors.As(err, &ue) {
		return ue.msg, true
	}

	return "", false
}
