package ratelimit

import (
	"testing"
	"time"
)

// fakeClock is a manually advanced clock.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestLimiter(perSecond float64, burst int) (*Limiter, *fakeClock) {
	clock := &fakeClock{t: time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)}
	l := New(perSecond, burst)
	l.now = clock.now
	l.lastSweep = clock.t
	return l, clock
}

func TestBurstThenRefuse(t *testing.T) {
	l, _ := newTestLimiter(10, 30)

	for i := range 30 {
		if ok, _ := l.Allow("slug"); !ok {
			t.Fatalf("request %d refused inside the burst", i+1)
		}
	}

	ok, retry := l.Allow("slug")
	if ok {
		t.Fatal("request 31 allowed past the burst")
	}
	if retry <= 0 || retry > 100*time.Millisecond {
		t.Errorf("retry after = %v, want (0, 100ms] at 10/s", retry)
	}
}

func TestRefillAtRate(t *testing.T) {
	l, clock := newTestLimiter(10, 30)
	for range 30 {
		l.Allow("slug")
	}

	clock.advance(250 * time.Millisecond) // 2.5 tokens back
	for i := range 2 {
		if ok, _ := l.Allow("slug"); !ok {
			t.Fatalf("refilled request %d refused", i+1)
		}
	}
	if ok, _ := l.Allow("slug"); ok {
		t.Fatal("third request allowed with only half a token")
	}

	// Never refills past the burst.
	clock.advance(time.Hour)
	for i := range 30 {
		if ok, _ := l.Allow("slug"); !ok {
			t.Fatalf("request %d refused after a long idle", i+1)
		}
	}
	if ok, _ := l.Allow("slug"); ok {
		t.Fatal("bucket refilled beyond its burst")
	}
}

func TestPerMinute(t *testing.T) {
	l, clock := newTestLimiter(0, 0)
	*l = *PerMinute(5, 10)
	l.now = clock.now
	l.lastSweep = clock.t

	for range 10 {
		l.Allow("ip")
	}
	ok, retry := l.Allow("ip")
	if ok {
		t.Fatal("11th login allowed at burst 10")
	}
	// 5 per minute: one token every 12 seconds.
	if retry < 11*time.Second || retry > 12*time.Second {
		t.Errorf("retry after = %v, want about 12s", retry)
	}

	clock.advance(12 * time.Second)
	if ok, _ := l.Allow("ip"); !ok {
		t.Fatal("refused after the 12s refill")
	}
}

func TestKeysDoNotShareBuckets(t *testing.T) {
	l, _ := newTestLimiter(1, 2)

	l.Allow("a")
	l.Allow("a")
	if ok, _ := l.Allow("a"); ok {
		t.Fatal("key a allowed past its burst")
	}
	for i := range 2 {
		if ok, _ := l.Allow("b"); !ok {
			t.Fatalf("key b request %d refused because of key a", i+1)
		}
	}
}

func TestSweepDropsIdleBuckets(t *testing.T) {
	l, clock := newTestLimiter(1, 2)

	for _, key := range []string{"a", "b", "c"} {
		l.Allow(key)
	}
	if got := l.size(); got != 3 {
		t.Fatalf("buckets = %d, want 3", got)
	}

	clock.advance(2 * sweepEvery)
	l.Allow("d") // triggers the sweep: a, b, c have refilled completely

	if got := l.size(); got != 1 {
		t.Errorf("buckets after sweep = %d, want 1 (only d)", got)
	}
}
