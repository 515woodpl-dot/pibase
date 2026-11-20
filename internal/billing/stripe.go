// Package billing talks to Stripe: hosted Checkout to subscribe, the hosted
// customer portal to manage a subscription, and signed webhooks that report
// changes. Card details never reach pibase.
package billing

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

// Stripe is a minimal client for the Stripe API.
type Stripe struct {
	Key  string // secret key, sk_live_... or sk_test_...
	API  string // base URL; https://api.stripe.com unless testing
	HTTP *http.Client
}

// New returns a client for the given secret key.
func New(key string) *Stripe {
	return &Stripe{Key: key, API: "https://api.stripe.com", HTTP: &http.Client{Timeout: 20 * time.Second}}
}

type apiError struct {
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (s *Stripe) do(ctx context.Context, method, path string, form url.Values, idempotencyKey string, out any) error {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(s.API, "/")+path, body)
	if err != nil {
		return err
	}
	req.SetBasicAuth(s.Key, "")
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("stripe: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("stripe: %w", err)
	}
	if resp.StatusCode >= 300 {
		var e apiError
		_ = json.Unmarshal(data, &e)
		if e.Error.Message == "" {
			e.Error.Message = resp.Status
		}
		return fmt.Errorf("stripe: %s", e.Error.Message)
	}
	return json.Unmarshal(data, out)
}

// CreateCustomer makes the Stripe customer of a dashboard account. The
// idempotency key stops a double click from making two.
func (s *Stripe) CreateCustomer(ctx context.Context, accountID, email, name string) (string, error) {
	var out struct {
		ID string `json:"id"`
	}
	form := url.Values{"email": {email}, "metadata[account_id]": {accountID}}
	if name != "" {
		form.Set("name", name)
	}
	if err := s.do(ctx, http.MethodPost, "/v1/customers", form, "pibase-customer-"+accountID, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

// CheckoutURL starts a hosted Checkout page that subscribes the customer to
// one price.
func (s *Stripe) CheckoutURL(ctx context.Context, customerID, accountID, priceID, successURL, cancelURL string) (string, error) {
	var out struct {
		URL string `json:"url"`
	}
	form := url.Values{
		"mode":                    {"subscription"},
		"customer":                {customerID},
		"client_reference_id":     {accountID},
		"line_items[0][price]":    {priceID},
		"line_items[0][quantity]": {"1"},
		"success_url":             {successURL},
		"cancel_url":              {cancelURL},
		"allow_promotion_codes":   {"true"},
		"subscription_data[metadata][account_id]": {accountID},
	}
	if err := s.do(ctx, http.MethodPost, "/v1/checkout/sessions", form, "", &out); err != nil {
		return "", err
	}
	return out.URL, nil
}

// PortalURL opens the hosted customer portal: card, invoices, plan, cancel.
func (s *Stripe) PortalURL(ctx context.Context, customerID, returnURL string) (string, error) {
	var out struct {
		URL string `json:"url"`
	}
	if err := s.do(ctx, http.MethodPost, "/v1/billing_portal/sessions", url.Values{"customer": {customerID}, "return_url": {returnURL}}, "", &out); err != nil {
		return "", err
	}
	return out.URL, nil
}

// Subscription is the part of a Stripe subscription pibase uses.
type Subscription struct {
	ID                string            `json:"id"`
	Customer          string            `json:"customer"`
	Status            string            `json:"status"`
	CancelAtPeriodEnd bool              `json:"cancel_at_period_end"`
	CurrentPeriodEnd  int64             `json:"current_period_end"` // older API versions
	Metadata          map[string]string `json:"metadata"`
	Items             struct {
		Data []struct {
			CurrentPeriodEnd int64 `json:"current_period_end"` // newer API versions
			Price            struct {
				ID string `json:"id"`
			} `json:"price"`
		} `json:"data"`
	} `json:"items"`
}

// PriceID is the subscription's (first) price.
func (s Subscription) PriceID() string {
	if len(s.Items.Data) == 0 {
		return ""
	}
	return s.Items.Data[0].Price.ID
}

// PeriodEnd is when the paid period ends, whichever API version answered.
func (s Subscription) PeriodEnd() *time.Time {
	end := s.CurrentPeriodEnd
	if end == 0 && len(s.Items.Data) > 0 {
		end = s.Items.Data[0].CurrentPeriodEnd
	}
	if end == 0 {
		return nil
	}
	t := time.Unix(end, 0).UTC()
	return &t
}

// GetSubscription reads a subscription's current state. Webhooks only say
// that something changed; reading it back is right even when events arrive
// out of order.
func (s *Stripe) GetSubscription(ctx context.Context, id string) (Subscription, error) {
	var out Subscription
	if id == "" || strings.ContainsAny(id, "/?#") {
		return out, errors.New("stripe: bad subscription id")
	}
	err := s.do(ctx, http.MethodGet, "/v1/subscriptions/"+id, nil, "", &out)
	return out, err
}

// CancelSubscription ends a subscription now, for example when its account
// is deleted.
func (s *Stripe) CancelSubscription(ctx context.Context, id string) error {
	if id == "" || strings.ContainsAny(id, "/?#") {
		return errors.New("stripe: bad subscription id")
	}
	var out Subscription
	return s.do(ctx, http.MethodDelete, "/v1/subscriptions/"+id, nil, "", &out)
}

// Event is a webhook event.
type Event struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Data struct {
		Object json.RawMessage `json:"object"`
	} `json:"data"`
}

// ErrBadSignature means a webhook did not come from Stripe (or is too old).
var ErrBadSignature = errors.New("webhook signature does not match")

// webhookTolerance is how old a signed webhook may be; older ones may be replays.
const webhookTolerance = 5 * time.Minute

// VerifyWebhook checks the Stripe-Signature header against the endpoint's
// signing secret and returns the event.
func VerifyWebhook(payload []byte, header, secret string, now time.Time) (Event, error) {
	var ev Event
	var ts int64
	var sigs []string
	for _, part := range strings.Split(header, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch k {
		case "t":
			ts, _ = strconv.ParseInt(v, 10, 64)
		case "v1":
			sigs = append(sigs, v)
		}
	}
	if ts == 0 || len(sigs) == 0 || secret == "" {
		return ev, ErrBadSignature
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(ts, 10) + "."))
	mac.Write(payload)
	want := mac.Sum(nil)
	matched := false
	for _, sig := range sigs {
		got, err := hex.DecodeString(sig)
		if err == nil && hmac.Equal(got, want) {
			matched = true
		}
	}
	age := now.Sub(time.Unix(ts, 0))
	if !matched || age > webhookTolerance || age < -webhookTolerance {
		return ev, ErrBadSignature
	}
	if err := json.Unmarshal(payload, &ev); err != nil {
		return ev, fmt.Errorf("webhook: %w", err)
	}
	return ev, nil
}

// SignWebhook makes a Stripe-Signature header, for tests and local tools.
func SignWebhook(payload []byte, secret string, at time.Time) string {
	ts := strconv.FormatInt(at.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "."))
	mac.Write(payload)
	return "t=" + ts + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}
