package onramp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestIndependentURLSignatureVectors(t *testing.T) {
	q := "?apiKey=pk_test_demo&baseCurrencyAmount=30.00&walletAddress=0x1111111111111111111111111111111111111111"
	if got := b64mac("golden-secret", q); got != "wCal2f+4riNMo0uDVutFWqoEX2XEPmJMNLk60Z4EDf0=" {
		t.Fatal(got)
	}
	params := url.Values{"timestamp": {"1700000000000"}, "fiatAmount": {"30.00"}, "appId": {"mock-app"}, "fiat": {"USD"}}
	if got := b64mac("golden-secret", "1700000000000GET/index/rampPageBuy?"+alchemyQuery(params)); got != "t2krO5cxMQoQX3dHpLfEoJK65rg7lkL4KL56CbcLzQQ=" {
		t.Fatal(got)
	}
}
func TestValidJSONInvalidSignatures(t *testing.T) {
	for _, name := range Names {
		t.Run(name, func(t *testing.T) {
			e := fixture(t)
			o := order(t, e, name)
			a := e.Adapters[name]
			r, b, _ := a.mockEvent(o, mockPayload(o, "completed"))
			switch name {
			case "moonpay":
				r.Header.Set("Moonpay-Signature-V2", "t=1700000000,s=00")
			case "ramp":
				r.Header.Set("X-Body-Signature", "AAAA")
			case "alchemypay":
				var m map[string]any
				json.Unmarshal(b, &m)
				m["address"] = "0x2222222222222222222222222222222222222222"
				b, _ = json.Marshal(m)
			case "transak":
				raw, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"webhookData": mockPayload(o, "completed")}).SignedString([]byte("wrong-secret"))
				b, _ = json.Marshal(map[string]string{"data": raw})
			}
			if _, err := a.Verify(r, b); err == nil {
				t.Fatal("forged signature accepted")
			}
		})
	}
}
func TestRampCanonicalizationAndOfficialKeys(t *testing.T) {
	for _, k := range []string{rampProductionKey, rampSandboxKey} {
		if _, err := rampPublic(k); err != nil {
			t.Fatal(err)
		}
	}
	e := fixture(t)
	o := order(t, e, "ramp")
	a := e.Adapters["ramp"]
	r, b, _ := a.mockEvent(o, mockPayload(o, "completed"))
	var m any
	json.Unmarshal(b, &m)
	pretty, _ := json.MarshalIndent(m, "", "  ")
	if _, err := a.Verify(r, pretty); err != nil {
		t.Fatal("canonical JSON verification failed", err)
	}
}
func TestTransakTokenCacheAndSessionAPI(t *testing.T) {
	tokens, sessions := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/partners/api/v2/refresh-token":
			tokens++
			if r.Header.Get("api-secret") != "secret" || r.Header.Get("x-api-key") != "key" {
				t.Error("bad auth headers")
			}
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"accessToken": "partner-token", "expiresAt": time.Now().Add(time.Hour).Unix()}})
		case "/api/v2/auth/session":
			sessions++
			if r.Header.Get("access-token") != "partner-token" || r.Header.Get("x-user-ip") != "203.0.113.1" {
				t.Error("missing session headers")
			}
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			p := object(body, "widgetParams")
			if str(p, "partnerOrderId") != "on_one" || p["disableWalletAddressForm"] != true {
				t.Error("bad session params")
			}
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"widgetUrl": "https://global-stg.transak.com/?sessionId=test"}})
		default:
			t.Error(r.URL.Path)
		}
	}))
	defer server.Close()
	a := &Adapter{Name: "transak", Key: "key", Secret: "secret", API: server.URL, SessionAPI: server.URL, Widget: "https://global-stg.transak.com", PublicURL: "https://merchant.test", HTTP: server.Client()}
	for i := 0; i < 2; i++ {
		if _, err := a.Create(context.Background(), Order{ID: "on_one", FiatMinor: 3000, ClientIP: "203.0.113.1", Wallet: "0x1111111111111111111111111111111111111111"}); err != nil {
			t.Fatal(err)
		}
	}
	if tokens != 1 || sessions != 2 {
		t.Fatal(tokens, sessions)
	}
}
func TestNoMockFallbackInLiveConfiguration(t *testing.T) {
	t.Setenv("ONRAMP_PROVIDERS", "moonpay")
	t.Setenv("MOONPAY_PUBLIC_KEY", "")
	t.Setenv("MOONPAY_SECRET_KEY", "")
	t.Setenv("MOONPAY_WEBHOOK_KEY", "")
	if _, err := Adapters("live", "https://merchant.test"); err == nil {
		t.Fatal("mock credentials silently enabled")
	}
}
func TestJSONRejectsTrailingAndDuplicateKeys(t *testing.T) {
	for _, raw := range []string{`{"a":1}junk`, `{"a":1,"a":2}`} {
		if _, err := jsonMap([]byte(raw)); err == nil {
			t.Fatal(raw)
		}
	}
}
func TestAlchemyAPIQuerySignatureUsesSortedRawValues(t *testing.T) {
	q := url.Values{"callbackUrl": {"https://merchant.test/callback?x=1&y=2"}, "appId": {"app"}, "sign": {"ignored"}, "empty": {""}}
	got := alchemyQuery(q)
	if !strings.HasPrefix(got, "appId=app&callbackUrl=https://") || strings.Contains(got, "sign=") {
		t.Fatal(got)
	}
}

func TestTransakTokenCacheSurvivesRestart(t *testing.T) {
	e := fixture(t)
	t.Setenv("ONRAMP_PROVIDERS", "transak")
	t.Setenv("ONRAMP_ENV", "sandbox")
	t.Setenv("TRANSAK_API_KEY", "test-key")
	t.Setenv("TRANSAK_API_SECRET", "test-secret")
	if _, err := e.DB.Exec("INSERT INTO onramp_tokens VALUES(?,?,?,?)", "transak", "cached-sensitive-token", "previous-sensitive-token", time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	restored, err := New(e.DB, "live", "https://merchant.test")
	if err != nil {
		t.Fatal(err)
	}
	token, err := restored.Adapters["transak"].accessToken(context.Background())
	if err != nil || token != "cached-sensitive-token" {
		t.Fatal("cache restoration failed")
	}
}

func TestLiveRejectsPublicMockOnrampCredentials(t *testing.T) {
	for _, tc := range []struct{ name, key, secret, webhook string }{
		{"moonpay", "MOONPAY_PUBLIC_KEY", "MOONPAY_SECRET_KEY", "MOONPAY_WEBHOOK_KEY"},
		{"alchemypay", "ALCHEMYPAY_APP_ID", "ALCHEMYPAY_APP_SECRET", ""},
		{"transak", "TRANSAK_API_KEY", "TRANSAK_API_SECRET", ""},
		{"ramp", "RAMP_HOST_API_KEY", "", ""},
	} {
		for _, field := range []string{"key", "secret", "webhook"} {
			if (field == "secret" && tc.secret == "") || (field == "webhook" && tc.webhook == "") {
				continue
			}
			t.Run(tc.name+"/"+field, func(t *testing.T) {
				t.Setenv("ONRAMP_PROVIDERS", tc.name)
				t.Setenv(tc.key, "merchant-key")
				if tc.secret != "" {
					t.Setenv(tc.secret, "merchant-secret")
				}
				if tc.webhook != "" {
					t.Setenv(tc.webhook, "merchant-webhook-secret")
				}
				switch field {
				case "key":
					t.Setenv(tc.key, "mock-public-"+tc.name)
				case "secret":
					t.Setenv(tc.secret, "mock-secret-"+tc.name)
				case "webhook":
					t.Setenv(tc.webhook, "mock-webhook-"+tc.name)
				}
				if _, err := Adapters("live", "https://merchant.test"); err == nil {
					t.Fatal("public mock credential accepted")
				}
			})
		}
	}
}
