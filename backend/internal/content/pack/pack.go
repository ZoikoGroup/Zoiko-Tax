// Package pack reads what a content pack declares about itself — its pack
// declaration and the source register its sources are licensed under — and
// turns a declaration into the signed pack section of a bundle manifest, but
// only once the build-time gates have passed.
//
// It is the Z4 content plane's half of ZTAX-CONT-001 §11 and ZTAX-SRC-001 §15
// (ADR-0005 §2.1): only ztax-contentc reads these files. A cell never sees a
// declaration or a register; it sees the pack section the compiler wrote into
// the manifest, signed, and the record digests that pin which licence records
// the build was judged against.
//
// # Where a pack declares its sources
//
// In a sidecar, pack.json, beside the .ztax source — not in the rule language.
// Three reasons:
//
//   - The DSL is the mutation-tested surface of ADR-0005 §5.1 control 3, and
//     its grammar is kept small on purpose. Pack metadata is not rule logic:
//     a level, a version range or a licence reference has no node in the DAG,
//     and putting it in the grammar would grow the surface that most needs to
//     stay small for something that does not execute.
//   - CONT-001 §35 already names the artefact: pack-manifest.yaml, separate
//     from rule definitions. JSON here rather than YAML because the module has
//     no YAML parser and this work adds no dependency; the content is the same.
//   - Licence records are not authored by the pack. They belong to the source
//     register (SRC-001 §16 "Source Registry: system of record for source
//     identity, authority and licence"; CONT-001 §35 source-register.yaml),
//     shared by every pack that uses a source. A pack names the source; the
//     register says what we may do with it. One record per source, so a licence
//     change is one edit and re-evaluates every pack that depends on it
//     (ZTAX-SRC-REQ-0094).
//
// Every file is decoded strictly: unknown fields are refused (ADR-0011 P5),
// for the reason the bundle format refuses them — a field this build does not
// understand is a field whose meaning it would be guessing.
package pack

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/content"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/sourcing"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// DeclarationFile is the conventional name of a pack declaration, beside the
// .ztax source it describes.
const DeclarationFile = "pack.json"

// Declaration is a pack's own statement of what it is.
type Declaration struct {
	// BundleID ties the declaration to one .ztax source: it must equal the
	// source's `bundle` identity, so a declaration copied into another pack's
	// directory is refused rather than silently describing the wrong law.
	BundleID          string                    `json:"bundleId"`
	PackID            content.PackID            `json:"packId"`
	Version           content.Version           `json:"version"`
	Level             content.Level             `json:"level"`
	Status            content.PackStatus        `json:"status"`
	Capabilities      []content.Capability      `json:"capabilities"`
	Territories       []string                  `json:"territories,omitempty"`
	DeploymentModes   []sourcing.DeploymentMode `json:"deploymentModes"`
	Dependencies      []content.Dependency      `json:"dependencies,omitempty"`
	Conflicts         []content.Conflict        `json:"conflicts,omitempty"`
	Sources           []DeclaredSource          `json:"sources"`
	Owner             string                    `json:"owner,omitempty"`
	WithdrawalPlanRef string                    `json:"withdrawalPlanRef,omitempty"`
}

// DeclaredSource is one source the pack is derived from, by identity, and any
// rights it needs beyond what every bundle needs (sourcing.BuildRights).
type DeclaredSource struct {
	Source sourcing.SourceID `json:"sourceId"`
	Uses   []sourcing.Right  `json:"uses,omitempty"`
}

// Node is the declaration as dependency resolution sees it.
func (d Declaration) Node() content.PackNode {
	return content.PackNode{ID: d.PackID, Version: d.Version, Level: d.Level, Dependencies: d.Dependencies, Conflicts: d.Conflicts}
}

// DecodeDeclaration reads a pack.json.
func DecodeDeclaration(data []byte) (Declaration, error) {
	var d Declaration
	if err := strict(data, &d); err != nil {
		return Declaration{}, fmt.Errorf("pack: declaration: %w", err)
	}
	return d, nil
}

// Register is the source register: one licence record per source.
type Register map[sourcing.SourceID]sourcing.SourceLicenseRecord

// DecodeRegister reads a register file and validates every record in it.
//
// A malformed record anywhere in the register refuses the whole register,
// including for packs that do not use it. That is stricter than necessary for
// any one build and it is deliberate: the register is shared, and a defect in
// it is found by whichever build runs next rather than by the one pack that
// happens to depend on the broken entry, months later.
func DecodeRegister(data []byte) (Register, error) {
	var doc struct {
		Sources []wireRecord `json:"sources"`
	}
	if err := strict(data, &doc); err != nil {
		return nil, fmt.Errorf("pack: register: %w", err)
	}
	reg := Register{}
	for _, w := range doc.Sources {
		r, err := w.record()
		if err != nil {
			return nil, fmt.Errorf("pack: register: %w", err)
		}
		if err := r.Validate(); err != nil {
			return nil, fmt.Errorf("pack: register: %w", err)
		}
		if _, dup := reg[r.SourceID]; dup {
			return nil, fmt.Errorf("pack: register: source %s is recorded twice", r.SourceID)
		}
		reg[r.SourceID] = r
	}
	return reg, nil
}

// Result is a pack section ready to sign, and the evidence of how it got there.
type Result struct {
	Pack content.PackManifest
	// LoadOrder is the resolved dependency order, this pack last among the
	// packs it reaches.
	LoadOrder []content.PackNode
	// Evaluations is the build-time rights evaluation for every source and
	// every deployment mode (SRC-001 §23 "bundle build-time rights
	// evaluation").
	Evaluations []sourcing.Evaluation
}

// Assemble checks a declaration against the bundle it describes, resolves it
// against every pack available to the build, runs the rights gate, and
// returns the pack section for the manifest. Any failure refuses the build:
//
//   - the declaration names a different bundle than the source compiled;
//   - the pack is SUSPENDED or WITHDRAWN (a new bundle is new outcomes);
//   - dependency resolution fails — missing, unsatisfied, level inversion,
//     conflict or cycle (ZTAX-CONT-REQ-0007, -0008, -0087);
//   - any source lacks a current SourceLicenseRecord permitting every build
//     right for every declared deployment mode (ZTAX-SRC-REQ-0050, -0051,
//     ZTAX-CONT-REQ-0021).
//
// at is the instant the licences are judged at. It is a parameter, not a
// clock read, so that a build is reproducible: the same inputs and the same
// instant give the same answer, and the answer can be re-derived later from
// the record digests the manifest carries.
func Assemble(decl Declaration, bundleID string, available []content.PackNode, reg Register, at time.Time) (Result, error) {
	if decl.BundleID != bundleID {
		return Result{}, fmt.Errorf("pack: the declaration describes bundle %q, the source compiles bundle %q", decl.BundleID, bundleID)
	}
	if !decl.Status.Releasable() {
		return Result{}, fmt.Errorf("pack: %s is %s; a suspended or withdrawn pack is not released (ZTAX-CONT-REQ-0042)", decl.PackID, decl.Status)
	}

	order, err := content.Resolve(available, decl.PackID)
	if err != nil {
		return Result{}, err
	}

	ids := make([]sourcing.SourceID, 0, len(decl.Sources))
	extra := map[sourcing.SourceID][]sourcing.Right{}
	for _, s := range decl.Sources {
		if _, dup := extra[s.Source]; dup {
			return Result{}, fmt.Errorf("pack: %s declares source %s twice", decl.PackID, s.Source)
		}
		ids = append(ids, s.Source)
		extra[s.Source] = append([]sourcing.Right{}, s.Uses...)
	}
	evals, err := sourcing.Gate(ids, reg, extra, decl.DeploymentModes, decl.Territories, at)
	if err != nil {
		return Result{Evaluations: evals}, fmt.Errorf("pack: %s: %w", decl.PackID, err)
	}

	m := content.PackManifest{
		ID: decl.PackID, Version: decl.Version, Level: decl.Level, Status: decl.Status,
		Capabilities: decl.Capabilities, Territories: decl.Territories, DeploymentModes: decl.DeploymentModes,
		Dependencies: decl.Dependencies, Conflicts: decl.Conflicts,
		Owner: decl.Owner, WithdrawalPlanRef: decl.WithdrawalPlanRef,
	}
	for _, s := range decl.Sources {
		rec := reg[s.Source]
		digest, err := RecordDigest(rec)
		if err != nil {
			return Result{}, err
		}
		m.Sources = append(m.Sources, content.SourceDependency{
			Source: s.Source, LicenceRef: rec.LicenceRef, RecordDigest: digest, Uses: s.Uses,
		})
	}
	m = m.Normalize()
	if err := m.Validate(); err != nil {
		return Result{}, err
	}
	return Result{Pack: m, LoadOrder: order, Evaluations: evals}, nil
}

// RecordDigest is the canon/v1 digest of a licence record, as the manifest
// records it (ZTAX-SRC-REQ-0083: a build is identifiable historically by
// source, version and rights profile).
//
// The record is written out field by field, for the reason internal/content/
// bundle writes the manifest that way: what gets digested is what somebody
// wrote down, not what a struct tag said.
func RecordDigest(r sourcing.SourceLicenseRecord) (string, error) {
	d, err := canonical.Sum(canonicalRecord(r))
	if err != nil {
		return "", fmt.Errorf("pack: digest record %s: %w", r.SourceID, err)
	}
	return d.String(), nil
}

func canonicalRecord(r sourcing.SourceLicenseRecord) canonical.Value {
	optTime := func(t time.Time) canonical.Value {
		if t.IsZero() {
			return canonical.Absent()
		}
		return canonical.Time(t)
	}
	strs := func(ss []string) canonical.Value {
		if len(ss) == 0 {
			return canonical.Absent()
		}
		sorted := append([]string(nil), ss...)
		sort.Strings(sorted)
		items := make([]canonical.Value, len(sorted))
		for i, s := range sorted {
			items[i] = canonical.String(s)
		}
		return canonical.Array(items...)
	}

	grants := make([]canonical.Field, 0, len(r.Rights.Grants))
	for right, g := range r.Rights.Grants {
		conds := make([]canonical.Value, 0, len(g.Conditions))
		for _, c := range g.Conditions {
			conds = append(conds, canonical.Object(
				canonical.F("kind", canonical.String(string(c.Kind))),
				canonical.F("values", strs(c.Values)),
				canonical.F("text", canonical.OptString(c.Text)),
			))
		}
		condValue := canonical.Absent()
		if len(conds) > 0 {
			// Conditions keep their authored order: they are evaluated in it,
			// and the first unmet one is the reason a refusal reports.
			condValue = canonical.Array(conds...)
		}
		grants = append(grants, canonical.F(string(right), canonical.Object(
			canonical.F("state", canonical.String(string(g.State))),
			canonical.F("conditions", condValue),
		)))
	}
	envs := make([]string, len(r.Environments))
	for i, e := range r.Environments {
		envs[i] = string(e)
	}
	surviving := make([]string, len(r.Termination.SurvivingRights))
	for i, s := range r.Termination.SurvivingRights {
		surviving[i] = string(s)
	}

	return canonical.Object(
		canonical.F("sourceId", canonical.String(string(r.SourceID))),
		canonical.F("providerLegalName", canonical.String(r.ProviderLegalName)),
		canonical.F("sourceName", canonical.String(r.SourceName)),
		canonical.F("sourceVersion", canonical.OptString(r.SourceVersion)),
		canonical.F("class", canonical.String(string(r.Class))),
		canonical.F("acquisition", canonical.String(string(r.Acquisition))),
		canonical.F("licenceRef", canonical.String(r.LicenceRef)),
		canonical.F("rights", canonical.Object(grants...)),
		canonical.F("attributionRequired", canonical.String(string(r.Rights.AttributionRequired))),
		canonical.F("shareAlikeTrigger", canonical.String(string(r.Rights.ShareAlikeTrigger))),
		canonical.F("territories", strs(r.Territories)),
		canonical.F("environments", strs(envs)),
		canonical.F("constraints", canonical.OptString(r.Constraints)),
		canonical.F("attributionNotice", canonical.OptString(r.AttributionNotice)),
		canonical.F("derivativeRules", canonical.OptString(r.DerivativeRules)),
		canonical.F("dataResidencyTerms", canonical.OptString(r.DataResidencyTerms)),
		canonical.F("aiTerms", canonical.OptString(r.AITerms)),
		canonical.F("term", canonical.Object(
			canonical.F("start", canonical.Time(r.Term.Start)),
			canonical.F("end", optTime(r.Term.End)),
			canonical.F("autoRenew", canonical.Bool(r.Term.AutoRenew)),
			canonical.F("noticeDays", canonical.Integer(int64(r.Term.NoticeDays))),
		)),
		canonical.F("termination", canonical.Object(
			canonical.F("survivingRights", strs(surviving)),
			canonical.F("deleteRawWithinDays", canonical.Integer(int64(r.Termination.DeleteRawWithinDays))),
			canonical.F("notes", canonical.OptString(r.Termination.Notes)),
		)),
		canonical.F("auditRights", canonical.OptString(r.AuditRights)),
		canonical.F("subprocessors", strs(r.Subprocessors)),
		canonical.F("costModel", canonical.String(string(r.CostModel))),
		canonical.F("legalOwner", canonical.String(r.LegalOwner)),
		canonical.F("technicalOwner", canonical.String(r.TechnicalOwner)),
		canonical.F("reviewDue", canonical.Time(r.ReviewDue)),
		canonical.F("licenceTermsHash", canonical.OptString(r.LicenceTermsHash)),
		canonical.F("state", canonical.String(string(r.State))),
	)
}

// ---------------------------------------------------------------------------
// wire form of the register
// ---------------------------------------------------------------------------

type wireRecord struct {
	SourceID            sourcing.SourceID            `json:"sourceId"`
	ProviderLegalName   string                       `json:"providerLegalName"`
	SourceName          string                       `json:"sourceName"`
	SourceVersion       string                       `json:"sourceVersion,omitempty"`
	Class               sourcing.SourceClass         `json:"class"`
	Acquisition         sourcing.AcquisitionMethod   `json:"acquisition"`
	LicenceRef          string                       `json:"licenceRef"`
	Rights              map[sourcing.Right]wireGrant `json:"rights"`
	AttributionRequired sourcing.ObligationState     `json:"attributionRequired"`
	ShareAlikeTrigger   sourcing.ObligationState     `json:"shareAlikeTrigger"`
	Territories         []string                     `json:"territories,omitempty"`
	Environments        []sourcing.DeploymentMode    `json:"environments"`
	Constraints         string                       `json:"constraints,omitempty"`
	AttributionNotice   string                       `json:"attributionNotice,omitempty"`
	DerivativeRules     string                       `json:"derivativeRules,omitempty"`
	DataResidencyTerms  string                       `json:"dataResidencyTerms,omitempty"`
	AITerms             string                       `json:"aiTerms,omitempty"`
	Term                struct {
		Start      time.Time  `json:"start"`
		End        *time.Time `json:"end,omitempty"`
		AutoRenew  bool       `json:"autoRenew"`
		NoticeDays int        `json:"noticeDays"`
	} `json:"term"`
	Termination struct {
		SurvivingRights     []sourcing.Right `json:"survivingRights"`
		DeleteRawWithinDays int              `json:"deleteRawWithinDays"`
		Notes               string           `json:"notes,omitempty"`
	} `json:"termination"`
	AuditRights      string                   `json:"auditRights,omitempty"`
	Subprocessors    []string                 `json:"subprocessors,omitempty"`
	CostModel        sourcing.CostModel       `json:"costModel"`
	LegalOwner       string                   `json:"legalOwner"`
	TechnicalOwner   string                   `json:"technicalOwner"`
	ReviewDue        time.Time                `json:"reviewDue"`
	LicenceTermsHash string                   `json:"licenceTermsHash,omitempty"`
	State            sourcing.OnboardingState `json:"state"`
}

// wireGrant accepts a bare state ("ALLOW") or an object with conditions. The
// bare form is what nearly every right in a register is, and a register that
// spelled out {"state":"ALLOW"} two dozen times per source would be read by
// skimming, which is the wrong way to read a licence.
type wireGrant struct {
	State      sourcing.State `json:"state"`
	Conditions []struct {
		Kind   sourcing.ConditionKind `json:"kind"`
		Values []string               `json:"values,omitempty"`
		Text   string                 `json:"text,omitempty"`
	} `json:"conditions,omitempty"`
}

// UnmarshalJSON implements json.Unmarshaler.
func (g *wireGrant) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*g = wireGrant{State: sourcing.State(s)}
		return nil
	}
	type plain wireGrant
	var p plain
	if err := strict(b, &p); err != nil {
		return err
	}
	*g = wireGrant(p)
	return nil
}

func (w wireRecord) record() (sourcing.SourceLicenseRecord, error) {
	grants := make(map[sourcing.Right]sourcing.Grant, len(w.Rights))
	for r, g := range w.Rights {
		var conds []sourcing.Condition
		for _, c := range g.Conditions {
			conds = append(conds, sourcing.Condition{Kind: c.Kind, Values: c.Values, Text: c.Text})
		}
		grants[r] = sourcing.Grant{State: g.State, Conditions: conds}
	}
	r := sourcing.SourceLicenseRecord{
		SourceID: w.SourceID, ProviderLegalName: w.ProviderLegalName, SourceName: w.SourceName,
		SourceVersion: w.SourceVersion, Class: w.Class, Acquisition: w.Acquisition, LicenceRef: w.LicenceRef,
		Rights: sourcing.RightsProfile{
			Grants: grants, AttributionRequired: w.AttributionRequired, ShareAlikeTrigger: w.ShareAlikeTrigger,
		},
		Territories: w.Territories, Environments: w.Environments, Constraints: w.Constraints,
		AttributionNotice: w.AttributionNotice, DerivativeRules: w.DerivativeRules,
		DataResidencyTerms: w.DataResidencyTerms, AITerms: w.AITerms,
		Term: sourcing.Term{Start: w.Term.Start.UTC(), AutoRenew: w.Term.AutoRenew, NoticeDays: w.Term.NoticeDays},
		Termination: sourcing.TerminationEffect{
			SurvivingRights: w.Termination.SurvivingRights, DeleteRawWithinDays: w.Termination.DeleteRawWithinDays,
			Notes: w.Termination.Notes,
		},
		AuditRights: w.AuditRights, Subprocessors: w.Subprocessors, CostModel: w.CostModel,
		LegalOwner: w.LegalOwner, TechnicalOwner: w.TechnicalOwner, ReviewDue: w.ReviewDue.UTC(),
		LicenceTermsHash: w.LicenceTermsHash, State: w.State,
	}
	if w.Term.End != nil {
		r.Term.End = w.Term.End.UTC()
	}
	if w.SourceID == "" {
		return r, fmt.Errorf("a record has no sourceId")
	}
	if strings.TrimSpace(string(w.SourceID)) != string(w.SourceID) {
		return r, fmt.Errorf("source id %q carries whitespace", w.SourceID)
	}
	return r, nil
}

func strict(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return fmt.Errorf("more than one document")
	}
	return nil
}
