package support_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/support"
)

// The support triage queue against Engineering Doc 18 W16. Test names carry
// the requirement each verifies.

var (
	tenant  = id.NewTenantID(uuid.MustParse("00000000-0000-7000-8000-000000000001"))
	entity  = id.NewLegalEntityID(uuid.MustParse("00000000-0000-7000-8000-000000000002"))
	opener  = id.NewUserID(uuid.MustParse("00000000-0000-7000-8000-000000000004"))
	agent   = id.NewUserID(uuid.MustParse("00000000-0000-7000-8000-000000000005"))
	counsel = id.NewUserID(uuid.MustParse("00000000-0000-7000-8000-000000000006"))
	opened  = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
)

func open(t *testing.T, typ support.Type, sev support.Severity) support.Case {
	t.Helper()
	c := support.Case{
		ID: id.NewSupportCaseID(uuid.MustParse("00000000-0000-7000-8000-000000000010")), TenantID: tenant,
		Type: typ, Severity: sev, Subject: "VAT on invoice INV-1", Description: "The customer asks why VAT was charged.",
		OpenedAt: opened, OpenedBy: opener, SLADueAt: opened.Add(sev.SLATarget()),
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	folded, err := support.Fold(c, []support.Event{c.Open()})
	if err != nil {
		t.Fatal(err)
	}
	return folded
}

// act decides an act and folds the event in, as the store would.
func act(t *testing.T, c support.Case, a support.Act, by id.UserID, at time.Time) support.Case {
	t.Helper()
	e, err := c.Decide(a, by, at)
	if err != nil {
		t.Fatalf("%s on %s: %v", a.Action, c.Status, err)
	}
	next, err := support.Fold(c, append(append([]support.Event{}, c.History...), e))
	if err != nil {
		t.Fatalf("fold %s: %v", a.Action, err)
	}
	return next
}

func refused(t *testing.T, c support.Case, a support.Act, by id.UserID, reason errs.ReasonCode) {
	t.Helper()
	_, err := c.Decide(a, by, opened.Add(time.Hour))
	if err == nil {
		t.Fatalf("%s on a %s case was accepted", a.Action, c.Status)
	}
	if got := errs.ReasonOf(err); got != reason {
		t.Fatalf("%s: reason %s, want %s (%v)", a.Action, got, reason, err)
	}
}

// triaged walks a case to TRIAGED with every fact present.
func triaged(t *testing.T, typ support.Type) support.Case {
	t.Helper()
	c := open(t, typ, support.SeverityS3)
	c = act(t, c, support.Act{Action: support.ActionVerify}, agent, opened.Add(time.Minute))
	return act(t, c, support.Act{Action: support.ActionTriage, ContractRef: "MSA-2026-01", Period: "2026-09", LegalEntity: entity},
		agent, opened.Add(2*time.Minute))
}

// responded walks a triaged case through review to RESOLVED.
func resolved(t *testing.T, c support.Case) support.Case {
	t.Helper()
	c = act(t, c, support.Act{Action: support.ActionStartReview}, agent, opened.Add(3*time.Minute))
	c = act(t, c, support.Act{Action: support.ActionRespond, Note: "Explained the reduced rate."}, agent, opened.Add(4*time.Minute))
	return act(t, c, support.Act{Action: support.ActionResolve}, agent, opened.Add(5*time.Minute))
}

func TestHappyPath(t *testing.T) {
	c := resolved(t, triaged(t, support.TypeInvoiceExplanation))
	c = act(t, c, support.Act{Action: support.ActionClose}, agent, opened.Add(6*time.Minute))
	if c.Status != support.StatusClosed {
		t.Fatalf("status %s", c.Status)
	}
	if len(c.History) != 7 {
		t.Fatalf("history %d events, want 7", len(c.History))
	}
}

func TestSUP02_TriageWithoutFactsStaysIncompleteAndRequestsThem(t *testing.T) {
	c := open(t, support.TypeFiling, support.SeverityS2)
	c = act(t, c, support.Act{Action: support.ActionVerify}, agent, opened.Add(time.Minute))
	c = act(t, c, support.Act{Action: support.ActionTriage, Period: "2026-09"}, agent, opened.Add(2*time.Minute))
	if c.Status != support.StatusTriageIncomplete {
		t.Fatalf("status %s, want TRIAGE_INCOMPLETE", c.Status)
	}
	want := []support.Fact{support.FactContract, support.FactSeller}
	if len(c.MissingFacts) != 2 || c.MissingFacts[0] != want[0] || c.MissingFacts[1] != want[1] {
		t.Fatalf("missing %v, want %v", c.MissingFacts, want)
	}
	if last := c.History[len(c.History)-1]; len(last.MissingFacts) != 2 {
		t.Fatalf("the triage event did not record the facts requested: %v", last.MissingFacts)
	}
	// It cannot be worked until it is complete.
	refused(t, c, support.Act{Action: support.ActionStartReview}, agent, errs.ReasonStateTransitionInvalid)
	refused(t, c, support.Act{Action: support.ActionRespond, Note: "x"}, agent, errs.ReasonStateTransitionInvalid)

	// The facts arrive; triage again completes it. A seller reference stands
	// in for a legal entity.
	c = act(t, c, support.Act{Action: support.ActionTriage, ContractRef: "MSA-1", SellerRef: "seller:acme-de"}, agent, opened.Add(time.Hour))
	if c.Status != support.StatusTriaged || len(c.MissingFacts) != 0 {
		t.Fatalf("status %s missing %v", c.Status, c.MissingFacts)
	}
	if c.Period != "2026-09" {
		t.Fatalf("the period from the first triage was lost: %q", c.Period)
	}
}

func TestTriageNeedsVerificationFirst(t *testing.T) {
	c := open(t, support.TypeFiling, support.SeverityS2)
	refused(t, c, support.Act{Action: support.ActionTriage, ContractRef: "x", Period: "2026-09", SellerRef: "s"}, agent,
		errs.ReasonStateTransitionInvalid)
}

func TestSLATargetsBySeverity(t *testing.T) {
	for sev, want := range map[support.Severity]time.Duration{
		support.SeverityS1: 4 * time.Hour, support.SeverityS2: 24 * time.Hour,
		support.SeverityS3: 72 * time.Hour, support.SeverityS4: 7 * 24 * time.Hour,
	} {
		if got := sev.SLATarget(); got != want {
			t.Errorf("%s: %s, want %s", sev, got, want)
		}
	}
}

func TestSUP06_ClocksAreSeparateAndTheEarlierStatutoryOneTakesPrecedence(t *testing.T) {
	c := open(t, support.TypeFiling, support.SeverityS3) // SLA due opened+72h
	k := c.Clocks(opened.Add(time.Hour))
	if k.Statutory != support.ClockNone || k.StatutoryPrecedes || !k.DueAt.Equal(c.SLADueAt) || k.SLA != support.ClockOnTrack {
		t.Fatalf("no statutory deadline: %+v", k)
	}

	statutory := opened.Add(30 * time.Hour)
	c = act(t, c, support.Act{Action: support.ActionVerify}, agent, opened.Add(time.Minute))
	c = act(t, c, support.Act{Action: support.ActionTriage, StatutoryDueAt: statutory}, agent, opened.Add(2*time.Minute))
	k = c.Clocks(opened.Add(time.Hour))
	if !k.StatutoryPrecedes || !k.DueAt.Equal(statutory) {
		t.Fatalf("the earlier statutory deadline did not take precedence: %+v", k)
	}
	if !k.SLADueAt.Equal(opened.Add(72*time.Hour)) || k.SLA != support.ClockOnTrack {
		t.Fatalf("the SLA clock was folded into the statutory one: %+v", k)
	}
	if k.Statutory != support.ClockAtRisk {
		t.Fatalf("statutory %s, want AT_RISK within %s", k.Statutory, support.StatutoryWarning)
	}
	if k := c.Clocks(opened.Add(31 * time.Hour)); k.Statutory != support.ClockPassed || k.SLA != support.ClockOnTrack {
		t.Fatalf("after the statutory deadline: %+v", k)
	}

	// A later statutory deadline does not take precedence.
	d := open(t, support.TypeFiling, support.SeverityS1)
	d = act(t, d, support.Act{Action: support.ActionVerify}, agent, opened.Add(time.Minute))
	d = act(t, d, support.Act{Action: support.ActionTriage, StatutoryDueAt: opened.Add(30 * 24 * time.Hour)}, agent, opened.Add(2*time.Minute))
	if k := d.Clocks(opened.Add(time.Hour)); k.StatutoryPrecedes || !k.DueAt.Equal(d.SLADueAt) {
		t.Fatalf("a later statutory deadline took precedence: %+v", k)
	}
}

func TestSLAStates(t *testing.T) {
	c := open(t, support.TypeInvoiceExplanation, support.SeverityS1) // due opened+4h
	if k := c.Clocks(opened.Add(3*time.Hour + 30*time.Minute)); k.SLA != support.ClockAtRisk {
		t.Fatalf("with 30 minutes of 4 hours left: %s", k.SLA)
	}
	if k := c.Clocks(opened.Add(5 * time.Hour)); k.SLA != support.ClockBreached {
		t.Fatalf("past due unanswered: %s", k.SLA)
	}
	c = act(t, c, support.Act{Action: support.ActionVerify}, agent, opened.Add(time.Minute))
	c = act(t, c, support.Act{Action: support.ActionTriage, ContractRef: "c", Period: "2026-09", SellerRef: "s"}, agent, opened.Add(2*time.Minute))
	c = act(t, c, support.Act{Action: support.ActionStartReview}, agent, opened.Add(3*time.Minute))
	c = act(t, c, support.Act{Action: support.ActionRespond, Note: "answered"}, agent, opened.Add(time.Hour))
	if k := c.Clocks(opened.Add(10 * time.Hour)); k.SLA != support.ClockMet || !k.FirstResponseAt.Equal(opened.Add(time.Hour)) {
		t.Fatalf("answered in time: %+v", k)
	}
}

func TestRaisedSeverityOnlyBringsTheSLAEarlier(t *testing.T) {
	c := open(t, support.TypeFiling, support.SeverityS4)
	c = act(t, c, support.Act{Action: support.ActionVerify}, agent, opened.Add(time.Minute))
	c = act(t, c, support.Act{Action: support.ActionTriage, Severity: support.SeverityS2}, agent, opened.Add(10*time.Hour))
	if want := opened.Add(24 * time.Hour); !c.SLADueAt.Equal(want) {
		t.Fatalf("raised to S2 at +10h: due %s, want %s measured from opening", c.SLADueAt, want)
	}
	c = act(t, c, support.Act{Action: support.ActionTriage, Severity: support.SeverityS4}, agent, opened.Add(11*time.Hour))
	if want := opened.Add(24 * time.Hour); !c.SLADueAt.Equal(want) {
		t.Fatalf("lowered back to S4: due %s, want it unchanged at %s", c.SLADueAt, want)
	}
}

func TestSUP07_ReopenIsLinkedAndNeverMovesAClock(t *testing.T) {
	statutory := opened.Add(10 * 24 * time.Hour)
	c := open(t, support.TypeInvoiceExplanation, support.SeverityS3)
	c = act(t, c, support.Act{Action: support.ActionVerify}, agent, opened.Add(time.Minute))
	c = act(t, c, support.Act{Action: support.ActionTriage, ContractRef: "c", Period: "2026-09", LegalEntity: entity,
		StatutoryDueAt: statutory}, agent, opened.Add(2*time.Minute))
	c = resolved(t, c)
	c = act(t, c, support.Act{Action: support.ActionClose}, agent, opened.Add(6*time.Minute))
	before := c.Clocks(opened.Add(time.Hour))
	closeSeq := c.History[len(c.History)-1].Seq

	refused(t, c, support.Act{Action: support.ActionReopen}, agent, errs.ReasonMissingField)
	c = act(t, c, support.Act{Action: support.ActionReopen, Note: "The customer disputes the answer."}, agent, opened.Add(7*24*time.Hour))
	if c.Status != support.StatusInReview {
		t.Fatalf("reopened to %s, want IN_REVIEW", c.Status)
	}
	if got := c.History[len(c.History)-1].Reopens; got != closeSeq {
		t.Fatalf("reopening links to event %d, want the closure %d", got, closeSeq)
	}
	after := c.Clocks(opened.Add(time.Hour))
	if !after.SLADueAt.Equal(before.SLADueAt) || !after.StatutoryDueAt.Equal(before.StatutoryDueAt) ||
		!after.FirstResponseAt.Equal(before.FirstResponseAt) || after.SLA != before.SLA {
		t.Fatalf("reopening moved a clock: before %+v after %+v", before, after)
	}
	// Triage is over; and even re-triage could not move the statutory clock.
	refused(t, c, support.Act{Action: support.ActionTriage, StatutoryDueAt: statutory.Add(time.Hour)}, agent, errs.ReasonStateTransitionInvalid)

}

func TestStatutoryDeadlineIsNotChangedOnceRecorded(t *testing.T) {
	c := open(t, support.TypeFiling, support.SeverityS3)
	c = act(t, c, support.Act{Action: support.ActionVerify}, agent, opened.Add(time.Minute))
	c = act(t, c, support.Act{Action: support.ActionTriage, StatutoryDueAt: opened.Add(48 * time.Hour)}, agent, opened.Add(2*time.Minute))
	refused(t, c, support.Act{Action: support.ActionTriage, StatutoryDueAt: opened.Add(96 * time.Hour)}, agent, errs.ReasonInvalidValue)
}

func TestSUP14_ClosureNeedsARecordedResponse(t *testing.T) {
	c := triaged(t, support.TypeInvoiceExplanation)
	c = act(t, c, support.Act{Action: support.ActionStartReview}, agent, opened.Add(3*time.Minute))
	refused(t, c, support.Act{Action: support.ActionClose}, agent, errs.ReasonStateTransitionInvalid)
	refused(t, c, support.Act{Action: support.ActionRespond}, agent, errs.ReasonMissingField)

	// Reopened after closure: the old response does not close it again.
	c = act(t, c, support.Act{Action: support.ActionRespond, Note: "answered"}, agent, opened.Add(4*time.Minute))
	c = act(t, c, support.Act{Action: support.ActionResolve}, agent, opened.Add(5*time.Minute))
	c = act(t, c, support.Act{Action: support.ActionReopen, Note: "Not resolved after all."}, agent, opened.Add(6*time.Minute))
	if c.Responded() {
		t.Fatal("a response before the reopening counts toward the next closure")
	}
	if err := c.Closable(); errs.ReasonOf(err) != errs.ReasonStateTransitionInvalid {
		t.Fatalf("closable after reopening: %v", err)
	}
}

func TestT1818_LegalAndTaxSpecialistCasesNeedAnIndependentSignOff(t *testing.T) {
	for _, typ := range []support.Type{support.TypeLegal, support.TypeTaxSpecialist} {
		t.Run(string(typ), func(t *testing.T) {
			c := resolved(t, triaged(t, typ))
			refused(t, c, support.Act{Action: support.ActionClose}, agent, support.ReasonSignOffRequired)
			// The opener cannot sign off their own case, whatever their role.
			refused(t, c, support.Act{Action: support.ActionSignOff, Note: "checked"}, opener, support.ReasonSegregationOfDuties)
			c = act(t, c, support.Act{Action: support.ActionSignOff, Note: "Counsel reviewed the response."}, counsel, opened.Add(time.Hour))
			if c.SignOff() == nil {
				t.Fatal("no sign-off recorded")
			}
			// Even the opener may close once someone else has signed off.
			c = act(t, c, support.Act{Action: support.ActionClose}, opener, opened.Add(2*time.Hour))
			if c.Status != support.StatusClosed {
				t.Fatalf("status %s", c.Status)
			}
		})
	}
}

func TestT1818_ReclassifyingAwayDoesNotShedTheSignOff(t *testing.T) {
	c := open(t, support.TypeLegal, support.SeverityS2)
	c = act(t, c, support.Act{Action: support.ActionVerify}, agent, opened.Add(time.Minute))
	c = act(t, c, support.Act{Action: support.ActionTriage, Type: support.TypeInvoiceExplanation, ContractRef: "c", Period: "2026-09",
		SellerRef: "s"}, agent, opened.Add(2*time.Minute))
	if c.Type != support.TypeInvoiceExplanation {
		t.Fatalf("type %s", c.Type)
	}
	c = resolved(t, c)
	refused(t, c, support.Act{Action: support.ActionClose}, agent, support.ReasonSignOffRequired)
}

func TestFoldRefusesAHistoryNoWriterProduces(t *testing.T) {
	c := open(t, support.TypeFiling, support.SeverityS3)
	bad := []support.Event{c.Open(), {Seq: 2, Action: support.ActionClose, Status: support.StatusClosed, RecordedAt: opened, RecordedBy: agent}}
	if _, err := support.Fold(c, bad); err == nil {
		t.Fatal("a NEW case closed by its second event folded")
	}
	skipped := []support.Event{c.Open(), {Seq: 3, Action: support.ActionVerify, Status: support.StatusVerified, RecordedAt: opened, RecordedBy: agent}}
	if _, err := support.Fold(c, skipped); err == nil {
		t.Fatal("an out-of-sequence event folded")
	}
	wrong := []support.Event{c.Open(), {Seq: 2, Action: support.ActionVerify, Status: support.StatusTriaged, RecordedAt: opened, RecordedBy: agent}}
	if _, err := support.Fold(c, wrong); err == nil {
		t.Fatal("VERIFY ending in TRIAGED folded")
	}
}

func TestNonTransitionActsKeepTheStatusAndAccumulate(t *testing.T) {
	c := triaged(t, support.TypeRemittance)
	c = act(t, c, support.Act{Action: support.ActionAssign, Assignee: counsel}, agent, opened.Add(time.Hour))
	c = act(t, c, support.Act{Action: support.ActionAddEvidence, Evidence: []string{" sha256:aa ", "sha256:aa", "doc:1"}}, agent, opened.Add(time.Hour))
	c = act(t, c, support.Act{Action: support.ActionRequestEvidence, Note: "Bank statement for September."}, agent, opened.Add(time.Hour))
	c = act(t, c, support.Act{Action: support.ActionRequestLegalHold, Note: "Customer threatens litigation."}, agent, opened.Add(time.Hour))
	if c.Status != support.StatusTriaged || c.Assignee != counsel || len(c.Evidence) != 2 {
		t.Fatalf("status %s assignee %s evidence %v", c.Status, c.Assignee, c.Evidence)
	}
	refused(t, c, support.Act{Action: support.ActionAssign}, agent, errs.ReasonMissingField)
	refused(t, c, support.Act{Action: support.ActionEscalate, Note: "x", EscalateTo: "BOSS"}, agent, errs.ReasonInvalidValue)
	c = act(t, c, support.Act{Action: support.ActionEscalate, Note: "Needs a specialist.", EscalateTo: support.EscalateTaxSpecialist}, agent, opened.Add(time.Hour))
	if c.Status != support.StatusInReview || c.EscalatedTo != support.EscalateTaxSpecialist {
		t.Fatalf("escalated: status %s to %s", c.Status, c.EscalatedTo)
	}
}

func TestAClosedCaseTakesOnlyNotesAndReopening(t *testing.T) {
	c := resolved(t, triaged(t, support.TypeCoverageEntitlement))
	c = act(t, c, support.Act{Action: support.ActionClose}, agent, opened.Add(time.Hour))
	got := c.Actionable()
	if len(got) != 2 || got[0] != support.ActionReopen || got[1] != support.ActionNote {
		t.Fatalf("actionable on a closed case: %v", got)
	}
	refused(t, c, support.Act{Action: support.ActionAssign, Assignee: agent}, agent, errs.ReasonStateTransitionInvalid)
}

func TestValidate(t *testing.T) {
	base := open(t, support.TypeFiling, support.SeverityS3)
	for name, mutate := range map[string]func(*support.Case){
		"type":     func(c *support.Case) { c.Type = "BILLING" },
		"severity": func(c *support.Case) { c.Severity = "S5" },
		"subject":  func(c *support.Case) { c.Subject = " " },
		"period":   func(c *support.Case) { c.Period = "2026-13" },
		"evidence": func(c *support.Case) { c.Evidence = []string{""} },
	} {
		c := base
		mutate(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
