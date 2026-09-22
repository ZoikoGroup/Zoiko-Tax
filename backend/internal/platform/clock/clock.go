// Package clock is the only source of wall time in the estate.
//
// ADR-0003 §2.5: now() does not appear in application SQL or in domain Go.
// Decision time enters as an explicit parameter from the request or replay
// envelope, and the determination path receives an instant, never this
// interface. Replay substitutes the historical instant and gets the historical
// answer, and there is no code path where it could do otherwise — because the
// evaluator's frame has nowhere to put a Clock (ADR-0005 §2.4).
//
// The forbidigo rule in .golangci.yml rejects time.Now outside this package, so
// this is a gate rather than a convention.
package clock

import "time"

// Clock reports wall time. It is injected, so a test supplies a fixed instant
// and gets a determinate result without sleeping.
type Clock interface {
	// Now returns the current instant in UTC.
	Now() time.Time
}

// System is the production clock.
type System struct{}

// Now returns the current instant in UTC. UTC rather than local because every
// timestamp that leaves this system is RFC 3339 UTC with six fractional digits
// (ADR-0011 §2.1 P2), and converting at the edge invites one path that forgets.
func (System) Now() time.Time { return time.Now().UTC() }

// Fixed is a clock stopped at an instant, for tests and for replay.
type Fixed struct{ Instant time.Time }

// Now returns the fixed instant.
func (f Fixed) Now() time.Time { return f.Instant.UTC() }

// Stepping is a clock that advances by a fixed amount on each read. It exists
// for tests that need two distinct instants without needing to care what they
// are — a session created before it is revoked, for instance.
type Stepping struct {
	Instant time.Time
	Step    time.Duration
}

// Now returns the current instant and advances the clock.
func (s *Stepping) Now() time.Time {
	t := s.Instant.UTC()
	s.Instant = s.Instant.Add(s.Step)
	return t
}
