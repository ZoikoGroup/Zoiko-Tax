package notice_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/notice"
)

var (
	t0       = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	preparer = id.NewUserID(uuid.MustParse("01920000-0000-7000-8000-0000000000a1"))
	approver = id.NewUserID(uuid.MustParse("01920000-0000-7000-8000-0000000000a2"))
	entity   = id.NewLegalEntityID(uuid.MustParse("01920000-0000-7000-8000-00000000e001"))
)

func date(s string) time.Time {
	d, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return d
}

func header() notice.Notice {
	return notice.Notice{
		ID:   id.NewNoticeID(uuid.MustParse("01920c00-0000-7000-8000-000000000001")),
		Type: notice.TypeAssessment, Authority: "DE-BZSt", Jurisdiction: "DE", LegalEntity: entity,
		AuthorityRef: "ST-2026-0042", ReceivedAt: t0, CreatedAt: t0, CreatedBy: preparer,
	}
}

func verified(due string) notice.Deadline {
	return notice.Deadline{Date: date(due), Timezone: "Europe/Berlin", Source: notice.SourceStatedOnNotice, Verified: true}
}

// open registers a notice and folds it.
func open(t *testing.T, n notice.Notice, in notice.Intake) notice.Notice {
	t.Helper()
	events, err := notice.Open(n, in, preparer, t0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	out, err := notice.Fold(n, events)
	if err != nil {
		t.Fatalf("fold: %v", err)
	}
	return out
}

// apply appends events to a notice, refolding to prove the history is one a
// correct writer produces.
func apply(t *testing.T, n notice.Notice, events ...notice.Event) notice.Notice {
	t.Helper()
	out, err := notice.Fold(n, append(append([]notice.Event(nil), n.History...), events...))
	if err != nil {
		t.Fatalf("fold: %v", err)
	}
	return out
}

func must(t *testing.T) func(notice.Event, error) notice.Event {
	return func(e notice.Event, err error) notice.Event {
		t.Helper()
		if err != nil {
			t.Fatalf("unexpected refusal: %v", err)
		}
		return e
	}
}

func mustAll(t *testing.T) func([]notice.Event, error) []notice.Event {
	return func(e []notice.Event, err error) []notice.Event {
		t.Helper()
		if err != nil {
			t.Fatalf("unexpected refusal: %v", err)
		}
		return e
	}
}

func refused(t *testing.T, err error, reason errs.ReasonCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected refusal %s, got none", reason)
	}
	if got := errs.ReasonOf(err); got != reason {
		t.Fatalf("refused with %s (%v), want %s", got, err, reason)
	}
}

func hasFlag(flags []notice.Flag, f notice.Flag) bool {
	for _, g := range flags {
		if g == f {
			return true
		}
	}
	return false
}

// inLegalReview is a verified notice, accepted into legal review.
func inLegalReview(t *testing.T) notice.Notice {
	t.Helper()
	n := open(t, header(), notice.Intake{Deadline: verified("2026-10-31"), DeadlineEvidence: "Stated on page 1."})
	n = apply(t, n, must(t)(n.Transition(notice.StatusAuthenticityReview, "", preparer, t0)))
	n = apply(t, n, mustAll(t)(n.RecordAuthenticity(notice.AuthenticityVerified, "Called the authority's published number.", preparer, t0))...)
	n = apply(t, n, must(t)(n.Transition(notice.StatusLegalReview, "", preparer, t0)))
	if n.Status != notice.StatusLegalReview {
		t.Fatalf("status %s", n.Status)
	}
	return n
}

// submitted is a notice whose approved response has the authority's receipt.
func submitted(t *testing.T) notice.Notice {
	t.Helper()
	n := inLegalReview(t)
	n = apply(t, n, must(t)(n.PrepareResponse("Objection to the assessment", "", preparer, t0)))
	n = apply(t, n, must(t)(n.ApproveResponse(1, "", approver, t0)))
	return apply(t, n, must(t)(n.RecordSubmission("RCPT-1", "Portal receipt", "", preparer, t0)))
}

func TestTheHappyPathWalksTheStateMachine(t *testing.T) {
	n := submitted(t)
	if n.Status != notice.StatusSubmitted {
		t.Fatalf("status %s", n.Status)
	}
	n = apply(t, n, must(t)(n.RecordOutcome(notice.OutcomeRecord{Outcome: notice.OutcomeAccepted, AuthorityRef: "DEC-9",
		Evidence: "Decision letter"}, preparer, t0)))
	n = apply(t, n, must(t)(n.Close("Accepted by the authority.", preparer, t0)))
	if n.Status != notice.StatusClosed {
		t.Fatalf("status %s", n.Status)
	}
	if len(n.FlagsAt(t0)) != 0 {
		t.Fatalf("a closed notice raises no flags: %v", n.FlagsAt(t0))
	}
}

func TestFoldRefusesAHistoryNoWriterProduces(t *testing.T) {
	n := inLegalReview(t)
	bad := append(append([]notice.Event(nil), n.History...),
		notice.Event{Seq: len(n.History) + 1, Kind: notice.EventSubmitted, Status: notice.StatusSubmitted, RecordedBy: preparer})
	if _, err := notice.Fold(header(), bad); err == nil {
		t.Fatal("LEGAL_REVIEW to SUBMITTED skips approval and must not fold")
	}
	if _, err := notice.Fold(header(), n.History[1:]); err == nil {
		t.Fatal("a history without its RECEIVED event must not fold")
	}
}

func TestIntakeValidatesTheHeaderAndDeadline(t *testing.T) {
	bad := header()
	bad.Jurisdiction = "Germany"
	if _, err := notice.Open(bad, notice.Intake{Deadline: verified("2026-10-31"), DeadlineEvidence: "x"}, preparer, t0); err == nil {
		t.Fatal("a jurisdiction that is not an ISO code is refused")
	}
	// A verified deadline says how; an estimate is never verified; an unknown
	// date claims no source.
	for _, d := range []notice.Deadline{
		verified("2026-10-31"),
		{Date: date("2026-10-31"), Timezone: "Europe/Berlin", Source: notice.SourceEstimated, Verified: true},
		{Timezone: "Europe/Berlin", Source: notice.SourceStatute},
		{Date: date("2026-10-31"), Timezone: "Mars/Olympus", Source: notice.SourceStatute},
	} {
		if _, err := notice.Open(header(), notice.Intake{Deadline: d}, preparer, t0); err == nil {
			t.Fatalf("deadline %+v with no evidence must be refused", d)
		}
	}
}

// W17: an unknown or unverified deadline is an urgent legal review, and no
// extension is recorded against it.
func TestUnknownDeadlineIsUrgentAndAdmitsNoExtension(t *testing.T) {
	n := open(t, header(), notice.Intake{Deadline: notice.Deadline{Timezone: "Europe/Berlin", Source: notice.SourceUnknown}})
	flags := n.FlagsAt(t0)
	if !hasFlag(flags, notice.FlagDeadlineUnknown) || !hasFlag(flags, notice.FlagUrgentLegalReview) {
		t.Fatalf("flags %v", flags)
	}
	_, err := n.RecordDeadline(notice.DeadlineRecord{Deadline: notice.Deadline{Kind: notice.DeadlineExtension,
		Date: date("2026-12-31"), Timezone: "Europe/Berlin", Source: notice.SourceAuthorityConfirmation, Verified: true},
		Evidence: "Phone call", AuthorityRef: "EXT-1"}, preparer, t0)
	refused(t, err, notice.ReasonDeadlineUnverified)

	unverified := open(t, header(), notice.Intake{Deadline: notice.Deadline{Date: date("2026-10-31"), Timezone: "Europe/Berlin",
		Source: notice.SourceEstimated}})
	if !hasFlag(unverified.FlagsAt(t0), notice.FlagDeadlineUnverified) || !hasFlag(unverified.FlagsAt(t0), notice.FlagUrgentLegalReview) {
		t.Fatalf("flags %v", unverified.FlagsAt(t0))
	}
	_, err = unverified.RecordDeadline(notice.DeadlineRecord{Deadline: notice.Deadline{Kind: notice.DeadlineExtension,
		Date: date("2026-12-31"), Timezone: "Europe/Berlin", Source: notice.SourceAuthorityConfirmation, Verified: true},
		Evidence: "Phone call", AuthorityRef: "EXT-1"}, preparer, t0)
	refused(t, err, notice.ReasonDeadlineUnverified)

	// A confirmation establishes the unknown date, and then it can be extended.
	n = apply(t, n, must(t)(n.RecordDeadline(notice.DeadlineRecord{Deadline: notice.Deadline{Kind: notice.DeadlineConfirmation,
		Date: date("2026-10-31"), Timezone: "Europe/Berlin", Source: notice.SourceAuthorityConfirmation, Verified: true},
		Evidence: "Letter of 2 October"}, preparer, t0)))
	if hasFlag(n.FlagsAt(t0), notice.FlagDeadlineUnknown) || !n.Current().Verified {
		t.Fatalf("confirmed deadline still flagged: %v", n.FlagsAt(t0))
	}
}

// T18-17: the statutory date moves only through an evidenced extension; the
// original stays visible; nothing resets it; the SLA clock is separate.
func TestStatutoryDeadlineChangesOnlyThroughAnEvidencedExtension(t *testing.T) {
	n := inLegalReview(t)
	ext := func(due, evidence, ref string, verifiedGrant bool) notice.DeadlineRecord {
		return notice.DeadlineRecord{Deadline: notice.Deadline{Kind: notice.DeadlineExtension, Date: date(due),
			Timezone: "Europe/Berlin", Source: notice.SourceAuthorityConfirmation, Verified: verifiedGrant},
			Evidence: evidence, AuthorityRef: ref}
	}
	_, err := n.RecordDeadline(ext("2026-11-30", "", "EXT-1", true), preparer, t0)
	refused(t, err, errs.ReasonMissingField)
	_, err = n.RecordDeadline(ext("2026-11-30", "Asked by phone", "", true), preparer, t0)
	refused(t, err, errs.ReasonMissingField)
	_, err = n.RecordDeadline(ext("2026-11-30", "Asked by phone", "EXT-1", false), preparer, t0)
	refused(t, err, errs.ReasonInvalidValue)
	_, err = n.RecordDeadline(ext("2026-10-15", "Earlier?", "EXT-1", true), preparer, t0)
	refused(t, err, errs.ReasonInvalidValue)

	// A confirmation cannot move the date.
	_, err = n.RecordDeadline(notice.DeadlineRecord{Deadline: notice.Deadline{Kind: notice.DeadlineConfirmation,
		Date: date("2026-11-15"), Timezone: "Europe/Berlin", Source: notice.SourceStatute, Verified: true},
		Evidence: "Statute"}, preparer, t0)
	refused(t, err, notice.ReasonDeadlineUnverified)

	// The SLA clock is ours and does not touch the statutory date.
	n = apply(t, n, must(t)(n.AssignOwner(approver, date("2026-10-20"), "", preparer, t0)))
	if got := n.Current().Date.Format(time.DateOnly); got != "2026-10-31" {
		t.Fatalf("SLA moved the statutory date to %s", got)
	}

	n = apply(t, n, must(t)(n.RecordDeadline(ext("2026-11-30", "Extension letter", "EXT-1", true), preparer, t0)))
	ledger := n.Deadlines()
	if len(ledger) != 2 || ledger[0].Kind != notice.DeadlineOriginal || ledger[0].Date.Format(time.DateOnly) != "2026-10-31" {
		t.Fatalf("the original deadline is not kept first in the ledger: %+v", ledger)
	}
	if n.Current().Date.Format(time.DateOnly) != "2026-11-30" || n.Original().Date.Format(time.DateOnly) != "2026-10-31" {
		t.Fatalf("current %v original %v", n.Current().Date, n.Original().Date)
	}

	// Approving, withdrawing the approval and preparing again resets nothing.
	n = apply(t, n, must(t)(n.PrepareResponse("v1", "", preparer, t0)))
	n = apply(t, n, must(t)(n.ApproveResponse(1, "", approver, t0)))
	n = apply(t, n, must(t)(n.Transition(notice.StatusLegalReview, "More work", preparer, t0)))
	n = apply(t, n, must(t)(n.PrepareResponse("v2", "", preparer, t0)))
	if n.Current().Date.Format(time.DateOnly) != "2026-11-30" || len(n.Deadlines()) != 2 {
		t.Fatalf("a reopened review moved the clock: %+v", n.Deadlines())
	}
}

func TestAClosedNoticeIsNeverReopened(t *testing.T) {
	n := submitted(t)
	n = apply(t, n, must(t)(n.RecordOutcome(notice.OutcomeRecord{Outcome: notice.OutcomeAdjusted, AuthorityRef: "DEC-9",
		Evidence: "Letter"}, preparer, t0)))
	n = apply(t, n, must(t)(n.Close("Paid as adjusted.", preparer, t0)))
	for _, to := range []notice.Status{notice.StatusLegalReview, notice.StatusAuthenticityReview, notice.StatusClosed} {
		if _, err := n.Transition(to, "reopen", preparer, t0); err == nil {
			t.Fatalf("a closed notice moved to %s", to)
		}
	}
	_, err := n.AssignOwner(approver, time.Time{}, "", preparer, t0)
	refused(t, err, errs.ReasonStateTransitionInvalid)
	_, err = n.RecordDeadline(notice.DeadlineRecord{Deadline: notice.Deadline{Kind: notice.DeadlineExtension,
		Date: date("2027-01-31"), Timezone: "Europe/Berlin", Source: notice.SourceAuthorityConfirmation, Verified: true},
		Evidence: "x", AuthorityRef: "y"}, preparer, t0)
	if err == nil {
		t.Fatal("a closed notice's clock is not extended")
	}
}

// SUP-05: authenticity that is not verified escalates to legal validation and
// admits no approved answer.
func TestUnverifiedAuthenticityEscalatesAndBlocksApproval(t *testing.T) {
	n := open(t, header(), notice.Intake{Deadline: verified("2026-10-31"), DeadlineEvidence: "Stated."})
	n = apply(t, n, must(t)(n.Transition(notice.StatusAuthenticityReview, "", preparer, t0)))
	_, err := n.Transition(notice.StatusLegalReview, "", preparer, t0)
	refused(t, err, notice.ReasonLegalValidationRequired)
	_, err = n.RecordAuthenticity(notice.AuthenticityVerified, "", preparer, t0)
	refused(t, err, errs.ReasonMissingField)

	n = apply(t, n, mustAll(t)(n.RecordAuthenticity(notice.AuthenticityUnverifiable, "No reply from the authority.", preparer, t0))...)
	if n.Status != notice.StatusLegalReview || !n.Escalated() {
		t.Fatalf("status %s escalated %v", n.Status, n.Escalated())
	}
	if !hasFlag(n.FlagsAt(t0), notice.FlagAuthenticityDisputed) || !hasFlag(n.FlagsAt(t0), notice.FlagUrgentLegalReview) {
		t.Fatalf("flags %v", n.FlagsAt(t0))
	}
	n = apply(t, n, must(t)(n.PrepareResponse("Holding reply", "", preparer, t0)))
	_, err = n.ApproveResponse(1, "", approver, t0)
	refused(t, err, notice.ReasonLegalValidationRequired)

	// Disputing authenticity after approval voids the approval.
	ok := inLegalReview(t)
	ok = apply(t, ok, must(t)(ok.PrepareResponse("Reply", "", preparer, t0)))
	ok = apply(t, ok, must(t)(ok.ApproveResponse(1, "", approver, t0)))
	ok = apply(t, ok, mustAll(t)(ok.RecordAuthenticity(notice.AuthenticityNotAuthentic, "Letterhead forged.", preparer, t0))...)
	if ok.Status != notice.StatusLegalReview {
		t.Fatalf("status %s", ok.Status)
	}
	if _, err := ok.RecordSubmission("R", "", "", preparer, t0); err == nil {
		t.Fatal("a response to a notice found not authentic is not submitted")
	}
}

// SUP-08: an absent or ambiguous recipient goes to legal review at intake, and
// no answer is approved until legal resolves it.
func TestAmbiguousRecipientGoesStraightToLegalReview(t *testing.T) {
	for _, h := range []func(notice.Notice) notice.Notice{
		func(n notice.Notice) notice.Notice { n.LegalEntity = id.LegalEntityID{}; return n },
		func(n notice.Notice) notice.Notice { n.EntityAmbiguous = true; return n },
	} {
		n := open(t, h(header()), notice.Intake{Deadline: verified("2026-10-31"), DeadlineEvidence: "Stated."})
		if n.Status != notice.StatusLegalReview || !n.Escalated() || n.History[1].Escalation != notice.EscalationEntity {
			t.Fatalf("status %s history %+v", n.Status, n.History)
		}
		if !hasFlag(n.FlagsAt(t0), notice.FlagEntityUnresolved) || !hasFlag(n.FlagsAt(t0), notice.FlagUrgentLegalReview) {
			t.Fatalf("flags %v", n.FlagsAt(t0))
		}
		n = apply(t, n, mustAll(t)(n.RecordAuthenticity(notice.AuthenticityVerified, "Verified.", preparer, t0))...)
		n = apply(t, n, must(t)(n.PrepareResponse("Reply", "", preparer, t0)))
		_, err := n.ApproveResponse(1, "", approver, t0)
		refused(t, err, notice.ReasonLegalValidationRequired)

		n = apply(t, n, must(t)(n.ResolveEntity(entity, "Tax number on the notice matches.", preparer, t0)))
		n = apply(t, n, must(t)(n.ApproveResponse(1, "", approver, t0)))
		if n.Status != notice.StatusResponseApproved {
			t.Fatalf("status %s", n.Status)
		}
	}
}

func TestResponseApprovalIsFourEye(t *testing.T) {
	n := inLegalReview(t)
	_, err := n.ApproveResponse(1, "", approver, t0)
	refused(t, err, errs.ReasonStateTransitionInvalid)
	n = apply(t, n, must(t)(n.PrepareResponse("Reply", "", preparer, t0)))
	_, err = n.ApproveResponse(1, "", preparer, t0)
	refused(t, err, notice.ReasonSegregationOfDuties)
	_, err = n.ApproveResponse(2, "", approver, t0)
	if err == nil {
		t.Fatal("a version that is not current is not approved")
	}
	n = apply(t, n, must(t)(n.ApproveResponse(1, "", approver, t0)))
	// A new version supersedes the approval and needs its own.
	n = apply(t, n, must(t)(n.PrepareResponse("Reply v2", "", approver, t0)))
	if n.Approval() != nil || n.Status != notice.StatusLegalReview {
		t.Fatalf("approval survived a new version: status %s", n.Status)
	}
	_, err = n.ApproveResponse(2, "", approver, t0)
	refused(t, err, notice.ReasonSegregationOfDuties)
}

func TestSubmissionAndOutcomeNeedTheAuthorityReceipt(t *testing.T) {
	n := inLegalReview(t)
	_, err := n.RecordSubmission("RCPT", "", "", preparer, t0)
	refused(t, err, errs.ReasonStateTransitionInvalid)
	n = apply(t, n, must(t)(n.PrepareResponse("Reply", "", preparer, t0)))
	n = apply(t, n, must(t)(n.ApproveResponse(1, "", approver, t0)))
	_, err = n.RecordSubmission("  ", "", "", preparer, t0)
	refused(t, err, errs.ReasonMissingField)
	n = apply(t, n, must(t)(n.RecordSubmission("RCPT-1", "", "", preparer, t0)))

	_, err = n.RecordOutcome(notice.OutcomeRecord{Outcome: notice.OutcomeAccepted, Evidence: "Letter"}, preparer, t0)
	refused(t, err, errs.ReasonMissingField)
	_, err = n.RecordOutcome(notice.OutcomeRecord{Outcome: "SETTLED", AuthorityRef: "D", Evidence: "Letter"}, preparer, t0)
	refused(t, err, errs.ReasonInvalidValue)
	_, err = n.Close("Done", preparer, t0)
	refused(t, err, notice.ReasonClosureBlocked)
}

// T18-20: a pending payment instruction does not settle a notice.
func TestPendingPaymentDoesNotSettle(t *testing.T) {
	n := submitted(t)
	amount, err := fiscal.ParseMoney("1250.00", "EUR")
	if err != nil {
		t.Fatal(err)
	}
	r := notice.OutcomeRecord{Outcome: notice.OutcomeAdjusted, AuthorityRef: "DEC-1", Evidence: "Letter", Amount: &amount,
		Settled: true, Settlement: notice.SettlementPaymentPending}
	_, err = n.RecordOutcome(r, preparer, t0)
	refused(t, err, notice.ReasonSettlementUnproven)
	r.Settlement = ""
	_, err = n.RecordOutcome(r, preparer, t0)
	refused(t, err, notice.ReasonSettlementUnproven)
	r.Settlement = notice.SettlementPaymentSettled
	e := must(t)(n.RecordOutcome(r, preparer, t0))
	if !e.Settled || e.OutcomeAmount.CanonicalString() != "1250.00" {
		t.Fatalf("event %+v", e)
	}
	// Not settled, with an instruction pending, is simply recorded as such.
	r.Settled, r.Settlement = false, notice.SettlementPaymentPending
	must(t)(n.RecordOutcome(r, preparer, t0))
}

// SUP-16: an appeal with a hearing to come keeps the matter open with a next
// deadline, and it cannot be closed while it runs.
func TestAnAppealWithAFutureHearingKeepsTheMatterOpen(t *testing.T) {
	n := submitted(t)
	n = apply(t, n, must(t)(n.RecordOutcome(notice.OutcomeRecord{Outcome: notice.OutcomeRejected, AuthorityRef: "DEC-1",
		Evidence: "Rejection letter"}, preparer, t0)))
	_, err := n.LodgeAppeal(notice.AppealRecord{Hearing: date("2027-02-01"), Evidence: "Appeal lodged",
		Next: notice.Deadline{Timezone: "Europe/Berlin", Source: notice.SourceUnknown}}, preparer, t0)
	refused(t, err, errs.ReasonMissingField)

	n = apply(t, n, must(t)(n.LodgeAppeal(notice.AppealRecord{Hearing: date("2027-02-01"), Evidence: "Appeal lodged",
		AuthorityRef: "APP-7", Next: notice.Deadline{Date: date("2027-01-15"), Timezone: "Europe/Berlin",
			Source: notice.SourceAuthorityConfirmation, Verified: true}}, preparer, t0)))
	if n.Status != notice.StatusAppeal {
		t.Fatalf("status %s", n.Status)
	}
	_, err = n.Close("Done", preparer, t0)
	refused(t, err, notice.ReasonClosureBlocked)
	if !hasFlag(n.FlagsAt(t0), notice.FlagHearingScheduled) {
		t.Fatalf("flags %v", n.FlagsAt(t0))
	}
	ledger := n.Deadlines()
	if ledger[0].Kind != notice.DeadlineOriginal || ledger[len(ledger)-1].Kind != notice.DeadlineAppeal ||
		n.Current().Date.Format(time.DateOnly) != "2027-01-15" {
		t.Fatalf("ledger %+v", ledger)
	}
	// The appeal is worked through the same review and four-eye approval.
	n = apply(t, n, must(t)(n.Transition(notice.StatusLegalReview, "", preparer, t0)))
	n = apply(t, n, must(t)(n.PrepareResponse("Appeal brief", "", preparer, t0)))
	n = apply(t, n, must(t)(n.ApproveResponse(2, "", approver, t0)))
	n = apply(t, n, must(t)(n.RecordSubmission("APP-RCPT", "", "", preparer, t0)))
	n = apply(t, n, must(t)(n.RecordOutcome(notice.OutcomeRecord{Outcome: notice.OutcomeAccepted, AuthorityRef: "APP-DEC",
		Evidence: "Appeal allowed"}, preparer, t0)))
	must(t)(n.Close("Appeal allowed.", preparer, t0))
}

func TestClosureWithoutOutcomeNeedsADocumentedEscalation(t *testing.T) {
	n := open(t, header(), notice.Intake{Deadline: verified("2026-10-31"), DeadlineEvidence: "Stated."})
	_, err := n.Close("Spam", preparer, t0)
	refused(t, err, notice.ReasonClosureBlocked)
	n = apply(t, n, must(t)(n.Escalate(notice.EscalationLegal, "Is this genuine?", "", preparer, t0)))
	_, err = n.Close("Spam", preparer, t0)
	refused(t, err, notice.ReasonClosureBlocked)
	n = apply(t, n, must(t)(n.Escalate(notice.EscalationLegal, "Counsel opinion", "Counsel memo LM-12: not an authority communication.", preparer, t0)))
	n = apply(t, n, must(t)(n.Transition(notice.StatusClosed, "Not an authority communication (LM-12).", preparer, t0)))
	if n.Status != notice.StatusClosed {
		t.Fatalf("status %s", n.Status)
	}
}

func TestAttachmentsAreEvidenceReferences(t *testing.T) {
	n := inLegalReview(t)
	sha := "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
	_, err := n.RecordAttachment(notice.Attachment{SHA256: "abc", Filename: "a.pdf", MediaType: "application/pdf"}, "", preparer, t0)
	refused(t, err, errs.ReasonInvalidValue)
	n = apply(t, n, must(t)(n.RecordAttachment(notice.Attachment{SHA256: sha, Filename: "notice.pdf", MediaType: "application/pdf"}, "", preparer, t0)))
	_, err = n.RecordAttachment(notice.Attachment{SHA256: sha, Filename: "again.pdf", MediaType: "application/pdf"}, "", preparer, t0)
	refused(t, err, errs.ReasonAlreadyExists)
	// An extension may cite the recorded document as its evidence.
	n = apply(t, n, must(t)(n.RecordDeadline(notice.DeadlineRecord{Deadline: notice.Deadline{Kind: notice.DeadlineExtension,
		Date: date("2026-11-30"), Timezone: "Europe/Berlin", Source: notice.SourceAuthorityConfirmation, Verified: true},
		Evidence: "Grant letter", Attachment: sha}, preparer, t0)))
	if got := n.Deadlines()[1].Attachment; got != sha {
		t.Fatalf("attachment %q", got)
	}
}

func TestDeadlineFlagsAgainstTheClock(t *testing.T) {
	n := inLegalReview(t)
	if hasFlag(n.FlagsAt(t0), notice.FlagDeadlineSoon) {
		t.Fatal("thirty days out is not soon")
	}
	if !hasFlag(n.FlagsAt(date("2026-10-27")), notice.FlagDeadlineSoon) {
		t.Fatal("four days out is soon")
	}
	if !hasFlag(n.FlagsAt(date("2026-11-02")), notice.FlagStatutoryOverdue) {
		t.Fatal("past the end of the due day in Berlin is overdue")
	}
	n = apply(t, n, must(t)(n.AssignOwner(approver, date("2026-10-10"), "", preparer, t0)))
	f := n.FlagsAt(date("2026-10-11"))
	if !hasFlag(f, notice.FlagSLABreached) || hasFlag(f, notice.FlagStatutoryOverdue) {
		t.Fatalf("an SLA breach is not a statutory one: %v", f)
	}
}
