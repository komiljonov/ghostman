// Package ratelimit is an in-memory token-bucket limiter keyed by an
// arbitrary string (a client IP, a webhook slug).
//
// Each key gets its own bucket of `burst` tokens that refills at `rate` tokens
// per second; a request spends one token or is refused. State lives in this
// process only, which is enough for a single server; it resets on restart.
package ratelimit

import (
	"math"
	"sync"
	"time"
)

// sweepEvery is how often idle buckets are dropped, so a flood of distinct
// keys cannot grow memory without bound.
const sweepEvery = time.Minute

// Limiter is safe for concurrent use.
type Limiter struct {
	rate  float64 // tokens per second
	burst float64

	// now is the clock; replaced in tests.
	now func() time.Time

	mu        sync.Mutex
	buckets   map[string]*bucket
	lastSweep time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

// New returns a limiter allowing `burst` requests at once per key, refilling
// at `perSecond` requests per second.
func New(perSecond float64, burst int) *Limiter {
	return &Limiter{
		rate:    perSecond,
		burst:   float64(burst),
		now:     time.Now,
		buckets: make(map[string]*bucket),
	}
}

// PerMinute is New for limits stated per minute, e.g. PerMinute(5, 10).
func PerMinute(perMinute float64, burst int) *Limiter {
	return New(perMinute/60, burst)
}

// Allow spends one token from key's bucket. When the bucket is empty it
// reports false and how long until a token is available, for Retry-After.
func (l *Limiter) Allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	l.sweep(now)

	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}

	b.tokens = math.Min(l.burst, b.tokens+now.Sub(b.last).Seconds()*l.rate)
	b.last = now

	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}

	wait := time.Duration((1 - b.tokens) / l.rate * float64(time.Second))
	return false, wait
}

// sweep drops buckets that have refilled completely: forgetting them changes
// nothing, since a new bucket starts full. Called with l.mu held.
func (l *Limiter) sweep(now time.Time) {
	if now.Sub(l.lastSweep) < sweepEvery {
		return
	}
	l.lastSweep = now

	for key, b := range l.buckets {
		if b.tokens+now.Sub(b.last).Seconds()*l.rate >= l.burst {
			delete(l.buckets, key)
		}
	}
}

// size reports how many buckets are held; for tests.
func (l *Limiter) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}
