// Package ratelimit provides per-key token buckets and connection counters.
package ratelimit

import (
	"errors"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// KeyedLimiter keeps one token bucket per key (usually a client IP).
type KeyedLimiter struct {
	limit rate.Limit
	burst int
	now   func() time.Time

	mu      sync.Mutex
	entries map[string]*keyedEntry
}

type keyedEntry struct {
	lim  *rate.Limiter
	seen time.Time
}

// NewKeyed returns a limiter allowing r events per second with the given burst, per key.
func NewKeyed(r rate.Limit, burst int) *KeyedLimiter {
	return &KeyedLimiter{limit: r, burst: burst, now: time.Now, entries: make(map[string]*keyedEntry)}
}

// SetClock replaces the time source (tests only).
func (k *KeyedLimiter) SetClock(now func() time.Time) { k.now = now }

// Allow reports whether an event for key may happen now.
func (k *KeyedLimiter) Allow(key string) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	now := k.now()
	e := k.entries[key]
	if e == nil {
		e = &keyedEntry{lim: rate.NewLimiter(k.limit, k.burst)}
		k.entries[key] = e
	}
	e.seen = now
	return e.lim.AllowN(now, 1)
}

// Cleanup drops keys not seen within idle. Call it periodically to bound memory.
func (k *KeyedLimiter) Cleanup(idle time.Duration) {
	k.mu.Lock()
	defer k.mu.Unlock()
	cutoff := k.now().Add(-idle)
	for key, e := range k.entries {
		if e.seen.Before(cutoff) {
			delete(k.entries, key)
		}
	}
}

// Len returns the number of tracked keys.
func (k *KeyedLimiter) Len() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.entries)
}

var (
	ErrTooManyForIP = errors.New("too many connections from this address")
	ErrTooManyTotal = errors.New("too many connections")
)

// ConnCounter caps concurrent connections per key and in total.
type ConnCounter struct {
	maxPerKey int
	maxTotal  int

	mu     sync.Mutex
	perKey map[string]int
	total  int
}

// NewConnCounter creates a counter with the given caps.
func NewConnCounter(maxPerKey, maxTotal int) *ConnCounter {
	return &ConnCounter{maxPerKey: maxPerKey, maxTotal: maxTotal, perKey: make(map[string]int)}
}

// Acquire reserves a slot for key. Call the returned release func exactly once
// when the connection ends.
func (c *ConnCounter) Acquire(key string) (release func(), err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.total >= c.maxTotal {
		return nil, ErrTooManyTotal
	}
	if c.perKey[key] >= c.maxPerKey {
		return nil, ErrTooManyForIP
	}
	c.perKey[key]++
	c.total++
	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			c.total--
			if c.perKey[key]--; c.perKey[key] <= 0 {
				delete(c.perKey, key)
			}
		})
	}, nil
}
