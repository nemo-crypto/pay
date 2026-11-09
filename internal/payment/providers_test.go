package payment

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestStripeCreateFieldsAndIdempotency(t *testing.T) {
	count := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		if r.URL.Path != "/v1/checkout/sessions" {
			t.Error(r.URL.Path)
		}
		r.ParseForm()
		if r.Header.Get("Idempotency-Key") != "checkout-top_one" || r.Form.Get("line_items[0][price_data][unit_amount]") != "100000" || r.Form.Get("metadata[order_id]") != "top_one" {
			t.Error("invalid request fields")
		}
		user, _, _ := r.BasicAuth()
		if user != "sk_test_fake" {
			t.Error("missing server auth")
		}
		io.WriteString(w, `{"id":"cs_test_one","livemode":false,"url":"https://checkout.stripe.com/c/pay/one","client_secret":"cs_secret"}`)
	}))
	defer server.Close()
	p := Stripe{Key: "sk_test_fake", BaseURL: server.URL, HTTP: Client()}
	o := Order{ID: "top_one", ProviderCurrency: "TWD", ProviderAmount: 100000, Coins: 2000, Mode: "hosted", ExpiresAt: time.Now().Add(time.Hour)}
	for i := 0; i < 2; i++ {
		if _, e := p.Create(context.Background(), o); e != nil {
			t.Fatal(e)
		}
	}
	if count != 2 {
		t.Fatal(count)
	}
}
func TestAllscaleProtocol(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		expected := MAC("test-secret", strings.Join([]string{r.Method, r.URL.Path, "", r.Header.Get("X-Timestamp"), r.Header.Get("X-Nonce"), BodyHash(body)}, "\n"))
		if r.Header.Get("X-Signature") != expected || r.Header.Get("X-API-Key") != "test-key" {
			t.Error("invalid HMAC")
		}
		if r.Method == "POST" {
			if r.URL.Path != "/v1/checkout_intents/" {
				t.Error("missing slash")
			}
			var p map[string]any
			json.Unmarshal(body, &p)
			if p["currency"] != float64(1) || p["amount_cents"] != float64(3125) {
				t.Error(p)
			}
			io.WriteString(w, `{"code":0,"payload":{"allscale_checkout_intent_id":"as_1","checkout_url":"https://checkout.allscale.io/as_1"}}`)
		} else {
			io.WriteString(w, `{"code":0,"payload":{"status":20,"currency":1,"amount_cents":3125,"order_id":"top_one","chain_id":8453,"coin_symbol":"USDC","amount_coins":"31.250000","tx_hash":"0x123"}}`)
		}
	}))
	defer server.Close()
	p := Allscale{Key: "test-key", Secret: "test-secret", BaseURL: server.URL, HTTP: Client()}
	o := Order{ID: "top_one", ProviderAmount: 3125}
	session, e := p.Create(context.Background(), o)
	if e != nil {
		t.Fatal(e)
	}
	o.Ref = session.Ref
	s, e := p.Fetch(context.Background(), o)
	if e != nil || s.Status != "succeeded" || s.Currency != "USD" || s.Amount != 3125 || s.Crypto.AmountCoins != "31.250000" {
		t.Fatal(s, e)
	}
}
func TestAllscaleUnknownStateAndMissingAmountFailClosed(t *testing.T) {
	for _, raw := range []string{`{"status":20,"currency":1}`, `{"status":999,"currency":1,"amount_cents":10}`, `{"currency":1,"amount_cents":10}`} {
		var v allscaleObject
		json.Unmarshal([]byte(raw), &v)
		if _, e := allscaleSnapshot(v, "ref", false); e == nil {
			t.Fatal("invalid details accepted", raw)
		}
	}
}
func TestCheckoutURLValidation(t *testing.T) {
	for _, raw := range []string{"http://checkout.stripe.com/x", "https://checkout.stripe.com.evil.test/x", "https://user@checkout.stripe.com/x", "javascript:alert(1)"} {
		if ValidateCheckout(raw, "stripe") == nil {
			t.Fatal(raw)
		}
	}
	if ValidateCheckout("https://checkout.allscale.io/abc", "allscale") != nil {
		t.Fatal("valid URL rejected")
	}
}
func TestAllscaleStateMapping(t *testing.T) {
	for code, status := range map[int]string{1: "pending", 2: "pending", 3: "pending", 4: "pending", 5: "pending", 10: "pending", 20: "succeeded", -1: "failed", -2: "failed", -3: "flagged", -4: "expired", -5: "expired"} {
		c := code
		amount := int64(100)
		currency := 1
		s, e := allscaleSnapshot(allscaleObject{Status: &c, Amount: &amount, Currency: &currency}, "ref", false)
		if e != nil || s.Status != status {
			t.Fatal(code, s, e)
		}
	}
}

func TestAllscaleIndependentSignatureVector(t *testing.T) {
	body := []byte(`{"currency":1,"amount_cents":3125,"order_id":"top_one"}`)
	canonical := "POST\n/v1/checkout_intents/\n\n1700000000\nnonce-fixed\n" + BodyHash(body)
	if got := MAC("test-secret", canonical); got != "v1=v3V+wfhPiM5ODzwKLN9zrs7ciN7lMhQL/+rCvrXHqDE=" {
		t.Fatal(got)
	}
}
func TestAllscaleRejectsUnsignedResponseWhenRequired(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"code":0,"payload":{"status":20,"currency":1,"amount_cents":100}}`)
	}))
	defer server.Close()
	p := Allscale{Key: "key", Secret: "secret", BaseURL: server.URL, HTTP: Client(), SignedResponses: true}
	if _, err := p.Fetch(context.Background(), Order{Ref: "intent"}); err == nil {
		t.Fatal("unsigned response accepted")
	}
}
