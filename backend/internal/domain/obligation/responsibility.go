package obligation

import (
	"fmt"
	"sort"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// Role is one of the five responsibility roles (ZTAX-OBL-REQ-0002 to -0006).
//
// They are never collapsed into one "taxpayer" field. Who bears a tax, who
// collects it, who reports it, who remits it and who economically pays it are
// five questions with five answers that usually coincide and routinely do not
// — a marketplace collects what a seller owes, a reseller reports what an
// end user bears — and the obligation that follows depends on which.
type Role string

// The roles.
const (
	RoleIncidence      Role = "INCIDENCE"
	RoleCollection     Role = "COLLECTION"
	RoleReporting      Role = "REPORTING"
	RoleRemittance     Role = "REMITTANCE"
	RoleEconomicBearer Role = "ECONOMIC_BEARER"
)

// Roles is every role, in a fixed order.
var Roles = []Role{RoleIncidence, RoleCollection, RoleReporting, RoleRemittance, RoleEconomicBearer}

// PartyKind names a party's position in the transaction as a fact, not as a
// role: the seller is a fact, "the party who remits" is a decision.
type PartyKind string

// The party kinds a responsibility rule can select. Wholesale, reseller and
// end-user relationships are explicit kinds rather than inferred from a
// buyer's attributes (ZTAX-OBL-REQ-0020).
const (
	PartySeller      PartyKind = "SELLER"
	PartyBuyer       PartyKind = "BUYER"
	PartyMarketplace PartyKind = "MARKETPLACE"
	PartyWholesaler  PartyKind = "WHOLESALER"
	PartyReseller    PartyKind = "RESELLER"
	PartyEndUser     PartyKind = "END_USER"
)

// Party is one party to the transaction, as a fact the caller supplies.
type Party struct {
	Kind PartyKind
	// LegalEntity is set when the party is one of the tenant's own legal
	// entities. A counterparty outside the tenant is named by Ref alone.
	LegalEntity id.LegalEntityID
	Ref         string
}

func (p Party) key() string {
	if !p.LegalEntity.IsZero() {
		return "entity:" + p.LegalEntity.String()
	}
	return "ref:" + p.Ref
}

// ResponsibilityStatus is the decision's outcome (ZTAX-OBL-REQ-0013 to -0016).
type ResponsibilityStatus string

// The statuses.
const (
	ResponsibilityResolved    ResponsibilityStatus = "RESOLVED"
	ResponsibilityAmbiguous   ResponsibilityStatus = "AMBIGUOUS"
	ResponsibilityConflicted  ResponsibilityStatus = "CONFLICTED"
	ResponsibilityUnsupported ResponsibilityStatus = "UNSUPPORTED"
)

// RoleRule is content's answer to one role: the party kinds that may hold it,
// in precedence order, and whether the law lets that role be delegated.
type RoleRule struct {
	Role Role
	// Candidates are tried in order; the first kind present among the
	// transaction's parties holds the role.
	Candidates []PartyKind
	// DelegationPermitted says the law allows an agent to perform this role
	// on the statutory party's behalf. Without it, tenant configuration cannot
	// name an agent for the role (ZTAX-OBL-REQ-0008).
	DelegationPermitted bool
}

// ResponsibilityRule is the content rule for one obligation definition. It
// carries its own provenance, which every decision made under it pins
// (ZTAX-OBL-REQ-0012).
type ResponsibilityRule struct {
	ID         string
	Version    string
	Provenance ContentRef
	Roles      []RoleRule
}

// ContentRef pins the content a decision was made under.
type ContentRef struct {
	BundleID     string
	BundleDigest string
}

// Validate refuses a ContentRef that pins nothing.
func (c ContentRef) Validate() error {
	if c.BundleID == "" || c.BundleDigest == "" {
		return fmt.Errorf("obligation: content provenance names no bundle or no digest")
	}
	return nil
}

// Delegation is tenant configuration naming an agent to perform a role. An
// agent performs; it does not become the statutory party
// (ZTAX-OBL-REQ-0009).
type Delegation struct {
	Role  Role
	Agent Party
}

// Assignment is one role's answer.
type Assignment struct {
	Role   Role
	Status ResponsibilityStatus
	// Statutory is the party the law places the role on. Nil unless RESOLVED.
	Statutory *Party
	// Agent performs the role on the statutory party's behalf, where the law
	// permits delegation. It never replaces Statutory.
	Agent *Party
	// Detail says why a role did not resolve.
	Detail string
}

// ResponsibilityDecision is the record of who holds each role for one
// obligation (ZTAX-OBL-REQ-0001). It is separate from the ObligationDecision
// that references it, scoped to a tenant and legal entity
// (ZTAX-OBL-REQ-0010), effective-dated (ZTAX-OBL-REQ-0011) and pinned to the
// content that made it (ZTAX-OBL-REQ-0012).
type ResponsibilityDecision struct {
	ID          id.ResponsibilityDecisionID
	TenantID    id.TenantID
	LegalEntity id.LegalEntityID
	RuleID      string
	RuleVersion string
	Provenance  ContentRef
	// EffectiveFrom is the event time the decision answers for.
	EffectiveFrom time.Time
	Status        ResponsibilityStatus
	// Assignments holds every role, separately, in Roles order
	// (ZTAX-OBL-REQ-0007): one party holding three roles is three
	// assignments naming it.
	Assignments []Assignment
}

// Assignment returns the assignment for one role.
func (d ResponsibilityDecision) Assignment(r Role) (Assignment, bool) {
	for _, a := range d.Assignments {
		if a.Role == r {
			return a, true
		}
	}
	return Assignment{}, false
}

// ResponsibilityInput is what a decision is made from.
type ResponsibilityInput struct {
	ID            id.ResponsibilityDecisionID
	TenantID      id.TenantID
	LegalEntity   id.LegalEntityID
	EffectiveFrom time.Time
	Parties       []Party
	Delegations   []Delegation
}

// DecideResponsibility applies a rule to the transaction's parties.
//
// Every role is resolved independently. A role the rule does not address is
// UNSUPPORTED; a role whose candidates are all absent is AMBIGUOUS; a role
// whose first present candidate kind is held by two different parties is
// CONFLICTED. None of them falls back to the seller, the provider or the
// customer (ZTAX-OBL-REQ-0017): the absence of an answer is recorded as such.
// The decision's own status is the worst of its roles'.
func DecideResponsibility(rule ResponsibilityRule, in ResponsibilityInput) (ResponsibilityDecision, error) {
	if err := validateRule(rule); err != nil {
		return ResponsibilityDecision{}, err
	}
	if in.TenantID.IsZero() || in.LegalEntity.IsZero() {
		return ResponsibilityDecision{}, fmt.Errorf("obligation: a responsibility decision is scoped to a tenant and a legal entity")
	}
	if in.EffectiveFrom.IsZero() {
		return ResponsibilityDecision{}, fmt.Errorf("obligation: a responsibility decision is effective-dated")
	}
	byKind := map[PartyKind][]Party{}
	for _, p := range in.Parties {
		byKind[p.Kind] = append(byKind[p.Kind], p)
	}
	rules := map[Role]RoleRule{}
	for _, rr := range rule.Roles {
		rules[rr.Role] = rr
	}
	delegated := map[Role]Party{}
	for _, d := range in.Delegations {
		rr, ok := rules[d.Role]
		if !ok || !rr.DelegationPermitted {
			return ResponsibilityDecision{}, fmt.Errorf("obligation: configuration delegates %s, which rule %s@%s does not permit to be delegated",
				d.Role, rule.ID, rule.Version)
		}
		delegated[d.Role] = d.Agent
	}

	out := ResponsibilityDecision{
		ID: in.ID, TenantID: in.TenantID, LegalEntity: in.LegalEntity,
		RuleID: rule.ID, RuleVersion: rule.Version, Provenance: rule.Provenance,
		EffectiveFrom: in.EffectiveFrom.UTC(), Status: ResponsibilityResolved,
	}
	for _, role := range Roles {
		a := Assignment{Role: role}
		rr, ok := rules[role]
		switch {
		case !ok:
			a.Status, a.Detail = ResponsibilityUnsupported, "the rule does not address this role"
		default:
			a = resolveRole(rr, byKind)
		}
		if a.Status == ResponsibilityResolved {
			if agent, ok := delegated[role]; ok {
				agent := agent
				a.Agent = &agent
			}
		}
		out.Assignments = append(out.Assignments, a)
		out.Status = worse(out.Status, a.Status)
	}
	return out, nil
}

func resolveRole(rr RoleRule, byKind map[PartyKind][]Party) Assignment {
	a := Assignment{Role: rr.Role}
	for _, kind := range rr.Candidates {
		parties := byKind[kind]
		if len(parties) == 0 {
			continue
		}
		distinct := map[string]Party{}
		for _, p := range parties {
			distinct[p.key()] = p
		}
		if len(distinct) > 1 {
			keys := make([]string, 0, len(distinct))
			for k := range distinct {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			a.Status = ResponsibilityConflicted
			a.Detail = fmt.Sprintf("%d %s parties hold the role: %v", len(distinct), kind, keys)
			return a
		}
		p := parties[0]
		a.Status, a.Statutory = ResponsibilityResolved, &p
		return a
	}
	a.Status = ResponsibilityAmbiguous
	a.Detail = fmt.Sprintf("none of %v is a party to the transaction", rr.Candidates)
	return a
}

var statusRank = map[ResponsibilityStatus]int{
	ResponsibilityResolved: 0, ResponsibilityAmbiguous: 1, ResponsibilityConflicted: 2, ResponsibilityUnsupported: 3,
}

func worse(a, b ResponsibilityStatus) ResponsibilityStatus {
	if statusRank[b] > statusRank[a] {
		return b
	}
	return a
}

func validateRule(rule ResponsibilityRule) error {
	if rule.ID == "" || rule.Version == "" {
		return fmt.Errorf("obligation: responsibility rule has no id or no version")
	}
	if err := rule.Provenance.Validate(); err != nil {
		return err
	}
	seen := map[Role]bool{}
	for _, rr := range rule.Roles {
		known := false
		for _, r := range Roles {
			if r == rr.Role {
				known = true
			}
		}
		if !known {
			return fmt.Errorf("obligation: rule %s addresses role %q", rule.ID, rr.Role)
		}
		if seen[rr.Role] {
			return fmt.Errorf("obligation: rule %s addresses %s twice", rule.ID, rr.Role)
		}
		seen[rr.Role] = true
		if len(rr.Candidates) == 0 {
			return fmt.Errorf("obligation: rule %s names no candidate for %s", rule.ID, rr.Role)
		}
		if rr.DelegationPermitted && (rr.Role == RoleIncidence || rr.Role == RoleEconomicBearer) {
			// Incidence and economic burden are facts about who owes and who
			// pays; an agent can perform an act, not bear a liability.
			return fmt.Errorf("obligation: rule %s permits delegating %s, which is not an act an agent can perform", rule.ID, rr.Role)
		}
	}
	return nil
}

// Canonical renders the decision for evidence.
func (d ResponsibilityDecision) Canonical() canonical.Value {
	party := func(p *Party) canonical.Value {
		if p == nil {
			return canonical.Absent()
		}
		entity := ""
		if !p.LegalEntity.IsZero() {
			entity = p.LegalEntity.String()
		}
		return canonical.Object(
			canonical.F("kind", canonical.String(string(p.Kind))),
			canonical.F("legalEntity", canonical.OptString(entity)),
			canonical.F("ref", canonical.OptString(p.Ref)),
		)
	}
	as := make([]canonical.Value, len(d.Assignments))
	for i, a := range d.Assignments {
		as[i] = canonical.Object(
			canonical.F("role", canonical.String(string(a.Role))),
			canonical.F("status", canonical.String(string(a.Status))),
			canonical.F("statutory", party(a.Statutory)),
			canonical.F("agent", party(a.Agent)),
			canonical.F("detail", canonical.OptString(a.Detail)),
		)
	}
	return canonical.Object(
		canonical.F("legalEntity", canonical.String(d.LegalEntity.String())),
		canonical.F("rule", canonical.String(d.RuleID)),
		canonical.F("ruleVersion", canonical.String(d.RuleVersion)),
		canonical.F("bundleId", canonical.String(d.Provenance.BundleID)),
		canonical.F("bundleDigest", canonical.String(d.Provenance.BundleDigest)),
		canonical.F("effectiveFrom", canonical.Time(d.EffectiveFrom)),
		canonical.F("status", canonical.String(string(d.Status))),
		canonical.F("assignments", canonical.Array(as...)),
	)
}
