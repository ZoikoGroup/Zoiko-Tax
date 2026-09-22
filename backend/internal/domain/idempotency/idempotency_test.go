package idempotency_test

import (
	"errors"
	"testing"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/idempotency"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
)

func digest(s string) canonical.Digest { return canonical.SumBytes([]byte(s)) }

// ADR-0013 §2.4 defines four situations and calls them exhaustive. This asserts
// all four, because "exhaustive" is the property that makes the table in the
// ADR a specification rather than a summary.
func TestDecideCoversTheFourSituations(t *testing.T) {
	body := digest(`{"amount":"10.00"}`)
	other := digest(`{"amount":"20.00"}`)

	t.Run("no record proceeds", func(t *testing.T) {
		d, err := idempotency.Decide(nil, body)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if d != idempotency.Proceed {
			t.Errorf("disposition is %v, want Proceed", d)
		}
	})

	t.Run("settled record with a matching digest replays", func(t *testing.T) {
		rec := &idempotency.Record{RequestDigest: body, State: idempotency.StateSucceeded}
		d, err := idempotency.Decide(rec, body)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if d != idempotency.Replay {
			t.Errorf("disposition is %v, want Replay", d)
		}
	})

	t.Run("differing digest conflicts", func(t *testing.T) {
		rec := &idempotency.Record{RequestDigest: body, State: idempotency.StateSucceeded}
		d, err := idempotency.Decide(rec, other)
		if d != idempotency.Conflict {
			t.Errorf("disposition is %v, want Conflict", d)
		}
		if errs.ReasonOf(err) != errs.ReasonIdempotencyKeyReuse {
			t.Errorf("reason is %q, want %q", errs.ReasonOf(err), errs.ReasonIdempotencyKeyReuse)
		}
	})

	t.Run("pending record is in progress", func(t *testing.T) {
		rec := &idempotency.Record{RequestDigest: body, State: idempotency.StatePending}
		d, err := idempotency.Decide(rec, body)
		if d != idempotency.InProgress {
			t.Errorf("disposition is %v, want InProgress", d)
		}
		if errs.ReasonOf(err) != errs.ReasonRequestInProgress {
			t.Errorf("reason is %q, want %q", errs.ReasonOf(err), errs.ReasonRequestInProgress)
		}
	})
}

// A key reused with a different body is a client defect whichever state the
// original is in. Reporting it as "in progress" would invite the client to
// retry the wrong request until the original settled.
func TestReuseWithADifferentBodyBeatsInProgress(t *testing.T) {
	rec := &idempotency.Record{RequestDigest: digest("a"), State: idempotency.StatePending}
	d, err := idempotency.Decide(rec, digest("b"))
	if d != idempotency.Conflict {
		t.Errorf("disposition is %v, want Conflict", d)
	}
	if errs.ReasonOf(err) != errs.ReasonIdempotencyKeyReuse {
		t.Errorf("reason is %q, want %q", errs.ReasonOf(err), errs.ReasonIdempotencyKeyReuse)
	}
}

// ADR-0013 §2.3: the digest is over the canonical form, so a reordered but
// semantically identical retry matches. This is the property that makes a
// correctly-behaving client's retry succeed rather than conflict.
func TestSemanticallyIdenticalRetriesMatch(t *testing.T) {
	build := func(reversed bool) canonical.Value {
		a := canonical.F("amount", canonical.String("10.00"))
		b := canonical.F("currency", canonical.String("EUR"))
		if reversed {
			return canonical.Object(b, a)
		}
		return canonical.Object(a, b)
	}
	first, err := canonical.Sum(build(false))
	if err != nil {
		t.Fatalf("Sum: %v", err)
	}
	second, err := canonical.Sum(build(true))
	if err != nil {
		t.Fatalf("Sum: %v", err)
	}

	rec := &idempotency.Record{RequestDigest: first, State: idempotency.StateSucceeded}
	d, err := idempotency.Decide(rec, second)
	if err != nil {
		t.Fatalf("a reordered retry conflicted: %v", err)
	}
	if d != idempotency.Replay {
		t.Errorf("disposition is %v, want Replay", d)
	}
}

// ADR-0013 §2.7: only a transient failure deletes its PENDING record. Getting
// this wrong in the other direction would leave a client permanently unable to
// complete a legitimate request with that key.
func TestOnlyUnavailableIsTransient(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		transient bool
	}{
		{"unavailable", errs.New(errs.CategoryUnavailable, errs.ReasonUnavailable, "x"), true},
		{"database unavailable", errs.New(errs.CategoryUnavailable, errs.ReasonDatabaseUnavailable, "x"), true},
		{"validation", errs.New(errs.CategoryValidation, errs.ReasonInvalidValue, "x"), false},
		{"conflict", errs.New(errs.CategoryConflict, errs.ReasonAlreadyExists, "x"), false},
		{"policy", errs.New(errs.CategoryPolicy, errs.ReasonForbidden, "x"), false},
		{"internal", errs.New(errs.CategoryInternal, errs.ReasonInternal, "x"), false},
		{"unclassified", errors.New("something"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := idempotency.TransientFailureIsNotTerminal(tc.err); got != tc.transient {
				t.Errorf("transient is %v, want %v", got, tc.transient)
			}
		})
	}
}

// A 4xx is recorded FAILED and replayed: the same request will fail identically
// and re-executing wastes work (ADR-0013 §2.7).
func TestCompleteRecordsClientFailuresAsTerminal(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	rec, err := idempotency.NewPending(validKey(t), digest("body"), now, 0)
	if err != nil {
		t.Fatalf("NewPending: %v", err)
	}

	ok := rec.Complete(200, []byte(`{}`), nil, now)
	if ok.State != idempotency.StateSucceeded {
		t.Errorf("200 recorded as %s, want SUCCEEDED", ok.State)
	}
	bad := rec.Complete(422, []byte(`{}`), nil, now)
	if bad.State != idempotency.StateFailed {
		t.Errorf("422 recorded as %s, want FAILED", bad.State)
	}
	if bad.CompletedAt == nil {
		t.Error("a settled record has no completion time")
	}
}

func TestMissingKeyIsRefusedBeforeAnyWork(t *testing.T) {
	k := idempotency.Key{TenantID: validKey(t).TenantID, Endpoint: "/v1/transactions:commit"}
	err := k.Validate()
	if err == nil {
		t.Fatal("an empty idempotency key was accepted")
	}
	if errs.ReasonOf(err) != errs.ReasonIdempotencyKeyRequired {
		t.Errorf("reason is %q, want %q", errs.ReasonOf(err), errs.ReasonIdempotencyKeyRequired)
	}
}

func TestOverlongKeyIsRefused(t *testing.T) {
	k := validKey(t)
	for len(k.Value) <= idempotency.MaxKeyLength {
		k.Value += "x"
	}
	if err := k.Validate(); err == nil {
		t.Fatal("an overlong idempotency key was accepted")
	}
}

// validKey builds a well-formed key. The tenant comes from idgen's
// deterministic generator so the test never depends on entropy.
func validKey(t *testing.T) idempotency.Key {
	t.Helper()
	var gen idgen.Sequential
	tenant, err := idgen.TenantID(&gen)
	if err != nil {
		t.Fatalf("TenantID: %v", err)
	}
	return idempotency.Key{
		TenantID: tenant,
		Endpoint: "/v1/transactions:commit",
		Value:    "client-supplied-key-0001",
	}
}
