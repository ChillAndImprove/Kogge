// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package httpx

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Limiter limits events per key (client address, user name) with a token bucket per key. Idle buckets are
// dropped. Limits are per process; with several nodes the effective limit multiplies, which is acceptable for
// brute-force protection in combination with account lockout.
type Limiter struct {
	mu      sync.Mutex
	every   rate.Limit
	burst   int
	buckets map[string]*bucket
	lastGC  time.Time
}

type bucket struct {
	limiter *rate.Limiter
	seen    time.Time
}

// NewLimiter allows burst events at once and then one event per interval.
func NewLimiter(interval time.Duration, burst int) *Limiter {
	return &Limiter{every: rate.Every(interval), burst: burst, buckets: map[string]*bucket{}, lastGC: time.Now()}
}

// Allow records an event for key and reports whether it is within the limit.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	return l.bucket(key, now).AllowN(now, 1)
}

// Blocked reports whether key has used up its events, without recording one. Together with Fail it limits only
// failed attempts: callers check Blocked before an attempt and call Fail when it failed. A key without events
// gets no bucket, so that probing many keys costs no memory.
func (l *Limiter) Blocked(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[key]
	if !ok {
		return false
	}
	now := time.Now()
	b.seen = now
	return b.limiter.TokensAt(now) < 1
}

// Fail records a failed attempt for key.
func (l *Limiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	l.bucket(key, now).AllowN(now, 1)
}

// bucket returns the bucket of key, dropping buckets idle for 30 minutes now and then. l.mu must be held.
func (l *Limiter) bucket(key string, now time.Time) *rate.Limiter {
	if now.Sub(l.lastGC) > 10*time.Minute {
		for k, b := range l.buckets {
			if now.Sub(b.seen) > 30*time.Minute {
				delete(l.buckets, k)
			}
		}
		l.lastGC = now
	}
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{limiter: rate.NewLimiter(l.every, l.burst)}
		l.buckets[key] = b
	}
	b.seen = now
	return b.limiter
}
