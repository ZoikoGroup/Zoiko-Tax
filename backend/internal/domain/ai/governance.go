package ai

import (
	"fmt"
	"slices"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/privacy"
)

// This file is the Go half of ADR-0006 §2.5's enforcement: an independent
// policy gate that decides whether an AI call may leave the process at all.
//
// The Python Gateway (intelligence/src/ztax_gateway/governance.py) is the
// enforcement point and stays so — "the Go caller cannot override any of this,
// because the decision is made after the call leaves it". This gate does not
// replace that decision and cannot relax it; it can only refuse earlier. Two
// properties follow from running it here as well:
//
//   - A refused call never leaves the process, so its input never crosses the
//     boundary, is never logged by the Gateway and never reaches a network a
//     provider could be on. For an A5 request that is the difference between
//     "refused" and "refused after being transmitted".
//   - ZTAX-AIGOV-001 §15 requires every A5 path to fail "independently of
//     model response". Two gates in two languages on two sides of an mTLS hop
//     are independent in the sense that matters: a defect in one does not
//     open the other.
//
// The gate is a pure function of the estate's own governance facts —
// registration, ceilings, residency, data classes, kill-switch state — and the
// governance context of the request. Its inputs never include model output,
// prompt text or tool output, so nothing a model says can change its verdict.
// GovernanceContext has no field that could carry such content, and a test
// pins its field set so adding one is a reviewed change.
//
// The vocabularies below are data in contracts/schemas/ai/ (ADR-0006 §2.2).
// internal/adapter/gateway's schema test proves these constants are exactly
// that data; it lives there because this package may not read a file
// (ADR-0007 §2.5).

// RiskTiers is the closed T0-T4 set, in ascending order of risk. The order is
// load-bearing: a ceiling comparison is an index comparison.
var RiskTiers = []RiskTier{RiskTierT0, RiskTierT1, RiskTierT2, RiskTierT3, RiskTierT4}

// AuthorityOutcomes is the closed A0-A5 set, in ascending order of authority.
var AuthorityOutcomes = []AuthorityOutcome{
	AuthorityOutcomeA0, AuthorityOutcomeA1, AuthorityOutcomeA2,
	AuthorityOutcomeA3, AuthorityOutcomeA4, AuthorityOutcomeA5,
}

// MaxPermittedAuthority is the authority ceiling for every use case. ADR-0006
// §2.5 refuses A5 "regardless of what any model or tool prompt says", and
// AIGOV-001 §4 has it "technically blocked", so this is a constant rather than
// a field on UseCasePolicy — a per-use-case setting would be a field somebody
// could set to A5. The Python side has the same constant
// (governance.MAX_PERMITTED_AUTHORITY).
const MaxPermittedAuthority = AuthorityOutcomeA4

// MaxPermittedRiskTier is the highest tier a use case may be registered at.
// AIGOV-001 §5 makes T4 "default prohibited; only deterministic non-AI control
// may own final authority. Any exceptional use requires constitutional
// amendment", and §15 forbids enabling it "through feature flags or customer
// configuration". A registry entry is configuration, so T4 is not registrable,
// and a T4 request therefore always exceeds its use case's ceiling.
const MaxPermittedRiskTier = RiskTierT3

// neverToAModel is the privacy classes no use case may declare. P7 is secrets
// and credentials (PRIV-001 §4): there is no AI purpose for which sending a
// credential to a model is correct, and an allowlist that could list it is an
// allowlist that eventually will.
var neverToAModel = []privacy.Class{privacy.P7}

func (t RiskTier) rank() int { return slices.Index(RiskTiers, t) }

func (a AuthorityOutcome) rank() int { return slices.Index(AuthorityOutcomes, a) }

// The refusal reason codes (ADR-0016 §2.4). The strings are the Python
// Gateway's governance.Refusal values, so a refusal means the same thing
// whichever side of the boundary raised it; contracts/schemas/ai/
// governance-refusal.schema.json is the list both are tested against.
//
// They are registered here rather than in errs/codes.go because they are the
// AI train's vocabulary and this package is their only producer; the register
// itself is still the one in errs, so a duplicate anywhere panics at start-up.
var (
	// ReasonMalformedContext is a governance context that cannot be acted on.
	// The context is assembled by this estate's own code, not by a customer,
	// so a malformed one is a defect in us — CategoryInternal, not validation.
	ReasonMalformedContext = errs.Register("AI_MALFORMED_CONTEXT", errs.CategoryInternal,
		"lane-l",
		"The AI request did not carry a complete governance context. This is a defect in ZoikoTax; the request was not sent.")

	// ReasonKillSwitchEngaged is the global or per-use-case kill switch.
	ReasonKillSwitchEngaged = errs.Register("AI_KILL_SWITCH_ENGAGED", errs.CategoryPolicy,
		"lane-l",
		"This AI capability has been switched off by an operator. Deterministic processing is unaffected.")

	// ReasonUnknownUseCase is a use case nobody registered.
	ReasonUnknownUseCase = errs.Register("AI_UNKNOWN_USE_CASE", errs.CategoryPolicy,
		"lane-l",
		"The AI capability requested is not a registered use case and cannot run.")

	// ReasonUseCaseSuspended is a registered use case taken out of service.
	ReasonUseCaseSuspended = errs.Register("AI_USE_CASE_SUSPENDED", errs.CategoryPolicy,
		"lane-l",
		"This AI capability is suspended. Deterministic processing is unaffected.")

	// ReasonAuthorityRefused is an authority outcome above the ceiling, A5 always.
	ReasonAuthorityRefused = errs.Register("AI_AUTHORITY_REFUSED", errs.CategoryPolicy,
		"lane-l",
		"The AI request asked for more authority than the use case is permitted. A5 actions are refused unconditionally.")

	// ReasonRiskTierExceeded is a request above its use case's risk ceiling.
	ReasonRiskTierExceeded = errs.Register("AI_RISK_TIER_EXCEEDED", errs.CategoryPolicy,
		"lane-l",
		"The AI request's risk tier exceeds what the use case is approved for. It is refused, not downgraded.")

	// ReasonResidencyRefused is a call from a region the use case did not declare.
	ReasonResidencyRefused = errs.Register("AI_RESIDENCY_REFUSED", errs.CategoryPolicy,
		"lane-l",
		"The AI use case is not approved to process data in this region.")

	// ReasonDataClassRefused is a privacy class the use case may not send to a
	// model. Go-only for now: the Python UseCase has no data-class allowlist.
	ReasonDataClassRefused = errs.Register("AI_DATA_CLASS_REFUSED", errs.CategoryPolicy,
		"lane-l",
		"The AI request would send a class of data the use case is not approved to send to a model.")
)

// RefusalCodes is the closed set the gate can return, in check order.
var RefusalCodes = []errs.ReasonCode{
	ReasonMalformedContext,
	ReasonKillSwitchEngaged,
	ReasonUnknownUseCase,
	ReasonUseCaseSuspended,
	ReasonAuthorityRefused,
	ReasonRiskTierExceeded,
	ReasonResidencyRefused,
	ReasonDataClassRefused,
}

// UseCasePolicy is the governance of one registered AI use case, as the gate
// reads it: the Go projection of AIUseCase
// (contracts/schemas/ai/ai-use-case.schema.json), carrying exactly the fields
// a refusal can turn on.
type UseCasePolicy struct {
	UseCase AiUseCase
	// Owner is required: an unowned use case is one nobody can be asked about
	// when it misbehaves, which is the first question an incident asks.
	Owner string
	// MaxAuthority can never be A5; NewPolicy refuses it.
	MaxAuthority AuthorityOutcome
	// MaxRiskTier can never be T4; NewPolicy refuses it.
	MaxRiskTier RiskTier
	// PermittedRegions is the residency allowlist (ADR-0006 §2.8). Empty
	// means no region, which is the correct default: a use case that has not
	// declared where it may run has not been through residency review.
	PermittedRegions []string
	// PermittedDataClasses is the PRIV-001 allowlist. Deny by default, for the
	// same reason.
	PermittedDataClasses []privacy.Class
	// Suspended takes the use case out of service without unregistering it,
	// so the history of what it was remains readable.
	Suspended bool
}

// Policy is a validated, immutable snapshot of the AI governance registry and
// kill-switch state. The gate evaluates against a snapshot rather than a live
// registry so a decision is a function of values, and so the adapter that
// holds the current snapshot can swap it atomically when an operator engages
// a kill switch.
//
// The zero Policy has no use cases and refuses everything, which is the
// correct failure mode for a Policy nobody loaded.
type Policy struct {
	useCases   map[AiUseCase]UseCasePolicy
	killed     map[AiUseCase]struct{}
	globalKill bool
}

// NewPolicy validates the registry and builds a snapshot.
//
// killed may name use cases that are not registered. During an incident the
// identifier in hand may be one nobody can find in the registry, and refusing
// to act on it because it is unrecognised is the wrong behaviour for a kill
// switch (the Python UseCaseRegistry.kill makes the same choice).
func NewPolicy(useCases []UseCasePolicy, killed []AiUseCase, globalKill bool) (Policy, error) {
	p := Policy{
		useCases:   make(map[AiUseCase]UseCasePolicy, len(useCases)),
		killed:     make(map[AiUseCase]struct{}, len(killed)),
		globalKill: globalKill,
	}
	for _, uc := range useCases {
		if err := uc.validate(); err != nil {
			return Policy{}, err
		}
		if _, dup := p.useCases[uc.UseCase]; dup {
			return Policy{}, fmt.Errorf("ai: use case %q registered twice", uc.UseCase)
		}
		uc.PermittedRegions = slices.Clone(uc.PermittedRegions)
		uc.PermittedDataClasses = slices.Clone(uc.PermittedDataClasses)
		p.useCases[uc.UseCase] = uc
	}
	for _, k := range killed {
		p.killed[k] = struct{}{}
	}
	return p, nil
}

func (uc UseCasePolicy) validate() error {
	if uc.UseCase == "" {
		return fmt.Errorf("ai: use case has no identifier")
	}
	if uc.Owner == "" {
		return fmt.Errorf("ai: use case %q has no owner", uc.UseCase)
	}
	if !uc.MaxAuthority.valid() {
		return fmt.Errorf("ai: use case %q: unknown authority outcome %q", uc.UseCase, uc.MaxAuthority)
	}
	if uc.MaxAuthority.rank() > MaxPermittedAuthority.rank() {
		return fmt.Errorf("ai: use case %q declares %s authority, which ADR-0006 §2.5 refuses unconditionally",
			uc.UseCase, uc.MaxAuthority)
	}
	if !uc.MaxRiskTier.valid() {
		return fmt.Errorf("ai: use case %q: unknown risk tier %q", uc.UseCase, uc.MaxRiskTier)
	}
	if uc.MaxRiskTier.rank() > MaxPermittedRiskTier.rank() {
		return fmt.Errorf("ai: use case %q declares risk tier %s, which AIGOV-001 §5 does not permit by configuration",
			uc.UseCase, uc.MaxRiskTier)
	}
	for _, r := range uc.PermittedRegions {
		if r == "" {
			return fmt.Errorf("ai: use case %q permits an empty region", uc.UseCase)
		}
	}
	for _, c := range uc.PermittedDataClasses {
		if !c.Valid() {
			return fmt.Errorf("ai: use case %q permits unknown privacy class %q", uc.UseCase, c)
		}
		if slices.Contains(neverToAModel, c) {
			return fmt.Errorf("ai: use case %q permits privacy class %s, which may never reach a model", uc.UseCase, c)
		}
	}
	return nil
}

// UseCase returns the registered policy for a use case.
func (p Policy) UseCase(uc AiUseCase) (UseCasePolicy, bool) {
	v, ok := p.useCases[uc]
	if !ok {
		return UseCasePolicy{}, false
	}
	v.PermittedRegions = slices.Clone(v.PermittedRegions)
	v.PermittedDataClasses = slices.Clone(v.PermittedDataClasses)
	return v, true
}

// Killed reports whether the kill switch is engaged for a use case, globally
// or individually.
func (p Policy) Killed(uc AiUseCase) bool {
	if p.globalKill {
		return true
	}
	_, ok := p.killed[uc]
	return ok
}

// GovernanceContext is ADR-0006 §2.5's governance context: what travels on
// every AI request and the only input the gate has about the request.
//
// It deliberately has no field for the request body, the prompt or anything a
// model produced. The gate's verdict is therefore a function of who is asking,
// for which registered capability, at what authority and risk, from where, and
// with which classes of data — never of what the content says.
type GovernanceContext struct {
	// Tenant comes from the request's security context (ADR-0012 §2.7). The
	// adapter fills it from ctx; nothing above the adapter supplies it.
	Tenant           id.TenantID
	UseCase          AiUseCase
	AuthorityOutcome AuthorityOutcome
	RiskTier         RiskTier
	// Region is the regional cell the call is made from (ADR-0006 §2.8).
	Region string
	// DataClasses is every PRIV-001 class present in the input. At least one:
	// a request that cannot say what it carries cannot be residency- or
	// privacy-routed, so it is malformed rather than assumed public.
	DataClasses []privacy.Class
}

func (g GovernanceContext) validate() error {
	if g.Tenant.IsZero() {
		return fmt.Errorf("no tenant in the security context")
	}
	if g.UseCase == "" {
		return fmt.Errorf("empty use case")
	}
	if !g.AuthorityOutcome.valid() {
		return fmt.Errorf("unknown authority outcome %q", g.AuthorityOutcome)
	}
	if !g.RiskTier.valid() {
		return fmt.Errorf("unknown risk tier %q", g.RiskTier)
	}
	if g.Region == "" {
		return fmt.Errorf("empty region")
	}
	if len(g.DataClasses) == 0 {
		return fmt.Errorf("no data classes declared")
	}
	for _, c := range g.DataClasses {
		if !c.Valid() {
			return fmt.Errorf("unknown privacy class %q", c)
		}
	}
	return nil
}

// HighestDataClass returns the most sensitive class in the context, in
// PRIV-001 order. It is what Provenance.DataClass records (ADR-0006 §2.7 logs
// one data classification per crossing). Empty if the context declares none.
func (g GovernanceContext) HighestDataClass() privacy.Class {
	best := -1
	for _, c := range g.DataClasses {
		if i := slices.Index(privacy.Classes, c); i > best {
			best = i
		}
	}
	if best < 0 {
		return ""
	}
	return privacy.Classes[best]
}

// Decision is the gate's verdict.
type Decision struct {
	Allowed bool
	// Reason is one of RefusalCodes when Allowed is false, and empty otherwise.
	Reason errs.ReasonCode
	// Detail is safe to show a caller and to log: it names the use case, the
	// level or the region, and never content.
	Detail string
}

// Err renders a refusal as the estate's typed error, and nil for an allowed
// decision.
func (d Decision) Err() error {
	if d.Allowed {
		return nil
	}
	category := errs.CategoryPolicy
	if d.Reason == ReasonMalformedContext {
		category = errs.CategoryInternal
	}
	return errs.New(category, d.Reason, d.Detail)
}

func refuse(reason errs.ReasonCode, format string, args ...any) Decision {
	return Decision{Reason: reason, Detail: fmt.Sprintf(format, args...)}
}

// Evaluate decides whether an AI call may proceed.
//
// The check order is the Python Gateway's, so one request is refused with the
// same code on either side, and it is the order of decreasing blast radius:
// the kill switch first, because an engaged switch means stop everything and
// the reason for stopping is not up for evaluation; then registration; then
// authority; then risk; then residency; then data classes. The order never
// changes which calls are permitted — every path below refuses — only what an
// operator reads during an incident.
//
// A5 is refused before the per-use-case ceiling is consulted, so no registry
// state can permit it; and since NewPolicy cannot register an A5 ceiling, the
// ceiling check would refuse it too. There is no option, flag or override
// parameter: a parameter that could relax a refusal is a parameter that will
// eventually be passed.
func Evaluate(p Policy, g GovernanceContext) Decision {
	if err := g.validate(); err != nil {
		return refuse(ReasonMalformedContext, "governance context: %v", err)
	}

	if p.Killed(g.UseCase) {
		return refuse(ReasonKillSwitchEngaged, "the kill switch is engaged for %q", g.UseCase)
	}

	uc, ok := p.useCases[g.UseCase]
	if !ok {
		return refuse(ReasonUnknownUseCase, "%q is not a registered AI use case", g.UseCase)
	}
	if uc.Suspended {
		return refuse(ReasonUseCaseSuspended, "%q is suspended", g.UseCase)
	}

	if g.AuthorityOutcome == AuthorityOutcomeA5 {
		return refuse(ReasonAuthorityRefused, "A5 actions are refused unconditionally")
	}
	if g.AuthorityOutcome.rank() > MaxPermittedAuthority.rank() {
		return refuse(ReasonAuthorityRefused, "authority %s exceeds the permitted ceiling", g.AuthorityOutcome)
	}
	if g.AuthorityOutcome.rank() > uc.MaxAuthority.rank() {
		return refuse(ReasonAuthorityRefused, "authority %s exceeds %q's ceiling of %s",
			g.AuthorityOutcome, g.UseCase, uc.MaxAuthority)
	}

	// Refused rather than downgraded: downgrading would let a caller reach a
	// lower-scrutiny path by mislabelling its own request.
	if g.RiskTier.rank() > uc.MaxRiskTier.rank() {
		return refuse(ReasonRiskTierExceeded, "risk tier %s exceeds %q's ceiling of %s",
			g.RiskTier, g.UseCase, uc.MaxRiskTier)
	}

	// ADR-0006 §2.8: there is no "the model is only in one region" exception.
	if !slices.Contains(uc.PermittedRegions, g.Region) {
		return refuse(ReasonResidencyRefused, "region %q is not permitted for %q", g.Region, g.UseCase)
	}

	for _, c := range g.DataClasses {
		if slices.Contains(neverToAModel, c) || !slices.Contains(uc.PermittedDataClasses, c) {
			return refuse(ReasonDataClassRefused, "privacy class %s is not permitted for %q", c, g.UseCase)
		}
	}

	return Decision{Allowed: true}
}
