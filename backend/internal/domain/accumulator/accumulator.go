// Package accumulator implements the arithmetic half of ADR-0004: a running
// total over a period, advanced by contributions, against which thresholds are
// evaluated exactly once.
//
// The hard part of an accumulator is not the addition. It is that a threshold
// is a legal boundary evaluated under concurrency (ADR-0004 §1), and the three
// properties the ADR calls non-negotiable — exactly-once contribution,
// exactly-once crossing, reconstructability — are split between this package
// and the database on purpose:
//
//   - Serialization is the database's. Commits on one key are serialized by a
//     row lock on the snapshot inside the commit transaction (§2.2), taken in
//     the canonical order LockOrder defines (§2.3). This package never sees a
//     lock, a transaction or a connection; it receives a snapshot that the
//     caller has already locked and returns the snapshot that should replace
//     it.
//   - Idempotence of contributions is the database's too. UNIQUE (tenant,
//     accumulator_key, source_decision_id) refuses a retried contribution
//     (§2.4), and the adapter reports that as "already applied". Nothing here
//     tries to detect a retry, because a check in Go is a check-then-act, and
//     the constraint is the thing that holds when a code path nobody
//     anticipated bypasses every check above it.
//   - Exactly-once crossing is shared. Apply consults and extends the
//     snapshot's set of thresholds already crossed, so it never proposes a
//     crossing twice; the primary key on threshold_crossing refuses the second
//     insert if something else ever does (§2.6).
//   - Reconstructability is this package's. Rebuild recomputes a snapshot from
//     the log and Replay recomputes the crossings, both as pure folds of the
//     same Apply the commit path uses, so a corrupted snapshot is an
//     availability incident rather than a fiscal one (§2.5).
//
// The package is pure (ADR-0007 §2.5): no I/O, no clock, no persistence types.
// Decision time arrives on each Contribution from the request envelope
// (ADR-0003 §2.5), and every amount is a fiscal.Money — apd underneath, never
// a binary float (ADR-0001 C1, enforced by tools/fiscalfloat).
package accumulator

import (
	"fmt"
	"slices"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
)

// MaxKeyLength bounds an accumulator key. Mirrored by the
// *_key_shape CHECK constraints in migration 000007.
const MaxKeyLength = 256

// Key names one accumulator — one running total, one serialization point.
//
// The grammar is deliberately absent. Which dimensions compose a key — tenant,
// jurisdiction, tax, period, an optional customer — determines both contention
// and index shape, and it is an open question blocked on OBL-001 (ADR-0004
// §7.2). Until that lands a Key is an opaque string, and the only things
// asserted about it are the ones the concurrency pattern itself depends on:
//
//   - it is non-empty and bounded, so it can be a primary-key component;
//   - it is printable ASCII with no whitespace. That is load-bearing rather
//     than tidy: for ASCII, Go's byte order, PostgreSQL's "C" collation and
//     every other collation agree on how two keys sort, so the canonical lock
//     order of §2.3 cannot be changed by a database or locale setting.
//
// The tenant is not part of the key. Every row is tenant-scoped by its own
// column (ADR-0012 §2.7), and the repository takes the tenant from the
// security context, so two tenants' keys never meet even when they are
// spelled the same.
//
// When OBL-001 lands, ParseKey is the one place the grammar is added; nothing
// that compares, sorts or stores a Key needs to change.
type Key string

// ParseKey validates an accumulator key.
func ParseKey(s string) (Key, error) {
	if s == "" {
		return "", errs.Invalid("accumulator_key", errs.ReasonMissingField,
			"An accumulator key is required.")
	}
	if len(s) > MaxKeyLength {
		return "", errs.Invalid("accumulator_key", errs.ReasonInvalidValue,
			fmt.Sprintf("An accumulator key may be at most %d characters.", MaxKeyLength))
	}
	if !printableASCII(s) {
		return "", errs.Invalid("accumulator_key", errs.ReasonInvalidValue,
			"An accumulator key must be printable ASCII with no whitespace.")
	}
	return Key(s), nil
}

// String returns the key as stored.
func (k Key) String() string { return string(k) }

// Validate reports whether k is a key ParseKey would accept. A Key can be
// written as a conversion from any string, so every entry point that receives
// one checks it rather than trusting the type.
func (k Key) Validate() error {
	_, err := ParseKey(string(k))
	return err
}

func printableASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < '!' || c > '~' {
			return false
		}
	}
	return true
}

// Ref names an accumulator together with the currency it is denominated in.
//
// The currency is part of the request to lock, not something discovered from
// the row, because the locking path creates a missing snapshot before any
// contribution exists (port.AccumulatorRepository.LockAll) and an empty total
// still has to be a total of something. Once created, a snapshot's currency is
// fixed — the column is not in the UPDATE grant — so a Ref naming a different
// currency for an existing key is refused rather than silently mixing two
// currencies into one running total.
type Ref struct {
	Key      Key
	Currency fiscal.Currency
}

// Validate refuses a malformed reference.
func (r Ref) Validate() error {
	if err := r.Key.Validate(); err != nil {
		return err
	}
	if r.Currency == "" {
		return errs.Invalid("currency", errs.ReasonMissingField,
			fmt.Sprintf("Accumulator %s names no currency.", r.Key))
	}
	return nil
}

// Contribution is one decision's effect on one accumulator: the
// ContributionEvent of W2 lane I, before the log has given it a sequence
// number.
type Contribution struct {
	Key Key
	// SourceDecisionID is the fiscal decision that caused the contribution, and
	// the idempotence key: one decision contributes to one accumulator at most
	// once (ADR-0004 §2.4).
	SourceDecisionID id.DecisionID
	// Amount is signed. A credit note contributes a negative amount, and
	// whether that "un-crosses" anything is a question for content (see
	// Apply), not for the arithmetic.
	Amount fiscal.Money
	// EventTime is when the taxable event occurred. A back-dated or late
	// contribution carries its true event time and is appended, never merged
	// into history (ADR-0004 §2.8); whether it reopens a period is an
	// obligations decision (OBL-001), not a storage one.
	EventTime time.Time
	// RecordedAt is decision time, from the request envelope (ADR-0003 §2.5).
	RecordedAt time.Time
}

// Validate refuses a contribution that cannot be recorded coherently.
func (c Contribution) Validate() error {
	if err := c.Key.Validate(); err != nil {
		return err
	}
	switch {
	case c.SourceDecisionID.IsZero():
		return errs.Invalid("source_decision_id", errs.ReasonMissingField,
			fmt.Sprintf("A contribution to %s must name the decision that caused it.", c.Key))
	case c.Amount.Currency() == "":
		return errs.Invalid("amount", errs.ReasonMissingField,
			fmt.Sprintf("A contribution to %s has no amount.", c.Key))
	case c.EventTime.IsZero():
		return errs.Invalid("event_time", errs.ReasonMissingField,
			fmt.Sprintf("A contribution to %s has no event time.", c.Key))
	case c.RecordedAt.IsZero():
		return errs.Invalid("recorded_at", errs.ReasonMissingField,
			fmt.Sprintf("A contribution to %s has no decision time.", c.Key))
	}
	return nil
}

// Event is a contribution as the log recorded it: at a sequence number, dense
// per key and starting at 1.
type Event struct {
	Contribution
	Seq int64
}

// Snapshot is the running total of one accumulator as of LastSeq.
//
// It is a derived cache that happens to be transactionally exact (ADR-0004
// §2.1): SUM(amount) over the log up to LastSeq equals Total, always, and
// Rebuild is the proof. A Snapshot passed to Apply is one the caller holds the
// row lock on; a value read without the lock is an Observation, a different
// type, so a stale reading cannot be fed into the commit path by mistake.
type Snapshot struct {
	Key   Key
	Total fiscal.Money
	// LastSeq is the sequence number of the last contribution folded into
	// Total. Zero means none has been.
	LastSeq int64
	// Crossed is the set of threshold ids already emitted for this key, sorted
	// ascending and without duplicates. It is what makes Apply's crossings
	// exactly-once: a threshold in this set is never proposed again, whatever
	// the total does afterwards.
	Crossed []ThresholdID
	// UpdatedAt is the RecordedAt of the contribution at LastSeq — decision
	// time, never a clock reading — and zero when LastSeq is zero.
	UpdatedAt time.Time
}

// Empty is the snapshot of an accumulator nothing has contributed to.
//
// The zero total is written "0" rather than at any currency's minor-unit
// scale. Scale is semantic (ADR-0011 §2.2) and is carried by the amounts
// contributed; an empty total asserts nothing about precision, and the first
// addition takes the scale of the first contribution — the same answer
// Rebuild reaches from an empty log, which is the property that matters.
func Empty(ref Ref) (Snapshot, error) {
	if err := ref.Validate(); err != nil {
		return Snapshot{}, err
	}
	zero, err := fiscal.ParseMoney("0", ref.Currency)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{Key: ref.Key, Total: zero, Crossed: []ThresholdID{}}, nil
}

// Validate refuses a snapshot whose fields contradict one another. The
// adapter calls it on every row it reads, so a snapshot corrupted at rest is
// caught before Apply builds on it.
func (s Snapshot) Validate() error {
	if err := s.Key.Validate(); err != nil {
		return err
	}
	switch {
	case s.Total.Currency() == "":
		return integrity(s.Key, "has no total")
	case s.LastSeq < 0:
		return integrity(s.Key, fmt.Sprintf("has negative last_seq %d", s.LastSeq))
	case (s.LastSeq == 0) != s.UpdatedAt.IsZero():
		return integrity(s.Key, "has a last-contribution time that disagrees with its sequence")
	}
	for i, t := range s.Crossed {
		if err := t.Validate(); err != nil {
			return err
		}
		if i > 0 && s.Crossed[i-1] >= t {
			return integrity(s.Key, "has a crossed-threshold set that is not sorted and unique")
		}
	}
	return nil
}

// HasCrossed reports whether threshold t has already been emitted for this
// key.
func (s Snapshot) HasCrossed(t ThresholdID) bool {
	_, found := slices.BinarySearch(s.Crossed, t)
	return found
}

// Ref returns the reference that names this snapshot.
func (s Snapshot) Ref() Ref { return Ref{Key: s.Key, Currency: s.Total.Currency()} }

// Observation is a snapshot read without the lock, for a quote (ADR-0004
// §2.7).
//
// It may be stale by whatever is in flight, and it is a separate type so that
// it cannot be passed where a locked Snapshot is required: a quote is an
// estimate, only a commit is a decision. ObservedSeq is what the quote
// response reports beside its non-authoritative marker, so a caller can tell
// which point in the log the estimate was taken against.
type Observation struct {
	Key         Key
	Total       fiscal.Money
	ObservedSeq int64
}

// Authoritative is always false. It exists so that the quote path states the
// fact at the point it renders the marker, rather than a reader inferring it
// from the type name.
func (Observation) Authoritative() bool { return false }

// integrity reports a snapshot or log that contradicts the invariants of
// ADR-0004 §2.5. That is a defect in what is stored, never in the request, so
// it is CategoryInternal: the remedy is the rebuild runbook (control 2), not a
// retry.
func integrity(k Key, what string) error {
	return errs.New(errs.CategoryInternal, errs.ReasonInternal,
		fmt.Sprintf("Accumulator %s %s.", k, what))
}
