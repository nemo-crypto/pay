package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nemo-crypto/pay/docs"
	"github.com/nemo-crypto/pay/internal/auth"
	"github.com/nemo-crypto/pay/internal/onramp"
	"github.com/nemo-crypto/pay/internal/payment"
	"github.com/nemo-crypto/pay/internal/service"
	"github.com/nemo-crypto/pay/internal/store"
)

type API struct {
	Service *service.Service
	Onramp  *onramp.Engine
	Auth    auth.Auth
	mu      sync.Mutex
	limits  map[string]bucket
}
type bucket struct {
	Start time.Time
	Count int
}

func New(s *service.Service) *API {
	return &API{Service: s, Auth: auth.Auth{Secret: s.Config.JWTSecret, Issuer: s.Config.Issuer, Audience: s.Config.Audience}, limits: map[string]bucket{}}
}
func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}
func fail(w http.ResponseWriter, e error) {
	a := service.MapError(e)
	write(w, a.HTTP, map[string]any{"code": a.Code, "message": a.Message, "retriable": a.Retriable, "trace_id": payment.ID("trace_"), "details": map[string]string{"order_id": a.OrderID}})
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 65536)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(v) != nil {
		return &service.Error{Code: "VALIDATION", Message: "请求 JSON 格式错误或包含未知字段", HTTP: 400}
	}
	if d.Decode(new(any)) != io.EOF {
		return &service.Error{Code: "VALIDATION", Message: "只允许一个 JSON 对象", HTTP: 400}
	}
	return nil
}
func (a *API) limit(key string, max int) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	if len(a.limits) > 5000 {
		for k, b := range a.limits {
			if now.Sub(b.Start) > time.Minute {
				delete(a.limits, k)
			}
		}
		if len(a.limits) > 10000 {
			return false
		}
	}
	b := a.limits[key]
	if now.Sub(b.Start) >= time.Minute {
		b = bucket{Start: now}
	}
	b.Count++
	a.limits[key] = b
	return b.Count <= max
}
func (a *API) secure(admin bool, fn func(http.ResponseWriter, *http.Request, *auth.Claims)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, e := a.Auth.Parse(r.Header.Get("Authorization"))
		if e != nil {
			fail(w, &service.Error{Code: "UNAUTHORIZED", Message: "请登录后操作", HTTP: 401})
			return
		}
		if c.Guest {
			fail(w, &service.Error{Code: "GUEST_NOT_ELIGIBLE", Message: "访客不可储值", HTTP: 403})
			return
		}
		if admin && c.Role != "admin" {
			fail(w, &service.Error{Code: "FORBIDDEN", Message: "需要管理员权限", HTTP: 403})
			return
		}
		key, max := c.Subject, 180
		if r.Method == "POST" {
			key += "-write"
			max = 10
		}
		if !a.limit(key, max) {
			w.Header().Set("Retry-After", "60")
			fail(w, &service.Error{Code: "RATE_LIMIT", Message: "请求过于频繁", HTTP: 429, Retriable: true})
			return
		}
		fn(w, r, c)
	}
}
func (a *API) Handler(static http.Handler) http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if e := a.Service.Store.DB.PingContext(r.Context()); e != nil {
			write(w, 503, map[string]bool{"ok": false})
			return
		}
		write(w, 200, map[string]bool{"ok": true})
	})
	m.HandleFunc("GET /topup/plans", a.plans)
	m.HandleFunc("GET /me", a.secure(false, func(w http.ResponseWriter, r *http.Request, c *auth.Claims) {
		coins, frozen, e := a.Service.Store.Account(r.Context(), c.Subject)
		if e != nil {
			fail(w, e)
			return
		}
		write(w, 200, map[string]any{"userId": c.Subject, "coins": coins, "frozen": frozen})
	}))
	m.HandleFunc("GET /me/ledger", a.secure(false, func(w http.ResponseWriter, r *http.Request, c *auth.Claims) {
		v, e := a.Service.Store.Ledger(r.Context(), c.Subject)
		if e != nil {
			fail(w, e)
			return
		}
		write(w, 200, map[string]any{"entries": v})
	}))
	m.HandleFunc("POST /topups", a.secure(false, func(w http.ResponseWriter, r *http.Request, c *auth.Claims) {
		var in service.CreateInput
		if e := decode(w, r, &in); e != nil {
			fail(w, e)
			return
		}
		v, e := a.Service.Create(r.Context(), c.Subject, r.Header.Get("Idempotency-Key"), in)
		if e != nil {
			fail(w, e)
			return
		}
		write(w, 201, v)
	}))
	m.HandleFunc("GET /topups", a.secure(false, a.list))
	m.HandleFunc("GET /topups/pending", a.secure(false, a.pending))
	m.HandleFunc("GET /topups/{id}", a.secure(false, func(w http.ResponseWriter, r *http.Request, c *auth.Claims) {
		o, e := a.Service.Owned(r.Context(), c.Subject, r.PathValue("id"))
		if e != nil {
			fail(w, e)
			return
		}
		write(w, 200, o)
	}))
	m.HandleFunc("POST /topups/{id}/cancel-hint", a.secure(false, func(w http.ResponseWriter, r *http.Request, c *auth.Claims) {
		o, e := a.Service.Owned(r.Context(), c.Subject, r.PathValue("id"))
		if e == nil {
			e = a.Service.Store.CancelHint(r.Context(), o.ID)
		}
		if e != nil {
			fail(w, e)
			return
		}
		write(w, 200, map[string]any{"ok": true, "status": o.Status})
	}))
	m.HandleFunc("POST /topups/{id}/resume", a.secure(false, a.resume))
	m.HandleFunc("POST /webhooks/{gateway}", a.webhook)
	m.HandleFunc("GET /admin/reconciliation", a.secure(true, func(w http.ResponseWriter, r *http.Request, c *auth.Claims) {
		v, e := a.Service.Store.Health(r.Context())
		if e != nil {
			fail(w, e)
			return
		}
		write(w, 200, v)
	}))
	m.HandleFunc("POST /admin/reconcile", a.secure(true, func(w http.ResponseWriter, r *http.Request, c *auth.Claims) {
		a.Service.Tick(r.Context())
		write(w, 200, map[string]bool{"ok": true})
	}))
	m.HandleFunc("POST /admin/orders/{id}/attach", a.secure(true, a.attach))
	if a.Service.Config.Mode == "demo" {
		m.HandleFunc("POST /demo/session", func(w http.ResponseWriter, r *http.Request) {
			token, e := a.Auth.Token("demo-user", "user")
			if e != nil {
				fail(w, e)
				return
			}
			write(w, 200, map[string]string{"token": token, "mode": "demo"})
		})
		m.HandleFunc("POST /demo/orders/{id}/pay", a.secure(false, a.demoPay))
	}
	if a.Onramp != nil {
		m.HandleFunc("GET /admin/onramp/reconciliation", a.secure(true, func(w http.ResponseWriter, r *http.Request, c *auth.Claims) {
			v, e := a.Onramp.Health(r.Context())
			if e != nil {
				fail(w, e)
				return
			}
			write(w, 200, v)
		}))
		m.Handle("/onramp/", a.Onramp.Handler(a.Auth, static))
		m.Handle("GET /onramp", static)
	}
	m.Handle("/guides/", docs.Handler())
	m.Handle("/", static)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		if origin := r.Header.Get("Origin"); origin != "" {
			allowed := false
			for _, o := range a.Service.Config.Origins {
				if origin == o {
					allowed = true
				}
			}
			if !allowed {
				write(w, 403, map[string]string{"code": "ORIGIN_NOT_ALLOWED"})
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Add("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Idempotency-Key")
		}
		if r.Method == "OPTIONS" {
			w.WriteHeader(204)
			return
		}
		m.ServeHTTP(w, r)
	})
}
func (a *API) plans(w http.ResponseWriter, r *http.Request) {
	g := []string{}
	for _, name := range []string{"stripe", "allscale"} {
		if a.Service.Providers[name] != nil {
			g = append(g, name)
		}
	}
	custom := map[string]any{"enabled": a.Service.Config.Custom}
	if a.Service.Config.Custom {
		custom = map[string]any{"enabled": true, "min_nt": 51000, "max_nt": 500000, "step_nt": 1000, "coins_per_nt": 2.4, "gateways": g}
	}
	write(w, 200, map[string]any{"plans": a.Service.Config.Plans, "custom": custom, "gateways": g, "mode": a.Service.Config.Mode})
}
func (a *API) list(w http.ResponseWriter, r *http.Request, c *auth.Claims) {
	status := r.URL.Query().Get("status")
	switch status {
	case "", "pending", "succeeded", "failed", "expired", "flagged", "refunded":
	default:
		fail(w, &service.Error{Code: "VALIDATION", Message: "无效状态筛选", HTTP: 400})
		return
	}
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, e := strconv.Atoi(raw)
		if e != nil || n < 1 || n > 100 {
			fail(w, &service.Error{Code: "VALIDATION", Message: "limit 必须为 1–100", HTTP: 400})
			return
		}
		limit = n
	}
	orders, e := a.Service.Store.List(r.Context(), c.Subject, status, limit)
	if e != nil {
		fail(w, e)
		return
	}
	for i := range orders {
		orders[i].Checkout = nil
	}
	write(w, 200, map[string]any{"topups": orders})
}
func (a *API) pending(w http.ResponseWriter, r *http.Request, c *auth.Claims) {
	orders, e := a.Service.Store.List(r.Context(), c.Subject, "pending", 100)
	if e != nil {
		fail(w, e)
		return
	}
	for _, o := range orders {
		if !o.Abandoned && o.Ref != "" && time.Now().Before(o.ExpiresAt) {
			write(w, 200, map[string]any{"order_id": o.ID, "gateway": o.Gateway, "nt_amount": o.NTAmount, "coin_amount": o.Coins, "plan_id": o.PlanID, "ui_mode": o.Mode, "created_at": o.CreatedAt, "expires_at": o.ExpiresAt})
			return
		}
	}
	w.WriteHeader(204)
}
func (a *API) resume(w http.ResponseWriter, r *http.Request, c *auth.Claims) {
	o, e := a.Service.Owned(r.Context(), c.Subject, r.PathValue("id"))
	if e != nil {
		fail(w, e)
		return
	}
	if o.Status == "pending" && o.Ref != "" {
		if e = a.Service.Sync(r.Context(), o); e != nil {
			fail(w, &service.Error{Code: "PAYMENT_UNAVAILABLE", Message: "暂时无法核对收银台状态", HTTP: 503, Retriable: true})
			return
		}
		o, e = a.Service.Owned(r.Context(), c.Subject, o.ID)
		if e != nil {
			fail(w, e)
			return
		}
	}
	resumable := o.Gateway == "stripe" && o.Status == "pending" && !o.Abandoned && time.Now().Before(o.ExpiresAt) && o.Checkout != nil
	write(w, 200, map[string]any{"resumable": resumable, "order_id": o.ID, "status": o.Status, "gateway": o.Gateway, "plan_id": o.PlanID, "amount_nt": o.NTAmount, "checkout": map[bool]*payment.Checkout{true: o.Checkout, false: nil}[resumable]})
}
func (a *API) ingest(ctx context.Context, r *http.Request, b []byte, gateway string) error {
	p := a.Service.Providers[gateway]
	if p == nil {
		return &service.Error{Code: "PAYMENT_UNAVAILABLE", Message: "通道未启用", HTTP: 503}
	}
	ev, e := p.Verify(r, b)
	if errors.Is(e, payment.ErrIgnored) {
		return nil
	}
	if e != nil {
		return &service.Error{Code: "INVALID_WEBHOOK", Message: "签名或通知内容无效", HTTP: 400}
	}
	if e = a.Service.Store.Inbox(ctx, gateway, ev, payment.BodyHash(b)); e != nil {
		return e
	}
	return nil
}
func (a *API) webhook(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	b, e := io.ReadAll(r.Body)
	if e != nil {
		write(w, 413, map[string]string{"code": "PAYLOAD_TOO_LARGE"})
		return
	}
	if e = a.ingest(r.Context(), r, b, r.PathValue("gateway")); e != nil {
		fail(w, e)
		return
	}
	write(w, 200, map[string]bool{"received": true})
}
func (a *API) attach(w http.ResponseWriter, r *http.Request, c *auth.Claims) {
	var in struct {
		Ref string `json:"provider_ref"`
	}
	if e := decode(w, r, &in); e != nil {
		fail(w, e)
		return
	}
	o, e := a.Service.Store.Get(r.Context(), r.PathValue("id"))
	if e != nil {
		fail(w, e)
		return
	}
	if o.Ref != "" || o.Credited > 0 || in.Ref == "" || len(in.Ref) > 256 {
		fail(w, store.ErrConflict)
		return
	}
	p := a.Service.Providers[o.Gateway]
	if p == nil {
		fail(w, &service.Error{Code: "PAYMENT_UNAVAILABLE", Message: "通道未启用", HTTP: 503})
		return
	}
	o.Ref = in.Ref
	snap, e := p.Fetch(r.Context(), o)
	if e != nil {
		fail(w, e)
		return
	}
	if snap.OrderID != o.ID || snap.Amount != o.ProviderAmount || snap.Currency != o.ProviderCurrency {
		fail(w, &service.Error{Code: "PAYMENT_MISMATCH", Message: "网关订单与本地快照不匹配，拒绝绑定", HTTP: 409})
		return
	}
	if e = a.Service.Store.Attach(r.Context(), o.ID, payment.Session{Ref: in.Ref}); e != nil {
		fail(w, e)
		return
	}
	if e = a.Service.Store.Apply(r.Context(), o.Gateway, snap, ""); e != nil {
		fail(w, e)
		return
	}
	write(w, 200, map[string]bool{"ok": true})
}
func (a *API) demoPay(w http.ResponseWriter, r *http.Request, c *auth.Claims) {
	var in struct {
		Outcome string `json:"outcome"`
		Deliver *bool  `json:"deliver_webhook"`
	}
	if e := decode(w, r, &in); e != nil {
		fail(w, e)
		return
	}
	o, e := a.Service.Owned(r.Context(), c.Subject, r.PathValue("id"))
	if e != nil {
		fail(w, e)
		return
	}
	p, ok := a.Service.Providers[o.Gateway].(*payment.Demo)
	if !ok {
		fail(w, store.ErrNotFound)
		return
	}
	snap, e := p.Set(r.Context(), o, in.Outcome)
	if e != nil {
		fail(w, &service.Error{Code: "VALIDATION", Message: "无效演示结果", HTTP: 400})
		return
	}
	if in.Deliver == nil || *in.Deliver {
		// Allscale 只在成功时发送通知；负面状态由定时查询获得。
		if o.Gateway == "stripe" || snap.Status == "succeeded" {
			req, b := a.demoWebhook(o, snap)
			if e = a.ingest(r.Context(), req, b, o.Gateway); e != nil {
				fail(w, e)
				return
			}
			a.Service.Tick(r.Context())
		}
	}
	write(w, 200, map[string]any{"accepted": true, "message": "虚拟收银台已更新；到账以订单查询为准"})
}
func (a *API) demoWebhook(o payment.Order, s payment.Snapshot) (*http.Request, []byte) {
	id, nonce := payment.ID("evt_demo_"), payment.ID("")
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	var data any
	if o.Gateway == "stripe" {
		typ, status, paid := "checkout.session.completed", "complete", "unpaid"
		if s.Status == "succeeded" {
			paid = "paid"
		}
		if s.Status == "expired" {
			typ, status = "checkout.session.expired", "expired"
		}
		if s.Status == "failed" {
			typ = "checkout.session.async_payment_failed"
		}
		data = map[string]any{"id": id, "type": typ, "livemode": false, "data": map[string]any{"object": map[string]any{"id": o.Ref, "livemode": false, "status": status, "payment_status": paid, "amount_total": o.ProviderAmount, "currency": strings.ToLower(o.ProviderCurrency), "payment_intent": s.PaymentIntent, "metadata": map[string]string{"order_id": o.ID}}}}
	} else {
		data = map[string]any{"webhook_id": id, "all_scale_checkout_intent_id": o.Ref, "order_id": o.ID, "status": 20, "currency": 1, "amount_cents": o.ProviderAmount, "chain_id": s.Crypto.ChainID, "coin_symbol": s.Crypto.CoinSymbol, "amount_coins": s.Crypto.AmountCoins, "tx_hash": s.Crypto.TxHash}
	}
	b, _ := json.Marshal(data)
	req, _ := http.NewRequest("POST", a.Service.Config.PublicURL+"/webhooks/"+o.Gateway, strings.NewReader(string(b)))
	if o.Gateway == "stripe" {
		h := hmac.New(sha256.New, []byte(DemoStripeSecret))
		fmt.Fprintf(h, "%s.%s", ts, b)
		req.Header.Set("Stripe-Signature", "t="+ts+",v1="+hex.EncodeToString(h.Sum(nil)))
	} else {
		req.Header.Set("X-API-Key", DemoAllscaleKey)
		req.Header.Set("X-Webhook-Id", id)
		req.Header.Set("X-Webhook-Timestamp", ts)
		req.Header.Set("X-Webhook-Nonce", nonce)
		canonical := strings.Join([]string{"allscale:webhook:v1", "POST", req.URL.Path, "", id, ts, nonce, payment.BodyHash(b)}, "\n")
		req.Header.Set("X-Webhook-Signature", payment.MAC(DemoAllscaleSecret, canonical))
	}
	return req, b
}

const DemoStripeSecret = "whsec_local_demo_only"
const DemoAllscaleKey = "local-demo-key"
const DemoAllscaleSecret = "local-demo-secret"
