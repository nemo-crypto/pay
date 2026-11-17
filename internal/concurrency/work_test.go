package concurrency

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestWaiterCancellationAndNoCache(t *testing.T) {
	var g Group[int]
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		v, err := g.Do(context.Background(), "order", func() (int, error) { close(started); <-release; return 7, nil })
		if v != 7 || err != nil {
			t.Errorf("leader: %d %v", v, err)
		}
	}()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := g.Do(ctx, "order", func() (int, error) { t.Error("duplicate work"); return 0, nil })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	close(release)
	<-done
	v, err := g.Do(context.Background(), "order", func() (int, error) { return 9, nil })
	if v != 9 || err != nil {
		t.Fatal(v, err)
	}
}

func TestEachBoundedAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var active, peak, finished atomic.Int32
	started, release, done := make(chan struct{}, 8), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		Each(ctx, make([]int, 100), 8, func(int) {
			n := active.Add(1)
			for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
			}
			started <- struct{}{}
			<-release
			active.Add(-1)
			finished.Add(1)
		})
	}()
	for range 8 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("workers serialized")
		}
	}
	cancel()
	close(release)
	<-done
	if peak.Load() != 8 || finished.Load() != 8 || active.Load() != 0 {
		t.Fatal(peak.Load(), finished.Load(), active.Load())
	}
}

type waitingContext struct {
	context.Context
	entered chan struct{}
}

func (c waitingContext) Done() <-chan struct{} { close(c.entered); return c.Context.Done() }
func TestCoalescesSuccessfulWork(t *testing.T) {
	var g Group[int]
	var calls atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	results := make(chan int, 33)
	fn := func() (int, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return 42, nil
	}
	go func() {
		v, err := g.Do(context.Background(), "order", fn)
		if err != nil {
			t.Error(err)
		}
		results <- v
	}()
	<-started
	for range 32 {
		ctx := waitingContext{context.Background(), make(chan struct{})}
		go func() {
			v, err := g.Do(ctx, "order", fn)
			if err != nil {
				t.Error(err)
			}
			results <- v
		}()
		<-ctx.entered
	}
	close(release)
	for range 33 {
		if v := <-results; v != 42 {
			t.Fatal(v)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("duplicate provider calls", calls.Load())
	}
}
