package contribution_test

import (
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/contribution"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
)

func knownOf(cs ...contribution.Contribution) func(contribution.SourceKey) bool {
	set := map[contribution.SourceKey]bool{}
	for _, c := range cs {
		set[c.Source] = true
	}
	return func(k contribution.SourceKey) bool { return set[k] }
}

func only(t *testing.T, got []contribution.Totals) contribution.Totals {
	t.Helper()
	if len(got) != 1 {
		t.Fatalf("got %d groups, want 1", len(got))
	}
	return got[0]
}

// UAT-OBL-034: totals are exact decimals. 0.10 added ten times is 1.00, not
// 0.9999999999999999.
func TestTotalsAreExactDecimals(t *testing.T) {
	var events []contribution.Contribution
	for i := range 10 {
		events = append(events, event(t, "E-"+string(rune('a'+i)), "0.10", "0.01"))
	}
	got := only(t, mustSummarize(t, events, knownOf(events...)))
	if got.Eligible.CanonicalString() != "1.00" || got.Excluded.CanonicalString() != "0.10" {
		t.Fatalf("eligible %s excluded %s", got.Eligible.CanonicalString(), got.Excluded.CanonicalString())
	}
	if got.Accepted != 10 || got.DataIncomplete() {
		t.Fatalf("accepted %d incomplete %v", got.Accepted, got.Incomplete)
	}
}

func mustSummarize(t *testing.T, events []contribution.Contribution, known func(contribution.SourceKey) bool) []contribution.Totals {
	t.Helper()
	got, err := contribution.Summarize("2026-09", events, known)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// A quarantined event is listed and counted, never summed, and leaves the
// period DATA_INCOMPLETE; it is not deleted.
func TestAQuarantinedEventIsExcludedFromTheTotals(t *testing.T) {
	a := event(t, "E-1", "100.00", "0.00")
	b := event(t, "E-2", "250.00", "0.00")
	b.Status = contribution.StatusQuarantined
	got := only(t, mustSummarize(t, []contribution.Contribution{a, b}, knownOf(a, b)))
	if got.Eligible.CanonicalString() != "100.00" || got.Accepted != 1 || got.Quarantined != 1 {
		t.Fatalf("eligible %s accepted %d quarantined %d", got.Eligible.CanonicalString(), got.Accepted, got.Quarantined)
	}
	if !slices.Equal(got.Incomplete, []contribution.IncompleteReason{contribution.IncompleteQuarantined}) {
		t.Fatalf("incomplete %v", got.Incomplete)
	}
	alone := only(t, mustSummarize(t, []contribution.Contribution{a}, knownOf(a)))
	if alone.Digest.Equal(got.Digest) == false {
		t.Fatal("a quarantined event changed the period digest")
	}
}

// Reversals count as the negatives they are; one whose original the
// ledger does not hold leaves the period DATA_INCOMPLETE.
func TestReversalsAreNegativeInTheTotals(t *testing.T) {
	orig := event(t, "E-1", "1000.00", "0.00")
	rev := reversal(t, "E-1-CR", orig, "-400.00", "0.00")
	got := only(t, mustSummarize(t, []contribution.Contribution{orig, rev}, knownOf(orig, rev)))
	if got.Eligible.CanonicalString() != "600.00" || got.Reversed.CanonicalString() != "-400.00" || got.Reversals != 1 {
		t.Fatalf("eligible %s reversed %s reversals %d", got.Eligible.CanonicalString(), got.Reversed.CanonicalString(), got.Reversals)
	}
	if got.DataIncomplete() {
		t.Fatalf("incomplete %v", got.Incomplete)
	}
	orphan := only(t, mustSummarize(t, []contribution.Contribution{rev}, knownOf(rev)))
	if !slices.Equal(orphan.Incomplete, []contribution.IncompleteReason{contribution.IncompleteUnresolvedReversal}) {
		t.Fatalf("incomplete %v", orphan.Incomplete)
	}
}

func TestTheDigestDoesNotDependOnArrivalOrder(t *testing.T) {
	a, b, c := event(t, "E-1", "1.00", "0.00"), event(t, "E-2", "2.00", "0.00"), event(t, "E-3", "3.00", "0.50")
	first := only(t, mustSummarize(t, []contribution.Contribution{a, b, c}, nil))
	second := only(t, mustSummarize(t, []contribution.Contribution{c, a, b}, nil))
	if !first.Digest.Equal(second.Digest) || first.Digest.IsZero() {
		t.Fatalf("digests %s and %s", first.Digest, second.Digest)
	}
	fewer := only(t, mustSummarize(t, []contribution.Contribution{a, b}, nil))
	if fewer.Digest.Equal(first.Digest) {
		t.Fatal("a missing event did not change the digest")
	}
}

// Currencies and entities are never added together.
func TestGroupsAreEntityRegimeAndCurrency(t *testing.T) {
	usd := event(t, "E-1", "1.00", "0.00")
	other := event(t, "E-2", "2.00", "0.00")
	other.LegalEntity = id.NewLegalEntityID(uuid.MustParse("01920a4c-0000-7000-8000-00000000e002"))
	ca := event(t, "E-3", "3.00", "0.00")
	ca.Regime = contribution.RegimeCACRTCNCF
	got := mustSummarize(t, []contribution.Contribution{usd, other, ca}, nil)
	if len(got) != 3 {
		t.Fatalf("got %d groups, want 3", len(got))
	}
	if _, err := contribution.Summarize("2026-10", []contribution.Contribution{usd}, nil); err == nil {
		t.Fatal("an event was summarised in another period")
	}
}
