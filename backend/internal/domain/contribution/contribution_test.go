package contribution_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/contribution"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/fiscaltest"
)

var (
	entity = id.NewLegalEntityID(uuid.MustParse("01920a4c-0000-7000-8000-00000000e001"))
	alice  = id.NewUserID(uuid.MustParse("01920a4c-0000-7000-8000-0000000000a1"))
	bob    = id.NewUserID(uuid.MustParse("01920a4c-0000-7000-8000-0000000000a2"))
	at     = time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
)

func event(t *testing.T, source string, eligible, excluded string) contribution.Contribution {
	t.Helper()
	c := contribution.Contribution{
		ID:     id.NewContributionID(uuid.New()),
		Source: contribution.SourceKey{EventID: source, Version: 1}, SourceType: contribution.SourceBSSInvoice,
		Regime: contribution.RegimeUSUSF, Period: "2026-09", LegalEntity: entity,
		ClassificationRef: "usf:interstate-telecom", SitusRef: "US", SourceRef: "INV-" + source,
		RuleVersion:  "usf-499a@2026.1",
		EligibleBase: fiscaltest.Money(t, eligible, "USD"), ExcludedBase: fiscaltest.Money(t, excluded, "USD"),
		RecordedAt: at, RecordedBy: alice,
	}
	if !c.ExcludedBase.IsZero() {
		c.ExclusionReason = "International-only revenue"
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("validate %s: %v", source, err)
	}
	if err := c.Seal(); err != nil {
		t.Fatal(err)
	}
	folded, err := contribution.Fold(c, []contribution.StatusEvent{contribution.Recorded(alice, at)})
	if err != nil {
		t.Fatal(err)
	}
	return folded
}

func reversal(t *testing.T, source string, of contribution.Contribution, eligible, excluded string) contribution.Contribution {
	t.Helper()
	c := event(t, "placeholder", "0", "0")
	c.Source = contribution.SourceKey{EventID: source, Version: 1}
	c.EligibleBase, c.ExcludedBase = fiscaltest.Money(t, eligible, "USD"), fiscaltest.Money(t, excluded, "USD")
	k := of.Source
	c.Reverses = &k
	if !c.ExcludedBase.IsZero() {
		c.ExclusionReason = "Reverses the international-only part"
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("validate reversal: %v", err)
	}
	if err := c.CheckReversal(of); err != nil {
		t.Fatalf("check reversal: %v", err)
	}
	if err := c.Seal(); err != nil {
		t.Fatal(err)
	}
	return c
}

// UAT-OBL-026: the same payload is the same record; a different payload
// under the same source key is a different hash, which the service refuses.
func TestTheImmutableHashDecidesIdempotency(t *testing.T) {
	a := event(t, "E-1", "1000.00", "0.00")
	again := event(t, "E-1", "1000.00", "0.00")
	if !a.SamePayload(again) {
		t.Fatal("the same source payload hashed differently")
	}
	changed := event(t, "E-1", "1000.01", "0.00")
	if a.SamePayload(changed) {
		t.Fatal("a different amount under the same source key hashed the same")
	}
	// Scale is significant: 1000.0 and 1000.00 are different assertions.
	rescaled := event(t, "E-1", "1000.0", "0.00")
	if a.SamePayload(rescaled) {
		t.Fatal("a different scale hashed the same")
	}
	// Identity and recording time are not part of the payload.
	moved := again
	moved.ID, moved.RecordedAt, moved.RecordedBy = id.NewContributionID(uuid.New()), at.Add(time.Hour), bob
	if err := moved.Seal(); err != nil {
		t.Fatal(err)
	}
	if !a.SamePayload(moved) {
		t.Fatal("who recorded the event changed its payload hash")
	}
}

// UAT-OBL-027: a reversal is a negative contribution linked to its
// original, and never more than the original.
func TestAReversalIsNegativeAndLinked(t *testing.T) {
	orig := event(t, "E-1", "1000.00", "50.00")
	rev := reversal(t, "E-1-CR", orig, "-1000.00", "-50.00")
	if !rev.IsReversal() || *rev.Reverses != orig.Source {
		t.Fatal("the reversal does not name its original")
	}

	positive := rev
	positive.EligibleBase = fiscaltest.Money(t, "10.00", "USD")
	if err := positive.Validate(); errs.ReasonOf(err) != errs.ReasonInvalidValue {
		t.Fatalf("a positive reversal: %v", err)
	}
	zero := rev
	zero.EligibleBase, zero.ExcludedBase = fiscaltest.Money(t, "0.00", "USD"), fiscaltest.Money(t, "0.00", "USD")
	if err := zero.Validate(); err == nil {
		t.Fatal("a reversal of nothing was accepted")
	}
	tooMuch := rev
	tooMuch.EligibleBase = fiscaltest.Money(t, "-1000.01", "USD")
	if err := tooMuch.CheckReversal(orig); err == nil {
		t.Fatal("a reversal of more than the original was accepted")
	}
	if err := orig.CheckReversal(rev); err == nil {
		t.Fatal("a reversal was reversed")
	}
	negative := orig
	negative.Reverses = nil
	negative.EligibleBase = fiscaltest.Money(t, "-1.00", "USD")
	if err := negative.Validate(); err == nil {
		t.Fatal("a negative contribution with no original was accepted")
	}
}

func TestAnExcludedBaseSaysWhy(t *testing.T) {
	c := event(t, "E-1", "100.00", "20.00")
	c.ExclusionReason = ""
	if err := c.Validate(); errs.ReasonOf(err) != errs.ReasonMissingField {
		t.Fatalf("an unexplained exclusion: %v", err)
	}
}

func TestQuarantineAndReleaseAreFourEyedEvents(t *testing.T) {
	c := event(t, "E-1", "100.00", "0.00")
	q, err := c.Quarantine("Duplicate import of file 7", alice, at.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	c, err = contribution.Fold(c, append(c.History, q))
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != contribution.StatusQuarantined || len(c.History) != 2 {
		t.Fatalf("status %s, history %d", c.Status, len(c.History))
	}
	if _, err := c.Quarantine("again", bob, at); errs.ReasonOf(err) != errs.ReasonStateTransitionInvalid {
		t.Fatalf("a second quarantine: %v", err)
	}
	if _, err := c.Release("Checked the file", alice, at); errs.ReasonOf(err) != contribution.ReasonMakerChecker {
		t.Fatalf("the quarantiner released their own hold: %v", err)
	}
	if _, err := c.Release("", bob, at); errs.ReasonOf(err) != errs.ReasonMissingField {
		t.Fatalf("a release without a reason: %v", err)
	}
	r, err := c.Release("Checked the file; the import is correct", bob, at.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	c, err = contribution.Fold(c, append(c.History, r))
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != contribution.StatusAccepted || len(c.History) != 3 {
		t.Fatalf("status %s, history %d", c.Status, len(c.History))
	}
}

func TestAnEventRecordedAfterItsPeriodIsAnAmendment(t *testing.T) {
	if contribution.AmendmentAt("2026-09", time.Date(2026, 9, 30, 23, 59, 59, 0, time.UTC)) {
		t.Fatal("an event inside its period is late")
	}
	if !contribution.AmendmentAt("2026-09", time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatal("an event after its period is not late")
	}
}
