package payment

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Stripe struct {
	Key, WebhookSecret, BaseURL string
	HTTP                        *http.Client
	Live                        bool
}

func (s *Stripe) request(ctx context.Context, method, path string, form url.Values, key string) ([]byte, error) {
	req, e := http.NewRequestWithContext(ctx, method, s.BaseURL+path, strings.NewReader(form.Encode()))
	if e != nil {
		return nil, e
	}
	req.SetBasicAuth(s.Key, "")
	req.Header.Set("Stripe-Version", "2024-06-20")
	if method == "POST" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Idempotency-Key", key)
	}
	r, e := s.HTTP.Do(req)
	if e != nil {
		return nil, &Uncertain{e}
	}
	b, e := readResponse(r)
	if e != nil {
		return nil, &Uncertain{e}
	}
	return b, nil
}
func (s *Stripe) Create(ctx context.Context, o Order) (Session, error) {
	f := url.Values{"mode": {"payment"}, "payment_method_types[0]": {"card"}, "client_reference_id": {o.ID}, "metadata[order_id]": {o.ID}, "payment_intent_data[metadata][order_id]": {o.ID}, "line_items[0][quantity]": {"1"}, "line_items[0][price_data][currency]": {strings.ToLower(o.ProviderCurrency)}, "line_items[0][price_data][unit_amount]": {strconv.FormatInt(o.ProviderAmount, 10)}, "line_items[0][price_data][product_data][name]": {fmt.Sprintf("点数储值 %d 点", o.Coins)}, "expires_at": {strconv.FormatInt(o.ExpiresAt.Unix(), 10)}, "ui_mode": {o.Mode}}
	if o.Mode == "embedded" {
		f.Set("return_url", o.ReturnURL)
		f.Set("redirect_on_completion", "if_required")
	} else {
		f.Set("success_url", o.SuccessURL)
		f.Set("cancel_url", o.CancelURL)
	}
	b, e := s.request(ctx, "POST", "/v1/checkout/sessions", f, "checkout-"+o.ID)
	if e != nil {
		return Session{}, e
	}
	var r struct {
		ID, URL      string
		ClientSecret string `json:"client_secret"`
		Live         bool   `json:"livemode"`
	}
	if e = json.Unmarshal(b, &r); e != nil {
		return Session{}, &Uncertain{e}
	}
	if r.ID == "" || r.Live != s.Live {
		return Session{}, &Uncertain{fmt.Errorf("invalid Stripe session")}
	}
	if o.Mode == "hosted" {
		if e = ValidateCheckout(r.URL, "stripe"); e != nil {
			return Session{}, &Uncertain{e}
		}
	} else if r.ClientSecret == "" {
		return Session{}, &Uncertain{fmt.Errorf("missing client secret")}
	}
	return Session{r.ID, Checkout{Mode: o.Mode, URL: r.URL, ClientSecret: r.ClientSecret}}, nil
}

type stripeSession struct {
	ID            string            `json:"id"`
	Status        string            `json:"status"`
	PaymentStatus string            `json:"payment_status"`
	Amount        int64             `json:"amount_total"`
	Currency      string            `json:"currency"`
	PaymentIntent string            `json:"payment_intent"`
	Metadata      map[string]string `json:"metadata"`
	Live          bool              `json:"livemode"`
}

func (s *Stripe) snapshot(b []byte) (Snapshot, error) {
	var v stripeSession
	if e := json.Unmarshal(b, &v); e != nil {
		return Snapshot{}, e
	}
	if v.ID == "" || v.Live != s.Live {
		return Snapshot{}, fmt.Errorf("invalid Stripe mode/session")
	}
	status := "pending"
	if v.PaymentStatus == "paid" {
		status = "succeeded"
	} else if v.Status == "expired" {
		status = "expired"
	}
	return Snapshot{Ref: v.ID, OrderID: v.Metadata["order_id"], Status: status, Amount: v.Amount, Currency: strings.ToUpper(v.Currency), PaymentIntent: v.PaymentIntent}, nil
}
func (s *Stripe) Fetch(ctx context.Context, o Order) (Snapshot, error) {
	b, e := s.request(ctx, "GET", "/v1/checkout/sessions/"+url.PathEscape(o.Ref), nil, "")
	if e != nil {
		return Snapshot{}, e
	}
	return s.snapshot(b)
}
func (s *Stripe) Verify(r *http.Request, b []byte) (Event, error) {
	parts := strings.Split(r.Header.Get("Stripe-Signature"), ",")
	ts := ""
	var sigs []string
	for _, p := range parts {
		k, v, ok := strings.Cut(strings.TrimSpace(p), "=")
		if !ok {
			continue
		}
		if k == "t" {
			ts = v
		}
		if k == "v1" {
			sigs = append(sigs, v)
		}
	}
	if s.WebhookSecret == "" || !fresh(ts, time.Now()) {
		return Event{}, ErrSignature
	}
	h := hmac.New(sha256.New, []byte(s.WebhookSecret))
	h.Write([]byte(ts + "."))
	h.Write(b)
	valid := false
	for _, sig := range sigs {
		decoded, e := hex.DecodeString(sig)
		if e == nil && hmac.Equal(h.Sum(nil), decoded) {
			valid = true
		}
	}
	if !valid {
		return Event{}, ErrSignature
	}
	var ev struct {
		ID, Type string
		Live     bool `json:"livemode"`
		Data     struct {
			Object json.RawMessage `json:"object"`
		} `json:"data"`
	}
	if json.Unmarshal(b, &ev) != nil || ev.ID == "" || ev.Live != s.Live {
		return Event{}, fmt.Errorf("invalid Stripe event")
	}
	var snap Snapshot
	var e error
	switch ev.Type {
	case "checkout.session.completed", "checkout.session.async_payment_succeeded", "checkout.session.async_payment_failed", "checkout.session.expired":
		snap, e = s.snapshot(ev.Data.Object)
		if ev.Type == "checkout.session.async_payment_failed" && snap.Status != "succeeded" {
			snap.Status = "failed"
		}
	case "charge.refunded":
		var v struct {
			PaymentIntent string `json:"payment_intent"`
			Amount        int64  `json:"amount"`
			Refunded      int64  `json:"amount_refunded"`
			Currency      string `json:"currency"`
		}
		e = json.Unmarshal(ev.Data.Object, &v)
		snap = Snapshot{Ref: v.PaymentIntent, ByPaymentIntent: true, Status: "refunded", Amount: v.Amount, Currency: strings.ToUpper(v.Currency)}
		if v.Refunded != v.Amount {
			snap.Status = "flagged"
			snap.Detail = "partial_refund_requires_review"
		}
	default:
		return Event{}, ErrIgnored
	}
	if e != nil || snap.Ref == "" {
		return Event{}, fmt.Errorf("invalid Stripe object")
	}
	return Event{ID: ev.ID, Snapshot: snap}, nil
}
