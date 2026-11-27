package entitlement

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/market"
)

// The effective-capability decision (Doc 18 §02, §06):
//
//	ALLOW = contract_valid ∧ product_scope ∧ legal_pack_active
//	        ∧ runtime_capability_healthy ∧ …
//	DENY  = any false / unknown / conflicting prerequisite
//
// The contract side is the grant: ACTIVE on the day. The rest is the
// coverage the cell already proves — the same market assessment the
// dashboard and the coverage screens read — so an entitlement can never say
// more than the cell can do. Unknown is DENY: a country neither the research
// catalogue nor content names is not covered, and a jurisdiction finer than
// what content is assessed at is not proven.

// Decision is the effective-capability answer.
type Decision string

// The decisions.
const (
	DecisionAllow Decision = "ALLOW"
	DecisionDeny  Decision = "DENY"
)

// Effective is the one-word state a customer sees for a grant: whether the
// right can be used now, and if not, which kind of not.
type Effective string

// The effective states.
const (
	// EffectivePending: recorded, in review, or approved for a later date.
	EffectivePending Effective = "PENDING"
	// EffectiveActive: an active grant over verified coverage — ALLOW.
	EffectiveActive Effective = "ACTIVE"
	// EffectiveRestricted: an active grant the cell's coverage does not
	// prove. The contract says yes; the cell says not yet.
	EffectiveRestricted Effective = "RESTRICTED"
	// EffectiveSuspended: suspended, with or without a resume requested.
	EffectiveSuspended Effective = "SUSPENDED"
	// EffectiveExpired: past its end date.
	EffectiveExpired Effective = "EXPIRED"
	// EffectiveEnded: rejected, withdrawn or revoked.
	EffectiveEnded Effective = "ENDED"
)

// Effectives is every effective state.
var Effectives = []Effective{EffectivePending, EffectiveActive, EffectiveRestricted, EffectiveSuspended, EffectiveExpired, EffectiveEnded}

// Valid reports whether the state is one of these.
func (e Effective) Valid() bool { return slices.Contains(Effectives, e) }

// CoverageState is whether the cell proves the coverage a grant needs.
type CoverageState string

// The coverage states.
const (
	CoverageVerified CoverageState = "VERIFIED"
	CoverageUnproven CoverageState = "UNPROVEN"
)

// Reason is why a grant is pending or blocked. The coverage reasons are Doc
// 18 §06's denial codes; the others say which step of the grant's own
// lifecycle is outstanding.
type Reason string

// The reasons.
const (
	ReasonAwaitingSubmission   Reason = "AWAITING_SUBMISSION"
	ReasonAwaitingApproval     Reason = "AWAITING_SECOND_APPROVAL"
	ReasonNotYetEffective      Reason = "AWAITING_EFFECTIVE_DATE"
	ReasonSuspended            Reason = "ENTITLEMENT_SUSPENDED"
	ReasonResumeAwaiting       Reason = "RESUME_AWAITING_APPROVAL"
	ReasonExpired              Reason = "ENTITLEMENT_EXPIRED"
	ReasonEnded                Reason = "ENTITLEMENT_ENDED"
	ReasonContentNotActive     Reason = "CONTENT_NOT_ACTIVE"
	ReasonOperationUnsupported Reason = "OPERATION_NOT_SUPPORTED"
	ReasonTechUnavailable      Reason = "TECHNICALLY_UNAVAILABLE"
	ReasonGeographyNotProven   Reason = "GEOGRAPHY_NOT_PROVEN"
	ReasonProviderUnproven     Reason = "PROVIDER_EVIDENCE_ABSENT"
)

// Finding is one reason, with what a person reads about it.
type Finding struct {
	Reason Reason
	Detail string
}

// MarketNotListed is the market status of a country neither the research
// catalogue nor content names.
const MarketNotListed = "NOT_LISTED"

// FilingGate is the legal filing gate for one legal entity in one country:
// ready when some authority there would accept a return from it now.
type FilingGate struct {
	Ready bool
	// Authority is the authority whose gate is ready, or the first one
	// considered when none is.
	Authority string
	// Reason is the gate's blocking reason when it is not ready; "" when no
	// authority there is governed at all.
	Reason string
}

// Facts are the cell's verified coverage, as of one instant.
type Facts struct {
	// Markets is market.Assess's answer, by country.
	Markets map[string]market.Market
	// Authoritative is whether the cell may emit authoritative output.
	Authoritative bool
	// Filing returns the filing gate for a legal entity in a country; nil
	// means no gate is known, which is never ready.
	Filing func(le id.LegalEntityID, country string) FilingGate
}

// Coverage is what the cell proves for one grant's scope.
type Coverage struct {
	State CoverageState
	// MarketStatus is the market's status (ACTIVE, PILOT, RESEARCH_ONLY) or
	// NOT_LISTED.
	MarketStatus string
	MarketReason market.Reason
	// Research is the catalogue's research state, "" when not catalogued.
	Research market.Research
	// ServicesInForce are the service families content in force covers in
	// the country.
	ServicesInForce []string
	// Filing is the filing gate, for FILE and REMIT grants.
	Filing   *FilingGate
	Findings []Finding
}

// Evaluation is a grant read on a day against the cell's coverage.
type Evaluation struct {
	Status    Status
	Effective Effective
	Decision  Decision
	// Findings are every reason the grant is pending or blocked: the
	// grant's own first, then coverage's. Empty only for ALLOW.
	Findings []Finding
	Coverage Coverage
	// Mismatch is an approved grant — in force now or scheduled — whose
	// coverage the cell does not prove: an entitlement claim the system
	// cannot back.
	Mismatch bool
}

// AssessCoverage reports what the cell proves for a grant's scope. It does
// not look at the grant's lifecycle.
func AssessCoverage(g Grant, f Facts) Coverage {
	country := g.Country()
	c := Coverage{MarketStatus: MarketNotListed, ServicesInForce: []string{}}
	add := func(r Reason, detail string) { c.Findings = append(c.Findings, Finding{Reason: r, Detail: detail}) }
	m, listed := f.Markets[country]
	if listed {
		c.MarketStatus, c.MarketReason, c.Research = string(m.Status), m.Reason, m.Research
		c.ServicesInForce = slices.Clone(m.Services)
		if c.ServicesInForce == nil {
			c.ServicesInForce = []string{}
		}
	}
	contentFor := listed && m.Status != market.StatusResearchOnly && slices.Contains(m.Services, string(g.Service))

	if g.Mode == ModeFederated {
		// The provider's coverage is the provider's; native content never
		// stands in for it (Doc 18 §08, CLM-07).
		add(ReasonProviderUnproven, "A federated grant runs on a third-party provider's calculation. No evidence of that provider's coverage is recorded in this cell, and native coverage is never counted as the provider's.")
	}
	switch {
	case !listed:
		add(ReasonContentNotActive, fmt.Sprintf("%s is in neither the research catalogue nor the content in force. It is not supported.", country))
	case m.Status == market.StatusResearchOnly:
		add(ReasonContentNotActive, fmt.Sprintf("%s is a research profile only: no content is in force there. A research profile is never a live market.", country))
	case !contentFor:
		add(ReasonContentNotActive, fmt.Sprintf("The content in force for %s covers %s, not %s.", country, listOrNone(m.Services), g.Service))
	}
	if strings.Contains(g.Jurisdiction, "-") {
		add(ReasonGeographyNotProven, fmt.Sprintf("Coverage is assessed for %s as a whole. Coverage of %s alone is not proven, so it stays restricted to the proven geography.", country, g.Jurisdiction))
	}
	switch g.Operation {
	case OpCommit:
		if !f.Authoritative {
			add(ReasonTechUnavailable, "This cell emits advisory output only. A commit right cannot be used until the cell is authorised to produce authoritative results.")
		}
	case OpFile, OpRemit:
		gate := FilingGate{}
		if f.Filing != nil {
			gate = f.Filing(g.LegalEntity, country)
		}
		c.Filing = &gate
		if !gate.Ready {
			detail := fmt.Sprintf("No filing gate in %s is ready for this legal entity", country)
			if gate.Authority != "" {
				detail += fmt.Sprintf(" (%s: %s)", gate.Authority, gate.Reason)
			}
			what := "file a return"
			if g.Operation == OpRemit {
				what = "remit the tax a return reports"
			}
			add(ReasonOperationUnsupported, detail+". The calculation may be covered while the right to "+what+" is not.")
		}
	case OpEInvoice:
		add(ReasonOperationUnsupported, "No e-invoicing clearance adapter is evidenced in this cell for any country. The e-invoicing right has nothing to run on yet.")
	}
	c.State = CoverageVerified
	if len(c.Findings) > 0 {
		c.State = CoverageUnproven
	}
	return c
}

// Evaluate reads a grant on day against the cell's coverage.
func Evaluate(g Grant, day time.Time, f Facts) Evaluation {
	ev := Evaluation{Status: g.StatusOn(day), Coverage: AssessCoverage(g, f), Decision: DecisionDeny}
	add := func(r Reason, detail string) { ev.Findings = append(ev.Findings, Finding{Reason: r, Detail: detail}) }
	switch ev.Status {
	case StatusDraft:
		ev.Effective = EffectivePending
		add(ReasonAwaitingSubmission, "Recorded as a draft. An administrator submits it for review.")
	case StatusInReview:
		ev.Effective = EffectivePending
		add(ReasonAwaitingApproval, "Submitted. A second administrator — not the one who recorded or submitted it — approves it before it has any effect.")
	case StatusScheduled:
		ev.Effective = EffectivePending
		add(ReasonNotYetEffective, fmt.Sprintf("Approved, in force from %s.", g.ActiveFrom().Format(time.DateOnly)))
	case StatusSuspended:
		ev.Effective = EffectiveSuspended
		if _, ok := g.ResumePending(); ok {
			add(ReasonResumeAwaiting, "Suspended. A resume is requested and awaits a second administrator.")
		} else {
			add(ReasonSuspended, "Suspended. Suspension trumps approval until a resume is requested and approved by a second administrator.")
		}
	case StatusExpired:
		ev.Effective = EffectiveExpired
		add(ReasonExpired, fmt.Sprintf("Ended on %s. History stays readable; a renewal is a new grant.", g.EffectiveTo.Format(time.DateOnly)))
	case StatusRejected, StatusWithdrawn, StatusRevoked:
		ev.Effective = EffectiveEnded
		add(ReasonEnded, fmt.Sprintf("%s. It gives no right.", strings.ToUpper(string(ev.Status[:1]))+strings.ToLower(string(ev.Status[1:]))))
	case StatusActive:
		ev.Effective = EffectiveActive
	}
	ev.Findings = append(ev.Findings, ev.Coverage.Findings...)
	unproven := ev.Coverage.State == CoverageUnproven
	if ev.Status == StatusActive {
		if unproven {
			ev.Effective = EffectiveRestricted
		} else {
			ev.Decision = DecisionAllow
		}
	}
	ev.Mismatch = unproven && (ev.Status == StatusActive || ev.Status == StatusScheduled)
	return ev
}

func listOrNone(s []string) string {
	if len(s) == 0 {
		return "no service family"
	}
	return strings.Join(s, ", ")
}
