package sourcing

import (
	"fmt"
	"strings"
)

// Right is one permission in SRC-001 §4's mandatory taxonomy.
//
// The wire spelling is the specification's, snake_case, because these names
// are vocabulary a lawyer reads in a register as often as an engineer reads
// them in code, and one spelling across both is worth more than house style.
//
// Two rows of §4 are not here: attribution_required and share_alike_trigger.
// They answer "must we do something", not "may we do something", and putting
// them on the ALLOW/DENY axis would make ALLOW mean "attribution is required" —
// a permission state whose meaning inverts by row. They are Obligations on the
// profile instead. Equally, the §5 example's hosted_use is not a §4 right; it
// is represented by the record's permitted deployment environments, which is
// where ZTAX-SRC-REQ-0008 puts the hosted/private distinction.
type Right string

// The permissions.
const (
	RightInternalUse       Right = "internal_use"
	RightAutomatedIngest   Right = "automated_ingest"
	RightCache             Right = "cache"
	RightArchive           Right = "archive"
	RightTransform         Right = "transform"
	RightQuote             Right = "quote"
	RightCustomerDisplay   Right = "customer_display"
	RightDerivedOutput     Right = "derived_output"
	RightRAGRetrieval      Right = "rag_retrieval"
	RightEmbedding         Right = "embedding"
	RightModelTraining     Right = "model_training"
	RightEvaluation        Right = "evaluation"
	RightRawRedistribution Right = "raw_redistribution"
	RightCustomerExport    Right = "customer_export"
	RightPrivateBundle     Right = "private_bundle"
	RightEdgeBundle        Right = "edge_bundle"
	RightAffiliateUse      Right = "affiliate_use"
	RightSubprocessorUse   Right = "subprocessor_use"
	RightSublicense        Right = "sublicense"
	RightBackupDR          Right = "backup_dr"
	RightCrossBorder       Right = "cross_border"
	RightHistoricalReplay  Right = "historical_replay"
	RightAuditDisclosure   Right = "audit_disclosure"
	RightCommercialUse     Right = "commercial_use"
)

// Rights is the whole taxonomy, in the specification's order.
var Rights = []Right{
	RightInternalUse, RightAutomatedIngest, RightCache, RightArchive, RightTransform, RightQuote,
	RightCustomerDisplay, RightDerivedOutput, RightRAGRetrieval, RightEmbedding, RightModelTraining,
	RightEvaluation, RightRawRedistribution, RightCustomerExport, RightPrivateBundle, RightEdgeBundle,
	RightAffiliateUse, RightSubprocessorUse, RightSublicense, RightBackupDR, RightCrossBorder,
	RightHistoricalReplay, RightAuditDisclosure, RightCommercialUse,
}

// Valid reports whether r is in the taxonomy. A right nobody defined is a right
// nobody can have granted, so a profile naming one is malformed rather than
// generous.
func (r Right) Valid() bool {
	for _, k := range Rights {
		if r == k {
			return true
		}
	}
	return false
}

// State is SRC-001 §4.1's tri-state-plus-constraints.
type State string

// The states.
const (
	// StateAllow: expressly granted, or clearly available on an approved public,
	// open or public-domain basis.
	StateAllow State = "ALLOW"
	// StateDeny: prohibited or unavailable.
	StateDeny State = "DENY"
	// StateConditional: allowed subject to the listed conditions.
	StateConditional State = "CONDITIONAL"
	// StateUnknown: not established. Resolves as DENY until Legal resolves it
	// (ZTAX-SRC-REQ-0005).
	StateUnknown State = "UNKNOWN"
)

// Valid reports whether s is one of the four states.
func (s State) Valid() bool {
	switch s {
	case StateAllow, StateDeny, StateConditional, StateUnknown:
		return true
	}
	return false
}

// ConditionKind is what a CONDITIONAL grant is conditional on.
//
// Only the kinds a build can check against the use it is evaluating are
// machine-satisfiable. Everything else — a seat cap, a volume limit, a
// downstream licence, a customer list — is OTHER, and OTHER is never satisfied
// by a machine: "CONDITIONAL-satisfied" (SRC-001 §15) has to mean satisfied,
// and a condition the gate cannot read is one it cannot vouch for.
type ConditionKind string

// The condition kinds.
const (
	// ConditionTerritory: every territory the use covers must be listed.
	ConditionTerritory ConditionKind = "TERRITORY"
	// ConditionDeploymentMode: the use's deployment mode must be listed.
	ConditionDeploymentMode ConditionKind = "DEPLOYMENT_MODE"
	// ConditionAttribution: permitted, provided the notice is rendered. The
	// gate treats it as met and reports the notice as an obligation for the
	// attribution service to discharge (SRC-001 §17).
	ConditionAttribution ConditionKind = "ATTRIBUTION"
	// ConditionOther: anything else, in words. Never machine-satisfied.
	ConditionOther ConditionKind = "OTHER"
)

// Valid reports whether k is a known kind.
func (k ConditionKind) Valid() bool {
	switch k {
	case ConditionTerritory, ConditionDeploymentMode, ConditionAttribution, ConditionOther:
		return true
	}
	return false
}

// Condition is one constraint on a CONDITIONAL grant.
type Condition struct {
	Kind ConditionKind
	// Values are territory codes for TERRITORY and DeploymentMode values for
	// DEPLOYMENT_MODE. Empty for the other kinds.
	Values []string
	// Text is the condition in words: the attribution notice, or what an OTHER
	// condition actually requires.
	Text string
}

// Grant is the state of one right and, for CONDITIONAL, its conditions.
type Grant struct {
	State      State
	Conditions []Condition
}

// ObligationState says whether a licence obligation applies.
type ObligationState string

// The obligation states. UNKNOWN is a legitimate value — it is what an
// unreviewed licence has — and is reported as an obligation that may apply,
// because a notice nobody knew was required is still a breach (R1, SRC-001 §22).
const (
	ObligationRequired    ObligationState = "REQUIRED"
	ObligationNotRequired ObligationState = "NOT_REQUIRED"
	ObligationUnknown     ObligationState = "UNKNOWN"
)

// Valid reports whether o is a known state.
func (o ObligationState) Valid() bool {
	switch o {
	case ObligationRequired, ObligationNotRequired, ObligationUnknown:
		return true
	}
	return false
}

// RightsProfile is a source's machine-readable permission set (SRC-001 §4).
//
// A right absent from Grants is UNKNOWN — "silent" in the constitutional rule's
// words — and therefore DENY. There is no default grant and no way to express
// one: a profile that wanted to allow everything would have to say so right by
// right, which is the point.
type RightsProfile struct {
	Grants map[Right]Grant
	// AttributionRequired and ShareAlikeTrigger are §4's two obligation rows.
	AttributionRequired ObligationState
	ShareAlikeTrigger   ObligationState
}

// Validate checks the profile is well-formed: every right is in the taxonomy,
// every state is one of four, and conditions appear exactly where the state
// is CONDITIONAL. A CONDITIONAL grant with no conditions is an ALLOW somebody
// did not want to write down, and an ALLOW carrying conditions is a CONDITIONAL
// somebody will read as unconditional; both are refused.
func (p RightsProfile) Validate() error {
	for r, g := range p.Grants {
		if !r.Valid() {
			return errorf("rights profile names %q, which is not in the SRC-001 §4 taxonomy", r)
		}
		if !g.State.Valid() {
			return errorf("right %s has state %q; a state is ALLOW, DENY, CONDITIONAL or UNKNOWN", r, g.State)
		}
		switch {
		case g.State == StateConditional && len(g.Conditions) == 0:
			return errorf("right %s is CONDITIONAL and lists no conditions", r)
		case g.State != StateConditional && len(g.Conditions) > 0:
			return errorf("right %s is %s and carries conditions; only a CONDITIONAL grant has conditions", r, g.State)
		}
		for _, c := range g.Conditions {
			if err := c.validate(r); err != nil {
				return err
			}
		}
	}
	if !p.AttributionRequired.Valid() || !p.ShareAlikeTrigger.Valid() {
		return errorf("rights profile must state attribution_required and share_alike_trigger as REQUIRED, NOT_REQUIRED or UNKNOWN")
	}
	return nil
}

func (c Condition) validate(r Right) error {
	if !c.Kind.Valid() {
		return errorf("right %s carries a condition of unknown kind %q", r, c.Kind)
	}
	switch c.Kind {
	case ConditionTerritory, ConditionDeploymentMode:
		if len(c.Values) == 0 {
			return errorf("right %s: a %s condition lists the values it permits", r, c.Kind)
		}
		if c.Kind == ConditionDeploymentMode {
			for _, v := range c.Values {
				if !DeploymentMode(v).Valid() {
					return errorf("right %s: %q is not a deployment mode", r, v)
				}
			}
		}
	case ConditionAttribution, ConditionOther:
		if strings.TrimSpace(c.Text) == "" {
			return errorf("right %s: a %s condition states what it requires", r, c.Kind)
		}
	}
	return nil
}

// Use is an intended use of a source: the rights it needs, where the result
// will run, and which territories it covers.
type Use struct {
	Rights []Right
	Mode   DeploymentMode
	// Territories is the scope the use covers. Empty means unrestricted, which
	// is the widest claim a use can make and the hardest to satisfy.
	Territories []string
}

// Decision is the outcome of evaluating one right for one use.
type Decision struct {
	Right Right
	State State
	// Permitted is the only field a caller may act on. It is false for DENY,
	// UNKNOWN, an absent right, and a CONDITIONAL grant any of whose
	// conditions is unmet or cannot be checked.
	Permitted bool
	// Reason says why, in words a content engineer can act on.
	Reason string
	// Obligations are notices the use must render if it proceeds.
	Obligations []string
}

// Evaluate decides one right for one use. It is total: every input produces a
// decision, and every decision that is not an explicit, satisfied grant is a
// refusal.
func (p RightsProfile) Evaluate(r Right, u Use) Decision {
	g, ok := p.Grants[r]
	if !ok {
		return Decision{Right: r, State: StateUnknown, Reason: "the rights profile is silent on " + string(r) + ", which resolves as DENY"}
	}
	switch g.State {
	case StateAllow:
		return Decision{Right: r, State: g.State, Permitted: true, Reason: "granted"}
	case StateDeny:
		return Decision{Right: r, State: g.State, Reason: "denied by the rights profile"}
	case StateConditional:
		d := Decision{Right: r, State: g.State, Permitted: true, Reason: "every condition is met"}
		for _, c := range g.Conditions {
			met, why := c.metBy(u)
			if !met {
				return Decision{Right: r, State: g.State, Reason: why}
			}
			if c.Kind == ConditionAttribution {
				d.Obligations = append(d.Obligations, c.Text)
			}
		}
		return d
	}
	return Decision{Right: r, State: StateUnknown, Reason: string(r) + " is UNKNOWN, which resolves as DENY until Legal resolves it"}
}

// metBy reports whether a condition is satisfied by a use, and if not, why.
func (c Condition) metBy(u Use) (bool, string) {
	switch c.Kind {
	case ConditionTerritory:
		if len(u.Territories) == 0 {
			return false, fmt.Sprintf("granted only in %s, and the use is not territorially bounded", strings.Join(c.Values, ", "))
		}
		for _, t := range u.Territories {
			if !contains(c.Values, t) {
				return false, fmt.Sprintf("granted only in %s; the use covers %s", strings.Join(c.Values, ", "), t)
			}
		}
		return true, ""
	case ConditionDeploymentMode:
		if !contains(c.Values, string(u.Mode)) {
			return false, fmt.Sprintf("granted only for %s; the use is %s", strings.Join(c.Values, ", "), u.Mode)
		}
		return true, ""
	case ConditionAttribution:
		return true, ""
	}
	return false, "conditional on " + quoteText(c.Text) + ", which a build cannot check"
}

func quoteText(s string) string { return fmt.Sprintf("%q", s) }

func contains(set []string, v string) bool {
	for _, s := range set {
		if s == v {
			return true
		}
	}
	return false
}
