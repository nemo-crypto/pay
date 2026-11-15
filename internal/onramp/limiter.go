package onramp

import (
	"sync"
	"time"
)

type requestBucket struct {
	At time.Time
	N  int
}
type requestLimiter struct {
	mu       sync.Mutex
	buckets  map[string]requestBucket
	capacity int
}

// 达到容量时拒绝新主体，不能继续插入；已有主体不受其他账号挤占影响。
func (l *requestLimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.buckets == nil {
		l.buckets = make(map[string]requestBucket)
	}
	if len(l.buckets) >= l.capacity {
		for k, b := range l.buckets {
			if now.Sub(b.At) >= time.Minute {
				delete(l.buckets, k)
			}
		}
	}
	b, exists := l.buckets[key]
	if !exists && len(l.buckets) >= l.capacity {
		return false
	}
	if !exists || now.Sub(b.At) >= time.Minute {
		b = requestBucket{At: now}
	}
	if b.N >= 120 {
		return false
	}
	b.N++
	l.buckets[key] = b
	return true
}
