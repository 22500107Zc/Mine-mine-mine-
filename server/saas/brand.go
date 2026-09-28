// Package saas implements the St.Cloud~OS commercial layer: company accounts,
// Stripe subscriptions, subscription gating, tenant isolation and the
// platform-level Founder console.
//
// Copyright Culp Industries. Portions of the surrounding application are
// derived from Apache-2.0 licensed open source software; see NOTICE.
package saas

import (
	"fmt"
	"os"
	"strings"
)

// Brand is the single source of truth for customer-facing product identity.
//
// Every value can be overridden through the environment, but the defaults
// represent the official St.Cloud~OS commercial offering.
type Brand struct {
	ProductName        string
	CompanyName        string
	ProductDescription string
	Tagline            string
	PriceCents         int64
	PriceDisplay       string
	Currency           string
	BillingInterval    string
	SupportEmail       string
	AppURL             string
	PublicAppURL       string

	// Legal document settings
	LegalEffectiveDate string
	GoverningLaw       string
}

const (
	DefaultProductName        = "St.Cloud~OS"
	DefaultCompanyName        = "Culp Industries"
	DefaultProductDescription = "Business Execution Intelligence OS"
	DefaultTagline            = "Run your company’s operations in one system and see exactly where work slows down, why it changed and what to do next."
	DefaultPriceCents         = int64(33388)
	DefaultCurrency           = "usd"
	DefaultBillingInterval    = "month"
	DefaultLegalEffectiveDate = "September 27, 2026"

	// LegacyProductName and LegacyProductDescription are the commercial name
	// and description used before the rename; kept only so content stored by
	// earlier versions can be migrated at boot
	LegacyProductName        = "CulpOS"
	LegacyProductDescription = "Business Operations System"
)

// LoadBrand reads brand values from the environment
func LoadBrand() Brand {
	b := Brand{
		ProductName:        env("PRODUCT_NAME", DefaultProductName),
		CompanyName:        env("COMPANY_NAME", DefaultCompanyName),
		ProductDescription: env("PRODUCT_DESCRIPTION", DefaultProductDescription),
		Tagline:            DefaultTagline,
		PriceCents:         DefaultPriceCents,
		Currency:           DefaultCurrency,
		BillingInterval:    env("BILLING_INTERVAL", DefaultBillingInterval),
		SupportEmail:       env("SUPPORT_EMAIL", ""),
		AppURL:             strings.TrimRight(env("APP_URL", ""), "/"),
		PublicAppURL:       strings.TrimRight(env("PUBLIC_APP_URL", ""), "/"),
		LegalEffectiveDate: env("LEGAL_EFFECTIVE_DATE", DefaultLegalEffectiveDate),
		GoverningLaw:       env("LEGAL_GOVERNING_LAW", ""),
	}

	if b.PublicAppURL == "" {
		b.PublicAppURL = b.AppURL
	}

	if b.AppURL == "" {
		b.AppURL = b.PublicAppURL
	}

	// The price is a fixed commercial term; PRICE_CENTS exists only so the
	// display stays consistent with the Stripe price configured for STRIPE_PRICE_ID.
	if v := env("PRICE_CENTS", ""); v != "" {
		var c int64
		if _, err := fmt.Sscan(v, &c); err == nil && c > 0 {
			b.PriceCents = c
		}
	}

	b.PriceDisplay = env("PRICE_DISPLAY", FormatCents(b.PriceCents))
	return b
}

// PricePerInterval returns "$333.88/month"
func (b Brand) PricePerInterval() string {
	return b.PriceDisplay + "/" + b.BillingInterval
}

// PageTitle returns "St.Cloud~OS | <section>" or "St.Cloud~OS"
func (b Brand) PageTitle(section string) string {
	if section == "" {
		return b.ProductName
	}

	return b.ProductName + " | " + section
}

// URL joins a path to the public application URL
func (b Brand) URL(path string) string {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	return b.PublicAppURL + path
}

// ContactEmail returns the address customers should write to, falling back to
// the transactional sender address when no dedicated support mailbox is set
func (b Brand) ContactEmail() string {
	if b.SupportEmail != "" {
		return b.SupportEmail
	}

	return env("MAIL_FROM", "")
}

// FormatCents formats USD cents as "$333.88"
func FormatCents(c int64) string {
	sign := ""
	if c < 0 {
		sign = "-"
		c = -c
	}

	whole := c / 100
	frac := c % 100

	// thousands separators
	s := fmt.Sprintf("%d", whole)
	if len(s) > 3 {
		var out []string
		for len(s) > 3 {
			out = append([]string{s[len(s)-3:]}, out...)
			s = s[:len(s)-3]
		}
		out = append([]string{s}, out...)
		s = strings.Join(out, ",")
	}

	return fmt.Sprintf("%s$%s.%02d", sign, s, frac)
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}

	return def
}

var fmtSscan = fmt.Sscan

// Built-in logo locations served from the embedded static assets
const (
	DefaultMainLogo = "/stcloud/static/stcloud-logo-light.svg"
	DefaultIconLogo = "/stcloud/static/stcloud-mark.svg"
)

// IsBuiltinLogo reports whether a logo setting is empty or points at one of
// the built-in defaults (current or from before the product rename), i.e. it
// was not customized by an administrator
func IsBuiltinLogo(v string) bool {
	switch v {
	case "", "/assets/logo.svg",
		"/culpos/static/culpos-logo.svg", "/culpos/static/culpos-logo-light.svg", "/culpos/static/culpos-mark.svg",
		"/stcloud/static/stcloud-logo.svg", DefaultMainLogo, DefaultIconLogo:
		return true
	}

	return false
}
