package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/nemo-crypto/pay/internal/payment"
	"modernc.org/sqlite"
)

var ErrNotFound = errors.New("not found")
var ErrConflict = errors.New("idempotency conflict")
var ErrLimit = errors.New("daily limit")
var ErrFrozen = errors.New("account frozen")

type Store struct{ DB *sql.DB }

func Open(path string) (*Store, error) {
	if path != ":memory:" {
		if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
			return nil, e
		}
		f, e := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
		if e != nil {
			return nil, e
		}
		f.Close()
		if e = os.Chmod(path, 0600); e != nil {
			return nil, e
		}
	}
	db, e := sql.Open("sqlite", path)
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	schema := `PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON; PRAGMA busy_timeout=5000;
 CREATE TABLE IF NOT EXISTS users(id TEXT PRIMARY KEY,coins INTEGER NOT NULL DEFAULT 0,frozen INTEGER NOT NULL DEFAULT 0,first_order TEXT NOT NULL DEFAULT '');
 CREATE TABLE IF NOT EXISTS orders(id TEXT PRIMARY KEY,user_id TEXT NOT NULL REFERENCES users(id),gateway TEXT NOT NULL,ref TEXT,payment_intent TEXT,status TEXT NOT NULL,nt_amount INTEGER NOT NULL,created INTEGER NOT NULL,next_check INTEGER NOT NULL,reuse_hash TEXT NOT NULL,abandoned INTEGER NOT NULL DEFAULT 0,lease_until INTEGER NOT NULL,data TEXT NOT NULL, UNIQUE(gateway,ref));
 CREATE INDEX IF NOT EXISTS order_user ON orders(user_id,created);
 CREATE INDEX IF NOT EXISTS order_ref ON orders(gateway,payment_intent);
 CREATE INDEX IF NOT EXISTS order_due ON orders(next_check,created) WHERE status='pending' OR (ref IS NOT NULL AND status IN ('failed','expired'));
 CREATE TABLE IF NOT EXISTS requests(user_id TEXT NOT NULL,key TEXT NOT NULL,hash TEXT NOT NULL,order_id TEXT NOT NULL REFERENCES orders(id),PRIMARY KEY(user_id,key));
 CREATE TABLE IF NOT EXISTS ledger(id TEXT PRIMARY KEY,order_id TEXT NOT NULL REFERENCES orders(id),user_id TEXT NOT NULL,kind TEXT NOT NULL,delta INTEGER NOT NULL,created INTEGER NOT NULL,UNIQUE(order_id,kind));
 CREATE TABLE IF NOT EXISTS inbox(gateway TEXT NOT NULL,id TEXT NOT NULL,nonce TEXT,body_hash TEXT NOT NULL,snapshot TEXT NOT NULL,processed INTEGER NOT NULL DEFAULT 0,attempts INTEGER NOT NULL DEFAULT 0,next_attempt INTEGER NOT NULL DEFAULT 0,error TEXT NOT NULL DEFAULT '',created INTEGER NOT NULL,PRIMARY KEY(gateway,id),UNIQUE(gateway,nonce));
 CREATE INDEX IF NOT EXISTS inbox_pending ON inbox(created,next_attempt) WHERE processed=0;`
	if _, e = db.Exec(schema); e != nil {
		db.Close()
		return nil, e
	}
	return &Store{db}, nil
}

type record struct {
	Order                                                        payment.Order
	UserID, Ref, PaymentIntent, SuccessURL, CancelURL, ReturnURL string
	NextCheck                                                    time.Time
}

func encode(o payment.Order) string {
	b, _ := json.Marshal(record{o, o.UserID, o.Ref, o.PaymentIntent, o.SuccessURL, o.CancelURL, o.ReturnURL, o.NextCheck})
	return string(b)
}
func decode(data string) (payment.Order, error) {
	var r record
	e := json.Unmarshal([]byte(data), &r)
	o := r.Order
	o.UserID = r.UserID
	o.Ref = r.Ref
	o.PaymentIntent = r.PaymentIntent
	o.SuccessURL = r.SuccessURL
	o.CancelURL = r.CancelURL
	o.ReturnURL = r.ReturnURL
	o.NextCheck = r.NextCheck
	return o, e
}

type query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func get(ctx context.Context, q query, id string) (payment.Order, error) {
	var data string
	e := q.QueryRowContext(ctx, "SELECT data FROM orders WHERE id=?", id).Scan(&data)
	if errors.Is(e, sql.ErrNoRows) {
		return payment.Order{}, ErrNotFound
	}
	if e != nil {
		return payment.Order{}, e
	}
	return decode(data)
}
func (s *Store) Get(ctx context.Context, id string) (payment.Order, error) { return get(ctx, s.DB, id) }
func put(ctx context.Context, t *sql.Tx, o payment.Order) error {
	o.UpdatedAt = time.Now().UTC()
	_, e := t.ExecContext(ctx, `UPDATE orders SET ref=NULLIF(?,''),payment_intent=NULLIF(?,''),status=?,next_check=?,abandoned=?,data=? WHERE id=?`, o.Ref, o.PaymentIntent, o.Status, o.NextCheck.Unix(), o.Abandoned, encode(o), o.ID)
	return e
}
func (s *Store) Account(ctx context.Context, user string) (int64, bool, error) {
	_, e := s.DB.ExecContext(ctx, "INSERT OR IGNORE INTO users(id) VALUES(?)", user)
	if e != nil {
		return 0, false, e
	}
	var coins int64
	var frozen bool
	e = s.DB.QueryRowContext(ctx, "SELECT coins,frozen FROM users WHERE id=?", user).Scan(&coins, &frozen)
	return coins, frozen, e
}

// Reserve 将价格快照、幂等键和每日额度预留一次性落库；网络调用不持有数据库锁。
func (s *Store) Reserve(ctx context.Context, o payment.Order, key, hash string, limit int64) (payment.Order, bool, bool, bool, error) {
	t, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return o, false, false, false, e
	}
	defer t.Rollback()
	if _, e = t.ExecContext(ctx, "INSERT OR IGNORE INTO users(id) VALUES(?)", o.UserID); e != nil {
		return o, false, false, false, e
	}
	var frozen bool
	if e = t.QueryRowContext(ctx, "SELECT frozen FROM users WHERE id=?", o.UserID).Scan(&frozen); e != nil {
		return o, false, false, false, e
	}
	if frozen {
		return o, false, false, false, ErrFrozen
	}
	var oldHash, id string
	e = t.QueryRowContext(ctx, "SELECT hash,order_id FROM requests WHERE user_id=? AND key=?", o.UserID, key).Scan(&oldHash, &id)
	if e == nil {
		if hash != oldHash {
			return o, false, false, false, ErrConflict
		}
		old, e := get(ctx, t, id)
		if e != nil {
			return o, false, false, false, e
		}
		need := false
		if old.Ref == "" && old.Status == "pending" && old.Gateway == "stripe" && time.Now().Before(old.ExpiresAt.Add(-30*time.Minute)) {
			r, e := t.ExecContext(ctx, "UPDATE orders SET lease_until=? WHERE id=? AND lease_until<?", time.Now().Add(time.Minute).Unix(), id, time.Now().Unix())
			if e != nil {
				return o, false, false, false, e
			}
			n, _ := r.RowsAffected()
			need = n == 1
		}
		return old, true, false, need, t.Commit()
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return o, false, false, false, e
	}
	// 新购买意图可复用尚未放弃、仍可付款且参数完全一致的订单。
	e = t.QueryRowContext(ctx, `SELECT id FROM orders WHERE user_id=? AND reuse_hash=? AND status='pending' AND abandoned=0 AND ref IS NOT NULL ORDER BY created DESC LIMIT 1`, o.UserID, hash).Scan(&id)
	if e == nil {
		old, e := get(ctx, t, id)
		if e != nil {
			return o, false, false, false, e
		}
		if time.Now().Before(old.ExpiresAt) {
			if _, e = t.ExecContext(ctx, "INSERT INTO requests VALUES(?,?,?,?)", o.UserID, key, hash, old.ID); e != nil {
				return o, false, false, false, e
			}
			return old, false, true, false, t.Commit()
		}
	}
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return o, false, false, false, e
	}
	zone := time.FixedZone("Asia/Taipei", 8*3600)
	now := time.Now().In(zone)
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, zone)
	var sum int64
	if e = t.QueryRowContext(ctx, `SELECT COALESCE(SUM(nt_amount),0) FROM orders WHERE user_id=? AND created>=? AND created<? AND status NOT IN ('failed','expired')`, o.UserID, day.Unix(), day.Add(24*time.Hour).Unix()).Scan(&sum); e != nil {
		return o, false, false, false, e
	}
	if sum+o.NTAmount > limit {
		return o, false, false, false, ErrLimit
	}
	if _, e = t.ExecContext(ctx, `INSERT INTO orders(id,user_id,gateway,status,nt_amount,created,next_check,reuse_hash,lease_until,data) VALUES(?,?,?,?,?,?,?,?,?,?)`, o.ID, o.UserID, o.Gateway, o.Status, o.NTAmount, o.CreatedAt.Unix(), time.Now().Add(time.Minute).Unix(), hash, time.Now().Add(time.Minute).Unix(), encode(o)); e != nil {
		return o, false, false, false, e
	}
	if _, e = t.ExecContext(ctx, "INSERT INTO requests VALUES(?,?,?,?)", o.UserID, key, hash, o.ID); e != nil {
		return o, false, false, false, e
	}
	return o, false, false, true, t.Commit()
}
func (s *Store) Attach(ctx context.Context, id string, session payment.Session) error {
	t, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer t.Rollback()
	o, e := get(ctx, t, id)
	if e != nil {
		return e
	}
	if o.Ref != "" && o.Ref != session.Ref {
		return ErrConflict
	}
	o.Ref = session.Ref
	o.Checkout = nil
	if session.Checkout.URL != "" || session.Checkout.ClientSecret != "" {
		o.Checkout = &session.Checkout
	}
	o.Detail = "awaiting_payment"
	o.NextCheck = time.Now().Add(30 * time.Second)
	if o.Status == "flagged" && o.Credited == 0 {
		o.Status = "pending"
	}
	if e = put(ctx, t, o); e != nil {
		return e
	}
	return t.Commit()
}
func (s *Store) CreateUnknown(ctx context.Context, id string) error {
	t, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer t.Rollback()
	o, e := get(ctx, t, id)
	if e != nil {
		return e
	}
	if o.Ref != "" {
		return nil
	}
	o.Detail = "provider_create_unknown"
	if o.Gateway == "allscale" || !time.Now().Before(o.ExpiresAt.Add(-30*time.Minute)) {
		o.Status = "flagged"
	}
	if e = put(ctx, t, o); e != nil {
		return e
	}
	return t.Commit()
}
func (s *Store) CancelHint(ctx context.Context, id string) error {
	t, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer t.Rollback()
	o, e := get(ctx, t, id)
	if e != nil {
		return e
	}
	o.Abandoned = true
	if e = put(ctx, t, o); e != nil {
		return e
	}
	return t.Commit()
}
func (s *Store) List(ctx context.Context, user, status string, limit int) ([]payment.Order, error) {
	rows, e := s.DB.QueryContext(ctx, `SELECT data FROM orders WHERE user_id=? AND (?='' OR status=?) ORDER BY created DESC,id DESC LIMIT ?`, user, status, status, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []payment.Order{}
	for rows.Next() {
		var d string
		if e = rows.Scan(&d); e != nil {
			return nil, e
		}
		o, e := decode(d)
		if e != nil {
			return nil, e
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
func (s *Store) Due(ctx context.Context, limit int) ([]payment.Order, error) {
	rows, e := s.DB.QueryContext(ctx, `SELECT data FROM orders WHERE next_check<=? AND created>? AND (status='pending' OR (ref IS NOT NULL AND status IN ('failed','expired'))) ORDER BY next_check LIMIT ?`, time.Now().Unix(), time.Now().Add(-7*24*time.Hour).Unix(), limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []payment.Order{}
	for rows.Next() {
		var d string
		if e = rows.Scan(&d); e != nil {
			return nil, e
		}
		o, e := decode(d)
		if e != nil {
			return nil, e
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
func (s *Store) Defer(ctx context.Context, id string) error {
	_, e := s.DB.ExecContext(ctx, "UPDATE orders SET next_check=? WHERE id=?", time.Now().Add(30*time.Second).Unix(), id)
	return e
}
func (s *Store) Inbox(ctx context.Context, gateway string, event payment.Event, hash string) error {
	b, _ := json.Marshal(event.Snapshot)
	_, e := s.DB.ExecContext(ctx, `INSERT INTO inbox(gateway,id,nonce,body_hash,snapshot,created) VALUES(?,?,NULLIF(?,''),?,?,?) ON CONFLICT(gateway,id) DO NOTHING`, gateway, event.ID, event.Nonce, hash, string(b), time.Now().Unix())
	if e != nil {
		var constraint *sqlite.Error
		if errors.As(e, &constraint) && constraint.Code() == 2067 {
			return ErrConflict
		}
		return e
	}
	var stored string
	if e = s.DB.QueryRowContext(ctx, "SELECT body_hash FROM inbox WHERE gateway=? AND id=?", gateway, event.ID).Scan(&stored); e != nil {
		return e
	}
	if stored != hash {
		return ErrConflict
	}
	return nil
}

type Job struct {
	Gateway, ID string
	Snapshot    payment.Snapshot
}

func (s *Store) Jobs(ctx context.Context) ([]Job, error) {
	rows, e := s.DB.QueryContext(ctx, `SELECT gateway,id,snapshot FROM inbox WHERE processed=0 AND next_attempt<=? ORDER BY created LIMIT 100`, time.Now().Unix())
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Job{}
	for rows.Next() {
		var j Job
		var b string
		if e = rows.Scan(&j.Gateway, &j.ID, &b); e != nil {
			return nil, e
		}
		if e = json.Unmarshal([]byte(b), &j.Snapshot); e != nil {
			return nil, e
		}
		out = append(out, j)
	}
	return out, rows.Err()
}
func (s *Store) RetryJob(ctx context.Context, j Job) error {
	_, e := s.DB.ExecContext(ctx, `UPDATE inbox SET attempts=attempts+1,next_attempt=?,error='reconciliation_pending' WHERE gateway=? AND id=? AND processed=0`, time.Now().Add(30*time.Second).Unix(), j.Gateway, j.ID)
	return e
}

// Apply 订单状态、唯一流水、用户余额以及 inbox 完成标记处于同一事务。
func (s *Store) Apply(ctx context.Context, gateway string, snap payment.Snapshot, eventID string) error {
	t, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer t.Rollback()
	column := "ref"
	if snap.ByPaymentIntent {
		column = "payment_intent"
	}
	var id string
	e = t.QueryRowContext(ctx, "SELECT id FROM orders WHERE gateway=? AND "+column+"=?", gateway, snap.Ref).Scan(&id)
	if errors.Is(e, sql.ErrNoRows) {
		return ErrNotFound
	}
	if e != nil {
		return e
	}
	o, e := get(ctx, t, id)
	if e != nil {
		return e
	}
	valid := snap.Amount == o.ProviderAmount && snap.Currency == o.ProviderCurrency && (snap.OrderID == "" || snap.OrderID == o.ID)
	if !valid {
		if o.Credited == 0 && o.Status != "refunded" {
			o.Status = "flagged"
			o.Detail = "payment_mismatch"
			o.Checkout = nil
		}
	} else {
		if snap.PaymentIntent != "" {
			if o.PaymentIntent != "" && o.PaymentIntent != snap.PaymentIntent {
				return fmt.Errorf("payment intent mismatch")
			}
			o.PaymentIntent = snap.PaymentIntent
		}
		switch snap.Status {
		case "succeeded":
			if o.Status != "refunded" && o.Credited == 0 && o.Status != "flagged" {
				var first string
				if e = t.QueryRowContext(ctx, "SELECT first_order FROM users WHERE id=?", o.UserID).Scan(&first); e != nil {
					return e
				}
				o.First = first == ""
				o.Credited = o.Coins
				o.TotalCredited = o.Coins
				o.Status = "succeeded"
				o.Detail = "credited"
				o.Crypto = snap.Crypto
				o.Checkout = nil
				if _, e = t.ExecContext(ctx, `INSERT INTO ledger VALUES(?,?,?,?,?,?)`, payment.ID("led_"), o.ID, o.UserID, "credit", o.Coins, time.Now().Unix()); e != nil {
					return e
				}
				if _, e = t.ExecContext(ctx, "UPDATE users SET coins=coins+?,first_order=CASE WHEN first_order='' THEN ? ELSE first_order END WHERE id=?", o.Coins, o.ID, o.UserID); e != nil {
					return e
				}
			}
		case "refunded":
			if o.Status != "refunded" {
				if o.Credited > 0 {
					if _, e = t.ExecContext(ctx, "INSERT INTO ledger VALUES(?,?,?,?,?,?)", payment.ID("led_"), o.ID, o.UserID, "refund", -o.TotalCredited, time.Now().Unix()); e != nil {
						return e
					}
					if _, e = t.ExecContext(ctx, "UPDATE users SET coins=coins-?,frozen=CASE WHEN coins-?<0 THEN 1 ELSE frozen END WHERE id=?", o.TotalCredited, o.TotalCredited, o.UserID); e != nil {
						return e
					}
				}
				o.Status = "refunded"
				o.Detail = "full_refund_confirmed"
				o.Checkout = nil
			}
		case "flagged":
			if o.Status != "refunded" {
				o.Status = "flagged"
				o.Detail = snap.Detail
				o.Checkout = nil
				if o.Credited > 0 {
					if _, e = t.ExecContext(ctx, "UPDATE users SET frozen=1 WHERE id=?", o.UserID); e != nil {
						return e
					}
				}
			}
		case "failed", "expired":
			if o.Status == "pending" {
				o.Status = snap.Status
				o.Detail = snap.Detail
				o.Checkout = nil
			}
		case "pending":
			if o.Status == "pending" {
				o.Detail = snap.Detail
			}
		default:
			return fmt.Errorf("invalid payment state")
		}
	}
	o.NextCheck = time.Now().Add(30 * time.Second)
	if e = put(ctx, t, o); e != nil {
		return e
	}
	if eventID != "" {
		if _, e = t.ExecContext(ctx, "UPDATE inbox SET processed=1,error=? WHERE gateway=? AND id=?", map[bool]string{true: "", false: "payment_mismatch"}[valid], gateway, eventID); e != nil {
			return e
		}
	}
	return t.Commit()
}
func (s *Store) Ledger(ctx context.Context, user string) ([]map[string]any, error) {
	rows, e := s.DB.QueryContext(ctx, "SELECT order_id,kind,delta,created FROM ledger WHERE user_id=? ORDER BY created DESC LIMIT 100", user)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, kind string
		var delta, created int64
		if e = rows.Scan(&id, &kind, &delta, &created); e != nil {
			return nil, e
		}
		out = append(out, map[string]any{"order_id": id, "kind": kind, "delta": delta, "created_at": time.Unix(created, 0).UTC()})
	}
	return out, rows.Err()
}
func (s *Store) Health(ctx context.Context) (map[string]int64, error) {
	out := map[string]int64{}
	for name, q := range map[string]string{"unprocessed_events": "SELECT COUNT(*) FROM inbox WHERE processed=0", "payment_mismatches": "SELECT COUNT(*) FROM inbox WHERE error='payment_mismatch'", "review_orders": "SELECT COUNT(*) FROM orders WHERE status='flagged'", "unknown_creations": "SELECT COUNT(*) FROM orders WHERE ref IS NULL", "ledger_drift": "SELECT COUNT(*) FROM users u WHERE coins != COALESCE((SELECT SUM(delta) FROM ledger l WHERE l.user_id=u.id),0)"} {
		var n int64
		if e := s.DB.QueryRowContext(ctx, q).Scan(&n); e != nil {
			return nil, e
		}
		out[name] = n
	}
	return out, nil
}

func (s *Store) ClaimCreation(ctx context.Context, id string) (bool, error) {
	r, e := s.DB.ExecContext(ctx, `UPDATE orders SET lease_until=? WHERE id=? AND gateway='stripe' AND ref IS NULL AND status='pending' AND lease_until<? AND created>?`, time.Now().Add(time.Minute).Unix(), id, time.Now().Unix(), time.Now().Add(-23*time.Hour).Unix())
	if e != nil {
		return false, e
	}
	n, e := r.RowsAffected()
	return n == 1, e
}
