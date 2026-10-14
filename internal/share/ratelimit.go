package share

import (
	"sync"
	"time"
)

// Limiter is a set of token buckets by key (a remote IP, a device id): a
// bucket holds up to burst tokens and refills at rate per second; each
// request takes one. State is in memory only.
type Limiter struct {
	rate, burst float64
	now         func() time.Time

	mu      sync.Mutex
	buckets map[string]*bucket
	calls   int
}

type bucket struct {
	tokens float64
	at     time.Time
}

// pruneEvery is how many calls pass between sweeps of idle buckets.
const pruneEvery = 1024

// NewLimiter returns a limiter of burst tokens refilled at rate per
// second, with the clock now (nil: time.Now).
func NewLimiter(rate, burst float64, now func() time.Time) *Limiter {
	if now == nil {
		now = time.Now
	}
	return &Limiter{rate: rate, burst: burst, now: now, buckets: map[string]*bucket{}}
}

// PerMinute is a limiter of n requests per minute (burst n).
func PerMinute(n int, now func() time.Time) *Limiter {
	return NewLimiter(float64(n)/60, float64(n), now)
}

// Allow takes a token from key's bucket; false when it is empty.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.calls++
	if l.calls%pruneEvery == 0 {
		l.pruneLocked(now)
	}
	b := l.buckets[key]
	if b == nil {
		b = &bucket{tokens: l.burst, at: now}
		l.buckets[key] = b
	}
	b.tokens = min(l.burst, b.tokens+now.Sub(b.at).Seconds()*l.rate)
	b.at = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// pruneLocked drops buckets that are full again: they behave like new.
func (l *Limiter) pruneLocked(now time.Time) {
	for k, b := range l.buckets {
		if b.tokens+now.Sub(b.at).Seconds()*l.rate >= l.burst {
			delete(l.buckets, k)
		}
	}
}

// Lockout counts failures per key (authentication failures by remote IP):
// more than the limiter allows locks the key out for a while, during
// which every request of it is refused.
type Lockout struct {
	fails *Limiter
	dur   time.Duration
	now   func() time.Time

	mu    sync.Mutex
	until map[string]time.Time
}

// NewLockout locks a key out for dur once it fails more than perMinute
// times a minute.
func NewLockout(perMinute int, dur time.Duration, now func() time.Time) *Lockout {
	if now == nil {
		now = time.Now
	}
	return &Lockout{fails: PerMinute(perMinute, now), dur: dur, now: now, until: map[string]time.Time{}}
}

// Locked reports whether key is locked out now.
func (l *Lockout) Locked(key string) bool { return l.LockedFor(key) > 0 }

// LockedFor is how long key stays locked out (0: it is not).
func (l *Lockout) LockedFor(key string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	t, ok := l.until[key]
	if !ok {
		return 0
	}
	if d := t.Sub(l.now()); d > 0 {
		return d
	}
	delete(l.until, key)
	return 0
}

// Fail records a failure of key, locking it out when over the limit.
func (l *Lockout) Fail(key string) {
	if l.fails.Allow(key) {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.until[key] = l.now().Add(l.dur)
}
