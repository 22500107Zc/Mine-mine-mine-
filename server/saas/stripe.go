package saas

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type (
	// StripeAPI is the subset of the Stripe API St.Cloud~OS relies on
	StripeAPI interface {
		CreateCustomer(ctx context.Context, p CustomerParams) (string, error)
		CreateCheckoutSession(ctx context.Context, p CheckoutParams) (*CheckoutSession, error)
		CreatePortalSession(ctx context.Context, customerID, returnURL string) (string, error)
		GetSubscription(ctx context.Context, id string) (*StripeSubscription, error)
		SetCancelAtPeriodEnd(ctx context.Context, id string, cancel bool) (*StripeSubscription, error)
	}

	CustomerParams struct {
		Email          string
		Name           string
		CompanyID      uint64
		IdempotencyKey string
	}

	CheckoutParams struct {
		CustomerID        string
		PriceID           string
		SuccessURL        string
		CancelURL         string
		ClientReferenceID string
		CompanyID         uint64
	}

	CheckoutSession struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}

	// StripeSubscription is the subset of subscription fields we persist
	StripeSubscription struct {
		ID                 string            `json:"id"`
		Customer           json.RawMessage   `json:"customer"`
		Status             string            `json:"status"`
		CurrentPeriodStart int64             `json:"current_period_start"`
		CurrentPeriodEnd   int64             `json:"current_period_end"`
		CancelAtPeriodEnd  bool              `json:"cancel_at_period_end"`
		CanceledAt         int64             `json:"canceled_at"`
		Metadata           map[string]string `json:"metadata"`
		Items              struct {
			Data []struct {
				CurrentPeriodStart int64 `json:"current_period_start"`
				CurrentPeriodEnd   int64 `json:"current_period_end"`
				Price              struct {
					ID string `json:"id"`
				} `json:"price"`
			} `json:"data"`
		} `json:"items"`
		DefaultPaymentMethod json.RawMessage `json:"default_payment_method"`
	}

	stripeClient struct {
		key     string
		base    string
		http    *http.Client
		version string
	}

	stripeError struct {
		Error struct {
			Type    string `json:"type"`
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
)

const (
	// Pinned API version so that subscription payloads keep a stable shape
	stripeAPIVersion = "2024-06-20"
)

var (
	ErrStripeNotConfigured = errors.New("billing is not configured")
	ErrInvalidSignature    = errors.New("invalid webhook signature")
)

func NewStripeClient(secretKey, base string) StripeAPI {
	if base == "" {
		base = "https://api.stripe.com"
	}

	return &stripeClient{
		key:     secretKey,
		base:    strings.TrimRight(base, "/"),
		http:    &http.Client{Timeout: 30 * time.Second},
		version: stripeAPIVersion,
	}
}

func (c *stripeClient) do(ctx context.Context, method, path string, form url.Values, idem string, out any) error {
	if c.key == "" {
		return ErrStripeNotConfigured
	}

	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}

	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}

	req.SetBasicAuth(c.key, "")
	req.Header.Set("Stripe-Version", c.version)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}

	if idem != "" {
		req.Header.Set("Idempotency-Key", idem)
	}

	rsp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("stripe request failed: %w", err)
	}

	defer rsp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(rsp.Body, 2<<20))
	if err != nil {
		return err
	}

	if rsp.StatusCode >= 300 {
		se := stripeError{}
		_ = json.Unmarshal(raw, &se)
		// Do not include request data in the error; message from Stripe is safe
		return fmt.Errorf("stripe error (%d %s): %s", rsp.StatusCode, se.Error.Type, se.Error.Message)
	}

	return json.Unmarshal(raw, out)
}

func (c *stripeClient) CreateCustomer(ctx context.Context, p CustomerParams) (string, error) {
	var (
		out  struct{ ID string }
		form = url.Values{}
	)

	form.Set("email", p.Email)
	form.Set("name", p.Name)
	form.Set("metadata[company_id]", strconv.FormatUint(p.CompanyID, 10))
	form.Set("metadata[product]", "St.Cloud~OS")

	if err := c.do(ctx, http.MethodPost, "/v1/customers", form, p.IdempotencyKey, &out); err != nil {
		return "", err
	}

	return out.ID, nil
}

func (c *stripeClient) CreateCheckoutSession(ctx context.Context, p CheckoutParams) (*CheckoutSession, error) {
	var (
		out  = &CheckoutSession{}
		cid  = strconv.FormatUint(p.CompanyID, 10)
		form = url.Values{}
	)

	form.Set("mode", "subscription")
	form.Set("customer", p.CustomerID)
	form.Set("line_items[0][price]", p.PriceID)
	form.Set("line_items[0][quantity]", "1")
	form.Set("success_url", p.SuccessURL)
	form.Set("cancel_url", p.CancelURL)
	form.Set("client_reference_id", p.ClientReferenceID)
	form.Set("metadata[company_id]", cid)
	form.Set("subscription_data[metadata][company_id]", cid)

	if err := c.do(ctx, http.MethodPost, "/v1/checkout/sessions", form, "", out); err != nil {
		return nil, err
	}

	return out, nil
}

func (c *stripeClient) CreatePortalSession(ctx context.Context, customerID, returnURL string) (string, error) {
	var (
		out  struct{ URL string }
		form = url.Values{}
	)

	form.Set("customer", customerID)
	form.Set("return_url", returnURL)

	if err := c.do(ctx, http.MethodPost, "/v1/billing_portal/sessions", form, "", &out); err != nil {
		return "", err
	}

	return out.URL, nil
}

func (c *stripeClient) GetSubscription(ctx context.Context, id string) (*StripeSubscription, error) {
	out := &StripeSubscription{}
	path := "/v1/subscriptions/" + url.PathEscape(id) + "?expand[]=default_payment_method"
	if err := c.do(ctx, http.MethodGet, path, nil, "", out); err != nil {
		return nil, err
	}

	return out, nil
}

func (c *stripeClient) SetCancelAtPeriodEnd(ctx context.Context, id string, cancel bool) (*StripeSubscription, error) {
	var (
		out  = &StripeSubscription{}
		form = url.Values{}
	)

	form.Set("cancel_at_period_end", strconv.FormatBool(cancel))
	if err := c.do(ctx, http.MethodPost, "/v1/subscriptions/"+url.PathEscape(id), form, "", out); err != nil {
		return nil, err
	}

	return out, nil
}

// CustomerID extracts customer ID from (possibly expanded) customer field
func (s *StripeSubscription) CustomerID() string {
	return rawID(s.Customer)
}

// PriceID returns the price of the first subscription item
func (s *StripeSubscription) PriceID() string {
	if len(s.Items.Data) > 0 {
		return s.Items.Data[0].Price.ID
	}

	return ""
}

// Period returns current billing period, falling back to item-level periods
// used by newer API versions
func (s *StripeSubscription) Period() (start, end *time.Time) {
	ps, pe := s.CurrentPeriodStart, s.CurrentPeriodEnd
	if (ps == 0 || pe == 0) && len(s.Items.Data) > 0 {
		ps, pe = s.Items.Data[0].CurrentPeriodStart, s.Items.Data[0].CurrentPeriodEnd
	}

	return unixPtr(ps), unixPtr(pe)
}

// PaymentMethodSummary returns e.g. "Visa •••• 4242" when the payment method is expanded
func (s *StripeSubscription) PaymentMethodSummary() string {
	if len(s.DefaultPaymentMethod) == 0 || s.DefaultPaymentMethod[0] != '{' {
		return ""
	}

	var pm struct {
		Type string `json:"type"`
		Card struct {
			Brand string `json:"brand"`
			Last4 string `json:"last4"`
		} `json:"card"`
	}

	if err := json.Unmarshal(s.DefaultPaymentMethod, &pm); err != nil {
		return ""
	}

	if pm.Card.Last4 != "" {
		brand := pm.Card.Brand
		if brand != "" {
			brand = strings.ToUpper(brand[:1]) + brand[1:]
		}
		return strings.TrimSpace(brand + " ending in " + pm.Card.Last4)
	}

	return pm.Type
}

// ToUpdate converts Stripe subscription into our persistence struct
func (s *StripeSubscription) ToUpdate() SubscriptionUpdate {
	start, end := s.Period()
	return SubscriptionUpdate{
		CustomerID:         s.CustomerID(),
		SubscriptionID:     s.ID,
		PriceID:            s.PriceID(),
		Status:             NormalizeSubscriptionStatus(s.Status),
		PeriodStart:        start,
		PeriodEnd:          end,
		CancelAtPeriodEnd:  s.CancelAtPeriodEnd,
		CanceledAt:         unixPtr(s.CanceledAt),
		PaymentMethodBrief: s.PaymentMethodSummary(),
	}
}

func unixPtr(ts int64) *time.Time {
	if ts <= 0 {
		return nil
	}

	t := time.Unix(ts, 0).UTC()
	return &t
}

func rawID(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}

	var s string
	if raw[0] == '"' {
		_ = json.Unmarshal(raw, &s)
		return s
	}

	var obj struct {
		ID string `json:"id"`
	}

	_ = json.Unmarshal(raw, &obj)
	return obj.ID
}

// VerifyWebhookSignature validates the Stripe-Signature header.
//
// See https://docs.stripe.com/webhooks#verify-manually — HMAC-SHA256 over
// "<timestamp>.<payload>" with the endpoint secret, compared in constant time
// against every v1 signature, with a timestamp tolerance against replays.
func VerifyWebhookSignature(payload []byte, header, secret string, tolerance time.Duration, now time.Time) error {
	if secret == "" || header == "" {
		return ErrInvalidSignature
	}

	var (
		ts   int64
		sigs [][]byte
	)

	for _, part := range strings.Split(header, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}

		switch kv[0] {
		case "t":
			v, err := strconv.ParseInt(kv[1], 10, 64)
			if err != nil {
				return ErrInvalidSignature
			}
			ts = v
		case "v1":
			if b, err := hex.DecodeString(kv[1]); err == nil {
				sigs = append(sigs, b)
			}
		}
	}

	if ts == 0 || len(sigs) == 0 {
		return ErrInvalidSignature
	}

	if tolerance > 0 {
		age := now.Sub(time.Unix(ts, 0))
		if age > tolerance || age < -tolerance {
			return ErrInvalidSignature
		}
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(ts, 10)))
	mac.Write([]byte("."))
	mac.Write(payload)
	expected := mac.Sum(nil)

	for _, sig := range sigs {
		if hmac.Equal(expected, sig) {
			return nil
		}
	}

	return ErrInvalidSignature
}

// SignWebhookPayload produces a Stripe-compatible signature header (used by tests and tooling)
func SignWebhookPayload(payload []byte, secret string, ts time.Time) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(ts.Unix(), 10)))
	mac.Write([]byte("."))
	mac.Write(payload)
	return fmt.Sprintf("t=%d,v1=%s", ts.Unix(), hex.EncodeToString(mac.Sum(nil)))
}
