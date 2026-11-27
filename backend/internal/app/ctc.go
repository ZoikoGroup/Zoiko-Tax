package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/ctc"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/document"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/clock"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// Global e-invoicing and continuous transaction controls (Engineering Doc 21,
// G-030). The rules are internal/domain/ctc's; this is where a country
// applicability record meets the committed fiscal documents it governs, and
// the store.
//
// The layer reads fiscal documents and writes none. A rejected submission is
// corrected through the fiscal documents' own corrections flow — a void, a
// credit note, a rebill — and the correction task here closes when a
// committed document names the rejected one as its predecessor.
//
// No authority, partner or access point is connected. A dispatch is the
// recorded, four-eyed authorisation of one attempt under the submission's
// fixed external reference; what the rail and the authority answered is
// recorded by a person, with the authority's references and the SHA-256 of
// the raw receipt. Nothing here is an authority answer ZoikoTax made up.
//
// Idempotency is natural rather than keyed: validating a document again
// under the same record version and ruleset returns the same report,
// preparing a document already live under a record returns that
// submission, a dispatch of a sent submission is a no-op, and an answer
// recorded twice is one event.

// CTCService runs the e-invoicing / CTC layer.
type CTCService struct {
	store    port.CTCRepository
	docs     port.FiscalDocumentReader
	entities port.LegalEntityRepository
	tx       port.TxManager
	clock    clock.Clock
	ids      idgen.Generator
}

// NewCTCService wires the service over the fiscal documents it reads.
func NewCTCService(store port.CTCRepository, docs port.FiscalDocumentReader, entities port.LegalEntityRepository,
	tx port.TxManager, clk clock.Clock, ids idgen.Generator) *CTCService {
	return &CTCService{store: store, docs: docs, entities: entities, tx: tx, clock: clk, ids: ids}
}

func (s *CTCService) now() time.Time { return s.clock.Now().UTC().Truncate(time.Microsecond) }

// The roles (Doc 21 §04). Operators and analysts record applicability
// research, attach evidence and validate; analysts — the tax specialists —
// sign gates, activate, return, resume and retire, never on their own
// records. Either may suspend at once. Operators prepare submissions; an
// operator or analyst other than the preparer dispatches; both record
// answers. All three fiscal roles read. ADMIN reads no fiscal record here.
var (
	ctcReaders    = []security.Role{security.RoleOperator, security.RoleAnalyst, security.RoleAuditor}
	ctcMakers     = []security.Role{security.RoleOperator, security.RoleAnalyst}
	ctcCheckers   = []security.Role{security.RoleAnalyst}
	ctcPreparers  = []security.Role{security.RoleOperator}
	ctcDispatch   = []security.Role{security.RoleOperator, security.RoleAnalyst}
	ctcResponders = []security.Role{security.RoleOperator, security.RoleAnalyst}
)

// CTCApplicabilityInput is a record to create.
type CTCApplicabilityInput struct {
	LegalEntity  id.LegalEntityID // zero for the tenant's default entity
	Country      string
	DocumentType document.Type
	BuyerKind    ctc.BuyerKind
	Terms        ctc.Terms
}

// CTCGateInput is a gate sign-off.
type CTCGateInput struct {
	Gate       ctc.Gate
	Version    int
	SignoffRef string
	Note       string
}

// CTCAction is a move of a record a person asks for.
type CTCAction string

// The actions.
const (
	CTCSubmit   CTCAction = "SUBMIT"
	CTCReturn   CTCAction = "RETURN"
	CTCActivate CTCAction = "ACTIVATE"
	CTCSuspend  CTCAction = "SUSPEND"
	CTCResume   CTCAction = "RESUME"
	CTCRetire   CTCAction = "RETIRE"
)

// Valid reports whether the action is one of these.
func (a CTCAction) Valid() bool {
	switch a {
	case CTCSubmit, CTCReturn, CTCActivate, CTCSuspend, CTCResume, CTCRetire:
		return true
	}
	return false
}

// CTCTransitionInput is a move of a record.
type CTCTransitionInput struct {
	Action CTCAction
	// Version is the terms version an activation is on.
	Version int
	Reason  string
}

// CTCSubmissionView is a submission with the correction task its rejection
// opened, if it was rejected.
type CTCSubmissionView struct {
	Submission ctc.Submission
	Correction *ctc.CorrectionTask
}

// CreateApplicability records a country applicability record in DRAFT. It
// decides nothing until it is signed off and activated.
func (s *CTCService) CreateApplicability(ctx context.Context, in CTCApplicabilityInput) (ctc.Applicability, error) {
	sc, err := requirePerson(ctx, ctcMakers...)
	if err != nil {
		return ctc.Applicability{}, err
	}
	var out ctc.Applicability
	err = s.inTx(ctx, func(ctx context.Context) error {
		le := in.LegalEntity
		if le.IsZero() {
			def, err := s.entities.Default(ctx)
			if err != nil {
				return err
			}
			le = def.ID
		} else if _, err := s.entities.ByID(ctx, le); err != nil {
			return err
		}
		aid, err := idgen.CTCApplicabilityID(s.ids)
		if err != nil {
			return internal(err, "The record could not be created.")
		}
		a, err := ctc.New(ctc.NewInput{
			ID: aid, TenantID: sc.Tenant(), CreatedBy: sc.Subject(), CreatedAt: s.now(), Terms: in.Terms,
			Cell: ctc.Cell{LegalEntity: le, Country: strings.ToUpper(strings.TrimSpace(in.Country)),
				DocumentType: in.DocumentType, BuyerKind: in.BuyerKind},
		})
		if err != nil {
			if errs.IsCategory(err, errs.CategoryValidation) {
				return err
			}
			return internal(err, "The record could not be created.")
		}
		if err := s.store.CreateApplicability(ctx, a); err != nil {
			return err
		}
		out, err = s.store.Applicability(ctx, aid)
		return err
	})
	return out, err
}

// Applicabilities lists the tenant's records.
func (s *CTCService) Applicabilities(ctx context.Context, f ctc.Filter) ([]ctc.Applicability, error) {
	if _, err := requireRoleOrSystem(ctx, ctcReaders...); err != nil {
		return nil, err
	}
	if f.Country != "" && !ctc.ValidCountry(f.Country) {
		return nil, errs.Invalid("country", errs.ReasonInvalidValue, "The country is an ISO 3166-1 alpha-2 code, such as FR.")
	}
	return s.store.Applicabilities(ctx, f)
}

// Applicability reads one record.
func (s *CTCService) Applicability(ctx context.Context, record id.CTCApplicabilityID) (ctc.Applicability, error) {
	if _, err := requireRoleOrSystem(ctx, ctcReaders...); err != nil {
		return ctc.Applicability{}, err
	}
	return s.store.Applicability(ctx, record)
}

// ReviseApplicability records the next version of a record's terms. Terms
// identical to the current version record nothing and return false.
func (s *CTCService) ReviseApplicability(ctx context.Context, record id.CTCApplicabilityID, t ctc.Terms) (ctc.Applicability, bool, error) {
	sc, err := requirePerson(ctx, ctcMakers...)
	if err != nil {
		return ctc.Applicability{}, false, err
	}
	var (
		out     ctc.Applicability
		created bool
	)
	err = s.inTx(ctx, func(ctx context.Context) error {
		if err := s.store.Lock(ctx, "record/"+record.String()); err != nil {
			return err
		}
		a, err := s.store.Applicability(ctx, record)
		if err != nil {
			return err
		}
		v, e, ok, err := a.Revise(t, sc.Subject(), s.now())
		if err != nil {
			return asDomain(err, "The terms could not be sealed.")
		}
		if ok {
			if err := s.store.AppendApplicabilityVersion(ctx, record, v); err != nil {
				return err
			}
			if err := s.store.AppendApplicabilityEvent(ctx, record, e); err != nil {
				return err
			}
			created = true
		}
		out, err = s.store.Applicability(ctx, record)
		return err
	})
	return out, created, err
}

// AddApplicabilityEvidence attaches an evidence document by its digest. The
// same document again records nothing and returns false.
func (s *CTCService) AddApplicabilityEvidence(ctx context.Context, record id.CTCApplicabilityID, doc ctc.Attachment) (ctc.Applicability, bool, error) {
	sc, err := requirePerson(ctx, ctcMakers...)
	if err != nil {
		return ctc.Applicability{}, false, err
	}
	return s.moveRecord(ctx, record, func(a ctc.Applicability) (ctc.Event, bool, error) {
		return a.AddEvidence(doc, sc.Subject(), s.now())
	})
}

// SignApplicabilityGate records a G0–G4 sign-off on the record's current
// version. A gate already signed on it records nothing and returns false.
func (s *CTCService) SignApplicabilityGate(ctx context.Context, record id.CTCApplicabilityID, in CTCGateInput) (ctc.Applicability, bool, error) {
	sc, err := requirePerson(ctx, ctcCheckers...)
	if err != nil {
		return ctc.Applicability{}, false, err
	}
	return s.moveRecord(ctx, record, func(a ctc.Applicability) (ctc.Event, bool, error) {
		return a.SignGate(in.Gate, in.Version, strings.TrimSpace(in.SignoffRef), in.Note, sc.Subject(), s.now())
	})
}

// TransitionApplicability moves a record. An activation also checks that no
// other active or suspended record holds an overlapping part of the cell.
func (s *CTCService) TransitionApplicability(ctx context.Context, record id.CTCApplicabilityID, in CTCTransitionInput) (ctc.Applicability, error) {
	if !in.Action.Valid() {
		return ctc.Applicability{}, errs.Invalid("action", errs.ReasonInvalidValue,
			"The action is SUBMIT, RETURN, ACTIVATE, SUSPEND, RESUME or RETIRE.")
	}
	roles := ctcCheckers
	if in.Action == CTCSubmit || in.Action == CTCSuspend {
		roles = ctcMakers
	}
	sc, err := requirePerson(ctx, roles...)
	if err != nil {
		return ctc.Applicability{}, err
	}
	reason := strings.TrimSpace(in.Reason)
	by, now := sc.Subject(), s.now()
	var out ctc.Applicability
	err = s.inTx(ctx, func(ctx context.Context) error {
		a, err := s.store.Applicability(ctx, record)
		if err != nil {
			return err
		}
		// The cell first, as every activation and resumption in it takes
		// it, then the record.
		if err := s.store.Lock(ctx, "cell/"+cellKey(a.Cell)); err != nil {
			return err
		}
		if err := s.store.Lock(ctx, "record/"+record.String()); err != nil {
			return err
		}
		if a, err = s.store.Applicability(ctx, record); err != nil {
			return err
		}
		var e ctc.Event
		switch in.Action {
		case CTCSubmit:
			e, err = a.Submit(by, now)
		case CTCReturn:
			e, err = a.Return(reason, by, now)
		case CTCActivate:
			if err = s.cellFree(ctx, a); err == nil {
				e, err = a.Activate(in.Version, by, now)
			}
		case CTCSuspend:
			e, err = a.Suspend(reason, by, now)
		case CTCResume:
			if err = s.cellFree(ctx, a); err == nil {
				e, err = a.Resume(reason, by, now)
			}
		case CTCRetire:
			e, err = a.Retire(reason, by, now)
		}
		if err != nil {
			return err
		}
		if err := s.store.AppendApplicabilityEvent(ctx, record, e); err != nil {
			return err
		}
		out, err = s.store.Applicability(ctx, record)
		return err
	})
	return out, err
}

func cellKey(c ctc.Cell) string {
	return strings.Join([]string{c.LegalEntity.String(), c.Country, string(c.DocumentType), string(c.BuyerKind)}, "/")
}

// cellFree refuses a record that would overlap another active or suspended
// record of the same cell (Doc 21 A03).
func (s *CTCService) cellFree(ctx context.Context, a ctc.Applicability) error {
	others, err := s.store.Applicabilities(ctx, ctc.Filter{LegalEntity: a.LegalEntity, Country: a.Country})
	if err != nil {
		return err
	}
	for _, o := range others {
		if o.Status.Held() && a.Overlaps(o) {
			return errs.New(errs.CategoryConflict, ctc.ReasonApplicabilityConflict,
				fmt.Sprintf("Record %s is %s for the same cell with overlapping dates.", o.ID, o.Status))
		}
	}
	return nil
}

func (s *CTCService) moveRecord(ctx context.Context, record id.CTCApplicabilityID,
	next func(ctc.Applicability) (ctc.Event, bool, error)) (ctc.Applicability, bool, error) {
	var (
		out   ctc.Applicability
		moved bool
	)
	err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.store.Lock(ctx, "record/"+record.String()); err != nil {
			return err
		}
		a, err := s.store.Applicability(ctx, record)
		if err != nil {
			return err
		}
		e, ok, err := next(a)
		if err != nil {
			return err
		}
		if ok {
			if err := s.store.AppendApplicabilityEvent(ctx, record, e); err != nil {
				return err
			}
			moved = true
		}
		out, err = s.store.Applicability(ctx, record)
		return err
	})
	return out, moved, err
}

// Resolve answers what a cell owes on a date: REQUIRED, OPTIONAL or
// PROHIBITED from its one active record in effect, or REVIEW_BLOCKED with the
// reason. It records nothing.
func (s *CTCService) Resolve(ctx context.Context, c ctc.Cell, date time.Time) (ctc.Resolution, error) {
	if _, err := requireRoleOrSystem(ctx, ctcReaders...); err != nil {
		return ctc.Resolution{}, err
	}
	if c.LegalEntity.IsZero() {
		def, err := s.entities.Default(ctx)
		if err != nil {
			return ctc.Resolution{}, err
		}
		c.LegalEntity = def.ID
	}
	c.Country = strings.ToUpper(strings.TrimSpace(c.Country))
	if err := c.Validate(); err != nil {
		return ctc.Resolution{}, err
	}
	if date.IsZero() {
		return ctc.Resolution{}, errs.Invalid("date", errs.ReasonMissingField, "Resolution is for a date, YYYY-MM-DD.")
	}
	records, err := s.store.Applicabilities(ctx, ctc.Filter{LegalEntity: c.LegalEntity, Country: c.Country})
	if err != nil {
		return ctc.Resolution{}, err
	}
	return ctc.Resolve(records, c, date), nil
}

// documentInput reads a committed document as the CTC layer sees it.
func (s *CTCService) documentInput(ctx context.Context, documentID id.FiscalDocumentID) (ctc.DocumentInput, error) {
	rec, history, err := s.docs.ByID(ctx, documentID)
	if err != nil {
		return ctc.DocumentInput{}, err
	}
	if len(history) == 0 {
		return ctc.DocumentInput{}, integrity(fmt.Errorf("ctc: document %s has no status history", documentID),
			"The fiscal document has no recorded status.")
	}
	return ctc.DocumentInput{Document: rec.Document, Status: history[len(history)-1].Status,
		Net: rec.Net, Tax: rec.Tax, Gross: rec.Gross}, nil
}

// Validate runs the current ruleset over a committed document under an
// active record's current terms and records the report. A report already
// made for the same document state, record version and ruleset is returned
// as it is, with false: validation is deterministic.
func (s *CTCService) Validate(ctx context.Context, documentID id.FiscalDocumentID, record id.CTCApplicabilityID) (ctc.Validation, bool, error) {
	sc, err := requirePerson(ctx, ctcMakers...)
	if err != nil {
		return ctc.Validation{}, false, err
	}
	var (
		out     ctc.Validation
		created bool
	)
	err = s.inTx(ctx, func(ctx context.Context) error {
		if err := s.store.Lock(ctx, "document/"+documentID.String()); err != nil {
			return err
		}
		a, err := s.store.Applicability(ctx, record)
		if err != nil {
			return err
		}
		// A report already made is not handed out under a record that has
		// since stopped being in force.
		if a.Status != ctc.StatusActive {
			return errs.New(errs.CategoryConflict, ctc.ReasonApplicabilityNotActive,
				fmt.Sprintf("Record %s is %s. Documents are validated under an active record.", a.ID, a.Status))
		}
		in, err := s.documentInput(ctx, documentID)
		if err != nil {
			return err
		}
		digest, err := in.InputDigest()
		if err != nil {
			return internal(err, "The document could not be digested.")
		}
		prior, err := s.store.Validations(ctx, ctc.ValidationFilter{Document: documentID, Applicability: record})
		if err != nil {
			return err
		}
		for _, v := range prior {
			if v.Current(digest, a) {
				out = v
				return nil
			}
		}
		vid, err := idgen.CTCValidationID(s.ids)
		if err != nil {
			return internal(err, "The validation could not be recorded.")
		}
		v, err := ctc.Validate(ctc.ValidateInput{ID: vid, TenantID: sc.Tenant(), Document: in, Applicability: a,
			At: s.now(), By: sc.Subject()})
		if err != nil {
			return asDomain(err, "The validation could not be run.")
		}
		if err := s.store.RecordValidation(ctx, v); err != nil {
			return err
		}
		out, created = v, true
		return nil
	})
	return out, created, err
}

// Validations lists reports, newest first.
func (s *CTCService) Validations(ctx context.Context, f ctc.ValidationFilter) ([]ctc.Validation, error) {
	if _, err := requireRoleOrSystem(ctx, ctcReaders...); err != nil {
		return nil, err
	}
	return s.store.Validations(ctx, f)
}

// Validation reads one report.
func (s *CTCService) Validation(ctx context.Context, validation id.CTCValidationID) (ctc.Validation, error) {
	if _, err := requireRoleOrSystem(ctx, ctcReaders...); err != nil {
		return ctc.Validation{}, err
	}
	return s.store.Validation(ctx, validation)
}

// PrepareSubmission seals a submission of a committed document under an
// active record. A document already live under the record — prepared, sent
// or answered — returns that submission with false: preparing is safe to
// retry and never makes a second submission of one document.
func (s *CTCService) PrepareSubmission(ctx context.Context, documentID id.FiscalDocumentID, record id.CTCApplicabilityID) (CTCSubmissionView, bool, error) {
	sc, err := requirePerson(ctx, ctcPreparers...)
	if err != nil {
		return CTCSubmissionView{}, false, err
	}
	var (
		out     CTCSubmissionView
		created bool
	)
	err = s.inTx(ctx, func(ctx context.Context) error {
		if err := s.store.Lock(ctx, "document/"+documentID.String()); err != nil {
			return err
		}
		existing, err := s.store.Submissions(ctx, ctc.SubmissionFilter{Document: documentID, Applicability: record})
		if err != nil {
			return err
		}
		for _, sub := range existing {
			if sub.Status.Live() {
				out, err = s.view(ctx, sub)
				return err
			}
		}
		a, err := s.store.Applicability(ctx, record)
		if err != nil {
			return err
		}
		in, err := s.documentInput(ctx, documentID)
		if err != nil {
			return err
		}
		var newest *ctc.Validation
		reports, err := s.store.Validations(ctx, ctc.ValidationFilter{Document: documentID, Applicability: record})
		if err != nil {
			return err
		}
		if len(reports) > 0 {
			newest = &reports[0]
		}
		sid, err := idgen.CTCSubmissionID(s.ids)
		if err != nil {
			return internal(err, "The submission could not be prepared.")
		}
		sub, _, err := ctc.Prepare(ctc.PrepareInput{ID: sid, TenantID: sc.Tenant(), Document: in, Applicability: a,
			Validation: newest, At: s.now(), By: sc.Subject()})
		if err != nil {
			return asDomain(err, "The submission could not be prepared.")
		}
		if err := s.store.CreateSubmission(ctx, sub); err != nil {
			return err
		}
		got, err := s.store.Submission(ctx, sid)
		if err != nil {
			return err
		}
		created = true
		out, err = s.view(ctx, got)
		return err
	})
	return out, created, err
}

// Submissions lists submissions, newest first, each rejected one with its
// correction task.
func (s *CTCService) Submissions(ctx context.Context, f ctc.SubmissionFilter) ([]CTCSubmissionView, error) {
	if _, err := requireRoleOrSystem(ctx, ctcReaders...); err != nil {
		return nil, err
	}
	if f.Status != "" && !f.Status.Valid() {
		return nil, errs.Invalid("status", errs.ReasonInvalidValue, "That is not a submission status.")
	}
	got, err := s.store.Submissions(ctx, f)
	if err != nil {
		return nil, err
	}
	out := make([]CTCSubmissionView, len(got))
	for i, sub := range got {
		if out[i], err = s.view(ctx, sub); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Submission reads one submission with its events.
func (s *CTCService) Submission(ctx context.Context, submission id.CTCSubmissionID) (CTCSubmissionView, error) {
	if _, err := requireRoleOrSystem(ctx, ctcReaders...); err != nil {
		return CTCSubmissionView{}, err
	}
	sub, err := s.store.Submission(ctx, submission)
	if err != nil {
		return CTCSubmissionView{}, err
	}
	return s.view(ctx, sub)
}

// view adds a rejected submission's correction task, derived from the
// rejected document's lineage in the fiscal documents.
func (s *CTCService) view(ctx context.Context, sub ctc.Submission) (CTCSubmissionView, error) {
	v := CTCSubmissionView{Submission: sub}
	if sub.Status != ctc.SubRejected {
		return v, nil
	}
	rec, _, err := s.docs.ByID(ctx, sub.Document)
	if err != nil {
		return CTCSubmissionView{}, err
	}
	recs, err := s.docs.Lineage(ctx, rec.Document.Root)
	if err != nil {
		return CTCSubmissionView{}, err
	}
	lineage := make([]document.Document, len(recs))
	for i, r := range recs {
		lineage[i] = r.Document
	}
	if t, ok := sub.Correction(lineage); ok {
		v.Correction = &t
	}
	return v, nil
}

// Dispatch authorises one dispatch attempt of a sealed submission. The
// checker names the payload digest they reviewed and is not the preparer; the
// payload is recomputed from the document, the record and the validation as
// they stand, and must still match the seal. A submission already sent or
// answered is returned as it is, with false.
func (s *CTCService) Dispatch(ctx context.Context, submission id.CTCSubmissionID, expectedDigest string) (CTCSubmissionView, bool, error) {
	sc, err := requirePerson(ctx, ctcDispatch...)
	if err != nil {
		return CTCSubmissionView{}, false, err
	}
	var (
		out        CTCSubmissionView
		dispatched bool
	)
	err = s.inTx(ctx, func(ctx context.Context) error {
		sub, err := s.store.Submission(ctx, submission)
		if err != nil {
			return err
		}
		if err := s.store.Lock(ctx, "document/"+sub.Document.String()); err != nil {
			return err
		}
		if sub, err = s.store.Submission(ctx, submission); err != nil {
			return err
		}
		a, err := s.store.Applicability(ctx, sub.Applicability)
		if err != nil {
			return err
		}
		in, err := s.documentInput(ctx, sub.Document)
		if err != nil {
			return err
		}
		v, err := s.store.Validation(ctx, sub.Validation)
		if err != nil {
			return err
		}
		current, err := ctc.PayloadDigest(ctc.PayloadInput{Document: in, Applicability: a, Validation: v})
		if err != nil {
			return internal(err, "The payload could not be digested.")
		}
		attempt, err := idgen.CTCAttemptID(s.ids)
		if err != nil {
			return internal(err, "The dispatch could not be recorded.")
		}
		e, ok, err := sub.Dispatch(ctc.DispatchInput{ExpectedDigest: expectedDigest, CurrentDigest: current,
			Applicability: a, Attempt: attempt, By: sc.Subject(), At: s.now()})
		if err != nil {
			return asDomain(err, "The dispatch could not be recorded.")
		}
		if ok {
			if err := s.store.AppendSubmissionEvent(ctx, submission, e); err != nil {
				return err
			}
			dispatched = true
		}
		got, err := s.store.Submission(ctx, submission)
		if err != nil {
			return err
		}
		out, err = s.view(ctx, got)
		return err
	})
	return out, dispatched, err
}

// Respond records one answer about a dispatch attempt. The same answer
// again records nothing and returns false.
func (s *CTCService) Respond(ctx context.Context, submission id.CTCSubmissionID, r ctc.Response) (CTCSubmissionView, bool, error) {
	sc, err := requirePerson(ctx, ctcResponders...)
	if err != nil {
		return CTCSubmissionView{}, false, err
	}
	r.TransportRef, r.AuthorityRef = strings.TrimSpace(r.TransportRef), strings.TrimSpace(r.AuthorityRef)
	r.AuthorityCode = strings.TrimSpace(r.AuthorityCode)
	r.EvidenceSHA256 = strings.ToLower(strings.TrimSpace(r.EvidenceSHA256))
	return s.moveSubmission(ctx, submission, func(sub ctc.Submission) (ctc.SubmissionEvent, bool, error) {
		return sub.Respond(r, sc.Subject(), s.now())
	})
}

// Withdraw releases a prepared submission that was never dispatched.
func (s *CTCService) Withdraw(ctx context.Context, submission id.CTCSubmissionID, reason string) (CTCSubmissionView, error) {
	sc, err := requirePerson(ctx, ctcPreparers...)
	if err != nil {
		return CTCSubmissionView{}, err
	}
	v, _, err := s.moveSubmission(ctx, submission, func(sub ctc.Submission) (ctc.SubmissionEvent, bool, error) {
		e, err := sub.Withdraw(reason, sc.Subject(), s.now())
		return e, err == nil, err
	})
	return v, err
}

func (s *CTCService) moveSubmission(ctx context.Context, submission id.CTCSubmissionID,
	next func(ctc.Submission) (ctc.SubmissionEvent, bool, error)) (CTCSubmissionView, bool, error) {
	var (
		out   CTCSubmissionView
		moved bool
	)
	err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.store.Lock(ctx, "submission/"+submission.String()); err != nil {
			return err
		}
		sub, err := s.store.Submission(ctx, submission)
		if err != nil {
			return err
		}
		e, ok, err := next(sub)
		if err != nil {
			return err
		}
		if ok {
			if err := s.store.AppendSubmissionEvent(ctx, submission, e); err != nil {
				return err
			}
			moved = true
		}
		got, err := s.store.Submission(ctx, submission)
		if err != nil {
			return err
		}
		out, err = s.view(ctx, got)
		return err
	})
	return out, moved, err
}

// asDomain passes a domain refusal through and wraps anything else as a
// defect.
func asDomain(err error, detail string) error {
	if errs.CategoryOf(err) == errs.CategoryInternal {
		return internal(err, detail)
	}
	return err
}

func (s *CTCService) inTx(ctx context.Context, fn func(context.Context) error) error {
	tx, txCtx, err := s.tx.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(txCtx) }()
	if err := fn(txCtx); err != nil {
		return err
	}
	return tx.Commit(txCtx)
}
