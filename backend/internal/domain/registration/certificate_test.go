package registration_test

import (
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/registration"
)

func nyResale() registration.Certificate {
	return registration.Certificate{
		ID: id.NewExemptionCertificateID(uuid.MustParse("01920000-0000-7000-8000-0000000000c1")), LegalEntity: entity,
		BuyerRef: "CUST-0042", BuyerName: "Hudson Wireless LLC", Type: registration.TypeResale,
		ExemptionReason: "Purchase for resale of telecommunication services", Jurisdiction: "US-NY",
		CertificateNumber: "ST-120-0042", IssuedOn: date("2026-09-15"), ExpiresOn: date("2027-09-14"),
	}
}

func received(t *testing.T, c registration.Certificate, by id.UserID) registration.Certificate {
	t.Helper()
	e, err := registration.Receive(c, doc(4), by, now)
	if err != nil {
		t.Fatal(err)
	}
	got, err := registration.FoldCertificate(c, []registration.CertificateEvent{e})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func step(t *testing.T, c registration.Certificate) func(registration.CertificateEvent, error) registration.Certificate {
	t.Helper()
	return func(e registration.CertificateEvent, err error) registration.Certificate {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		got, err := registration.FoldCertificate(c, append(slices.Clone(c.History), e))
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
}

func complete() registration.Screening {
	p := registration.CheckPass
	return registration.Screening{SellerMatches: p, BuyerMatches: p, JurisdictionMatches: p, FormAccepted: p,
		SignaturePresent: p, DatesValid: p, NumberPresent: p, Result: registration.ScreenDocumentComplete}
}

func approve() registration.Recommendation {
	return registration.Recommendation{Approve: true, EffectiveFrom: date("2026-09-15"), EffectiveTo: date("2027-09-14")}
}

// ER-025: receipt is SUBMITTED evidence version 1, never approved.
func TestReceiptIsNotApproval(t *testing.T) {
	c := received(t, nyResale(), maker)
	if c.Status != registration.CertSubmitted || c.EvidenceVersion != 1 || c.InForce(registration.Day(now)) {
		t.Fatalf("status %s version %d", c.Status, c.EvidenceVersion)
	}
	bad := doc(5)
	bad.MediaType = "application/x-msdownload"
	if _, err := registration.Receive(nyResale(), bad, maker, now); errs.ReasonOf(err) != errs.ReasonInvalidValue {
		t.Fatalf("a prohibited type was received: %v", err)
	}
	bad = doc(5)
	bad.Bytes = registration.MaxDocument + 1
	if _, err := registration.Receive(nyResale(), bad, maker, now); errs.ReasonOf(err) != errs.ReasonInvalidValue {
		t.Fatalf("an oversized document was received: %v", err)
	}
}

// ER-030: a certificate lacking a signature is incomplete and waits for a
// corrected document; a corrected document is the next evidence version.
func TestIncompleteScreeningWaitsForANewVersion(t *testing.T) {
	c := received(t, nyResale(), maker)
	s := complete()
	s.SignaturePresent = registration.CheckFail
	if _, err := c.Screen(s, "", checker, now); errs.ReasonOf(err) != errs.ReasonInvalidValue {
		t.Fatalf("a failed criterion screened complete: %v", err)
	}
	s.Result = registration.ScreenIncomplete
	c = step(t, c)(c.Screen(s, "The buyer did not sign section 4", checker, now))
	if c.Status != registration.CertScreening || !slices.Contains(c.FlagsAt(registration.Day(now)), registration.FlagCorrectionRequested) {
		t.Fatalf("status %s", c.Status)
	}
	if _, err := c.Recommend(approve(), "fine", checker, now); errs.ReasonOf(err) != errs.ReasonStateTransitionInvalid {
		t.Fatalf("an incomplete certificate was recommended: %v", err)
	}
	c = step(t, c)(c.AddVersion(doc(6), "Signed copy", maker, now))
	if c.Status != registration.CertSubmitted || c.EvidenceVersion != 2 || len(c.Documents()) != 2 {
		t.Fatalf("status %s version %d", c.Status, c.EvidenceVersion)
	}
	if _, ok := c.LastScreening(); ok {
		t.Fatal("the old version's screening counts for the new one")
	}
}

// ER-040, ER-041, ER-042: maker and checker are different people, the
// uploader never approves, and approval after a replacement is a version
// conflict.
func TestDualControlOnTheExactVersion(t *testing.T) {
	c := received(t, nyResale(), maker)
	c = step(t, c)(c.Screen(complete(), "", checker, now))
	c = step(t, c)(c.Recommend(approve(), "NY ST-120 complete, buyer and seller match", checker, now))
	if !slices.Contains(c.FlagsAt(registration.Day(now)), registration.FlagAwaitingSecondReview) {
		t.Fatalf("flags %v", c.FlagsAt(registration.Day(now)))
	}
	if _, err := c.Decide(registration.DecisionConfirm, 1, "", checker, now); errs.ReasonOf(err) != registration.ReasonMakerChecker {
		t.Fatalf("the recommender confirmed: %v", err)
	}
	if _, err := c.Decide(registration.DecisionConfirm, 1, "", maker, now); errs.ReasonOf(err) != registration.ReasonMakerChecker {
		t.Fatalf("the uploader approved their own document: %v", err)
	}
	if _, err := c.Decide(registration.DecisionConfirm, 2, "", third, now); errs.ReasonOf(err) != registration.ReasonVersionConflict {
		t.Fatalf("a different version was confirmed: %v", err)
	}
	// A replacement while the recommendation waits voids it.
	replaced := step(t, c)(c.AddVersion(doc(7), "rescanned", maker, now))
	if _, err := replaced.Decide(registration.DecisionConfirm, 1, "", third, now); errs.ReasonOf(err) != registration.ReasonVersionConflict {
		t.Fatalf("approval after replacement: %v", err)
	}
	c = step(t, c)(c.Decide(registration.DecisionConfirm, 1, "Checked against the original", third, now))
	rec, mk, ck, _, ok := c.Approval()
	if c.Status != registration.CertApproved || !ok || mk != checker || ck != third || !rec.EffectiveTo.Equal(date("2027-09-14")) {
		t.Fatalf("status %s approval %v %s %s", c.Status, ok, mk, ck)
	}
	if !c.InForce(registration.Day(now)) {
		t.Fatal("an approved certificate within its dates is not in force")
	}
}

// A returned recommendation is not a decision; the next recommendation is.
func TestReturnClearsTheRecommendation(t *testing.T) {
	c := received(t, nyResale(), maker)
	c = step(t, c)(c.Screen(complete(), "", checker, now))
	c = step(t, c)(c.Recommend(approve(), "ok", checker, now))
	if _, err := c.Decide(registration.DecisionReturn, 1, "", third, now); errs.ReasonOf(err) != errs.ReasonMissingField {
		t.Fatalf("a return with no reason: %v", err)
	}
	c = step(t, c)(c.Decide(registration.DecisionReturn, 1, "Scope should exclude prepaid", third, now))
	if _, ok := c.Pending(); ok || c.Status != registration.CertReviewRequired {
		t.Fatalf("status %s", c.Status)
	}
	rej := registration.Recommendation{Approve: false, EffectiveFrom: date("2026-01-01")}
	c = step(t, c)(c.Recommend(rej, "Prepaid resale lacks source facts", checker, now))
	c = step(t, c)(c.Decide(registration.DecisionConfirm, 1, "", third, now))
	if c.Status != registration.CertRejected || c.InForce(registration.Day(now)) {
		t.Fatalf("status %s", c.Status)
	}
	if _, err := c.AddVersion(doc(8), "", maker, now); errs.ReasonOf(err) != errs.ReasonStateTransitionInvalid {
		t.Fatalf("a rejected certificate took a new version: %v", err)
	}
}

// T15-13A, ER-037, ER-038, ER-039: an expired document is not approved, an
// expiring one is flagged, and expiry or revocation keeps the history.
func TestExpiryAndRevocation(t *testing.T) {
	old := nyResale()
	old.IssuedOn, old.ExpiresOn = date("2024-01-01"), date("2025-12-31")
	c := received(t, old, maker)
	if _, err := c.Screen(complete(), "", checker, now); errs.ReasonOf(err) != errs.ReasonInvalidValue {
		t.Fatalf("an expired document screened complete: %v", err)
	}
	if !slices.Contains(c.FlagsAt(registration.Day(now)), registration.FlagPastExpiry) {
		t.Fatalf("flags %v", c.FlagsAt(registration.Day(now)))
	}

	soon := nyResale()
	soon.ExpiresOn = date("2026-11-19")
	c = received(t, soon, maker)
	c = step(t, c)(c.Screen(complete(), "", checker, now))
	r := approve()
	r.EffectiveTo = date("2026-11-19")
	c = step(t, c)(c.Recommend(r, "ok", checker, now))
	c = step(t, c)(c.Decide(registration.DecisionConfirm, 1, "", third, now))
	if !slices.Contains(c.FlagsAt(registration.Day(now)), registration.FlagExpiringSoon) {
		t.Fatalf("a certificate expiring tomorrow is not flagged: %v", c.FlagsAt(registration.Day(now)))
	}
	if _, err := c.Transition(registration.CertExpired, "", checker, now); errs.ReasonOf(err) != errs.ReasonStateTransitionInvalid {
		t.Fatalf("expired before its end: %v", err)
	}
	later := now.AddDate(0, 0, 2)
	if c.InForce(registration.Day(later)) || !slices.Contains(c.FlagsAt(registration.Day(later)), registration.FlagPastExpiry) {
		t.Fatal("a lapsed approval is still in force")
	}
	c = step(t, c)(c.Transition(registration.CertExpired, "", checker, later))
	if c.Status != registration.CertExpired || len(c.History) != 5 {
		t.Fatalf("status %s history %d", c.Status, len(c.History))
	}
	if _, _, _, _, ok := c.Approval(); !ok {
		t.Fatal("the approval vanished from the history")
	}
}

func TestSuspensionIsFourEyed(t *testing.T) {
	c := received(t, nyResale(), maker)
	c = step(t, c)(c.Screen(complete(), "", checker, now))
	c = step(t, c)(c.Recommend(approve(), "ok", checker, now))
	c = step(t, c)(c.Decide(registration.DecisionConfirm, 1, "", third, now))
	c = step(t, c)(c.Transition(registration.CertSuspended, "Buyer reported a name change", checker, now))
	if c.InForce(registration.Day(now)) {
		t.Fatal("a suspended certificate is in force")
	}
	if _, err := c.Transition(registration.CertApproved, "resolved", checker, now); errs.ReasonOf(err) != registration.ReasonMakerChecker {
		t.Fatalf("self-reinstatement: %v", err)
	}
	c = step(t, c)(c.Transition(registration.CertApproved, "Name change evidenced", third, now))
	sup, err := c.Supersede(id.NewExemptionCertificateID(uuid.MustParse("01920000-0000-7000-8000-0000000000c2")), third, now)
	c = step(t, c)(sup, err)
	if c.Status != registration.CertSuperseded || c.InForce(registration.Day(now)) {
		t.Fatalf("status %s", c.Status)
	}
}
