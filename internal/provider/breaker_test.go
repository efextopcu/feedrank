package provider

import (
	"errors"
	"testing"
	"time"
)

func TestBreakerLifecycle(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	b := NewBreaker(3, time.Minute)
	b.now = func() time.Time { return now }

	// Failures below the threshold keep it closed.
	b.Failure()
	b.Failure()
	if b.Allow() != nil || b.State() != StateClosed {
		t.Fatal("breaker opened before the threshold")
	}

	// The third consecutive failure opens it.
	b.Failure()
	if !errors.Is(b.Allow(), ErrCircuitOpen) {
		t.Fatal("breaker did not open at the threshold")
	}

	// Still open just before the cooldown ends.
	now = now.Add(time.Minute - time.Second)
	if !errors.Is(b.Allow(), ErrCircuitOpen) {
		t.Fatal("breaker closed before the cooldown ended")
	}

	// After the cooldown one trial is let through; failing it reopens at once.
	now = now.Add(time.Second)
	if b.Allow() != nil || b.State() != StateHalfOpen {
		t.Fatal("breaker did not let a trial through after the cooldown")
	}
	b.Failure()
	if !errors.Is(b.Allow(), ErrCircuitOpen) {
		t.Fatal("a failed trial did not reopen the breaker")
	}

	// A successful trial closes it.
	now = now.Add(time.Minute)
	if b.Allow() != nil {
		t.Fatal("no trial after the second cooldown")
	}
	b.Success()
	if b.State() != StateClosed {
		t.Fatalf("state = %s, want closed", b.State())
	}
}

func TestBreakerCountsOnlyConsecutiveFailures(t *testing.T) {
	b := NewBreaker(2, time.Minute)
	b.Failure()
	b.Success()
	b.Failure()
	if b.State() != StateClosed {
		t.Fatal("non-consecutive failures opened the breaker")
	}
}
