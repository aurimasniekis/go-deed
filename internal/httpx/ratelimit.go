// Adapted from Porch internal/httpx/ratelimit.go (Apache-2.0).

package httpx

import (
	"sync"
	"time"

	"github.com/aurimasniekis/go-deed/internal/clock"
)

// Limiter is an in-memory token bucket per key, used for cheap per-source throttles in front of expensive work
// (password hashing, token endpoints). Durable limits that must survive restarts live in the database.
type Limiter struct {
	mu      sync.Mutex
	clock   clock.Clock
	rate    float64 // tokens per second
	burst   float64
	buckets map[string]*bucket
	lastGC  time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

// NewLimiter allows burst requests at once and refills at perMinute per minute.
func NewLimiter(c clock.Clock, perMinute, burst int) *Limiter {
	if c == nil {
		c = clock.System{}
	}
	return &Limiter{clock: c, rate: float64(perMinute) / 60, burst: float64(burst), buckets: map[string]*bucket{}}
}

// Allow takes one token for key and reports whether it was available.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock.Now()
	if now.Sub(l.lastGC) > time.Minute {
		l.gc(now)
	}
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	b.tokens = min(l.burst, b.tokens+now.Sub(b.last).Seconds()*l.rate)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// gc drops buckets that have refilled completely.
func (l *Limiter) gc(now time.Time) {
	l.lastGC = now
	for k, b := range l.buckets {
		if b.tokens+now.Sub(b.last).Seconds()*l.rate >= l.burst {
			delete(l.buckets, k)
		}
	}
}
