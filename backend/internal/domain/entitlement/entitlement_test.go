package entitlement_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/entitlement"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/market"
)

var (
	maker   = id.NewUserID(uuid.MustParse("01920000-0000-7000-8000-0000000000a1"))
	checker = id.NewUserID(uuid.MustParse("01920000-0000-7000-8000-0000000000a2"))
	third   = id.NewUserID(uuid.MustParse("01920000-0000-7000-8000-0000000000a3"))
	entity  = id.NewLegalEntityID(uuid.MustParse("01920000-0000-7000-8000-00000000e001"))
	today   = time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
)

func grant(t *testing.T, op entitlement.Operation, mode entitlement.Mode, mutate ...func(*entitlement.Grant)) entitlement.Grant {
	t.Helper()
	g := entitlement.Grant{
		ID: id.NewEntitlementID(uuid.New()), LegalEntity: entity, Jurisdiction: "GB", Service: "MOBILE_VOICE", Mode: mode,
		Operation: op, EffectiveFrom: entitlement.Day(today), ContractRef: "MSA-1", CreatedBy: maker, CreatedAt: today,
	}
	for _, m := range mutate {
		m(&g)
	}
	events, err := entitlement.Open(g, true, maker, today)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	g, err = entitlement.Fold(g, events)
	if err != nil {
		t.Fatalf("fold: %v", err)
	}
	return g
}

func apply(t *testing.T, g entitlement.Grant, a entitlement.Action, by id.UserID, others ...entitlement.Grant) entitlement.Grant {
	t.Helper()
	e, err := g.Apply(entitlement.Act{Action: a, ExpectedVersion: g.Version(), Reason: "because"}, others, by, today)
	if err != nil {
		t.Fatalf("%s: %v", a, err)
	}
	g, err = entitlement.Fold(g, append(g.History, e))
	if err != nil {
		t.Fatalf("fold %s: %v", a, err)
	}
	return g
}

func reasonOf(err error) errs.ReasonCode { return errs.ReasonOf(err) }

func TestModesCarryOnlyTheirOwnOperations(t *testing.T) {
	g := entitlement.Grant{LegalEntity: entity, Jurisdiction: "GB", Service: "MOBILE_VOICE", Mode: entitlement.ModeShadowAssurance,
		Operation: entitlement.OpCommit, EffectiveFrom: today, ContractRef: "MSA-1"}
	if err := g.Validate(); err == nil {
		t.Fatal("a shadow-assurance grant carried COMMIT (COM-02)")
	}
	g.Mode, g.Operation = entitlement.ModeManagedCompliance, entitlement.OpQuote
	if err := g.Validate(); err == nil {
		t.Fatal("managed compliance carried QUOTE")
	}
	g.ContractRef, g.Mode, g.Operation = "", entitlement.ModeNative, entitlement.OpQuote
	if err := g.Validate(); err == nil {
		t.Fatal("a grant without a contract was accepted")
	}
}

func TestGrantIsNeverBackdated(t *testing.T) {
	g := entitlement.Grant{LegalEntity: entity, Jurisdiction: "GB", Service: "MOBILE_VOICE", Mode: entitlement.ModeNative,
		Operation: entitlement.OpQuote, EffectiveFrom: entitlement.Day(today).AddDate(0, 0, -1), ContractRef: "MSA-1"}
	if _, err := entitlement.Open(g, false, maker, today); err == nil {
		t.Fatal("a backdated grant was opened")
	}
}

func TestApprovalIsASecondAdministrators(t *testing.T) {
	g := grant(t, entitlement.OpQuote, entitlement.ModeNative)
	if g.StatusOn(entitlement.Day(today)) != entitlement.StatusInReview {
		t.Fatalf("status %s", g.StatusOn(entitlement.Day(today)))
	}
	_, err := g.Apply(entitlement.Act{Action: entitlement.ActApprove, ExpectedVersion: g.Version(), Reason: "ok"}, nil, maker, today)
	if reasonOf(err) != entitlement.ReasonMakerChecker {
		t.Fatalf("self-approval: %v", err)
	}
	_, err = g.Apply(entitlement.Act{Action: entitlement.ActApprove, ExpectedVersion: 1, Reason: "ok"}, nil, checker, today)
	if reasonOf(err) != entitlement.ReasonVersionConflict {
		t.Fatalf("stale version: %v", err)
	}
	_, err = g.Apply(entitlement.Act{Action: entitlement.ActApprove, ExpectedVersion: g.Version()}, nil, checker, today)
	if err == nil {
		t.Fatal("an approval without a reason was accepted")
	}
	g = apply(t, g, entitlement.ActApprove, checker)
	if s := g.StatusOn(entitlement.Day(today)); s != entitlement.StatusActive {
		t.Fatalf("approved grant reads %s", s)
	}
}

func TestOverlappingApprovalIsRefused(t *testing.T) {
	first := apply(t, grant(t, entitlement.OpCommit, entitlement.ModeNative), entitlement.ActApprove, checker)
	second := grant(t, entitlement.OpCommit, entitlement.ModeNative)
	_, err := second.Apply(entitlement.Act{Action: entitlement.ActApprove, ExpectedVersion: second.Version(), Reason: "ok"},
		[]entitlement.Grant{first}, checker, today)
	if reasonOf(err) != entitlement.ReasonOverlap {
		t.Fatalf("overlap: %v", err)
	}
	// A different operation is a different scope (T18-06 needs the exact scope).
	other := grant(t, entitlement.OpQuote, entitlement.ModeNative)
	apply(t, other, entitlement.ActApprove, checker, first)
}

func TestSuspensionTrumpsActivationAndResumeIsIndependent(t *testing.T) {
	g := apply(t, grant(t, entitlement.OpQuote, entitlement.ModeNative), entitlement.ActApprove, checker)
	g = apply(t, g, entitlement.ActSuspend, checker)
	if s := g.StatusOn(entitlement.Day(today)); s != entitlement.StatusSuspended {
		t.Fatalf("suspended reads %s", s)
	}
	if _, err := g.Apply(entitlement.Act{Action: entitlement.ActResume, ExpectedVersion: g.Version(), Reason: "x"}, nil, third, today); err == nil {
		t.Fatal("resumed without a request")
	}
	g = apply(t, g, entitlement.ActRequestResume, maker)
	_, err := g.Apply(entitlement.Act{Action: entitlement.ActResume, ExpectedVersion: g.Version(), Reason: "x"}, nil, maker, today)
	if reasonOf(err) != entitlement.ReasonMakerChecker {
		t.Fatalf("requester resumed: %v", err)
	}
	_, err = g.Apply(entitlement.Act{Action: entitlement.ActResume, ExpectedVersion: g.Version(), Reason: "x"}, nil, checker, today)
	if reasonOf(err) != entitlement.ReasonMakerChecker {
		t.Fatalf("suspender resumed: %v", err)
	}
	g = apply(t, g, entitlement.ActResume, third)
	if s := g.StatusOn(entitlement.Day(today)); s != entitlement.StatusActive {
		t.Fatalf("resumed reads %s", s)
	}
}

func TestDatesScheduleAndExpire(t *testing.T) {
	g := grant(t, entitlement.OpQuote, entitlement.ModeNative, func(g *entitlement.Grant) {
		g.EffectiveFrom = entitlement.Day(today).AddDate(0, 0, 10)
		g.EffectiveTo = entitlement.Day(today).AddDate(0, 0, 20)
	})
	g = apply(t, g, entitlement.ActApprove, checker)
	day := entitlement.Day(today)
	if s := g.StatusOn(day); s != entitlement.StatusScheduled {
		t.Fatalf("before start: %s", s)
	}
	if s := g.StatusOn(day.AddDate(0, 0, 15)); s != entitlement.StatusActive {
		t.Fatalf("within: %s", s)
	}
	if s := g.StatusOn(day.AddDate(0, 0, 21)); s != entitlement.StatusExpired {
		t.Fatalf("after: %s", s)
	}
}

func markets(services ...string) map[string]market.Market {
	return map[string]market.Market{
		"GB": {Country: "GB", Status: market.StatusPilot, Reason: market.ReasonFilingNotReady, Services: services},
		"FR": {Country: "FR", Status: market.StatusResearchOnly, Reason: market.ReasonNoContent, Research: market.ResearchOnly},
	}
}

func hasReason(fs []entitlement.Finding, r entitlement.Reason) bool {
	for _, f := range fs {
		if f.Reason == r {
			return true
		}
	}
	return false
}

func TestActiveGrantOverVerifiedCoverageIsAllowed(t *testing.T) {
	g := apply(t, grant(t, entitlement.OpQuote, entitlement.ModeNative), entitlement.ActApprove, checker)
	ev := entitlement.Evaluate(g, entitlement.Day(today), entitlement.Facts{Markets: markets("MOBILE_VOICE")})
	if ev.Decision != entitlement.DecisionAllow || ev.Effective != entitlement.EffectiveActive || len(ev.Findings) != 0 || ev.Mismatch {
		t.Fatalf("%+v", ev)
	}
}

func TestResearchProfileNeverBecomesLive(t *testing.T) {
	// T18-02 / COV-01: an approved grant over a research profile is
	// RESTRICTED with CONTENT_NOT_ACTIVE, never ALLOW.
	g := apply(t, grant(t, entitlement.OpQuote, entitlement.ModeNative, func(g *entitlement.Grant) { g.Jurisdiction = "FR" }),
		entitlement.ActApprove, checker)
	ev := entitlement.Evaluate(g, entitlement.Day(today), entitlement.Facts{Markets: markets("MOBILE_VOICE"), Authoritative: true})
	if ev.Decision != entitlement.DecisionDeny || ev.Effective != entitlement.EffectiveRestricted || !ev.Mismatch ||
		!hasReason(ev.Findings, entitlement.ReasonContentNotActive) {
		t.Fatalf("%+v", ev)
	}
	// A country nobody names is not covered either.
	g.Jurisdiction = "ZZ"
	ev = entitlement.Evaluate(g, entitlement.Day(today), entitlement.Facts{Markets: markets("MOBILE_VOICE")})
	if ev.Coverage.MarketStatus != entitlement.MarketNotListed || ev.Decision != entitlement.DecisionDeny {
		t.Fatalf("%+v", ev.Coverage)
	}
}

func TestFilingDeniedWhileCalculationIsCovered(t *testing.T) {
	// T18-15 / COV-06: filing unavailable though calculation is covered.
	facts := entitlement.Facts{Markets: markets("MOBILE_VOICE"), Authoritative: true,
		Filing: func(id.LegalEntityID, string) entitlement.FilingGate {
			return entitlement.FilingGate{Authority: "HMRC VAT", Reason: "AUTHORIZATION_MISSING"}
		}}
	quote := apply(t, grant(t, entitlement.OpQuote, entitlement.ModeNative), entitlement.ActApprove, checker)
	file := apply(t, grant(t, entitlement.OpFile, entitlement.ModeNative), entitlement.ActApprove, checker)
	if ev := entitlement.Evaluate(quote, entitlement.Day(today), facts); ev.Decision != entitlement.DecisionAllow {
		t.Fatalf("quote: %+v", ev)
	}
	ev := entitlement.Evaluate(file, entitlement.Day(today), facts)
	if ev.Decision != entitlement.DecisionDeny || !hasReason(ev.Findings, entitlement.ReasonOperationUnsupported) ||
		ev.Coverage.Filing == nil || ev.Coverage.Filing.Ready {
		t.Fatalf("file: %+v", ev)
	}
	facts.Filing = func(id.LegalEntityID, string) entitlement.FilingGate {
		return entitlement.FilingGate{Ready: true, Authority: "HMRC VAT"}
	}
	if ev := entitlement.Evaluate(file, entitlement.Day(today), facts); ev.Decision != entitlement.DecisionAllow {
		t.Fatalf("file with a ready gate: %+v", ev)
	}
}

func TestCommitNeedsAnAuthoritativeCellAndOtherGaps(t *testing.T) {
	commit := apply(t, grant(t, entitlement.OpCommit, entitlement.ModeNative), entitlement.ActApprove, checker)
	ev := entitlement.Evaluate(commit, entitlement.Day(today), entitlement.Facts{Markets: markets("MOBILE_VOICE")})
	if !hasReason(ev.Findings, entitlement.ReasonTechUnavailable) || ev.Decision != entitlement.DecisionDeny {
		t.Fatalf("advisory commit: %+v", ev)
	}
	// Service not covered by content in force.
	ev = entitlement.Evaluate(commit, entitlement.Day(today), entitlement.Facts{Markets: markets("MOBILE_DATA"), Authoritative: true})
	if !hasReason(ev.Findings, entitlement.ReasonContentNotActive) {
		t.Fatalf("service gap: %+v", ev)
	}
	// A subdivision is not proven by a country's content.
	sub := apply(t, grant(t, entitlement.OpQuote, entitlement.ModeNative, func(g *entitlement.Grant) { g.Jurisdiction = "GB-SCT" }),
		entitlement.ActApprove, checker)
	ev = entitlement.Evaluate(sub, entitlement.Day(today), entitlement.Facts{Markets: markets("MOBILE_VOICE")})
	if !hasReason(ev.Findings, entitlement.ReasonGeographyNotProven) {
		t.Fatalf("subdivision: %+v", ev)
	}
	// Federated coverage is never native coverage.
	fed := apply(t, grant(t, entitlement.OpQuote, entitlement.ModeFederated), entitlement.ActApprove, checker)
	ev = entitlement.Evaluate(fed, entitlement.Day(today), entitlement.Facts{Markets: markets("MOBILE_VOICE")})
	if !hasReason(ev.Findings, entitlement.ReasonProviderUnproven) {
		t.Fatalf("federated: %+v", ev)
	}
}

func TestPendingGrantHasNoEffect(t *testing.T) {
	// T18-07: a partial approval has no live capability effect.
	g := grant(t, entitlement.OpQuote, entitlement.ModeNative)
	ev := entitlement.Evaluate(g, entitlement.Day(today), entitlement.Facts{Markets: markets("MOBILE_VOICE")})
	if ev.Decision != entitlement.DecisionDeny || ev.Effective != entitlement.EffectivePending ||
		ev.Findings[0].Reason != entitlement.ReasonAwaitingApproval || ev.Mismatch {
		t.Fatalf("%+v", ev)
	}
}

func TestFoldRefusesAnIllegalHistory(t *testing.T) {
	g := grant(t, entitlement.OpQuote, entitlement.ModeNative)
	bad := append(g.History, entitlement.Event{Seq: 3, Kind: entitlement.EventResumed, Lifecycle: entitlement.LifecycleApproved})
	if _, err := entitlement.Fold(g, bad); err == nil {
		t.Fatal("a resume from IN_REVIEW folded")
	}
}
