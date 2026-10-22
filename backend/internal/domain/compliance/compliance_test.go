package compliance_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/compliance"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/fiscaltest"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// Lane M's compliance package against ZTAX-COMP-001's register. Test names
// carry the requirement id each verifies, so docs/requirements.yaml can point
// at it.

var (
	tenant   = id.NewTenantID(uuid.MustParse("00000000-0000-7000-8000-000000000001"))
	entity   = id.NewLegalEntityID(uuid.MustParse("00000000-0000-7000-8000-000000000002"))
	preparer = id.NewUserID(uuid.MustParse("00000000-0000-7000-8000-000000000004"))
	reviewer = id.NewUserID(uuid.MustParse("00000000-0000-7000-8000-000000000005"))
	approver = id.NewUserID(uuid.MustParse("00000000-0000-7000-8000-000000000006"))
	attempt  = id.NewSubmissionAttemptID(uuid.MustParse("00000000-0000-7000-8000-000000000007"))
	when     = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	due      = time.Date(2026, 10, 20, 23, 59, 59, 999999000, time.UTC)
)

func digest(t *testing.T, s string) canonical.Digest {
	t.Helper()
	d, err := canonical.Sum(canonical.String(s))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func fresh(t *testing.T) compliance.Package {
	t.Helper()
	p := compliance.Package{
		ID: id.NewCompliancePackageID(uuid.MustParse("00000000-0000-7000-8000-000000000010")), TenantID: tenant,
		Obligation:    id.NewObligationID(uuid.MustParse("00000000-0000-7000-8000-000000000011")),
		ObligationKey: "xa-vat-return/le/2026-07-01", LegalEntity: entity, ObligationType: "VAT_RETURN",
		Jurisdiction: "jurisdiction:xa", PeriodStart: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		PeriodEnd: time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC), Mode: compliance.ModeSoftwareOnly,
		Channel: compliance.ChannelManualPortal, FilingCountry: "DE", FilingAuthority: "DE-ELSTER",
		StatutoryDue: due, Timezone: "UTC", CreatedAt: when, CreatedBy: preparer,
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	out, err := compliance.Fold(p, []compliance.Step{{Seq: 1, Kind: compliance.StepCreated, Status: compliance.StatusReady,
		RecordedAt: when, RecordedBy: preparer}})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// apply appends a step and folds again, as the store does.
func apply(t *testing.T, p compliance.Package, s compliance.Step, err error) compliance.Package {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	out, err := compliance.Fold(p, append(append([]compliance.Step{}, p.History...), s))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func prepared(t *testing.T) compliance.Package {
	t.Helper()
	p := fresh(t)
	total := fiscaltest.Money(t, "21.00", "EUR")
	s, err := p.Prepare(digest(t, "v1"), &total, "", preparer, when)
	return apply(t, p, s, err)
}

func approved(t *testing.T) compliance.Package {
	t.Helper()
	p := prepared(t)
	s, err := p.Review(1, compliance.ReviewAccept, "", reviewer, when)
	p = apply(t, p, s, err)
	s, err = p.Approve(1, digest(t, "v1"), "", approver, when)
	return apply(t, p, s, err)
}

func transmitting(t *testing.T) compliance.Package {
	t.Helper()
	p := approved(t)
	s, err := p.Submit(attempt, "", preparer, when)
	return apply(t, p, s, err)
}

func wantReason(t *testing.T, err error, reason errs.ReasonCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("succeeded; want %s", reason)
	}
	if got := errs.ReasonOf(err); got != reason {
		t.Fatalf("reason %s, want %s (%v)", got, reason, err)
	}
}

func TestCOMPREQ0008And0011InternalTargetsNeverPassTheStatutoryDate(t *testing.T) {
	p := fresh(t)
	p.SubmitTarget = due.Add(time.Hour)
	if err := p.Validate(); err == nil {
		t.Fatal("an internal target after the statutory due date was accepted")
	}
	p.SubmitTarget = due.Add(-48 * time.Hour)
	if err := p.Validate(); err != nil {
		t.Fatalf("an earlier internal target was refused: %v", err)
	}
	if !p.StatutoryDue.Equal(due) {
		t.Fatal("setting an internal target moved the statutory due date")
	}
}

func TestCOMPREQ0005And0006OperatingModesAreDistinguishable(t *testing.T) {
	if compliance.ModeSoftwareOnly.Managed() || !compliance.ModeManagedFile.Managed() {
		t.Fatal("software-only and managed modes are not distinguished")
	}
	p := fresh(t)
	p.Mode = "SOMETHING_ELSE"
	if err := p.Validate(); err == nil {
		t.Fatal("an unknown operating mode was accepted")
	}
}

func TestCOMPREQ0035PreparationVersionsAreImmutableAndNumbered(t *testing.T) {
	p := prepared(t)
	first := *p.Preparation()
	s, err := p.Prepare(digest(t, "v2"), nil, "", preparer, when)
	p = apply(t, p, s, err)
	if p.Preparation().Version != 2 {
		t.Fatalf("version %d, want 2", p.Preparation().Version)
	}
	if p.History[1].Version != first.Version || p.History[1].PayloadHash != first.PayloadHash {
		t.Fatal("preparing again changed the earlier version")
	}
}

func TestCOMPREQ0041PreparerCannotReviewOrApproveTheirOwnVersion(t *testing.T) {
	p := prepared(t)
	_, err := p.Review(1, compliance.ReviewAccept, "", preparer, when)
	wantReason(t, err, compliance.ReasonSegregationOfDuties)

	s, err := p.Review(1, compliance.ReviewAccept, "", reviewer, when)
	p = apply(t, p, s, err)
	_, err = p.Approve(1, digest(t, "v1"), "", preparer, when)
	wantReason(t, err, compliance.ReasonSegregationOfDuties)
}

func TestCOMPREQ0044ApprovalPinsTheExactVersionAndHash(t *testing.T) {
	p := prepared(t)
	_, err := p.Approve(1, digest(t, "v1"), "", approver, when)
	if err == nil {
		t.Fatal("a version with no accepting review was approved")
	}
	s, err := p.Review(1, compliance.ReviewAccept, "", reviewer, when)
	p = apply(t, p, s, err)
	_, err = p.Approve(1, digest(t, "something else"), "", approver, when)
	wantReason(t, err, compliance.ReasonStaleVersion)
	_, err = p.Approve(2, digest(t, "v1"), "", approver, when)
	wantReason(t, err, compliance.ReasonStaleVersion)

	s, err = p.Approve(1, digest(t, "v1"), "", approver, when)
	p = apply(t, p, s, err)
	if a := p.Approval(); a == nil || a.Version != 1 || a.PayloadHash != digest(t, "v1") {
		t.Fatalf("approval %+v does not pin version 1 and its hash", a)
	}
}

func TestCOMPREQ0045APreparationAfterApprovalInvalidatesIt(t *testing.T) {
	p := approved(t)
	s, err := p.Prepare(digest(t, "v2"), nil, "", preparer, when)
	p = apply(t, p, s, err)
	if p.Approval() != nil {
		t.Fatal("the approval of version 1 survived the preparation of version 2")
	}
	if p.Status != compliance.StatusInReview {
		t.Fatalf("status %s, want IN_REVIEW", p.Status)
	}
	if _, err := p.Submit(attempt, "", preparer, when); err == nil {
		t.Fatal("a version with no approval was submitted")
	}
}

func TestCOMPREQ0050And0051EachAttemptHasItsIdentityAndTheApprovedHash(t *testing.T) {
	p := transmitting(t)
	a := p.Attempt()
	if a == nil || a.Submission != attempt || a.PayloadHash != digest(t, "v1") || a.Channel != compliance.ChannelManualPortal {
		t.Fatalf("attempt %+v does not carry its identity, the approved hash and the channel", a)
	}
}

func TestCOMPREQ0055And0058TransmittingIsNotFiled(t *testing.T) {
	p := transmitting(t)
	if p.Status != compliance.StatusTransmitting {
		t.Fatalf("status %s, want TRANSMITTING", p.Status)
	}
	if p.Status == compliance.StatusFiled {
		t.Fatal("starting a submission filed it")
	}
}

func TestCOMPREQ0056FiledRequiresTheAuthoritysReference(t *testing.T) {
	p := transmitting(t)
	_, err := p.Acknowledge(compliance.AckReceived, "", "portal shows received", "", preparer, when)
	if errs.CategoryOf(err) != errs.CategoryValidation {
		t.Fatalf("a receipt with no reference: %v", err)
	}
	s, err := p.Acknowledge(compliance.AckReceived, "REF-1", "portal shows received", "", preparer, when)
	p = apply(t, p, s, err)
	if p.Status != compliance.StatusFiled {
		t.Fatalf("status %s, want FILED", p.Status)
	}
}

func TestCOMPREQ0059And0060UncertainBlocksBlindResubmission(t *testing.T) {
	p := transmitting(t)
	s, err := p.Acknowledge(compliance.AckUnknown, "", "portal timed out after upload", "", preparer, when)
	p = apply(t, p, s, err)
	if p.Status != compliance.StatusUncertain {
		t.Fatalf("status %s, want UNCERTAIN_SUBMISSION", p.Status)
	}
	if _, err := p.Submit(id.NewSubmissionAttemptID(uuid.New()), "", preparer, when); err == nil {
		t.Fatal("an uncertain submission was submitted again")
	}
	if compliance.CanTransition(compliance.StatusUncertain, compliance.StatusTransmitting) {
		t.Fatal("the state machine has an edge from UNCERTAIN_SUBMISSION to TRANSMITTING")
	}
}

func TestCOMPREQ0061ResolutionKeepsTheUncertainAttempt(t *testing.T) {
	p := transmitting(t)
	s, err := p.Acknowledge(compliance.AckUnknown, "", "timeout", "", preparer, when)
	p = apply(t, p, s, err)
	if _, err := p.Resolve(compliance.ResolutionConfirmedNotFiled, "", "", preparer, when); err == nil {
		t.Fatal("a resolution with no evidence was accepted")
	}
	s, err = p.Resolve(compliance.ResolutionConfirmedNotFiled, "", "authority status service has no record", preparer, when)
	p = apply(t, p, s, err)
	if p.Status != compliance.StatusApproved {
		t.Fatalf("status %s, want APPROVED", p.Status)
	}
	next := id.NewSubmissionAttemptID(uuid.New())
	s, err = p.Submit(next, "", preparer, when)
	p = apply(t, p, s, err)
	var attempts []id.SubmissionAttemptID
	for _, st := range p.History {
		if st.Kind == compliance.StepSubmitted {
			attempts = append(attempts, st.Submission)
			if st.PayloadHash != digest(t, "v1") {
				t.Fatal("an attempt lost its payload identity")
			}
		}
	}
	if len(attempts) != 2 || attempts[0] != attempt || attempts[1] != next {
		t.Fatalf("attempts %v, want the uncertain one and the next", attempts)
	}
}

func TestCOMPREQ0063RejectionKeepsTheRawResponseAndOpensRemediation(t *testing.T) {
	p := transmitting(t)
	if _, err := p.Acknowledge(compliance.AckRejectedValidation, "", "", "", preparer, when); err == nil {
		t.Fatal("a rejection with no raw response was accepted")
	}
	raw := `{"code":"E123","message":"box 81 does not reconcile"}`
	s, err := p.Acknowledge(compliance.AckRejectedValidation, "", raw, "", preparer, when)
	p = apply(t, p, s, err)
	if p.Status != compliance.StatusRejected || p.History[len(p.History)-1].Evidence != raw {
		t.Fatal("the rejection or its raw response was not kept")
	}
	// ZTAX-COMP-REQ-0065: a rejected filing is prepared again.
	if _, err := p.Prepare(digest(t, "v2"), nil, "", preparer, when); err != nil {
		t.Fatalf("a rejected filing could not be prepared again: %v", err)
	}
}

func TestCOMPREQ0017StatutoryAndInternalBreachesAreSeparate(t *testing.T) {
	p := fresh(t)
	p.SubmitTarget = due.Add(-72 * time.Hour)
	o := p.OverdueAt(due.Add(-24 * time.Hour))
	if !o.Internal || o.Statutory {
		t.Fatalf("%+v: want the internal target breached and the statutory date not", o)
	}
	o = p.OverdueAt(due.Add(time.Hour))
	if !o.Internal || !o.Statutory {
		t.Fatalf("%+v: want both breached", o)
	}
	if o := transmitting(t).OverdueAt(due.Add(time.Hour)); o.Statutory {
		t.Fatal("a submitted package was reported overdue")
	}
}

func TestCOMPREQ0062And0091PriorityWeighsTheDeadline(t *testing.T) {
	p := fresh(t)
	if got := p.PriorityAt(due.Add(-30 * 24 * time.Hour)); got != compliance.PriorityP3 {
		t.Fatalf("a month out: %s, want P3", got)
	}
	if got := p.PriorityAt(due.Add(-24 * time.Hour)); got != compliance.PriorityP0 {
		t.Fatalf("a day out: %s, want P0", got)
	}
	u := transmitting(t)
	s, err := u.Acknowledge(compliance.AckUnknown, "", "timeout", "", preparer, when)
	u = apply(t, u, s, err)
	if got := u.PriorityAt(due.Add(-24 * time.Hour)); got != compliance.PriorityP0 {
		t.Fatalf("uncertain near the deadline: %s, want P0 (escalated)", got)
	}
}

func TestCOMPREQ0129HistoryIsAppendOnlyAndFoldsInOrder(t *testing.T) {
	p := approved(t)
	bad := append([]compliance.Step{}, p.History...)
	bad[2].Seq = 9
	if _, err := compliance.Fold(p, bad); err == nil {
		t.Fatal("a history out of sequence folded")
	}
	skip := append([]compliance.Step{}, p.History[:1]...)
	skip = append(skip, compliance.Step{Seq: 2, Kind: compliance.StepApproved, Status: compliance.StatusTransmitting})
	if _, err := compliance.Fold(p, skip); err == nil {
		t.Fatal("a history that skips the state machine folded")
	}
}

func TestCancellationIsRefusedOnceSubmitted(t *testing.T) {
	if _, err := transmitting(t).Cancel("customer filed it", preparer, when); err == nil {
		t.Fatal("a submitted package was cancelled")
	}
	p := prepared(t)
	s, err := p.Cancel("customer filed it", preparer, when)
	p = apply(t, p, s, err)
	if p.Status != compliance.StatusCancelled || len(p.History) != 3 {
		t.Fatal("cancellation did not keep the history")
	}
}
