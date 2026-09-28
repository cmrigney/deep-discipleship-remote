// Package auth verifies the shared relay password and locks out clients that
// guess wrong too often.
package auth

import (
	"context"
	"errors"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

var (
	ErrBadPassword = errors.New("bad password")
	ErrLockedOut   = errors.New("locked out")
)

// Config controls lockout behavior.
type Config struct {
	// PerIPFailures failed attempts within Window lock that IP out for Lockout.
	PerIPFailures int
	// GlobalFailures failed attempts across all IPs within Window lock out every
	// IP that hasn't authenticated successfully within TrustedFor.
	GlobalFailures int
	Window         time.Duration
	Lockout        time.Duration
	// TrustedFor is how long an IP that authenticated successfully may bypass
	// the global lockout, so an attack can't lock the church out.
	TrustedFor time.Duration
	// MaxConcurrent bounds simultaneous bcrypt comparisons (they are CPU heavy on a Pi).
	MaxConcurrent int
}

// DefaultConfig matches PLAN.md §5.4.
func DefaultConfig() Config {
	return Config{
		PerIPFailures:  5,
		GlobalFailures: 30,
		Window:         15 * time.Minute,
		Lockout:        15 * time.Minute,
		TrustedFor:     24 * time.Hour,
		MaxConcurrent:  2,
	}
}

type ipRecord struct {
	failures    []time.Time
	lockedUntil time.Time
	lastSuccess time.Time
}

// Guard checks passwords and tracks failures.
type Guard struct {
	hash []byte
	cfg  Config
	now  func() time.Time
	sem  chan struct{}

	mu             sync.Mutex
	ips            map[string]*ipRecord
	globalFailures []time.Time
	globalLocked   time.Time
}

// NewGuard creates a Guard for the given bcrypt hash.
func NewGuard(hash []byte, cfg Config) (*Guard, error) {
	if _, err := bcrypt.Cost(hash); err != nil {
		return nil, err
	}
	if cfg.MaxConcurrent < 1 {
		cfg.MaxConcurrent = 1
	}
	return &Guard{
		hash: hash,
		cfg:  cfg,
		now:  time.Now,
		sem:  make(chan struct{}, cfg.MaxConcurrent),
		ips:  make(map[string]*ipRecord),
	}, nil
}

// SetClock replaces the time source (tests only).
func (g *Guard) SetClock(now func() time.Time) { g.now = now }

// Check verifies password for a client at ip. It returns nil, ErrBadPassword,
// ErrLockedOut, or ctx.Err() if the context ends while waiting for a bcrypt slot.
func (g *Guard) Check(ctx context.Context, ip, password string) error {
	if err := g.checkLocked(ip); err != nil {
		return err
	}

	select {
	case g.sem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	err := bcrypt.CompareHashAndPassword(g.hash, []byte(password))
	<-g.sem

	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	rec := g.record(ip)
	if err == nil {
		rec.failures = nil
		rec.lastSuccess = now
		return nil
	}

	rec.failures = append(prune(rec.failures, now.Add(-g.cfg.Window)), now)
	if len(rec.failures) >= g.cfg.PerIPFailures {
		rec.lockedUntil = now.Add(g.cfg.Lockout)
		rec.failures = nil
	}
	g.globalFailures = append(prune(g.globalFailures, now.Add(-g.cfg.Window)), now)
	if len(g.globalFailures) >= g.cfg.GlobalFailures {
		g.globalLocked = now.Add(g.cfg.Lockout)
		g.globalFailures = nil
	}
	return ErrBadPassword
}

// LockedFor reports how much longer ip is locked out (0 if not locked).
func (g *Guard) LockedFor(ip string) time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.lockedForLocked(ip)
}

func (g *Guard) checkLocked(ip string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.lockedForLocked(ip) > 0 {
		return ErrLockedOut
	}
	return nil
}

func (g *Guard) lockedForLocked(ip string) time.Duration {
	now := g.now()
	rec := g.ips[ip]
	if rec != nil && now.Before(rec.lockedUntil) {
		return rec.lockedUntil.Sub(now)
	}
	trusted := rec != nil && !rec.lastSuccess.IsZero() && now.Sub(rec.lastSuccess) < g.cfg.TrustedFor
	if !trusted && now.Before(g.globalLocked) {
		return g.globalLocked.Sub(now)
	}
	return 0
}

func (g *Guard) record(ip string) *ipRecord {
	rec := g.ips[ip]
	if rec == nil {
		rec = &ipRecord{}
		g.ips[ip] = rec
	}
	return rec
}

// Cleanup forgets IPs with no recent activity. Call it periodically.
func (g *Guard) Cleanup() {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	for ip, rec := range g.ips {
		rec.failures = prune(rec.failures, now.Add(-g.cfg.Window))
		if len(rec.failures) == 0 && now.After(rec.lockedUntil) &&
			(rec.lastSuccess.IsZero() || now.Sub(rec.lastSuccess) >= g.cfg.TrustedFor) {
			delete(g.ips, ip)
		}
	}
}

// prune drops timestamps before cutoff. ts is sorted ascending.
func prune(ts []time.Time, cutoff time.Time) []time.Time {
	i := 0
	for i < len(ts) && ts[i].Before(cutoff) {
		i++
	}
	return ts[i:]
}

// HashPassword returns a bcrypt hash suitable for RELAY_PASSWORD_HASH.
func HashPassword(password string) ([]byte, error) {
	return bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
}
