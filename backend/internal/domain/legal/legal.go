// Package legal resolves whether ZoikoTax may perform a legally sensitive
// action, and on what authority (ZTAX-LEG-001 §3, §4, §9).
//
// Two records meet here. The authorization matrix is Zoiko's legal posture:
// for a country, an authority and a service state, whether Zoiko may act
// directly, only with the customer's authorization, only through a qualified
// person or a partner, not at all — or whether counsel has yet to say. It is
// legal content, versioned and effective-dated on its own clock, and every
// rule cites the counsel memo or authority source it rests on
// (ZTAX-LEG-REQ-0009, -0102). A customer authorization is the customer's
// grant — a power of attorney, a filing mandate, a portal delegation — to one
// authority, for named permissions, matters and periods, with its revocation
// and supersession history (ZTAX-LEG-REQ-0014, -0015).
//
// Resolve is the gate. It fails closed: no rule, two rules that disagree, a
// rule counsel has not settled, an authorization that is missing, expired,
// revoked or out of scope — each blocks, and says which
// (ZTAX-LEG-REQ-0001, -0007, -0008, -0016).
package legal

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// ServiceState is what kind of thing an action is (ZTAX-LEG-REQ-0003).
type ServiceState string

// The six service states. They are product capabilities, not tiers: a pack
// may be in production for COMPUTE while FILE needs a partner and REPRESENT is
// prohibited (ZTAX-LEG-REQ-0057).
const (
	ServiceInform    ServiceState = "INFORM"
	ServiceCompute   ServiceState = "COMPUTE"
	ServicePrepare   ServiceState = "PREPARE"
	ServiceFile      ServiceState = "FILE"
	ServiceRepresent ServiceState = "REPRESENT"
	ServiceAdvise    ServiceState = "ADVISE"
)

// Valid reports whether s is a service state.
func (s ServiceState) Valid() bool {
	switch s {
	case ServiceInform, ServiceCompute, ServicePrepare, ServiceFile, ServiceRepresent, ServiceAdvise:
		return true
	}
	return false
}

// Status is a LegalAuthorization state (ZTAX-LEG-001 §3.1).
type Status string

// The eight states.
const (
	StatusDirectAllowed   Status = "DIRECT_ALLOWED"
	StatusDirectWithAuth  Status = "DIRECT_WITH_AUTH"
	StatusQualifiedPerson Status = "QUALIFIED_PERSON_REQUIRED"
	StatusPartnerRequired Status = "PARTNER_REQUIRED"
	StatusCustomerOnly    Status = "CUSTOMER_ONLY"
	StatusCounselPending  Status = "COUNSEL_PENDING"
	StatusProhibited      Status = "PROHIBITED"
	StatusSuspended       Status = "SUSPENDED"
)

// Valid reports whether s is a state.
func (s Status) Valid() bool {
	switch s {
	case StatusDirectAllowed, StatusDirectWithAuth, StatusQualifiedPerson, StatusPartnerRequired,
		StatusCustomerOnly, StatusCounselPending, StatusProhibited, StatusSuspended:
		return true
	}
	return false
}

// FundsPosture is how money moves for the service (ZTAX-LEG-001 §10).
type FundsPosture string

// The funds postures. NO_CUSTODY is the default.
const (
	FundsNoCustody       FundsPosture = "NO_CUSTODY"
	FundsInstructionOnly FundsPosture = "INSTRUCTION_ONLY"
	FundsPSPPartner      FundsPosture = "PSP_PARTNER"
	FundsLicensedProgram FundsPosture = "LICENSED_PROGRAM"
)

// Valid reports whether f is a posture.
func (f FundsPosture) Valid() bool {
	return f == FundsNoCustody || f == FundsInstructionOnly || f == FundsPSPPartner || f == FundsLicensedProgram
}

// AuthorizationType is the instrument a customer grants authority by.
type AuthorizationType string

// The authorization types. TAX_INFORMATION is access to information, and is
// not representation (ZTAX-LEG-REQ-0013).
const (
	AuthNone             AuthorizationType = "NONE"
	AuthContract         AuthorizationType = "CONTRACT"
	AuthDeclaration      AuthorizationType = "DECLARATION"
	AuthPowerOfAttorney  AuthorizationType = "POA"
	AuthTaxInformation   AuthorizationType = "TAX_INFORMATION"
	AuthPortalDelegation AuthorizationType = "PORTAL_DELEGATION"
	AuthFilingMandate    AuthorizationType = "FILING_MANDATE"
)

// Valid reports whether t is a type.
func (t AuthorizationType) Valid() bool {
	switch t {
	case AuthNone, AuthContract, AuthDeclaration, AuthPowerOfAttorney, AuthTaxInformation, AuthPortalDelegation, AuthFilingMandate:
		return true
	}
	return false
}

// Permission is one thing an authorization lets Zoiko do.
type Permission string

// The permissions (ZTAX-LEG-001 §9).
const (
	PermReadInfo      Permission = "READ_INFO"
	PermPrepare       Permission = "PREPARE"
	PermSubmit        Permission = "SUBMIT"
	PermReceiveNotice Permission = "RECEIVE_NOTICE"
	PermRepresent     Permission = "REPRESENT"
	PermSign          Permission = "SIGN"
)

// Valid reports whether p is a permission.
func (p Permission) Valid() bool {
	switch p {
	case PermReadInfo, PermPrepare, PermSubmit, PermReceiveNotice, PermRepresent, PermSign:
		return true
	}
	return false
}

// Needs is the permission a service state requires of an authorization, or
// "" when it requires none. Submission is not representation and preparation
// is not submission: each needs its own (ZTAX-LEG-REQ-0061, -0062).
func (s ServiceState) Needs() Permission {
	switch s {
	case ServiceInform:
		return PermReadInfo
	case ServicePrepare:
		return PermPrepare
	case ServiceFile:
		return PermSubmit
	case ServiceRepresent:
		return PermRepresent
	}
	return ""
}

// ---------------------------------------------------------------------------
// the matrix
// ---------------------------------------------------------------------------

// Rule is one version of one row of the authorization matrix.
type Rule struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
	// Country is ISO 3166-1 alpha-2; Authority is the authority within it.
	// Filing authority is evaluated per authority, not per country
	// (ZTAX-LEG-REQ-0059).
	Country   string       `json:"country"`
	Authority string       `json:"authority"`
	Service   ServiceState `json:"service"`
	Status    Status       `json:"status"`
	// Provider is the Zoiko entity or approved partner that performs it.
	Provider          string            `json:"provider"`
	AuthorizationType AuthorizationType `json:"authorizationType"`
	// RequiresPeriods and RequiresMatters say the authority accepts no
	// generic grant: an authorization must name the periods or matters.
	RequiresPeriods bool         `json:"requiresPeriods,omitempty"`
	RequiresMatters bool         `json:"requiresMatters,omitempty"`
	Qualification   string       `json:"qualification,omitempty"`
	Credential      string       `json:"credential,omitempty"`
	Funds           FundsPosture `json:"funds"`
	// OpinionRef is the counsel memo or authority source the rule rests on
	// (ZTAX-LEG-REQ-0102).
	OpinionRef    string    `json:"opinionRef"`
	EffectiveFrom time.Time `json:"effectiveFrom"`
	EffectiveTo   time.Time `json:"effectiveTo,omitzero"`
}

var countryShape = regexp.MustCompile(`^[A-Z]{2}$`)

// Validate refuses a rule that does not say where, what, how and on whose
// opinion.
func (r Rule) Validate() error {
	switch {
	case strings.TrimSpace(r.ID) == "" || len(r.ID) > 128:
		return fmt.Errorf("legal: a rule has an id of at most 128 characters")
	case r.Version < 1:
		return fmt.Errorf("legal: rule %s version %d", r.ID, r.Version)
	case !countryShape.MatchString(r.Country):
		return fmt.Errorf("legal: rule %s country %q is not ISO 3166-1 alpha-2", r.ID, r.Country)
	case strings.TrimSpace(r.Authority) == "" || len(r.Authority) > 128:
		return fmt.Errorf("legal: rule %s names no authority", r.ID)
	case !r.Service.Valid():
		return fmt.Errorf("legal: rule %s service %q", r.ID, r.Service)
	case !r.Status.Valid():
		return fmt.Errorf("legal: rule %s status %q", r.ID, r.Status)
	case !r.AuthorizationType.Valid():
		return fmt.Errorf("legal: rule %s authorization type %q", r.ID, r.AuthorizationType)
	case r.Status == StatusDirectWithAuth && r.AuthorizationType == AuthNone:
		return fmt.Errorf("legal: rule %s needs an authorization and names no type of one", r.ID)
	case !r.Funds.Valid():
		return fmt.Errorf("legal: rule %s funds posture %q", r.ID, r.Funds)
	case strings.TrimSpace(r.OpinionRef) == "":
		return fmt.Errorf("legal: rule %s cites no counsel memo or authority source", r.ID)
	case r.EffectiveFrom.IsZero():
		return fmt.Errorf("legal: rule %s has no effective date", r.ID)
	case !r.EffectiveTo.IsZero() && !r.EffectiveTo.After(r.EffectiveFrom):
		return fmt.Errorf("legal: rule %s ends before it starts", r.ID)
	}
	return nil
}

// effective reports whether the rule governs at t.
func (r Rule) effective(t time.Time) bool {
	return !t.Before(r.EffectiveFrom) && (r.EffectiveTo.IsZero() || t.Before(r.EffectiveTo))
}

// Matrix is the authorization matrix a cell resolves against.
type Matrix struct {
	// Version names this edition of the matrix.
	Version string `json:"version"`
	// Draft marks a matrix nobody has approved — the development one. A
	// cell outside development refuses it.
	Draft bool   `json:"draft,omitempty"`
	Rules []Rule `json:"rules"`
	// Digest is the canonical digest of the matrix as parsed, recorded with
	// each resolution so the posture it used can be named.
	Digest canonical.Digest `json:"-"`
}

// ParseMatrix reads and validates a matrix document.
func ParseMatrix(b []byte) (Matrix, error) {
	var m Matrix
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return Matrix{}, fmt.Errorf("legal: the matrix does not parse: %w", err)
	}
	if strings.TrimSpace(m.Version) == "" {
		return Matrix{}, fmt.Errorf("legal: the matrix names no version")
	}
	seen := map[string]bool{}
	for _, r := range m.Rules {
		if err := r.Validate(); err != nil {
			return Matrix{}, err
		}
		key := fmt.Sprintf("%s/%d", r.ID, r.Version)
		if seen[key] {
			return Matrix{}, fmt.Errorf("legal: rule %s version %d appears twice", r.ID, r.Version)
		}
		seen[key] = true
	}
	sort.SliceStable(m.Rules, func(i, j int) bool {
		if m.Rules[i].ID != m.Rules[j].ID {
			return m.Rules[i].ID < m.Rules[j].ID
		}
		return m.Rules[i].Version < m.Rules[j].Version
	})
	d, err := m.digest()
	if err != nil {
		return Matrix{}, err
	}
	m.Digest = d
	return m, nil
}

func (m Matrix) digest() (canonical.Digest, error) {
	rules := make([]canonical.Value, len(m.Rules))
	for i, r := range m.Rules {
		fields := []canonical.Field{
			canonical.F("id", canonical.String(r.ID)),
			canonical.F("version", canonical.Integer(int64(r.Version))),
			canonical.F("country", canonical.String(r.Country)),
			canonical.F("authority", canonical.String(r.Authority)),
			canonical.F("service", canonical.String(string(r.Service))),
			canonical.F("status", canonical.String(string(r.Status))),
			canonical.F("provider", canonical.String(r.Provider)),
			canonical.F("authorizationType", canonical.String(string(r.AuthorizationType))),
			canonical.F("requiresPeriods", canonical.Bool(r.RequiresPeriods)),
			canonical.F("requiresMatters", canonical.Bool(r.RequiresMatters)),
			canonical.F("qualification", canonical.String(r.Qualification)),
			canonical.F("credential", canonical.String(r.Credential)),
			canonical.F("funds", canonical.String(string(r.Funds))),
			canonical.F("opinionRef", canonical.String(r.OpinionRef)),
			canonical.F("effectiveFrom", canonical.String(canonical.FormatTime(r.EffectiveFrom))),
		}
		if !r.EffectiveTo.IsZero() {
			fields = append(fields, canonical.F("effectiveTo", canonical.String(canonical.FormatTime(r.EffectiveTo))))
		}
		rules[i] = canonical.Object(fields...)
	}
	d, err := canonical.Sum(canonical.Object(
		canonical.F("artifact", canonical.String("legal-authorization-matrix")),
		canonical.F("version", canonical.String(m.Version)),
		canonical.F("draft", canonical.Bool(m.Draft)),
		canonical.F("rules", canonical.Array(rules...)),
	))
	if err != nil {
		return canonical.Digest{}, fmt.Errorf("legal: the matrix cannot be digested: %w", err)
	}
	return d, nil
}

// Governing returns, for each rule id matching the country, authority and
// service, its latest version in effect at t.
func (m Matrix) Governing(country, authority string, service ServiceState, t time.Time) []Rule {
	latest := map[string]Rule{}
	for _, r := range m.Rules {
		if r.Country != country || r.Authority != authority || r.Service != service || !r.effective(t) {
			continue
		}
		if cur, ok := latest[r.ID]; !ok || r.Version > cur.Version {
			latest[r.ID] = r
		}
	}
	out := make([]Rule, 0, len(latest))
	for _, r := range latest {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ---------------------------------------------------------------------------
// customer authorizations
// ---------------------------------------------------------------------------

// AuthStatus is an authorization's recorded state. Expiry is not recorded;
// it is read off the expiry date at the moment of asking.
type AuthStatus string

// The authorization states.
const (
	AuthActive     AuthStatus = "ACTIVE"
	AuthRevoked    AuthStatus = "REVOKED"
	AuthSuperseded AuthStatus = "SUPERSEDED"
)

// EventKind is what an authorization event did.
type EventKind string

// The event kinds.
const (
	EventGranted    EventKind = "GRANTED"
	EventRevoked    EventKind = "REVOKED"
	EventSuperseded EventKind = "SUPERSEDED"
)

// Event is one entry of an authorization's history.
type Event struct {
	Seq    int
	Kind   EventKind
	Reason string
	// By is, on a SUPERSEDED event, the authorization that replaced it.
	By         id.AuthorizationID
	RecordedAt time.Time
	RecordedBy id.UserID
}

// Authorization is a customer's grant of authority to one authority.
type Authorization struct {
	ID          id.AuthorizationID
	TenantID    id.TenantID
	LegalEntity id.LegalEntityID
	Country     string
	Authority   string
	Type        AuthorizationType
	Permissions []Permission
	// Matters are the forms or matters covered; empty is any.
	Matters []string
	// PeriodFrom and PeriodTo are the legal periods covered, YYYY-MM,
	// inclusive; both empty is any.
	PeriodFrom string
	PeriodTo   string
	// Representative is the individual or entity acting under it.
	Representative string
	EffectiveFrom  time.Time
	// ExpiresAt is when it lapses; zero is never, though most do.
	ExpiresAt time.Time
	// Evidence names the signed artifact, consent proof or authority
	// acknowledgement — references, never the documents.
	Evidence []string
	// CredentialRef points at the authority credential in the secrets
	// vault. The credential itself is never in this record
	// (ZTAX-LEG-REQ-0017, -0018).
	CredentialRef string
	Supersedes    id.AuthorizationID
	RecordedAt    time.Time
	RecordedBy    id.UserID
	Status        AuthStatus
	History       []Event
}

var (
	periodShape = regexp.MustCompile(`^[0-9]{4}-(0[1-9]|1[0-2])$`)
	// credentialRef is a reference into a secrets store: a scheme the cell
	// knows and a path. A value that is not one is refused, so a pasted
	// password or certificate never lands in a domain record.
	credentialRef = regexp.MustCompile(`^(vault|awssm|gcpsm|azkv)://[A-Za-z0-9._/-]{1,240}$`)
)

// Validate refuses a grant that does not say who, to whom, for what and
// when.
func (a Authorization) Validate() error {
	switch {
	case a.LegalEntity.IsZero():
		return fmt.Errorf("legal: an authorization names the legal entity granting it")
	case !countryShape.MatchString(a.Country):
		return fmt.Errorf("legal: an authorization's country is ISO 3166-1 alpha-2")
	case strings.TrimSpace(a.Authority) == "" || len(a.Authority) > 128:
		return fmt.Errorf("legal: an authorization names the authority it is granted to")
	case !a.Type.Valid() || a.Type == AuthNone:
		return fmt.Errorf("legal: authorization type %q", a.Type)
	case len(a.Permissions) == 0:
		return fmt.Errorf("legal: an authorization grants at least one permission")
	case a.EffectiveFrom.IsZero():
		return fmt.Errorf("legal: an authorization has an effective date")
	case !a.ExpiresAt.IsZero() && !a.ExpiresAt.After(a.EffectiveFrom):
		return fmt.Errorf("legal: an authorization expires after it takes effect")
	case len(a.Evidence) == 0:
		// A grant nobody can produce is not a grant (ZTAX-LEG-001 §9).
		return fmt.Errorf("legal: an authorization names the evidence of the grant")
	case a.CredentialRef != "" && !credentialRef.MatchString(a.CredentialRef):
		return fmt.Errorf("legal: a credential is a vault reference (vault://, awssm://, gcpsm://, azkv://), never the credential")
	case len(a.Representative) > 255:
		return fmt.Errorf("legal: a representative is at most 255 characters")
	}
	for _, p := range a.Permissions {
		if !p.Valid() {
			return fmt.Errorf("legal: permission %q", p)
		}
	}
	if a.Type == AuthTaxInformation && (slices.Contains(a.Permissions, PermRepresent) || slices.Contains(a.Permissions, PermSubmit)) {
		// Access to information is not authority to act (ZTAX-LEG-REQ-0013).
		return fmt.Errorf("legal: a tax-information authorization grants no submission or representation")
	}
	if (a.PeriodFrom == "") != (a.PeriodTo == "") {
		return fmt.Errorf("legal: an authorization's periods have a start and an end")
	}
	if a.PeriodFrom != "" && (!periodShape.MatchString(a.PeriodFrom) || !periodShape.MatchString(a.PeriodTo) || a.PeriodTo < a.PeriodFrom) {
		return fmt.Errorf("legal: an authorization's periods are YYYY-MM, the end not before the start")
	}
	if len(a.Matters) > 100 || len(a.Evidence) > 20 {
		return fmt.Errorf("legal: at most 100 matters and 20 evidence references")
	}
	for _, s := range append(slices.Clone(a.Matters), a.Evidence...) {
		if strings.TrimSpace(s) == "" || len(s) > 255 {
			return fmt.Errorf("legal: a matter or evidence reference is between 1 and 255 characters")
		}
	}
	return nil
}

// Fold sets an authorization's status from its history.
func Fold(a Authorization, events []Event) (Authorization, error) {
	a.History = events
	for i, e := range events {
		if e.Seq != i+1 {
			return Authorization{}, fmt.Errorf("legal: authorization %s event %d has sequence %d", a.ID, i+1, e.Seq)
		}
		switch e.Kind {
		case EventGranted:
			if i != 0 {
				return Authorization{}, fmt.Errorf("legal: authorization %s is granted twice", a.ID)
			}
			a.Status = AuthActive
		case EventRevoked, EventSuperseded:
			if a.Status != AuthActive {
				return Authorization{}, fmt.Errorf("legal: authorization %s is %s while %s", a.ID, e.Kind, a.Status)
			}
			a.Status = AuthRevoked
			if e.Kind == EventSuperseded {
				a.Status = AuthSuperseded
			}
		default:
			return Authorization{}, fmt.Errorf("legal: authorization %s event kind %q", a.ID, e.Kind)
		}
	}
	if len(events) == 0 {
		return Authorization{}, fmt.Errorf("legal: authorization %s has no history", a.ID)
	}
	return a, nil
}

// InForce reports whether the authorization can be relied on at t: active,
// effective, not expired.
func (a Authorization) InForce(t time.Time) bool {
	return a.Status == AuthActive && !t.Before(a.EffectiveFrom) && (a.ExpiresAt.IsZero() || t.Before(a.ExpiresAt))
}

// Expired reports whether it has lapsed by t.
func (a Authorization) Expired(t time.Time) bool {
	return !a.ExpiresAt.IsZero() && !t.Before(a.ExpiresAt)
}

// covers reports whether the grant's matters and periods reach the action,
// given what the rule demands of them.
func (a Authorization) covers(act Action, r Rule) bool {
	switch {
	case act.Period == "" && r.RequiresPeriods,
		act.Matter == "" && r.RequiresMatters:
		// An action that does not say its period or matter cannot be
		// checked against an authority that demands them.
		return false
	case act.Period != "" && a.PeriodFrom == "" && r.RequiresPeriods,
		act.Matter != "" && len(a.Matters) == 0 && r.RequiresMatters:
		return false
	case act.Period != "" && a.PeriodFrom != "" && (act.Period < a.PeriodFrom || act.Period > a.PeriodTo):
		return false
	case act.Matter != "" && len(a.Matters) > 0 && !slices.Contains(a.Matters, act.Matter):
		return false
	}
	return true
}

// ---------------------------------------------------------------------------
// the gate
// ---------------------------------------------------------------------------

// Action is what somebody means to do.
type Action struct {
	Country     string
	Authority   string
	Service     ServiceState
	LegalEntity id.LegalEntityID
	// Period and Matter are the legal period (YYYY-MM) and form or matter
	// the action concerns, when it concerns one (ZTAX-LEG-REQ-0101).
	Period string
	Matter string
}

// Reason is why a resolution blocks, or "" when it allows.
type Reason string

// The reasons a resolution blocks.
const (
	ReasonNoRule              Reason = "NO_RULE"
	ReasonRuleConflict        Reason = "RULE_CONFLICT"
	ReasonCounselPending      Reason = "COUNSEL_PENDING"
	ReasonProhibited          Reason = "PROHIBITED"
	ReasonSuspended           Reason = "SUSPENDED"
	ReasonPartnerRequired     Reason = "PARTNER_REQUIRED"
	ReasonCustomerOnly        Reason = "CUSTOMER_ONLY"
	ReasonQualifiedPerson     Reason = "QUALIFIED_PERSON_REQUIRED"
	ReasonAuthMissing         Reason = "AUTHORIZATION_MISSING"
	ReasonAuthExpired         Reason = "AUTHORIZATION_EXPIRED"
	ReasonAuthRevoked         Reason = "AUTHORIZATION_REVOKED"
	ReasonAuthOutOfScope      Reason = "AUTHORIZATION_OUT_OF_SCOPE"
	ReasonCredentialMissing   Reason = "CREDENTIAL_MISSING" //nolint:gosec // G101: a reason code naming an absence, not a credential
	ReasonUnsupportedMatrix   Reason = "MATRIX_UNAVAILABLE"
	ReasonActionNotWellFormed Reason = "ACTION_NOT_WELL_FORMED"
)

// Resolution is the gate's answer, with what it rested on.
type Resolution struct {
	Allowed bool
	// Status is the matrix's state for the action; COUNSEL_PENDING when no
	// rule, or no single rule, governs it.
	Status        Status
	Reason        Reason
	Rule          *Rule
	Authorization id.AuthorizationID
	MatrixVersion string
	MatrixDigest  canonical.Digest
	Detail        string
}

// Resolve decides whether the action may proceed at now, under the matrix
// and the customer's authorizations as they stand.
func Resolve(m Matrix, auths []Authorization, act Action, now time.Time) Resolution {
	res := Resolution{Status: StatusCounselPending, MatrixVersion: m.Version, MatrixDigest: m.Digest}
	block := func(r Reason, detail string) Resolution {
		res.Allowed, res.Reason, res.Detail = false, r, detail
		return res
	}
	if !countryShape.MatchString(act.Country) || strings.TrimSpace(act.Authority) == "" || !act.Service.Valid() ||
		(act.Period != "" && !periodShape.MatchString(act.Period)) {
		return block(ReasonActionNotWellFormed, "An action names its country, authority and service state, and its period as YYYY-MM.")
	}
	if m.Version == "" {
		return block(ReasonUnsupportedMatrix, "No authorization matrix is loaded; every legally sensitive action is blocked.")
	}
	rules := m.Governing(act.Country, act.Authority, act.Service, now)
	switch {
	case len(rules) == 0:
		return block(ReasonNoRule, fmt.Sprintf("No rule governs %s before %s %s; an unsupported posture fails closed.", act.Service, act.Country, act.Authority))
	case len(rules) > 1:
		for _, r := range rules[1:] {
			if r.Status != rules[0].Status || r.AuthorizationType != rules[0].AuthorizationType {
				return block(ReasonRuleConflict, fmt.Sprintf("%d rules govern %s before %s %s and disagree; counsel resolves it.", len(rules), act.Service, act.Country, act.Authority))
			}
		}
	}
	rule := rules[0]
	res.Rule, res.Status = &rule, rule.Status
	switch rule.Status {
	case StatusDirectAllowed:
		res.Allowed = true
		return res
	case StatusCounselPending:
		return block(ReasonCounselPending, "Counsel has not settled this posture; the action is blocked until it does (ZTAX-LEG-REQ-0007).")
	case StatusProhibited:
		return block(ReasonProhibited, "ZoikoTax must not perform this action.")
	case StatusSuspended:
		return block(ReasonSuspended, "This posture is suspended.")
	case StatusPartnerRequired:
		return block(ReasonPartnerRequired, "A licensed or qualified partner performs this action, not ZoikoTax.")
	case StatusCustomerOnly:
		return block(ReasonCustomerOnly, "ZoikoTax may prepare and assist; the customer executes.")
	case StatusQualifiedPerson:
		return block(ReasonQualifiedPerson, "An eligible qualified professional performs or approves this action: "+rule.Qualification)
	}

	// DIRECT_WITH_AUTH: a customer authorization in force, of the type the
	// rule names, granting what the service needs, reaching the action's
	// period and matter, bound to a credential if the authority needs one.
	need := act.Service.Needs()
	best := ReasonAuthMissing
	rank := map[Reason]int{ReasonAuthMissing: 0, ReasonAuthRevoked: 1, ReasonAuthExpired: 2, ReasonAuthOutOfScope: 3, ReasonCredentialMissing: 4}
	note := func(r Reason) {
		if rank[r] > rank[best] {
			best = r
		}
	}
	for _, a := range auths {
		if a.LegalEntity != act.LegalEntity || a.Country != act.Country || a.Authority != act.Authority || a.Type != rule.AuthorizationType {
			continue
		}
		if need != "" && !slices.Contains(a.Permissions, need) {
			continue
		}
		switch {
		case a.Status != AuthActive:
			note(ReasonAuthRevoked)
		case a.Expired(now) || now.Before(a.EffectiveFrom):
			note(ReasonAuthExpired)
		case !a.covers(act, rule):
			note(ReasonAuthOutOfScope)
		case rule.Credential != "" && a.CredentialRef == "":
			note(ReasonCredentialMissing)
		default:
			res.Allowed, res.Authorization = true, a.ID
			return res
		}
	}
	details := map[Reason]string{
		ReasonAuthMissing:       fmt.Sprintf("No %s authorization granting %s before %s %s is on record for the legal entity.", rule.AuthorizationType, need, act.Country, act.Authority),
		ReasonAuthRevoked:       "The authorization that granted this has been revoked or superseded (ZTAX-LEG-REQ-0016).",
		ReasonAuthExpired:       "The authorization that granted this has expired, or is not yet in effect (ZTAX-LEG-REQ-0016).",
		ReasonAuthOutOfScope:    "No authorization reaches this period or matter (ZTAX-LEG-REQ-0101).",
		ReasonCredentialMissing: "The authority needs a credential, and the authorization binds none: " + rule.Credential,
	}
	return block(best, details[best])
}
