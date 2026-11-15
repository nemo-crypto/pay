package onramp

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestLimiterCapacityAndExpiry(t *testing.T) {
	l := &requestLimiter{capacity: 2}
	now := time.Now()
	if !l.allow("alice", now) || !l.allow("bob", now) {
		t.Fatal("initial request rejected")
	}
	for i := 0; i < 100; i++ {
		if l.allow(fmt.Sprint(i), now) {
			t.Fatal("capacity exceeded")
		}
	}
	if len(l.buckets) != 2 || !l.allow("alice", now) {
		t.Fatal("new subjects evicted existing users or grew the map")
	}
	if !l.allow("carol", now.Add(time.Minute)) || len(l.buckets) != 1 {
		t.Fatal("expired capacity not released")
	}
}
func TestLimiterConcurrentQuota(t *testing.T) {
	l := &requestLimiter{capacity: 10}
	now := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); l.allow("alice", now) }()
	}
	wg.Wait()
	if l.buckets["alice"].N != 120 || l.allow("alice", now) {
		t.Fatal("quota exceeded")
	}
	if !l.allow("bob", now) || !l.allow("alice", now.Add(time.Minute)) {
		t.Fatal("isolation or reset failed")
	}
}
