package onramp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nemo-crypto/pay/internal/auth"
	"github.com/nemo-crypto/pay/internal/store"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestConfirmedCallbackWinsOverLateCreationFailure(t *testing.T) {
	e := fixture(t)
	a := e.Adapters["transak"]
	a.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		raw, _ := io.ReadAll(r.Body)
		m, err := jsonMap(raw)
		if err != nil {
			t.Fatal(err)
		}
		id := str(object(m, "widgetParams"), "partnerOrderId")
		o, err := get(context.Background(), e.DB, id)
		if err != nil {
			t.Fatal(err)
		}
		req, body, err := a.mockEvent(o, mockPayload(o, "completed"))
		if err != nil {
			t.Fatal(err)
		}
		if err = e.Ingest(context.Background(), req, body, "transak"); err != nil {
			t.Fatal(err)
		}
		e.Tick(context.Background())
		return nil, errors.New("provider response lost after successful callback")
	})}
	o, err := e.Create(context.Background(), "alice", "callback-before-timeout", "127.0.0.1", Input{"transak", "0x1111111111111111111111111111111111111111", 3000})
	if err != nil || o.Status != "completed" || o.Detail != "provider_confirmed" || o.Checkout != "" {
		t.Fatal(o, err)
	}
	persisted, err := e.Get(context.Background(), "alice", o.ID)
	if err != nil || persisted.Status != "completed" {
		t.Fatal(persisted, err)
	}
}
func TestInboxIsolationDoesNotStarveNewPayment(t *testing.T) {
	e := fixture(t)
	o := order(t, e, "moonpay")
	for i := 0; i < 100; i++ {
		if _, err := e.DB.Exec("INSERT INTO onramp_events(provider,event_id,order_id,snapshot,created) VALUES(?,?,?,?,?)", "moonpay", fmt.Sprintf("invalid-%03d", i), o.ID, "invalid-json", time.Now().Add(-time.Minute).Unix()); err != nil {
			t.Fatal(err)
		}
	}
	r, b, _ := e.Adapters["moonpay"].mockEvent(o, mockPayload(o, "completed"))
	if err := e.Ingest(context.Background(), r, b, "moonpay"); err != nil {
		t.Fatal(err)
	}
	e.Tick(context.Background())
	e.Tick(context.Background())
	v, err := e.Get(context.Background(), "alice", o.ID)
	if err != nil || v.Status != "completed" {
		t.Fatal(v, err)
	}
	health, err := e.Health(context.Background())
	if err != nil || health["dead_letter_events"] != 100 {
		t.Fatal(health, err)
	}
}
func TestTransientInboxFailureBacksOff(t *testing.T) {
	e := fixture(t)
	o := order(t, e, "moonpay")
	if _, err := e.DB.Exec("INSERT INTO onramp_events(provider,event_id,order_id,snapshot,created) VALUES(?,?,?,?,?)", "moonpay", "retry-test", o.ID, "{}", time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	j := inboxJob{Provider: "moonpay", ID: "retry-test", OrderID: o.ID, Attempts: 10}
	if err := e.deferEvent(context.Background(), j, errors.New("temporary database contention")); err != nil {
		t.Fatal(err)
	}
	var attempt, dead int
	var next int64
	if err := e.DB.QueryRow("SELECT attempts,dead_letter,next_attempt FROM onramp_events WHERE event_id='retry-test'").Scan(&attempt, &dead, &next); err != nil {
		t.Fatal(err)
	}
	delay := next - time.Now().Unix()
	if attempt != 1 || dead != 0 || delay < 3598 || delay > 3601 {
		t.Fatal(attempt, dead, delay)
	}
}
func TestLegacyInboxMigrationPreservesExistingEvents(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "old.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	_, err = db.DB.Exec(`CREATE TABLE onramp_events(provider TEXT NOT NULL,event_id TEXT NOT NULL,order_id TEXT NOT NULL,snapshot TEXT NOT NULL,processed INTEGER NOT NULL DEFAULT 0,created INTEGER NOT NULL,PRIMARY KEY(provider,event_id)); INSERT INTO onramp_events VALUES('moonpay','old-event','old-order','{}',0,1);`)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err = New(db.DB, "demo", "http://localhost"); err != nil {
			t.Fatal(err)
		}
	}
	var attempts, dead int
	var id string
	if err = db.DB.QueryRow("SELECT event_id,attempts,dead_letter FROM onramp_events").Scan(&id, &attempts, &dead); err != nil || id != "old-event" || attempts != 0 || dead != 0 {
		t.Fatal(id, attempts, dead, err)
	}
}
func TestActionableHTTPErrorAndUpstreamRetry(t *testing.T) {
	e := fixture(t)
	o := order(t, e, "moonpay")
	a := auth.Auth{Secret: "test-secret", Issuer: "test", Audience: "test"}
	token, _ := a.Token("alice", "user")
	h := e.Handler(a, http.NotFoundHandler())
	call := func(path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Idempotency-Key", "invalid-example")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	w := call("/onramp/orders", `{"provider":"moonpay","wallet_address":"invalid","fiat_amount_minor":3000}`)
	var result map[string]any
	json.Unmarshal(w.Body.Bytes(), &result)
	if w.Code != 400 || result["code"] != "VALIDATION" || result["retriable"] != false || result["trace_id"] == "" {
		t.Fatal(w.Code, result)
	}
	e.Adapters["moonpay"].HTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("secret-must-not-leak") })}
	w = call("/onramp/orders/"+o.ID+"/sync", "")
	json.Unmarshal(w.Body.Bytes(), &result)
	if w.Code != 503 || result["retriable"] != true || w.Header().Get("Retry-After") == "" || strings.Contains(w.Body.String(), "secret-must-not-leak") {
		t.Fatal(w.Code, w.Body.String())
	}
}
