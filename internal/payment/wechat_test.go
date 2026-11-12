package payment

import (
	"context"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

type wechatTransport func(*http.Request) (*http.Response, error)

func (f wechatTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func wechatFixture(t *testing.T) (*Wechat, *rsa.PrivateKey) {
	t.Helper()
	merchant, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	platform, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	c := DemoWechatConfig()
	return &Wechat{Config: c, NotifyURL: "https://pay.example.com/webhooks/wechat", BaseURL: "https://api.mch.weixin.qq.com", private: merchant, platform: map[string]*rsa.PublicKey{c.PlatformKeyID: &platform.PublicKey}}, platform
}

func signWechatTest(t *testing.T, key *rsa.PrivateKey, text string) string {
	t.Helper()
	h := sha256.Sum256([]byte(text))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, h[:])
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(sig)
}

func signedWechatHeaders(t *testing.T, key *rsa.PrivateKey, serial string, b []byte) http.Header {
	ts, nonce := strconv.FormatInt(time.Now().Unix(), 10), "independent-response-nonce"
	h := http.Header{}
	h.Set("Wechatpay-Timestamp", ts)
	h.Set("Wechatpay-Nonce", nonce)
	h.Set("Wechatpay-Serial", serial)
	h.Set("Wechatpay-Signature", signWechatTest(t, key, ts+"\n"+nonce+"\n"+string(b)+"\n"))
	return h
}

func verifyWechatTest(t *testing.T, pub *rsa.PublicKey, text, signature string) {
	t.Helper()
	sig, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256([]byte(text))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, h[:], sig); err != nil {
		t.Fatal("signature does not match independent canonical string", err)
	}
}

func TestWechatFiveSceneRequestsAndClientSignatures(t *testing.T) {
	w, platform := wechatFixture(t)
	for _, scene := range []string{"mini", "jsapi", "h5", "native", "app"} {
		t.Run(scene, func(t *testing.T) {
			o := Order{ID: "wx_test_order", Coins: 2000, WechatScene: scene, WechatAppID: w.Config.AppFor(scene), WechatOpenID: "bound-openid", ClientIP: "203.0.113.10", ProviderCurrency: "CNY", ProviderAmount: 22223, ReturnURL: "https://pay.example.com/top-up/success?order_id=wx_test_order", ExpiresAt: time.Now().UTC().Add(30 * time.Minute)}
			w.HTTP = &http.Client{Transport: wechatTransport(func(r *http.Request) (*http.Response, error) {
				product := scene
				if scene == "mini" {
					product = "jsapi"
				}
				if r.Method != "POST" || r.URL.Path != "/v3/pay/transactions/"+product {
					t.Fatal(r.Method, r.URL.Path)
				}
				b, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatal(err)
				}
				fields := map[string]string{}
				for _, match := range regexp.MustCompile(`([a-z_]+)="([^"]*)"`).FindAllStringSubmatch(r.Header.Get("Authorization"), -1) {
					fields[match[1]] = match[2]
				}
				if fields["mchid"] != w.Config.MchID || fields["serial_no"] != w.Config.Serial {
					t.Fatal(fields)
				}
				verifyWechatTest(t, &w.private.PublicKey, "POST\n"+r.URL.RequestURI()+"\n"+fields["timestamp"]+"\n"+fields["nonce_str"]+"\n"+string(b)+"\n", fields["signature"])
				var body map[string]json.RawMessage
				if err := json.Unmarshal(b, &body); err != nil {
					t.Fatal(err)
				}
				for key, want := range map[string]string{"appid": o.WechatAppID, "mchid": w.Config.MchID, "out_trade_no": o.ID, "notify_url": w.NotifyURL} {
					var got string
					if json.Unmarshal(body[key], &got) != nil || got != want {
						t.Fatal(key, got, want)
					}
				}
				if string(body["amount"]) != `{"currency":"CNY","total":22223}` {
					t.Fatal(string(body["amount"]))
				}
				if product == "jsapi" && string(body["payer"]) != `{"openid":"bound-openid"}` {
					t.Fatal("missing bound payer", string(body["payer"]))
				}
				if product != "jsapi" && body["payer"] != nil {
					t.Fatal("unnecessary payer identity")
				}
				if scene == "h5" && !strings.Contains(string(body["scene_info"]), `"payer_client_ip":"203.0.113.10"`) {
					t.Fatal("missing client IP")
				}
				response := `{"prepay_id":"wx_prepay_test","h5_url":"https://wx.tenpay.com/cgi-bin/mmpayweb-bin/checkmweb?prepay_id=abc","code_url":"weixin://wxpay/bizpayurl?pr=abc"}`
				return &http.Response{StatusCode: 200, Header: signedWechatHeaders(t, platform, w.Config.PlatformKeyID, []byte(response)), Body: io.NopCloser(strings.NewReader(response))}, nil
			})}
			s, err := w.Create(context.Background(), o)
			if err != nil || s.Ref != o.ID || s.Checkout.Mode != scene {
				t.Fatal(s, err)
			}
			switch scene {
			case "mini", "jsapi", "app":
				p := s.Checkout.Wechat
				if p == nil || p.SignType != "RSA" {
					t.Fatal("missing SDK parameters")
				}
				signed := "prepay_id=wx_prepay_test"
				if scene == "app" {
					signed = "wx_prepay_test"
					if p.Package != "Sign=WXPay" || p.PartnerID != w.Config.MchID || p.PrepayID != signed {
						t.Fatal(p)
					}
				} else if p.Package != signed {
					t.Fatal(p)
				}
				verifyWechatTest(t, &w.private.PublicKey, p.AppID+"\n"+p.Timestamp+"\n"+p.Nonce+"\n"+signed+"\n", p.PaySign)
			case "h5":
				if !strings.Contains(s.Checkout.URL, "redirect_url=https%3A%2F%2Fpay.example.com") {
					t.Fatal(s.Checkout.URL)
				}
			case "native":
				if s.Checkout.CodeURL != "weixin://wxpay/bizpayurl?pr=abc" {
					t.Fatal(s)
				}
			}
		})
	}
}

func wechatPlain(w *Wechat) map[string]any {
	return map[string]any{"appid": w.Config.MiniAppID, "mchid": w.Config.MchID, "out_trade_no": "wx_test_order", "transaction_id": "4200000001", "trade_state": "SUCCESS", "amount": map[string]any{"total": 22223, "payer_total": 20000, "currency": "CNY"}, "payer": map[string]string{"openid": "bound-openid"}}
}

func wechatNotificationTest(t *testing.T, w *Wechat, platform *rsa.PrivateKey, plain map[string]any, nonce string) (*http.Request, []byte) {
	t.Helper()
	b, err := json.Marshal(plain)
	if err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher([]byte(w.Config.APIv3Key))
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	const aad = "transaction"
	ciphertext := gcm.Seal(nil, []byte("123456789012"), b, []byte(aad))
	b, err = json.Marshal(map[string]any{"id": "wx_event_test", "event_type": "TRANSACTION.SUCCESS", "resource_type": "encrypt-resource", "resource": map[string]string{"algorithm": "AEAD_AES_256_GCM", "original_type": "transaction", "nonce": nonce, "associated_data": aad, "ciphertext": base64.StdEncoding.EncodeToString(ciphertext)}})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/webhooks/wechat", nil)
	r.Header = signedWechatHeaders(t, platform, w.Config.PlatformKeyID, b)
	return r, b
}

func TestWechatNotificationVerificationAndFailClosed(t *testing.T) {
	w, platform := wechatFixture(t)
	r, b := wechatNotificationTest(t, w, platform, wechatPlain(w), "123456789012")
	ev, err := w.Verify(r, b)
	if err != nil || ev.ID != "wx_event_test" || ev.Snapshot.Status != "succeeded" || ev.Snapshot.Amount != 22223 || ev.Snapshot.WechatOpenID != "bound-openid" {
		t.Fatal(ev, err)
	}
	for _, tc := range []string{"tampered", "expired", "unknown-key", "merchant", "app", "currency", "missing-total", "missing-transaction", "bad-resource-nonce", "wrong-api-key", "non-success"} {
		t.Run(tc, func(t *testing.T) {
			v, nonce := wechatPlain(w), "123456789012"
			switch tc {
			case "merchant":
				v["mchid"] = "other-merchant"
			case "app":
				v["appid"] = "unbound-app"
			case "currency":
				v["amount"] = map[string]any{"total": 22223, "currency": "USD"}
			case "missing-total":
				v["amount"] = map[string]any{"currency": "CNY"}
			case "missing-transaction":
				delete(v, "transaction_id")
			case "bad-resource-nonce":
				nonce = "short"
			case "non-success":
				v["trade_state"] = "NOTPAY"
			}
			r, body := wechatNotificationTest(t, w, platform, v, nonce)
			copyW := *w
			switch tc {
			case "tampered":
				body = append(body, ' ')
			case "expired":
				r.Header.Set("Wechatpay-Timestamp", "1")
			case "unknown-key":
				r.Header.Set("Wechatpay-Serial", "UNTRUSTED")
			case "wrong-api-key":
				copyW.Config.APIv3Key = strings.Repeat("x", 32)
			}
			if _, err := copyW.Verify(r, body); err == nil {
				t.Fatal("accepted invalid notification")
			}
		})
	}
}

func TestWechatFetchVerifiesResponseAndStates(t *testing.T) {
	w, platform := wechatFixture(t)
	for state, want := range map[string]string{"SUCCESS": "succeeded", "NOTPAY": "pending", "USERPAYING": "pending", "CLOSED": "expired", "PAYERROR": "failed", "REFUND": "flagged", "FUTURE_STATE": "flagged"} {
		t.Run(state, func(t *testing.T) {
			w.HTTP = &http.Client{Transport: wechatTransport(func(r *http.Request) (*http.Response, error) {
				if r.Method != "GET" || r.URL.RequestURI() != "/v3/pay/transactions/out-trade-no/wx_test_order?mchid="+w.Config.MchID {
					t.Fatal(r.URL)
				}
				v := wechatPlain(w)
				v["trade_state"] = state
				b, _ := json.Marshal(v)
				return &http.Response{StatusCode: 200, Header: signedWechatHeaders(t, platform, w.Config.PlatformKeyID, b), Body: io.NopCloser(strings.NewReader(string(b)))}, nil
			})}
			s, err := w.Fetch(context.Background(), Order{ID: "wx_test_order"})
			if err != nil || s.Status != want {
				t.Fatal(s, err)
			}
		})
	}
	w.HTTP = &http.Client{Transport: wechatTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"prepay_id":"fake"}`))}, nil
	})}
	if _, err := w.Create(context.Background(), Order{ID: "wx_test_order", WechatScene: "native", WechatAppID: w.Config.AppID, ProviderCurrency: "CNY", ProviderAmount: 1}); err == nil {
		t.Fatal("accepted unsigned provider response")
	}
}

func TestWechatRejectsInvalidCheckoutAndIdentity(t *testing.T) {
	w, platform := wechatFixture(t)
	for _, scene := range []string{"native", "h5"} {
		w.HTTP = &http.Client{Transport: wechatTransport(func(r *http.Request) (*http.Response, error) {
			b := []byte(`{"code_url":"https://attacker.example/qr","h5_url":"https://wx.tenpay.com.attacker.example/pay"}`)
			return &http.Response{StatusCode: 200, Header: signedWechatHeaders(t, platform, w.Config.PlatformKeyID, b), Body: io.NopCloser(strings.NewReader(string(b)))}, nil
		})}
		if _, err := w.Create(context.Background(), Order{ID: "wx_test", WechatScene: scene, WechatAppID: w.Config.AppID, ClientIP: "203.0.113.1", ProviderCurrency: "CNY", ProviderAmount: 100}); err == nil {
			t.Fatal("accepted checkout URL", scene)
		}
	}
	for _, o := range []Order{
		{ID: strings.Repeat("x", 33), WechatScene: "native", WechatAppID: w.Config.AppID, ProviderCurrency: "CNY", ProviderAmount: 1},
		{ID: "wx_test", WechatScene: "mini", WechatAppID: w.Config.MiniAppID, ProviderCurrency: "CNY", ProviderAmount: 1},
		{ID: "wx_test", WechatScene: "h5", WechatAppID: w.Config.AppID, ClientIP: "spoofed", ProviderCurrency: "CNY", ProviderAmount: 1},
	} {
		if _, err := w.Create(context.Background(), o); err == nil {
			t.Fatal(fmt.Sprint(o), "accepted invalid order")
		}
	}
}

func TestWechatPinnedKeyLoading(t *testing.T) {
	w, platform := wechatFixture(t)
	keyFile, publicFile := filepath.Join(t.TempDir(), "merchant.pem"), filepath.Join(t.TempDir(), "platform.pem")
	pub, err := x509.MarshalPKIXPublicKey(&platform.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(publicFile, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub}), 0600); err != nil {
		t.Fatal(err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(w.private)
	if err != nil {
		t.Fatal(err)
	}
	for _, block := range []*pem.Block{{Type: "PRIVATE KEY", Bytes: pkcs8}, {Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(w.private)}} {
		if err := os.WriteFile(keyFile, pem.EncodeToMemory(block), 0600); err != nil {
			t.Fatal(err)
		}
		cfg := w.Config
		cfg.PrivateKeyFile, cfg.PlatformKeyFile = keyFile, publicFile
		loaded, err := NewWechat(cfg, "https://pay.example.com")
		if err != nil || loaded.NotifyURL != "https://pay.example.com/webhooks/wechat" || !loaded.private.Equal(w.private) || !loaded.platform[cfg.PlatformKeyID].Equal(&platform.PublicKey) {
			t.Fatal("failed to load pinned RSA keys", err)
		}
	}
	if err := os.WriteFile(keyFile, []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := w.Config
	cfg.PrivateKeyFile, cfg.PlatformKeyFile = keyFile, publicFile
	if _, err := NewWechat(cfg, "https://pay.example.com"); err == nil {
		t.Fatal("accepted invalid merchant key")
	}
}
