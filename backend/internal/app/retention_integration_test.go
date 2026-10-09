//go:build integration

package app_test

import (
	"testing"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/retention"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
)

// Retention and legal hold against PostgreSQL.

func (c *fiscalCell) retention() *app.RetentionService {
	return app.NewRetentionService(c.store.Retention(), c.store.Decisions(), c.store.LegalEntities(), c.store.Audit(),
		c.store, c.clock, idgen.V7{})
}

// verdict asks as the cell's own work would: a disposition job.
func (c *fiscalCell) verdict(t *testing.T, svc *app.RetentionService, d id.DecisionID) retention.Verdict {
	t.Helper()
	_, v, _, err := svc.Verdict(security.Into(c.ctx, security.System(c.tenant)), d)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// ZTAX-EVID-REQ-0022, -0023, -0026, -0052, -0053, -0054, -0092, -0105,
// -0115, -0126; ZTAX-PRIV-REQ-0050, -0051, -0052; ZTAX-LEG-REQ-0091.
func TestIntegrationEVIDREQ0023RetentionAndLegalHold(t *testing.T) {
	c := openFiscalCell(t)
	svc := c.retention()
	held := c.mustCommit(t, "k-r1", "INV-R1/1", "100.00", "3", nil)
	other := c.mustCommit(t, "k-r2", "INV-R2/1", "100.00", "3", nil)
	before, err := c.store.Decisions().ByID(c.ctx, held)
	if err != nil {
		t.Fatal(err)
	}
	admin, auditor := c.as(security.RoleAdmin), c.as(security.RoleAuditor)
	sys := security.Into(c.ctx, security.System(c.tenant))

	// Who may do what: the operator nothing; system work reads a verdict
	// and writes no policy.
	if _, err := svc.Policies(c.ctx); errs.ReasonOf(err) != errs.ReasonForbidden {
		t.Fatalf("an operator read policies: %v", err)
	}
	if _, err := svc.RecordPolicy(sys, app.PolicyInput{}); errs.ReasonOf(err) != errs.ReasonUnauthenticated {
		t.Fatalf("system work recorded a policy: %v", err)
	}

	// No country, no policy: kept, never guessed (ZTAX-EVID-REQ-0022, -0092).
	if v := c.verdict(t, svc, held); v.Outcome != retention.OutcomeNoPolicy {
		t.Fatalf("with no country recorded: %s", v.Outcome)
	}
	owner := ownerPool(t)
	if _, err := owner.Exec(c.ctx, `UPDATE ztax.legal_entity SET country_code = 'DE' WHERE tenant_id = $1`, c.tenant.UUID()); err != nil {
		t.Fatal(err)
	}
	if v := c.verdict(t, svc, held); v.Outcome != retention.OutcomeNoPolicy {
		t.Fatalf("with no DE policy: %s", v.Outcome)
	}

	// A policy, then its next version; both stay (ZTAX-EVID-REQ-0052).
	in := app.PolicyInput{ID: "de-decisions", Class: retention.ClassDecision, Country: "DE", Years: 10,
		Trigger: retention.TriggerYearEnd, EffectiveFrom: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), Citation: "AO §147(3)"}
	if _, err := svc.RecordPolicy(admin, app.PolicyInput{ID: "x", Class: retention.ClassDecision, Country: "DE", Years: 10,
		Trigger: retention.TriggerYearEnd, EffectiveFrom: in.EffectiveFrom}); errs.ReasonOf(err) != errs.ReasonInvalidValue {
		t.Fatalf("a policy citing nothing: %v", err)
	}
	if p, err := svc.RecordPolicy(admin, in); err != nil || p.Version != 1 {
		t.Fatalf("version 1: %v %+v", err, p)
	}
	if v := c.verdict(t, svc, held); v.Outcome != retention.OutcomeRetain ||
		!v.RetainUntil.Equal(time.Date(2037, 1, 1, 0, 0, 0, 0, time.UTC)) || v.Policy.Version != 1 {
		t.Fatalf("inside the period: %+v", v)
	}
	in.Years, in.EffectiveFrom = 8, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if p, err := svc.RecordPolicy(admin, in); err != nil || p.Version != 2 {
		t.Fatalf("version 2: %v %+v", err, p)
	}
	policies, err := svc.Policies(auditor)
	if err != nil || len(policies) != 2 || policies[0].Years != 10 || policies[1].Years != 8 {
		t.Fatalf("the versions: %v %+v", err, policies)
	}

	// Past the period it is eligible, under the version that governs then
	// (ZTAX-EVID-REQ-0126).
	c.clock.Set(time.Date(2035, 6, 1, 0, 0, 0, 0, time.UTC))
	if v := c.verdict(t, svc, held); v.Outcome != retention.OutcomeEligible || v.Policy.Version != 2 {
		t.Fatalf("past the period: %+v", v)
	}

	// A hold is specific (ZTAX-PRIV-REQ-0051), names its matter
	// (ZTAX-LEG-REQ-0091), and is placed by a person.
	if _, err := svc.PlaceHold(admin, "Claim 1", "notice", retention.Scope{LegalEntity: id.LegalEntityID{}}); errs.ReasonOf(err) != errs.ReasonInvalidValue {
		t.Fatalf("a hold with no scope: %v", err)
	}
	if _, err := svc.PlaceHold(admin, " ", "notice", retention.Scope{BusinessKeys: []string{"INV-R1/1"}}); errs.ReasonOf(err) != errs.ReasonMissingField {
		t.Fatalf("a hold with no matter: %v", err)
	}
	if _, err := svc.PlaceHold(auditor, "Claim 1", "notice", retention.Scope{BusinessKeys: []string{"INV-R1/1"}}); errs.ReasonOf(err) != errs.ReasonForbidden {
		t.Fatalf("an auditor placed a hold: %v", err)
	}
	h, err := svc.PlaceHold(admin, "Claim 1", "litigation notice", retention.Scope{BusinessKeys: []string{"INV-R1/1"}})
	if err != nil || !h.Active() {
		t.Fatalf("placing: %v %+v", err, h)
	}

	// It blocks disposition of what it scopes, and only that
	// (ZTAX-EVID-REQ-0023, ZTAX-PRIV-REQ-0050).
	if v := c.verdict(t, svc, held); v.Outcome != retention.OutcomeHeld || len(v.Holds) != 1 || v.Holds[0] != h.ID {
		t.Fatalf("held: %+v", v)
	}
	if v := c.verdict(t, svc, other); v.Outcome != retention.OutcomeEligible {
		t.Fatalf("the unscoped decision: %s", v.Outcome)
	}

	// A scope change is history (ZTAX-EVID-REQ-0053).
	h, err = svc.ChangeHoldScope(admin, h.ID, "second line named", retention.Scope{Decisions: []id.DecisionID{held, other}})
	if err != nil || len(h.History) != 2 || h.History[0].Scope.BusinessKeys[0] != "INV-R1/1" {
		t.Fatalf("a scope change: %v %+v", err, h)
	}
	if v := c.verdict(t, svc, other); v.Outcome != retention.OutcomeHeld {
		t.Fatalf("the widened scope: %s", v.Outcome)
	}

	// Release is final, and retention decides again (ZTAX-EVID-REQ-0054).
	if h, err = svc.ReleaseHold(admin, h.ID, "claim settled"); err != nil || h.Active() || len(h.History) != 3 {
		t.Fatalf("a release: %v %+v", err, h)
	}
	if _, err := svc.ReleaseHold(admin, h.ID, "again"); errs.ReasonOf(err) != errs.ReasonStateTransitionInvalid {
		t.Fatalf("a second release: %v", err)
	}
	if v := c.verdict(t, svc, held); v.Outcome != retention.OutcomeEligible {
		t.Fatalf("after the release: %s", v.Outcome)
	}

	// Two policies that disagree: kept, both named (ZTAX-EVID-REQ-0092).
	if _, err := svc.RecordPolicy(admin, app.PolicyInput{ID: "de-commercial", Class: retention.ClassDecision, Country: "DE",
		Years: 6, Trigger: retention.TriggerEventTime, EffectiveFrom: in.EffectiveFrom, Citation: "HGB §257"}); err != nil {
		t.Fatal(err)
	}
	if v := c.verdict(t, svc, held); v.Outcome != retention.OutcomeConflicted || len(v.Candidates) != 2 {
		t.Fatalf("disagreeing policies: %+v", v)
	}

	// Hold reads are privileged access, audited (ZTAX-EVID-REQ-0115).
	if _, err := svc.Holds(c.ctx); errs.ReasonOf(err) != errs.ReasonForbidden {
		t.Fatalf("an operator searched holds: %v", err)
	}
	if hs, err := svc.Holds(auditor); err != nil || len(hs) != 1 {
		t.Fatalf("the auditor's search: %v %d", err, len(hs))
	}
	if _, err := svc.Hold(auditor, h.ID); err != nil {
		t.Fatal(err)
	}
	trail, err := c.store.Audit().ListForSubject(c.ctx, "legal_hold", "*", 10)
	if err != nil || len(trail) != 1 || trail[0].Action != "LEGAL_HOLD_SEARCHED" {
		t.Fatalf("the search's audit: %v %+v", err, trail)
	}
	trail, err = c.store.Audit().ListForSubject(c.ctx, "legal_hold", h.ID.String(), 10)
	actions := map[string]bool{}
	for _, r := range trail {
		actions[r.Action] = true
	}
	// The clock is fixed, so the four share an instant: compare as a set.
	if err != nil || len(trail) != 4 || !actions["LEGAL_HOLD_PLACED"] || !actions["LEGAL_HOLD_SCOPE_CHANGED"] ||
		!actions["LEGAL_HOLD_RELEASED"] || !actions["LEGAL_HOLD_READ"] {
		t.Fatalf("the hold's audit trail: %v %+v", err, trail)
	}

	// None of it touched the decision (ZTAX-EVID-REQ-0105).
	after, err := c.store.Decisions().ByID(c.ctx, held)
	if err != nil || !after.EnvelopeDigest.Equal(before.EnvelopeDigest) || !after.ResultDigest.Equal(before.ResultDigest) {
		t.Fatalf("the decision changed: %v", err)
	}
}
