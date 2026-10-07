package evidence

import (
	"bytes"
	"fmt"
	"sort"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// Period sealing — ADR-0011 §2.4 and §2.5, EVID-001's period seal.
//
// A seal is a signed statement that, for one tenant in one cell, the decisions
// recorded in [PeriodStart, PeriodEnd) were exactly these, with exactly these
// results. The statement is a Merkle root over one leaf per decision; the
// signature covers the root together with everything that says what the root
// is a root *of*, so a valid signature cannot be lifted onto another period,
// tenant or cell.
//
// Granularity is tenant-period. ADR-0011 §7 leaves the choice open between
// tenant-period and cell-period; tenant-period is taken for the prototype
// because every other read in the estate is tenant-scoped (ADR-0012 §2.7), and
// a seal an auditor for one tenant can verify without seeing another tenant's
// leaves is the one that does not need a redaction step. It is recorded in the
// seal itself (SealGranularity) so a later change is a new granularity beside
// this one, not a reinterpretation of it.

// SealArtifact distinguishes an evidence-period seal from a content-bundle
// seal, which is signed through the same kms interface by a different key
// hierarchy (ADR-0017 §2.6).
const SealArtifact = "evidence-period-seal"

// SealGranularity is the scope one seal covers.
const SealGranularity = "TENANT_PERIOD"

// LeafOrder is the canonical order of a seal's leaves (ADR-0011 §2.4: "leaf
// order is the canonical order of the sealed records, declared per seal type").
// It is recorded in the signed payload, so a verifier never has to guess it.
const LeafOrder = "recordedAt,decisionId"

// SealLeaf is one decision's contribution to a seal.
//
// The leaf commits to the result digest, and the result names its envelope by
// digest, and the envelope carries the input and the bundle — so this one
// leaf transitively commits to everything the decision was and depended on.
// The decision identity and recorded instant are in the leaf as well, so that
// swapping two decisions' results, or moving one in time, changes the root
// even though every individual result is untouched.
type SealLeaf struct {
	DecisionID   id.DecisionID
	RecordedAt   time.Time
	ResultDigest canonical.Digest
}

// Digest is the leaf's value in the tree.
func (l SealLeaf) Digest() (canonical.Digest, error) {
	if l.DecisionID.IsZero() || l.RecordedAt.IsZero() || l.ResultDigest.IsZero() {
		return canonical.Digest{}, fmt.Errorf("evidence: seal leaf is incomplete")
	}
	return canonical.Sum(canonical.Object(
		canonical.F("decisionId", canonical.String(l.DecisionID.String())),
		canonical.F("recordedAt", canonical.Time(l.RecordedAt)),
		canonical.F("resultDigest", canonical.String(l.ResultDigest.String())),
	))
}

// OrderLeaves sorts leaves into LeafOrder, in place.
//
// The repository returns them in this order already. Sorting again here is
// not redundancy: the order is part of what is signed, and it should rest on
// this function rather than on a SQL ORDER BY and a collation nobody declared.
func OrderLeaves(leaves []SealLeaf) {
	sort.SliceStable(leaves, func(i, j int) bool {
		if !leaves[i].RecordedAt.Equal(leaves[j].RecordedAt) {
			return leaves[i].RecordedAt.Before(leaves[j].RecordedAt)
		}
		return leaves[i].DecisionID.String() < leaves[j].DecisionID.String()
	})
}

// PeriodRoot is the Merkle root over leaves in LeafOrder. It orders a copy, so
// the caller's slice is left as it was.
func PeriodRoot(leaves []SealLeaf) (canonical.Digest, error) {
	ordered := append([]SealLeaf(nil), leaves...)
	OrderLeaves(ordered)
	digests := make([]canonical.Digest, len(ordered))
	for i, l := range ordered {
		d, err := l.Digest()
		if err != nil {
			return canonical.Digest{}, fmt.Errorf("evidence: leaf %d (%s): %w", i, l.DecisionID, err)
		}
		digests[i] = d
	}
	return canonical.MerkleRoot(digests)
}

// PeriodSeal is the payload a seal signature covers (ADR-0011 §2.5: root, seal
// metadata, period identity, cell identity and canon profile version).
type PeriodSeal struct {
	TenantID    id.TenantID
	Cell        string
	PeriodStart time.Time
	PeriodEnd   time.Time
	LeafCount   int
	MerkleRoot  canonical.Digest
	// SealedAt is when the seal was made, and the instant the signing key's
	// validity is judged at on verification — so a seal made last year by a
	// key since rotated still verifies, which it must (ADR-0017 §2.7).
	SealedAt time.Time
}

// Validate refuses a payload that does not say what it seals.
func (p PeriodSeal) Validate() error {
	switch {
	case p.TenantID.IsZero():
		return fmt.Errorf("evidence: seal names no tenant")
	case p.Cell == "":
		return fmt.Errorf("evidence: seal names no cell")
	case p.PeriodStart.IsZero() || p.PeriodEnd.IsZero() || !p.PeriodEnd.After(p.PeriodStart):
		return fmt.Errorf("evidence: seal period [%s, %s) is empty or inverted",
			canonical.FormatTime(p.PeriodStart), canonical.FormatTime(p.PeriodEnd))
	case p.LeafCount < 0:
		return fmt.Errorf("evidence: seal leaf count is negative")
	case p.MerkleRoot.IsZero():
		return fmt.Errorf("evidence: seal names no root")
	case p.SealedAt.IsZero():
		return fmt.Errorf("evidence: seal names no sealing instant")
	case p.SealedAt.Before(p.PeriodEnd):
		// A period is sealed after it closes. A seal dated inside its own
		// period claims completeness over records that could still arrive.
		return fmt.Errorf("evidence: seal is dated before its period ends")
	}
	return nil
}

// Canonical renders the payload.
func (p PeriodSeal) Canonical() canonical.Value {
	return canonical.Object(
		canonical.F("artifact", canonical.String(SealArtifact)),
		canonical.F("granularity", canonical.String(SealGranularity)),
		canonical.F("tenantId", canonical.String(p.TenantID.String())),
		canonical.F("cell", canonical.String(p.Cell)),
		canonical.F("periodStart", canonical.Time(p.PeriodStart)),
		canonical.F("periodEnd", canonical.Time(p.PeriodEnd)),
		canonical.F("leafCount", canonical.Integer(int64(p.LeafCount))),
		canonical.F("leafOrder", canonical.String(LeafOrder)),
		canonical.F("merkleRoot", canonical.String(p.MerkleRoot.String())),
		canonical.F("canonProfile", canonical.String(canonical.ProfileVersion)),
		canonical.F("sealedAt", canonical.Time(p.SealedAt)),
	)
}

// EncodePeriodSeal renders the payload as the bytes that are signed.
func EncodePeriodSeal(p PeriodSeal) ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return canonical.Encode(p.Canonical())
}

// DecodePeriodSeal reads a payload, with DecodeEnvelope's round-trip check: a
// signed payload that is not canonical is one another verifier could read
// differently, and is refused.
func DecodePeriodSeal(data []byte) (PeriodSeal, error) {
	var w struct {
		Artifact     string `json:"artifact"`
		Granularity  string `json:"granularity"`
		TenantID     string `json:"tenantId"`
		Cell         string `json:"cell"`
		PeriodStart  string `json:"periodStart"`
		PeriodEnd    string `json:"periodEnd"`
		LeafCount    int    `json:"leafCount"`
		LeafOrder    string `json:"leafOrder"`
		MerkleRoot   string `json:"merkleRoot"`
		CanonProfile string `json:"canonProfile"`
		SealedAt     string `json:"sealedAt"`
	}
	if err := strictDecode(data, &w); err != nil {
		return PeriodSeal{}, fmt.Errorf("evidence: decode seal: %w", err)
	}
	switch {
	case w.Artifact != SealArtifact:
		return PeriodSeal{}, fmt.Errorf("evidence: seal names artifact %q, want %q", w.Artifact, SealArtifact)
	case w.Granularity != SealGranularity:
		return PeriodSeal{}, fmt.Errorf("evidence: seal granularity %q is not implemented here", w.Granularity)
	case w.LeafOrder != LeafOrder:
		return PeriodSeal{}, fmt.Errorf("evidence: seal leaf order %q is not implemented here", w.LeafOrder)
	case w.CanonProfile != canonical.ProfileVersion:
		return PeriodSeal{}, fmt.Errorf("evidence: seal is under profile %q, this build implements %q",
			w.CanonProfile, canonical.ProfileVersion)
	}
	tenant, err := id.ParseTenantID(w.TenantID)
	if err != nil {
		return PeriodSeal{}, fmt.Errorf("evidence: seal tenant: %w", err)
	}
	root, err := canonical.ParseDigest(w.MerkleRoot)
	if err != nil {
		return PeriodSeal{}, fmt.Errorf("evidence: seal root: %w", err)
	}
	var instants [3]time.Time
	for i, s := range []string{w.PeriodStart, w.PeriodEnd, w.SealedAt} {
		if instants[i], err = parseInstant(s); err != nil {
			return PeriodSeal{}, fmt.Errorf("evidence: seal: %w", err)
		}
	}
	p := PeriodSeal{
		TenantID: tenant, Cell: w.Cell,
		PeriodStart: instants[0], PeriodEnd: instants[1], SealedAt: instants[2],
		LeafCount: w.LeafCount, MerkleRoot: root,
	}
	again, err := EncodePeriodSeal(p)
	if err != nil {
		return PeriodSeal{}, err
	}
	if !bytes.Equal(again, data) {
		return PeriodSeal{}, fmt.Errorf("evidence: seal payload is not in %s form", canonical.ProfileVersion)
	}
	return p, nil
}

// SealRecord is a seal's row in the cell database. As with Record, the
// evidence object it names is authoritative and the row is an index to it.
type SealRecord struct {
	SealID           id.SealID
	TenantID         id.TenantID
	Cell             string
	PeriodStart      time.Time
	PeriodEnd        time.Time
	LeafCount        int
	MerkleRoot       canonical.Digest
	SealObjectDigest canonical.Digest
	KeyID            string
	SealedAt         time.Time
}

// SealVerdict is what verifying a seal established.
type SealVerdict string

// The verdicts, in the order a verifier reaches them.
const (
	// SealValid: the signature verifies, the payload is canonical, and the
	// leaves in the store today reproduce the signed root.
	SealValid SealVerdict = "VALID"
	// SealSignatureInvalid: the payload was not signed by a key the verifier
	// trusts for the instant it claims.
	SealSignatureInvalid SealVerdict = "SIGNATURE_INVALID"
	// SealRecordMismatch: the database row disagrees with the signed payload
	// it indexes.
	SealRecordMismatch SealVerdict = "RECORD_MISMATCH"
	// SealRootMismatch: the leaves recorded for the period today do not
	// reproduce the signed root. Something in the period moved.
	SealRootMismatch SealVerdict = "ROOT_MISMATCH"
	// SealEvidenceMissing: a leaf names a result object the evidence store
	// cannot produce, or produces bytes that do not hash to it.
	SealEvidenceMissing SealVerdict = "EVIDENCE_MISSING"
)

// Inclusion is one decision's proof of membership in a sealed period: its
// leaf, where the leaf sits in LeafOrder, and the RFC 6962 audit path from it
// to the seal's root. Checked against the signed payload's root and leaf
// count — never against a size the proof itself supplies.
type Inclusion struct {
	Leaf       SealLeaf
	LeafDigest canonical.Digest
	Index      int
	TreeSize   int
	Path       []canonical.Digest
}

// PeriodInclusion builds the inclusion proof for one decision among a
// period's leaves.
func PeriodInclusion(leaves []SealLeaf, decisionID id.DecisionID) (Inclusion, error) {
	ordered := append([]SealLeaf(nil), leaves...)
	OrderLeaves(ordered)
	digests := make([]canonical.Digest, len(ordered))
	index := -1
	for i, l := range ordered {
		d, err := l.Digest()
		if err != nil {
			return Inclusion{}, fmt.Errorf("evidence: leaf %d (%s): %w", i, l.DecisionID, err)
		}
		digests[i] = d
		if l.DecisionID == decisionID {
			index = i
		}
	}
	if index < 0 {
		return Inclusion{}, fmt.Errorf("evidence: decision %s is not among the period's leaves", decisionID)
	}
	path, err := canonical.InclusionProof(digests, index)
	if err != nil {
		return Inclusion{}, err
	}
	return Inclusion{Leaf: ordered[index], LeafDigest: digests[index], Index: index, TreeSize: len(ordered), Path: path}, nil
}

// Verify checks the proof against a seal's signed root and leaf count.
func (in Inclusion) Verify(root canonical.Digest, leafCount int) error {
	if in.TreeSize != leafCount {
		return fmt.Errorf("evidence: the proof is for a tree of %d; the seal signed %d", in.TreeSize, leafCount)
	}
	d, err := in.Leaf.Digest()
	if err != nil {
		return err
	}
	if !d.Equal(in.LeafDigest) {
		return fmt.Errorf("evidence: the leaf does not digest to the proof's leaf digest")
	}
	return canonical.VerifyInclusion(d, in.Index, leafCount, in.Path, root)
}
