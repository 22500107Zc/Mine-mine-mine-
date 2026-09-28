package saas

import (
	"strings"
	"time"
)

type (
	SubscriptionStatus string
	CompanyStatus      string
	ProvisioningStatus string
	CompanyRole        string
	AccessLevel        int

	Company struct {
		ID                   uint64
		Name                 string
		Slug                 string
		Status               CompanyStatus
		OwnerUserID          uint64
		OwnerEmail           string
		OwnerName            string
		NamespaceID          uint64
		RoleOwnerID          uint64
		RoleAdminID          uint64
		RoleManagerID        uint64
		RoleEmployeeID       uint64
		StripeCustomerID     string
		StripeSubscriptionID string
		StripePriceID        string
		SubscriptionStatus   SubscriptionStatus
		BillingPeriodStart   *time.Time
		BillingPeriodEnd     *time.Time
		CancelAtPeriodEnd    bool
		CanceledAt           *time.Time
		ProvisioningStatus   ProvisioningStatus
		ProvisioningError    string
		ProvisionedAt        *time.Time
		LastActivityAt       *time.Time
		PaymentMethodSummary string
		Website              string
		Phone                string
		Address              string
		Industry             string
		OnboardingDoneAt     *time.Time
		ActivityBackfilledAt *time.Time
		CreatedAt            time.Time
		UpdatedAt            time.Time

		// Computed on read
		UserCount int
	}

	Member struct {
		CompanyID uint64
		UserID    uint64
		Role      CompanyRole
		InvitedBy uint64
		CreatedAt time.Time

		// Joined from the users table for display
		Email     string
		Name      string
		Suspended bool
	}

	// Founder is the single platform owner identity; it has no username
	Founder struct {
		ID             uint64
		PasswordHash   string
		FailedAttempts int
		LockedUntil    *time.Time
		LastLoginAt    *time.Time
		CreatedAt      time.Time
		UpdatedAt      time.Time
	}

	FounderSession struct {
		TokenHash  string
		FounderID  uint64
		CSRFToken  string
		CreatedAt  time.Time
		ExpiresAt  time.Time
		LastSeenAt time.Time
		IP         string
		UserAgent  string
	}

	Payment struct {
		ID          string
		CompanyID   uint64
		CompanyName string
		AmountCents int64
		Currency    string
		Status      string
		CreatedAt   time.Time
	}

	StripeEventRecord struct {
		ID          string
		Type        string
		Status      string
		Error       string
		CompanyID   uint64
		ReceivedAt  time.Time
		ProcessedAt *time.Time
	}

	AuditEntry struct {
		ID         int64
		OccurredAt time.Time
		ActorType  string
		ActorID    string
		ActorLabel string
		Role       string
		CompanyID  uint64
		Action     string
		Target     string
		Result     string
		IP         string
		Metadata   map[string]string

		// CompanyName is resolved for display; it is not stored
		CompanyName string
	}

	// AccessDecision is the outcome of evaluating a company's commercial state
	AccessDecision struct {
		Level   AccessLevel
		Reason  string
		Warning string
	}
)

const (
	SubActive            SubscriptionStatus = "active"
	SubPastDue           SubscriptionStatus = "past_due"
	SubCanceled          SubscriptionStatus = "canceled"
	SubUnpaid            SubscriptionStatus = "unpaid"
	SubIncomplete        SubscriptionStatus = "incomplete"
	SubIncompleteExpired SubscriptionStatus = "incomplete_expired"
	SubTrialing          SubscriptionStatus = "trialing"
	SubPaused            SubscriptionStatus = "paused"

	CompanyPending  CompanyStatus = "pending"
	CompanyActive   CompanyStatus = "active"
	CompanyDisabled CompanyStatus = "disabled"

	ProvAwaitingPayment ProvisioningStatus = "awaiting_payment"
	ProvProvisioning    ProvisioningStatus = "provisioning"
	ProvProvisioned     ProvisioningStatus = "provisioned"
	ProvFailed          ProvisioningStatus = "failed"

	RoleOwner         CompanyRole = "owner"
	RoleAdministrator CompanyRole = "administrator"
	RoleManager       CompanyRole = "manager"
	RoleEmployee      CompanyRole = "employee"
)

const (
	// AccessNone - company disabled by the platform
	AccessNone AccessLevel = iota
	// AccessBillingOnly - only billing and account recovery
	AccessBillingOnly
	// AccessFull - normal operational access
	AccessFull
)

var (
	// roleRank defines the company hierarchy. Founder is a platform role and
	// deliberately NOT part of this list: no company role can grant it.
	roleRank = map[CompanyRole]int{
		RoleOwner:         40,
		RoleAdministrator: 30,
		RoleManager:       20,
		RoleEmployee:      10,
	}
)

// ParseCompanyRole validates a company role; "founder" and unknown values are rejected
func ParseCompanyRole(s string) (CompanyRole, bool) {
	r := CompanyRole(strings.ToLower(strings.TrimSpace(s)))
	_, ok := roleRank[r]
	return r, ok
}

// Label returns human friendly label for the role
func (r CompanyRole) Label() string {
	switch r {
	case RoleOwner:
		return "Company Owner"
	case RoleAdministrator:
		return "Administrator"
	case RoleManager:
		return "Manager"
	case RoleEmployee:
		return "Employee"
	}

	return string(r)
}

// Outranks reports whether r is strictly higher than o
func (r CompanyRole) Outranks(o CompanyRole) bool {
	return roleRank[r] > roleRank[o]
}

// CanManageMembers reports whether the role may invite/remove users
func (r CompanyRole) CanManageMembers() bool {
	return r == RoleOwner || r == RoleAdministrator
}

// CanAssign reports whether actor role r may assign role target to someone.
//
// Owners may assign administrator/manager/employee; administrators may assign
// manager/employee. Nobody can assign owner through the member management UI
// (ownership transfer is a Founder operation) and Founder is not a company role.
func (r CompanyRole) CanAssign(target CompanyRole) bool {
	if target == RoleOwner {
		return false
	}

	if _, ok := roleRank[target]; !ok {
		return false
	}

	return r.CanManageMembers() && r.Outranks(target)
}

// CanViewBilling reports whether the role may see billing details
func (r CompanyRole) CanViewBilling() bool {
	return r == RoleOwner || r == RoleAdministrator
}

// CanManageSubscription reports whether the role may change the subscription
func (r CompanyRole) CanManageSubscription() bool {
	return r == RoleOwner
}

// Label for the subscription status
// CancelScheduled reports a subscription that is still running but will not
// renew. Stripe keeps cancel_at_period_end set after the subscription ends,
// so ended subscriptions are excluded.
func (c *Company) CancelScheduled() bool {
	if c == nil || !c.CancelAtPeriodEnd {
		return false
	}

	switch c.SubscriptionStatus {
	case SubActive, SubPastDue, SubTrialing:
		return true
	}

	return false
}

func (s SubscriptionStatus) Label() string {
	switch s {
	case SubActive:
		return "Active"
	case SubPastDue:
		return "Past due"
	case SubCanceled:
		return "Canceled"
	case SubUnpaid:
		return "Unpaid"
	case SubIncomplete:
		return "Incomplete"
	case SubIncompleteExpired:
		return "Incomplete (expired)"
	case SubTrialing:
		return "Trialing"
	case SubPaused:
		return "Paused"
	case "":
		return "Awaiting payment"
	}

	return string(s)
}

// NormalizeSubscriptionStatus maps unknown Stripe values to incomplete so
// that an unexpected status can never grant access
func NormalizeSubscriptionStatus(s string) SubscriptionStatus {
	switch st := SubscriptionStatus(strings.ToLower(s)); st {
	case SubActive, SubPastDue, SubCanceled, SubUnpaid, SubIncomplete, SubIncompleteExpired, SubTrialing, SubPaused:
		return st
	}

	return SubIncomplete
}

// Evaluate decides what a company may do right now.
//
// This is the single server-side authority used by the API gate, the auth
// flow guard and the customer pages.
func Evaluate(c *Company, now time.Time) AccessDecision {
	if c == nil {
		return AccessDecision{Level: AccessNone, Reason: "unknown"}
	}

	if c.Status == CompanyDisabled {
		return AccessDecision{Level: AccessNone, Reason: "disabled"}
	}

	if c.ProvisioningStatus != ProvProvisioned {
		return AccessDecision{Level: AccessBillingOnly, Reason: "not-provisioned"}
	}

	switch c.SubscriptionStatus {
	case SubActive:
		d := AccessDecision{Level: AccessFull}
		if c.CancelAtPeriodEnd && c.BillingPeriodEnd != nil {
			d.Warning = "Your subscription is scheduled to end on " + c.BillingPeriodEnd.Format("January 2, 2006") + "."
		}
		return d

	case SubTrialing:
		// No trial is offered; if Stripe ever reports one we treat it as paid
		// access only while the paid-through period is valid.
		if c.BillingPeriodEnd != nil && now.Before(*c.BillingPeriodEnd) {
			return AccessDecision{Level: AccessFull}
		}
		return AccessDecision{Level: AccessBillingOnly, Reason: "trial-not-offered"}

	case SubPastDue:
		return AccessDecision{
			Level:   AccessFull,
			Reason:  "past-due",
			Warning: "We could not process your latest payment. Update your payment method to keep your CulpOS workspace active.",
		}

	case SubCanceled:
		if c.BillingPeriodEnd != nil && now.Before(*c.BillingPeriodEnd) {
			return AccessDecision{
				Level:   AccessFull,
				Reason:  "canceled-in-period",
				Warning: "Your subscription was canceled. Access remains available until " + c.BillingPeriodEnd.Format("January 2, 2006") + ".",
			}
		}
		return AccessDecision{Level: AccessBillingOnly, Reason: "canceled"}

	case SubUnpaid:
		return AccessDecision{Level: AccessBillingOnly, Reason: "unpaid"}

	default:
		// incomplete, incomplete_expired, paused, empty and anything unknown
		return AccessDecision{Level: AccessBillingOnly, Reason: "payment-required"}
	}
}
