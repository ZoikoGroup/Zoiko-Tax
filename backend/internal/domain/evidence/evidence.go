// Package evidence holds the record of what a decision was and how it was
// reached.
//
// The central type is Envelope. ADR-0011 §2.8 defines a replay as the canonical
// input plus the decision time, the bundle identity, the seven train versions,
// the IR version, the canon profile version and the rounding policies applied —
// and states the consequence that makes the envelope worth taking seriously:
//
//	Anything not in the envelope is, by definition, something a replay is not
//	allowed to depend on.
//
// So the envelope is the operational definition of determinism for this estate.
// A field added to it is a new thing a decision may depend on; a field left out
// is a thing it may not. That is a governance decision, not a struct change.
package evidence

import (
	"fmt"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/rule"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// Trains are the seven release-train versions that produced a decision
// (ADR-0015 §2.6). A missing one fails the readiness probe rather than starting
// a process that cannot say which combination produced a result.
type Trains struct {
	App       string
	Content   string
	AI        string
	Adapter   string
	Infra     string
	Schema    string
	Migration string
}

// Complete reports whether every train is named.
func (t Trains) Complete() bool {
	return t.App != "" && t.Content != "" && t.AI != "" && t.Adapter != "" &&
		t.Infra != "" && t.Schema != "" && t.Migration != ""
}

// Canonical renders the trains for the envelope.
func (t Trains) Canonical() canonical.Value {
	return canonical.Object(
		canonical.F("app", canonical.String(t.App)),
		canonical.F("content", canonical.String(t.Content)),
		canonical.F("ai", canonical.String(t.AI)),
		canonical.F("adapter", canonical.String(t.Adapter)),
		canonical.F("infra", canonical.String(t.Infra)),
		canonical.F("schema", canonical.String(t.Schema)),
		canonical.F("migration", canonical.String(t.Migration)),
	)
}

// Outcome is what a determination concluded.
//
// ADR-0016 §2.1: the refusals are outcomes with evidence, returned 200 with an
// explicit non-authoritative marker, because "we do not support this
// jurisdiction" is a fact about our coverage that a customer is entitled to
// have recorded against their transaction. An error response records nothing.
type Outcome string

// The outcomes.
const (
	// OutcomeAuthoritative is the only one that may be filed, and only after A4.
	OutcomeAuthoritative Outcome = "AUTHORITATIVE"
	// OutcomeAdvisory is a real computation that this deployment is not
	// authorized to make authoritative.
	OutcomeAdvisory       Outcome = "ADVISORY"
	OutcomeAmbiguous      Outcome = "AMBIGUOUS"
	OutcomeConflicted     Outcome = "CONFLICTED"
	OutcomeUnsupported    Outcome = "UNSUPPORTED"
	OutcomeReviewRequired Outcome = "REVIEW_REQUIRED"
)

// Filable reports whether an outcome may be used for a filing.
func (o Outcome) Filable() bool { return o == OutcomeAuthoritative }

// Envelope is everything a replay is allowed to depend on.
type Envelope struct {
	DecisionTime time.Time
	EventTime    time.Time

	BundleID     string
	BundleDigest string
	IRVersion    int
	CanonProfile string
	Trains       Trains

	// Input is the canonical input document. Its digest is what the idempotency
	// record keys on (ADR-0013 §2.3) and what a replay is verified against.
	Input canonical.Value
}

// Validate refuses an envelope that cannot support a replay.
//
// This runs before a decision is written, not when someone tries to replay it
// years later. A decision recorded with an incomplete envelope is a decision
// that will fail to replay at exactly the moment replay matters.
func (e Envelope) Validate() error {
	switch {
	case e.DecisionTime.IsZero():
		return fmt.Errorf("evidence: envelope names no decision time")
	case e.EventTime.IsZero():
		return fmt.Errorf("evidence: envelope names no event time")
	case e.BundleID == "" || e.BundleDigest == "":
		return fmt.Errorf("evidence: envelope names no content bundle")
	case e.IRVersion == 0:
		return fmt.Errorf("evidence: envelope names no IR version")
	case e.CanonProfile == "":
		return fmt.Errorf("evidence: envelope names no canonicalization profile")
	case !e.Trains.Complete():
		return fmt.Errorf("evidence: envelope does not name all seven release trains")
	case e.Input.IsAbsent():
		return fmt.Errorf("evidence: envelope carries no canonical input")
	}
	return nil
}

// Canonical renders the envelope for digesting.
func (e Envelope) Canonical() canonical.Value {
	return canonical.Object(
		canonical.F("decisionTime", canonical.Time(e.DecisionTime)),
		canonical.F("eventTime", canonical.Time(e.EventTime)),
		canonical.F("bundleId", canonical.String(e.BundleID)),
		canonical.F("bundleDigest", canonical.String(e.BundleDigest)),
		canonical.F("irVersion", canonical.Integer(int64(e.IRVersion))),
		canonical.F("canonProfile", canonical.String(e.CanonProfile)),
		canonical.F("trains", e.Trains.Canonical()),
		canonical.F("input", e.Input),
	)
}

// Digest returns the envelope's own digest, which is what a replay is verified
// against (ADR-0011 §2.8).
func (e Envelope) Digest() (canonical.Digest, error) {
	if err := e.Validate(); err != nil {
		return canonical.Digest{}, err
	}
	return canonical.Sum(e.Canonical())
}

// InputDigest returns the digest of the canonical input alone. The idempotency
// record keys on this rather than on the envelope, because two retries of one
// request carry the same input at different decision times.
func (e Envelope) InputDigest() (canonical.Digest, error) {
	return canonical.Sum(e.Input)
}

// Decision is the record written for one determination.
type Decision struct {
	ID       id.DecisionID
	TenantID id.TenantID

	// BusinessKey is stable across corrections; a correction is a new row
	// linked by Supersedes (ADR-0003 §2.2).
	BusinessKey string
	Supersedes  *id.DecisionID

	Envelope Envelope
	Outcome  Outcome
	Reason   errs.ReasonCode

	// Trace is the execution trace (ADR-0005 §2.7): evidence, sealed and
	// retained under statutory retention, not telemetry.
	Trace []rule.TraceStep

	// Emitted are the result slots the content filled.
	Emitted map[string]rule.Value
}

// CanonicalTrace renders the trace for digesting and for storage.
func CanonicalTrace(steps []rule.TraceStep) canonical.Value {
	items := make([]canonical.Value, 0, len(steps))
	for _, s := range steps {
		args := canonical.Absent()
		if len(s.Args) > 0 {
			values := make([]canonical.Value, 0, len(s.Args))
			for _, a := range s.Args {
				values = append(values, canonical.String(string(a)))
			}
			args = canonical.Array(values...)
		}
		items = append(items, canonical.Object(
			canonical.F("node", canonical.String(string(s.Node))),
			canonical.F("op", canonical.String(string(s.Op))),
			canonical.F("args", args),
			canonical.F("output", canonical.String(s.Output)),
			canonical.F("outputType", canonical.String(string(s.OutputType))),
			canonical.F("ruleVersion", canonical.String(s.RuleVersion)),
			canonical.F("ruleSemanticId", canonical.String(s.RuleSemanticID)),
			canonical.F("policy", canonical.OptString(s.Policy)),
		))
	}
	// Array order is the evaluation order and is significant (ADR-0011 P4).
	// Sorting it would destroy the thing the trace is for.
	return canonical.Array(items...)
}
