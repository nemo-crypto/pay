package payment

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
)

// Demo 仅保存虚拟支付，不调用真实网关。仍走真实验签、inbox 和入账事务。
type Demo struct {
	Gateway, PublicURL string
	DB                 *sql.DB
	Verifier           Provider
}

func (d *Demo) Init() error {
	_, e := d.DB.Exec(`CREATE TABLE IF NOT EXISTS demo_payments(ref TEXT PRIMARY KEY,order_id TEXT NOT NULL,snapshot TEXT NOT NULL)`)
	return e
}
func (d *Demo) Create(ctx context.Context, o Order) (Session, error) {
	ref := "demo_" + d.Gateway + "_" + o.ID
	snap := Snapshot{Ref: ref, OrderID: o.ID, Status: "pending", Amount: o.ProviderAmount, Currency: o.ProviderCurrency}
	if d.Gateway == "stripe" {
		snap.PaymentIntent = "pi_demo_" + o.ID
	}
	b, _ := json.Marshal(snap)
	_, e := d.DB.ExecContext(ctx, "INSERT OR IGNORE INTO demo_payments VALUES(?,?,?)", ref, o.ID, string(b))
	return Session{ref, Checkout{Mode: "hosted", URL: d.PublicURL + "/demo/checkout?order_id=" + o.ID}}, e
}
func (d *Demo) Fetch(ctx context.Context, o Order) (Snapshot, error) {
	var b string
	e := d.DB.QueryRowContext(ctx, "SELECT snapshot FROM demo_payments WHERE ref=?", o.Ref).Scan(&b)
	var snap Snapshot
	if e != nil {
		return snap, e
	}
	e = json.Unmarshal([]byte(b), &snap)
	return snap, e
}
func (d *Demo) Set(ctx context.Context, o Order, status string) (Snapshot, error) {
	snap, e := d.Fetch(ctx, o)
	if e != nil {
		return snap, e
	}
	if snap.Status == "succeeded" || snap.Status == "refunded" {
		return snap, nil
	}
	switch status {
	case "succeeded", "failed", "expired", "pending", "flagged":
	default:
		return snap, fmt.Errorf("unsupported demo outcome")
	}
	snap.Status = status
	if status == "pending" {
		snap.Detail = "confirming_onchain"
	}
	if status == "flagged" {
		snap.Detail = "underpaid"
	}
	if d.Gateway == "allscale" && status == "succeeded" {
		snap.Crypto = &Crypto{8453, "USDC", fmt.Sprintf("%d.%02d", o.ProviderAmount/100, o.ProviderAmount%100), "demo-not-a-real-transaction"}
	}
	b, _ := json.Marshal(snap)
	_, e = d.DB.ExecContext(ctx, "UPDATE demo_payments SET snapshot=? WHERE ref=?", string(b), o.Ref)
	return snap, e
}
func (d *Demo) Verify(r *http.Request, b []byte) (Event, error) { return d.Verifier.Verify(r, b) }
