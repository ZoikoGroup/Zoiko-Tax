// Package migration models bringing a customer's history into ZoikoTax
// (ZTAX-MIG-001, W4 lane M).
//
// Three rules shape it:
//
//   - Preserve before transform. A source snapshot is immutable and hashed;
//     every converted record keeps its source identifier in a crosswalk, and
//     a source code is never rewritten into canonical truth
//     (ZTAX-MIG-001 §1, §9).
//   - History as it was. Conversion is AS_KNOWN: the amounts a legacy system
//     recorded are carried as recorded, never recalculated under today's
//     content (the historical rule, §1).
//   - Nothing is forced. An unmapped code, a duplicate, a bad date or a
//     currency conflict is an exception with an owner and a disposition,
//     never an OTHER bucket (§15); and completeness is reconciled by
//     population and value, not by counting inserts (§16).
//
// Cutover is a governed state machine (§26). An aggregate percentage never
// authorizes it: an open high-severity exception or a failed reconciliation
// blocks the switch whatever share of records converted.
package migration

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// ReasonCutoverBlocked is a switch that readiness or authorization refuses.
var ReasonCutoverBlocked = errs.Register("CUTOVER_BLOCKED", errs.CategoryPolicy,
	"lane-m",
	"The cutover may not proceed: a readiness gate failed or it lacks independent authorization. The detail says which.")

// ---------------------------------------------------------------------------
// programs
// ---------------------------------------------------------------------------

// Type is what kind of migration a program is (ZTAX-MIG-001 §3).
type Type string

// Types are the migration types §3 defines.
var Types = []Type{
	"GREENFIELD_ONBOARD", "CONFIGURATION_MIGRATION", "HISTORICAL_DECISION_MIGRATION", "FULL_LEDGER_MIGRATION",
	"INCUMBENT_ENGINE_REPLACEMENT", "BSS_ERP_TRANSFORMATION", "COUNTRY_PACK_EXPANSION", "MERGER_ACQUISITION",
	"DATA_CENTER_REGION_MOVE", "EXIT_EXPORT",
}

// Valid reports whether the type is one of §3's.
func (t Type) Valid() bool { return slices.Contains(Types, t) }

// Program is one migration program and its single wave: a bounded legal
// entity and period population (ZTAX-MIG-001 §5).
type Program struct {
	ID          id.MigrationProgramID
	TenantID    id.TenantID
	Name        string
	Type        Type
	LegalEntity id.LegalEntityID
	PeriodFrom  time.Time
	PeriodTo    time.Time
	// Scale is how many decimal places the migrated amounts may carry. It is
	// declared, not assumed: currency precision is content, and a source
	// amount finer than this is a PRECISION_LOSS exception (§15).
	Scale     int
	CreatedAt time.Time
	CreatedBy id.UserID
}

// Validate refuses a program that cannot be created.
func (p Program) Validate() error {
	switch {
	case strings.TrimSpace(p.Name) == "":
		return fmt.Errorf("migration: a program has a name")
	case !p.Type.Valid():
		return fmt.Errorf("migration: type %q is not one this service knows", p.Type)
	case p.LegalEntity.IsZero():
		return fmt.Errorf("migration: a program names the legal entity it migrates")
	case !p.PeriodTo.After(p.PeriodFrom):
		return fmt.Errorf("migration: the period ends after it starts")
	case p.Scale < 0 || p.Scale > 6:
		return fmt.Errorf("migration: the amount scale is between 0 and 6 decimal places")
	}
	return nil
}

// ---------------------------------------------------------------------------
// snapshots
// ---------------------------------------------------------------------------

// SourceRecord is one historical tax result as the source system held it.
// Amounts are kept as the decimal strings the source gave, so a precision
// the target cannot hold is detected, not silently rounded (§15).
type SourceRecord struct {
	SourceID     string
	DocumentDate string
	Currency     string
	Total        string
	Components   map[string]string
}

// Snapshot is an immutable extraction (ZTAX-MIG-001 §7).
type Snapshot struct {
	ID            id.MigrationSnapshotID
	TenantID      id.TenantID
	Program       id.MigrationProgramID
	SourceSystem  string
	SourceVersion string
	Method        string
	Watermark     string
	Records       []SourceRecord
	// Hash fingerprints the records in source-identifier order; Count and
	// ControlTotals are what the source said it held, by currency.
	Hash          canonical.Digest
	Count         int
	ControlTotals map[string]string
	CreatedAt     time.Time
	CreatedBy     id.UserID
}

// Methods are the extraction methods (ZTAX-MIG-001 §7).
var Methods = []string{"API", "DB_EXPORT", "OBJECT_EXPORT", "FILE", "PROVIDER_EXPORT", "CONTROLLED_MANUAL"}

var decimalShape = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?$`)

// Seal validates the snapshot's shape and computes its hash. The control
// totals are the source's own declaration and are reconciled later, not
// computed here — a snapshot whose records do not add up to what its source
// declared is a reconciliation failure, not a snapshot to correct.
func (s *Snapshot) Seal() error {
	switch {
	case strings.TrimSpace(s.SourceSystem) == "":
		return fmt.Errorf("migration: a snapshot names its source system")
	case !slices.Contains(Methods, s.Method):
		return fmt.Errorf("migration: extraction method %q is not one this service knows", s.Method)
	case len(s.Records) == 0:
		return fmt.Errorf("migration: a snapshot holds at least one record")
	}
	for cur, total := range s.ControlTotals {
		if !currencyShape.MatchString(cur) || !decimalShape.MatchString(total) {
			return fmt.Errorf("migration: control total %s %q is not a currency and a decimal", cur, total)
		}
	}
	sorted := append([]SourceRecord{}, s.Records...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].SourceID < sorted[j].SourceID })
	items := make([]canonical.Value, len(sorted))
	for i, r := range sorted {
		codes := make([]string, 0, len(r.Components))
		for c := range r.Components {
			codes = append(codes, c)
		}
		sort.Strings(codes)
		comps := make([]canonical.Field, len(codes))
		for j, c := range codes {
			comps[j] = canonical.F(c, canonical.String(r.Components[c]))
		}
		items[i] = canonical.Object(
			canonical.F("sourceId", canonical.String(r.SourceID)),
			canonical.F("documentDate", canonical.String(r.DocumentDate)),
			canonical.F("currency", canonical.String(r.Currency)),
			canonical.F("total", canonical.String(r.Total)),
			canonical.F("components", canonical.Object(comps...)),
		)
	}
	h, err := canonical.Sum(canonical.Array(items...))
	if err != nil {
		return err
	}
	s.Hash, s.Count = h, len(s.Records)
	return nil
}

var currencyShape = regexp.MustCompile(`^[A-Z]{3}$`)

// ---------------------------------------------------------------------------
// mappings
// ---------------------------------------------------------------------------

// Mapping is one version of a program's legacy-code mapping (§10, §11).
type Mapping struct {
	ID        id.MigrationMappingID
	TenantID  id.TenantID
	Program   id.MigrationProgramID
	Version   int
	Codes     map[string]string
	CreatedAt time.Time
	CreatedBy id.UserID
}

// Validate refuses an empty or malformed mapping.
func (m Mapping) Validate() error {
	if len(m.Codes) == 0 {
		return fmt.Errorf("migration: a mapping maps at least one legacy code")
	}
	for code, slot := range m.Codes {
		if strings.TrimSpace(code) == "" || strings.TrimSpace(slot) == "" {
			return fmt.Errorf("migration: a mapping names both codes")
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// runs
// ---------------------------------------------------------------------------

// ExceptionKind is a migration exception (ZTAX-MIG-001 §15).
type ExceptionKind string

// The exception kinds this conversion raises.
const (
	ExUnmappedCode     ExceptionKind = "UNMAPPED_CODE"
	ExDuplicateSource  ExceptionKind = "DUPLICATE_SOURCE"
	ExPrecisionLoss    ExceptionKind = "PRECISION_LOSS"
	ExDateConflict     ExceptionKind = "DATE_CONFLICT"
	ExCurrencyConflict ExceptionKind = "CURRENCY_CONFLICT"
	ExControlTotal     ExceptionKind = "CONTROL_TOTAL_MISMATCH"
	ExMissingSource    ExceptionKind = "MISSING_SOURCE"
	ExComponentTotal   ExceptionKind = "INVALID_RELATION"
)

// Severity is an exception's weight. HIGH blocks cutover until it is
// dispositioned.
type Severity string

// The severities.
const (
	SeverityHigh   Severity = "HIGH"
	SeverityMedium Severity = "MEDIUM"
	SeverityLow    Severity = "LOW"
)

// Disposition is what was decided about an exception (§15).
type Disposition string

// The dispositions.
const (
	DispositionOpen     Disposition = "OPEN"
	DispositionResolved Disposition = "RESOLVED"
	// DispositionResidual is a KnownMigrationResidual: accepted for cutover
	// with its reason and impact, never treated as converted.
	DispositionResidual Disposition = "ACCEPTED_RESIDUAL"
)

// Exception is one record or control the conversion could not carry, with
// its current disposition.
type Exception struct {
	ID          id.MigrationExceptionID
	Seq         int
	Kind        ExceptionKind
	Severity    Severity
	SourceID    string
	Detail      string
	Disposition Disposition
	Note        string
	DecidedAt   time.Time
	DecidedBy   id.UserID
}

// Converted is one record carried into ZoikoTax as it was known: a new
// canonical identity, its crosswalk to the source identifier, and the
// source's amounts under mapped slots.
type Converted struct {
	Seq          int
	SourceID     string
	CanonicalID  id.MigratedRecordID
	DocumentDate time.Time
	Total        fiscal.Money
	Components   map[string]fiscal.Money
}

// Reconciliation is a run's reconciliation (ZTAX-MIG-001 §16): each stage
// reported separately; no single "100% migrated".
type Reconciliation struct {
	// R2: source records, converted, and excepted. Converted plus excepted
	// records account for every source record.
	SourceCount    int
	ConvertedCount int
	ExceptedCount  int
	// R3: by currency, what the source declared, what the snapshot's
	// records sum to, and what was converted.
	Totals []CurrencyTotal
	// R9: every converted record resolves back to exactly one source id.
	CrosswalkComplete bool
}

// CurrencyTotal is one currency's monetary reconciliation.
type CurrencyTotal struct {
	Currency  string
	Declared  *fiscal.Money
	Source    fiscal.Money
	Converted fiscal.Money
	Matches   bool
}

// Passed reports whether every reconciliation stage holds.
func (r Reconciliation) Passed() bool {
	if r.ConvertedCount+r.ExceptedCount != r.SourceCount || !r.CrosswalkComplete {
		return false
	}
	for _, t := range r.Totals {
		if !t.Matches {
			return false
		}
	}
	return true
}

// Run is one reproducible conversion of a snapshot under a mapping.
type Run struct {
	ID         id.MigrationRunID
	TenantID   id.TenantID
	Program    id.MigrationProgramID
	Snapshot   id.MigrationSnapshotID
	Mapping    id.MigrationMappingID
	Mode       string
	Records    []Converted
	Exceptions []Exception
	Recon      Reconciliation
	CreatedAt  time.Time
	CreatedBy  id.UserID
}

// ModeAsKnown converts history exactly as the source recorded it.
const ModeAsKnown = "AS_KNOWN"

// Convert runs a snapshot through a mapping. newRecord issues canonical
// identities; newException issues exception identities. It is
// deterministic for a given snapshot, mapping and identity sequence.
func Convert(p Program, s Snapshot, m Mapping, newRecord func() (id.MigratedRecordID, error),
	newException func() (id.MigrationExceptionID, error)) ([]Converted, []Exception, Reconciliation, error) {
	var (
		converted  []Converted
		exceptions []Exception
		seen       = map[string]bool{}
		sources    = map[string]fiscal.Money{}
		done       = map[string]fiscal.Money{}
	)
	raise := func(kind ExceptionKind, sev Severity, sourceID, detail string) error {
		eid, err := newException()
		if err != nil {
			return err
		}
		exceptions = append(exceptions, Exception{ID: eid, Seq: len(exceptions) + 1, Kind: kind, Severity: sev,
			SourceID: sourceID, Detail: detail, Disposition: DispositionOpen})
		return nil
	}
	add := func(into map[string]fiscal.Money, m fiscal.Money) error {
		cur := string(m.Currency())
		if prev, ok := into[cur]; ok {
			next, err := prev.Add(m)
			if err != nil {
				return err
			}
			into[cur] = next
			return nil
		}
		into[cur] = m
		return nil
	}
	excepted := 0

	recs := append([]SourceRecord{}, s.Records...)
	sort.SliceStable(recs, func(i, j int) bool { return recs[i].SourceID < recs[j].SourceID })
	for _, r := range recs {
		fail := func(kind ExceptionKind, sev Severity, detail string) error {
			excepted++
			return raise(kind, sev, r.SourceID, detail)
		}
		if strings.TrimSpace(r.SourceID) == "" {
			if err := fail(ExMissingSource, SeverityHigh, "A source record has no identifier; it cannot be crosswalked."); err != nil {
				return nil, nil, Reconciliation{}, err
			}
			continue
		}
		if seen[r.SourceID] {
			if err := fail(ExDuplicateSource, SeverityHigh, fmt.Sprintf("Source id %s appears more than once.", r.SourceID)); err != nil {
				return nil, nil, Reconciliation{}, err
			}
			continue
		}
		seen[r.SourceID] = true
		if !currencyShape.MatchString(r.Currency) {
			if err := fail(ExCurrencyConflict, SeverityHigh, fmt.Sprintf("Currency %q is not an ISO 4217 code.", r.Currency)); err != nil {
				return nil, nil, Reconciliation{}, err
			}
			continue
		}
		cur := fiscal.Currency(r.Currency)
		total, err := parseExact(r.Total, cur, p.Scale)
		if err != nil {
			if err := fail(ExPrecisionLoss, SeverityHigh, fmt.Sprintf("Total %q: %v", r.Total, err)); err != nil {
				return nil, nil, Reconciliation{}, err
			}
			continue
		}
		if err := add(sources, total); err != nil {
			return nil, nil, Reconciliation{}, err
		}
		date, err := time.Parse(time.DateOnly, r.DocumentDate)
		if err != nil || date.Before(p.PeriodFrom) || !date.Before(p.PeriodTo) {
			if err := fail(ExDateConflict, SeverityMedium,
				fmt.Sprintf("Document date %q is not a date inside the program's period.", r.DocumentDate)); err != nil {
				return nil, nil, Reconciliation{}, err
			}
			continue
		}
		comps := map[string]fiscal.Money{}
		var problem string
		var problemKind ExceptionKind
		sum, err := fiscal.ParseMoney("0", cur)
		if err != nil {
			return nil, nil, Reconciliation{}, err
		}
		codes := make([]string, 0, len(r.Components))
		for c := range r.Components {
			codes = append(codes, c)
		}
		sort.Strings(codes)
		for _, code := range codes {
			slot, ok := m.Codes[code]
			if !ok {
				problem, problemKind = fmt.Sprintf("Legacy code %s has no approved mapping.", code), ExUnmappedCode
				break
			}
			amount, err := parseExact(r.Components[code], cur, p.Scale)
			if err != nil {
				problem, problemKind = fmt.Sprintf("Component %s %q: %v", code, r.Components[code], err), ExPrecisionLoss
				break
			}
			if prev, ok := comps[slot]; ok {
				if amount, err = prev.Add(amount); err != nil {
					return nil, nil, Reconciliation{}, err
				}
			}
			comps[slot] = amount
			if sum, err = sum.Add(amount); err != nil {
				return nil, nil, Reconciliation{}, err
			}
		}
		if problem == "" {
			if c, err := sum.Cmp(total); err != nil {
				return nil, nil, Reconciliation{}, err
			} else if c != 0 {
				problem, problemKind = fmt.Sprintf("Components sum to %s, the total is %s.", sum.CanonicalString(), total.CanonicalString()), ExComponentTotal
			}
		}
		if problem != "" {
			sev := SeverityHigh
			if problemKind == ExUnmappedCode {
				sev = SeverityMedium
			}
			if err := fail(problemKind, sev, problem); err != nil {
				return nil, nil, Reconciliation{}, err
			}
			continue
		}
		cid, err := newRecord()
		if err != nil {
			return nil, nil, Reconciliation{}, err
		}
		converted = append(converted, Converted{Seq: len(converted) + 1, SourceID: r.SourceID, CanonicalID: cid,
			DocumentDate: date, Total: total, Components: comps})
		if err := add(done, total); err != nil {
			return nil, nil, Reconciliation{}, err
		}
	}

	rec := Reconciliation{SourceCount: len(s.Records), ConvertedCount: len(converted), ExceptedCount: excepted,
		CrosswalkComplete: true}
	ids := map[string]bool{}
	for _, c := range converted {
		if c.SourceID == "" || ids[c.SourceID] || c.CanonicalID.IsZero() {
			rec.CrosswalkComplete = false
		}
		ids[c.SourceID] = true
	}
	currencies := map[string]bool{}
	for c := range sources {
		currencies[c] = true
	}
	for c := range s.ControlTotals {
		currencies[c] = true
	}
	ordered := make([]string, 0, len(currencies))
	for c := range currencies {
		ordered = append(ordered, c)
	}
	sort.Strings(ordered)
	for _, c := range ordered {
		cur := fiscal.Currency(c)
		zero, err := fiscal.ParseMoney("0", cur)
		if err != nil {
			return nil, nil, Reconciliation{}, err
		}
		t := CurrencyTotal{Currency: c, Source: zero, Converted: zero}
		if v, ok := sources[c]; ok {
			t.Source = v
		}
		if v, ok := done[c]; ok {
			t.Converted = v
		}
		match, err := equal(t.Source, t.Converted)
		if err != nil {
			return nil, nil, Reconciliation{}, err
		}
		if declared, ok := s.ControlTotals[c]; ok {
			d, err := fiscal.ParseMoney(declared, cur)
			if err != nil {
				return nil, nil, Reconciliation{}, err
			}
			t.Declared = &d
			agree, err := equal(d, t.Source)
			if err != nil {
				return nil, nil, Reconciliation{}, err
			}
			match = match && agree
			if !agree {
				if err := raise(ExControlTotal, SeverityHigh, "", fmt.Sprintf("The source declared %s %s; its records sum to %s.",
					d.CanonicalString(), c, t.Source.CanonicalString())); err != nil {
					return nil, nil, Reconciliation{}, err
				}
			}
		}
		t.Matches = match
		rec.Totals = append(rec.Totals, t)
	}
	return converted, exceptions, rec, nil
}

func equal(a, b fiscal.Money) (bool, error) {
	c, err := a.Cmp(b)
	return err == nil && c == 0, err
}

// parseExact reads a source amount without losing precision: an amount
// with more decimal places than its currency carries is refused rather than
// rounded (ZTAX-MIG-001 §15 PRECISION_LOSS).
func parseExact(s string, cur fiscal.Currency, scale int) (fiscal.Money, error) {
	if !decimalShape.MatchString(s) {
		return fiscal.Money{}, fmt.Errorf("not a decimal")
	}
	if i := strings.IndexByte(s, '.'); i >= 0 && len(s)-i-1 > scale {
		return fiscal.Money{}, fmt.Errorf("carries more than the program's %d decimal places", scale)
	}
	return fiscal.ParseMoney(s, cur)
}

// Decide records a decision about an exception. Accepting a residual
// needs a reason, and an exception is decided once (§15).
func (e Exception) Decide(d Disposition, note string, by id.UserID, at time.Time) (Exception, error) {
	if e.Disposition != DispositionOpen {
		return Exception{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("The exception is already %s.", e.Disposition))
	}
	if d != DispositionResolved && d != DispositionResidual {
		return Exception{}, errs.Invalid("disposition", errs.ReasonInvalidValue, "An exception is RESOLVED or ACCEPTED_RESIDUAL.")
	}
	if strings.TrimSpace(note) == "" {
		return Exception{}, errs.Invalid("note", errs.ReasonMissingField, "A disposition says what was done, or why the residual is accepted.")
	}
	e.Disposition, e.Note, e.DecidedAt, e.DecidedBy = d, note, at.UTC(), by
	return e, nil
}

// ---------------------------------------------------------------------------
// cutover
// ---------------------------------------------------------------------------

// CutoverState is where a program's cutover is (ZTAX-MIG-001 §26).
type CutoverState string

// The cutover states.
const (
	CutoverPlanned        CutoverState = "PLANNED"
	CutoverPrecheck       CutoverState = "PRECHECK"
	CutoverFreeze         CutoverState = "FREEZE_ACTIVE"
	CutoverFinalDelta     CutoverState = "FINAL_DELTA"
	CutoverReconciling    CutoverState = "RECONCILING"
	CutoverReady          CutoverState = "READY_TO_SWITCH"
	CutoverSwitching      CutoverState = "ROUTING_SWITCH"
	CutoverValidating     CutoverState = "VALIDATING_LIVE"
	CutoverComplete       CutoverState = "CUTOVER_COMPLETE"
	CutoverAborted        CutoverState = "ABORTED"
	CutoverRollback       CutoverState = "ROLLBACK_ACTIVE"
	CutoverRolledBack     CutoverState = "ROLLED_BACK"
	CutoverManualRecovery CutoverState = "FAILED_MANUAL_RECOVERY"
)

// cutoverTransitions is §26 as a table. Abort is before the switch;
// rollback is after it; they are distinct procedures (§27).
var cutoverTransitions = map[CutoverState][]CutoverState{
	CutoverPlanned:        {CutoverPrecheck, CutoverAborted},
	CutoverPrecheck:       {CutoverFreeze, CutoverAborted},
	CutoverFreeze:         {CutoverFinalDelta, CutoverAborted},
	CutoverFinalDelta:     {CutoverReconciling, CutoverAborted},
	CutoverReconciling:    {CutoverReady, CutoverFinalDelta, CutoverAborted},
	CutoverReady:          {CutoverSwitching, CutoverAborted},
	CutoverSwitching:      {CutoverValidating, CutoverRollback},
	CutoverValidating:     {CutoverComplete, CutoverRollback},
	CutoverComplete:       {CutoverRollback},
	CutoverRollback:       {CutoverRolledBack, CutoverManualRecovery},
	CutoverAborted:        nil,
	CutoverRolledBack:     nil,
	CutoverManualRecovery: nil,
}

// CanCutover reports whether a cutover move is permitted.
func CanCutover(from, to CutoverState) bool { return slices.Contains(cutoverTransitions[from], to) }

// CutoverEvent is one recorded cutover move.
type CutoverEvent struct {
	Seq        int
	State      CutoverState
	Note       string
	RecordedAt time.Time
	RecordedBy id.UserID
}

// Readiness is what a switch needs from the program's latest run.
type Readiness struct {
	HasRun     bool
	Reconciled bool
	OpenHigh   int
	OpenOther  int
	Residuals  int
}

// Blockers lists why the program may not switch (ZTAX-MIG-001 §24).
func (r Readiness) Blockers() []string {
	var out []string
	if !r.HasRun {
		out = append(out, "No migration run has been executed.")
		return out
	}
	if !r.Reconciled {
		out = append(out, "The latest run does not reconcile: counts, control totals or the crosswalk disagree.")
	}
	if r.OpenHigh > 0 {
		out = append(out, fmt.Sprintf("%d high-severity exception(s) are open.", r.OpenHigh))
	}
	return out
}

// NextCutover returns the event that moves a cutover to the given state. The
// switch to READY_TO_SWITCH and to ROUTING_SWITCH needs readiness with no
// blockers, and the switch itself needs someone other than the program's
// creator (§24, §29).
func NextCutover(history []CutoverEvent, to CutoverState, p Program, r Readiness, note string, by id.UserID, at time.Time) (CutoverEvent, error) {
	from := CutoverPlanned
	if len(history) > 0 {
		from = history[len(history)-1].State
	}
	if !CanCutover(from, to) {
		return CutoverEvent{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("A cutover that is %s cannot move to %s.", from, to))
	}
	if strings.TrimSpace(note) == "" {
		return CutoverEvent{}, errs.Invalid("note", errs.ReasonMissingField, "Every cutover move says why.")
	}
	if to == CutoverReady || to == CutoverSwitching {
		if b := r.Blockers(); len(b) > 0 {
			return CutoverEvent{}, errs.New(errs.CategoryPolicy, ReasonCutoverBlocked, strings.Join(b, " "))
		}
	}
	if to == CutoverSwitching && by == p.CreatedBy {
		return CutoverEvent{}, errs.New(errs.CategoryPolicy, ReasonCutoverBlocked,
			"The switch is authorized by someone other than the program's creator.")
	}
	return CutoverEvent{Seq: len(history) + 1, State: to, Note: note, RecordedAt: at.UTC(), RecordedBy: by}, nil
}
