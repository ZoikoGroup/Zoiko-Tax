package registration_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/registration"
)

var (
	maker   = id.NewUserID(uuid.MustParse("01920000-0000-7000-8000-0000000000d1"))
	checker = id.NewUserID(uuid.MustParse("01920000-0000-7000-8000-0000000000d2"))
	third   = id.NewUserID(uuid.MustParse("01920000-0000-7000-8000-0000000000d3"))
	entity  = id.NewLegalEntityID(uuid.MustParse("01920000-0000-7000-8000-00000000e001"))
	now     = time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
)

func date(s string) time.Time {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return t
}

func doc(sha byte) registration.Attachment {
	return registration.Attachment{SHA256: strings.Repeat(string("0123456789abcdef"[sha%16]), 64), Filename: "permit.pdf",
		MediaType: "application/pdf", Bytes: 1024}
}

func usac499() registration.Registration {
	return registration.Registration{
		ID: id.NewRegistrationID(uuid.MustParse("01920000-0000-7000-8000-0000000000f1")), LegalEntity: entity,
		Scheme: registration.SchemeUSUSAC499, Jurisdiction: "US", Authority: "USAC", Number: "812345",
		EffectiveFrom: date("2026-01-01"), EffectiveTo: date("2026-12-31"),
	}
}

func opened(t *testing.T, r registration.Registration, submit bool) registration.Registration {
	t.Helper()
	events, err := registration.Open(r, []registration.Attachment{doc(1)}, submit, maker, now)
	if err != nil {
		t.Fatal(err)
	}
	got, err := registration.Fold(r, events)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func apply(t *testing.T, r registration.Registration) func(registration.Event, error) registration.Registration {
	t.Helper()
	return func(e registration.Event, err error) registration.Registration {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		got, err := registration.Fold(r, append(slices.Clone(r.History), e))
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
}

func verified() registration.Check {
	return registration.Check{Source: registration.SourceUSAC, Method: registration.MethodManualLookup,
		Outcome: registration.OutcomeVerified, Reference: "USAC-499-LOOKUP-2026-11-18", CheckedAt: now.Add(-time.Hour)}
}

// ER-010, ER-011, XC-05: FRN, 499 Filer ID and 498 ID are distinct, and a
// number of one scheme entered under another is a typed mismatch.
func TestSchemesAreDistinctAndNeverConverted(t *testing.T) {
	for _, tc := range []struct {
		scheme registration.Scheme
		number string
		want   errs.ReasonCode
	}{
		{registration.SchemeUSFCCFRN, "0012345678", ""},
		{registration.SchemeUSUSAC499, "812345", ""},
		{registration.SchemeUSUSAC498, "143001234", ""},
		{registration.SchemeUSUSAC499, "0012345678", registration.ReasonSchemeMismatch}, // an FRN in the 499 field
		{registration.SchemeUSFCCFRN, "812345", registration.ReasonSchemeMismatch},
		{registration.SchemeEUVAT, "GB123456789", registration.ReasonSchemeMismatch}, // ER-018: GB is not VIES
		{registration.SchemeUKVAT, "GB123456789", ""},
		{registration.SchemeUSUSAC499, "81234", errs.ReasonInvalidValue},
		{registration.SchemeUSSalesTax, "NY-12345678", ""},
	} {
		n := registration.NormalizeNumber(tc.scheme, tc.number)
		var got errs.ReasonCode
		if err := registration.CheckNumber(tc.scheme, n); err != nil {
			got = errs.ReasonOf(err)
		}
		if got != tc.want {
			t.Errorf("%s %q: %q, want %q", tc.scheme, tc.number, got, tc.want)
		}
	}
	r := usac499()
	r.Jurisdiction = "US-NY"
	if err := r.Validate(); errs.ReasonOf(err) != errs.ReasonInvalidValue {
		t.Fatalf("a federal registration in a state: %v", err)
	}
}

// ER-013, ER-014: a submitted registration is SUBMITTED, never ACTIVE.
func TestASubmittedRegistrationIsNotActive(t *testing.T) {
	r := opened(t, usac499(), true)
	if r.Status != registration.StatusSubmitted || r.InForce(registration.Day(now)) || r.Version() != 3 {
		t.Fatalf("status %s version %d", r.Status, r.Version())
	}
	if _, err := r.Transition(registration.StatusActive, "approved", r.Version(), checker, now); errs.ReasonOf(err) != errs.ReasonStateTransitionInvalid {
		t.Fatalf("SUBMITTED went straight to ACTIVE: %v", err)
	}
	if _, err := r.Transition(registration.StatusVerified, "looks fine", r.Version(), checker, now); errs.ReasonOf(err) != errs.ReasonStateTransitionInvalid {
		t.Fatalf("VERIFIED without a check: %v", err)
	}
}

// ER-015, XC-06: an unavailable or inconclusive authority never verifies.
func TestAnInconclusiveCheckNeverVerifies(t *testing.T) {
	r := opened(t, usac499(), true)
	for _, o := range []registration.CheckOutcome{registration.OutcomeUnavailable, registration.OutcomeInconclusive} {
		c := verified()
		c.Outcome, c.Reference = o, ""
		r = apply(t, r)(r.RecordCheck(c, "USAC lookup timed out", checker, now))
		if r.Status != registration.StatusSubmitted || !slices.Contains(r.FlagsAt(registration.Day(now)), registration.FlagPendingValidation) {
			t.Fatalf("%s: status %s flags %v", o, r.Status, r.FlagsAt(registration.Day(now)))
		}
	}
	c := verified()
	c.Reference = ""
	if _, err := r.RecordCheck(c, "", checker, now); errs.ReasonOf(err) != errs.ReasonMissingField {
		t.Fatalf("VERIFIED with no authority reference: %v", err)
	}
	c = verified()
	c.Source = registration.SourceVIES
	if _, err := r.RecordCheck(c, "", checker, now); errs.ReasonOf(err) != errs.ReasonInvalidValue {
		t.Fatalf("a 499 checked against VIES: %v", err)
	}
	if _, err := r.RecordCheck(verified(), "", maker, now); errs.ReasonOf(err) != registration.ReasonMakerChecker {
		t.Fatalf("the submitter verified their own registration: %v", err)
	}
}

// Doc 17 §07: VERIFIED is not ACTIVE until someone other than the
// submitter activates the exact version, from its effective date.
func TestActivationIsFourEyedAndVersioned(t *testing.T) {
	r := opened(t, usac499(), true)
	r = apply(t, r)(r.RecordCheck(verified(), "", checker, now))
	if r.Status != registration.StatusVerified || r.InForce(registration.Day(now)) {
		t.Fatalf("status %s", r.Status)
	}
	if _, err := r.Transition(registration.StatusActive, "approved", r.Version(), maker, now); errs.ReasonOf(err) != registration.ReasonMakerChecker {
		t.Fatalf("the submitter activated: %v", err)
	}
	stale := r.Version()
	r = apply(t, r)(r.RecordAttachment(doc(2), maker, now))
	if _, err := r.Transition(registration.StatusActive, "approved", stale, checker, now); errs.ReasonOf(err) != registration.ReasonVersionConflict {
		t.Fatalf("activation of a stale version: %v", err)
	}
	future := usac499()
	future.EffectiveFrom = date("2027-01-01")
	future.EffectiveTo = time.Time{}
	f := opened(t, future, true)
	f = apply(t, f)(f.RecordCheck(verified(), "", checker, now))
	if _, err := f.Transition(registration.StatusActive, "approved", f.Version(), checker, now); errs.ReasonOf(err) != errs.ReasonStateTransitionInvalid {
		t.Fatalf("activated before its effective date: %v", err)
	}
	r = apply(t, r)(r.Transition(registration.StatusActive, "Officer approval on file", r.Version(), checker, now))
	if !r.InForce(registration.Day(now)) || !slices.Contains(r.FlagsAt(registration.Day(now)), registration.FlagExpiringSoon) {
		t.Fatalf("in force %v flags %v", r.InForce(registration.Day(now)), r.FlagsAt(registration.Day(now)))
	}
}

// ER-012: an expired or revoked registration stays as recorded, and is not
// in force. Expiry only once the end date has passed; a negative check does
// not suspend by itself; reinstatement is four-eyed.
func TestExpiryRevocationAndSuspension(t *testing.T) {
	r := opened(t, usac499(), true)
	r = apply(t, r)(r.RecordCheck(verified(), "", checker, now))
	r = apply(t, r)(r.Transition(registration.StatusActive, "ok", r.Version(), checker, now))
	if _, err := r.Transition(registration.StatusExpired, "", r.Version(), checker, now); errs.ReasonOf(err) != errs.ReasonStateTransitionInvalid {
		t.Fatalf("expired before its end date: %v", err)
	}
	neg := verified()
	neg.Outcome = registration.OutcomeNegative
	r = apply(t, r)(r.RecordCheck(neg, "USAC shows the filer as inactive", checker, now))
	if r.Status != registration.StatusActive || !slices.Contains(r.FlagsAt(registration.Day(now)), registration.FlagAuthorityNegative) {
		t.Fatalf("a negative check: status %s", r.Status)
	}
	r = apply(t, r)(r.Transition(registration.StatusSuspended, "USAC shows inactive", r.Version(), checker, now))
	if _, err := r.Transition(registration.StatusActive, "resolved", r.Version(), checker, now); errs.ReasonOf(err) != registration.ReasonMakerChecker {
		t.Fatalf("self-reinstatement: %v", err)
	}
	later := date("2027-01-02").Add(time.Hour)
	if _, err := r.Transition(registration.StatusActive, "resolved", r.Version(), third, later); errs.ReasonOf(err) != errs.ReasonStateTransitionInvalid {
		t.Fatalf("reinstated past its end: %v", err)
	}
	r = apply(t, r)(r.Transition(registration.StatusExpired, "", r.Version(), third, later))
	if r.InForce(registration.Day(later)) || r.Status != registration.StatusExpired || len(r.History) != 8 {
		t.Fatalf("status %s history %d", r.Status, len(r.History))
	}
	if _, err := r.RecordAttachment(doc(3), maker, later); errs.ReasonOf(err) != errs.ReasonStateTransitionInvalid {
		t.Fatalf("evidence on an expired registration: %v", err)
	}
}

func TestRegistrationFoldRefusesAnInventedMove(t *testing.T) {
	r := opened(t, usac499(), true)
	bad := registration.Event{Seq: r.Version() + 1, Kind: registration.EventActivated, Status: registration.StatusActive,
		RecordedAt: now, RecordedBy: checker}
	if _, err := registration.Fold(r, append(slices.Clone(r.History), bad)); err == nil {
		t.Fatal("a SUBMITTED registration folded straight to ACTIVE")
	}
}

func TestMaskNumber(t *testing.T) {
	if got := registration.MaskNumber("0012345678"); got != "****5678" {
		t.Fatalf("%q", got)
	}
	if got := registration.MaskNumber("123"); got != "****" {
		t.Fatalf("%q", got)
	}
}
