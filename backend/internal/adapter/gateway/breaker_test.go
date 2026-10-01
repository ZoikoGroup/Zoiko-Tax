package gateway

import (
	"testing"
	"time"
)

func TestBreakerStates(t *testing.T) {
	t0 := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	b := NewBreaker(3, 10*time.Second)

	for i := range 2 {
		if !b.Allow(t0) {
			t.Fatalf("closed breaker refused call %d", i)
		}
		b.Failure(t0)
	}
	b.Success() // a success resets the count
	for range 2 {
		b.Failure(t0)
	}
	if b.Open() {
		t.Fatal("opened before the threshold of consecutive failures")
	}
	b.Failure(t0)
	if !b.Open() || b.Allow(t0.Add(9*time.Second)) {
		t.Fatal("did not open at the threshold")
	}

	// Half-open: exactly one probe.
	probeAt := t0.Add(10 * time.Second)
	if !b.Allow(probeAt) {
		t.Fatal("no probe after the cooldown")
	}
	if b.Allow(probeAt) {
		t.Fatal("a second caller got through while the probe was in flight")
	}
	// A failed probe re-opens for a full cooldown from the probe.
	b.Failure(probeAt)
	if b.Allow(probeAt.Add(9 * time.Second)) {
		t.Fatal("re-opened breaker admitted a call inside the cooldown")
	}
	if !b.Allow(probeAt.Add(10 * time.Second)) {
		t.Fatal("no second probe")
	}
	b.Success()
	if b.Open() || !b.Allow(probeAt.Add(10*time.Second)) {
		t.Fatal("successful probe did not close the breaker")
	}
}

func TestBreakerThresholdFloor(t *testing.T) {
	b := NewBreaker(0, time.Second)
	b.Failure(time.Time{})
	if !b.Open() {
		t.Fatal("threshold below one was not treated as one")
	}
}
