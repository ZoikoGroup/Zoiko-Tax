package obligation_test

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/accumulator"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/obligation"
	"github.com/zoikogroup/zoikotax/backend/internal/fiscaltest"
)

// Lane I's obligation domain against ZTAX-OBL-001's register. Test names carry
// the requirement id each verifies, so docs/requirements.yaml can point at it.

var (
	tenant  = id.NewTenantID(uuid.MustParse("00000000-0000-7000-8000-000000000001"))
	entity  = id.NewLegalEntityID(uuid.MustParse("00000000-0000-7000-8000-000000000002"))
	other   = id.NewLegalEntityID(uuid.MustParse("00000000-0000-7000-8000-000000000003"))
	user    = id.NewUserID(uuid.MustParse("00000000-0000-7000-8000-000000000004"))
	content = obligation.ContentRef{BundleID: "eu-vat-worked-2026.09", BundleDigest: "zt1:00"}
	when    = time.Date(2027, 3, 15, 12, 0, 0, 0, time.UTC)
)

func rule(roles ...obligation.RoleRule) obligation.ResponsibilityRule {
	return obligation.ResponsibilityRule{ID: "resp-1", Version: "1", Provenance: content, Roles: roles}
}

func every(kinds ...obligation.PartyKind) []obligation.RoleRule {
	var out []obligation.RoleRule
	for _, r := range obligation.Roles {
		out = append(out, obligation.RoleRule{Role: r, Candidates: kinds})
	}
	return out
}

func decide(t *testing.T, r obligation.ResponsibilityRule, parties []obligation.Party, delegations ...obligation.Delegation) obligation.ResponsibilityDecision {
	t.Helper()
	d, err := obligation.DecideResponsibility(r, obligation.ResponsibilityInput{
		ID:       id.NewResponsibilityDecisionID(uuid.MustParse("00000000-0000-7000-8000-0000000000aa")),
		TenantID: tenant, LegalEntity: entity, EffectiveFrom: when, Parties: parties, Delegations: delegations,
	})
	if err != nil {
		t.Fatalf("DecideResponsibility: %v", err)
	}
	return d
}

var (
	seller      = obligation.Party{Kind: obligation.PartySeller, LegalEntity: entity}
	buyer       = obligation.Party{Kind: obligation.PartyBuyer, Ref: "customer-42"}
	marketplace = obligation.Party{Kind: obligation.PartyMarketplace, Ref: "market-1"}
	reseller    = obligation.Party{Kind: obligation.PartyReseller, Ref: "reseller-9"}
	endUser     = obligation.Party{Kind: obligation.PartyEndUser, Ref: "subscriber-7"}
)

// ---------------------------------------------------------------------------
// responsibility
// ---------------------------------------------------------------------------

func TestOBLREQ0002To0006EachRoleIsResolvedIndependently(t *testing.T) {
	// Five roles, five different answers: the seller bears incidence, the
	// marketplace collects and remits, the reseller reports, the end user
	// bears the cost.
	r := rule(
		obligation.RoleRule{Role: obligation.RoleIncidence, Candidates: []obligation.PartyKind{obligation.PartySeller}},
		obligation.RoleRule{Role: obligation.RoleCollection, Candidates: []obligation.PartyKind{obligation.PartyMarketplace, obligation.PartySeller}},
		obligation.RoleRule{Role: obligation.RoleReporting, Candidates: []obligation.PartyKind{obligation.PartyReseller}},
		obligation.RoleRule{Role: obligation.RoleRemittance, Candidates: []obligation.PartyKind{obligation.PartyMarketplace}},
		obligation.RoleRule{Role: obligation.RoleEconomicBearer, Candidates: []obligation.PartyKind{obligation.PartyEndUser}},
	)
	d := decide(t, r, []obligation.Party{seller, marketplace, reseller, endUser})
	want := map[obligation.Role]obligation.PartyKind{
		obligation.RoleIncidence: obligation.PartySeller, obligation.RoleCollection: obligation.PartyMarketplace,
		obligation.RoleReporting: obligation.PartyReseller, obligation.RoleRemittance: obligation.PartyMarketplace,
		obligation.RoleEconomicBearer: obligation.PartyEndUser,
	}
	if d.Status != obligation.ResponsibilityResolved {
		t.Fatalf("status %s", d.Status)
	}
	for role, kind := range want {
		a, _ := d.Assignment(role)
		if a.Statutory == nil || a.Statutory.Kind != kind {
			t.Errorf("%s held by %+v, want %s", role, a.Statutory, kind)
		}
	}
}

func TestOBLREQ0007OnePartyHoldingSeveralRolesIsRecordedPerRole(t *testing.T) {
	d := decide(t, rule(every(obligation.PartySeller)...), []obligation.Party{seller})
	if len(d.Assignments) != len(obligation.Roles) {
		t.Fatalf("%d assignments for %d roles", len(d.Assignments), len(obligation.Roles))
	}
	for _, a := range d.Assignments {
		if a.Statutory == nil || a.Statutory.LegalEntity != entity {
			t.Errorf("%s: %+v", a.Role, a.Statutory)
		}
	}
}

func TestOBLREQ0008ConfigurationCannotDelegateWhatTheLawDoesNot(t *testing.T) {
	r := rule(every(obligation.PartySeller)...)
	_, err := obligation.DecideResponsibility(r, obligation.ResponsibilityInput{
		TenantID: tenant, LegalEntity: entity, EffectiveFrom: when, Parties: []obligation.Party{seller},
		Delegations: []obligation.Delegation{{Role: obligation.RoleRemittance, Agent: obligation.Party{Kind: obligation.PartyMarketplace, Ref: "agent"}}},
	})
	if err == nil {
		t.Fatal("tenant configuration delegated remittance under a rule that does not permit it")
	}
}

func TestOBLREQ0009AnAgentNeverBecomesTheStatutoryParty(t *testing.T) {
	roles := every(obligation.PartySeller)
	for i := range roles {
		if roles[i].Role == obligation.RoleRemittance {
			roles[i].DelegationPermitted = true
		}
	}
	agent := obligation.Party{Kind: obligation.PartyMarketplace, Ref: "filing-agent"}
	d := decide(t, rule(roles...), []obligation.Party{seller}, obligation.Delegation{Role: obligation.RoleRemittance, Agent: agent})
	a, _ := d.Assignment(obligation.RoleRemittance)
	if a.Statutory == nil || a.Statutory.LegalEntity != entity || a.Agent == nil || a.Agent.Ref != "filing-agent" {
		t.Fatalf("remittance %+v", a)
	}
	if _, err := obligation.DecideResponsibility(rule(obligation.RoleRule{
		Role: obligation.RoleIncidence, Candidates: []obligation.PartyKind{obligation.PartySeller}, DelegationPermitted: true,
	}), obligation.ResponsibilityInput{TenantID: tenant, LegalEntity: entity, EffectiveFrom: when}); err == nil {
		t.Fatal("a rule delegating incidence was accepted")
	}
}

func TestOBLREQ0010To0012DecisionIsScopedEffectiveDatedAndPinned(t *testing.T) {
	d := decide(t, rule(every(obligation.PartySeller)...), []obligation.Party{seller})
	if d.TenantID != tenant || d.LegalEntity != entity || !d.EffectiveFrom.Equal(when) || d.Provenance != content || d.RuleVersion != "1" {
		t.Fatalf("decision %+v", d)
	}
	if _, err := obligation.DecideResponsibility(rule(every(obligation.PartySeller)...),
		obligation.ResponsibilityInput{TenantID: tenant, EffectiveFrom: when}); err == nil {
		t.Fatal("a decision with no legal entity was made")
	}
	if _, err := obligation.DecideResponsibility(rule(every(obligation.PartySeller)...),
		obligation.ResponsibilityInput{TenantID: tenant, LegalEntity: entity}); err == nil {
		t.Fatal("a decision with no effective date was made")
	}
}

func TestOBLREQ0013To0016DecisionExposesEveryStatus(t *testing.T) {
	resolved := decide(t, rule(every(obligation.PartySeller)...), []obligation.Party{seller})
	ambiguous := decide(t, rule(every(obligation.PartyMarketplace)...), []obligation.Party{seller})
	conflicted := decide(t, rule(every(obligation.PartyBuyer)...), []obligation.Party{buyer, {Kind: obligation.PartyBuyer, Ref: "customer-43"}})
	unsupported := decide(t, rule(obligation.RoleRule{Role: obligation.RoleIncidence, Candidates: []obligation.PartyKind{obligation.PartySeller}}), []obligation.Party{seller})
	for got, want := range map[obligation.ResponsibilityStatus]obligation.ResponsibilityStatus{
		resolved.Status: obligation.ResponsibilityResolved, ambiguous.Status: obligation.ResponsibilityAmbiguous,
		conflicted.Status: obligation.ResponsibilityConflicted, unsupported.Status: obligation.ResponsibilityUnsupported,
	} {
		if got != want {
			t.Errorf("status %s, want %s", got, want)
		}
	}
}

func TestOBLREQ0017UnresolvedResponsibilityNeverDefaults(t *testing.T) {
	// The seller is present; the rule asks for a marketplace. The role is
	// AMBIGUOUS and names nobody — it does not fall back to the seller.
	d := decide(t, rule(every(obligation.PartyMarketplace)...), []obligation.Party{seller, buyer})
	for _, a := range d.Assignments {
		if a.Statutory != nil {
			t.Fatalf("%s defaulted to %+v", a.Role, a.Statutory)
		}
	}
}

func TestOBLREQ0019AffiliatesAreNotOneFilerByDefault(t *testing.T) {
	// Two affiliated legal entities as sellers on one transaction conflict;
	// nothing merges them into one statutory party.
	d := decide(t, rule(every(obligation.PartySeller)...), []obligation.Party{seller, {Kind: obligation.PartySeller, LegalEntity: other}})
	if d.Status != obligation.ResponsibilityConflicted {
		t.Fatalf("status %s", d.Status)
	}
}

// ---------------------------------------------------------------------------
// definitions and triggers
// ---------------------------------------------------------------------------

func definition() obligation.Definition {
	return obligation.Definition{
		ID: "vat-return", Version: "2026.1", Authority: "authority:xa-revenue", Jurisdiction: "jurisdiction:xa",
		Duty: obligation.DutyTransactionMonetary, Trigger: "sold", ResponsibilityRule: "resp-1",
		Basis:      obligation.Basis{Kind: "TAX_COLLECTED", Unit: "EUR", Classification: obligation.ClassifiedByTaxability},
		Period:     obligation.PeriodRule{Kind: obligation.PeriodMonth, Timezone: "UTC"},
		Due:        obligation.DueDateRule{Anchor: obligation.AnchorPeriodEnd, OffsetMonths: 1, DayOfMonth: 20, Adjustment: obligation.AdjustNone},
		Amendment:  []obligation.AmendmentTreatment{obligation.AmendCurrentPeriod, obligation.AmendPriorPeriod},
		Provenance: obligation.Provenance{Content: content, Source: "authority instrument", Citation: "s.1"},
	}
}

func TestOBLREQ0022To0030DefinitionIdentifiesEverythingItMust(t *testing.T) {
	if err := definition().Validate(); err != nil {
		t.Fatalf("a complete definition was refused: %v", err)
	}
	for name, mutate := range map[string]func(*obligation.Definition){
		"authority":      func(d *obligation.Definition) { d.Authority = "" },
		"jurisdiction":   func(d *obligation.Definition) { d.Jurisdiction = "" },
		"trigger":        func(d *obligation.Definition) { d.Trigger = "" },
		"responsibility": func(d *obligation.Definition) { d.ResponsibilityRule = "" },
		"basis unit":     func(d *obligation.Definition) { d.Basis.Unit = "" },
		"period":         func(d *obligation.Definition) { d.Period.Timezone = "" },
		"due date":       func(d *obligation.Definition) { d.Due.Adjustment = "" },
		"threshold": func(d *obligation.Definition) {
			d.Threshold = &obligation.Threshold{ID: "t", Kind: obligation.ThresholdCap}
		},
		"amendment":  func(d *obligation.Definition) { d.Amendment = []obligation.AmendmentTreatment{"WHENEVER"} },
		"provenance": func(d *obligation.Definition) { d.Provenance.Citation = "" },
	} {
		d := definition()
		mutate(&d)
		if err := d.Validate(); err == nil {
			t.Errorf("a definition with no %s was accepted", name)
		}
	}
}

func TestOBLREQ0031To0036DefinitionSupportsEveryDutyKind(t *testing.T) {
	for _, duty := range []obligation.DutyKind{
		obligation.DutyRegistration, obligation.DutyInformationReturn, obligation.DutyRecordkeeping, obligation.DutyNoticeResponse,
	} {
		d := definition()
		d.Duty, d.Basis = duty, obligation.Basis{Classification: obligation.ClassifiedByNone}
		if err := d.Validate(); err != nil {
			t.Errorf("%s refused: %v", duty, err)
		}
		if duty.Monetary() {
			t.Errorf("%s reports itself monetary", duty)
		}
	}
	c := definition()
	c.Duty = obligation.DutyPeriodicContribution
	c.Basis.Classification = obligation.ClassifiedByRegulatoryRevenue
	if err := c.Validate(); err != nil {
		t.Errorf("periodic contribution refused: %v", err)
	}
}

func TestOBLREQ0104ContributionBasisIsRegulatoryRevenueNotTaxability(t *testing.T) {
	d := definition()
	d.Duty = obligation.DutyPeriodicContribution
	d.Basis.Classification = obligation.ClassifiedByTaxability
	if err := d.Validate(); err == nil || !strings.Contains(err.Error(), "regulatory-revenue") {
		t.Fatalf("a contribution measured on taxability was accepted: %v", err)
	}
}

func TestOBLREQ0050TriggerKindsAreClosedAndAllSupported(t *testing.T) {
	set := map[string]obligation.Trigger{}
	for i, k := range []obligation.TriggerKind{
		obligation.TriggerEvent, obligation.TriggerActivity, obligation.TriggerThreshold, obligation.TriggerPeriod,
		obligation.TriggerRegistration, obligation.TriggerAuthorityDemand, obligation.TriggerDependency, obligation.TriggerChange,
	} {
		set[string(rune('a'+i))] = obligation.Trigger{Kind: k, Fact: "f"}
	}
	if err := obligation.ValidateTriggers(set); err != nil {
		t.Fatal(err)
	}
	if err := obligation.ValidateTriggers(map[string]obligation.Trigger{"x": {Kind: "HUNCH", Fact: "f"}}); err == nil {
		t.Fatal("an invented trigger kind was accepted")
	}
}

func TestOBLREQ0051CompositeTriggersAreCycleSafe(t *testing.T) {
	set := map[string]obligation.Trigger{
		"a": {Kind: obligation.TriggerAll, Children: []obligation.Trigger{{Kind: obligation.TriggerRef, Ref: "b"}, {Kind: obligation.TriggerEvent, Fact: "sale"}}},
		"b": {Kind: obligation.TriggerAny, Children: []obligation.Trigger{{Kind: obligation.TriggerRef, Ref: "a"}}},
	}
	if err := obligation.ValidateTriggers(set); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("a reference cycle was accepted: %v", err)
	}
}

func TestOBLREQ0052UnknownFactIsNotFalse(t *testing.T) {
	trig := obligation.Trigger{Kind: obligation.TriggerAll, Children: []obligation.Trigger{
		{Kind: obligation.TriggerEvent, Fact: "sale"}, {Kind: obligation.TriggerActivity, Fact: "resident"},
	}}
	if got := trig.Evaluate(obligation.Facts{"sale": true}, nil); got != obligation.Unknown {
		t.Fatalf("a missing required fact evaluated %s", got)
	}
	if got := trig.Evaluate(obligation.Facts{"sale": true, "resident": false}, nil); got != obligation.False {
		t.Fatalf("a known false fact evaluated %s", got)
	}
}

func TestOBLREQ0099And0101DependenciesAreAcyclicAndCheckState(t *testing.T) {
	reg := definition()
	reg.ID, reg.Duty, reg.Basis = "registration", obligation.DutyRegistration, obligation.Basis{Classification: obligation.ClassifiedByNone}
	ret := definition()
	ret.Dependencies = []obligation.Dependency{{Kind: obligation.DependsActivatedBy, On: "registration"}}
	triggers := map[string]obligation.Trigger{"sold": {Kind: obligation.TriggerEvent, Fact: "sale"}}
	set, err := obligation.NewDefinitionSet([]obligation.Definition{reg, ret}, triggers)
	if err != nil {
		t.Fatal(err)
	}
	if ok, missing := set.PrerequisitesMet("vat-return", map[string]obligation.RegistrationState{"registration": obligation.RegistrationInProgress}, nil); ok || len(missing) != 1 {
		t.Fatalf("a return was ready before its registration: %v", missing)
	}
	if ok, _ := set.PrerequisitesMet("vat-return", map[string]obligation.RegistrationState{"registration": obligation.RegistrationRegistered}, nil); !ok {
		t.Fatal("a return was not ready once registered")
	}
	if got := set.Activated("registration"); len(got) != 1 || got[0].ID != "vat-return" {
		t.Fatalf("registration seeds %v", got)
	}

	loop := definition()
	loop.ID, loop.Dependencies = "loop", []obligation.Dependency{{Kind: obligation.DependsRequiresFiled, On: "loop2"}}
	loop2 := definition()
	loop2.ID, loop2.Dependencies = "loop2", []obligation.Dependency{{Kind: obligation.DependsRequiresFiled, On: "loop"}}
	if _, err := obligation.NewDefinitionSet([]obligation.Definition{loop, loop2}, triggers); err == nil {
		t.Fatal("a dependency cycle was accepted")
	}
}

// ---------------------------------------------------------------------------
// thresholds and registration
// ---------------------------------------------------------------------------

func eur(t *testing.T, s string) fiscal.Money { return fiscaltest.Money(t, s, "EUR") }

func threshold(t *testing.T, kind obligation.ThresholdKind, cmp accumulator.Comparison, mode obligation.CrossingMode) obligation.Threshold {
	return obligation.Threshold{ID: "t1", Kind: kind, Limit: eur(t, "10000.00"), Comparison: cmp, Crossing: mode}
}

func TestOBLREQ0066ComparatorIsExplicit(t *testing.T) {
	at := eur(t, "9999.00")
	gte, err := threshold(t, obligation.ThresholdRegistration, accumulator.AtOrAbove, obligation.CrossProspective).Evaluate(&at, eur(t, "1.00"), false)
	if err != nil {
		t.Fatal(err)
	}
	gt, err := threshold(t, obligation.ThresholdRegistration, accumulator.Above, obligation.CrossProspective).Evaluate(&at, eur(t, "1.00"), false)
	if err != nil {
		t.Fatal(err)
	}
	// Exactly at the limit: "reaches" crosses, "exceeds" does not.
	if !gte.Crossed || gt.Crossed {
		t.Fatalf("GTE crossed %t, GT crossed %t", gte.Crossed, gt.Crossed)
	}
	if err := (obligation.Threshold{ID: "t", Kind: obligation.ThresholdRegistration, Limit: eur(t, "1"), Crossing: obligation.CrossSplit}).Validate(); err == nil {
		t.Fatal("a threshold with no comparator was accepted")
	}
}

func TestDETREQ0026CrossingModeIsDeclaredAndCapDefaultsToSplit(t *testing.T) {
	before := eur(t, "9900.00")
	split, err := threshold(t, obligation.ThresholdCap, accumulator.AtOrAbove, "").Evaluate(&before, eur(t, "250.00"), false)
	if err != nil {
		t.Fatal(err)
	}
	// 100.00 below the cap, 150.00 above it, recorded as two parts.
	if split.UnderPrior.String() != "100.00" || split.UnderNew.String() != "150.00" {
		t.Fatalf("CAP split %s / %s", split.UnderPrior, split.UnderNew)
	}
	if err := threshold(t, obligation.ThresholdRegistration, accumulator.AtOrAbove, "").Validate(); err == nil {
		t.Fatal("a registration threshold with no crossing mode was accepted")
	}
	inclusive, _ := threshold(t, obligation.ThresholdRegistration, accumulator.AtOrAbove, obligation.CrossInclusive).Evaluate(&before, eur(t, "250.00"), false)
	prospective, _ := threshold(t, obligation.ThresholdRegistration, accumulator.AtOrAbove, obligation.CrossProspective).Evaluate(&before, eur(t, "250.00"), false)
	if inclusive.UnderNew.String() != "250.00" || prospective.UnderPrior.String() != "250.00" {
		t.Fatalf("inclusive new %s, prospective prior %s", inclusive.UnderNew, prospective.UnderPrior)
	}
}

func TestOBLREQ0068CrossingRecordsBeforeAndAfter(t *testing.T) {
	before := eur(t, "9900.00")
	res, err := threshold(t, obligation.ThresholdRegistration, accumulator.AtOrAbove, obligation.CrossProspective).Evaluate(&before, eur(t, "250.00"), false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Crossed || res.Before.String() != "9900.00" || res.After.String() != "10150.00" {
		t.Fatalf("crossed %t before %v after %v", res.Crossed, res.Before, res.After)
	}
}

func TestOBLREQ0069FallingBelowDoesNotCancelADuty(t *testing.T) {
	before := eur(t, "10150.00")
	res, err := threshold(t, obligation.ThresholdRegistration, accumulator.AtOrAbove, obligation.CrossProspective).Evaluate(&before, eur(t, "-500.00"), true)
	if err != nil {
		t.Fatal(err)
	}
	if res.State != obligation.ThresholdReached || !res.Latched {
		t.Fatalf("a credit took a crossed threshold back to %s", res.State)
	}
}

func TestOBLREQ0071UnknownIsNeverBelow(t *testing.T) {
	res, err := threshold(t, obligation.ThresholdRegistration, accumulator.AtOrAbove, obligation.CrossProspective).Evaluate(nil, eur(t, "1.00"), false)
	if err != nil {
		t.Fatal(err)
	}
	if res.State != obligation.ThresholdUnknown {
		t.Fatalf("an unknown prior total gave %s", res.State)
	}
}

func TestOBLREQ0070DeMinimisIsAPackDefinedLegalState(t *testing.T) {
	th := threshold(t, obligation.ThresholdDeMinimis, accumulator.Above, obligation.CrossProspective)
	if err := th.Validate(); err == nil {
		t.Fatal("a de-minimis threshold naming no legal state was accepted")
	}
	th.DeMinimisState = "SMALL_SUPPLIER_EXEMPT"
	if err := th.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestOBLREQ0067RollingWindowIsExact(t *testing.T) {
	r := obligation.PeriodRule{Kind: obligation.PeriodRolling, Timezone: "UTC", RollingMonths: 12, RollingIncludesEvent: false}
	span, err := r.PeriodFor(when, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if !span.Start.Equal(when.AddDate(-1, 0, 0)) || span.Contains(when) {
		t.Fatalf("window %v..%v; the event itself must be outside an exclusive window", span.Start, span.End)
	}
	r.RollingIncludesEvent = true
	span, _ = r.PeriodFor(when, time.UTC)
	if !span.Contains(when) {
		t.Fatal("an inclusive window excludes the event")
	}
}

func TestOBLREQ0072To0077RegistrationLifecycle(t *testing.T) {
	r := obligation.Registration{TenantID: tenant, LegalEntity: entity, Authority: "authority:xa-revenue", State: obligation.RegistrationNotRequired}
	monitored, _ := r.Discover(obligation.Unknown, obligation.False)
	if monitored.State != obligation.RegistrationMonitor {
		t.Fatalf("unknown trigger moved NOT_REQUIRED to %s", monitored.State)
	}
	triggered, _ := monitored.Discover(obligation.True, obligation.False)
	if triggered.State != obligation.RegistrationTriggered {
		t.Fatalf("true trigger gave %s", triggered.State)
	}
	triggered.State = obligation.RegistrationInProgress
	if _, err := triggered.Issue("", when); err == nil {
		t.Fatal("a registration was issued with no authority identifier")
	}
	issued, err := triggered.Issue("XA-VAT-123", when)
	if err != nil || issued.State != obligation.RegistrationRegistered || issued.AuthorityIdentifier != "XA-VAT-123" {
		t.Fatalf("issue: %v %+v", err, issued)
	}
	stillRegistered, _ := issued.Discover(obligation.False, obligation.Unknown)
	if stillRegistered.State != obligation.RegistrationRegistered {
		t.Fatalf("falling revenue deregistered: %s", stillRegistered.State)
	}
	eligible, _ := issued.Discover(obligation.False, obligation.True)
	if eligible.State != obligation.RegistrationDeregistrationEligible {
		t.Fatalf("a met cessation rule gave %s", eligible.State)
	}
}

// ---------------------------------------------------------------------------
// calendar
// ---------------------------------------------------------------------------

func TestOBLREQ0062And0063LateEventsBelongToTheirLegalPeriod(t *testing.T) {
	// An event on 31 March arriving in April belongs to March. PeriodFor
	// takes no receipt time at all, so arrival cannot move it.
	r := obligation.PeriodRule{Kind: obligation.PeriodMonth, Timezone: "UTC"}
	span, err := r.PeriodFor(time.Date(2027, 3, 31, 23, 0, 0, 0, time.UTC), time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if span.Start.Month() != time.March {
		t.Fatalf("period starts %v", span.Start)
	}
}

func TestOBLREQ0078PeriodEngineSupportsEveryKind(t *testing.T) {
	loc := time.UTC
	for kind, wantStart := range map[obligation.PeriodKind]time.Time{
		obligation.PeriodMonth:   time.Date(2027, 3, 1, 0, 0, 0, 0, loc),
		obligation.PeriodQuarter: time.Date(2027, 1, 1, 0, 0, 0, 0, loc),
		obligation.PeriodYear:    time.Date(2026, 4, 1, 0, 0, 0, 0, loc),
	} {
		r := obligation.PeriodRule{Kind: kind, Timezone: "UTC", YearStartMonth: time.January}
		if kind == obligation.PeriodYear {
			r.YearStartMonth = time.April
		}
		span, err := r.PeriodFor(when, loc)
		if err != nil {
			t.Fatal(err)
		}
		if !span.Start.Equal(wantStart) {
			t.Errorf("%s starts %v, want %v", kind, span.Start, wantStart)
		}
	}
	for _, kind := range []obligation.PeriodKind{obligation.PeriodTransaction, obligation.PeriodEvent} {
		span, err := obligation.PeriodRule{Kind: kind, Timezone: "UTC"}.PeriodFor(when, loc)
		if err != nil || !span.Start.Equal(when) || !span.End.Equal(when) {
			t.Errorf("%s: %v %v", kind, span, err)
		}
	}
	custom := obligation.PeriodRule{Kind: obligation.PeriodCustom, Timezone: "UTC", Boundaries: []time.Time{
		time.Date(2027, 1, 1, 0, 0, 0, 0, loc), time.Date(2027, 2, 15, 0, 0, 0, 0, loc), time.Date(2027, 6, 1, 0, 0, 0, 0, loc),
	}}
	span, err := custom.PeriodFor(when, loc)
	if err != nil || span.Start.Day() != 15 {
		t.Fatalf("custom: %v %v", span, err)
	}
}

func TestOBLREQ0079LegalTimezoneIsExplicit(t *testing.T) {
	tokyo := time.FixedZone("Asia/Tokyo", 9*3600)
	r := obligation.PeriodRule{Kind: obligation.PeriodMonth, Timezone: "Asia/Tokyo"}
	// 31 March 20:00 UTC is 1 April in Tokyo.
	span, err := r.PeriodFor(time.Date(2027, 3, 31, 20, 0, 0, 0, time.UTC), tokyo)
	if err != nil {
		t.Fatal(err)
	}
	if span.Start.In(tokyo).Month() != time.April {
		t.Fatalf("period starts %v", span.Start.In(tokyo))
	}
	if _, err := r.PeriodFor(when, time.UTC); err == nil {
		t.Fatal("a location other than the declared legal timezone was accepted")
	}
	if err := (obligation.PeriodRule{Kind: obligation.PeriodMonth}).Validate(); err == nil {
		t.Fatal("a period rule with no timezone was accepted")
	}
}

func calendar() *obligation.HolidayCalendar {
	return &obligation.HolidayCalendar{
		Ref:      obligation.CalendarRef{ID: "cal:xa", Version: "2027.1"},
		Weekend:  []time.Weekday{time.Saturday, time.Sunday},
		Holidays: map[string]bool{"2027-04-19": true},
	}
}

func TestOBLREQ0080And0081BusinessDayAdjustmentIsContentAndPinsItsCalendar(t *testing.T) {
	march := obligation.Span{Start: time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2027, 4, 1, 0, 0, 0, 0, time.UTC)}
	ref := calendar().Ref
	// Due the 17th of the following month: 17 April 2027 is a Saturday, 19
	// April a holiday, so FOLLOWING lands on Tuesday the 20th and PRECEDING on
	// Friday the 16th.
	for adj, want := range map[obligation.BusinessDayAdjustment]int{obligation.AdjustFollowing: 20, obligation.AdjustPreceding: 16, obligation.AdjustNone: 17} {
		r := obligation.DueDateRule{Anchor: obligation.AnchorPeriodEnd, OffsetMonths: 1, DayOfMonth: 17, Adjustment: adj}
		if adj != obligation.AdjustNone {
			r.Calendar = &ref
		}
		due, err := r.Compute(march, time.UTC, calendar())
		if err != nil {
			t.Fatal(err)
		}
		if due.Legal.Day() != want {
			t.Errorf("%s: due %v, want the %d", adj, due.Legal, want)
		}
		if adj != obligation.AdjustNone && (due.Calendar == nil || due.Calendar.Version != "2027.1") {
			t.Errorf("%s: the calendar version is not recorded", adj)
		}
	}
	if err := (obligation.DueDateRule{Anchor: obligation.AnchorPeriodEnd, Adjustment: obligation.AdjustFollowing}).Validate(); err == nil {
		t.Fatal("an adjusting rule with no pinned calendar was accepted")
	}
	r := obligation.DueDateRule{Anchor: obligation.AnchorPeriodEnd, Adjustment: obligation.AdjustFollowing, Calendar: &obligation.CalendarRef{ID: "cal:xa", Version: "2026.9"}}
	if _, err := r.Compute(march, time.UTC, calendar()); err == nil {
		t.Fatal("a different calendar version than the rule pins was used")
	}
}

func TestOBLREQ0082And0083ExtensionAndCutoffNeverReplaceTheLegalDate(t *testing.T) {
	march := obligation.Span{Start: time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2027, 4, 1, 0, 0, 0, 0, time.UTC)}
	r := obligation.DueDateRule{Anchor: obligation.AnchorPeriodEnd, OffsetMonths: 1, DayOfMonth: 20, Adjustment: obligation.AdjustNone,
		ExtensionDays: 30, InternalCutoffDays: 5}
	due, err := r.Compute(march, time.UTC, nil)
	if err != nil {
		t.Fatal(err)
	}
	if due.Legal.Day() != 20 || due.Extended == nil || due.Extended.Day() != 20 || due.Extended.Month() != time.May ||
		due.InternalCutoff == nil || due.InternalCutoff.Day() != 15 {
		t.Fatalf("legal %v extended %v cutoff %v", due.Legal, due.Extended, due.InternalCutoff)
	}
}

// ---------------------------------------------------------------------------
// lifecycle, decisions, forecasts
// ---------------------------------------------------------------------------

func obligationAt(status obligation.Status) obligation.Obligation {
	due := time.Date(2027, 4, 20, 0, 0, 0, 0, time.UTC)
	return obligation.Obligation{
		ID: id.NewObligationID(uuid.MustParse("00000000-0000-7000-8000-0000000000b1")), TenantID: tenant,
		BusinessKey: "vat-return/2027-03", JurisdictionID: "jurisdiction:xa", Type: "vat-return",
		Period: obligation.Period{Start: time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2027, 3, 31, 0, 0, 0, 0, time.UTC), Due: due},
		Status: status, LegalEntity: entity, Authority: "authority:xa-revenue",
		Definition: obligation.DefinitionRef{ID: "vat-return", Version: "2026.1"}, Content: content,
		Responsibility: id.NewResponsibilityDecisionID(uuid.MustParse("00000000-0000-7000-8000-0000000000aa")),
		Duty:           obligation.DutyTransactionMonetary, Due: obligation.DueDates{Legal: due},
	}
}

func next() id.ObligationID {
	return id.NewObligationID(uuid.MustParse("00000000-0000-7000-8000-0000000000b2"))
}

func TestOBLREQ0039To0043DecisionReferencesWhatItMust(t *testing.T) {
	o := obligationAt(obligation.StatusOpen)
	if err := o.ValidateDecision(); err != nil {
		t.Fatalf("a complete decision was refused: %v", err)
	}
	for name, mutate := range map[string]func(*obligation.Obligation){
		"responsibility": func(o *obligation.Obligation) { o.Responsibility = id.ResponsibilityDecisionID{} },
		"content":        func(o *obligation.Obligation) { o.Content = obligation.ContentRef{} },
		"legal entity":   func(o *obligation.Obligation) { o.LegalEntity = id.LegalEntityID{} },
		"authority":      func(o *obligation.Obligation) { o.Authority = "" },
		"duty":           func(o *obligation.Obligation) { o.Duty = "" },
	} {
		c := o
		mutate(&c)
		if err := c.ValidateDecision(); err == nil {
			t.Errorf("a decision with no %s was accepted", name)
		}
	}
}

func TestOBLREQ0141NonMonetaryDutyHasNoAmountAndFilesWithoutOne(t *testing.T) {
	o := obligationAt(obligation.StatusReady)
	o.Duty = obligation.DutyInformationReturn
	if _, err := o.Transition(obligation.StatusFiled, when, next()); err != nil {
		t.Fatalf("an information return could not file without an amount: %v", err)
	}
	amount := eur(t, "0.00")
	o.Assessed = &amount
	if err := o.ValidateDecision(); err == nil {
		t.Fatal("a non-monetary duty carried a zero amount")
	}
}

func TestOBLREQ0084To0088LifecycleDistinguishesItsStates(t *testing.T) {
	for _, step := range []struct{ from, to obligation.Status }{
		{obligation.StatusOpen, obligation.StatusDataRequired},
		{obligation.StatusAccepted, obligation.StatusPaymentDue},
		{obligation.StatusPaymentDue, obligation.StatusPaid},
		{obligation.StatusAccepted, obligation.StatusAmendmentRequired},
	} {
		if !obligation.CanTransition(step.from, step.to) {
			t.Errorf("%s -> %s refused", step.from, step.to)
		}
	}
	if obligation.CanTransition(obligation.StatusDataRequired, obligation.StatusFiled) {
		t.Error("DATA_REQUIRED filed directly")
	}
	if obligation.CanTransition(obligation.StatusFiled, obligation.StatusPaid) {
		t.Error("FILED became PAID without acceptance and payment")
	}
	o := obligationAt(obligation.StatusReady)
	if got := o.EffectiveStatus(time.Date(2027, 4, 21, 0, 0, 0, 0, time.UTC)); got != obligation.StatusOverdue {
		t.Errorf("a ready obligation past its due date is %s", got)
	}
	if got := obligationAt(obligation.StatusFiled).EffectiveStatus(time.Date(2027, 4, 21, 0, 0, 0, 0, time.UTC)); got != obligation.StatusFiled {
		t.Errorf("a filed obligation is %s after its due date", got)
	}
}

func TestOBLREQ0089SuspendedBlocksAuthoritativeTransitions(t *testing.T) {
	for _, to := range []obligation.Status{obligation.StatusFiled, obligation.StatusPaid, obligation.StatusClosed, obligation.StatusAccepted} {
		if obligation.CanTransition(obligation.StatusSuspended, to) {
			t.Errorf("SUSPENDED -> %s permitted", to)
		}
	}
}

func TestOBLREQ0037And0038CorrectionsAreSuccessorsNotMutations(t *testing.T) {
	o := obligationAt(obligation.StatusReady)
	amount := eur(t, "120.00")
	o.Assessed = &amount
	filed, err := o.Transition(obligation.StatusFiled, when, next())
	if err != nil {
		t.Fatal(err)
	}
	if o.Status != obligation.StatusReady || filed.Supersedes == nil || *filed.Supersedes != o.ID || filed.ID == o.ID {
		t.Fatalf("original %s, successor %+v", o.Status, filed.Supersedes)
	}
}

func TestOBLREQ0094And0122ReopeningRequiresReasonAndAuthority(t *testing.T) {
	closed := obligationAt(obligation.StatusClosed)
	if _, err := closed.Transition(obligation.StatusAmendmentRequired, when, next()); err == nil {
		t.Fatal("a closed obligation reopened through Transition")
	}
	if _, err := closed.Reopen("", user, when, next()); err == nil {
		t.Fatal("a closed obligation reopened with no reason")
	}
	if _, err := closed.Reopen(errs.ReasonReviewRequired, id.UserID{}, when, next()); err == nil {
		t.Fatal("a closed obligation reopened with no authorizer")
	}
	reopened, err := closed.Reopen(errs.ReasonReviewRequired, user, when, next())
	if err != nil || reopened.Status != obligation.StatusAmendmentRequired || reopened.Reason != errs.ReasonReviewRequired {
		t.Fatalf("reopen: %v %+v", err, reopened.Status)
	}
}

func TestOBLREQ0046To0048ForecastIsASeparateNonAuthoritativeType(t *testing.T) {
	f := obligation.Forecast{
		ID: id.NewForecastID(uuid.MustParse("00000000-0000-7000-8000-0000000000c1")), TenantID: tenant, LegalEntity: entity,
		Source: obligation.ForecastFromAI,
	}
	if err := f.Validate(); err != nil {
		t.Fatal(err)
	}
	if f.Authoritative() {
		t.Fatal("a forecast reports itself authoritative")
	}
	// The compile-time half: id.ForecastID and id.ObligationID are distinct
	// types, so obligation.Obligation{ID: f.ID} does not compile.
}

// ---------------------------------------------------------------------------
// period close and late changes
// ---------------------------------------------------------------------------

func manifest(t *testing.T) obligation.PeriodCloseManifest {
	assessed := eur(t, "120.00")
	m, err := obligation.SealPeriod(obligation.PeriodCloseManifest{
		ID: id.NewPeriodCloseID(uuid.MustParse("00000000-0000-7000-8000-0000000000d1")), TenantID: tenant, LegalEntity: entity,
		Authority:    "authority:xa-revenue",
		Period:       obligation.Span{Start: time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2027, 4, 1, 0, 0, 0, 0, time.UTC)},
		Obligations:  []obligation.ClosedObligation{{ID: obligationAt(obligation.StatusClosed).ID, Status: obligation.StatusClosed, Assessed: &assessed}},
		Accumulators: []obligation.ClosedAccumulator{{Key: "xa.vat.2027-03", LastSeq: 41, Total: eur(t, "10150.00")}},
		ClosedAt:     time.Date(2027, 4, 5, 0, 0, 0, 0, time.UTC), ClosedBy: user,
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestOBLREQ0090And0091CloseManifestIsSealedAndReferencesItsState(t *testing.T) {
	m := manifest(t)
	if err := m.Verify(); err != nil {
		t.Fatal(err)
	}
	if len(m.Obligations) != 1 || len(m.Accumulators) != 1 || m.Accumulators[0].LastSeq != 41 {
		t.Fatalf("manifest %+v", m)
	}
	m.Accumulators[0].LastSeq = 42
	if err := m.Verify(); err == nil {
		t.Fatal("an edited manifest still verified")
	}
}

func TestOBLREQ0092To0098LateChangesRouteWithoutMutatingTheClose(t *testing.T) {
	m := manifest(t)
	april := obligation.Span{Start: time.Date(2027, 4, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2027, 5, 1, 0, 0, 0, 0, time.UTC)}
	march30 := time.Date(2027, 3, 30, 0, 0, 0, 0, time.UTC)

	inApril, err := obligation.RouteLateChange(m, definition(), march30, time.Date(2027, 4, 10, 0, 0, 0, 0, time.UTC), april)
	if err != nil || inApril.Treatment != obligation.AmendCurrentPeriod {
		t.Fatalf("an April arrival routed to %+v (%v)", inApril, err)
	}
	inJune, _ := obligation.RouteLateChange(m, definition(), march30, time.Date(2027, 6, 10, 0, 0, 0, 0, time.UTC), april)
	if inJune.Treatment != obligation.AmendPriorPeriod {
		t.Fatalf("a June arrival routed to %+v", inJune)
	}
	none := definition()
	none.Amendment = nil
	review, _ := obligation.RouteLateChange(m, none, march30, time.Date(2027, 4, 10, 0, 0, 0, 0, time.UTC), april)
	if !review.Review || review.Treatment != "" {
		t.Fatalf("an unapproved treatment did not route to review: %+v", review)
	}
	if err := m.Verify(); err != nil {
		t.Fatal("routing a late change altered the close")
	}
}
