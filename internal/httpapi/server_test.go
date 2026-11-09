package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/nemo-crypto/pay/internal/auth"
	"github.com/nemo-crypto/pay/internal/config"
	"github.com/nemo-crypto/pay/internal/payment"
	"github.com/nemo-crypto/pay/internal/service"
	"github.com/nemo-crypto/pay/internal/store"
	"github.com/nemo-crypto/pay/web"
)

type fixture struct {
	api     *API
	handler http.Handler
	token   string
	db      *store.Store
	svc     *service.Service
}

func setup(t *testing.T) fixture {
	t.Helper()
	db, e := store.Open(filepath.Join(t.TempDir(), "pay.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.DB.Close() })
	cfg := config.Config{Mode: "demo", PublicURL: "http://localhost", JWTSecret: "test-secret-long-enough-for-this-local-fixture", Issuer: "test", Audience: "pay", Origins: []string{"http://localhost"}, DailyLimit: 100000, FXRate: "32", Custom: true, Plans: []config.Plan{{ID: "p1", NTAmount: 1000, Coins: 2000}, {ID: "p2", NTAmount: 2000, Coins: 4120}}}
	ps := map[string]payment.Provider{}
	for _, g := range []string{"stripe", "allscale"} {
		var v payment.Provider = &payment.Stripe{WebhookSecret: DemoStripeSecret}
		if g == "allscale" {
			v = &payment.Allscale{Key: DemoAllscaleKey, Secret: DemoAllscaleSecret}
		}
		d := &payment.Demo{Gateway: g, PublicURL: cfg.PublicURL, DB: db.DB, Verifier: v}
		if e = d.Init(); e != nil {
			t.Fatal(e)
		}
		ps[g] = d
	}
	svc := &service.Service{Store: db, Config: cfg, Providers: ps}
	a := New(svc)
	token, _ := a.Auth.Token("alice", "user")
	return fixture{a, a.Handler(web.Handler()), token, db, svc}
}
func (f fixture) call(method, path, body, key string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if f.token != "" {
		r.Header.Set("Authorization", "Bearer "+f.token)
	}
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	return w
}
func create(t *testing.T, f fixture, gateway, key string) payment.Order {
	t.Helper()
	w := f.call("POST", "/topups", `{"plan_id":"p1","gateway":"`+gateway+`"}`, key)
	if w.Code != 201 {
		t.Fatalf("create %d %s", w.Code, w.Body.String())
	}
	var r service.Result
	if e := json.Unmarshal(w.Body.Bytes(), &r); e != nil {
		t.Fatal(e)
	}
	o, e := f.db.Get(context.Background(), r.OrderID)
	if e != nil {
		t.Fatal(e)
	}
	return o
}
func balance(t *testing.T, f fixture, want int64) {
	t.Helper()
	v, _, e := f.db.Account(context.Background(), "alice")
	if e != nil || v != want {
		t.Fatalf("balance %d want %d error %v", v, want, e)
	}
}
func signed(t *testing.T, f fixture, o payment.Order, status string) (*http.Request, []byte) {
	t.Helper()
	snap, e := f.svc.Providers[o.Gateway].(*payment.Demo).Set(context.Background(), o, status)
	if e != nil {
		t.Fatal(e)
	}
	r, b := f.api.demoWebhook(o, snap)
	return r, b
}
func deliver(f fixture, r *http.Request, b []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", r.URL.String(), bytes.NewReader(b))
	req.Header = r.Header.Clone()
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, req)
	return w
}
func TestBothGatewaysLifecycle(t *testing.T) {
	for _, g := range []string{"stripe", "allscale"} {
		t.Run(g, func(t *testing.T) {
			f := setup(t)
			o := create(t, f, g, "purchase-one")
			if o.ProviderAmount != 100000 && g == "stripe" {
				t.Fatal("TWD cents conversion")
			}
			if g == "allscale" && o.ProviderAmount != 3125 {
				t.Fatal("fixed quote")
			}
			balance(t, f, 0)
			r, b := signed(t, f, o, "succeeded")
			w := deliver(f, r, b)
			if w.Code != 200 {
				t.Fatalf("webhook %d %s", w.Code, w.Body.String())
			}
			balance(t, f, 0)
			f.svc.Tick(context.Background())
			balance(t, f, 2000)
			got, _ := f.db.Get(context.Background(), o.ID)
			if got.Status != "succeeded" || !got.First {
				t.Fatalf("bad order %+v", got)
			}
			replay := f.call("POST", "/topups", `{"plan_id":"p1","gateway":"`+g+`"}`, "purchase-one")
			var res service.Result
			json.Unmarshal(replay.Body.Bytes(), &res)
			if !res.Replayed || res.Checkout != nil || res.Status != "succeeded" {
				t.Fatal("terminal replay exposes checkout")
			}
			health, _ := f.db.Health(context.Background())
			if health["ledger_drift"] != 0 {
				t.Fatal(health)
			}
		})
	}
}
func TestConcurrentDuplicateCallbacks(t *testing.T) {
	f := setup(t)
	o := create(t, f, "allscale", "purchase-two")
	r, b := signed(t, f, o, "succeeded")
	var wg sync.WaitGroup
	errs := make(chan int, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if code := deliver(f, r, b).Code; code != 200 {
				errs <- code
			}
		}()
	}
	wg.Wait()
	close(errs)
	for code := range errs {
		t.Errorf("callback %d", code)
	}
	f.svc.Tick(context.Background())
	for i := 0; i < 5; i++ {
		r, b = signed(t, f, o, "succeeded")
		if deliver(f, r, b).Code != 200 {
			t.Fatal("new event duplicate")
		}
		f.svc.Tick(context.Background())
	}
	balance(t, f, 2000)
	var n int
	f.db.DB.QueryRow("SELECT COUNT(*) FROM ledger WHERE order_id=?", o.ID).Scan(&n)
	if n != 1 {
		t.Fatalf("ledger entries %d", n)
	}
}
func TestIdempotencyConflictAndConcurrentCreation(t *testing.T) {
	f := setup(t)
	var wg sync.WaitGroup
	ids := make(chan string, 20)
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := f.svc.Create(context.Background(), "alice", "one-intent-key", service.CreateInput{PlanID: "p1", Gateway: "stripe"})
			if e != nil {
				errs <- e
			} else {
				ids <- r.OrderID
			}
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	seen := map[string]bool{}
	for id := range ids {
		seen[id] = true
	}
	if len(seen) != 1 {
		t.Fatal(seen)
	}
	w := f.call("POST", "/topups", `{"plan_id":"p1","gateway":"allscale"}`, "one-intent-key")
	if w.Code != 409 {
		t.Fatalf("conflict %d", w.Code)
	}
}
func TestMoneyMismatchCannotCredit(t *testing.T) {
	for _, field := range []string{"amount", "currency", "order"} {
		t.Run(field, func(t *testing.T) {
			f := setup(t)
			o := create(t, f, "stripe", "money-check")
			snap := payment.Snapshot{Ref: o.Ref, OrderID: o.ID, Status: "succeeded", Amount: o.ProviderAmount, Currency: o.ProviderCurrency}
			switch field {
			case "amount":
				snap.Amount--
			case "currency":
				snap.Currency = "USD"
			case "order":
				snap.OrderID = "another-order"
			}
			if e := f.db.Apply(context.Background(), o.Gateway, snap, ""); e != nil {
				t.Fatal(e)
			}
			balance(t, f, 0)
			o, _ = f.db.Get(context.Background(), o.ID)
			if o.Status != "flagged" {
				t.Fatal(o.Status)
			}
		})
	}
}
func TestForgedAndTamperedCallbacks(t *testing.T) {
	for _, g := range []string{"stripe", "allscale"} {
		t.Run(g, func(t *testing.T) {
			f := setup(t)
			o := create(t, f, g, "signature-key")
			r, b := signed(t, f, o, "succeeded")
			b = append(b, ' ')
			if deliver(f, r, b).Code != 400 {
				t.Fatal("tampered signature accepted")
			}
			r, b = signed(t, f, o, "succeeded")
			if g == "stripe" {
				r.Header.Set("Stripe-Signature", "t=1,v1=deadbeef")
			} else {
				r.Header.Set("X-Webhook-Timestamp", "1")
			}
			if deliver(f, r, b).Code != 400 {
				t.Fatal("stale signature accepted")
			}
			balance(t, f, 0)
		})
	}
}
func TestOwnershipGuestAndAuth(t *testing.T) {
	f := setup(t)
	o := create(t, f, "stripe", "owner-check")
	f.token, _ = f.api.Auth.Token("bob", "user")
	for _, path := range []string{"/topups/" + o.ID, "/topups/" + o.ID + "/resume", "/topups/" + o.ID + "/cancel-hint"} {
		method := "GET"
		if strings.Contains(path, "resume") || strings.Contains(path, "cancel") {
			method = "POST"
		}
		if code := f.call(method, path, "", "").Code; code != 404 {
			t.Fatalf("IDOR %s %d", path, code)
		}
	}
	if f.call("POST", "/admin/reconcile", "", "").Code != 403 {
		t.Fatal("admin privilege")
	}
	f.token = ""
	if f.call("GET", "/me", "", "").Code != 401 {
		t.Fatal("missing token")
	}
}
func TestCancelIsOnlyHintAndLatePaymentCredits(t *testing.T) {
	f := setup(t)
	o := create(t, f, "stripe", "cancel-hint")
	if f.call("POST", "/topups/"+o.ID+"/cancel-hint", "", "").Code != 200 {
		t.Fatal("cancel")
	}
	if f.call("GET", "/topups/pending", "", "").Code != 204 {
		t.Fatal("pending still visible")
	}
	r, b := signed(t, f, o, "succeeded")
	deliver(f, r, b)
	f.svc.Tick(context.Background())
	balance(t, f, 2000)
}
func TestOutOfOrderAndFullRefund(t *testing.T) {
	f := setup(t)
	o := create(t, f, "stripe", "refund-key")
	success := payment.Snapshot{Ref: o.Ref, OrderID: o.ID, PaymentIntent: "pi_order", Status: "succeeded", Amount: o.ProviderAmount, Currency: o.ProviderCurrency}
	if e := f.db.Apply(context.Background(), "stripe", success, ""); e != nil {
		t.Fatal(e)
	}
	stale := success
	stale.Status = "expired"
	f.db.Apply(context.Background(), "stripe", stale, "")
	balance(t, f, 2000)
	refund := payment.Snapshot{Ref: "pi_order", ByPaymentIntent: true, Status: "refunded", Amount: o.ProviderAmount, Currency: o.ProviderCurrency}
	for i := 0; i < 2; i++ {
		if e := f.db.Apply(context.Background(), "stripe", refund, ""); e != nil {
			t.Fatal(e)
		}
	}
	balance(t, f, 0)
	f.db.Apply(context.Background(), "stripe", success, "")
	balance(t, f, 0)
	got, _ := f.db.Get(context.Background(), o.ID)
	if got.Status != "refunded" {
		t.Fatal(got.Status)
	}
}
func TestLostWebhookReconcilesAndSurvivesRestart(t *testing.T) {
	f := setup(t)
	o := create(t, f, "allscale", "missed-event")
	f.svc.Providers["allscale"].(*payment.Demo).Set(context.Background(), o, "succeeded")
	f.db.DB.Exec("UPDATE orders SET next_check=0 WHERE id=?", o.ID)
	f.svc.Tick(context.Background())
	balance(t, f, 2000)
	var path string
	rows, e := f.db.DB.Query("PRAGMA database_list")
	if e != nil {
		t.Fatal(e)
	}
	for rows.Next() {
		var seq int
		var name string
		rows.Scan(&seq, &name, &path)
	}
	rows.Close()
	f.db.DB.Close()
	db, e := store.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer db.DB.Close()
	coins, _, e := db.Account(context.Background(), "alice")
	if e != nil || coins != 2000 {
		t.Fatal(coins, e)
	}
}
func TestReuseAndDailyReservation(t *testing.T) {
	f := setup(t)
	first := create(t, f, "stripe", "original-key")
	w := f.call("POST", "/topups", `{"plan_id":"p1","gateway":"stripe"}`, "other-key")
	var r service.Result
	json.Unmarshal(w.Body.Bytes(), &r)
	if !r.Reused || r.OrderID != first.ID {
		t.Fatal("not reused")
	}
	f.svc.Config.DailyLimit = 1000
	w = f.call("POST", "/topups", `{"plan_id":"p2","gateway":"stripe"}`, "limit-key")
	if w.Code != 429 {
		t.Fatal(w.Code)
	}
}
func TestNoSuccessFromBrowserReturnAndInputValidation(t *testing.T) {
	f := setup(t)
	create(t, f, "stripe", "return-key")
	f.call("GET", "/top-up/success?status=succeeded", "", "")
	balance(t, f, 0)
	for i, body := range []string{`{"plan_id":"p1","amount_nt":5000}`, `{"plan_id":"p1","coin_amount":999999}`, `{"plan_id":"p1","gateway":"evil"}`, `{"plan_id":"p1","success_url":"https://evil.test"}`, `{"amount_nt":51000.5}`, `{"amount_nt":-1}`} {
		if w := f.call("POST", "/topups", body, fmt.Sprintf("invalid-%d", i)); w.Code != 400 {
			t.Fatalf("invalid body %s: %d %s", body, w.Code, w.Body.String())
		}
	}
}
func TestWebhookBeforeAttachRetriesWithoutLosingMoney(t *testing.T) {
	f := setup(t)
	o := create(t, f, "stripe", "race-event")
	o.ID = "top_early_unbound"
	o.Ref = ""
	o.Checkout = nil
	if _, _, _, _, err := f.db.Reserve(context.Background(), o, "early-reserve", "unique-early-hash", 100000); err != nil {
		t.Fatal(err)
	}
	o.Ref = "cs_early"
	snap := payment.Snapshot{Ref: o.Ref, Status: "succeeded", Amount: o.ProviderAmount, Currency: o.ProviderCurrency}
	ev := payment.Event{ID: "evt_early", Snapshot: snap}
	f.db.Inbox(context.Background(), "stripe", ev, "hash")
	f.svc.Tick(context.Background())
	var processed int
	f.db.DB.QueryRow("SELECT processed FROM inbox WHERE id='evt_early'").Scan(&processed)
	if processed != 0 {
		t.Fatal("unknown reference discarded")
	}
	balance(t, f, 0)
	if err := f.db.Attach(context.Background(), o.ID, payment.Session{Ref: o.Ref}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.DB.Exec("UPDATE inbox SET next_attempt=0"); err != nil {
		t.Fatal(err)
	}
	f.svc.Tick(context.Background())
	balance(t, f, 2000)
	f.svc.Tick(context.Background())
	balance(t, f, 2000)
}

func TestSignedGuestCannotTopup(t *testing.T) {
	f := setup(t)
	c := auth.Claims{Guest: true, RegisteredClaims: jwt.RegisteredClaims{Subject: "guest", Issuer: f.api.Auth.Issuer, Audience: jwt.ClaimStrings{f.api.Auth.Audience}, ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}}
	f.token, _ = jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString([]byte(f.api.Auth.Secret))
	if w := f.call("POST", "/topups", `{"plan_id":"p1"}`, "guest-key"); w.Code != 403 {
		t.Fatal(w.Code)
	}
}
func TestInboxNonceCannotBeReusedForDifferentEvent(t *testing.T) {
	f := setup(t)
	ev := payment.Event{ID: "one", Nonce: "same-nonce"}
	if err := f.db.Inbox(context.Background(), "allscale", ev, "one-hash"); err != nil {
		t.Fatal(err)
	}
	ev.ID = "two"
	if err := f.db.Inbox(context.Background(), "allscale", ev, "two-hash"); err != store.ErrConflict {
		t.Fatal(err)
	}
}

type interruptedCreate struct {
	payment.Provider
	orders []payment.Order
}

func (p *interruptedCreate) Create(ctx context.Context, o payment.Order) (payment.Session, error) {
	p.orders = append(p.orders, o)
	if len(p.orders) == 1 {
		return payment.Session{}, &payment.Uncertain{Err: fmt.Errorf("connection lost")}
	}
	return p.Provider.Create(ctx, o)
}
func TestUnknownCreationRecoveryPolicy(t *testing.T) {
	for _, gateway := range []string{"stripe", "allscale"} {
		t.Run(gateway, func(t *testing.T) {
			f := setup(t)
			p := &interruptedCreate{Provider: f.svc.Providers[gateway]}
			f.svc.Providers[gateway] = p
			body := `{"plan_id":"p1","gateway":"` + gateway + `"}`
			if w := f.call("POST", "/topups", body, "uncertain-key"); w.Code != 503 {
				t.Fatal(w.Code)
			}
			if _, err := f.db.DB.Exec("UPDATE orders SET lease_until=0,next_check=0"); err != nil {
				t.Fatal(err)
			}
			f.svc.Tick(context.Background())
			w := f.call("POST", "/topups", body, "uncertain-key")
			if w.Code != 201 {
				t.Fatal(w.Code, w.Body.String())
			}
			var result service.Result
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if gateway == "allscale" {
				if len(p.orders) != 1 || result.Status != "flagged" || result.Checkout != nil {
					t.Fatal("uncertain Allscale checkout recreated", result)
				}
			} else {
				if len(p.orders) != 2 || p.orders[0].ID != p.orders[1].ID || !p.orders[0].ExpiresAt.Equal(p.orders[1].ExpiresAt) || result.Checkout == nil {
					t.Fatal("Stripe recovery did not preserve intent", result)
				}
			}
			balance(t, f, 0)
		})
	}
}
