package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/evidence"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/clock"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/kms"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// DefaultSettleWindow is how long after a period ends before it may be sealed.
//
// A decision's recorded_at is its decision time, taken before its transaction
// commits. A decision made one millisecond before a period ends can therefore
// commit after a sealer has already read the period's leaves, and the seal
// would then be missing a record that belongs to it — which the next
// verification reports as ROOT_MISMATCH, correctly, and for no fault of the
// data. Waiting out the longest plausible transaction closes the window. The
// value is deliberately generous; a seal is not latency-sensitive.
const DefaultSettleWindow = 15 * time.Minute

// SealService seals evidence periods and verifies seals (ADR-0011 §2.4, §2.5;
// the W1 lane D period-seal prototype).
//
// The signer is the evidence-seal key hierarchy of ADR-0017 §2.6, which is not
// the content-bundle hierarchy even though both sign through kms.Signer. In
// development that is a LocalSigner; in a cell it is the KMS or HSM, and the
// private key never has an in-process representation.
type SealService struct {
	decisions port.DecisionRepository
	seals     port.SealRepository
	evidence  port.EvidenceStore
	tx        port.TxManager
	signer    kms.Signer
	verifier  kms.Verifier
	clock     clock.Clock
	ids       idgen.Generator
	cell      string
	settle    time.Duration
}

// NewSealService wires the service. settle of zero means DefaultSettleWindow.
func NewSealService(
	decisions port.DecisionRepository,
	seals port.SealRepository,
	store port.EvidenceStore,
	tx port.TxManager,
	signer kms.Signer,
	verifier kms.Verifier,
	clk clock.Clock,
	ids idgen.Generator,
	cell string,
	settle time.Duration,
) *SealService {
	if settle <= 0 {
		settle = DefaultSettleWindow
	}
	return &SealService{
		decisions: decisions, seals: seals, evidence: store, tx: tx, signer: signer, verifier: verifier,
		clock: clk, ids: ids, cell: cell, settle: settle,
	}
}

// signedSeal is the evidence object a seal is stored as: the canonical payload
// carried as base64, so the bytes that were signed are the bytes that are
// verified with no re-encoding between them — the same format discipline as
// the content-bundle seal (internal/content/bundle/seal.go).
type signedSeal struct {
	Payload   string        `json:"payload"`
	Signature kms.Signature `json:"signature"`
}

// SealPeriod seals [start, end) for the tenant in scope.
func (s *SealService) SealPeriod(ctx context.Context, start, end time.Time) (evidence.SealRecord, error) {
	sc, err := requireRoleOrSystem(ctx, security.RoleAdmin)
	if err != nil {
		return evidence.SealRecord{}, err
	}
	start, end = start.UTC().Truncate(time.Microsecond), end.UTC().Truncate(time.Microsecond)
	if !end.After(start) {
		return evidence.SealRecord{}, errs.Invalid("periodEnd", errs.ReasonInvalidValue,
			"A seal period must end after it starts.")
	}
	now := s.clock.Now().UTC().Truncate(time.Microsecond)
	if end.After(now.Add(-s.settle)) {
		return evidence.SealRecord{}, errs.Invalid("periodEnd", errs.ReasonInvalidValue,
			fmt.Sprintf("The period has not settled. It may be sealed from %s.",
				canonical.FormatTime(end.Add(s.settle))))
	}

	tx, ctx, err := s.tx.Begin(ctx)
	if err != nil {
		return evidence.SealRecord{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := s.seals.LockSealing(ctx); err != nil {
		return evidence.SealRecord{}, err
	}
	overlapping, err := s.seals.Overlapping(ctx, start, end)
	if err != nil {
		return evidence.SealRecord{}, err
	}
	if len(overlapping) > 0 {
		// A record in two seals is a record two statements vouch for, and
		// when they disagree there is no rule for which one is evidence.
		return evidence.SealRecord{}, errs.New(errs.CategoryConflict, errs.ReasonAlreadyExists,
			fmt.Sprintf("The period overlaps seal %s.", overlapping[0].SealID))
	}

	leaves, err := s.decisions.SealLeaves(ctx, start, end)
	if err != nil {
		return evidence.SealRecord{}, err
	}
	root, err := evidence.PeriodRoot(leaves)
	if err != nil {
		return evidence.SealRecord{}, integrity(err, "A decision in the period cannot be sealed.")
	}
	payload := evidence.PeriodSeal{
		TenantID:    sc.Tenant(),
		Cell:        s.cell,
		PeriodStart: start,
		PeriodEnd:   end,
		LeafCount:   len(leaves),
		MerkleRoot:  root,
		SealedAt:    now,
	}
	payloadBytes, err := evidence.EncodePeriodSeal(payload)
	if err != nil {
		return evidence.SealRecord{}, internal(err, "The seal could not be built.")
	}
	sig, err := s.signer.Sign(ctx, payloadBytes)
	if err != nil {
		return evidence.SealRecord{}, errs.Wrap(err, errs.CategoryUnavailable, errs.ReasonUnavailable,
			"The signing service was unavailable. Nothing was sealed; the request may be retried.")
	}
	doc, err := json.Marshal(signedSeal{Payload: base64.StdEncoding.EncodeToString(payloadBytes), Signature: sig})
	if err != nil {
		return evidence.SealRecord{}, internal(err, "The seal could not be encoded.")
	}
	objectDigest, err := s.evidence.Put(ctx, doc)
	if err != nil {
		return evidence.SealRecord{}, err
	}

	sealID, err := idgen.SealID(s.ids)
	if err != nil {
		return evidence.SealRecord{}, internal(err, "The seal could not be recorded.")
	}
	rec := evidence.SealRecord{
		SealID:           sealID,
		TenantID:         sc.Tenant(),
		Cell:             s.cell,
		PeriodStart:      start,
		PeriodEnd:        end,
		LeafCount:        len(leaves),
		MerkleRoot:       root,
		SealObjectDigest: objectDigest,
		KeyID:            sig.KeyID,
		SealedAt:         now,
	}
	if err := s.seals.Append(ctx, rec); err != nil {
		return evidence.SealRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return evidence.SealRecord{}, err
	}
	return rec, nil
}

// SealVerification is the outcome of verifying one seal.
type SealVerification struct {
	SealID         id.SealID
	Verdict        evidence.SealVerdict
	KeyID          string
	LeafCount      int
	SignedRoot     canonical.Digest
	RecomputedRoot canonical.Digest
	// Detail says what failed, for the operator. Empty when VALID.
	Detail string
}

// VerifySeal checks a seal end to end: the signature over the payload, the
// payload against its index row, the root against the leaves recorded for the
// period today, and every leaf's result object against its digest.
//
// A verification failure is a verdict, not an error. Errors are reserved for
// not being able to perform the verification at all.
func (s *SealService) VerifySeal(ctx context.Context, sealID id.SealID) (SealVerification, error) {
	if _, err := requireRoleOrSystem(ctx, security.RoleAdmin, security.RoleAuditor, security.RoleAnalyst); err != nil {
		return SealVerification{}, err
	}
	rec, err := s.seals.ByID(ctx, sealID)
	if err != nil {
		return SealVerification{}, err
	}
	out := SealVerification{SealID: rec.SealID, KeyID: rec.KeyID, SignedRoot: rec.MerkleRoot}

	docBytes, err := s.evidence.Get(ctx, rec.SealObjectDigest)
	if err != nil {
		return SealVerification{}, err
	}
	var doc signedSeal
	dec := json.NewDecoder(bytes.NewReader(docBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return SealVerification{}, integrity(err, "The seal object is not a signed seal.")
	}
	payloadBytes, err := base64.StdEncoding.DecodeString(doc.Payload)
	if err != nil {
		return SealVerification{}, integrity(err, "The seal payload is not base64.")
	}

	// Verify before parsing, so nothing unverified reaches a parser. The
	// instant is the row's sealing time — the key must have been valid when
	// the seal was made, not now (ADR-0017 §2.7) — and is cross-checked
	// against the signed payload below, so the row cannot choose it freely.
	if err := s.verifier.Verify(ctx, doc.Signature, payloadBytes, rec.SealedAt); err != nil {
		out.Verdict = evidence.SealSignatureInvalid
		out.Detail = err.Error()
		return out, nil
	}
	payload, err := evidence.DecodePeriodSeal(payloadBytes)
	if err != nil {
		return SealVerification{}, integrity(err, "The signed seal payload is not canonical.")
	}
	out.SignedRoot = payload.MerkleRoot

	if mismatch := compareSealRecord(rec, payload, doc.Signature.KeyID); mismatch != "" {
		out.Verdict = evidence.SealRecordMismatch
		out.Detail = mismatch
		return out, nil
	}

	leaves, err := s.decisions.SealLeaves(ctx, payload.PeriodStart, payload.PeriodEnd)
	if err != nil {
		return SealVerification{}, err
	}
	out.LeafCount = len(leaves)
	root, err := evidence.PeriodRoot(leaves)
	if err != nil {
		out.Verdict = evidence.SealRootMismatch
		out.Detail = err.Error()
		return out, nil
	}
	out.RecomputedRoot = root
	if len(leaves) != payload.LeafCount || !root.Equal(payload.MerkleRoot) {
		out.Verdict = evidence.SealRootMismatch
		out.Detail = fmt.Sprintf("the period now holds %d decision(s) with root %s; the seal signed %d with root %s",
			len(leaves), root, payload.LeafCount, payload.MerkleRoot)
		return out, nil
	}

	// The root proves the leaves are the ones sealed. This proves the objects
	// the leaves name are still the objects that were sealed.
	for _, l := range leaves {
		if _, err := s.evidence.Get(ctx, l.ResultDigest); err != nil {
			out.Verdict = evidence.SealEvidenceMissing
			out.Detail = fmt.Sprintf("decision %s: result %s: %v", l.DecisionID, l.ResultDigest, err)
			return out, nil
		}
	}

	out.Verdict = evidence.SealValid
	return out, nil
}

// compareSealRecord reports the first way the index row disagrees with the
// signed payload it indexes, or "".
func compareSealRecord(rec evidence.SealRecord, p evidence.PeriodSeal, keyID string) string {
	switch {
	case rec.TenantID != p.TenantID:
		return "tenant differs from the signed payload"
	case rec.Cell != p.Cell:
		return "cell differs from the signed payload"
	case !rec.PeriodStart.Equal(p.PeriodStart) || !rec.PeriodEnd.Equal(p.PeriodEnd):
		return "period differs from the signed payload"
	case rec.LeafCount != p.LeafCount:
		return "leaf count differs from the signed payload"
	case !rec.MerkleRoot.Equal(p.MerkleRoot):
		return "root differs from the signed payload"
	case !rec.SealedAt.Equal(p.SealedAt):
		return "sealing instant differs from the signed payload"
	case rec.KeyID != keyID:
		return "signing key differs from the one recorded"
	}
	return ""
}
