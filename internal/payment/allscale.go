package payment

import (
	"bytes"
	"context"
	"crypto/hmac"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Allscale struct {
	Key, Secret, BaseURL string
	HTTP                 *http.Client
	SignedResponses      bool
}

func (a *Allscale) request(ctx context.Context, method, path string, body []byte) (json.RawMessage, error) {
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := ID("")
	canonical := strings.Join([]string{method, path, "", ts, nonce, BodyHash(body)}, "\n")
	r, e := http.NewRequestWithContext(ctx, method, a.BaseURL+path, bytes.NewReader(body))
	if e != nil {
		return nil, e
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-API-Key", a.Key)
	r.Header.Set("X-Timestamp", ts)
	r.Header.Set("X-Nonce", nonce)
	r.Header.Set("X-Signature", MAC(a.Secret, canonical))
	res, e := a.HTTP.Do(r)
	if e != nil {
		return nil, &Uncertain{e}
	}
	b, e := readResponse(res)
	if e != nil {
		return nil, &Uncertain{e}
	}
	if a.SignedResponses {
		t := res.Header.Get("X-Response-Timestamp")
		n := res.Header.Get("X-Response-Nonce")
		c := strings.Join([]string{strconv.Itoa(res.StatusCode), path, nonce, BodyHash(body), t, n, BodyHash(b)}, "\n")
		if !fresh(t, time.Now()) || n == "" || res.Header.Get("X-Request-Nonce") != nonce || !hmac.Equal([]byte(MAC(a.Secret, c)), []byte(res.Header.Get("X-Response-Signature"))) {
			return nil, &Uncertain{ErrSignature}
		}
	}
	var envelope struct {
		Code    *int            `json:"code"`
		Payload json.RawMessage `json:"payload"`
	}
	if json.Unmarshal(b, &envelope) != nil || envelope.Code == nil || *envelope.Code != 0 || len(envelope.Payload) == 0 {
		return nil, &Uncertain{fmt.Errorf("Allscale rejected request")}
	}
	return envelope.Payload, nil
}
func (a *Allscale) Create(ctx context.Context, o Order) (Session, error) {
	// 官方公共币种表未列 TWD；服务端按固定报价快照转换成 USD（currency=1）。
	b, _ := json.Marshal(map[string]any{"currency": 1, "amount_cents": o.ProviderAmount, "order_id": o.ID, "order_description": fmt.Sprintf("点数储值 %d 点", o.Coins), "user_id": o.UserID, "redirect_url": o.SuccessURL})
	raw, e := a.request(ctx, "POST", "/v1/checkout_intents/", b)
	if e != nil {
		return Session{}, e
	}
	var v struct {
		ID  string `json:"allscale_checkout_intent_id"`
		URL string `json:"checkout_url"`
	}
	if json.Unmarshal(raw, &v) != nil || v.ID == "" {
		return Session{}, &Uncertain{fmt.Errorf("invalid Allscale session")}
	}
	if e = ValidateCheckout(v.URL, "allscale"); e != nil {
		return Session{}, &Uncertain{e}
	}
	return Session{v.ID, Checkout{Mode: "hosted", URL: v.URL}}, nil
}

type allscaleObject struct {
	Ref       string `json:"all_scale_checkout_intent_id"`
	OrderID   string `json:"order_id"`
	WebhookID string `json:"webhook_id"`
	Status    *int   `json:"status"`
	Amount    *int64 `json:"amount_cents"`
	Currency  *int   `json:"currency"`
	Chain     int64  `json:"chain_id"`
	Coin      string `json:"coin_symbol"`
	Coins     string `json:"amount_coins"`
	Hash      string `json:"tx_hash"`
}

func allscaleSnapshot(v allscaleObject, ref string, webhook bool) (Snapshot, error) {
	if v.Amount == nil || v.Currency == nil {
		return Snapshot{}, fmt.Errorf("Allscale missing monetary fields")
	}
	code := 20
	if v.Status != nil {
		code = *v.Status
	} else if !webhook {
		return Snapshot{}, fmt.Errorf("Allscale missing status")
	}
	status, detail := "pending", "awaiting_payment"
	switch code {
	case 20:
		status, detail = "succeeded", "confirmed"
	case -1:
		status, detail = "failed", "processing_failed"
	case -2:
		status, detail = "failed", "kyt_rejected"
	case -3:
		status, detail = "flagged", "underpaid"
	case -4:
		status, detail = "expired", "provider_canceled"
	case -5:
		status, detail = "expired", "provider_timeout"
	case 4:
		detail = "manual_review"
	case 5:
		detail = "returning_funds"
	case 10:
		detail = "confirming_onchain"
	case 1, 2, 3:
	default:
		return Snapshot{}, fmt.Errorf("unknown Allscale status %d", code)
	}
	currency := fmt.Sprintf("ALLSCALE_ENUM_%d", *v.Currency)
	if *v.Currency == 1 {
		currency = "USD"
	}
	snap := Snapshot{Ref: ref, OrderID: v.OrderID, Status: status, Detail: detail, Amount: *v.Amount, Currency: currency}
	if code == 20 && v.Chain > 0 && v.Coin != "" && v.Coins != "" && v.Hash != "" {
		snap.Crypto = &Crypto{v.Chain, v.Coin, v.Coins, v.Hash}
	}
	return snap, nil
}
func (a *Allscale) Fetch(ctx context.Context, o Order) (Snapshot, error) {
	raw, e := a.request(ctx, "GET", "/v1/checkout_intents/"+url.PathEscape(o.Ref), nil)
	if e != nil {
		return Snapshot{}, e
	}
	var v allscaleObject
	if e = json.Unmarshal(raw, &v); e != nil {
		return Snapshot{}, e
	}
	return allscaleSnapshot(v, o.Ref, false)
}
func (a *Allscale) Verify(r *http.Request, b []byte) (Event, error) {
	id, ts, nonce := r.Header.Get("X-Webhook-Id"), r.Header.Get("X-Webhook-Timestamp"), r.Header.Get("X-Webhook-Nonce")
	if a.Secret == "" || id == "" || nonce == "" || !fresh(ts, time.Now()) || r.Header.Get("X-API-Key") != a.Key {
		return Event{}, ErrSignature
	}
	canonical := strings.Join([]string{"allscale:webhook:v1", r.Method, r.URL.EscapedPath(), r.URL.RawQuery, id, ts, nonce, BodyHash(b)}, "\n")
	if !hmac.Equal([]byte(MAC(a.Secret, canonical)), []byte(r.Header.Get("X-Webhook-Signature"))) {
		return Event{}, ErrSignature
	}
	var v allscaleObject
	if json.Unmarshal(b, &v) != nil || v.WebhookID != id || v.Ref == "" {
		return Event{}, fmt.Errorf("invalid Allscale webhook")
	}
	snap, e := allscaleSnapshot(v, v.Ref, true)
	return Event{ID: id, Nonce: nonce, Snapshot: snap}, e
}
