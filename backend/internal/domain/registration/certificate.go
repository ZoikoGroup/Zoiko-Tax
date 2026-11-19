package registration

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// Exemption certificates as evidence (Doc 17 EER-08, EER-09, EER-10,
// EER-13, EER-14; Doc 15 OPS-13).
//
// A certificate is a buyer's claim, on a document, that a sale to it is
// exempt in a jurisdiction. Here it is screened for completeness, reviewed
// by a specialist, and confirmed by a second person on its exact evidence
// version. APPROVED says the document was checked and found acceptable as
// evidence for its scope and dates. It does not say any transaction is
// exempt: that is the determination engine's, from its own approved rules
// (Doc 17 §01; T15-13C).

// CertificateType is the kind of exemption claimed.
type CertificateType string

// The certificate types. A wholesale or resale certificate is not an
// unconditional exemption and relieves no provider-side duty (Doc 17 ER-035,
// XC-10).
const (
	TypeResale             CertificateType = "RESALE"
	TypeWholesaleTelecom   CertificateType = "WHOLESALE_TELECOM"
	TypeExemptOrganization CertificateType = "EXEMPT_ORGANIZATION"
	TypeGovernment         CertificateType = "GOVERNMENT_ENTITY"
	TypeDiplomatic         CertificateType = "DIPLOMATIC"
	TypeDirectPay          CertificateType = "DIRECT_PAY_PERMIT"
	TypeOtherExemption     CertificateType = "OTHER_EXEMPTION"
)

// CertificateTypes is every certificate type.
var CertificateTypes = []CertificateType{TypeResale, TypeWholesaleTelecom, TypeExemptOrganization, TypeGovernment,
	TypeDiplomatic, TypeDirectPay, TypeOtherExemption}

// Valid reports whether the type is one of these.
func (t CertificateType) Valid() bool { return slices.Contains(CertificateTypes, t) }

// CertificateStatus is where a certificate stands (Doc 17 §07).
type CertificateStatus string

// The statuses. SCREENING is a certificate whose screening found it
// incomplete or inconsistent: it waits for a corrected document.
const (
	CertSubmitted      CertificateStatus = "SUBMITTED"
	CertScreening      CertificateStatus = "SCREENING"
	CertReviewRequired CertificateStatus = "REVIEW_REQUIRED"
	CertApproved       CertificateStatus = "APPROVED"
	CertRejected       CertificateStatus = "REJECTED"
	CertExpired        CertificateStatus = "EXPIRED"
	CertSuspended      CertificateStatus = "SUSPENDED"
	CertRevoked        CertificateStatus = "REVOKED"
	CertSuperseded     CertificateStatus = "SUPERSEDED"
)

// CertificateStatuses is every certificate status.
var CertificateStatuses = []CertificateStatus{CertSubmitted, CertScreening, CertReviewRequired, CertApproved,
	CertRejected, CertExpired, CertSuspended, CertRevoked, CertSuperseded}

// Valid reports whether the status is one of these.
func (s CertificateStatus) Valid() bool { return slices.Contains(CertificateStatuses, s) }

// Closed reports whether nothing more happens to a certificate in this
// status. A renewal is a new certificate, reviewed afresh (Doc 17 ER-038).
func (s CertificateStatus) Closed() bool {
	return s == CertRejected || s == CertExpired || s == CertRevoked || s == CertSuperseded
}

// Open reports whether the certificate is still being reviewed: a new
// document version may replace the current one.
func (s CertificateStatus) Open() bool {
	return s == CertSubmitted || s == CertScreening || s == CertReviewRequired
}

// CertificateEventKind is what happened to a certificate.
type CertificateEventKind string

// The events. RECEIVED is always the first.
const (
	CertEventReceived    CertificateEventKind = "RECEIVED"
	CertEventVersion     CertificateEventKind = "VERSION_ADDED"
	CertEventScreened    CertificateEventKind = "SCREENED"
	CertEventRecommended CertificateEventKind = "RECOMMENDED"
	CertEventConfirmed   CertificateEventKind = "CONFIRMED"
	CertEventReturned    CertificateEventKind = "RETURNED"
	CertEventSuspended   CertificateEventKind = "SUSPENDED"
	CertEventReinstated  CertificateEventKind = "REINSTATED"
	CertEventExpired     CertificateEventKind = "EXPIRED"
	CertEventRevoked     CertificateEventKind = "REVOKED"
	CertEventSuperseded  CertificateEventKind = "SUPERSEDED"
)

// CheckResult is one completeness criterion's result.
type CheckResult string

// The results.
const (
	CheckPass          CheckResult = "PASS"
	CheckFail          CheckResult = "FAIL"
	CheckNotApplicable CheckResult = "NOT_APPLICABLE"
)

// Valid reports whether the result is one of these.
func (c CheckResult) Valid() bool { return c == CheckPass || c == CheckFail || c == CheckNotApplicable }

// ScreeningResult is what screening concluded (Doc 17 EER-10).
type ScreeningResult string

// The results. DOCUMENT_COMPLETE is not APPROVED_FOR_USE, and neither is a
// legal applicability pass (Doc 17 §02 "Verification").
const (
	ScreenDocumentComplete ScreeningResult = "DOCUMENT_COMPLETE"
	ScreenIncomplete       ScreeningResult = "INCOMPLETE"
	ScreenInconsistent     ScreeningResult = "INCONSISTENT"
	ScreenNeedsLegal       ScreeningResult = "NEEDS_LEGAL_DECISION"
)

// Valid reports whether the result is one of these.
func (r ScreeningResult) Valid() bool {
	switch r {
	case ScreenDocumentComplete, ScreenIncomplete, ScreenInconsistent, ScreenNeedsLegal:
		return true
	}
	return false
}

// Screening is the completeness checklist a specialist records against one
// evidence version (Doc 17 EER-10 "Criteria").
type Screening struct {
	SellerMatches       CheckResult
	BuyerMatches        CheckResult
	JurisdictionMatches CheckResult
	FormAccepted        CheckResult
	SignaturePresent    CheckResult
	DatesValid          CheckResult
	NumberPresent       CheckResult
	Result              ScreeningResult
}

// Criteria returns the checklist in its fixed order, by name.
func (s Screening) Criteria() []struct {
	Name   string
	Result CheckResult
} {
	return []struct {
		Name   string
		Result CheckResult
	}{
		{"sellerMatches", s.SellerMatches}, {"buyerMatches", s.BuyerMatches}, {"jurisdictionMatches", s.JurisdictionMatches},
		{"formAccepted", s.FormAccepted}, {"signaturePresent", s.SignaturePresent}, {"datesValid", s.DatesValid},
		{"numberPresent", s.NumberPresent},
	}
}

// Recommendation is the maker's half of a decision: approve or reject one
// evidence version, and for an approval the period it is evidence for.
type Recommendation struct {
	Approve       bool
	EffectiveFrom time.Time
	EffectiveTo   time.Time // zero for open-ended
}

// Decision is the checker's half: confirm the recommendation, or return it.
type Decision string

// The decisions.
const (
	DecisionConfirm Decision = "CONFIRM"
	DecisionReturn  Decision = "RETURN"
)

// CertificateEvent is one appended act on a certificate.
type CertificateEvent struct {
	Seq             int
	Kind            CertificateEventKind
	Status          CertificateStatus
	EvidenceVersion int
	Note            string
	Document        *Attachment
	Screening       *Screening
	Recommendation  *Recommendation
	Decision        Decision
	RecordedAt      time.Time
	RecordedBy      id.UserID
}

// Certificate is one exemption certificate, folded with its events.
type Certificate struct {
	ID       id.ExemptionCertificateID
	TenantID id.TenantID

	// LegalEntity is the seller the certificate is given to, and BuyerRef
	// the buyer in the tenant's own customer system. A certificate is
	// evidence only for this pair (Doc 17 EER-08).
	LegalEntity id.LegalEntityID
	BuyerRef    string
	BuyerName   string

	Type              CertificateType
	ExemptionReason   string
	Jurisdiction      string
	CertificateNumber string
	// ProductScope is the product families or classification patterns the
	// certificate claims, as written. Never interpreted here.
	ProductScope []string

	IssuedOn  time.Time
	ExpiresOn time.Time // zero when the document states no expiry

	// Supersedes is the certificate this one renews or replaces, zero for
	// none. On this one's approval the other becomes SUPERSEDED.
	Supersedes id.ExemptionCertificateID

	IntakeKey  string
	IntakeHash canonical.Digest

	CreatedAt time.Time
	CreatedBy id.UserID

	Status          CertificateStatus
	EvidenceVersion int
	History         []CertificateEvent
}

// Validate refuses a certificate that cannot be received.
func (c Certificate) Validate() error {
	if c.LegalEntity.IsZero() {
		return errs.Invalid("legalEntityId", errs.ReasonMissingField, "A certificate is given to one of the tenant's legal entities, the seller.")
	}
	for _, f := range []struct {
		name, value string
		required    bool
		limit       int
	}{
		{"buyerRef", c.BuyerRef, true, MaxText},
		{"buyerName", c.BuyerName, true, MaxText},
		{"exemptionReason", c.ExemptionReason, true, 500},
		{"certificateNumber", c.CertificateNumber, false, MaxText},
	} {
		if err := text(f.name, f.value, f.required, f.limit); err != nil {
			return err
		}
	}
	if !c.Type.Valid() {
		return errs.Invalid("certificateType", errs.ReasonInvalidValue, fmt.Sprintf("%q is not a certificate type.", c.Type))
	}
	if !ValidJurisdiction(c.Jurisdiction) {
		return errs.Invalid("jurisdiction", errs.ReasonInvalidValue,
			"A certificate names the one jurisdiction it is for: a country, or a country and one subdivision.")
	}
	if len(c.ProductScope) > 20 {
		return errs.Invalid("productScope", errs.ReasonInvalidValue, "A certificate's product scope lists at most 20 entries.")
	}
	for _, p := range c.ProductScope {
		if err := text("productScope", p, true, 100); err != nil {
			return err
		}
	}
	if c.IssuedOn.IsZero() {
		return errs.Invalid("issuedOn", errs.ReasonMissingField, "A certificate states the date it was issued or signed.")
	}
	if !c.ExpiresOn.IsZero() && c.ExpiresOn.Before(c.IssuedOn) {
		return errs.Invalid("expiresOn", errs.ReasonInvalidValue, "A certificate expires on or after the date it was issued.")
	}
	if !c.Supersedes.IsZero() && c.Supersedes == c.ID {
		return errs.Invalid("supersedes", errs.ReasonInvalidValue, "A certificate does not supersede itself.")
	}
	return nil
}

// Payload is the canonical document the intake hash covers.
func (c Certificate) Payload(doc Attachment) canonical.Value {
	opt := func(s string) canonical.Value { return canonical.OptString(s) }
	supersedes := canonical.Absent()
	if !c.Supersedes.IsZero() {
		supersedes = canonical.String(c.Supersedes.String())
	}
	expires := canonical.Absent()
	if !c.ExpiresOn.IsZero() {
		expires = canonical.String(c.ExpiresOn.Format(time.DateOnly))
	}
	scope := make([]canonical.Value, len(c.ProductScope))
	for i, p := range c.ProductScope {
		scope[i] = canonical.String(p)
	}
	return canonical.Object(
		canonical.F("legalEntityId", canonical.String(c.LegalEntity.String())),
		canonical.F("buyerRef", canonical.String(c.BuyerRef)),
		canonical.F("buyerName", canonical.String(c.BuyerName)),
		canonical.F("certificateType", canonical.String(string(c.Type))),
		canonical.F("exemptionReason", canonical.String(c.ExemptionReason)),
		canonical.F("jurisdiction", canonical.String(c.Jurisdiction)),
		canonical.F("certificateNumber", opt(c.CertificateNumber)),
		canonical.F("productScope", canonical.Array(scope...)),
		canonical.F("issuedOn", canonical.String(c.IssuedOn.Format(time.DateOnly))),
		canonical.F("expiresOn", expires),
		canonical.F("supersedes", supersedes),
		canonical.F("document", attachmentValue(doc)),
	)
}

// Receive is a certificate's first event: RECEIVED, SUBMITTED, with its
// document as evidence version 1. Receipt is not approval (Doc 17 EER-09).
func Receive(c Certificate, doc Attachment, by id.UserID, at time.Time) (CertificateEvent, error) {
	if err := c.Validate(); err != nil {
		return CertificateEvent{}, err
	}
	doc = doc.Normalize()
	if err := doc.Validate(); err != nil {
		return CertificateEvent{}, err
	}
	return CertificateEvent{Seq: 1, Kind: CertEventReceived, Status: CertSubmitted, EvidenceVersion: 1, Document: &doc,
		RecordedAt: at.UTC(), RecordedBy: by}, nil
}

// FoldCertificate applies a certificate's events in order.
func FoldCertificate(c Certificate, events []CertificateEvent) (Certificate, error) {
	if len(events) == 0 || events[0].Kind != CertEventReceived || events[0].Seq != 1 || events[0].Status != CertSubmitted ||
		events[0].EvidenceVersion != 1 || events[0].Document == nil {
		return Certificate{}, fmt.Errorf("registration: a certificate's history starts with its RECEIVED event and document")
	}
	c.Status, c.EvidenceVersion = CertSubmitted, 1
	for i, e := range events {
		if e.Seq != i+1 {
			return Certificate{}, fmt.Errorf("registration: certificate event %d is out of sequence", e.Seq)
		}
		if i == 0 {
			continue
		}
		want := c.EvidenceVersion
		if e.Kind == CertEventVersion {
			want++
		}
		if e.EvidenceVersion != want || !certFolds(c.Status, e) {
			return Certificate{}, fmt.Errorf("registration: certificate %s from %s to %s", e.Kind, c.Status, e.Status)
		}
		c.Status, c.EvidenceVersion = e.Status, e.EvidenceVersion
	}
	c.History = slices.Clone(events)
	return c, nil
}

func certFolds(from CertificateStatus, e CertificateEvent) bool {
	switch e.Kind {
	case CertEventVersion:
		return from.Open() && e.Status == CertSubmitted && e.Document != nil
	case CertEventScreened:
		return from == CertSubmitted && e.Screening != nil && e.Status == screenedStatus(e.Screening.Result)
	case CertEventRecommended:
		return from == CertReviewRequired && e.Status == CertReviewRequired && e.Recommendation != nil
	case CertEventReturned:
		return from == CertReviewRequired && e.Status == CertReviewRequired && e.Decision == DecisionReturn
	case CertEventConfirmed:
		return from == CertReviewRequired && (e.Status == CertApproved || e.Status == CertRejected) && e.Decision == DecisionConfirm
	case CertEventSuspended:
		return from == CertApproved && e.Status == CertSuspended
	case CertEventReinstated:
		return from == CertSuspended && e.Status == CertApproved
	case CertEventExpired, CertEventRevoked, CertEventSuperseded:
		return (from == CertApproved || from == CertSuspended) && e.Status == certKindStatus[e.Kind]
	}
	return false
}

var certKindStatus = map[CertificateEventKind]CertificateStatus{
	CertEventExpired: CertExpired, CertEventRevoked: CertRevoked, CertEventSuperseded: CertSuperseded,
}

func screenedStatus(r ScreeningResult) CertificateStatus {
	if r == ScreenDocumentComplete || r == ScreenNeedsLegal {
		return CertReviewRequired
	}
	return CertScreening
}

func (c Certificate) next(kind CertificateEventKind, to CertificateStatus, note string, by id.UserID, at time.Time) CertificateEvent {
	return CertificateEvent{Seq: len(c.History) + 1, Kind: kind, Status: to, EvidenceVersion: c.EvidenceVersion, Note: note,
		RecordedAt: at.UTC(), RecordedBy: by}
}

// Document is the current evidence version's document, and who recorded it.
func (c Certificate) Document() (Attachment, id.UserID) {
	for i := len(c.History) - 1; i >= 0; i-- {
		if e := c.History[i]; e.Document != nil {
			return *e.Document, e.RecordedBy
		}
	}
	return Attachment{}, id.UserID{}
}

// Documents is every document version, oldest first.
func (c Certificate) Documents() []CertificateEvent {
	var out []CertificateEvent
	for _, e := range c.History {
		if e.Document != nil {
			out = append(out, e)
		}
	}
	return out
}

// HasDocument reports whether any version's document has this digest.
func (c Certificate) HasDocument(sha string) bool {
	for _, e := range c.History {
		if e.Document != nil && e.Document.SHA256 == sha {
			return true
		}
	}
	return false
}

// LastScreening is the screening of the current evidence version, and false
// when it has not been screened.
func (c Certificate) LastScreening() (Screening, bool) {
	for i := len(c.History) - 1; i >= 0; i-- {
		e := c.History[i]
		if e.EvidenceVersion != c.EvidenceVersion {
			break
		}
		if e.Screening != nil {
			return *e.Screening, true
		}
	}
	return Screening{}, false
}

// Pending is the recommendation awaiting a checker, with its event, and
// false when there is none: the latest RECOMMENDED event of the current
// evidence version not since confirmed or returned.
func (c Certificate) Pending() (CertificateEvent, bool) {
	if c.Status != CertReviewRequired {
		return CertificateEvent{}, false
	}
	for i := len(c.History) - 1; i >= 0; i-- {
		e := c.History[i]
		switch {
		case e.EvidenceVersion != c.EvidenceVersion:
			return CertificateEvent{}, false
		case e.Kind == CertEventReturned || e.Kind == CertEventScreened:
			return CertificateEvent{}, false
		case e.Kind == CertEventRecommended:
			return e, true
		}
	}
	return CertificateEvent{}, false
}

// Approval is the recommendation a checker confirmed into APPROVED, with
// the maker and the checker, and false when the certificate was never
// approved.
func (c Certificate) Approval() (rec Recommendation, maker, checker id.UserID, at time.Time, ok bool) {
	for i := len(c.History) - 1; i >= 0; i-- {
		e := c.History[i]
		if e.Kind != CertEventConfirmed || e.Status != CertApproved {
			continue
		}
		for j := i - 1; j >= 0; j-- {
			if r := c.History[j]; r.Kind == CertEventRecommended && r.Recommendation != nil {
				return *r.Recommendation, r.RecordedBy, e.RecordedBy, e.RecordedAt, true
			}
		}
	}
	return Recommendation{}, id.UserID{}, id.UserID{}, time.Time{}, false
}

// End is the last day the certificate is evidence for: the approved
// period's end, else the document's expiry; zero when open-ended.
func (c Certificate) End() time.Time {
	if rec, _, _, _, ok := c.Approval(); ok && !rec.EffectiveTo.IsZero() {
		return rec.EffectiveTo
	}
	return c.ExpiresOn
}

// InForce reports whether the certificate is APPROVED evidence for day. It
// is a statement about the document, never a tax treatment.
func (c Certificate) InForce(day time.Time) bool {
	rec, _, _, _, ok := c.Approval()
	if c.Status != CertApproved || !ok || rec.EffectiveFrom.After(day) {
		return false
	}
	end := c.End()
	return end.IsZero() || !day.After(end)
}

// FlagsAt is what needs attention on day.
func (c Certificate) FlagsAt(day time.Time) []Flag {
	var start time.Time
	if rec, _, _, _, ok := c.Approval(); ok {
		start = rec.EffectiveFrom
	}
	out := endFlags(c.Status == CertApproved || c.Status == CertSuspended, start, c.End(), day)
	if c.Status.Open() && !c.ExpiresOn.IsZero() && day.After(c.ExpiresOn) {
		out = append(out, FlagPastExpiry)
	}
	if c.Status == CertScreening {
		out = append(out, FlagCorrectionRequested)
	}
	if _, ok := c.Pending(); ok {
		out = append(out, FlagAwaitingSecondReview)
	}
	return out
}

// AddVersion replaces the document with a corrected one, as the next
// evidence version. The certificate goes back to SUBMITTED, and anything
// screened or recommended on the old version no longer counts (Doc 17
// EER-09 "replace through linked new version"; ER-042).
func (c Certificate) AddVersion(doc Attachment, note string, by id.UserID, at time.Time) (CertificateEvent, error) {
	doc, note = doc.Normalize(), strings.TrimSpace(note)
	if !c.Status.Open() {
		return CertificateEvent{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("A %s certificate takes no new document. A renewal is a new certificate that supersedes it.", c.Status))
	}
	if err := doc.Validate(); err != nil {
		return CertificateEvent{}, err
	}
	if cur, _ := c.Document(); cur.SHA256 == doc.SHA256 {
		return CertificateEvent{}, errs.New(errs.CategoryConflict, errs.ReasonAlreadyExists, "This document is already the current version.")
	}
	if err := optionalText("note", note); err != nil {
		return CertificateEvent{}, err
	}
	e := c.next(CertEventVersion, CertSubmitted, note, by, at)
	e.EvidenceVersion = c.EvidenceVersion + 1
	e.Document = &doc
	return e, nil
}

// Screen records the completeness checklist on the current version.
// DOCUMENT_COMPLETE needs every criterion to pass or not apply, and is
// refused for a document already past its expiry (T15-13A); INCOMPLETE and
// INCONSISTENT name at least one failed criterion and wait for a corrected
// document.
func (c Certificate) Screen(s Screening, note string, by id.UserID, at time.Time) (CertificateEvent, error) {
	note = strings.TrimSpace(note)
	if c.Status != CertSubmitted {
		return CertificateEvent{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("Only a SUBMITTED certificate is screened; this one is %s. A corrected document is a new version.", c.Status))
	}
	failed := 0
	for _, cr := range s.Criteria() {
		if !cr.Result.Valid() {
			return CertificateEvent{}, errs.Invalid(cr.Name, errs.ReasonInvalidValue, "A criterion is PASS, FAIL or NOT_APPLICABLE.")
		}
		if cr.Result == CheckFail {
			failed++
		}
	}
	switch {
	case !s.Result.Valid():
		return CertificateEvent{}, errs.Invalid("result", errs.ReasonInvalidValue, fmt.Sprintf("%q is not a screening result.", s.Result))
	case s.Result == ScreenDocumentComplete && failed > 0:
		return CertificateEvent{}, errs.Invalid("result", errs.ReasonInvalidValue, "A document with a failed criterion is not DOCUMENT_COMPLETE.")
	case s.Result == ScreenDocumentComplete && !c.ExpiresOn.IsZero() && Day(at).After(c.ExpiresOn):
		return CertificateEvent{}, errs.Invalid("result", errs.ReasonInvalidValue,
			fmt.Sprintf("The certificate expired on %s. An expired document is not complete evidence.", c.ExpiresOn.Format(time.DateOnly)))
	case (s.Result == ScreenIncomplete || s.Result == ScreenInconsistent) && failed == 0:
		return CertificateEvent{}, errs.Invalid("result", errs.ReasonInvalidValue, "An INCOMPLETE or INCONSISTENT result names the criterion that failed.")
	case s.Result != ScreenDocumentComplete && note == "":
		return CertificateEvent{}, errs.Invalid("note", errs.ReasonMissingField, "Say what is missing, inconsistent or needs a legal decision.")
	}
	if err := optionalText("note", note); err != nil {
		return CertificateEvent{}, err
	}
	e := c.next(CertEventScreened, screenedStatus(s.Result), note, by, at)
	e.Screening = &s
	return e, nil
}

// Recommend records the maker's recommendation on the current version, with
// its rationale. An approval states the period the certificate is evidence
// for, within the document's own dates, and is refused for a document
// already expired.
func (c Certificate) Recommend(r Recommendation, rationale string, by id.UserID, at time.Time) (CertificateEvent, error) {
	rationale = strings.TrimSpace(rationale)
	if c.Status != CertReviewRequired {
		return CertificateEvent{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("Only a certificate in REVIEW_REQUIRED is recommended; this one is %s.", c.Status))
	}
	if _, ok := c.Pending(); ok {
		return CertificateEvent{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			"A recommendation already awaits a second reviewer. It is confirmed or returned first.")
	}
	if err := reasonText("rationale", rationale); err != nil {
		return CertificateEvent{}, err
	}
	if r.Approve {
		day := Day(at)
		switch {
		case r.EffectiveFrom.IsZero():
			return CertificateEvent{}, errs.Invalid("effectiveFrom", errs.ReasonMissingField, "An approval states the date the evidence is effective from.")
		case r.EffectiveFrom.Before(c.IssuedOn):
			return CertificateEvent{}, errs.Invalid("effectiveFrom", errs.ReasonInvalidValue, "A certificate is not evidence before the date it was issued.")
		case !c.ExpiresOn.IsZero() && (r.EffectiveTo.IsZero() || r.EffectiveTo.After(c.ExpiresOn)):
			return CertificateEvent{}, errs.Invalid("effectiveTo", errs.ReasonInvalidValue,
				fmt.Sprintf("The certificate expires on %s; the approved period ends on or before it.", c.ExpiresOn.Format(time.DateOnly)))
		case !r.EffectiveTo.IsZero() && r.EffectiveTo.Before(r.EffectiveFrom):
			return CertificateEvent{}, errs.Invalid("effectiveTo", errs.ReasonInvalidValue, "The approved period ends on or after it starts.")
		case !r.EffectiveTo.IsZero() && day.After(r.EffectiveTo):
			return CertificateEvent{}, errs.Invalid("effectiveTo", errs.ReasonInvalidValue, "The approved period has already ended. An expired certificate is not approved.")
		}
	} else {
		r.EffectiveFrom, r.EffectiveTo = time.Time{}, time.Time{}
	}
	e := c.next(CertEventRecommended, CertReviewRequired, rationale, by, at)
	e.Recommendation = &r
	return e, nil
}

// Decide is the checker's act on the pending recommendation, against the
// evidence version they reviewed. A version other than the current one is
// VERSION_CONFLICT and nothing is published (Doc 17 ER-042). The checker is
// neither the recommender nor the person who recorded the current document:
// an uploader never approves their own evidence (ER-041).
func (c Certificate) Decide(d Decision, version int, note string, by id.UserID, at time.Time) (CertificateEvent, error) {
	note = strings.TrimSpace(note)
	if d != DecisionConfirm && d != DecisionReturn {
		return CertificateEvent{}, errs.Invalid("decision", errs.ReasonInvalidValue, "A decision is CONFIRM or RETURN.")
	}
	if version != c.EvidenceVersion {
		return CertificateEvent{}, errs.New(errs.CategoryConflict, ReasonVersionConflict,
			fmt.Sprintf("The certificate is at evidence version %d; version %d was reviewed. Review the current version.", c.EvidenceVersion, version))
	}
	pending, ok := c.Pending()
	if !ok {
		return CertificateEvent{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			"No recommendation awaits a second reviewer on this certificate.")
	}
	if pending.RecordedBy == by {
		return CertificateEvent{}, makerChecker("The person who recommended this decision cannot also confirm or return it. Another person must.")
	}
	if _, uploader := c.Document(); uploader == by {
		return CertificateEvent{}, makerChecker("The person who recorded this certificate's document cannot approve it. Another person must.")
	}
	if d == DecisionReturn {
		if err := reasonText("note", note); err != nil {
			return CertificateEvent{}, err
		}
		e := c.next(CertEventReturned, CertReviewRequired, note, by, at)
		e.Decision = DecisionReturn
		return e, nil
	}
	if err := optionalText("note", note); err != nil {
		return CertificateEvent{}, err
	}
	to := CertRejected
	if pending.Recommendation.Approve {
		to = CertApproved
		if end := pending.Recommendation.EffectiveTo; !end.IsZero() && Day(at).After(end) {
			return CertificateEvent{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
				"The recommended period has ended since it was recommended. Return it for a new recommendation.")
		}
	}
	e := c.next(CertEventConfirmed, to, note, by, at)
	e.Decision = DecisionConfirm
	return e, nil
}

// Transition suspends, reinstates, expires or revokes an approved
// certificate, with the reason. Expiry or revocation blocks future use and
// rewrites nothing already recorded (Doc 17 §03 C; ER-039). EXPIRED only
// once the evidence period has ended; a reinstatement is by someone other
// than the person who suspended it, and not after the period has ended.
func (c Certificate) Transition(to CertificateStatus, reason string, by id.UserID, at time.Time) (CertificateEvent, error) {
	reason = strings.TrimSpace(reason)
	day := Day(at)
	var kind CertificateEventKind
	switch {
	case to == CertSuspended && c.Status == CertApproved:
		kind = CertEventSuspended
	case to == CertApproved && c.Status == CertSuspended:
		kind = CertEventReinstated
		for i := len(c.History) - 1; i >= 0; i-- {
			if e := c.History[i]; e.Kind == CertEventSuspended {
				if e.RecordedBy == by {
					return CertificateEvent{}, makerChecker("The person who suspended this certificate cannot also reinstate it. Another person must.")
				}
				break
			}
		}
		if end := c.End(); !end.IsZero() && day.After(end) {
			return CertificateEvent{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
				"The certificate's evidence period has ended. Record it as expired; a renewal is a new certificate.")
		}
	case to == CertExpired && (c.Status == CertApproved || c.Status == CertSuspended):
		kind = CertEventExpired
		if end := c.End(); end.IsZero() || !day.After(end) {
			return CertificateEvent{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
				"A certificate expires only once its evidence period has ended. To stop its use sooner, suspend or revoke it.")
		}
	case to == CertRevoked && (c.Status == CertApproved || c.Status == CertSuspended):
		kind = CertEventRevoked
	default:
		if !to.Valid() {
			return CertificateEvent{}, errs.Invalid("to", errs.ReasonInvalidValue, fmt.Sprintf("%q is not a certificate status.", to))
		}
		return CertificateEvent{}, invalidMove("certificate", string(c.Status), string(to))
	}
	if kind == CertEventExpired {
		if err := optionalText("reason", reason); err != nil {
			return CertificateEvent{}, err
		}
	} else if err := reasonText("reason", reason); err != nil {
		return CertificateEvent{}, err
	}
	return c.next(kind, to, reason, by, at), nil
}

// Supersede marks an approved or suspended certificate SUPERSEDED by the
// approval of its renewal. Its history stays as recorded.
func (c Certificate) Supersede(by id.ExemptionCertificateID, who id.UserID, at time.Time) (CertificateEvent, error) {
	if c.Status != CertApproved && c.Status != CertSuspended {
		return CertificateEvent{}, invalidMove("certificate", string(c.Status), string(CertSuperseded))
	}
	return c.next(CertEventSuperseded, CertSuperseded, "Superseded by certificate "+by.String()+".", who, at), nil
}

func optionalText(field, v string) error {
	if len(v) > MaxReason {
		return errs.Invalid(field, errs.ReasonInvalidValue, fmt.Sprintf("%s is at most %d characters.", field, MaxReason))
	}
	return nil
}

// CertificateFilter narrows a listing. Empty fields do not filter.
type CertificateFilter struct {
	Status       CertificateStatus
	LegalEntity  id.LegalEntityID
	BuyerRef     string
	Jurisdiction string
	Flag         Flag
}

// Match reports whether a certificate passes the filter on day.
func (f CertificateFilter) Match(c Certificate, day time.Time) bool {
	return (f.Status == "" || c.Status == f.Status) && (f.LegalEntity.IsZero() || c.LegalEntity == f.LegalEntity) &&
		(f.BuyerRef == "" || c.BuyerRef == f.BuyerRef) && jurisdictionMatch(f.Jurisdiction, c.Jurisdiction) &&
		(f.Flag == "" || slices.Contains(c.FlagsAt(day), f.Flag))
}
