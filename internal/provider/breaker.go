package provider

import (
	"errors"
	"sync"
	"time"
)

// ErrCircuitOpen is returned, without calling the provider, while the
// provider's circuit is open.
var ErrCircuitOpen = errors.New("circuit breaker is open")

// BreakerState is the state of a circuit breaker.
type BreakerState string

// The three states of a circuit breaker.
const (
	StateClosed   BreakerState = "closed"
	StateOpen     BreakerState = "open"
	StateHalfOpen BreakerState = "half-open"
)

// Breaker is a small circuit breaker.
//
// It opens after threshold consecutive failures and rejects calls until the
// cooldown has passed. The next call is a trial: success closes the circuit,
// failure opens it for another cooldown. A provider is called by one run at a
// time, so the trial needs no extra bookkeeping to stay a single call.
type Breaker struct {
	threshold int
	cooldown  time.Duration
	now       func() time.Time // replaced in tests

	mu       sync.Mutex
	state    BreakerState
	failures int
	openedAt time.Time
}

// NewBreaker returns a closed breaker.
func NewBreaker(threshold int, cooldown time.Duration) *Breaker {
	return &Breaker{
		threshold: max(threshold, 1),
		cooldown:  cooldown,
		now:       time.Now,
		state:     StateClosed,
	}
}

// Allow reports whether a call may go ahead.
func (b *Breaker) Allow() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.state == StateOpen {
		if b.now().Sub(b.openedAt) < b.cooldown {
			return ErrCircuitOpen
		}
		b.state = StateHalfOpen
	}
	return nil
}

// Success records a successful call and closes the circuit.
func (b *Breaker) Success() {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.state = StateClosed
	b.failures = 0
}

// Failure records a failed call. A failed trial reopens the circuit at once.
func (b *Breaker) Failure() {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.failures++
	if b.state == StateHalfOpen || b.failures >= b.threshold {
		b.state = StateOpen
		b.openedAt = b.now()
	}
}

// State returns the current state.
func (b *Breaker) State() BreakerState {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}
