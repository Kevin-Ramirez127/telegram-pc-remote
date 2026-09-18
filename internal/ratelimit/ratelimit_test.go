package ratelimit

import (
	"testing"
	"time"
)

func TestAllowsOnePerSecond(t *testing.T) {
	l := New()
	if !l.Allow(1) {
		t.Fatal("first message should be allowed")
	}
	if l.Allow(1) {
		t.Fatal("second message within the same second must be dropped")
	}
}

func TestRefillsAfterOneSecond(t *testing.T) {
	l := New()
	l.Allow(1)
	l.Allow(1) // dropped
	time.Sleep(1100 * time.Millisecond)
	if !l.Allow(1) {
		t.Fatal("after 1.1s a new token should have refilled")
	}
}

func TestBucketsArePerKey(t *testing.T) {
	l := New()
	if !l.Allow(1) {
		t.Fatal("chat 1 first message should be allowed")
	}
	if !l.Allow(2) {
		t.Fatal("chat 2 must be independent of chat 1")
	}
	if l.Allow(1) {
		t.Fatal("chat 1 is still rate limited")
	}
	if l.Allow(2) {
		t.Fatal("chat 2 is also rate limited within the second")
	}
}

func TestNoBurstEver(t *testing.T) {
	// Capacity is 1: after a long idle period only ONE message is allowed,
	// the second one in the same second is dropped.
	l := New()
	if !l.Allow(42) {
		t.Fatal("expected allow")
	}
	if l.Allow(42) {
		t.Fatal("no burst allowance: capacity is exactly 1")
	}
}

func TestPruneKeepsActive(t *testing.T) {
	l := New()
	if !l.Allow(7) {
		t.Fatal("first message allowed")
	}
	if l.Allow(7) {
		t.Fatal("second message in same second dropped")
	}
	// A bucket that was active a moment ago must survive pruning.
	l.Prune(30 * time.Minute)
	if l.Allow(7) {
		t.Fatal("bucket survived prune and is still empty")
	}
}

func TestPruneRemovesIdle(t *testing.T) {
	l := New()
	l.mu.Lock()
	l.buckets[9] = &bucket{tokens: 0, last: time.Now().Add(-2 * time.Hour)}
	l.mu.Unlock()

	l.Prune(30 * time.Minute) // idle 2h > 30min → must be removed

	l.mu.Lock()
	_, ok := l.buckets[9]
	l.mu.Unlock()
	if ok {
		t.Fatal("idle bucket should have been pruned")
	}
}
