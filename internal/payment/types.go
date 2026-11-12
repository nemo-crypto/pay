package payment

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"time"
)

type Checkout struct {
	Mode         string           `json:"mode"`
	URL          string           `json:"url,omitempty"`
	ClientSecret string           `json:"client_secret,omitempty"`
	CodeURL      string           `json:"code_url,omitempty"`
	Wechat       *WechatPayParams `json:"wechat,omitempty"`
	ExpiresAt    *time.Time       `json:"expires_at,omitempty"`
}
type Crypto struct {
	ChainID     int64  `json:"chain_id"`
	CoinSymbol  string `json:"coin_symbol"`
	AmountCoins string `json:"amount_coins"`
	TxHash      string `json:"tx_hash"`
}
type Order struct {
	ID               string    `json:"id"`
	UserID           string    `json:"-"`
	PlanID           string    `json:"plan_id"`
	Kind             string    `json:"topup_kind"`
	Gateway          string    `json:"gateway"`
	WechatScene      string    `json:"wechat_scene,omitempty"`
	WechatAppID      string    `json:"-"`
	WechatOpenID     string    `json:"-"`
	ClientIP         string    `json:"-"`
	NTAmount         int64     `json:"nt_amount"`
	Coins            int64     `json:"coin_amount"`
	Credited         int64     `json:"coins_credited"`
	TotalCredited    int64     `json:"total_credited"`
	PromoCoins       int64     `json:"promo_coins"`
	First            bool      `json:"is_first_topup"`
	Status           string    `json:"status"`
	Detail           string    `json:"status_detail,omitempty"`
	Currency         string    `json:"currency"`
	ProviderCurrency string    `json:"payment_currency"`
	ProviderAmount   int64     `json:"payment_amount_minor"`
	FXRate           string    `json:"fx_rate,omitempty"`
	Ref              string    `json:"-"`
	PaymentIntent    string    `json:"-"`
	Checkout         *Checkout `json:"checkout"`
	Mode             string    `json:"ui_mode"`
	Source           string    `json:"source"`
	LiveRoom         string    `json:"live_room,omitempty"`
	SuccessURL       string    `json:"-"`
	CancelURL        string    `json:"-"`
	ReturnURL        string    `json:"-"`
	Abandoned        bool      `json:"checkout_abandoned"`
	Crypto           *Crypto   `json:"crypto"`
	CreatedAt        time.Time `json:"created_at"`
	ExpiresAt        time.Time `json:"expires_at"`
	UpdatedAt        time.Time `json:"updated_at"`
	NextCheck        time.Time `json:"-"`
}
type Session struct {
	Ref      string
	Checkout Checkout
}
type Snapshot struct {
	Ref             string  `json:"ref"`
	OrderID         string  `json:"order_id,omitempty"`
	PaymentIntent   string  `json:"payment_intent,omitempty"`
	ByPaymentIntent bool    `json:"by_payment_intent,omitempty"`
	Status          string  `json:"status"`
	Detail          string  `json:"detail,omitempty"`
	Currency        string  `json:"currency"`
	Amount          int64   `json:"amount"`
	Crypto          *Crypto `json:"crypto,omitempty"`
	WechatAppID     string  `json:"wechat_app_id,omitempty"`
	WechatOpenID    string  `json:"wechat_open_id,omitempty"`
}
type Event struct {
	ID       string
	Nonce    string
	Snapshot Snapshot
}
type Provider interface {
	Create(context.Context, Order) (Session, error)
	Fetch(context.Context, Order) (Snapshot, error)
	Verify(*http.Request, []byte) (Event, error)
}

var ErrSignature = errors.New("invalid webhook signature")
var ErrIgnored = errors.New("ignored event")

// Uncertain 表示请求可能已被网关执行，不能直接重新建单。
type Uncertain struct{ Err error }

func (e *Uncertain) Error() string { return "provider outcome unknown" }
func (e *Uncertain) Unwrap() error { return e.Err }
func ID(prefix string) string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return prefix + hex.EncodeToString(b)
}
