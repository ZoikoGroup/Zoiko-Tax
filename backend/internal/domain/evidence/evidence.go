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
//
// A decision is two canonical documents, each identified by its own digest:
//
//	envelope  what the determination was allowed to depend on
//	result    what it concluded, with the execution trace that explains it,
//	          naming the envelope by digest
//
// A replay rebuilds the result from the envelope alone and compares bytes. The
// result's digest is also the leaf a period seal commits to (seal.go), so one
// digest ties the decision to the seal, the seal to the envelope, and the
// envelope to the input.
package evidence

import (
	"fmt"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
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

// Valid reports whether o is one of the recorded outcomes.
func (o Outcome) Valid() bool {
	switch o {
	case OutcomeAuthoritative, OutcomeAdvisory, OutcomeAmbiguous, OutcomeConflicted,
		OutcomeUnsupported, OutcomeReviewRequired:
		return true
	}
	return false
}

// Envelope is everything a replay is allowed to depend on.
type Envelope struct {
	DecisionTime time.Time
	EventTime    time.Time

	BundleID     string
	BundleDigest string
	IRVersion    int
	CanonProfile string
	Trains       Trains

	// Input is the canonical input. Its digest is what the idempotency record
	// keys on (ADR-0013 §2.3) and what a replay is verified against.
	Input Input

	// Accumulators is the read set: every accumulator value the evaluation was
	// given, read before it began (ZTAX-DET-REQ-0002). It is in the envelope
	// because ZTAX-DET-REQ-0034 requires it there, and because a replay that
	// re-read the accumulators would read today's totals and reach a
	// different answer for a threshold rule that was right at the time.
	//
	// An empty read set is recorded as an empty object rather than omitted:
	// "this decision read no accumulators" is a fact about it.
	Accumulators map[string]fiscal.Money
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
	case !e.DecisionTime.Equal(e.DecisionTime.Truncate(time.Microsecond)),
		!e.EventTime.Equal(e.EventTime.Truncate(time.Microsecond)):
		// canon/v1 carries six fractional digits (ADR-0011 P2). An instant
		// finer than that would be evaluated at one precision and replayed at
		// another, so it is refused here rather than silently truncated.
		return fmt.Errorf("evidence: envelope instants must be whole microseconds")
	case e.BundleID == "" || e.BundleDigest == "":
		return fmt.Errorf("evidence: envelope names no content bundle")
	case e.IRVersion == 0:
		return fmt.Errorf("evidence: envelope names no IR version")
	case e.CanonProfile == "":
		return fmt.Errorf("evidence: envelope names no canonicalization profile")
	case !e.Trains.Complete():
		return fmt.Errorf("evidence: envelope does not name all seven release trains")
	}
	if err := e.Input.Validate(); err != nil {
		return err
	}
	for name := range e.Accumulators {
		if name == "" {
			return fmt.Errorf("evidence: envelope read set names an accumulator with no key")
		}
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
		canonical.F("input", e.Input.Canonical()),
		canonical.F("accumulators", moneySection(e.Accumulators, true)),
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
	return canonical.Sum(e.Input.Canonical())
}

// Frame is the evaluation frame the envelope describes. Determination and
// replay both build the frame here, from the envelope and nothing else — so
// there is one construction, and a replay cannot see anything the original
// evaluation did not.
func (e Envelope) Frame() rule.Frame {
	return rule.Frame{
		DecisionTime: e.DecisionTime,
		EventTime:    e.EventTime,
		Money:        e.Input.Money,
		Rates:        e.Input.Rates,
		Quantities:   e.Input.Quantities,
		Flags:        e.Input.Flags,
		Strings:      e.Input.Strings,
		Accumulators: e.Accumulators,
	}
}

// Decision is one determination, as recorded.
type Decision struct {
	ID       id.DecisionID
	TenantID id.TenantID

	// BusinessKey is stable across corrections; a correction is a new row
	// linked by Supersedes (ADR-0003 §2.2).
	BusinessKey string
	Supersedes  *id.DecisionID

	Envelope       Envelope
	EnvelopeDigest canonical.Digest

	Result       Result
	ResultDigest canonical.Digest
}

// Conclude maps an evaluation to an outcome.
//
// This path cannot produce AUTHORITATIVE, and that is the point of writing it
// this way rather than taking an "authoritative" flag. ZTAX-DET-REQ-0037
// requires all five A4 conditions at once, and ZTAX-DET-REQ-0038 requires the
// authoritative result to be a structurally distinct type rather than a
// boolean on this one. Until both exist, every computed figure is ADVISORY and
// says why.
func Conclude(r rule.Result) (Outcome, errs.ReasonCode) {
	if !r.Refused {
		return OutcomeAdvisory, errs.ReasonNotAuthoritative
	}
	switch r.Reason {
	case errs.ReasonAmbiguous:
		return OutcomeAmbiguous, r.Reason
	case errs.ReasonConflicted:
		return OutcomeConflicted, r.Reason
	case errs.ReasonReviewRequired:
		return OutcomeReviewRequired, r.Reason
	}
	return OutcomeUnsupported, r.Reason
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
