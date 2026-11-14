package onramp

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/nemo-crypto/pay/internal/payment"
)

type Engine struct {
	DB              *sql.DB
	Mode, PublicURL string
	Adapters        map[string]*Adapter
	worker          sync.Mutex
}
type record struct {
	Order                                    Order
	User, ViewToken, CallbackToken, ClientIP string
}

func encode(o Order) string {
	b, _ := json.Marshal(record{o, o.User, o.ViewToken, o.CallbackToken, o.ClientIP})
	return string(b)
}
func decode(s string) (Order, error) {
	var r record
	e := json.Unmarshal([]byte(s), &r)
	o := r.Order
	o.User = r.User
	o.ViewToken = r.ViewToken
	o.CallbackToken = r.CallbackToken
	o.ClientIP = r.ClientIP
	return o, e
}
func New(db *sql.DB, mode, public string) (*Engine, error) {
	adapters, e := Adapters(mode, public)
	if e != nil {
		return nil, e
	}
	_, e = db.Exec(`CREATE TABLE IF NOT EXISTS onramp_orders(id TEXT PRIMARY KEY,user_id TEXT NOT NULL,provider TEXT NOT NULL,idem TEXT NOT NULL,fingerprint TEXT NOT NULL,ref TEXT,status TEXT NOT NULL,next_check INTEGER NOT NULL,created INTEGER NOT NULL,data TEXT NOT NULL,UNIQUE(user_id,idem),UNIQUE(provider,ref));
 CREATE TABLE IF NOT EXISTS onramp_events(provider TEXT NOT NULL,event_id TEXT NOT NULL,order_id TEXT NOT NULL,snapshot TEXT NOT NULL,processed INTEGER NOT NULL DEFAULT 0,created INTEGER NOT NULL,PRIMARY KEY(provider,event_id));
 CREATE INDEX IF NOT EXISTS onramp_due ON onramp_orders(status,next_check);
 CREATE INDEX IF NOT EXISTS onramp_inbox_due ON onramp_events(processed,created);
 CREATE TABLE IF NOT EXISTS onramp_tokens(provider TEXT PRIMARY KEY,token TEXT NOT NULL,previous TEXT NOT NULL,expires INTEGER NOT NULL);
 CREATE TABLE IF NOT EXISTS onramp_mock(id TEXT PRIMARY KEY,provider TEXT NOT NULL,payload TEXT NOT NULL);`)
	if e != nil {
		return nil, e
	}
	if err := migrateInbox(db); err != nil {
		return nil, err
	}
	if a := adapters["transak"]; a != nil && mode != "demo" {
		a.TokenStore = db
		var expiry int64
		err := db.QueryRow("SELECT token,previous,expires FROM onramp_tokens WHERE provider='transak'").Scan(&a.Token, &a.PreviousToken, &expiry)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if err == nil {
			a.TokenExpires = time.Unix(expiry, 0)
		}
	}
	if mode == "demo" {
		for _, a := range adapters {
			a.HTTP = &http.Client{Timeout: 15 * time.Second, Transport: &MockTransport{DB: db, Adapter: a}}
		}
	}
	return &Engine{DB: db, Mode: mode, PublicURL: public, Adapters: adapters}, nil
}

type querier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func get(ctx context.Context, q querier, id string) (Order, error) {
	var raw string
	e := q.QueryRowContext(ctx, "SELECT data FROM onramp_orders WHERE id=?", id).Scan(&raw)
	if errors.Is(e, sql.ErrNoRows) {
		return Order{}, ErrNotFound
	}
	if e != nil {
		return Order{}, e
	}
	return decode(raw)
}
func (e *Engine) Get(ctx context.Context, user, id string) (Order, error) {
	o, err := get(ctx, e.DB, id)
	if err == nil && o.User != user {
		return Order{}, ErrNotFound
	}
	if time.Now().After(o.Expires) {
		o.Checkout = ""
	}
	return o, err
}
func put(ctx context.Context, tx *sql.Tx, o Order) error {
	o.Updated = time.Now().UTC()
	_, err := tx.ExecContext(ctx, "UPDATE onramp_orders SET ref=NULLIF(?,''),status=?,next_check=?,data=? WHERE id=?", o.Ref, o.Status, time.Now().Add(30*time.Second).Unix(), encode(o), o.ID)
	return err
}
func (e *Engine) List(ctx context.Context, user string) ([]Order, error) {
	rows, err := e.DB.QueryContext(ctx, "SELECT data FROM onramp_orders WHERE user_id=? ORDER BY created DESC LIMIT 50", user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Order{}
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		o, err := decode(raw)
		if err != nil {
			return nil, err
		}
		o.Checkout = ""
		out = append(out, o)
	}
	return out, rows.Err()
}
func (e *Engine) Create(ctx context.Context, user, key, ip string, in Input) (Order, error) {
	if err := validInput(in); err != nil {
		return Order{}, err
	}
	if len(key) < 8 || len(key) > 128 {
		return Order{}, validation("Idempotency-Key 长度须为8–128")
	}
	a := e.Adapters[in.Provider]
	if a == nil {
		return Order{}, &APIError{Code: "PROVIDER_DISABLED", Message: "当前供应商通道尚未配置，请选择已启用的通道", HTTP: 503}
	}
	in.Wallet = strings.ToLower(in.Wallet)
	b, _ := json.Marshal(in)
	hash := digest(b)
	tx, err := e.DB.BeginTx(ctx, nil)
	if err != nil {
		return Order{}, err
	}
	defer tx.Rollback()
	var id, oldHash string
	err = tx.QueryRowContext(ctx, "SELECT id,fingerprint FROM onramp_orders WHERE user_id=? AND idem=?", user, key).Scan(&id, &oldHash)
	if err == nil {
		if hash != oldHash {
			return Order{}, ErrConflict
		}
		o, err := get(ctx, tx, id)
		if err != nil {
			return Order{}, err
		}
		if time.Now().After(o.Expires) {
			o.Checkout = ""
		}
		return o, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Order{}, err
	}
	now := time.Now().UTC()
	o := Order{ID: payment.ID("on_"), User: user, Provider: in.Provider, Wallet: in.Wallet, Fiat: "USD", FiatMinor: in.FiatMinor, Asset: "USDC", Network: "ethereum", Status: "pending", Detail: "awaiting_checkout", Mode: e.Mode, Created: now, Updated: now, Expires: now.Add(5 * time.Minute), CallbackToken: payment.ID("bind_"), ClientIP: ip}
	if e.Mode == "demo" {
		o.Estimate = dollars(in.FiatMinor - 100)
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO onramp_orders VALUES(?,?,?,?,?,NULL,?,?,?,?)", o.ID, user, o.Provider, key, hash, o.Status, now.Add(time.Minute).Unix(), now.Unix(), encode(o))
	if err != nil {
		return Order{}, err
	}
	if err = tx.Commit(); err != nil {
		return Order{}, err
	}
	if e.Mode == "demo" {
		raw, _ := json.Marshal(mockPayload(o, "pending"))
		if _, err = e.DB.ExecContext(ctx, "INSERT INTO onramp_mock VALUES(?,?,?)", o.ID, o.Provider, string(raw)); err != nil {
			return o, err
		}
	}
	checkout, createErr := a.Create(ctx, o)
	tx, err = e.DB.BeginTx(context.WithoutCancel(ctx), nil)
	if err != nil {
		return o, err
	}
	defer tx.Rollback()
	// 不覆盖可能早于建单返回到达的回调。
	current, err := get(context.WithoutCancel(ctx), tx, o.ID)
	if err != nil {
		return o, err
	}
	o = current
	if createErr != nil {
		if o.Ref == "" && o.Status == "pending" && o.Detail == "awaiting_checkout" {
			o.Detail = "checkout_creation_unknown"
			o.Status = "review"
		}
	} else if o.Status == "pending" {
		o.Checkout = checkout
		if e.Mode == "demo" {
			o.Checkout = localCheckout(e.PublicURL, o.ID)
			bound := mockRef(o)
			o.Ref = bound.Ref
			o.ViewToken = bound.ViewToken
		}
	}
	if err = put(context.WithoutCancel(ctx), tx, o); err != nil {
		return o, err
	}
	if err = tx.Commit(); err != nil {
		return o, err
	}
	return o, nil
}
func (e *Engine) Ingest(ctx context.Context, r *http.Request, b []byte, name string) error {
	a := e.Adapters[name]
	if a == nil {
		return ErrNotFound
	}
	event, err := a.Verify(r, b)
	if err != nil {
		return &APIError{HTTP: 400, Code: "INVALID_WEBHOOK", Message: "回调签名或内容无效"}
	}
	id := event.Snapshot.ExternalID
	if name == "ramp" {
		id = r.URL.Query().Get("order_id")
		o, err := get(ctx, e.DB, id)
		if err != nil {
			return err
		}
		if r.URL.Query().Get("binding") != o.CallbackToken {
			return &APIError{HTTP: 400, Code: "INVALID_WEBHOOK", Message: "回调订单关联无效"}
		}
	}
	if id == "" { // 查询已由 SDK 事件 + 服务端查询核对后绑定的订单。
		if err = e.DB.QueryRowContext(ctx, "SELECT id FROM onramp_orders WHERE provider=? AND ref=?", name, event.Snapshot.Ref).Scan(&id); err != nil {
			return ErrNotFound
		}
	}
	o, err := get(ctx, e.DB, id)
	if err != nil {
		return err
	}
	if o.Provider != name {
		return ErrConflict
	}
	raw, _ := json.Marshal(event.Snapshot)
	_, err = e.DB.ExecContext(ctx, "INSERT INTO onramp_events(provider,event_id,order_id,snapshot,processed,created) VALUES(?,?,?,?,0,?) ON CONFLICT(provider,event_id) DO NOTHING", name, event.ID, id, string(raw), time.Now().Unix())
	return err
}
func (e *Engine) apply(ctx context.Context, id string, s Snapshot, eventID string) error {
	tx, err := e.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	o, err := get(ctx, tx, id)
	if err != nil {
		return err
	}
	valid := strings.EqualFold(o.Wallet, s.Wallet) && o.Fiat == s.Fiat && o.FiatMinor == s.FiatMinor && o.Asset == s.Asset && o.Network == s.Network && (s.ExternalID == "" || s.ExternalID == o.ID) && (o.Ref == "" || o.Ref == s.Ref)
	if !valid {
		// 已交付/已退款结果不能由不匹配的快照撤销；保留原结果并隔离通知。
		if o.Status == "completed" || o.Status == "refunded" {
			return errSnapshotMismatch
		}
		o.Status = "review"
		o.Detail = "provider_fields_mismatch"
		o.Checkout = ""
	} else {
		var other string
		err = tx.QueryRowContext(ctx, "SELECT id FROM onramp_orders WHERE provider=? AND ref=? AND id!=?", o.Provider, s.Ref, o.ID).Scan(&other)
		if err == nil {
			return ErrConflict
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		o.Ref = s.Ref
		if o.Status == "review" && o.Detail == "checkout_creation_unknown" {
			o.Status = "pending"
		}
		if s.ViewToken != "" {
			o.ViewToken = s.ViewToken
		}
		if o.Status != "refunded" && o.Status != "review" {
			if o.Status != "completed" || s.Status == "refunded" {
				if !(o.Status == "processing" && s.Status == "pending") && !(o.Status == "failed" && (s.Status == "pending" || s.Status == "processing")) && !(o.Status == "cancelled" && (s.Status == "pending" || s.Status == "processing")) {
					o.Status = s.Status
					o.Detail = "provider_confirmed"
				}
			}
			if s.Status == "completed" && o.Status == "completed" {
				o.CryptoAmount = s.CryptoAmount
				o.TxHash = s.TxHash
				o.Checkout = ""
			}
			if o.Status == "failed" || o.Status == "cancelled" || o.Status == "refunded" {
				o.Checkout = ""
			}
		}
	}
	if err = put(ctx, tx, o); err != nil {
		return err
	}
	if eventID != "" {
		if _, err = tx.ExecContext(ctx, "UPDATE onramp_events SET processed=1 WHERE provider=? AND event_id=?", o.Provider, eventID); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (e *Engine) Tick(ctx context.Context) {
	if !e.worker.TryLock() {
		return
	}
	defer e.worker.Unlock()
	rows, err := e.DB.QueryContext(ctx, "SELECT provider,order_id,snapshot,event_id,attempts FROM onramp_events WHERE processed=0 AND dead_letter=0 AND next_attempt<=? ORDER BY next_attempt,created LIMIT 100", time.Now().Unix())
	if err != nil {
		return
	}
	jobs := []inboxJob{}
	for rows.Next() {
		var j inboxJob
		if rows.Scan(&j.Provider, &j.OrderID, &j.Raw, &j.ID, &j.Attempts) == nil {
			jobs = append(jobs, j)
		}
	}
	rows.Close()
	for _, j := range jobs {
		if ctx.Err() != nil {
			return
		}
		var s Snapshot
		if json.Unmarshal([]byte(j.Raw), &s) != nil {
			_ = e.deferEvent(ctx, j, errInvalidEvent)
			continue
		}
		if err := e.apply(ctx, j.OrderID, s, j.ID); err != nil {
			_ = e.deferEvent(ctx, j, err)
		}
	}
	rows, err = e.DB.QueryContext(ctx, "SELECT data FROM onramp_orders WHERE status IN ('pending','processing') AND next_check<=? AND created>? ORDER BY next_check LIMIT 50", time.Now().Unix(), time.Now().Add(-7*24*time.Hour).Unix())
	if err != nil {
		return
	}
	orders := []Order{}
	for rows.Next() {
		var raw string
		if rows.Scan(&raw) == nil {
			if o, err := decode(raw); err == nil {
				orders = append(orders, o)
			}
		}
	}
	rows.Close()
	for _, o := range orders {
		if ctx.Err() != nil {
			return
		}
		_, _ = e.DB.ExecContext(ctx, "UPDATE onramp_orders SET next_check=? WHERE id=?", time.Now().Add(30*time.Second).Unix(), o.ID)
		a := e.Adapters[o.Provider]
		if a == nil {
			continue
		}
		if s, err := a.Fetch(ctx, o); err == nil {
			_ = e.apply(ctx, o.ID, s, "")
		}
	}
}
func (e *Engine) Sync(ctx context.Context, user, id string) (Order, error) {
	o, err := e.Get(ctx, user, id)
	if err != nil {
		return o, err
	}
	a := e.Adapters[o.Provider]
	if a == nil {
		return o, ErrNotFound
	}
	s, err := a.Fetch(ctx, o)
	if err != nil {
		return o, providerError(err)
	}
	if err = e.apply(ctx, id, s, ""); err != nil {
		return o, err
	}
	return e.Get(ctx, user, id)
}
func (e *Engine) Bind(ctx context.Context, user, id, ref, view string) (Order, error) {
	o, err := e.Get(ctx, user, id)
	if err != nil {
		return o, err
	}
	if o.Ref != "" || ref == "" || len(ref) > 200 || len(view) > 500 {
		return o, ErrConflict
	}
	o.Ref = ref
	o.ViewToken = view
	a := e.Adapters[o.Provider]
	if a == nil {
		return o, ErrNotFound
	}
	s, err := a.Fetch(ctx, o)
	if err != nil {
		return o, providerError(err)
	}
	if s.Ref != ref {
		return o, ErrConflict
	}
	// 只在 provider GET 验证后的字段匹配时绑定；浏览器事件自身不是付款证明。
	if s.Wallet != o.Wallet && !strings.EqualFold(s.Wallet, o.Wallet) {
		return o, ErrConflict
	}
	if s.FiatMinor != o.FiatMinor || s.Fiat != o.Fiat || s.Asset != o.Asset || s.Network != o.Network || (o.Provider != "ramp" && s.ExternalID != o.ID) {
		return o, ErrConflict
	}
	if s.ViewToken == "" {
		s.ViewToken = view
	}
	if err = e.apply(ctx, id, s, ""); err != nil {
		return o, err
	}
	return e.Get(ctx, user, id)
}
func (e *Engine) Advance(ctx context.Context, user, id, action string, deliver bool) (Order, error) {
	if e.Mode != "demo" {
		return Order{}, ErrNotFound
	}
	tx, err := e.DB.BeginTx(ctx, nil)
	if err != nil {
		return Order{}, err
	}
	defer tx.Rollback()
	o, err := get(ctx, tx, id)
	if err != nil {
		return o, err
	}
	if o.User != user {
		return Order{}, ErrNotFound
	}
	if o.Status == "completed" || o.Status == "failed" || o.Status == "cancelled" || o.Status == "review" || o.DemoStage >= 4 {
		return o, ErrConflict
	}
	status := "pending"
	switch action {
	case "kyc":
		if o.DemoStage != 0 {
			return o, ErrConflict
		}
		o.DemoStage = 1
	case "pay":
		if o.DemoStage != 1 {
			return o, ErrConflict
		}
		o.DemoStage = 2
		status = "processing"
	case "deliver":
		if o.DemoStage != 2 {
			return o, ErrConflict
		}
		o.DemoStage = 3
		status = "completed"
	case "fail":
		o.DemoStage = 4
		status = "failed"
	case "cancel":
		o.DemoStage = 4
		status = "cancelled"
	default:
		return o, validation("无效的演示操作")
	}
	payload := mockPayload(o, status)
	raw, _ := json.Marshal(payload)
	if _, err = tx.ExecContext(ctx, "UPDATE onramp_mock SET payload=? WHERE id=?", string(raw), id); err != nil {
		return o, err
	}
	if err = put(ctx, tx, o); err != nil {
		return o, err
	}
	if err = tx.Commit(); err != nil {
		return o, err
	}
	if deliver {
		r, b, err := e.Adapters[o.Provider].mockEvent(o, payload)
		if err != nil {
			return o, err
		}
		if err = e.Ingest(ctx, r, b, o.Provider); err != nil {
			return o, err
		}
		e.Tick(ctx)
	}
	return e.Get(ctx, user, id)
}

func (e *Engine) Health(ctx context.Context) (map[string]int64, error) {
	result := map[string]int64{}
	for name, query := range map[string]string{"unprocessed_events": "SELECT COUNT(*) FROM onramp_events WHERE processed=0", "deferred_events": "SELECT COUNT(*) FROM onramp_events WHERE processed=0 AND dead_letter=0 AND next_attempt>strftime('%s','now')", "dead_letter_events": "SELECT COUNT(*) FROM onramp_events WHERE processed=0 AND dead_letter=1", "review_orders": "SELECT COUNT(*) FROM onramp_orders WHERE status='review'", "awaiting_provider_reference": "SELECT COUNT(*) FROM onramp_orders WHERE ref IS NULL", "stale_pending": "SELECT COUNT(*) FROM onramp_orders WHERE status IN ('pending','processing') AND created < strftime('%s','now')-604800"} {
		var n int64
		if err := e.DB.QueryRowContext(ctx, query).Scan(&n); err != nil {
			return nil, err
		}
		result[name] = n
	}
	return result, nil
}
