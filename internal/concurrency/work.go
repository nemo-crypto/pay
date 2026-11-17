// Package concurrency bounds background work and coalesces simultaneous reads.
package concurrency

import (
	"context"
	"errors"
	"sync"
)

type call[T any] struct {
	done  chan struct{}
	value T
	err   error
}

// Group shares only in-flight work, never caches completed payment state.
// The initiating request owns the work context; waiters can cancel independently.
type Group[T any] struct {
	mu    sync.Mutex
	calls map[string]*call[T]
}

func (g *Group[T]) Do(ctx context.Context, key string, fn func() (T, error)) (T, error) {
	if err := ctx.Err(); err != nil {
		var zero T
		return zero, err
	}
	g.mu.Lock()
	if c := g.calls[key]; c != nil {
		g.mu.Unlock()
		select {
		case <-ctx.Done():
			var zero T
			return zero, ctx.Err()
		case <-c.done:
			return c.value, c.err
		}
	}
	if g.calls == nil {
		g.calls = make(map[string]*call[T])
	}
	c := &call[T]{done: make(chan struct{}), err: errors.New("shared work aborted")}
	g.calls[key] = c
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		delete(g.calls, key)
		close(c.done)
		g.mu.Unlock()
	}()
	c.value, c.err = fn()
	return c.value, c.err
}

// Each runs at most limit jobs at once, joins all started work, and stops on cancellation.
func Each[T any](ctx context.Context, values []T, limit int, fn func(T)) {
	if limit < 1 {
		limit = 1
	}
	if limit > len(values) {
		limit = len(values)
	}
	jobs := make(chan T)
	var wg sync.WaitGroup
	for i := 0; i < limit; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for value := range jobs {
				if ctx.Err() != nil {
					return
				}
				fn(value)
			}
		}()
	}
dispatch:
	for _, value := range values {
		select {
		case <-ctx.Done():
			break dispatch
		case jobs <- value:
		}
	}
	close(jobs)
	wg.Wait()
}
