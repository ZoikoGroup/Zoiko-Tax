package http

import (
	"testing"
	"time"
)

func TestLimiterAllowsAWindowFullThenWaits(t *testing.T) {
	now := time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)
	l := newLimiter(2, 10*time.Minute)
	l.now = func() time.Time { return now }

	for i := range 2 {
		if ok, _ := l.allow("203.0.113.7"); !ok {
			t.Fatalf("request %d refused inside the limit", i+1)
		}
	}
	ok, wait := l.allow("203.0.113.7")
	if ok || wait != 10*time.Minute {
		t.Fatalf("third request: %v, wait %s; want refused for the whole window", ok, wait)
	}
	if ok, _ := l.allow("198.51.100.1"); !ok {
		t.Fatal("another client was refused for the first one's requests")
	}

	now = now.Add(10 * time.Minute)
	if ok, _ := l.allow("203.0.113.7"); !ok {
		t.Fatal("refused after the window reset")
	}
}

func TestLimiterSweepsExpiredWindows(t *testing.T) {
	now := time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)
	l := newLimiter(1, time.Minute)
	l.now = func() time.Time { return now }
	for i := range pruneAt {
		l.allow(string(rune(i)))
	}
	now = now.Add(time.Minute)
	l.allow("fresh")
	if len(l.hits) != 1 {
		t.Fatalf("%d windows kept; want only the fresh one", len(l.hits))
	}
}
