// Package ratelimit implements a minimal in-memory fixed-window rate limiter. It exists to
// put a ceiling on abuse of the webhook trigger route (pkg/service.HooksHandler), which by
// design bypasses this backend's normal Validator.Middleware bearer-token auth — the
// per-workflow token in the URL is the only gate, so nothing else stops a caller from
// flooding a known (or guessed) token. A single-process, in-memory counter is adequate for
// this sidecar's current single-instance deployment; a multi-instance deployment would need
// a shared store (e.g. Redis) instead, out of scope here.
package ratelimit

import (
	"sync"
	"time"
)

// evictionThreshold is how many distinct keys accumulate before Allow bothers sweeping
// expired entries — keeps the common case (few distinct webhook tokens) allocation-free.
const evictionThreshold = 1000

// Limiter enforces "at most max requests per key every window" using a fixed window
// counter per key.
type Limiter struct {
	max    int
	window time.Duration

	mu       sync.Mutex
	counters map[string]*bucket
}

type bucket struct {
	count   int
	resetAt time.Time
}

// New builds a Limiter allowing at most max requests per key within window. max and window
// must both be positive; the zero value is not usable.
func New(max int, window time.Duration) *Limiter {
	return &Limiter{max: max, window: window, counters: map[string]*bucket{}}
}

// Allow reports whether a request for key is within the rate limit, and — if so — counts
// it against the limit. Safe for concurrent use.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	l.evictStaleLocked(now)

	b, ok := l.counters[key]
	if !ok || now.After(b.resetAt) {
		l.counters[key] = &bucket{count: 1, resetAt: now.Add(l.window)}
		return true
	}
	if b.count >= l.max {
		return false
	}
	b.count++
	return true
}

func (l *Limiter) evictStaleLocked(now time.Time) {
	if len(l.counters) < evictionThreshold {
		return
	}
	for k, b := range l.counters {
		if now.After(b.resetAt) {
			delete(l.counters, k)
		}
	}
}
