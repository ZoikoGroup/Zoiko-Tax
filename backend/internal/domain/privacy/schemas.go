package privacy

import (
	"errors"
	"fmt"
	"slices"
	"time"
)

// The PRIV-001 record schemas: RetentionPolicy, ProcessingActivity,
// PrivacyProfile and TransferProfile (PRIV-001 §33's first P0 backlog item).
//
// These are the shapes of governance records, not of customer data. They are
// authored and approved by Privacy, Legal and Product, and the platform reads
// them to decide what it may do — so their validation is the platform's
// statement of what a record must say before it can be relied on. Each Validate
// cites the requirement it discharges.
//
// The closed vocabularies they use (role, legal basis, DPIA status, collection
// source, transfer mechanism) live in contracts/privacy/vocabulary.json beside
// the classes and purposes, for the same reason those do: one source, shared
// with the contract lint, and a value added in one place only fails CI.
//
// What is deliberately open: data-subject categories ("subscribers, customer
// employees, sole traders, contacts, workforce, etc." — §5's own list ends in
// etc.), recipients, regions and profile references. Those are names in other
// registers, and closing them here would make this package the register.

// ErrInvalidRecord is the sentinel every schema validation wraps.
var ErrInvalidRecord = errors.New("privacy: invalid governance record")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrInvalidRecord}, args...)...)
}

// ProcessingRole is PRIV-001 §3's processing role.
type ProcessingRole string

// The roles.
const (
	RoleProcessor             ProcessingRole = "PROCESSOR"
	RoleController            ProcessingRole = "CONTROLLER"
	RoleIndependentController ProcessingRole = "INDEPENDENT_CONTROLLER"
	RoleJointController       ProcessingRole = "JOINT_CONTROLLER"
	RoleSubprocessor          ProcessingRole = "SUBPROCESSOR"
	RoleRecipientAuthority    ProcessingRole = "RECIPIENT_AUTHORITY"
)

// ProcessingRoles is the closed set.
var ProcessingRoles = []ProcessingRole{
	RoleProcessor, RoleController, RoleIndependentController,
	RoleJointController, RoleSubprocessor, RoleRecipientAuthority,
}

// LegalBasisCategory is PRIV-001 §11's basis category.
type LegalBasisCategory string

// The categories.
const (
	BasisContract            LegalBasisCategory = "CONTRACT"
	BasisLegalObligation     LegalBasisCategory = "LEGAL_OBLIGATION"
	BasisLegitimateInterests LegalBasisCategory = "LEGITIMATE_INTERESTS"
	BasisConsent             LegalBasisCategory = "CONSENT"
	BasisPublicTask          LegalBasisCategory = "PUBLIC_TASK"
	BasisCustomerInstruction LegalBasisCategory = "CUSTOMER_INSTRUCTION"
	BasisLocalStatutory      LegalBasisCategory = "LOCAL_STATUTORY"
)

// LegalBases is the closed set.
var LegalBases = []LegalBasisCategory{
	BasisContract, BasisLegalObligation, BasisLegitimateInterests, BasisConsent,
	BasisPublicTask, BasisCustomerInstruction, BasisLocalStatutory,
}

// LegalBasisRef is PRIV-001 §11's LegalBasisRef: "the platform records a
// LegalBasisRef, not a universal hard-coded 'GDPR consent' field." The category
// is from the closed set; the reference names the jurisdiction PrivacyProfile
// provision, or the customer instruction, that grounds it. Both are required —
// a category with no reference is the "free-text ungoverned basis" §11 forbids,
// arrived at from the other direction.
type LegalBasisRef struct {
	Category  LegalBasisCategory
	Reference string
}

// Validate requires both halves.
func (b LegalBasisRef) Validate() error {
	if !slices.Contains(LegalBases, b.Category) {
		return invalid("legal basis %q is not a PRIV-001 §11 category", b.Category)
	}
	if b.Reference == "" {
		return invalid("legal basis %s names no profile provision or customer instruction", b.Category)
	}
	return nil
}

// DPIAStatus is PRIV-001 §5's DPIA_status.
type DPIAStatus string

// The statuses.
const (
	DPIANotRequired    DPIAStatus = "NOT_REQUIRED"
	DPIAScreenRequired DPIAStatus = "SCREEN_REQUIRED"
	DPIARequired       DPIAStatus = "REQUIRED"
	DPIAApproved       DPIAStatus = "APPROVED"
	DPIAReviewDue      DPIAStatus = "REVIEW_DUE"
)

// DPIAStatuses is the closed set.
var DPIAStatuses = []DPIAStatus{DPIANotRequired, DPIAScreenRequired, DPIARequired, DPIAApproved, DPIAReviewDue}

// CollectionSource is PRIV-001 §5's collection_source.
type CollectionSource string

// The sources.
const (
	SourceCustomer    CollectionSource = "CUSTOMER"
	SourceAuthority   CollectionSource = "AUTHORITY"
	SourceSubscriber  CollectionSource = "SUBSCRIBER"
	SourceNetwork     CollectionSource = "NETWORK"
	SourceIntegration CollectionSource = "INTEGRATION"
	SourceDerived     CollectionSource = "DERIVED"
)

// CollectionSources is the closed set.
var CollectionSources = []CollectionSource{
	SourceCustomer, SourceAuthority, SourceSubscriber, SourceNetwork, SourceIntegration, SourceDerived,
}

// TransferMechanism is the PRIV-001 §16 mechanism a TransferProfile relies on.
//
// §16 names the mechanisms per jurisdiction (GDPR Chapter V, UK GDPR, DPDP,
// LGPD, APP 8) without fixing a cross-jurisdiction vocabulary; this set is the
// smallest one each of those maps onto. NOT_RESTRICTED is a mechanism rather
// than an absence on purpose: a movement the legal profile does not treat as a
// restricted transfer — two cells in one country — is still a cross-cell copy
// and is still recorded (SEC-REQ-0042), and "no mechanism needed" has to be a
// recorded conclusion rather than a missing field.
type TransferMechanism string

// The mechanisms.
const (
	MechanismNotRestricted         TransferMechanism = "NOT_RESTRICTED"
	MechanismAdequacy              TransferMechanism = "ADEQUACY"
	MechanismContractualSafeguards TransferMechanism = "CONTRACTUAL_SAFEGUARDS"
	MechanismBindingCorporateRules TransferMechanism = "BINDING_CORPORATE_RULES"
	MechanismDerogation            TransferMechanism = "DEROGATION"
	MechanismLocalStatutory        TransferMechanism = "LOCAL_STATUTORY"
)

// TransferMechanisms is the closed set.
var TransferMechanisms = []TransferMechanism{
	MechanismNotRestricted, MechanismAdequacy, MechanismContractualSafeguards,
	MechanismBindingCorporateRules, MechanismDerogation, MechanismLocalStatutory,
}

// ---------------------------------------------------------------------------
// RetentionPolicy
// ---------------------------------------------------------------------------

// RetentionPolicy is the record a ProcessingActivity's retention_policy_id
// names: "rule set, trigger and exceptions" (PRIV-001 §5).
//
// Retention (the RET-* family) says what kind of policy applies, and is what a
// field's metadata carries. This says what the policy actually is. Neither
// holds a number of years, and that is §14 rather than an omission: the period
// is decided by jurisdiction, purpose, contract, evidence need and legal hold,
// and is resolved from the pack or legal profile the Rule names.
type RetentionPolicy struct {
	ID     string
	Family Retention
	// Trigger is the event from which retention runs — session end, account
	// closure, filing period close, hold release.
	Trigger string
	// Rule names where the period comes from: a pack's record-retention rule,
	// a contract clause, an EVID replay window.
	Rule string
	// Exceptions are the recorded conditions that suspend disposal. Legal
	// hold always does, whether or not it is listed (§14, §25 HOLD).
	Exceptions []string
}

// Validate requires a policy that a disposal job could act on.
func (p RetentionPolicy) Validate() error {
	switch {
	case p.ID == "":
		return invalid("retention policy has no id")
	case !slices.Contains(Retentions, p.Family):
		return invalid("retention policy %s: family %q is not a §14 policy family", p.ID, p.Family)
	case p.Trigger == "":
		return invalid("retention policy %s names no trigger", p.ID)
	case p.Rule == "" && p.Family != RetentionTransient && p.Family != RetentionSession:
		// Transient and session retention end with the processing that
		// created them; every other family has a period somebody decided,
		// and the record must say who.
		return invalid("retention policy %s (%s) names no rule its period comes from", p.ID, p.Family)
	}
	return nil
}

// ---------------------------------------------------------------------------
// ProcessingActivity
// ---------------------------------------------------------------------------

// Regions are PRIV-001 §5's regions: "collection, processing, storage, backup
// and support locations." Each is a list of region identifiers as ZTAX_REGION
// spells them.
type Regions struct {
	Collection []string
	Processing []string
	Storage    []string
	Backup     []string
	Support    []string
}

// ProcessingActivity is one PRIV-001 §5 registry entry.
//
// "Every ProcessingActivity record must state role, customer relationship,
// purpose, categories, subjects, recipients, regions, retention, security
// class and legal-basis reference" (§3), and G-PRIV-02 makes the registry
// cover every production processing purpose and role.
type ProcessingActivity struct {
	ID      string
	Purpose Purpose
	Role    ProcessingRole
	// CustomerScope is the tenants or customers affected — "all tenants of
	// the cell", or a named set.
	CustomerScope      string
	DataSubjects       []string
	DataCategories     []Class
	CollectionSources  []CollectionSource
	LegalBasis         LegalBasisRef
	NecessityRationale string
	Recipients         []string
	Regions            Regions
	// TransferProfileRef names the TransferProfile, where a cross-border
	// mechanism is required. Empty where none is.
	TransferProfileRef string
	RetentionPolicyID  string
	DPIA               DPIAStatus
	// AutomationProfile records AI, profiling or automated-decision
	// involvement; empty means none. An AI use case additionally needs its
	// AIUseCase record (§18), which is AIGOV's register, not this one.
	AutomationProfile string
	SecurityProfile   string
	RightsProfile     string
	Owner             string
	ReviewDue         time.Time
}

// Validate applies PRIV-REQ-0004 to -0007 and §7's P6 rule.
func (a ProcessingActivity) Validate() error {
	if a.ID == "" {
		return invalid("processing activity has no id")
	}
	if !slices.Contains(Purposes, a.Purpose) {
		return invalid("activity %s: purpose %q is not an approved purpose", a.ID, a.Purpose)
	}
	if !slices.Contains(ProcessingRoles, a.Role) {
		// PRIV-REQ-0004: the record identifies Zoiko's role.
		return invalid("activity %s: role %q is not a §3 processing role", a.ID, a.Role)
	}
	if a.CustomerScope == "" {
		return invalid("activity %s states no customer scope", a.ID)
	}
	if len(a.DataSubjects) == 0 || slices.Contains(a.DataSubjects, "") {
		// PRIV-REQ-0005.
		return invalid("activity %s identifies no data-subject categories", a.ID)
	}
	if len(a.Recipients) == 0 || slices.Contains(a.Recipients, "") {
		// PRIV-REQ-0005. Processing with no recipient at all — not even the
		// customer's own users — is processing for nobody.
		return invalid("activity %s identifies no recipients", a.ID)
	}
	if len(a.DataCategories) == 0 {
		return invalid("activity %s names no data categories", a.ID)
	}
	for _, c := range a.DataCategories {
		if !c.Valid() {
			return invalid("activity %s: %q is not a PRIV-001 class", a.ID, c)
		}
	}
	if len(a.CollectionSources) == 0 {
		return invalid("activity %s names no collection source", a.ID)
	}
	for _, s := range a.CollectionSources {
		if !slices.Contains(CollectionSources, s) {
			return invalid("activity %s: collection source %q is not a §5 source", a.ID, s)
		}
	}
	if err := a.LegalBasis.Validate(); err != nil {
		// PRIV-REQ-0007.
		return fmt.Errorf("activity %s: %w", a.ID, err)
	}
	if a.NecessityRationale == "" {
		// PRIV-REQ-0009: nothing collected for unspecified future use.
		return invalid("activity %s states no necessity rationale", a.ID)
	}
	if len(a.Regions.Processing) == 0 || len(a.Regions.Storage) == 0 {
		// PRIV-REQ-0006.
		return invalid("activity %s identifies no processing or storage region", a.ID)
	}
	if a.RetentionPolicyID == "" {
		return invalid("activity %s names no retention policy", a.ID)
	}
	if !slices.Contains(DPIAStatuses, a.DPIA) {
		return invalid("activity %s: DPIA status %q is not a §5 status", a.ID, a.DPIA)
	}
	if slices.Contains(a.DataCategories, P6) && (a.DPIA != DPIAApproved || a.LegalBasis.Category != BasisLegalObligation) {
		// §7: P6 is prohibited from ordinary schemas unless a documented legal
		// requirement exists. "Documented" is read as an approved assessment
		// and a legal-obligation basis naming the requirement.
		return invalid("activity %s processes P6 without an approved DPIA and a legal-obligation basis", a.ID)
	}
	if a.Owner == "" {
		return invalid("activity %s has no owner", a.ID)
	}
	if a.ReviewDue.IsZero() {
		return invalid("activity %s has no review date", a.ID)
	}
	return nil
}

// ---------------------------------------------------------------------------
// PrivacyProfile
// ---------------------------------------------------------------------------

// RightRule is one data-subject right in a jurisdiction: what it is, how long
// the controller has, and whether an appeal path is required.
type RightRule struct {
	Right        string
	DeadlineDays int
	Appeal       bool
}

// BreachRule is a jurisdiction's breach-notification rule.
type BreachRule struct {
	Threshold  string
	Clock      time.Duration
	Recipients []string
}

// PrivacyProfile is a PRIV-001 §26 jurisdiction profile.
//
// It is bitemporal (§26 "effective/known time"): EffectiveFrom/To is when the
// law applies, KnownAt is when the platform recorded it, so a historical
// decision can be explained by the profile that was known at the time rather
// than the one known now.
//
//nolint:revive // PRIV-001 §26 names this type; kept verbatim, as domain/ai does for ADR-0006 §2.6.
type PrivacyProfile struct {
	LawID            string
	LawVersion       string
	Jurisdiction     string
	TerritorialScope string
	RoleRules        []ProcessingRole
	LawfulBases      []LegalBasisCategory
	NoticeRules      []string
	// SensitiveClasses are the classes the jurisdiction treats as sensitive
	// beyond P6, under its own definitions.
	SensitiveClasses       []Class
	Rights                 []RightRule
	DPIATriggers           []string
	TransferRules          []TransferMechanism
	BreachRules            []BreachRule
	ChildrenRules          []string
	AutomatedDecisionRules []string
	RetentionConstraints   []string
	// Localization records a legal requirement that data stay in the
	// jurisdiction, which forbids any cross-border TransferProfile for it.
	Localization  bool
	Authority     string
	EffectiveFrom time.Time
	// EffectiveTo is exclusive. Zero means still in force.
	EffectiveTo time.Time
	KnownAt     time.Time
}

// Validate applies PRIV-REQ-0104 and -0105.
func (p PrivacyProfile) Validate() error {
	if p.LawID == "" || p.LawVersion == "" || p.Jurisdiction == "" {
		return invalid("privacy profile names no law, version or jurisdiction")
	}
	name := p.Jurisdiction + "/" + p.LawID + "@" + p.LawVersion
	if p.EffectiveFrom.IsZero() || p.KnownAt.IsZero() {
		// PRIV-REQ-0104.
		return invalid("privacy profile %s is not effective-dated and known-dated", name)
	}
	if !p.EffectiveTo.IsZero() && !p.EffectiveTo.After(p.EffectiveFrom) {
		return invalid("privacy profile %s ends before it begins", name)
	}
	// PRIV-REQ-0105: rights, transfer, breach and DPIA rules are defined. An
	// empty list is "we did not look", which is not the same as "none apply";
	// a jurisdiction where genuinely none apply has no business needing a
	// profile.
	switch {
	case len(p.Rights) == 0:
		return invalid("privacy profile %s defines no rights", name)
	case len(p.TransferRules) == 0 && !p.Localization:
		return invalid("privacy profile %s defines no transfer rules", name)
	case len(p.BreachRules) == 0:
		return invalid("privacy profile %s defines no breach rules", name)
	case len(p.DPIATriggers) == 0:
		return invalid("privacy profile %s defines no DPIA triggers", name)
	case len(p.LawfulBases) == 0:
		return invalid("privacy profile %s defines no lawful bases", name)
	}
	for _, r := range p.Rights {
		if r.Right == "" || r.DeadlineDays <= 0 {
			return invalid("privacy profile %s has a right with no name or deadline", name)
		}
	}
	for _, b := range p.BreachRules {
		if b.Threshold == "" || b.Clock <= 0 || len(b.Recipients) == 0 {
			return invalid("privacy profile %s has an incomplete breach rule", name)
		}
	}
	for _, r := range p.RoleRules {
		if !slices.Contains(ProcessingRoles, r) {
			return invalid("privacy profile %s: role %q is not a §3 role", name, r)
		}
	}
	for _, b := range p.LawfulBases {
		if !slices.Contains(LegalBases, b) {
			return invalid("privacy profile %s: basis %q is not a §11 category", name, b)
		}
	}
	for _, m := range p.TransferRules {
		if !slices.Contains(TransferMechanisms, m) {
			return invalid("privacy profile %s: mechanism %q is not a transfer mechanism", name, m)
		}
	}
	for _, c := range p.SensitiveClasses {
		if !c.Valid() {
			return invalid("privacy profile %s: %q is not a PRIV-001 class", name, c)
		}
	}
	if p.Localization && len(p.TransferRules) > 0 {
		return invalid("privacy profile %s requires localization and also permits transfer mechanisms", name)
	}
	return nil
}

// EffectiveAt reports whether the profile is in force at t.
func (p PrivacyProfile) EffectiveAt(t time.Time) bool {
	return !t.Before(p.EffectiveFrom) && (p.EffectiveTo.IsZero() || t.Before(p.EffectiveTo))
}

// ---------------------------------------------------------------------------
// TransferProfile
// ---------------------------------------------------------------------------

// Party is one side of a transfer: who, and in what PRIV-001 §3 role.
type Party struct {
	Name string
	Role ProcessingRole
}

// Location is where data sits: the region, and the cell within it. A profile
// is scoped to cells rather than to regions alone because a cell is the unit
// of residency (ADR-0009 §2.6), and two cells in one region are still two
// residency boundaries.
type Location struct {
	Region string
	Cell   string
}

// TransferProfile is PRIV-001 §16's transfer record: "exporter/controller/
// processor roles, importer, destination, mechanism, purpose, data
// categories, safeguards, effective date and review date."
//
// It is the approval a cross-cell copy cites. A transfer outside it — another
// class, another destination, after its review date — is not a transfer it
// covers, and is refused (see Permits).
type TransferProfile struct {
	ID       string
	Version  int
	Exporter Party
	Importer Party
	Origin   Location
	// Destination is where the data goes.
	Destination Location
	Mechanism   TransferMechanism
	Purposes    []Purpose
	DataClasses []Class
	Safeguards  []string
	// EffectiveFrom is inclusive and EffectiveTo exclusive; zero EffectiveTo
	// means no fixed end.
	EffectiveFrom time.Time
	EffectiveTo   time.Time
	// ReviewDue is when the mechanism must be re-examined for legal change
	// (PRIV-REQ-0067). A profile past its review date covers nothing.
	ReviewDue time.Time
}

// Validate applies PRIV-REQ-0066 and -0067.
func (p TransferProfile) Validate() error {
	if p.ID == "" || p.Version <= 0 {
		return invalid("transfer profile has no id or version")
	}
	name := fmt.Sprintf("%s@%d", p.ID, p.Version)
	// PRIV-REQ-0066: exporter, importer, roles, purpose, data, destination,
	// mechanism.
	switch {
	case p.Exporter.Name == "" || !slices.Contains(ProcessingRoles, p.Exporter.Role):
		return invalid("transfer profile %s does not identify the exporter and its role", name)
	case p.Importer.Name == "" || !slices.Contains(ProcessingRoles, p.Importer.Role):
		return invalid("transfer profile %s does not identify the importer and its role", name)
	case p.Origin.Region == "" || p.Origin.Cell == "":
		return invalid("transfer profile %s does not identify its origin", name)
	case p.Destination.Region == "" || p.Destination.Cell == "":
		return invalid("transfer profile %s does not identify its destination", name)
	case p.Origin.Cell == p.Destination.Cell:
		return invalid("transfer profile %s moves nothing: origin and destination are one cell", name)
	case !slices.Contains(TransferMechanisms, p.Mechanism):
		return invalid("transfer profile %s: mechanism %q is not a transfer mechanism", name, p.Mechanism)
	case len(p.Purposes) == 0:
		return invalid("transfer profile %s names no purpose", name)
	case len(p.DataClasses) == 0:
		return invalid("transfer profile %s names no data classes", name)
	}
	for _, pu := range p.Purposes {
		if !slices.Contains(Purposes, pu) {
			return invalid("transfer profile %s: purpose %q is not an approved purpose", name, pu)
		}
	}
	for _, c := range p.DataClasses {
		if !c.Valid() {
			return invalid("transfer profile %s: %q is not a PRIV-001 class", name, c)
		}
		if c == P7 {
			// Secrets and credentials never cross a cell boundary. SEC-001 §12
			// keeps keys distinct per cell; a credential copied to another
			// cell is a credential that now has two custodians.
			return invalid("transfer profile %s lists P7; secrets and credentials are never transferred", name)
		}
	}
	switch p.Mechanism {
	case MechanismContractualSafeguards, MechanismBindingCorporateRules:
		if len(p.Safeguards) == 0 {
			return invalid("transfer profile %s relies on %s and records no safeguards", name, p.Mechanism)
		}
	}
	// PRIV-REQ-0067: effective-dated and reviewed.
	if p.EffectiveFrom.IsZero() || p.ReviewDue.IsZero() {
		return invalid("transfer profile %s is not effective-dated with a review date", name)
	}
	if !p.EffectiveTo.IsZero() && !p.EffectiveTo.After(p.EffectiveFrom) {
		return invalid("transfer profile %s ends before it begins", name)
	}
	if !p.ReviewDue.After(p.EffectiveFrom) {
		return invalid("transfer profile %s is due for review before it takes effect", name)
	}
	return nil
}

// ErrTransferNotPermitted is the sentinel for a transfer its profile does not
// cover.
var ErrTransferNotPermitted = errors.New("privacy: transfer not permitted by its profile")

// Permits reports whether this profile covers moving data of the given classes,
// for the given purpose, from one cell to another at an instant. Every
// difference is a refusal; there is no partial cover, because a transfer that
// moves one class the profile does not list has moved that class without
// approval, whatever else it moved.
func (p TransferProfile) Permits(sourceCell, destinationCell string, purpose Purpose, classes []Class, at time.Time) error {
	if err := p.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrTransferNotPermitted, err)
	}
	name := fmt.Sprintf("%s@%d", p.ID, p.Version)
	switch {
	case sourceCell != p.Origin.Cell:
		return fmt.Errorf("%w: %s does not cover transfers from cell %q", ErrTransferNotPermitted, name, sourceCell)
	case destinationCell != p.Destination.Cell:
		return fmt.Errorf("%w: %s does not cover transfers to cell %q", ErrTransferNotPermitted, name, destinationCell)
	case !slices.Contains(p.Purposes, purpose):
		return fmt.Errorf("%w: %s does not cover purpose %s", ErrTransferNotPermitted, name, purpose)
	case at.Before(p.EffectiveFrom):
		return fmt.Errorf("%w: %s is not yet in effect", ErrTransferNotPermitted, name)
	case !p.EffectiveTo.IsZero() && !at.Before(p.EffectiveTo):
		return fmt.Errorf("%w: %s has ended", ErrTransferNotPermitted, name)
	case !at.Before(p.ReviewDue):
		return fmt.Errorf("%w: %s is past its review date and covers nothing until reviewed", ErrTransferNotPermitted, name)
	case len(classes) == 0:
		return fmt.Errorf("%w: a transfer must state the data classes it moves", ErrTransferNotPermitted)
	}
	for _, c := range classes {
		if !slices.Contains(p.DataClasses, c) {
			return fmt.Errorf("%w: %s does not cover class %s", ErrTransferNotPermitted, name, c)
		}
	}
	return nil
}
