package canonical_test

import (
	"fmt"
	"testing"

	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

func leaves(t *testing.T, n int) []canonical.Digest {
	t.Helper()
	out := make([]canonical.Digest, n)
	for i := range out {
		d, err := canonical.Sum(canonical.String(fmt.Sprintf("leaf-%d", i)))
		if err != nil {
			t.Fatal(err)
		}
		out[i] = d
	}
	return out
}

// Every leaf of every tree up to 33 leaves — powers of two, one either side
// of them, and the odd promotions in between — proves its inclusion against
// MerkleRoot, and nothing else does.
func TestEveryLeafProvesItsInclusionAgainstTheRoot(t *testing.T) {
	for size := 1; size <= 33; size++ {
		ls := leaves(t, size)
		root, err := canonical.MerkleRoot(ls)
		if err != nil {
			t.Fatal(err)
		}
		for i := range ls {
			path, err := canonical.InclusionProof(ls, i)
			if err != nil {
				t.Fatalf("size %d leaf %d: %v", size, i, err)
			}
			if err := canonical.VerifyInclusion(ls[i], i, size, path, root); err != nil {
				t.Fatalf("size %d leaf %d: %v", size, i, err)
			}
			// The wrong leaf, the wrong position, the wrong size and a
			// truncated or extended path all fail.
			other := ls[(i+1)%size]
			if size > 1 && canonical.VerifyInclusion(other, i, size, path, root) == nil {
				t.Fatalf("size %d leaf %d: another leaf verified in its place", size, i)
			}
			if size > 1 && canonical.VerifyInclusion(ls[i], (i+1)%size, size, path, root) == nil {
				t.Fatalf("size %d leaf %d: verified at another position", size, i)
			}
			// The size is not checked here and cannot be: leaf 0 of three
			// has the same path as leaf 0 of four. A path proves membership
			// of a tree whose size the verifier already trusts, which is why
			// the size a decision's proof is checked against is the signed
			// seal's leaf count, never the proof's own claim.
			if len(path) > 0 && canonical.VerifyInclusion(ls[i], i, size, path[:len(path)-1], root) == nil {
				t.Fatalf("size %d leaf %d: a truncated path verified", size, i)
			}
			if canonical.VerifyInclusion(ls[i], i, size, append(path, root), root) == nil {
				t.Fatalf("size %d leaf %d: an extended path verified", size, i)
			}
		}
	}
	if _, err := canonical.InclusionProof(leaves(t, 3), 3); err == nil {
		t.Fatal("a proof for a leaf outside the tree")
	}
}
