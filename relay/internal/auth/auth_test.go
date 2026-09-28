package auth

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) Advance(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

func newTestGuard(t *testing.T, cfg Config) (*Guard, *fakeClock) {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("correct horse"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	g, err := NewGuard(hash, cfg)
	if err != nil {
		t.Fatal(err)
	}
	clk := &fakeClock{t: time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC)}
	g.SetClock(clk.Now)
	return g, clk
}

func TestCheckPassword(t *testing.T) {
	g, _ := newTestGuard(t, DefaultConfig())
	ctx := context.Background()
	if err := g.Check(ctx, "1.1.1.1", "correct horse"); err != nil {
		t.Fatalf("correct password: %v", err)
	}
	if err := g.Check(ctx, "1.1.1.1", "wrong"); !errors.Is(err, ErrBadPassword) {
		t.Fatalf("wrong password: %v", err)
	}
}

func TestPerIPLockout(t *testing.T) {
	g, clk := newTestGuard(t, DefaultConfig())
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if err := g.Check(ctx, "1.1.1.1", "wrong"); !errors.Is(err, ErrBadPassword) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	// Locked now, even with the right password.
	if err := g.Check(ctx, "1.1.1.1", "correct horse"); !errors.Is(err, ErrLockedOut) {
		t.Fatalf("expected lockout, got %v", err)
	}
	// Other IPs are unaffected.
	if err := g.Check(ctx, "2.2.2.2", "correct horse"); err != nil {
		t.Fatalf("other ip: %v", err)
	}
	clk.Advance(15*time.Minute + time.Second)
	if err := g.Check(ctx, "1.1.1.1", "correct horse"); err != nil {
		t.Fatalf("after lockout expiry: %v", err)
	}
}

func TestFailuresOutsideWindowDoNotCount(t *testing.T) {
	g, clk := newTestGuard(t, DefaultConfig())
	ctx := context.Background()
	for i := 0; i < 4; i++ {
		_ = g.Check(ctx, "1.1.1.1", "wrong")
	}
	clk.Advance(16 * time.Minute)
	for i := 0; i < 4; i++ {
		_ = g.Check(ctx, "1.1.1.1", "wrong")
	}
	if err := g.Check(ctx, "1.1.1.1", "correct horse"); err != nil {
		t.Fatalf("should not be locked: %v", err)
	}
}

func TestGlobalLockoutSparesTrustedIPs(t *testing.T) {
	cfg := DefaultConfig()
	cfg.GlobalFailures = 6
	g, _ := newTestGuard(t, cfg)
	ctx := context.Background()

	// The church computer authenticated earlier.
	if err := g.Check(ctx, "10.0.0.1", "correct horse"); err != nil {
		t.Fatal(err)
	}
	// A distributed attacker: 3 IPs, 2 failures each.
	for _, ip := range []string{"6.6.6.1", "6.6.6.2", "6.6.6.3"} {
		for i := 0; i < 2; i++ {
			_ = g.Check(ctx, ip, "guess")
		}
	}
	if err := g.Check(ctx, "7.7.7.7", "correct horse"); !errors.Is(err, ErrLockedOut) {
		t.Fatalf("new ip during global lockout: %v", err)
	}
	if err := g.Check(ctx, "10.0.0.1", "correct horse"); err != nil {
		t.Fatalf("trusted ip should bypass global lockout: %v", err)
	}
}

func TestCleanup(t *testing.T) {
	g, clk := newTestGuard(t, DefaultConfig())
	_ = g.Check(context.Background(), "1.1.1.1", "wrong")
	clk.Advance(time.Hour)
	g.Cleanup()
	if n := len(g.ips); n != 0 {
		t.Fatalf("expected 0 tracked ips, got %d", n)
	}
}
