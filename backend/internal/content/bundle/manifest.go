// Package bundle is the on-disk form of a compiled content bundle, and the one
// place that form is defined.
//
// It sits between two things that must never disagree: the compiler, which
// writes a bundle, and a regional cell, which loads one. Both import this
// package, so there is exactly one definition of what a bundle *is* — the
// alternative is a writer and a reader that agree until the day they do not,
// and the day they do not is a day a cell either refuses valid content or
// accepts content nobody compiled.
//
// Two decisions shape everything here.
//
// **The manifest file is the canonical bytes.** It is not JSON that happens to
// be canonicalizable; it is the output of internal/platform/canonical, written
// verbatim. A cell digests the bytes it read from disk (canonical.SumBytes) and
// never re-encodes them, because re-encoding would mean the digest attests to
// the cell's serializer rather than to the artifact that was reviewed and
// signed (ADR-0011 §2.7).
//
// **The digest is not in the manifest.** A document cannot contain its own
// digest without the digest being computed over something other than the
// document. So the digest lives in the seal, which is the thing that is signed,
// and rule.Manifest's Digest field is populated by the loader from the verified
// seal rather than read from the file.
package bundle

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/rule"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// Artifact names what a seal is sealing. It exists so that a seal over an
// evidence period cannot be presented as a seal over a content bundle: the
// signed payload says which, and a verifier that expects one refuses the other
// (ADR-0011 §2.4's domain-separation reasoning, one level up).
const Artifact = "content-bundle"

// EncodeManifest renders a manifest as the canonical bytes that go on disk.
//
// Every collection is explicitly ordered before encoding. ADR-0011 P4 makes
// array order significant and makes the ordering key a property of the schema,
// so the schema declares it here: constants by name, nodes by identifier, roots
// by identifier. None of the three carries meaning in its order — which is
// exactly why it must be imposed, because otherwise the order is whatever the
// compiler's map iteration produced, and two compilations of one source would
// digest differently.
func EncodeManifest(m rule.Manifest) ([]byte, error) {
	v, err := canonicalManifest(m)
	if err != nil {
		return nil, err
	}
	return canonical.Encode(v)
}

// DecodeManifest reads manifest bytes and reports their digest.
//
// The digest is over the bytes as supplied, never over a re-encoding. The
// re-encoding that does happen is a *check*: if the bytes are not already in
// canonical form, this refuses them. A non-canonical file would still decode,
// would still execute, and would digest to something no other implementation
// could reproduce — which is the failure mode canon/v1 exists to make
// impossible rather than merely unlikely.
func DecodeManifest(data []byte) (rule.Manifest, canonical.Digest, error) {
	var m rule.Manifest
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return rule.Manifest{}, canonical.Digest{}, fmt.Errorf("bundle: decode manifest: %w", err)
	}
	if dec.More() {
		return rule.Manifest{}, canonical.Digest{}, fmt.Errorf("bundle: manifest file carries more than one document")
	}
	if m.Digest != "" {
		// The manifest file must not carry a digest field: a document
		// containing its own digest is a document whose digest was taken over
		// something else. The digest belongs in the seal, which is signed.
		return rule.Manifest{}, canonical.Digest{}, fmt.Errorf("bundle: manifest carries a digest field; the digest is recorded in the seal")
	}

	round, err := EncodeManifest(m)
	if err != nil {
		return rule.Manifest{}, canonical.Digest{}, err
	}
	if !bytes.Equal(round, data) {
		return rule.Manifest{}, canonical.Digest{}, fmt.Errorf(
			"bundle: manifest is not in %s form; it decodes but its bytes are not the canonical encoding of what it decodes to",
			canonical.ProfileVersion)
	}

	return m, canonical.SumBytes(data), nil
}

// canonicalManifest builds the document. It is written out field by field
// rather than reflected from the struct, which is the whole point of the Value
// tree (ADR-0011 §2.7): what gets digested is what somebody wrote down, not
// what a struct tag happened to say.
func canonicalManifest(m rule.Manifest) (canonical.Value, error) {
	if m.BundleID == "" {
		return canonical.Value{}, fmt.Errorf("bundle: manifest names no bundle id")
	}
	if len(m.Nodes) == 0 {
		return canonical.Value{}, fmt.Errorf("bundle: manifest %s has no nodes", m.BundleID)
	}

	constants := append([]rule.Constant(nil), m.Constants...)
	sort.Slice(constants, func(i, j int) bool { return constants[i].Name < constants[j].Name })
	constValues := make([]canonical.Value, 0, len(constants))
	for _, c := range constants {
		constValues = append(constValues, canonical.Object(
			canonical.F("name", canonical.String(c.Name)),
			canonical.F("type", canonical.String(string(c.Type))),
			canonical.F("value", canonical.String(c.Value)),
			// Absent rather than empty: a MONEY constant has a currency and a
			// STRING constant does not, and P3 says those are different facts.
			canonical.F("currency", canonical.OptString(c.Currency)),
			canonical.F("unit", canonical.OptString(c.Unit)),
			canonical.F("basis", canonical.OptString(c.Basis)),
		))
	}

	nodes := append([]rule.Node(nil), m.Nodes...)
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	nodeValues := make([]canonical.Value, 0, len(nodes))
	for _, n := range nodes {
		policy, err := canonicalPolicy(n.Policy)
		if err != nil {
			return canonical.Value{}, fmt.Errorf("bundle: node %s: %w", n.ID, err)
		}
		nodeValues = append(nodeValues, canonical.Object(
			canonical.F("id", canonical.String(string(n.ID))),
			canonical.F("op", canonical.String(string(n.Op))),
			canonical.F("type", canonical.String(string(n.Type))),
			// Argument order is significant — SUB and QUO are not commutative
			// and SELECT's three arguments are positional — so this array is
			// the caller's order and is never sorted.
			canonical.F("args", canonicalNodeIDs(n.Args)),
			canonical.F("const", canonical.OptString(n.Const)),
			canonical.F("field", canonical.OptString(n.Field)),
			canonical.F("comparison", canonical.OptString(string(n.Comparison))),
			canonical.F("emit", canonical.OptString(n.Emit)),
			canonical.F("reason", canonical.OptString(string(n.Reason))),
			canonical.F("policy", policy),
			canonical.F("ruleVersion", canonical.String(n.RuleVersion)),
			canonical.F("ruleSemanticId", canonical.String(n.RuleSemanticID)),
		))
	}

	roots := append([]rule.NodeID(nil), m.Roots...)
	sort.Slice(roots, func(i, j int) bool { return roots[i] < roots[j] })

	return canonical.Object(
		canonical.F("bundleId", canonical.String(m.BundleID)),
		canonical.F("irVersion", canonical.Integer(int64(m.IRVersion))),
		canonical.F("constants", canonical.Array(constValues...)),
		canonical.F("nodes", canonical.Array(nodeValues...)),
		canonical.F("roots", canonicalNodeIDs(roots)),
	), nil
}

// canonicalNodeIDs renders a node-identifier list, or Absent for an empty one.
//
// Absent rather than an empty array, because rule.Node tags Args omitempty: an
// empty array in the canonical document would not survive a round trip through
// the struct, and DecodeManifest's canonical-form check would then reject a
// file this package had just written.
func canonicalNodeIDs(ids []rule.NodeID) canonical.Value {
	if len(ids) == 0 {
		return canonical.Absent()
	}
	items := make([]canonical.Value, 0, len(ids))
	for _, id := range ids {
		items = append(items, canonical.String(string(id)))
	}
	return canonical.Array(items...)
}

// canonicalPolicy renders a rounding policy, or Absent where a node does not
// round. Mode and basis are written by name and the scale is an integer, which
// matches the form fiscal.RoundingPolicy decodes from — content and evidence
// carry one shape for a policy, not two.
func canonicalPolicy(p *fiscal.RoundingPolicy) (canonical.Value, error) {
	if p == nil {
		return canonical.Absent(), nil
	}
	if p.Mode() == "" || p.Basis() == "" {
		// A zero RoundingPolicy is what a composite literal elsewhere in the
		// estate produces (ADR-0002 §2.3). Encoding one would put an
		// unroundable policy into signed content.
		return canonical.Value{}, fmt.Errorf("rounding policy is the zero value; a policy comes from decoded content")
	}
	return canonical.Object(
		canonical.F("mode", canonical.String(string(p.Mode()))),
		canonical.F("scale", canonical.Integer(int64(p.Scale()))),
		canonical.F("basis", canonical.String(string(p.Basis()))),
	), nil
}
