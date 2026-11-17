package service

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nemo-crypto/pay/internal/config"
	"github.com/nemo-crypto/pay/internal/payment"
	"github.com/nemo-crypto/pay/internal/store"
)

func concurrentFixture(t testing.TB, gateway string) (*Service, *payment.Demo) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "pay.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.DB.Close() })
	cfg := config.Config{Mode: "demo", PublicURL: "http://localhost", Origins: []string{"http://localhost"}, DailyLimit: 100000, FXRate: "32", Plans: []config.Plan{{ID: "p1", NTAmount: 1000, Coins: 2000}}, Wechat: payment.DemoWechatConfig()}
	d := &payment.Demo{Gateway: gateway, DB: db.DB, PublicURL: cfg.PublicURL}
	if err = d.Init(); err != nil {
		t.Fatal(err)
	}
	return &Service{Store: db, Config: cfg, Providers: map[string]payment.Provider{gateway: d}}, d
}
func TestAllTopupConcurrentCreationAndSettlement(t *testing.T) {
	for _, gateway := range []string{"stripe", "allscale", "wechat"} {
		t.Run(gateway, func(t *testing.T) {
			s, d := concurrentFixture(t, gateway)
			ctx := context.Background()
			in := CreateInput{PlanID: "p1", Gateway: gateway}
			if gateway == "wechat" {
				in.WechatScene = "native"
			}
			var wg sync.WaitGroup
			ids := make(chan string, 64)
			for range 64 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					r, err := s.Create(ctx, "alice", "concurrent-key", in)
					if err != nil {
						t.Error(err)
						return
					}
					ids <- r.OrderID
				}()
			}
			wg.Wait()
			close(ids)
			id := ""
			for got := range ids {
				if id != "" && got != id {
					t.Fatal("duplicate order")
				}
				id = got
			}
			o, err := s.Store.Get(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = d.Set(ctx, o, "succeeded"); err != nil {
				t.Fatal(err)
			}
			for range 64 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if err := s.Sync(ctx, o); err != nil {
						t.Error(err)
					}
				}()
			}
			wg.Wait()
			coins, _, err := s.Store.Account(ctx, "alice")
			if err != nil || coins != 2000 {
				t.Fatal(coins, err)
			}
			var n int
			if err = s.Store.DB.QueryRow("SELECT COUNT(*) FROM demo_payments").Scan(&n); err != nil || n != 1 {
				t.Fatal(n, err)
			}
		})
	}
}

// Disk SQLite + mock provider: full service create, provider success, sync and balance check.
func BenchmarkTopupLifecycle(b *testing.B) {
	for _, gateway := range []string{"stripe", "allscale", "wechat"} {
		b.Run(gateway, func(b *testing.B) {
			s, d := concurrentFixture(b, gateway)
			ctx := context.Background()
			var seq atomic.Int64
			in := CreateInput{PlanID: "p1", Gateway: gateway}
			if gateway == "wechat" {
				in.WechatScene = "native"
			}
			durations := make([]time.Duration, b.N)
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					index := seq.Add(1) - 1
					started := time.Now()
					user := fmt.Sprintf("user-%d", index)
					r, err := s.Create(ctx, user, "purchase-key", in)
					if err != nil {
						b.Error(err)
						continue
					}
					o, err := s.Store.Get(ctx, r.OrderID)
					if err != nil {
						b.Error(err)
						continue
					}
					if _, err = d.Set(ctx, o, "succeeded"); err != nil {
						b.Error(err)
						continue
					}
					if err = s.Sync(ctx, o); err != nil {
						b.Error(err)
						continue
					}
					coins, _, err := s.Store.Account(ctx, user)
					if err != nil || coins != 2000 {
						b.Errorf("balance %d: %v", coins, err)
					}
					durations[index] = time.Since(started)
				}
			})
			b.StopTimer()
			slices.Sort(durations)
			b.ReportMetric(float64(durations[(b.N-1)*95/100])/float64(time.Millisecond), "p95-ms")
			b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "flows/s")
		})
	}
}

type blockedProvider struct {
	payment.Provider
	started chan string
	release chan struct{}
	calls   atomic.Int32
}

func (p *blockedProvider) Fetch(ctx context.Context, o payment.Order) (payment.Snapshot, error) {
	p.calls.Add(1)
	p.started <- o.ID
	select {
	case <-ctx.Done():
		return payment.Snapshot{}, ctx.Err()
	case <-p.release:
	}
	return payment.Snapshot{Ref: o.Ref, OrderID: o.ID, Amount: o.ProviderAmount, Currency: o.ProviderCurrency, Status: "succeeded"}, nil
}
func TestTickRunsBoundedConcurrentFetches(t *testing.T) {
	s, d := concurrentFixture(t, "stripe")
	ctx := context.Background()
	for i := range 16 {
		if _, err := s.Create(ctx, fmt.Sprintf("user-%d", i), "purchase-key", CreateInput{PlanID: "p1", Gateway: "stripe"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Store.DB.Exec("UPDATE orders SET next_check=0"); err != nil {
		t.Fatal(err)
	}
	p := &blockedProvider{Provider: d, started: make(chan string, 16), release: make(chan struct{})}
	s.Providers["stripe"] = p
	done := make(chan struct{})
	go func() { defer close(done); s.Tick(ctx) }()
	for range 8 {
		select {
		case <-p.started:
		case <-time.After(2 * time.Second):
			close(p.release)
			<-done
			t.Fatal("slow fetch serialized reconciliation")
		}
	}
	if p.calls.Load() != 8 {
		t.Errorf("worker bound exceeded: %d", p.calls.Load())
	}
	close(p.release)
	<-done
	var n int
	if err := s.Store.DB.QueryRow("SELECT COUNT(*) FROM orders WHERE status='succeeded'").Scan(&n); err != nil || n != 16 {
		t.Fatal(n, err)
	}
}
