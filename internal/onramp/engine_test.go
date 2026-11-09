package onramp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nemo-crypto/pay/internal/auth"
	"github.com/nemo-crypto/pay/internal/store"
)

func fixture(t *testing.T) *Engine {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "pay.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.DB.Close() })
	e, err := New(db.DB, "demo", "http://localhost")
	if err != nil {
		t.Fatal(err)
	}
	return e
}
func order(t *testing.T, e *Engine, name string) Order {
	t.Helper()
	o, err := e.Create(context.Background(), "alice", "purchase-"+name, "127.0.0.1", Input{name, "0x1111111111111111111111111111111111111111", 3000})
	if err != nil {
		t.Fatal(err)
	}
	if o.Checkout == "" || o.Status != "pending" {
		t.Fatal(o)
	}
	return o
}
func TestFourProviderLifecycleAndNoPlatformCredit(t *testing.T) {
	for _, name := range Names {
		t.Run(name, func(t *testing.T) {
			e := fixture(t)
			o := order(t, e, name)
			for i, action := range []string{"kyc", "pay", "deliver"} {
				var err error
				o, err = e.Advance(context.Background(), "alice", o.ID, action, true)
				if err != nil {
					t.Fatal(action, err)
				}
				if o.DemoStage != i+1 {
					t.Fatal(o)
				}
			}
			if o.Status != "completed" || o.CryptoAmount == "" || o.TxHash == "" {
				t.Fatal(o)
			}
			var n int
			if err := e.DB.QueryRow("SELECT COUNT(*) FROM ledger").Scan(&n); err != nil || n != 0 {
				t.Fatal("on-ramp must not credit top-up ledger", n, err)
			}
			b, _ := json.Marshal(o)
			for _, private := range []string{"mock-view-token", o.CallbackToken, "127.0.0.1"} {
				if strings.Contains(string(b), private) {
					t.Fatal("private field leaked")
				}
			}
		})
	}
}
func TestDroppedWebhooksReconcileEachProvider(t *testing.T) {
	for _, name := range Names {
		t.Run(name, func(t *testing.T) {
			e := fixture(t)
			o := order(t, e, name)
			for _, action := range []string{"kyc", "pay", "deliver"} {
				if _, err := e.Advance(context.Background(), "alice", o.ID, action, false); err != nil {
					t.Fatal(err)
				}
			}
			current, _ := e.Get(context.Background(), "alice", o.ID)
			if current.Status == "completed" {
				t.Fatal("unconfirmed browser action finalized order")
			}
			if _, err := e.DB.Exec("UPDATE onramp_orders SET next_check=0"); err != nil {
				t.Fatal(err)
			}
			e.Tick(context.Background())
			current, _ = e.Get(context.Background(), "alice", o.ID)
			if current.Status != "completed" {
				t.Fatal(current)
			}
		})
	}
}
func TestWebhookTamperingDuplicatesAndLateEvents(t *testing.T) {
	for _, name := range Names {
		t.Run(name, func(t *testing.T) {
			e := fixture(t)
			o := order(t, e, name)
			a := e.Adapters[name]
			r, b, err := a.mockEvent(o, mockPayload(o, "completed"))
			if err != nil {
				t.Fatal(err)
			}
			bad := append([]byte(nil), b...)
			bad = append(bad, byte('x'))
			if e.Ingest(context.Background(), r, bad, name) == nil {
				t.Fatal("invalid body accepted")
			}
			var wg sync.WaitGroup
			for i := 0; i < 16; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if err := e.Ingest(context.Background(), r, b, name); err != nil {
						t.Error(err)
					}
				}()
			}
			wg.Wait()
			e.Tick(context.Background())
			late, body, _ := a.mockEvent(o, mockPayload(o, "pending"))
			if err = e.Ingest(context.Background(), late, body, name); err != nil {
				t.Fatal(err)
			}
			e.Tick(context.Background())
			v, _ := e.Get(context.Background(), "alice", o.ID)
			if v.Status != "completed" {
				t.Fatal(v)
			}
			var n int
			e.DB.QueryRow("SELECT COUNT(*) FROM onramp_events").Scan(&n)
			if n != 2 {
				t.Fatal("duplicate event was not deduplicated", n)
			}
		})
	}
}
func TestSignedWrongWalletCannotComplete(t *testing.T) {
	for _, name := range Names {
		t.Run(name, func(t *testing.T) {
			e := fixture(t)
			o := order(t, e, name)
			wrong := o
			wrong.Wallet = "0x2222222222222222222222222222222222222222"
			r, b, _ := e.Adapters[name].mockEvent(o, mockPayload(wrong, "completed"))
			if err := e.Ingest(context.Background(), r, b, name); err != nil {
				t.Fatal(err)
			}
			e.Tick(context.Background())
			v, _ := e.Get(context.Background(), "alice", o.ID)
			if v.Status != "review" || v.CryptoAmount != "" {
				t.Fatal(v)
			}
		})
	}
}
func TestIdempotencyConcurrencyAndOwnership(t *testing.T) {
	e := fixture(t)
	var wg sync.WaitGroup
	ids := make(chan string, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o, err := e.Create(context.Background(), "alice", "concurrent-key", "127.0.0.1", Input{"moonpay", "0x1111111111111111111111111111111111111111", 3000})
			if err != nil {
				t.Error(err)
			}
			ids <- o.ID
		}()
	}
	wg.Wait()
	close(ids)
	id := ""
	for v := range ids {
		if id != "" && id != v {
			t.Fatal("duplicate orders")
		}
		id = v
	}
	if _, err := e.Create(context.Background(), "alice", "concurrent-key", "127.0.0.1", Input{"ramp", "0x1111111111111111111111111111111111111111", 3000}); err != ErrConflict {
		t.Fatal(err)
	}
	if _, err := e.Get(context.Background(), "bob", id); err != ErrNotFound {
		t.Fatal(err)
	}
	if _, err := e.Advance(context.Background(), "bob", id, "kyc", true); err != ErrNotFound {
		t.Fatal(err)
	}
	if _, err := e.Advance(context.Background(), "alice", id, "deliver", true); err != ErrConflict {
		t.Fatal("out-of-order demo step accepted", err)
	}
}
func TestPersistentEventInbox(t *testing.T) {
	e := fixture(t)
	o := order(t, e, "ramp")
	r, b, _ := e.Adapters["ramp"].mockEvent(o, mockPayload(o, "completed"))
	if err := e.Ingest(context.Background(), r, b, "ramp"); err != nil {
		t.Fatal(err)
	}
	restored, err := New(e.DB, "demo", e.PublicURL)
	if err != nil {
		t.Fatal(err)
	}
	restored.Tick(context.Background())
	v, _ := restored.Get(context.Background(), "alice", o.ID)
	if v.Status != "completed" {
		t.Fatal(v)
	}
}
func TestHTTPAuthenticationAndDemoIsolation(t *testing.T) {
	e := fixture(t)
	a := auth.Auth{Secret: "test-only", Issuer: "test", Audience: "test"}
	h := e.Handler(a, http.NotFoundHandler())
	r := httptest.NewRequest("POST", "/onramp/orders", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
	token, _ := a.Token("alice", "user")
	r = httptest.NewRequest("GET", "/onramp/orders", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	e.Mode = "live"
	h = e.Handler(a, http.NotFoundHandler())
	r = httptest.NewRequest("POST", "/onramp/demo/anything/advance", strings.NewReader(`{"action":"deliver"}`))
	r.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 404 {
		t.Fatal("demo exposed in live mode", w.Code)
	}
}
func TestUnknownStatesAndMissingDeliveryProof(t *testing.T) {
	for _, name := range Names {
		o := Order{ID: "on_test", Provider: name, Wallet: "0x1111111111111111111111111111111111111111", FiatMinor: 3000}
		m := mockPayload(o, "completed")
		m["status"] = "NEW_UNKNOWN_STATE"
		if _, err := normalize(name, m); err == nil {
			t.Fatal(name)
		}
		m = mockPayload(o, "completed")
		for _, k := range []string{"cryptoTransactionId", "txHash", "transactionHash", "finalTxHash"} {
			delete(m, k)
		}
		if _, err := normalize(name, m); err == nil {
			t.Fatal("missing hash", name)
		}
	}
}
func TestExpiredCheckoutIsNotReopened(t *testing.T) {
	e := fixture(t)
	o := order(t, e, "transak")
	tx, err := e.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	o.Expires = time.Now().Add(-time.Minute)
	if err = put(context.Background(), tx, o); err != nil {
		t.Fatal(err)
	}
	tx.Commit()
	v, err := e.Get(context.Background(), "alice", o.ID)
	if err != nil || v.Checkout != "" {
		t.Fatal(v, err)
	}
}

func TestFailureAndCancellationDoNotDeliver(t *testing.T) {
	for _, name := range Names {
		for _, action := range []string{"fail", "cancel"} {
			t.Run(name+"/"+action, func(t *testing.T) {
				e := fixture(t)
				o := order(t, e, name)
				v, err := e.Advance(context.Background(), "alice", o.ID, action, true)
				if err != nil {
					t.Fatal(err)
				}
				if v.Status != "failed" && v.Status != "cancelled" {
					t.Fatal(v)
				}
				if v.CryptoAmount != "" || v.TxHash != "" {
					t.Fatal("failure delivered assets")
				}
				if _, err = e.Advance(context.Background(), "alice", o.ID, "deliver", true); err != ErrConflict {
					t.Fatal(err)
				}
			})
		}
	}
}
func TestRampShortAssetCodeRequiresEthereumContract(t *testing.T) {
	o := Order{ID: "test", Provider: "ramp", Wallet: "0x1111111111111111111111111111111111111111", FiatMinor: 3000}
	raw, _ := json.Marshal(mockPayload(o, "completed"))
	m, _ := jsonMap(raw)
	s, err := normalize("ramp", m)
	if err != nil || s.Network != "ethereum" || s.Asset != "USDC" {
		t.Fatal(s, err)
	}
	asset := object(m, "asset")
	asset["address"] = "0x2222222222222222222222222222222222222222"
	s, err = normalize("ramp", m)
	if err == nil && s.Network == "ethereum" {
		t.Fatal("unknown token contract treated as Ethereum USDC")
	}
}
func TestSameProviderPurchaseCannotBindTwoOrders(t *testing.T) {
	e := fixture(t)
	o := order(t, e, "ramp")
	other, err := e.Create(context.Background(), "alice", "second-ramp-purchase", "127.0.0.1", Input{"ramp", o.Wallet, 3000})
	if err != nil {
		t.Fatal(err)
	}
	tx, _ := e.DB.Begin()
	other.Ref = ""
	if err = put(context.Background(), tx, other); err != nil {
		t.Fatal(err)
	}
	tx.Commit()
	if _, err = e.Bind(context.Background(), "alice", other.ID, o.Ref, "mock-view-token"); err != ErrConflict {
		t.Fatal("provider purchase reused", err)
	}
}
