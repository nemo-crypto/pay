package store

import (
	"context"
	"github.com/nemo-crypto/pay/internal/payment"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestTwoConnectionsDuplicateCreditAndRefund(t *testing.T) {
	for _, gateway := range []string{"stripe", "allscale", "wechat"} {
		t.Run(gateway, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "pay ?# db.sqlite")
			a, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer a.DB.Close()
			b, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer b.DB.Close()
			ctx := context.Background()
			o := payment.Order{ID: "order", UserID: "alice", Gateway: gateway, Status: "pending", Coins: 2000, NTAmount: 1000, ProviderAmount: 1000, ProviderCurrency: "USD", Ref: "reference", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}
			if _, _, _, _, err = a.Reserve(ctx, o, "key", "hash", 100000); err != nil {
				t.Fatal(err)
			}
			snap := payment.Snapshot{Ref: o.Ref, OrderID: o.ID, Amount: 1000, Currency: "USD", Status: "succeeded"}
			for phase, status := range []string{"succeeded", "refunded", "succeeded"} {
				snap.Status = status
				var wg sync.WaitGroup
				for i := range 64 {
					wg.Add(1)
					go func() {
						defer wg.Done()
						s := a
						if i%2 == 1 {
							s = b
						}
						if err := s.Apply(ctx, gateway, snap, ""); err != nil {
							t.Error(err)
						}
					}()
				}
				wg.Wait()
				coins, _, err := a.Account(ctx, "alice")
				want := int64(0)
				if phase == 0 {
					want = 2000
				}
				if err != nil || coins != want {
					t.Fatalf("balance %d want %d: %v", coins, want, err)
				}
			}
			var n int
			if err := a.DB.QueryRow("SELECT COUNT(*) FROM ledger").Scan(&n); err != nil || n != 2 {
				t.Fatal(n, err)
			}
		})
	}
}
