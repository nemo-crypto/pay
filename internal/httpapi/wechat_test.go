package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nemo-crypto/pay/internal/payment"
	"github.com/nemo-crypto/pay/internal/service"
	"github.com/nemo-crypto/pay/internal/store"
)

func setupWechat(t *testing.T) fixture {
	t.Helper()
	f := setup(t)
	f.svc.Config.Wechat = payment.DemoWechatConfig()
	p, err := payment.NewDemoWechat(f.db.DB, f.svc.Config.PublicURL)
	if err != nil {
		t.Fatal(err)
	}
	f.svc.Providers["wechat"] = p
	ids := map[string]string{}
	for _, scene := range f.svc.Config.Wechat.Scenes() {
		ids[f.svc.Config.Wechat.AppFor(scene)] = "openid-" + f.svc.Config.Wechat.AppFor(scene)
	}
	f.token, err = f.api.Auth.TokenWithWechat("alice", "user", ids)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func createWechat(t *testing.T, f fixture, scene, key string) payment.Order {
	t.Helper()
	w := f.call("POST", "/topups", fmt.Sprintf(`{"plan_id":"p1","gateway":"wechat","wechat_scene":%q}`, scene), key)
	if w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body.String())
	}
	var result service.Result
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.PaymentCurrency != "CNY" || result.PaymentAmountMinor != 22223 || result.Checkout == nil || result.Checkout.Mode != scene {
		t.Fatal(result)
	}
	o, err := f.db.Get(context.Background(), result.OrderID)
	if err != nil {
		t.Fatal(err)
	}
	if len(o.ID) > 32 || o.Ref != o.ID {
		t.Fatal("invalid merchant order identity", o.ID, o.Ref)
	}
	if scene != "mini" && scene != "jsapi" && o.WechatOpenID != "" {
		t.Fatal("scene does not require a bound payer")
	}
	return o
}

func TestWechatAllScenesCreditOnceAndReplay(t *testing.T) {
	for _, scene := range []string{"mini", "jsapi", "h5", "native", "app"} {
		t.Run(scene, func(t *testing.T) {
			f := setupWechat(t)
			o := createWechat(t, f, scene, "wechat-purchase-one")
			other := createWechat(t, f, scene, "wechat-purchase-one")
			if other.ID != o.ID {
				t.Fatal("idempotency changed order")
			}
			f.svc.Config.Wechat.TWDPerCNY = "5"
			if replay := createWechat(t, f, scene, "wechat-purchase-one"); replay.ID != o.ID {
				t.Fatal("quote change duplicated order")
			}
			p := f.svc.Providers["wechat"].(*payment.Demo)
			snap, err := p.Set(context.Background(), o, "succeeded")
			if err != nil {
				t.Fatal(err)
			}
			r, b, err := p.WechatNotification(o, snap)
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				if w := deliver(f, r, b); w.Code != http.StatusNoContent || w.Body.Len() != 0 {
					t.Fatal(w.Code, w.Body.String())
				}
				f.svc.Tick(context.Background())
			}
			balance(t, f, 2000)
			got, err := f.db.Get(context.Background(), o.ID)
			if err != nil || got.Status != "succeeded" || got.PaymentIntent == "" {
				t.Fatal(got, err)
			}
			w := f.call("GET", "/topups/"+o.ID, "", "")
			if strings.Contains(w.Body.String(), "openid-") || strings.Contains(w.Body.String(), "203.0.113") {
				t.Fatal("private payer information exposed")
			}
		})
	}
}

func TestWechatIdentityAndSceneValidation(t *testing.T) {
	f := setupWechat(t)
	for _, body := range []string{
		`{"plan_id":"p1","gateway":"wechat"}`,
		`{"plan_id":"p1","gateway":"wechat","wechat_scene":"unsupported"}`,
		`{"plan_id":"p1","gateway":"stripe","wechat_scene":"native"}`,
		`{"plan_id":"p1","gateway":"wechat","wechat_scene":"mini","openid":"injected"}`,
		`{"plan_id":"p1","gateway":"wechat","wechat_scene":"mini","ui_mode":"embedded"}`,
	} {
		if w := f.call("POST", "/topups", body, "bad-wechat-input"); w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	var err error
	f.token, err = f.api.Auth.TokenWithWechat("alice", "user", map[string]string{f.svc.Config.Wechat.AppID: "public-app-openid"})
	if err != nil {
		t.Fatal(err)
	}
	if w := f.call("POST", "/topups", `{"plan_id":"p1","gateway":"wechat","wechat_scene":"mini"}`, "wrong-app-identity"); w.Code != 403 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestWechatMismatchesDoNotCredit(t *testing.T) {
	for _, field := range []string{"app", "openid", "currency", "amount", "order"} {
		t.Run(field, func(t *testing.T) {
			f := setupWechat(t)
			o := createWechat(t, f, "mini", "wechat-mismatch-one")
			snap, err := f.svc.Providers["wechat"].(*payment.Demo).Set(context.Background(), o, "succeeded")
			if err != nil {
				t.Fatal(err)
			}
			switch field {
			case "app":
				snap.WechatAppID = f.svc.Config.Wechat.AppID
			case "openid":
				snap.WechatOpenID = "another-user"
			case "currency":
				snap.Currency = "TWD"
			case "amount":
				snap.Amount++
			case "order":
				snap.OrderID = "another-order"
			}
			if err := f.db.Apply(context.Background(), "wechat", snap, ""); err != nil {
				t.Fatal(err)
			}
			balance(t, f, 0)
			got, err := f.db.Get(context.Background(), o.ID)
			if err != nil || got.Status != "flagged" {
				t.Fatal(got, err)
			}
			if err := f.db.Attach(context.Background(), o.ID, payment.Session{Ref: o.ID}); err != nil {
				t.Fatal(err)
			}
			got, err = f.db.Get(context.Background(), o.ID)
			if err != nil || got.Status != "flagged" {
				t.Fatal("late create response cleared payment mismatch", got, err)
			}
		})
	}
}

func TestWechatLostNotificationAndEarlyCallback(t *testing.T) {
	f := setupWechat(t)
	o := createWechat(t, f, "native", "wechat-recovery-one")
	p := f.svc.Providers["wechat"].(*payment.Demo)
	if _, err := p.Set(context.Background(), o, "succeeded"); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Sync(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	balance(t, f, 2000)
	// 模拟已确认入账后下单响应才返回，不得恢复 checkout 或覆盖成功详情。
	if err := f.db.Attach(context.Background(), o.ID, payment.Session{Ref: o.ID, Checkout: payment.Checkout{Mode: "native", CodeURL: "weixin://wxpay/bizpayurl?pr=late"}}); err != nil {
		t.Fatal(err)
	}
	got, err := f.db.Get(context.Background(), o.ID)
	if err != nil || got.Status != "succeeded" || got.Detail != "credited" || got.Checkout != nil {
		t.Fatal(got, err)
	}
	if err := f.db.CreateUnknown(context.Background(), o.ID); err != nil {
		t.Fatal(err)
	}
	got, err = f.db.Get(context.Background(), o.ID)
	if err != nil || got.Detail != "credited" {
		t.Fatal("late creation failure overwrote confirmed payment", got, err)
	}
}

func TestWechatExpiredCheckoutIsNotReturned(t *testing.T) {
	f := setupWechat(t)
	o := createWechat(t, f, "h5", "wechat-expired-h5")
	past := time.Now().Add(-time.Second)
	if err := f.db.Attach(context.Background(), o.ID, payment.Session{Ref: o.ID, Checkout: payment.Checkout{Mode: "h5", URL: "https://wx.tenpay.com/expired", ExpiresAt: &past}}); err != nil {
		t.Fatal(err)
	}
	for _, request := range []struct{ method, path, body, key string }{
		{"GET", "/topups/" + o.ID, "", ""},
		{"POST", "/topups/" + o.ID + "/resume", "", ""},
		{"POST", "/topups", `{"plan_id":"p1","gateway":"wechat","wechat_scene":"h5"}`, "wechat-expired-h5"},
	} {
		w := f.call(request.method, request.path, request.body, request.key)
		if w.Code < 200 || w.Code >= 300 || !strings.Contains(w.Body.String(), `"checkout":null`) {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}

func TestWechatUnpaidQueryMayOmitPayer(t *testing.T) {
	f := setupWechat(t)
	o := createWechat(t, f, "mini", "wechat-unpaid-payer")
	snap := payment.Snapshot{Ref: o.ID, OrderID: o.ID, Status: "pending", Amount: o.ProviderAmount, Currency: "CNY", WechatAppID: o.WechatAppID}
	if err := f.db.Apply(context.Background(), "wechat", snap, ""); err != nil {
		t.Fatal(err)
	}
	got, err := f.db.Get(context.Background(), o.ID)
	if err != nil || got.Status != "pending" {
		t.Fatal("unpaid order incorrectly flagged", got, err)
	}
	snap.Status, snap.PaymentIntent = "succeeded", "wx_transaction_missing_payer"
	if err := f.db.Apply(context.Background(), "wechat", snap, ""); err != nil {
		t.Fatal(err)
	}
	got, err = f.db.Get(context.Background(), o.ID)
	if err != nil || got.Status != "flagged" {
		t.Fatal("success without required payer was accepted", got, err)
	}
	balance(t, f, 0)
}

type unknownWechat struct{ payment.Provider }

func (p unknownWechat) Create(ctx context.Context, o payment.Order) (payment.Session, error) {
	if _, err := p.Provider.Create(ctx, o); err != nil {
		return payment.Session{}, err
	}
	return payment.Session{}, &payment.Uncertain{Err: fmt.Errorf("simulated timeout after acceptance")}
}

func TestWechatCreationTimeoutKeepsQueryableMerchantOrder(t *testing.T) {
	f := setupWechat(t)
	p := f.svc.Providers["wechat"].(*payment.Demo)
	f.svc.Providers["wechat"] = unknownWechat{p}
	w := f.call("POST", "/topups", `{"plan_id":"p1","gateway":"wechat","wechat_scene":"native"}`, "wechat-timeout-one")
	if w.Code != 503 {
		t.Fatal(w.Code, w.Body.String())
	}
	orders, err := f.db.List(context.Background(), "alice", "pending", 10)
	if err != nil || len(orders) != 1 {
		t.Fatal(orders, err)
	}
	o := orders[0]
	if o.Ref != o.ID || o.Detail != "provider_create_unknown" {
		t.Fatal(o)
	}
	if _, err := p.Set(context.Background(), o, "succeeded"); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Sync(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	balance(t, f, 2000)
}

func TestWechatH5TrustedProxyIP(t *testing.T) {
	f := setupWechat(t)
	for _, tc := range []struct {
		remote, forwarded string
		proxies           []string
		want              string
	}{
		{"203.0.113.10:1234", "198.51.100.2", nil, "203.0.113.10"},
		{"127.0.0.1:1234", "198.51.100.2", []string{"127.0.0.0/8"}, "198.51.100.2"},
		{"[2001:db8::1]:1234", "198.51.100.2", nil, "2001:db8::1"},
		{"127.0.0.1:1234", "invalid", []string{"127.0.0.0/8"}, ""},
	} {
		f.svc.Config.WechatTrustedProxies = tc.proxies
		r := httptest.NewRequest("POST", "/topups", nil)
		r.RemoteAddr = tc.remote
		r.Header.Set("X-Real-IP", tc.forwarded)
		if got := f.api.wechatClientIP(r); got != tc.want {
			t.Fatalf("client IP = %q, want %q", got, tc.want)
		}
	}
}

func TestWechatIdentityPersistsAcrossDatabaseReopen(t *testing.T) {
	f := setupWechat(t)
	o := createWechat(t, f, "mini", "wechat-persistent-identity")
	var seq int
	var name, path string
	if err := f.db.DB.QueryRow("PRAGMA database_list").Scan(&seq, &name, &path); err != nil {
		t.Fatal(err)
	}
	if err := f.db.DB.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	f.db.DB = reopened.DB
	p := f.svc.Providers["wechat"].(*payment.Demo)
	p.DB = reopened.DB
	got, err := f.db.Get(context.Background(), o.ID)
	if err != nil || got.WechatAppID != o.WechatAppID || got.WechatOpenID != o.WechatOpenID || got.Ref != o.ID || got.ClientIP != o.ClientIP {
		t.Fatal("identity changed after reopen", err)
	}
	if _, err := p.Set(context.Background(), got, "succeeded"); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Sync(context.Background(), got); err != nil {
		t.Fatal(err)
	}
	balance(t, f, 2000)
}
