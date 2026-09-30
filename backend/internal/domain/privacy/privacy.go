// Package privacy holds ZTAX-PRIV-001's vocabulary as types, and the one way a
// classified value may be carried in Go.
//
// The vocabulary itself is data: contracts/privacy/vocabulary.json is the
// source, the contract lint reads it to classify every API field, and this
// package's test reads it to prove the constants below say the same thing. A
// class or purpose added in one place and not the other fails CI.
//
// The value type is ADR-0015 §2.2's structural redaction. A privacy-classified
// value is carried as a Value, whose only log, format and JSON representations
// are redacted; the underlying string is reachable only through Reveal, which
// is a word a reviewer can search for. A denylist protects the fields someone
// remembered; a type protects every field of that type on the day it is
// created.
package privacy

import (
	"fmt"
	"log/slog"
	"slices"
)

// Class is a PRIV-001 §4 privacy class.
type Class string

// The classes.
const (
	P0 Class = "P0" // non-personal / public
	P1 Class = "P1" // business contact
	P2 Class = "P2" // account / customer personal
	P3 Class = "P3" // telecom / usage identifier
	P4 Class = "P4" // location / situs evidence
	P5 Class = "P5" // financial / fiscal personal
	P6 Class = "P6" // special / highly sensitive
	P7 Class = "P7" // secrets / credentials
)

// Classes is the closed set, in order.
var Classes = []Class{P0, P1, P2, P3, P4, P5, P6, P7}

// Valid reports whether c is a known class.
func (c Class) Valid() bool { return slices.Contains(Classes, c) }

// Personal reports whether values of this class are personal data, and so need
// an approved purpose and a retention policy (PRIV-001 §7). P7 is included: a
// credential is governed primarily by SEC-001, and it still may not be held
// without a reason or kept without a limit.
func (c Class) Personal() bool { return c.Valid() && c != P0 }

// Purpose is a PRIV-001 §6 approved purpose.
type Purpose string

// The approved purposes. PRIV-001 §6: a new purpose needs Privacy and Product
// review, and an existing one may not be reused to legitimise materially
// different processing.
const (
	PurposeTaxCalc   Purpose = "PURP-TAX-CALC"
	PurposeSitus     Purpose = "PURP-SITUS"
	PurposeObl       Purpose = "PURP-OBL"
	PurposeFile      Purpose = "PURP-FILE"
	PurposeRecon     Purpose = "PURP-RECON"
	PurposeEvid      Purpose = "PURP-EVID"
	PurposeSec       Purpose = "PURP-SEC"
	PurposeSupport   Purpose = "PURP-SUPPORT"
	PurposeSvcOps    Purpose = "PURP-SVCOPS"
	PurposeAIAssist  Purpose = "PURP-AI-ASSIST"
	PurposeAnalytics Purpose = "PURP-ANALYTICS"
	PurposeLegal     Purpose = "PURP-LEGAL"
)

// Purposes is the closed set.
var Purposes = []Purpose{
	PurposeTaxCalc, PurposeSitus, PurposeObl, PurposeFile, PurposeRecon, PurposeEvid,
	PurposeSec, PurposeSupport, PurposeSvcOps, PurposeAIAssist, PurposeAnalytics, PurposeLegal,
}

// Retention is a PRIV-001 §14 retention policy. None of them is a number of
// years: retention is decided by jurisdiction, purpose, contract, evidence need
// and legal hold, and PRIV-001 is explicit that no universal period is encoded.
type Retention string

// The retention policies.
const (
	RetentionTransient   Retention = "RET-TRANSIENT"
	RetentionSession     Retention = "RET-SESSION"
	RetentionOperational Retention = "RET-OPERATIONAL"
	RetentionFiscal      Retention = "RET-FISCAL"
	RetentionEvidence    Retention = "RET-EVIDENCE"
	RetentionSecurity    Retention = "RET-SECURITY"
	RetentionLegalHold   Retention = "RET-LEGAL-HOLD"
	RetentionCustom      Retention = "RET-CUSTOM"
)

// Retentions is the closed set.
var Retentions = []Retention{
	RetentionTransient, RetentionSession, RetentionOperational, RetentionFiscal,
	RetentionEvidence, RetentionSecurity, RetentionLegalHold, RetentionCustom,
}

// Redaction says how logs and telemetry treat a value.
type Redaction string

// The redaction policies.
const (
	RedactNone  Redaction = "NONE"
	RedactValue Redaction = "REDACT"
	RedactNoLog Redaction = "NO_LOG"
)

// Redactions is the closed set.
var Redactions = []Redaction{RedactNone, RedactValue, RedactNoLog}

// EvidencePolicy is PRIV-001 §24's situs evidence policy.
type EvidencePolicy string

// The evidence policies.
const (
	EvidenceRawRequired EvidencePolicy = "RAW_REQUIRED"
	EvidenceDerivedOnly EvidencePolicy = "DERIVED_ONLY"
	EvidenceSourceHeld  EvidencePolicy = "SOURCE_HELD"
	EvidenceNone        EvidencePolicy = "NONE"
)

// EvidencePolicies is the closed set.
var EvidencePolicies = []EvidencePolicy{EvidenceRawRequired, EvidenceDerivedOnly, EvidenceSourceHeld, EvidenceNone}

// AIAllowed says whether, and how, a value may reach a model (AIGOV-001,
// through the Governed Model Gateway only).
type AIAllowed string

// The AI permissions.
const (
	AINone                 AIAllowed = "NONE"
	AIDerivedOnly          AIAllowed = "DERIVED_ONLY"
	AIApprovedModelGateway AIAllowed = "APPROVED_MODEL_GATEWAY"
)

// AIAllowances is the closed set.
var AIAllowances = []AIAllowed{AINone, AIDerivedOnly, AIApprovedModelGateway}

// Metadata is PRIV-001 §24's field-level privacy metadata — the same shape as
// the contract's x-ztax-privacy extension.
type Metadata struct {
	Class          Class
	Purposes       []Purpose
	Retention      Retention
	Redaction      Redaction
	EvidencePolicy EvidencePolicy
	AIAllowed      AIAllowed
}

// Validate applies the rules the contract lint applies, so that metadata
// declared in Go and metadata declared in the contract mean the same thing.
func (m Metadata) Validate() error {
	if !m.Class.Valid() {
		return fmt.Errorf("privacy: class %q is not a PRIV-001 class", m.Class)
	}
	for _, p := range m.Purposes {
		if !slices.Contains(Purposes, p) {
			return fmt.Errorf("privacy: purpose %q is not an approved purpose", p)
		}
	}
	if m.Retention != "" && !slices.Contains(Retentions, m.Retention) {
		return fmt.Errorf("privacy: retention %q is not a registered policy", m.Retention)
	}
	if m.Redaction != "" && !slices.Contains(Redactions, m.Redaction) {
		return fmt.Errorf("privacy: redaction %q is not a policy", m.Redaction)
	}
	if m.EvidencePolicy != "" && !slices.Contains(EvidencePolicies, m.EvidencePolicy) {
		return fmt.Errorf("privacy: evidence policy %q is not a policy", m.EvidencePolicy)
	}
	if m.AIAllowed != "" && !slices.Contains(AIAllowances, m.AIAllowed) {
		return fmt.Errorf("privacy: AI permission %q is not a permission", m.AIAllowed)
	}
	if m.Class.Personal() {
		switch {
		case len(m.Purposes) == 0:
			return fmt.Errorf("privacy: class %s is personal data and names no purpose", m.Class)
		case m.Retention == "":
			return fmt.Errorf("privacy: class %s is personal data and names no retention policy", m.Class)
		case m.Redaction == "":
			return fmt.Errorf("privacy: class %s is personal data and does not say how logs treat it", m.Class)
		}
	}
	switch m.Class {
	case P2, P3, P4, P5, P6:
		if m.Redaction == RedactNone {
			return fmt.Errorf("privacy: class %s may not be logged as-is", m.Class)
		}
	case P7:
		if m.Redaction != RedactNoLog {
			return fmt.Errorf("privacy: class P7 is a secret and is never logged")
		}
	}
	if m.Class == P6 {
		return fmt.Errorf("privacy: class P6 is prohibited from ordinary schemas unless a documented legal requirement exists")
	}
	return nil
}

// ---------------------------------------------------------------------------
// the classified value
// ---------------------------------------------------------------------------

// Value is a privacy-classified string.
//
// It has no String method, so fmt's %s cannot reach the content by accident;
// Format, LogValue and MarshalText all produce the redacted form. The content
// is available only through Reveal — at the point where it is genuinely needed,
// such as a database write or an authentication comparison, and nowhere a log
// line could be built from it.
type Value struct {
	v    string
	kind string
	cls  Class
}

// Classify wraps s. kind names what it is ("email", "client_ip") and appears
// in the redacted form, so a log reader knows what was withheld without seeing
// it.
func Classify(s string, kind string, cls Class) Value {
	return Value{v: s, kind: kind, cls: cls}
}

// Email wraps a business-contact email address (P1).
func Email(s string) Value { return Classify(s, "email", P1) }

// Reveal returns the content. Every call site is a place classified data
// leaves its protection, which is why the method is named for that rather than
// for convenience.
func (v Value) Reveal() string { return v.v }

// Class reports the value's classification.
func (v Value) Class() Class { return v.cls }

// IsZero reports whether the value holds nothing.
func (v Value) IsZero() bool { return v.v == "" }

// Redacted is the only rendering a classified value has outside Reveal.
func (v Value) Redacted() string {
	if v.v == "" {
		return "[empty:" + v.kind + "]"
	}
	return "[redacted:" + v.kind + ":" + string(v.cls) + "]"
}

// LogValue implements slog.LogValuer (ADR-0015 §2.2).
func (v Value) LogValue() slog.Value { return slog.StringValue(v.Redacted()) }

// Format implements fmt.Formatter, so every verb — %v, %s, %q, %+v, %#v —
// produces the redacted form.
func (v Value) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(v.Redacted())) }

// MarshalText renders the redacted form, so a Value that ends up in an
// encoding/json document or a telemetry attribute is withheld there too. A
// response that must carry the content builds its wire type from Reveal,
// explicitly.
func (v Value) MarshalText() ([]byte, error) { return []byte(v.Redacted()), nil }
