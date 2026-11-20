package port

import (
	"context"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/ctc"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/document"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
)

// CTCRepository stores the e-invoicing / CTC layer (Doc 21, G-030): country
// applicability records with their terms versions and events, immutable
// validation reports, and authority submissions with their events. It is
// append-only: no row is updated or deleted, and a record's or submission's
// status is the fold of its events.
type CTCRepository interface {
	// Lock serializes the tenant's writes to one key for the rest of the
	// transaction: a cell, a record, a document or a submission.
	Lock(ctx context.Context, key string) error

	// CreateApplicability writes a record, its first terms version and its
	// CREATED event.
	CreateApplicability(ctx context.Context, a ctc.Applicability) error
	// AppendApplicabilityVersion writes a record's next terms version. A
	// version already taken is refused as already existing.
	AppendApplicabilityVersion(ctx context.Context, record id.CTCApplicabilityID, v ctc.Version) error
	// AppendApplicabilityEvent writes a record's next event. A sequence
	// already taken is an optimistic conflict.
	AppendApplicabilityEvent(ctx context.Context, record id.CTCApplicabilityID, e ctc.Event) error
	// Applicability returns one record, folded.
	Applicability(ctx context.Context, record id.CTCApplicabilityID) (ctc.Applicability, error)
	// Applicabilities returns the tenant's records matching the filter,
	// folded, oldest first.
	Applicabilities(ctx context.Context, f ctc.Filter) ([]ctc.Applicability, error)

	// RecordValidation writes a sealed validation report and its results.
	RecordValidation(ctx context.Context, v ctc.Validation) error
	// Validation returns one report.
	Validation(ctx context.Context, validation id.CTCValidationID) (ctc.Validation, error)
	// Validations returns the reports matching the filter, newest first.
	Validations(ctx context.Context, f ctc.ValidationFilter) ([]ctc.Validation, error)

	// CreateSubmission writes a sealed submission and its PREPARED event.
	CreateSubmission(ctx context.Context, s ctc.Submission) error
	// AppendSubmissionEvent writes a submission's next event. A sequence
	// already taken is an optimistic conflict.
	AppendSubmissionEvent(ctx context.Context, submission id.CTCSubmissionID, e ctc.SubmissionEvent) error
	// Submission returns one submission, folded.
	Submission(ctx context.Context, submission id.CTCSubmissionID) (ctc.Submission, error)
	// Submissions returns the submissions matching the filter, folded,
	// newest first.
	Submissions(ctx context.Context, f ctc.SubmissionFilter) ([]ctc.Submission, error)
}

// FiscalDocumentReader reads committed fiscal documents. The CTC layer reads
// them and writes none: a document's tax, totals and corrections are the
// fiscal documents service's.
type FiscalDocumentReader interface {
	// ByID returns a document and its status history, oldest first.
	ByID(ctx context.Context, documentID id.FiscalDocumentID) (DocumentRecord, []document.StatusEvent, error)
	// Lineage returns every document sharing a root, in commit order.
	Lineage(ctx context.Context, root id.FiscalDocumentID) ([]DocumentRecord, error)
}
