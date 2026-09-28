package saas

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"go.uber.org/zap"
)

type (
	stripeEvent struct {
		ID      string `json:"id"`
		Type    string `json:"type"`
		Created int64  `json:"created"`
		Data    struct {
			Object             json.RawMessage `json:"object"`
			PreviousAttributes json.RawMessage `json:"previous_attributes"`
		} `json:"data"`
	}

	checkoutSessionObject struct {
		ID                string            `json:"id"`
		Mode              string            `json:"mode"`
		Status            string            `json:"status"`
		PaymentStatus     string            `json:"payment_status"`
		ClientReferenceID string            `json:"client_reference_id"`
		Customer          json.RawMessage   `json:"customer"`
		Subscription      json.RawMessage   `json:"subscription"`
		Metadata          map[string]string `json:"metadata"`
	}

	invoiceObject struct {
		ID           string          `json:"id"`
		Customer     json.RawMessage `json:"customer"`
		Subscription json.RawMessage `json:"subscription"`
		AmountPaid   int64           `json:"amount_paid"`
		AmountDue    int64           `json:"amount_due"`
		Currency     string          `json:"currency"`
		Created      int64           `json:"created"`
		Parent       struct {
			SubscriptionDetails struct {
				Subscription json.RawMessage `json:"subscription"`
			} `json:"subscription_details"`
		} `json:"parent"`
	}
)

// HandledWebhookEvents lists the Stripe events CulpOS processes
var HandledWebhookEvents = []string{
	"checkout.session.completed",
	"checkout.session.async_payment_succeeded",
	"checkout.session.async_payment_failed",
	"customer.subscription.created",
	"customer.subscription.updated",
	"customer.subscription.deleted",
	"customer.subscription.paused",
	"customer.subscription.resumed",
	"invoice.paid",
	"invoice.payment_failed",
	"invoice.payment_action_required",
	"payment_method.attached",
	"customer.updated",
}

// HandleWebhook verifies and processes a Stripe webhook delivery.
//
// A nil error means the delivery should be acknowledged (2xx) — including
// duplicates and ignored event types. ErrInvalidSignature must result in 400.
// Other errors should produce 5xx so Stripe retries.
func (svc *Service) HandleWebhook(ctx context.Context, payload []byte, sigHeader string) error {
	if err := VerifyWebhookSignature(payload, sigHeader, svc.cfg.StripeWebhookSecret, svc.cfg.WebhookTolerance, svc.now()); err != nil {
		svc.log.Warn("rejected Stripe webhook with invalid signature")
		return ErrInvalidSignature
	}

	ev := stripeEvent{}
	if err := json.Unmarshal(payload, &ev); err != nil || ev.ID == "" || ev.Type == "" {
		return fmt.Errorf("%w: malformed event", ErrInvalidSignature)
	}

	fresh, err := svc.repo.RecordStripeEvent(ctx, ev.ID, ev.Type)
	if err != nil {
		return err
	}

	if !fresh {
		svc.log.Info("duplicate Stripe webhook ignored", zap.String("event", ev.ID), zap.String("type", ev.Type))
		return nil
	}

	companyID, procErr := svc.processEvent(ctx, ev)
	if ferr := svc.repo.FinishStripeEvent(ctx, ev.ID, companyID, procErr); ferr != nil {
		svc.log.Error("failed to finalize Stripe event", zap.String("event", ev.ID), zap.Error(ferr))
	}

	if procErr != nil {
		svc.log.Error("Stripe webhook processing failed", zap.String("event", ev.ID), zap.String("type", ev.Type), zap.Error(procErr))
		return procErr
	}

	svc.log.Info("Stripe webhook processed", zap.String("event", ev.ID), zap.String("type", ev.Type), zap.Uint64("companyID", companyID))
	return nil
}

func (svc *Service) processEvent(ctx context.Context, ev stripeEvent) (uint64, error) {
	switch ev.Type {
	case "checkout.session.completed", "checkout.session.async_payment_succeeded":
		return svc.onCheckoutCompleted(ctx, ev)

	case "customer.subscription.created", "customer.subscription.updated", "customer.subscription.deleted",
		"customer.subscription.paused", "customer.subscription.resumed":
		return svc.onSubscriptionChanged(ctx, ev)

	case "invoice.paid":
		return svc.onInvoice(ctx, ev, true)

	case "invoice.payment_failed", "invoice.payment_action_required", "checkout.session.async_payment_failed":
		if ev.Type == "checkout.session.async_payment_failed" {
			return 0, nil
		}
		return svc.onInvoice(ctx, ev, false)

	default:
		// payment_method.attached, customer.updated and others: acknowledged,
		// state is refreshed on the next subscription/invoice event
		return 0, nil
	}
}

func (svc *Service) onCheckoutCompleted(ctx context.Context, ev stripeEvent) (uint64, error) {
	obj := checkoutSessionObject{}
	if err := json.Unmarshal(ev.Data.Object, &obj); err != nil {
		return 0, err
	}

	if obj.Mode != "subscription" {
		return 0, nil
	}

	var (
		customerID = rawID(obj.Customer)
		subID      = rawID(obj.Subscription)
		c          *Company
		err        error
	)

	if idStr := firstNonEmpty(obj.Metadata["company_id"], obj.ClientReferenceID); idStr != "" {
		if id, perr := strconv.ParseUint(idStr, 10, 64); perr == nil {
			c, err = svc.repo.CompanyByID(ctx, id)
		}
	}

	if c == nil && customerID != "" {
		c, err = svc.repo.CompanyByStripeCustomer(ctx, customerID)
	}

	if c == nil {
		if errors.Is(err, ErrNotFound) || err == nil {
			svc.log.Warn("checkout completed for unknown company", zap.String("session", obj.ID))
			return 0, nil
		}
		return 0, err
	}

	// The customer on the session must be the customer we created for this company
	if c.StripeCustomerID != "" && customerID != "" && c.StripeCustomerID != customerID {
		svc.log.Error("checkout session customer mismatch", zap.Uint64("companyID", c.ID))
		return c.ID, fmt.Errorf("customer mismatch for company %d", c.ID)
	}

	if obj.Status != "complete" || (obj.PaymentStatus != "paid" && obj.PaymentStatus != "no_payment_required") {
		// async payment methods: wait for checkout.session.async_payment_succeeded / invoice.paid
		return c.ID, nil
	}

	if subID == "" {
		return c.ID, fmt.Errorf("checkout session without subscription")
	}

	// Authoritative state comes from the Stripe API, not from the redirect
	sub, err := svc.stripe.GetSubscription(ctx, subID)
	if err != nil {
		return c.ID, err
	}

	return c.ID, svc.applySubscription(ctx, c, sub, "")
}

func (svc *Service) onSubscriptionChanged(ctx context.Context, ev stripeEvent) (uint64, error) {
	sub := &StripeSubscription{}
	if err := json.Unmarshal(ev.Data.Object, sub); err != nil {
		return 0, err
	}

	c, err := svc.companyForSubscription(ctx, sub.ID, sub.CustomerID(), sub.Metadata["company_id"])
	if err != nil || c == nil {
		return 0, err
	}

	if ev.Type == "customer.subscription.deleted" {
		sub.Status = string(SubCanceled)
	}

	return c.ID, svc.applySubscription(ctx, c, sub, ev.Type)
}

func (svc *Service) onInvoice(ctx context.Context, ev stripeEvent, paid bool) (uint64, error) {
	inv := invoiceObject{}
	if err := json.Unmarshal(ev.Data.Object, &inv); err != nil {
		return 0, err
	}

	subID := rawID(inv.Subscription)
	if subID == "" {
		subID = rawID(inv.Parent.SubscriptionDetails.Subscription)
	}

	c, err := svc.companyForSubscription(ctx, subID, rawID(inv.Customer), "")
	if err != nil || c == nil {
		return 0, err
	}

	p := &Payment{
		ID:          inv.ID,
		CompanyID:   c.ID,
		AmountCents: inv.AmountPaid,
		Currency:    inv.Currency,
		Status:      "paid",
		CreatedAt:   svc.now(),
	}

	if inv.Created > 0 {
		p.CreatedAt = time.Unix(inv.Created, 0).UTC()
	}

	if !paid {
		p.Status = "failed"
		p.AmountCents = inv.AmountDue
	}

	if err = svc.repo.UpsertPayment(ctx, p); err != nil {
		return c.ID, err
	}

	if subID != "" {
		sub, err := svc.stripe.GetSubscription(ctx, subID)
		if err != nil {
			return c.ID, err
		}

		if err = svc.applySubscription(ctx, c, sub, ""); err != nil {
			return c.ID, err
		}
	}

	if paid {
		svc.audit(ctx, AuditEntry{ActorType: ActorStripe, CompanyID: c.ID}.with("billing.payment.succeeded", inv.ID, ResultSuccess,
			map[string]string{"amount": FormatCents(inv.AmountPaid)}))
		svc.sendMail(ctx, c.OwnerEmail, "Payment received — "+svc.cfg.Brand.ProductName, "payment-success", map[string]any{
			"Company": c.Name,
			"Amount":  FormatCents(inv.AmountPaid),
			"URL":     svc.cfg.Brand.URL("/billing"),
		})
	} else {
		svc.audit(ctx, AuditEntry{ActorType: ActorStripe, CompanyID: c.ID}.with("billing.payment.failed", inv.ID, ResultFailure,
			map[string]string{"amount": FormatCents(inv.AmountDue)}))
		svc.sendMail(ctx, c.OwnerEmail, "Action required: payment failed — "+svc.cfg.Brand.ProductName, "payment-failed", map[string]any{
			"Company": c.Name,
			"Amount":  FormatCents(inv.AmountDue),
			"URL":     svc.cfg.Brand.URL("/billing"),
		})
	}

	return c.ID, nil
}

func (svc *Service) companyForSubscription(ctx context.Context, subID, customerID, companyIDStr string) (*Company, error) {
	var (
		c   *Company
		err error
	)

	if subID != "" {
		if c, err = svc.repo.CompanyByStripeSubscription(ctx, subID); err != nil && !errors.Is(err, ErrNotFound) {
			return nil, err
		}
	}

	if c == nil && customerID != "" {
		if c, err = svc.repo.CompanyByStripeCustomer(ctx, customerID); err != nil && !errors.Is(err, ErrNotFound) {
			return nil, err
		}
	}

	if c == nil && companyIDStr != "" {
		if id, perr := strconv.ParseUint(companyIDStr, 10, 64); perr == nil {
			if c, err = svc.repo.CompanyByID(ctx, id); err != nil && !errors.Is(err, ErrNotFound) {
				return nil, err
			}

			// metadata alone is not trusted when the customer does not match
			if c != nil && c.StripeCustomerID != "" && customerID != "" && c.StripeCustomerID != customerID {
				return nil, nil
			}
		}
	}

	if c == nil {
		svc.log.Warn("Stripe event for unknown subscription/customer ignored")
	}

	return c, nil
}

// applySubscription persists Stripe state, provisions on first successful
// payment, and notifies on cancellation
func (svc *Service) applySubscription(ctx context.Context, c *Company, sub *StripeSubscription, evType string) error {
	upd := sub.ToUpdate()

	// Only the configured CulpOS price is accepted
	if svc.cfg.StripePriceID != "" && upd.PriceID != "" && upd.PriceID != svc.cfg.StripePriceID {
		svc.log.Error("subscription with unexpected price ignored", zap.Uint64("companyID", c.ID), zap.String("price", upd.PriceID))
		return nil
	}

	prev := c.SubscriptionStatus
	prevCancel := c.CancelAtPeriodEnd

	if err := svc.repo.ApplySubscription(ctx, c.ID, upd); err != nil {
		return err
	}

	svc.cache.invalidateCompany(c.ID)

	if prev != upd.Status || prevCancel != upd.CancelAtPeriodEnd {
		svc.audit(ctx, AuditEntry{ActorType: ActorStripe, CompanyID: c.ID}.with("billing.subscription.changed", sub.ID, ResultSuccess,
			map[string]string{"from": string(prev), "to": string(upd.Status), "cancelAtPeriodEnd": strconv.FormatBool(upd.CancelAtPeriodEnd)}))
	}

	if upd.Status == SubActive && c.ProvisioningStatus != ProvProvisioned {
		if err := svc.Provision(ctx, c.ID); err != nil {
			return err
		}
	}

	endDate := ""
	if upd.PeriodEnd != nil {
		endDate = upd.PeriodEnd.Format("January 2, 2006")
	}

	switch {
	case evType == "customer.subscription.deleted" && prev != SubCanceled:
		svc.sendMail(ctx, c.OwnerEmail, "Your "+svc.cfg.Brand.ProductName+" subscription has ended", "subscription-canceled", map[string]any{
			"Company": c.Name, "EndDate": endDate, "Ended": true, "URL": svc.cfg.Brand.URL("/billing"),
		})
	case !upd.CancelAtPeriodEnd && prevCancel && upd.Status == SubActive:
		svc.sendMail(ctx, c.OwnerEmail, "Your "+svc.cfg.Brand.ProductName+" subscription will continue", "subscription-resumed", map[string]any{
			"Company": c.Name, "NextDate": endDate, "URL": svc.cfg.Brand.URL("/billing"),
		})
	case upd.CancelAtPeriodEnd && !prevCancel:
		svc.sendMail(ctx, c.OwnerEmail, "Your "+svc.cfg.Brand.ProductName+" subscription is set to cancel", "subscription-canceled", map[string]any{
			"Company": c.Name, "EndDate": endDate, "Ended": false, "URL": svc.cfg.Brand.URL("/billing"),
		})
	}

	return nil
}
