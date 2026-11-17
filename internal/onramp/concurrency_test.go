package onramp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestEveryProviderConcurrentCreate(t *testing.T) {
	for _, name := range Names {
		t.Run(name, func(t *testing.T) {
			e := fixture(t)
			var wg sync.WaitGroup
			ids := make(chan string, 64)
			for range 64 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					o, err := e.Create(context.Background(), "alice", "concurrent-key", "127.0.0.1", Input{name, "0x1111111111111111111111111111111111111111", 3000})
					if err != nil {
						t.Error(err)
						return
					}
					ids <- o.ID
				}()
			}
			wg.Wait()
			close(ids)
			id := ""
			for got := range ids {
				if id != "" && id != got {
					t.Fatal("duplicate order")
				}
				id = got
			}
			var n int
			if err := e.DB.QueryRow("SELECT COUNT(*) FROM onramp_orders").Scan(&n); err != nil || n != 1 {
				t.Fatal(n, err)
			}
		})
	}
}
func TestTransakRefreshDoesNotHoldVerificationLock(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	a := &Adapter{API: "https://mock.invalid", Token: "old", HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		close(started)
		<-release
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(fmt.Sprintf(`{"data":{"accessToken":"new","expiresAt":%d}}`, time.Now().Add(time.Hour).Unix())))}, nil
	})}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		token, err := a.accessToken(context.Background())
		if err != nil || token != "new" {
			t.Errorf("%q %v", token, err)
		}
	}()
	<-started
	unlocked := make(chan struct{})
	go func() { a.tokenMu.Lock(); a.tokenMu.Unlock(); close(unlocked) }()
	select {
	case <-unlocked:
	case <-time.After(time.Second):
		close(release)
		<-done
		t.Fatal("webhook verification blocked by refresh")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := a.accessToken(ctx); err == nil {
		t.Error("cancelled waiter succeeded")
	}
	close(release)
	<-done
	if token, err := a.accessToken(context.Background()); err != nil || token != "new" || calls.Load() != 1 || a.PreviousToken != "old" {
		t.Fatal(token, err, calls.Load())
	}
}

// Disk SQLite + mock provider: create, three provider stages, sync and no platform credit.
func BenchmarkOnrampLifecycle(b *testing.B) {
	for _, name := range Names {
		b.Run(name, func(b *testing.B) {
			e := fixture(b)
			ctx := context.Background()
			var seq atomic.Int64
			durations := make([]time.Duration, b.N)
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					index := seq.Add(1) - 1
					started := time.Now()
					user := fmt.Sprintf("user-%d", index)
					o, err := e.Create(ctx, user, "purchase-key", "127.0.0.1", Input{name, "0x1111111111111111111111111111111111111111", 3000})
					if err != nil {
						b.Error(err)
						continue
					}
					for _, action := range []string{"kyc", "pay", "deliver"} {
						if _, err = e.Advance(ctx, user, o.ID, action, false); err != nil {
							b.Error(err)
						}
					}
					current, err := e.Sync(ctx, user, o.ID)
					if err != nil || current.Status != "completed" {
						b.Errorf("state %s: %v", current.Status, err)
					}
					durations[index] = time.Since(started)
				}
			})
			b.StopTimer()
			slices.Sort(durations)
			b.ReportMetric(float64(durations[(b.N-1)*95/100])/float64(time.Millisecond), "p95-ms")
			b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "flows/s")
			var n int
			if err := e.DB.QueryRow("SELECT COUNT(*) FROM ledger").Scan(&n); err != nil || n != 0 {
				b.Fatal(n, err)
			}
		})
	}
}
