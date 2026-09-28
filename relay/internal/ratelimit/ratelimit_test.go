package ratelimit

import (
	"errors"
	"testing"
	"time"
)

func TestKeyedLimiter(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	k := NewKeyed(1, 3)
	k.SetClock(func() time.Time { return now })

	for i := 0; i < 3; i++ {
		if !k.Allow("a") {
			t.Fatalf("burst event %d denied", i)
		}
	}
	if k.Allow("a") {
		t.Fatal("4th event should be denied")
	}
	if !k.Allow("b") {
		t.Fatal("other key should have its own bucket")
	}
	now = now.Add(time.Second)
	if !k.Allow("a") {
		t.Fatal("token should refill after 1s")
	}

	now = now.Add(time.Hour)
	k.Cleanup(10 * time.Minute)
	if k.Len() != 0 {
		t.Fatalf("cleanup left %d keys", k.Len())
	}
}

func TestConnCounter(t *testing.T) {
	c := NewConnCounter(2, 3)
	r1, err := c.Acquire("a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Acquire("a"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Acquire("a"); !errors.Is(err, ErrTooManyForIP) {
		t.Fatalf("per-ip cap: %v", err)
	}
	if _, err := c.Acquire("b"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Acquire("c"); !errors.Is(err, ErrTooManyTotal) {
		t.Fatalf("total cap: %v", err)
	}
	r1()
	r1() // double release must be harmless
	if _, err := c.Acquire("c"); err != nil {
		t.Fatalf("after release: %v", err)
	}
	if _, err := c.Acquire("d"); !errors.Is(err, ErrTooManyTotal) {
		t.Fatalf("double release freed two slots: %v", err)
	}
}
