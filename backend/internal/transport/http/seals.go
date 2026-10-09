package http

import (
	"net/http"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/evidence"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/transport/http/gen"
)

// The evidence surface: period seals, their verification, and a decision's
// inclusion proof (ADR-0011 §2.4, §2.5; EVID-001).

func toSeal(rec evidence.SealRecord) gen.Seal {
	return gen.Seal{
		ID: rec.SealID.String(), PeriodStart: canonical.FormatTime(rec.PeriodStart), PeriodEnd: canonical.FormatTime(rec.PeriodEnd),
		LeafCount: saturate32(rec.LeafCount), MerkleRoot: rec.MerkleRoot.String(), KeyID: rec.KeyID, Cell: rec.Cell,
		SealedAt: canonical.FormatTime(rec.SealedAt),
	}
}

func (rt *Router) sealsUnavailable(w http.ResponseWriter, r *http.Request) bool {
	if rt.Seals != nil {
		return false
	}
	writeProblem(w, r, rt.log, errs.New(errs.CategoryUnavailable, errs.ReasonUnavailable,
		"This cell has no evidence-seal keyring. The request was not applied."))
	return true
}

func (rt *Router) sealID(w http.ResponseWriter, r *http.Request) (id.SealID, bool) {
	sealID, err := id.ParseSealID(r.PathValue("sealId"))
	if err != nil {
		writeProblem(w, r, rt.log, errs.Invalid("sealId", errs.ReasonInvalidValue, "That is not a valid seal identifier."))
		return id.SealID{}, false
	}
	return sealID, true
}

func (rt *Router) handleListSeals(w http.ResponseWriter, r *http.Request) {
	if rt.sealsUnavailable(w, r) {
		return
	}
	recs, err := rt.Seals.Seals(r.Context(), limitOf(r))
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	out := gen.SealList{Seals: make([]gen.Seal, len(recs))}
	for i, rec := range recs {
		out.Seals[i] = toSeal(rec)
	}
	writeJSON(w, r, rt.log, http.StatusOK, out)
}

func (rt *Router) handleSealPeriod(w http.ResponseWriter, r *http.Request) {
	if rt.sealsUnavailable(w, r) {
		return
	}
	var req gen.SealRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	start, err := parseTimestamp("periodStart", req.PeriodStart)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	end, err := parseTimestamp("periodEnd", req.PeriodEnd)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	rec, err := rt.Seals.SealPeriod(r.Context(), start, end)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusCreated, toSeal(rec))
}

func (rt *Router) handleGetSeal(w http.ResponseWriter, r *http.Request) {
	if rt.sealsUnavailable(w, r) {
		return
	}
	sealID, ok := rt.sealID(w, r)
	if !ok {
		return
	}
	doc, err := rt.Seals.Seal(r.Context(), sealID)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusOK, gen.SealDocument{
		Seal:          toSeal(doc.Record),
		SignedPayload: doc.Payload,
		Signature: gen.SealSignature{
			KeyID: doc.Signature.KeyID, Algorithm: gen.SealSignatureAlgorithm(doc.Signature.Algorithm),
			Value: doc.Signature.Bytes,
		},
	})
}

func (rt *Router) handleVerifySeal(w http.ResponseWriter, r *http.Request) {
	if rt.sealsUnavailable(w, r) {
		return
	}
	sealID, ok := rt.sealID(w, r)
	if !ok {
		return
	}
	v, err := rt.Seals.VerifySeal(r.Context(), sealID)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	out := gen.SealVerification{
		SealID: v.SealID.String(), Verdict: gen.SealVerificationVerdict(v.Verdict), KeyID: v.KeyID,
		LeafCount: saturate32(v.LeafCount), SignedRoot: v.SignedRoot.String(),
	}
	if !v.RecomputedRoot.IsZero() {
		root := v.RecomputedRoot.String()
		out.RecomputedRoot = &root
	}
	if v.Detail != "" {
		detail := v.Detail
		out.Detail = &detail
	}
	writeJSON(w, r, rt.log, http.StatusOK, out)
}

func (rt *Router) handleDecisionInclusion(w http.ResponseWriter, r *http.Request) {
	if rt.sealsUnavailable(w, r) {
		return
	}
	decisionID, ok := rt.decisionID(w, r)
	if !ok {
		return
	}
	seal, proof, err := rt.Seals.Inclusion(r.Context(), decisionID)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	path := make([]gen.Digest, len(proof.Path))
	for i, p := range proof.Path {
		path[i] = p.String()
	}
	writeJSON(w, r, rt.log, http.StatusOK, gen.InclusionProof{
		SealID: seal.SealID.String(), MerkleRoot: seal.MerkleRoot.String(), LeafCount: saturate32(seal.LeafCount),
		LeafIndex: saturate32(proof.Index), LeafDigest: proof.LeafDigest.String(), AuditPath: path,
		Leaf: gen.SealLeaf{
			DecisionID: proof.Leaf.DecisionID.String(), RecordedAt: canonical.FormatTime(proof.Leaf.RecordedAt),
			ResultDigest: proof.Leaf.ResultDigest.String(),
		},
	})
}
