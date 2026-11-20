package erp

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/subledger"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// Status is where an export batch stands (LED-23 "Transfer state").
type Status string

// The statuses. WITHDRAWN is the one this package adds to LED-23's six: a
// batch that was prepared and never dispatched, released so its journals can
// be prepared again under a new mapping.
const (
	StatusPrepared       Status = "PREPARED"
	StatusTransmitted    Status = "TRANSMITTED"
	StatusAcknowledged   Status = "ACKNOWLEDGED"
	StatusERPPosted      Status = "ERP_POSTED"
	StatusRejected       Status = "REJECTED"
	StatusOutcomeUnknown Status = "OUTCOME_UNKNOWN"
	StatusWithdrawn      Status = "WITHDRAWN"
)

// Valid reports whether the status is one of the seven.
func (s Status) Valid() bool {
	switch s {
	case StatusPrepared, StatusTransmitted, StatusAcknowledged, StatusERPPosted, StatusRejected, StatusOutcomeUnknown, StatusWithdrawn:
		return true
	}
	return false
}

// Live reports whether a batch in this status holds its journals: they are
// not prepared into another batch while it does. A rejected or withdrawn
// batch releases them.
func (s Status) Live() bool { return s != StatusRejected && s != StatusWithdrawn }

// EventKind is what happened to a batch.
type EventKind string

// The events. PREPARED is always the first; DISPATCHED is ZoikoTax's own act;
// the five after it are the ERP's answers as a person recorded them;
// WITHDRAWN releases a batch that was never sent.
const (
	EventPrepared       EventKind = "PREPARED"
	EventDispatched     EventKind = "DISPATCHED"
	EventAcknowledged   EventKind = "ACKNOWLEDGED"
	EventPosted         EventKind = "POSTED"
	EventRejected       EventKind = "REJECTED"
	EventOutcomeUnknown EventKind = "OUTCOME_UNKNOWN"
	EventStatusNotFound EventKind = "STATUS_NOT_FOUND"
	EventWithdrawn      EventKind = "WITHDRAWN"
)

// Response reports whether the kind is an ERP answer a person records.
func (k EventKind) Response() bool {
	switch k {
	case EventAcknowledged, EventPosted, EventRejected, EventOutcomeUnknown, EventStatusNotFound:
		return true
	}
	return false
}

// Event is one appended act on a batch: the delivery evidence LED-23 asks
// for, attempt by attempt.
type Event struct {
	Seq    int
	Kind   EventKind
	Status Status
	// Attempt names the dispatch attempt: the attempt a DISPATCHED event
	// made, or the one an ERP answer is about. Zero on PREPARED and
	// WITHDRAWN.
	Attempt id.ERPAttemptID
	// RemoteCorrelation is the ERP's own message, receipt or query
	// reference.
	RemoteCorrelation string
	// ERPReference is the ERP's posted GL journal or document number. A
	// POSTED answer carries it; it is what a GL comparison relies on, not
	// the transport receipt (LED-18).
	ERPReference string
	// ResponseDigest is the checksum of the raw ERP response kept as
	// evidence elsewhere.
	ResponseDigest string
	Detail         string
	RecordedAt     time.Time
	RecordedBy     id.UserID
}

// Line is one exported line: a TCSL journal line mapped onto the customer's
// GL account, with the trace back to the journal (ZTAX-FIN-REQ-0053).
type Line struct {
	Journal      id.JournalID
	JournalLine  int // the journal's own line number, from 1
	Account      subledger.Account
	GLAccount    string
	Side         subledger.Side
	Amount       fiscal.Money
	Authority    string
	Jurisdiction string
}

// Batch is one sealed export batch.
type Batch struct {
	ID          id.ERPExportID
	TenantID    id.TenantID
	Bridge      id.ERPBridgeID
	LegalEntity id.LegalEntityID
	Period      string
	Currency    fiscal.Currency
	// PostingDate is the date the ERP is asked to post on, a calendar date.
	PostingDate time.Time

	CompanyCode    string
	MappingVersion int
	ChartVersion   string
	MappingDigest  canonical.Digest

	// Journals is the exact journal ID set, sorted.
	Journals     []id.JournalID
	Lines        []Line
	TotalDebits  fiscal.Money
	TotalCredits fiscal.Money
	// Digest seals everything above.
	Digest canonical.Digest

	PreparedAt time.Time
	PreparedBy id.UserID

	Status  Status
	History []Event
}

// ExternalRef is the reference every attempt of the batch carries to the
// ERP, and that the ERP deduplicates on. It is the batch's identity, so a
// retry is never a distinct journal.
func (b Batch) ExternalRef() string { return "ztax-erp-export:" + b.ID.String() }

// PrepareInput is what a batch is prepared from.
type PrepareInput struct {
	ID          id.ERPExportID
	TenantID    id.TenantID
	Bridge      Bridge
	Mapping     Mapping
	Period      string
	Currency    fiscal.Currency
	PostingDate time.Time
	// Journals are the posted journals to export: the bridge's legal entity,
	// the period and the currency, none already in a live batch.
	Journals   []subledger.Journal
	PreparedAt time.Time
	PreparedBy id.UserID
}

// Prepare maps the journals onto the mapping and seals the batch. A line on
// an account the mapping does not map holds the whole export
// (ERP_MAPPING_INCOMPLETE): nothing goes to a suspense account by default.
func Prepare(in PrepareInput) (Batch, error) {
	if !ValidPeriod(in.Period) {
		return Batch{}, errs.Invalid("period", errs.ReasonInvalidValue, "The period is a month, YYYY-MM.")
	}
	if !ValidCurrency(string(in.Currency)) {
		return Batch{}, errs.Invalid("currency", errs.ReasonInvalidValue, "The currency is an ISO 4217 code, such as USD.")
	}
	if in.PostingDate.IsZero() {
		return Batch{}, errs.Invalid("postingDate", errs.ReasonMissingField, "An export names the date the ERP posts on.")
	}
	if len(in.Journals) == 0 {
		return Batch{}, errs.New(errs.CategoryConflict, ReasonNothingToExport,
			fmt.Sprintf("No posted %s journal of %s is waiting for export.", in.Currency, in.Period))
	}
	journals := slices.Clone(in.Journals)
	slices.SortFunc(journals, func(a, b subledger.Journal) int { return strings.Compare(a.ID.String(), b.ID.String()) })

	zero, err := fiscal.ParseMoney("0", in.Currency)
	if err != nil {
		return Batch{}, err
	}
	b := Batch{
		ID: in.ID, TenantID: in.TenantID, Bridge: in.Bridge.ID, LegalEntity: in.Bridge.LegalEntity, Period: in.Period,
		Currency: in.Currency, PostingDate: dateOf(in.PostingDate), CompanyCode: in.Bridge.CompanyCode,
		MappingVersion: in.Mapping.Version, ChartVersion: in.Mapping.ChartVersion, MappingDigest: in.Mapping.Digest,
		TotalDebits: zero, TotalCredits: zero, PreparedAt: in.PreparedAt.UTC(), PreparedBy: in.PreparedBy,
	}
	unmapped := map[subledger.Account]bool{}
	for _, j := range journals {
		if j.LegalEntity != b.LegalEntity || j.LegalPeriod != b.Period || j.Currency != b.Currency {
			return Batch{}, fmt.Errorf("erp: journal %s is not a %s journal of %s for the bridge's legal entity", j.ID, b.Currency, b.Period)
		}
		b.Journals = append(b.Journals, j.ID)
		for i, l := range j.Lines {
			gl, ok := in.Mapping.GLAccountFor(l.Account)
			if !ok {
				unmapped[l.Account] = true
				continue
			}
			b.Lines = append(b.Lines, Line{
				Journal: j.ID, JournalLine: i + 1, Account: l.Account, GLAccount: gl, Side: l.Side, Amount: l.Amount,
				Authority: l.Authority, Jurisdiction: l.Jurisdiction,
			})
			if l.Side == subledger.Debit {
				b.TotalDebits, err = b.TotalDebits.Add(l.Amount)
			} else {
				b.TotalCredits, err = b.TotalCredits.Add(l.Amount)
			}
			if err != nil {
				return Batch{}, err
			}
		}
	}
	if len(unmapped) > 0 {
		names := make([]string, 0, len(unmapped))
		for a := range unmapped {
			names = append(names, string(a))
		}
		slices.Sort(names)
		return Batch{}, errs.New(errs.CategoryConflict, ReasonMappingIncomplete,
			fmt.Sprintf("Mapping version %d has no GL account for %s. The export is held; nothing was exported.",
				in.Mapping.Version, strings.Join(names, ", ")))
	}
	if c, err := b.TotalDebits.Cmp(b.TotalCredits); err != nil || c != 0 {
		return Batch{}, fmt.Errorf("erp: batch debits %s and credits %s do not balance", b.TotalDebits.CanonicalString(), b.TotalCredits.CanonicalString())
	}
	if b.Digest, err = canonical.Sum(b.Payload()); err != nil {
		return Batch{}, err
	}
	return b, nil
}

func dateOf(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}

// Payload is the canonical document the batch digest covers: the export
// mapping (company code, mapping and chart version, currency, posting date),
// the exact journal ID set and every line.
func (b Batch) Payload() canonical.Value {
	journals := make([]canonical.Value, len(b.Journals))
	for i, j := range b.Journals {
		journals[i] = canonical.String(j.String())
	}
	lines := make([]canonical.Value, len(b.Lines))
	for i, l := range b.Lines {
		lines[i] = canonical.Object(
			canonical.F("journalId", canonical.String(l.Journal.String())),
			canonical.F("journalLine", canonical.Integer(int64(l.JournalLine))),
			canonical.F("account", canonical.String(string(l.Account))),
			canonical.F("glAccount", canonical.String(l.GLAccount)),
			canonical.F("side", canonical.String(string(l.Side))),
			canonical.F("amount", canonical.Money(l.Amount)),
			canonical.F("authority", canonical.OptString(l.Authority)),
			canonical.F("jurisdiction", canonical.OptString(l.Jurisdiction)),
		)
	}
	return canonical.Object(
		canonical.F("artifact", canonical.String("erp-export-batch")),
		canonical.F("batchId", canonical.String(b.ID.String())),
		canonical.F("bridgeId", canonical.String(b.Bridge.String())),
		canonical.F("legalEntityId", canonical.String(b.LegalEntity.String())),
		canonical.F("companyCode", canonical.String(b.CompanyCode)),
		canonical.F("mappingVersion", canonical.Integer(int64(b.MappingVersion))),
		canonical.F("chartVersion", canonical.String(b.ChartVersion)),
		canonical.F("mappingDigest", canonical.String(b.MappingDigest.String())),
		canonical.F("period", canonical.String(b.Period)),
		canonical.F("currency", canonical.String(string(b.Currency))),
		canonical.F("postingDate", canonical.String(b.PostingDate.Format(time.DateOnly))),
		canonical.F("journals", canonical.Array(journals...)),
		canonical.F("lines", canonical.Array(lines...)),
		canonical.F("totalDebits", canonical.Money(b.TotalDebits)),
		canonical.F("totalCredits", canonical.Money(b.TotalCredits)),
	)
}

// Verify recomputes the digest from what the batch holds. A stored batch
// whose digest no longer matches its own contents is never dispatched.
func (b Batch) Verify() error {
	d, err := canonical.Sum(b.Payload())
	if err != nil {
		return err
	}
	if !d.Equal(b.Digest) {
		return fmt.Errorf("erp: batch %s holds contents whose digest is %s, not its sealed %s", b.ID, d, b.Digest)
	}
	return nil
}

// Attempts returns the batch's DISPATCHED events, oldest first.
func (b Batch) Attempts() []Event {
	var out []Event
	for _, e := range b.History {
		if e.Kind == EventDispatched {
			out = append(out, e)
		}
	}
	return out
}

func (b Batch) hasAttempt(a id.ERPAttemptID) bool {
	for _, e := range b.Attempts() {
		if e.Attempt == a {
			return true
		}
	}
	return false
}

func (b Batch) last() Event { return b.History[len(b.History)-1] }

// next is the state machine: the status an event moves a batch to from s,
// with prior the kind of the batch's latest event.
func next(s Status, k EventKind, prior EventKind) (Status, bool) {
	switch k {
	case EventDispatched:
		// A first dispatch, or a re-dispatch after a status query found
		// nothing under the batch's external reference.
		if s == StatusPrepared || (s == StatusOutcomeUnknown && prior == EventStatusNotFound) {
			return StatusTransmitted, true
		}
	case EventAcknowledged:
		if s == StatusTransmitted || s == StatusOutcomeUnknown {
			return StatusAcknowledged, true
		}
	case EventPosted:
		if s == StatusTransmitted || s == StatusAcknowledged || s == StatusOutcomeUnknown {
			return StatusERPPosted, true
		}
	case EventRejected:
		if s == StatusTransmitted || s == StatusAcknowledged || s == StatusOutcomeUnknown {
			return StatusRejected, true
		}
	case EventOutcomeUnknown:
		if s == StatusTransmitted || s == StatusAcknowledged {
			return StatusOutcomeUnknown, true
		}
	case EventStatusNotFound:
		if s == StatusOutcomeUnknown {
			return StatusOutcomeUnknown, true
		}
	case EventWithdrawn:
		if s == StatusPrepared {
			return StatusWithdrawn, true
		}
	}
	return "", false
}

// Prepared is the first event of a batch.
func Prepared(by id.UserID, at time.Time) Event {
	return Event{Seq: 1, Kind: EventPrepared, Status: StatusPrepared, RecordedAt: at.UTC(), RecordedBy: by}
}

// Fold applies a batch's events in order.
func Fold(b Batch, events []Event) (Batch, error) {
	if len(events) == 0 || events[0].Kind != EventPrepared || events[0].Seq != 1 || events[0].Status != StatusPrepared {
		return Batch{}, fmt.Errorf("erp: a batch's history starts with its PREPARED event")
	}
	b.Status = StatusPrepared
	prior := EventPrepared
	for i, e := range events[1:] {
		if e.Seq != i+2 {
			return Batch{}, fmt.Errorf("erp: batch event %d is out of sequence", e.Seq)
		}
		to, ok := next(b.Status, e.Kind, prior)
		if !ok || to != e.Status {
			return Batch{}, fmt.Errorf("erp: %s from %s to %s", e.Kind, b.Status, e.Status)
		}
		b.Status, prior = to, e.Kind
	}
	b.History = slices.Clone(events)
	return b, nil
}

// DispatchInput is a checker's authorisation of a dispatch.
type DispatchInput struct {
	// ExpectedDigest is the digest the checker reviewed.
	ExpectedDigest string
	// CurrentMapping is the bridge's current mapping version.
	CurrentMapping int
	Attempt        id.ERPAttemptID
	By             id.UserID
	At             time.Time
}

// Dispatch authorises the batch's dispatch, returning the DISPATCHED event
// to append. It returns false, and no event, when the batch is already
// transmitted, acknowledged or posted: a retry of the same batch is
// idempotent and never a second journal.
//
// The checker names the digest they reviewed, and is not the person who
// prepared the batch. A batch sealed under a mapping that is no longer
// current is not sent (QA-LED-068); a batch whose outcome is unknown is sent
// again only after a status query recorded that the ERP holds nothing under
// its external reference.
func (b Batch) Dispatch(in DispatchInput) (Event, bool, error) {
	if strings.TrimSpace(in.ExpectedDigest) != b.Digest.String() {
		return Event{}, false, errs.New(errs.CategoryConflict, ReasonBatchDigestMismatch,
			fmt.Sprintf("Batch %s is sealed as %s; the dispatch named %q.", b.ID, b.Digest, in.ExpectedDigest))
	}
	if in.By == b.PreparedBy {
		return Event{}, false, errs.New(errs.CategoryPolicy, ReasonMakerChecker,
			"The person who prepared this batch cannot also authorise its dispatch. Another person must.")
	}
	switch b.Status {
	case StatusTransmitted, StatusAcknowledged, StatusERPPosted:
		return Event{}, false, nil
	case StatusOutcomeUnknown:
		if b.last().Kind != EventStatusNotFound {
			return Event{}, false, errs.New(errs.CategoryConflict, ReasonOutcomeUnknown,
				fmt.Sprintf("The ERP's outcome for %s is unknown. Query it by %s and record the answer first.", b.ID, b.ExternalRef()))
		}
	case StatusPrepared:
	default:
		return Event{}, false, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("A %s batch is not dispatched. Prepare a new batch for its journals.", b.Status))
	}
	if in.CurrentMapping != b.MappingVersion {
		return Event{}, false, errs.New(errs.CategoryConflict, ReasonMappingChanged,
			fmt.Sprintf("Batch %s was sealed under mapping version %d; the bridge's current version is %d.",
				b.ID, b.MappingVersion, in.CurrentMapping))
	}
	if err := b.Verify(); err != nil {
		return Event{}, false, err
	}
	if in.Attempt.IsZero() {
		return Event{}, false, fmt.Errorf("erp: a dispatch names its attempt")
	}
	return Event{Seq: len(b.History) + 1, Kind: EventDispatched, Status: StatusTransmitted, Attempt: in.Attempt,
		RecordedAt: in.At.UTC(), RecordedBy: in.By}, true, nil
}

// Response is one ERP answer, as a person records it.
type Response struct {
	Kind              EventKind
	Attempt           id.ERPAttemptID
	RemoteCorrelation string
	ERPReference      string
	ResponseDigest    string
	Detail            string
}

func (r Response) validate() error {
	if !r.Kind.Response() {
		return errs.Invalid("kind", errs.ReasonInvalidValue, fmt.Sprintf("%q is not an ERP answer.", r.Kind))
	}
	if r.Attempt.IsZero() {
		return errs.Invalid("attemptId", errs.ReasonMissingField, "An ERP answer names the dispatch attempt it is about.")
	}
	for _, f := range []struct {
		name, value string
		required    bool
		max         int
	}{
		{"remoteCorrelation", r.RemoteCorrelation, r.Kind == EventAcknowledged || r.Kind == EventStatusNotFound, MaxRef},
		{"erpReference", r.ERPReference, r.Kind == EventPosted, MaxRef},
		{"detail", r.Detail, r.Kind == EventRejected || r.Kind == EventOutcomeUnknown, MaxDetail},
	} {
		if err := text(f.name, f.value, f.required, f.max); err != nil {
			return err
		}
	}
	if r.ResponseDigest != "" && !ValidDigestRef(r.ResponseDigest) {
		return errs.Invalid("responseDigest", errs.ReasonInvalidValue, "A response digest is up to 200 characters of a digest's alphabet.")
	}
	return nil
}

// Respond records an ERP answer, returning the event to append. The same
// answer about the same attempt again is idempotent: false, and no event.
//
// A receipt (ACKNOWLEDGED) is kept apart from a posting (POSTED, which names
// the ERP's posted journal). A rejection opens a correction task and holds
// the period's GL reconciliation. A status query that found nothing
// (STATUS_NOT_FOUND) is what allows a batch of unknown outcome to be sent
// again.
func (b Batch) Respond(r Response, by id.UserID, at time.Time) (Event, bool, error) {
	if err := r.validate(); err != nil {
		return Event{}, false, err
	}
	if !b.hasAttempt(r.Attempt) {
		return Event{}, false, errs.New(errs.CategoryNotFound, errs.ReasonNotFound,
			fmt.Sprintf("Batch %s has no dispatch attempt %s.", b.ID, r.Attempt))
	}
	if l := b.last(); l.Kind == r.Kind && l.Attempt == r.Attempt && l.RemoteCorrelation == r.RemoteCorrelation &&
		l.ERPReference == r.ERPReference && l.ResponseDigest == r.ResponseDigest {
		return Event{}, false, nil
	}
	to, ok := next(b.Status, r.Kind, b.last().Kind)
	if !ok {
		return Event{}, false, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("A %s answer does not apply to a %s batch.", r.Kind, b.Status))
	}
	return Event{Seq: len(b.History) + 1, Kind: r.Kind, Status: to, Attempt: r.Attempt,
		RemoteCorrelation: r.RemoteCorrelation, ERPReference: r.ERPReference, ResponseDigest: r.ResponseDigest,
		Detail: r.Detail, RecordedAt: at.UTC(), RecordedBy: by}, true, nil
}

// Withdraw releases a prepared batch that was never dispatched, with the
// reason, so its journals can be prepared again.
func (b Batch) Withdraw(reason string, by id.UserID, at time.Time) (Event, error) {
	if b.Status != StatusPrepared {
		return Event{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("Only a prepared batch is withdrawn; this one is %s.", b.Status))
	}
	if strings.TrimSpace(reason) == "" {
		return Event{}, errs.Invalid("reason", errs.ReasonMissingField, "A withdrawal gives its reason.")
	}
	if len(reason) > MaxDetail {
		return Event{}, errs.Invalid("reason", errs.ReasonInvalidValue, fmt.Sprintf("A reason is at most %d characters.", MaxDetail))
	}
	return Event{Seq: len(b.History) + 1, Kind: EventWithdrawn, Status: StatusWithdrawn, Detail: reason,
		RecordedAt: at.UTC(), RecordedBy: by}, nil
}

// CorrectionTask is the task an ERP rejection opens (LED-23 "ERP posting
// failure"): the rejected batch's journals that no posted batch has carried
// into the GL since. It is open while any is outstanding.
type CorrectionTask struct {
	Batch       id.ERPExportID
	OpenedAt    time.Time
	Reason      string
	Outstanding []id.JournalID
	// PostedBy are the posted batches that carried the rejected journals.
	PostedBy []id.ERPExportID
}

// Open reports whether the task still has journals outstanding.
func (t CorrectionTask) Open() bool { return len(t.Outstanding) > 0 }

// CorrectionTasks derives the correction task of every rejected batch from
// the bridge's batches.
func CorrectionTasks(batches []Batch) []CorrectionTask {
	posted := map[id.JournalID]id.ERPExportID{}
	for _, b := range batches {
		if b.Status == StatusERPPosted {
			for _, j := range b.Journals {
				posted[j] = b.ID
			}
		}
	}
	var out []CorrectionTask
	for _, b := range batches {
		if b.Status != StatusRejected {
			continue
		}
		rejection := b.last()
		t := CorrectionTask{Batch: b.ID, OpenedAt: rejection.RecordedAt, Reason: rejection.Detail}
		seen := map[id.ERPExportID]bool{}
		for _, j := range b.Journals {
			by, ok := posted[j]
			if !ok {
				t.Outstanding = append(t.Outstanding, j)
				continue
			}
			if !seen[by] {
				seen[by] = true
				t.PostedBy = append(t.PostedBy, by)
			}
		}
		out = append(out, t)
	}
	return out
}

// Filter narrows a listing of batches. Empty fields do not filter.
type Filter struct {
	Period   string
	Currency fiscal.Currency
	Status   Status
}
