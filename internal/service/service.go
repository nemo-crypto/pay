package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/nemo-crypto/pay/internal/config"
	"github.com/nemo-crypto/pay/internal/payment"
	"github.com/nemo-crypto/pay/internal/store"
)

type Service struct {
	Store     *store.Store
	Providers map[string]payment.Provider
	Config    config.Config
	worker    sync.Mutex
}
type CreateInput struct {
	PlanID     string `json:"plan_id"`
	AmountNT   int64  `json:"amount_nt"`
	Gateway    string `json:"gateway"`
	Mode       string `json:"ui_mode"`
	SuccessURL string `json:"success_url"`
	CancelURL  string `json:"cancel_url"`
	ReturnURL  string `json:"return_url"`
	Source     string `json:"source"`
	LiveRoom   string `json:"live_room"`
	TrackingID string `json:"tracking_event_id"`
}
type Result struct {
	OrderID       string            `json:"order_id"`
	Gateway       string            `json:"gateway"`
	Status        string            `json:"status"`
	Checkout      *payment.Checkout `json:"checkout"`
	Replayed      bool              `json:"replayed"`
	Reused        bool              `json:"reused"`
	NTAmount      int64             `json:"nt_amount"`
	Currency      string            `json:"currency"`
	FirstDiscount bool              `json:"first_topup_discount"`
}
type Error struct {
	Code, Message string
	HTTP          int
	Retriable     bool
	OrderID       string
}

func (e *Error) Error() string    { return e.Code }
func bad(code, msg string) *Error { return &Error{Code: code, Message: msg, HTTP: 400} }

var decimal = regexp.MustCompile(`^[0-9]+(\.[0-9]{1,8})?$`)

func Quote(nt int64, rate string) (int64, error) {
	if !decimal.MatchString(rate) {
		return 0, fmt.Errorf("invalid fixed FX quote")
	}
	r, ok := new(big.Rat).SetString(rate)
	if !ok || r.Sign() <= 0 {
		return 0, fmt.Errorf("invalid FX rate")
	}
	v := new(big.Rat).Quo(new(big.Rat).SetInt64(nt*100), r)
	q, rem := new(big.Int), new(big.Int)
	q.QuoRem(v.Num(), v.Denom(), rem)
	if rem.Sign() > 0 {
		q.Add(q, big.NewInt(1))
	}
	if !q.IsInt64() || q.Sign() <= 0 || q.Int64() > 99999999 {
		return 0, fmt.Errorf("FX quote outside limits")
	}
	return q.Int64(), nil
}
func (s *Service) returnURL(raw, fallback string) (string, error) {
	if raw == "" {
		raw = s.Config.PublicURL + fallback
	}
	u, e := url.Parse(raw)
	if e != nil || u.User != nil || u.Fragment != "" {
		return "", bad("VALIDATION", "无效的返回地址")
	}
	origin := u.Scheme + "://" + u.Host
	for _, allowed := range s.Config.Origins {
		if origin == allowed {
			return raw, nil
		}
	}
	return "", bad("VALIDATION", "返回地址不在允许列表")
}
func addOrder(raw, id string) string {
	u, _ := url.Parse(raw)
	q := u.Query()
	q.Set("order_id", id)
	u.RawQuery = q.Encode()
	return strings.ReplaceAll(u.String(), "%7BCHECKOUT_SESSION_ID%7D", "{CHECKOUT_SESSION_ID}")
}
func (s *Service) Create(ctx context.Context, user, key string, in CreateInput) (Result, error) {
	if len(key) < 8 || len(key) > 128 {
		return Result{}, bad("VALIDATION", "需要 8–128 字符的 Idempotency-Key")
	}
	if in.Gateway == "" {
		in.Gateway = "stripe"
	}
	if in.Gateway != "stripe" && in.Gateway != "allscale" {
		return Result{}, bad("VALIDATION", "不支持的支付方式")
	}
	provider, ok := s.Providers[in.Gateway]
	if !ok {
		return Result{}, &Error{Code: "PAYMENT_UNAVAILABLE", Message: "支付通道尚未配置", HTTP: 503, Retriable: true}
	}
	if in.Mode == "" {
		in.Mode = "hosted"
	}
	if in.Mode != "hosted" && in.Mode != "embedded" {
		return Result{}, bad("VALIDATION", "无效 ui_mode")
	}
	if (in.PlanID == "") == (in.AmountNT == 0) {
		return Result{}, bad("VALIDATION", "plan_id 与 amount_nt 必须二选一")
	}
	if len(in.LiveRoom) > 128 || len(in.TrackingID) > 128 {
		return Result{}, bad("VALIDATION", "字段长度超限")
	}
	if in.Source == "" {
		in.Source = "topup_panel"
	}
	switch in.Source {
	case "live_room", "topup_panel", "trial", "daily", "profile", "guess_lowbal", "guess_voucher_out":
	default:
		return Result{}, bad("VALIDATION", "无效 source")
	}
	if in.LiveRoom != "" && in.Gateway != "stripe" {
		return Result{}, bad("VALIDATION", "直播间仅支持 Stripe")
	}
	var nt, coins int64
	kind := "plan"
	if in.PlanID != "" {
		for _, p := range s.Config.Plans {
			if p.ID == in.PlanID {
				nt, coins = p.NTAmount, p.Coins
			}
		}
		if nt == 0 {
			return Result{}, bad("VALIDATION", "未知储值档位")
		}
	} else {
		kind = "custom"
		if !s.Config.Custom {
			return Result{}, &Error{Code: "CUSTOM_TOPUP_DISABLED", Message: "自定义储值未开启", HTTP: 403}
		}
		nt = in.AmountNT
		if nt < 51000 || nt > 500000 || nt%1000 != 0 {
			return Result{}, bad("CUSTOM_AMOUNT_INVALID", "金额必须为 51000–500000 的整千数")
		}
		coins = nt * 24 / 10
	}
	var e error
	if in.SuccessURL, e = s.returnURL(in.SuccessURL, "/top-up/success"); e != nil {
		return Result{}, e
	}
	if in.CancelURL, e = s.returnURL(in.CancelURL, "/top-up/cancel"); e != nil {
		return Result{}, e
	}
	if in.ReturnURL, e = s.returnURL(in.ReturnURL, "/top-up/success?session_id={CHECKOUT_SESSION_ID}"); e != nil {
		return Result{}, e
	}
	// 请求指纹与网关价格快照分离；相同购买意图不会因汇率刷新被重新收费。
	b, _ := json.Marshal(in)
	hash := payment.BodyHash(b)
	now := time.Now().UTC()
	mode := in.Mode
	if in.Gateway == "allscale" || s.Config.Mode == "demo" {
		mode = "hosted"
	}
	o := payment.Order{ID: payment.ID("top_"), UserID: user, PlanID: in.PlanID, Kind: kind, Gateway: in.Gateway, NTAmount: nt, Coins: coins, Status: "pending", Currency: "TWD", ProviderCurrency: "TWD", ProviderAmount: nt * 100, Mode: mode, Source: in.Source, LiveRoom: in.LiveRoom, CreatedAt: now, ExpiresAt: now.Add(2 * time.Hour), UpdatedAt: now, NextCheck: now.Add(time.Minute)}
	if in.Gateway == "allscale" {
		o.ProviderCurrency = "USD"
		o.FXRate = s.Config.FXRate
		o.ProviderAmount, e = Quote(nt, o.FXRate)
		if e != nil {
			return Result{}, &Error{Code: "PAYMENT_UNAVAILABLE", Message: "报价配置不可用", HTTP: 503}
		}
	}
	o.SuccessURL = addOrder(in.SuccessURL, o.ID)
	o.CancelURL = addOrder(in.CancelURL, o.ID)
	o.ReturnURL = addOrder(in.ReturnURL, o.ID)
	o, replay, reuse, create, e := s.Store.Reserve(ctx, o, key, hash, s.Config.DailyLimit)
	if e != nil {
		return Result{}, e
	}
	if create {
		session, e := provider.Create(ctx, o)
		if e != nil {
			if err := s.Store.CreateUnknown(context.WithoutCancel(ctx), o.ID); err != nil {
				return Result{}, err
			}
			return Result{OrderID: o.ID}, &Error{Code: "PAYMENT_UNAVAILABLE", Message: "网关结果尚未确定，请用原幂等键查询或重试", HTTP: 503, Retriable: o.Gateway == "stripe", OrderID: o.ID}
		}
		if e = s.Store.Attach(context.WithoutCancel(ctx), o.ID, session); e != nil {
			return Result{}, e
		}
		o, e = s.Store.Get(ctx, o.ID)
		if e != nil {
			return Result{}, e
		}
	}
	return result(o, replay, reuse), nil
}
func result(o payment.Order, replay, reuse bool) Result {
	checkout := o.Checkout
	if o.Status != "pending" || o.Abandoned || time.Now().After(o.ExpiresAt) {
		checkout = nil
	}
	return Result{o.ID, o.Gateway, o.Status, checkout, replay, reuse, o.NTAmount, o.Currency, false}
}
func (s *Service) Owned(ctx context.Context, user, id string) (payment.Order, error) {
	o, e := s.Store.Get(ctx, id)
	if e != nil {
		return o, e
	}
	if o.UserID != user {
		return payment.Order{}, store.ErrNotFound
	}
	if o.Status != "pending" || o.Abandoned || time.Now().After(o.ExpiresAt) {
		o.Checkout = nil
	}
	return o, nil
}
func (s *Service) Sync(ctx context.Context, o payment.Order) error {
	if o.Ref == "" {
		return nil
	}
	p, ok := s.Providers[o.Gateway]
	if !ok {
		return fmt.Errorf("provider unavailable")
	}
	snap, e := p.Fetch(ctx, o)
	if e != nil {
		return e
	}
	return s.Store.Apply(ctx, o.Gateway, snap, "")
}
func (s *Service) Tick(ctx context.Context) {
	if !s.worker.TryLock() {
		return
	}
	defer s.worker.Unlock()
	jobs, e := s.Store.Jobs(ctx)
	if e != nil {
		slog.Error("inbox query failed")
		return
	}
	for _, j := range jobs {
		if e = s.Store.Apply(ctx, j.Gateway, j.Snapshot, j.ID); e != nil {
			_ = s.Store.RetryJob(ctx, j)
			slog.Warn("payment event deferred", "gateway", j.Gateway, "event_id", j.ID)
		}
	}
	due, e := s.Store.Due(ctx, 50)
	if e != nil {
		slog.Error("reconcile query failed")
		return
	}
	for _, o := range due {
		if ctx.Err() != nil {
			return
		}
		_ = s.Store.Defer(ctx, o.ID)
		if o.Ref == "" {
			if o.Gateway == "stripe" {
				if !time.Now().Before(o.ExpiresAt.Add(-30 * time.Minute)) {
					_ = s.Store.CreateUnknown(ctx, o.ID)
					continue
				}
				ok, e := s.Store.ClaimCreation(ctx, o.ID)
				if e != nil || !ok {
					continue
				}
				p := s.Providers[o.Gateway]
				if p == nil {
					continue
				}
				session, e := p.Create(ctx, o)
				if e == nil {
					e = s.Store.Attach(ctx, o.ID, session)
				}
				if e != nil {
					slog.Warn("checkout recovery pending", "order_id", o.ID)
				}
			}
			continue
		}
		if e = s.Sync(ctx, o); e != nil {
			slog.Warn("payment reconciliation deferred", "order_id", o.ID)
		}
	}
}
func MapError(e error) *Error {
	var api *Error
	if errors.As(e, &api) {
		return api
	}
	switch {
	case errors.Is(e, store.ErrNotFound):
		return &Error{Code: "NOT_FOUND", Message: "订单不存在", HTTP: 404}
	case errors.Is(e, store.ErrConflict):
		return &Error{Code: "IDEMPOTENCY_CONFLICT", Message: "幂等键已用于不同请求", HTTP: 409}
	case errors.Is(e, store.ErrLimit):
		return &Error{Code: "DAILY_LIMIT", Message: "已达到台北自然日储值上限（含待付订单）", HTTP: 429}
	case errors.Is(e, store.ErrFrozen):
		return &Error{Code: "ACCOUNT_FROZEN", Message: "账户需要人工复核", HTTP: 403}
	default:
		return &Error{Code: "INTERNAL", Message: "服务暂时不可用", HTTP: 503, Retriable: true}
	}
}
