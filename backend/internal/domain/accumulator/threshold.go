package accumulator

import (
	"fmt"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
)

// MaxThresholdIDLength bounds a threshold id. Mirrored by
// threshold_crossing_id_shape in migration 000007.
const MaxThresholdIDLength = 128

// ThresholdID names a threshold within an accumulator: a de-minimis limit, a
// registration trigger, a cap. It is a content identifier — a stable string
// the bundle declares — and, like Key, opaque here.
type ThresholdID string

// Validate refuses a malformed threshold id.
func (t ThresholdID) Validate() error {
	switch {
	case t == "":
		return errs.Invalid("threshold_id", errs.ReasonMissingField, "A threshold id is required.")
	case len(t) > MaxThresholdIDLength:
		return errs.Invalid("threshold_id", errs.ReasonInvalidValue,
			fmt.Sprintf("A threshold id may be at most %d characters.", MaxThresholdIDLength))
	case !printableASCII(string(t)):
		return errs.Invalid("threshold_id", errs.ReasonInvalidValue,
			"A threshold id must be printable ASCII with no whitespace.")
	}
	return nil
}

// Comparison is how a running total is compared against a threshold's limit.
//
// There is no default, and that is the point. Whether a cap is crossed at
// equality or only beyond it is content, blocked on ZTAX-DET-001 (ADR-0004
// §7.1), and the ADR requires the runtime to express both. A zero-valued
// Comparison is refused by Validate rather than quietly meaning one of them,
// so a threshold whose content never answered the question cannot be
// evaluated at all.
type Comparison string

// The comparisons. The values are what threshold_crossing.comparison stores.
const (
	// AtOrAbove crosses when the total reaches the limit: total >= limit.
	AtOrAbove Comparison = "GTE"
	// Above crosses only when the total exceeds the limit: total > limit.
	Above Comparison = "GT"
)

// Valid reports whether c is a known comparison.
func (c Comparison) Valid() bool { return c == AtOrAbove || c == Above }

// Threshold is a limit on one accumulator's running total.
type Threshold struct {
	ID         ThresholdID
	Limit      fiscal.Money
	Comparison Comparison
}

// Validate refuses a threshold that cannot be evaluated.
func (t Threshold) Validate() error {
	if err := t.ID.Validate(); err != nil {
		return err
	}
	if t.Limit.Currency() == "" {
		return errs.Invalid("threshold.limit", errs.ReasonMissingField,
			fmt.Sprintf("Threshold %s has no limit.", t.ID))
	}
	if !t.Comparison.Valid() {
		return errs.Invalid("threshold.comparison", errs.ReasonInvalidValue,
			fmt.Sprintf("Threshold %s has comparison %q; it must be %s or %s.", t.ID, t.Comparison, AtOrAbove, Above))
	}
	return nil
}

// Reached reports whether total satisfies the threshold.
//
// "Reached" rather than "crossed": a total can be at or beyond a limit
// without this contribution having been the one that crossed it. Apply is
// what turns Reached into a crossing, once.
func (t Threshold) Reached(total fiscal.Money) (bool, error) {
	c, err := compare(total, t.Limit)
	if err != nil {
		return false, err
	}
	switch t.Comparison {
	case AtOrAbove:
		return c >= 0, nil
	case Above:
		return c > 0, nil
	}
	return false, t.Validate()
}

// Crossing is one threshold crossed by one contribution: the payload of the
// threshold_crossing row and of the outbox event written beside it in the
// same transaction (ADR-0004 §2.6).
type Crossing struct {
	Key       Key
	Threshold Threshold
	// Seq and SourceDecisionID name the contribution that crossed it — the
	// audit question every threshold dispute begins with (ADR-0004 §3.2).
	Seq              int64
	SourceDecisionID id.DecisionID
	// Before and After are the running total either side of that
	// contribution. Both are carried because ADR-0004 §7.1 asks a second
	// question beside ">= or >": whether the crossing transaction itself falls
	// inside or outside the threshold. That is content too, and it cannot be
	// answered by content that is only shown one side.
	Before fiscal.Money
	After  fiscal.Money
	// RecordedAt is the crossing contribution's decision time.
	RecordedAt time.Time
}

// compare returns the sign of a-b.
//
// It goes through fiscal's exact subtraction rather than a comparison of its
// own, so that the one-decimal-library rule holds without exception. The cost
// is that a difference needing more than fiscal.Precision significant digits
// is an error rather than an answer; at accumulator magnitudes that would be a
// total already beyond what the estate can represent, which is an error
// anyway.
func compare(a, b fiscal.Money) (int, error) {
	if a.Currency() != b.Currency() {
		return 0, currencyMismatch(a.Currency(), b.Currency())
	}
	d, err := a.Sub(b)
	if err != nil {
		return 0, err
	}
	if d.IsZero() {
		return 0, nil
	}
	p, err := d.NumericParts()
	if err != nil {
		return 0, err
	}
	if p.Negative {
		return -1, nil
	}
	return 1, nil
}

func currencyMismatch(want, got fiscal.Currency) error {
	return errs.Invalid("currency", errs.ReasonCurrencyMismatch,
		fmt.Sprintf("An accumulator denominated in %s cannot combine an amount in %s.", want, got))
}
