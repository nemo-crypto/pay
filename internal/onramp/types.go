// Package onramp 实现法币买币订单；资产交付到用户钱包，不给平台余额加点。
package onramp

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"regexp"
	"time"
)

var Names = []string{"moonpay", "alchemypay", "transak", "ramp"}
var ErrPending = errors.New("provider order not known yet")
var ErrConflict = errors.New("conflict")
var ErrNotFound = errors.New("not found")
var addressRE = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)

type Input struct {
	Provider  string `json:"provider"`
	Wallet    string `json:"wallet_address"`
	FiatMinor int64  `json:"fiat_amount_minor"`
}
type Order struct {
	ID            string    `json:"id"`
	User          string    `json:"-"`
	Provider      string    `json:"provider"`
	Wallet        string    `json:"wallet_address"`
	Fiat          string    `json:"fiat_currency"`
	FiatMinor     int64     `json:"fiat_amount_minor"`
	Asset         string    `json:"asset"`
	Network       string    `json:"network"`
	Status        string    `json:"status"`
	Detail        string    `json:"detail"`
	DemoStage     int       `json:"demo_stage"`
	Mode          string    `json:"mode"`
	Checkout      string    `json:"checkout_url,omitempty"`
	Estimate      string    `json:"estimated_crypto_amount,omitempty"`
	CryptoAmount  string    `json:"crypto_amount,omitempty"`
	TxHash        string    `json:"tx_hash,omitempty"`
	Ref           string    `json:"provider_order_id,omitempty"`
	ViewToken     string    `json:"-"`
	CallbackToken string    `json:"-"`
	ClientIP      string    `json:"-"`
	Created       time.Time `json:"created_at"`
	Updated       time.Time `json:"updated_at"`
	Expires       time.Time `json:"checkout_expires_at"`
}
type Snapshot struct {
	Ref, ExternalID, Wallet, Fiat, Asset, Network, Status, CryptoAmount, TxHash, ViewToken string
	FiatMinor                                                                              int64
}
type Event struct {
	ID       string
	Snapshot Snapshot
}
type Provider interface {
	Create(context.Context, Order) (string, error)
	Fetch(context.Context, Order) (Snapshot, error)
	Verify(*http.Request, []byte) (Event, error)
}

func validInput(in Input) error {
	known := false
	for _, n := range Names {
		known = known || in.Provider == n
	}
	if !known || !addressRE.MatchString(in.Wallet) || in.Wallet == "0x0000000000000000000000000000000000000000" || in.FiatMinor < 3000 || in.FiatMinor > 100000 {
		return fmt.Errorf("provider、EVM 钱包或金额无效（USD 30–1000）")
	}
	return nil
}
func decimalMinor(s string) (int64, error) {
	v, ok := new(big.Rat).SetString(s)
	if !ok || v.Sign() < 0 {
		return 0, fmt.Errorf("invalid amount")
	}
	v.Mul(v, big.NewRat(100, 1))
	if !v.IsInt() || !v.Num().IsInt64() {
		return 0, fmt.Errorf("amount must have cent precision")
	}
	return v.Num().Int64(), nil
}
func dollars(n int64) string { return fmt.Sprintf("%d.%02d", n/100, n%100) }
func mac(secret, s string) []byte {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(s))
	return h.Sum(nil)
}
func b64mac(secret, s string) string { return base64.StdEncoding.EncodeToString(mac(secret, s)) }
func digest(b []byte) string         { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func str(m map[string]any, k string) string {
	switch v := m[k].(type) {
	case string:
		return v
	case json.Number:
		return string(v)
	}
	return ""
}
func object(m map[string]any, k string) map[string]any { v, _ := m[k].(map[string]any); return v }
func jsonMap(b []byte) (map[string]any, error) {
	var v map[string]any
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	err := d.Decode(&v)
	if v == nil && err == nil {
		err = fmt.Errorf("expected JSON object")
	}
	return v, err
}
