package gateway

import (
	"sync"
	"time"
)

// The default breaker settings. Five consecutive failures is enough to tell a
// sick Gateway from one slow call, and thirty seconds is long enough to stop
// an operator's screen from hammering it while short enough that recovery is
// noticed without anybody intervening.
const (
	DefaultBreakerThreshold = 5
	DefaultBreakerCooldown  = 30 * time.Second
)

// Breaker is the circuit breaker ADR-0006 §2.4 requires on every synchronous
// AI call.
//
// Closed, it counts consecutive failures; at the threshold it opens. Open, it
// refuses every call until the cooldown has passed, then lets exactly one
// probe through (half-open). The probe's result closes it or re-opens it for
// another cooldown. Only one probe, because the point of opening was to stop
// sending load to something that is failing, and a half-open state that
// admits everyone is not that.
//
// Time comes from the caller, so the breaker reads no clock of its own and a
// test drives it with fixed instants.
type Breaker struct {
	mu        sync.Mutex
	threshold int
	cooldown  time.Duration

	failures int
	open     bool
	openedAt time.Time
	probing  bool
}

// NewBreaker builds a breaker. A threshold below one is treated as one.
func NewBreaker(threshold int, cooldown time.Duration) *Breaker {
	if threshold < 1 {
		threshold = 1
	}
	return &Breaker{threshold: threshold, cooldown: cooldown}
}

// Allow reports whether a call may be attempted at now. When it returns true
// the caller must report the result with Success or Failure.
func (b *Breaker) Allow(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.open {
		return true
	}
	if b.probing || now.Before(b.openedAt.Add(b.cooldown)) {
		return false
	}
	b.probing = true
	return true
}

// Success records a call that reached a working Gateway.
func (b *Breaker) Success() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures, b.open, b.probing = 0, false, false
}

// Failure records a call that did not.
func (b *Breaker) Failure(now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures++
	if b.probing || b.failures >= b.threshold {
		b.open, b.openedAt, b.probing = true, now, false
	}
}

// Open reports whether the breaker is refusing calls (open, or half-open with
// a probe in flight). For readiness reporting and tests.
func (b *Breaker) Open() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.open
}
