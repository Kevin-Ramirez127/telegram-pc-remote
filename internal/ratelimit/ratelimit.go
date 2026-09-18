// Package ratelimit implements a per-chat token bucket that allows at most
// one event per second per key.
//
// The bucket capacity is exactly one token, so bursts are never permitted:
// when a chat fires more than one message within the same second, the extra
// messages are denied (and the bot ignores them).
package ratelimit

import (
	"sync"
	"time"
)

const (
	capacity  = 1.0
	refillPer = time.Second
)

type bucket struct {
	tokens float64
	last   time.Time
}

type Limiter struct {
	mu      sync.Mutex
	buckets map[int64]*bucket
}

func New() *Limiter {
	return &Limiter{buckets: make(map[int64]*bucket)}
}

// Allow reports whether an event for key may proceed. It never blocks.
func (l *Limiter) Allow(key int64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	b, ok := l.buckets[key]
	if !ok {
		// Brand new key: the first message is allowed and consumes the token,
		// so a second message in the same second is still dropped.
		l.buckets[key] = &bucket{tokens: capacity - 1, last: now}
		return true
	}

	// Refill based on elapsed time: +1 token per second, capped at capacity.
	elapsed := now.Sub(b.last).Seconds()
	b.last = now
	b.tokens += elapsed
	if b.tokens > capacity {
		b.tokens = capacity
	}

	if b.tokens >= 1.0 {
		b.tokens--
		return true
	}
	return false
}

// Prune removes buckets idle for longer than idleFor, avoiding unbounded
// memory growth on long-running bots.
func (l *Limiter) Prune(idleFor time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := time.Now().Add(-idleFor)
	for k, b := range l.buckets {
		if b.last.Before(cutoff) {
			delete(l.buckets, k)
		}
	}
}
