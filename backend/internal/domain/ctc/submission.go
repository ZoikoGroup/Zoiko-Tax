package ctc

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/document"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// Authority submissions (Doc 21 W14 Submission Workbench & Dispatch
// Controls, W15 Authority Results, Quarantine & Retry; §06 lifecycle).
//
// A submission is one fiscal document under one applicability record. It is
// sealed when prepared — the document's projection, the record version, the
// passing validation report — under one payload digest, and it is the
// identity every dispatch attempt carries, so a retry is never a second
// fiscal document at the authority. Its status is the fold of appended
// events: PREPARED, then DISPATCHED (authorised by someone other than the
// preparer, on the digest they reviewed), then the answers a person recorded
// with the authority's references and the digest of the raw receipt.
//
// Transport receipt, authority acceptance and authority rejection are
// separate events: a receipt leaves the authority outcome pending (A44). An
// unknown outcome is resolved by a status enquiry before anything is sent
// again (A42). A final outcome is never overwritten by a later answer (A27,
// A45). A rejection opens a correction task that the existing fiscal
// corrections flow closes with a new document naming the rejected one; the
// rejected document is never edited (A46, A47).

// SubmissionStatus is where a submission stands.
type SubmissionStatus string

// The statuses, Doc 21 §06's names.
const (
	SubPrepared          SubmissionStatus = "PREPARED"
	SubSent              SubmissionStatus = "SENT"
	SubTransportReceived SubmissionStatus = "TRANSPORT_RECEIVED"
	SubAccepted          SubmissionStatus = "AUTHORITY_ACCEPTED"
	SubRejected          SubmissionStatus = "AUTHORITY_REJECTED"
	SubUncertain         SubmissionStatus = "UNCERTAIN_EXTERNAL_STATE"
	SubWithdrawn         SubmissionStatus = "WITHDRAWN"
)

// SubmissionStatuses is every status.
var SubmissionStatuses = []SubmissionStatus{SubPrepared, SubSent, SubTransportReceived, SubAccepted, SubRejected,
	SubUncertain, SubWithdrawn}

// Valid reports whether the status is one of these.
func (s SubmissionStatus) Valid() bool { return slices.Contains(SubmissionStatuses, s) }

// Final reports whether the authority's outcome is established, or the
// submission was withdrawn before it was sent.
func (s SubmissionStatus) Final() bool {
	return s == SubAccepted || s == SubRejected || s == SubWithdrawn
}

// Live reports whether the submission holds its document and record: a
// second submission of the same document under the same record is not
// prepared while it does. Only a withdrawn submission releases them.
func (s SubmissionStatus) Live() bool { return s != SubWithdrawn }

// SubmissionEventKind is what happened to a submission.
type SubmissionEventKind string

// The events. PREPARED is always the first; DISPATCHED is ZoikoTax's own act;
// the five after it are answers a person recorded; WITHDRAWN releases a
// submission never sent.
const (
	SubEventPrepared          SubmissionEventKind = "PREPARED"
	SubEventDispatched        SubmissionEventKind = "DISPATCHED"
	SubEventTransportReceived SubmissionEventKind = "TRANSPORT_RECEIVED"
	SubEventAccepted          SubmissionEventKind = "AUTHORITY_ACCEPTED"
	SubEventRejected          SubmissionEventKind = "AUTHORITY_REJECTED"
	SubEventOutcomeUnknown    SubmissionEventKind = "OUTCOME_UNKNOWN"
	SubEventStatusNotFound    SubmissionEventKind = "STATUS_NOT_FOUND"
	SubEventWithdrawn         SubmissionEventKind = "WITHDRAWN"
)

// Response reports whether the kind is an answer a person records.
func (k SubmissionEventKind) Response() bool {
	switch k {
	case SubEventTransportReceived, SubEventAccepted, SubEventRejected, SubEventOutcomeUnknown, SubEventStatusNotFound:
		return true
	}
	return false
}

// SubmissionEvent is one appended act on a submission.
type SubmissionEvent struct {
	Seq    int
	Kind   SubmissionEventKind
	Status SubmissionStatus
	// Attempt is the dispatch attempt a DISPATCHED event made, or the one an
	// answer is about. Zero on PREPARED and WITHDRAWN.
	Attempt id.CTCAttemptID
	// TransportRef is the partner's or rail's message, receipt or enquiry
	// reference.
	TransportRef string
	// AuthorityRef is the authority's own document reference — an SdI
	// identifier, a KSeF number, an IRN, an NFCom access key.
	AuthorityRef string
	// AuthorityCode is the authority's own rejection code, kept raw.
	AuthorityCode string
	// EvidenceSHA256 is the SHA-256 of the raw receipt or response, hashed
	// where it is held. The response itself is not stored here.
	EvidenceSHA256 string
	Detail         string
	RecordedAt     time.Time
	RecordedBy     id.UserID
}

// Submission is one submission, folded.
type Submission struct {
	ID                   id.CTCSubmissionID
	TenantID             id.TenantID
	Document             id.FiscalDocumentID
	DocumentNumber       string
	DocumentType         document.Type
	LegalEntity          id.LegalEntityID
	Country              string
	BuyerKind            BuyerKind
	Applicability        id.CTCApplicabilityID
	ApplicabilityVersion int
	Model                Model
	Profile              Profile
	FormatVersion        string
	Validation           id.CTCValidationID
	Currency             fiscal.Currency
	Gross                fiscal.Money
	// PayloadDigest seals the document projection, the record version and
	// the validation report.
	PayloadDigest canonical.Digest
	PreparedAt    time.Time
	PreparedBy    id.UserID

	Status  SubmissionStatus
	History []SubmissionEvent
}

// ExternalRef is the reference every attempt carries to the partner or
// authority, and that a status enquiry asks by. It never changes across
// attempts (A42).
func (s Submission) ExternalRef() string { return "ztax-ctc:" + s.ID.String() }

// Attempts returns the DISPATCHED events, oldest first.
func (s Submission) Attempts() []SubmissionEvent {
	var out []SubmissionEvent
	for _, e := range s.History {
		if e.Kind == SubEventDispatched {
			out = append(out, e)
		}
	}
	return out
}

func (s Submission) hasAttempt(a id.CTCAttemptID) bool {
	for _, e := range s.Attempts() {
		if e.Attempt == a {
			return true
		}
	}
	return false
}

func (s Submission) last() SubmissionEvent { return s.History[len(s.History)-1] }

// Rejection is the AUTHORITY_REJECTED event, if the submission was rejected.
func (s Submission) Rejection() (SubmissionEvent, bool) {
	if s.Status != SubRejected {
		return SubmissionEvent{}, false
	}
	return s.last(), true
}

// PayloadInput is what a submission's payload digest covers.
type PayloadInput struct {
	Document      DocumentInput
	Applicability Applicability
	Validation    Validation
}

// PayloadDigest seals the document projection with the record version it is
// submitted under and the validation report that passed it.
func PayloadDigest(in PayloadInput) (canonical.Digest, error) {
	cur := in.Applicability.Current()
	return canonical.Sum(canonical.Object(
		canonical.F("artifact", canonical.String("ctc-submission-payload")),
		canonical.F("document", in.Document.Projection()),
		canonical.F("applicabilityId", canonical.String(in.Applicability.ID.String())),
		canonical.F("applicabilityVersion", canonical.Integer(int64(cur.Number))),
		canonical.F("termsDigest", canonical.String(cur.Digest.String())),
		canonical.F("validationId", canonical.String(in.Validation.ID.String())),
		canonical.F("validationDigest", canonical.String(in.Validation.Digest.String())),
	))
}

// PrepareInput is a submission to prepare.
type PrepareInput struct {
	ID            id.CTCSubmissionID
	TenantID      id.TenantID
	Document      DocumentInput
	Applicability Applicability
	// Validation is the newest validation of the document under the record,
	// if any.
	Validation *Validation
	At         time.Time
	By         id.UserID
}

// Prepare seals a submission. The record is ACTIVE, dispatches documents and
// covers the document's cell and issue date; the newest validation of the
// document under the record passed, and still speaks for the document, the
// record's current version and the current ruleset.
func Prepare(in PrepareInput) (Submission, SubmissionEvent, error) {
	a, d := in.Applicability, in.Document.Document
	if a.Status != StatusActive {
		return Submission{}, SubmissionEvent{}, errs.New(errs.CategoryConflict, ReasonApplicabilityNotActive,
			fmt.Sprintf("Record %s is %s. Nothing is submitted under it.", a.ID, a.Status))
	}
	cur := a.Current()
	if !cur.Terms.Duty.Dispatches() {
		return Submission{}, SubmissionEvent{}, errs.New(errs.CategoryConflict, ReasonSubmissionProhibited,
			fmt.Sprintf("Record %s says this document is PROHIBITED.", a.ID))
	}
	if d.LegalEntity != a.LegalEntity || d.Type != a.DocumentType {
		return Submission{}, SubmissionEvent{}, errs.New(errs.CategoryConflict, ReasonDocumentMismatch,
			fmt.Sprintf("Document %s is a %s of legal entity %s; record %s covers %ss of %s.", d.ID, d.Type, d.LegalEntity,
				a.ID, a.DocumentType, a.LegalEntity))
	}
	if !cur.Terms.InEffect(d.IssueDate) {
		return Submission{}, SubmissionEvent{}, errs.New(errs.CategoryConflict, ReasonRuleOutOfEffect,
			fmt.Sprintf("Document %s was issued %s; record %s's terms are in effect %s.", d.ID,
				d.IssueDate.UTC().Format(time.DateOnly), a.ID, window(cur.Terms)))
	}
	v := in.Validation
	if v == nil {
		return Submission{}, SubmissionEvent{}, errs.New(errs.CategoryConflict, ReasonValidationRequired,
			fmt.Sprintf("Document %s has not been validated under record %s.", d.ID, a.ID))
	}
	digest, err := in.Document.InputDigest()
	if err != nil {
		return Submission{}, SubmissionEvent{}, err
	}
	if !v.Current(digest, a) {
		return Submission{}, SubmissionEvent{}, errs.New(errs.CategoryConflict, ReasonValidationStale,
			fmt.Sprintf("Validation %s was made under version %d and ruleset %s against an earlier state of the document; the record is at version %d and the ruleset is %s.",
				v.ID, v.ApplicabilityVersion, v.Ruleset, cur.Number, Ruleset))
	}
	if v.Outcome != OutcomePassed {
		return Submission{}, SubmissionEvent{}, errs.New(errs.CategoryConflict, ReasonValidationRequired,
			fmt.Sprintf("Validation %s FAILED with %d fatal rule(s). Correct the document through the corrections flow; a fatal rule is never waived.", v.ID, v.Failures()))
	}
	pd, err := PayloadDigest(PayloadInput{Document: in.Document, Applicability: a, Validation: *v})
	if err != nil {
		return Submission{}, SubmissionEvent{}, err
	}
	at := in.At.UTC()
	s := Submission{
		ID: in.ID, TenantID: in.TenantID, Document: d.ID, DocumentNumber: d.Number, DocumentType: d.Type,
		LegalEntity: a.LegalEntity, Country: a.Country, BuyerKind: a.BuyerKind, Applicability: a.ID,
		ApplicabilityVersion: cur.Number, Model: cur.Terms.Model, Profile: cur.Terms.Profile,
		FormatVersion: cur.Terms.FormatVersion, Validation: v.ID, Currency: d.Currency, Gross: in.Document.Gross,
		PayloadDigest: pd, PreparedAt: at, PreparedBy: in.By, Status: SubPrepared,
	}
	first := SubmissionEvent{Seq: 1, Kind: SubEventPrepared, Status: SubPrepared, RecordedAt: at, RecordedBy: in.By}
	s.History = []SubmissionEvent{first}
	return s, first, nil
}

// subStep is the state machine: the status an event moves a submission to
// from s, with prior the kind of its latest event.
func subStep(s SubmissionStatus, k, prior SubmissionEventKind) (SubmissionStatus, bool) {
	switch k {
	case SubEventDispatched:
		if s == SubPrepared || (s == SubUncertain && prior == SubEventStatusNotFound) {
			return SubSent, true
		}
	case SubEventTransportReceived:
		if s == SubSent || s == SubUncertain {
			return SubTransportReceived, true
		}
	case SubEventAccepted:
		if s == SubSent || s == SubTransportReceived || s == SubUncertain {
			return SubAccepted, true
		}
	case SubEventRejected:
		if s == SubSent || s == SubTransportReceived || s == SubUncertain {
			return SubRejected, true
		}
	case SubEventOutcomeUnknown:
		if s == SubSent || s == SubTransportReceived {
			return SubUncertain, true
		}
	case SubEventStatusNotFound:
		if s == SubUncertain {
			return SubUncertain, true
		}
	case SubEventWithdrawn:
		if s == SubPrepared {
			return SubWithdrawn, true
		}
	}
	return "", false
}

// FoldSubmission applies a submission's events in order.
func FoldSubmission(s Submission, events []SubmissionEvent) (Submission, error) {
	if len(events) == 0 || events[0].Kind != SubEventPrepared || events[0].Seq != 1 || events[0].Status != SubPrepared {
		return Submission{}, fmt.Errorf("ctc: a submission's history starts with its PREPARED event")
	}
	s.Status = SubPrepared
	prior := SubEventPrepared
	for i, e := range events[1:] {
		if e.Seq != i+2 {
			return Submission{}, fmt.Errorf("ctc: submission event %d is out of sequence", e.Seq)
		}
		to, ok := subStep(s.Status, e.Kind, prior)
		if !ok || to != e.Status {
			return Submission{}, fmt.Errorf("ctc: submission %s: %s from %s to %s", s.ID, e.Kind, s.Status, e.Status)
		}
		s.Status, prior = to, e.Kind
	}
	s.History = slices.Clone(events)
	return s, nil
}

// DispatchInput is a checker's authorisation of a dispatch.
type DispatchInput struct {
	// ExpectedDigest is the payload digest the checker reviewed.
	ExpectedDigest string
	// CurrentDigest is the payload digest recomputed now from the document,
	// the record and the validation as they stand. A difference means
	// something moved after the submission was sealed.
	CurrentDigest canonical.Digest
	// Applicability is the record as it stands now.
	Applicability Applicability
	Attempt       id.CTCAttemptID
	By            id.UserID
	At            time.Time
}

// Dispatch authorises the dispatch of the sealed payload, returning the
// DISPATCHED event to append. It returns false, and no event, when the
// submission is already sent or answered: a retry is idempotent and never a
// second fiscal document.
//
// The checker names the digest they reviewed and is not the preparer. The
// record must still be ACTIVE on the version sealed (a suspension stops new
// dispatch, A60), and the payload recomputed now must match the seal. A
// submission of unknown outcome is sent again only after a status enquiry
// found nothing under its external reference.
func (s Submission) Dispatch(in DispatchInput) (SubmissionEvent, bool, error) {
	if strings.TrimSpace(in.ExpectedDigest) != s.PayloadDigest.String() {
		return SubmissionEvent{}, false, errs.New(errs.CategoryConflict, ReasonPayloadDigestMismatch,
			fmt.Sprintf("Submission %s is sealed as %s; the dispatch named %q.", s.ID, s.PayloadDigest, in.ExpectedDigest))
	}
	if in.By == s.PreparedBy {
		return SubmissionEvent{}, false, makerChecker(
			"The person who prepared this submission cannot also authorise its dispatch. Another person must.")
	}
	switch s.Status {
	case SubSent, SubTransportReceived, SubAccepted, SubRejected:
		return SubmissionEvent{}, false, nil
	case SubUncertain:
		if s.last().Kind != SubEventStatusNotFound {
			return SubmissionEvent{}, false, errs.New(errs.CategoryConflict, ReasonOutcomeUncertain,
				fmt.Sprintf("The authority's outcome for %s is unknown. Enquire by %s and record the answer first.", s.ID, s.ExternalRef()))
		}
	case SubPrepared:
	default:
		return SubmissionEvent{}, false, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("A %s submission is not dispatched.", s.Status))
	}
	a := in.Applicability
	if a.Status != StatusActive {
		return SubmissionEvent{}, false, errs.New(errs.CategoryConflict, ReasonApplicabilityNotActive,
			fmt.Sprintf("Record %s is %s. Nothing new is dispatched under it.", a.ID, a.Status))
	}
	if a.Current().Number != s.ApplicabilityVersion || !in.CurrentDigest.Equal(s.PayloadDigest) {
		return SubmissionEvent{}, false, errs.New(errs.CategoryConflict, ReasonValidationStale,
			fmt.Sprintf("Submission %s was sealed under record version %d; what it was sealed from has changed since. Withdraw it and prepare again.",
				s.ID, s.ApplicabilityVersion))
	}
	if in.Attempt.IsZero() {
		return SubmissionEvent{}, false, fmt.Errorf("ctc: a dispatch names its attempt")
	}
	return SubmissionEvent{Seq: len(s.History) + 1, Kind: SubEventDispatched, Status: SubSent, Attempt: in.Attempt,
		RecordedAt: in.At.UTC(), RecordedBy: in.By}, true, nil
}

// Response is one answer, as a person records it.
type Response struct {
	Kind           SubmissionEventKind
	Attempt        id.CTCAttemptID
	TransportRef   string
	AuthorityRef   string
	AuthorityCode  string
	EvidenceSHA256 string
	Detail         string
}

func (r Response) validate() error {
	if !r.Kind.Response() {
		return errs.Invalid("kind", errs.ReasonInvalidValue, fmt.Sprintf("%q is not an answer a person records.", r.Kind))
	}
	if r.Attempt.IsZero() {
		return errs.Invalid("attemptId", errs.ReasonMissingField, "An answer names the dispatch attempt it is about.")
	}
	for _, f := range []struct {
		name, value string
		required    bool
	}{
		{"transportRef", r.TransportRef, r.Kind == SubEventTransportReceived || r.Kind == SubEventStatusNotFound},
		{"authorityRef", r.AuthorityRef, r.Kind == SubEventAccepted},
		{"authorityCode", r.AuthorityCode, r.Kind == SubEventRejected},
	} {
		if err := ref(f.name, f.value, f.required); err != nil {
			return err
		}
	}
	needsEvidence := r.Kind == SubEventAccepted || r.Kind == SubEventRejected
	switch {
	case r.EvidenceSHA256 == "" && needsEvidence:
		return errs.Invalid("evidenceSha256", errs.ReasonMissingField,
			"An authority outcome is recorded with the SHA-256 of the authority's raw receipt or response.")
	case r.EvidenceSHA256 != "" && !ValidSHA256(r.EvidenceSHA256):
		return errs.Invalid("evidenceSha256", errs.ReasonInvalidValue, "The evidence digest is a SHA-256 in lowercase hex.")
	}
	needsDetail := r.Kind == SubEventRejected || r.Kind == SubEventOutcomeUnknown || r.Kind == SubEventStatusNotFound
	return reasonText("detail", r.Detail, needsDetail)
}

// Respond records an answer, returning the event to append. The same answer
// about the same attempt again records nothing and returns false.
//
// An answer after the authority's outcome is final is refused: an
// out-of-order or replayed answer never overwrites an acceptance or a
// rejection.
func (s Submission) Respond(r Response, by id.UserID, at time.Time) (SubmissionEvent, bool, error) {
	if err := r.validate(); err != nil {
		return SubmissionEvent{}, false, err
	}
	if !s.hasAttempt(r.Attempt) {
		return SubmissionEvent{}, false, errs.New(errs.CategoryNotFound, errs.ReasonNotFound,
			fmt.Sprintf("Submission %s has no dispatch attempt %s.", s.ID, r.Attempt))
	}
	if l := s.last(); l.Kind == r.Kind && l.Attempt == r.Attempt && l.TransportRef == r.TransportRef &&
		l.AuthorityRef == r.AuthorityRef && l.AuthorityCode == r.AuthorityCode && l.EvidenceSHA256 == r.EvidenceSHA256 {
		return SubmissionEvent{}, false, nil
	}
	if s.Status == SubAccepted || s.Status == SubRejected {
		return SubmissionEvent{}, false, errs.New(errs.CategoryConflict, ReasonFinalOutcome,
			fmt.Sprintf("Submission %s is %s; a %s answer does not change that.", s.ID, s.Status, r.Kind))
	}
	to, ok := subStep(s.Status, r.Kind, s.last().Kind)
	if !ok {
		return SubmissionEvent{}, false, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("A %s answer does not apply to a %s submission.", r.Kind, s.Status))
	}
	return SubmissionEvent{Seq: len(s.History) + 1, Kind: r.Kind, Status: to, Attempt: r.Attempt,
		TransportRef: r.TransportRef, AuthorityRef: r.AuthorityRef, AuthorityCode: r.AuthorityCode,
		EvidenceSHA256: r.EvidenceSHA256, Detail: strings.TrimSpace(r.Detail), RecordedAt: at.UTC(), RecordedBy: by}, true, nil
}

// Withdraw releases a prepared submission that was never dispatched, with
// the reason, so the document can be prepared again.
func (s Submission) Withdraw(reason string, by id.UserID, at time.Time) (SubmissionEvent, error) {
	if s.Status != SubPrepared {
		return SubmissionEvent{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("Only a prepared submission is withdrawn; this one is %s.", s.Status))
	}
	if err := reasonText("reason", reason, true); err != nil {
		return SubmissionEvent{}, err
	}
	return SubmissionEvent{Seq: len(s.History) + 1, Kind: SubEventWithdrawn, Status: SubWithdrawn,
		Detail: strings.TrimSpace(reason), RecordedAt: at.UTC(), RecordedBy: by}, nil
}

// CorrectionTask is what an authority rejection opens: the rejected document
// is corrected through the fiscal corrections flow by a new document naming
// it, never edited. The task is open until such a document exists.
type CorrectionTask struct {
	Document      id.FiscalDocumentID
	OpenedAt      time.Time
	AuthorityCode string
	Reason        string
	// CorrectedBy are the committed documents that name the rejected one as
	// their predecessor.
	CorrectedBy []id.FiscalDocumentID
}

// Open reports whether no correcting document exists yet.
func (t CorrectionTask) Open() bool { return len(t.CorrectedBy) == 0 }

// Correction derives a rejected submission's correction task from the
// document's lineage.
func (s Submission) Correction(lineage []document.Document) (CorrectionTask, bool) {
	r, ok := s.Rejection()
	if !ok {
		return CorrectionTask{}, false
	}
	t := CorrectionTask{Document: s.Document, OpenedAt: r.RecordedAt, AuthorityCode: r.AuthorityCode, Reason: r.Detail}
	for _, d := range lineage {
		if slices.Contains(d.Predecessors, s.Document) {
			t.CorrectedBy = append(t.CorrectedBy, d.ID)
		}
	}
	return t, true
}

// SubmissionFilter narrows a listing of submissions. Empty fields do not
// filter.
type SubmissionFilter struct {
	Document      id.FiscalDocumentID
	Applicability id.CTCApplicabilityID
	Status        SubmissionStatus
}
