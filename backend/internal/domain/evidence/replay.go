package evidence

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// ReplayVerdict is what a replay established.
type ReplayVerdict string

// The verdicts.
const (
	// ReplayMatch: the result rebuilt from the envelope alone is byte-identical
	// to the recorded one. This is the W1 exit criterion, and the only verdict
	// that means the decision replays.
	ReplayMatch ReplayVerdict = "MATCH"
	// ReplayDiverged: the rebuilt result differs. ADR-0015 §2.8 makes any
	// non-zero count of these a P1 — it means the same envelope produced two
	// answers, so one of the runtimes is not deterministic or not the runtime
	// the record claims.
	ReplayDiverged ReplayVerdict = "DIVERGED"
	// ReplayBundleUnavailable: the content bundle the envelope names is not
	// held here, so nothing was evaluated. It is not a divergence and is not
	// counted as one; it is an operational gap in bundle retention.
	ReplayBundleUnavailable ReplayVerdict = "BUNDLE_UNAVAILABLE"
)

// ReplayReport is the outcome of replaying one decision.
type ReplayReport struct {
	DecisionID     id.DecisionID
	Verdict        ReplayVerdict
	EnvelopeDigest canonical.Digest
	RecordedResult canonical.Digest
	// ReplayedResult is zero when nothing was evaluated.
	ReplayedResult canonical.Digest
	// Divergence names the first point of difference, for the operator who
	// has to find out why. Empty unless Verdict is DIVERGED.
	Divergence string
}

// CompareResults compares a recorded result with a replayed one.
//
// The verdict rests on bytes and nothing else: canon/v1 guarantees one
// encoding per meaning, so equal bytes is equal meaning and any other test
// would be a weaker one. The divergence path is diagnostic only — it is
// computed after the verdict and cannot change it.
func CompareResults(recorded, replayed []byte) (match bool, divergence string) {
	if bytes.Equal(recorded, replayed) {
		return true, ""
	}
	var a, b any
	if json.Unmarshal(recorded, &a) != nil || json.Unmarshal(replayed, &b) != nil {
		return false, "(the recorded result is not valid JSON)"
	}
	if path, ok := firstDifference("$", a, b); ok {
		return false, path
	}
	// Semantically equal documents with different bytes: one of them is not
	// canonical. Still a divergence — the recorded bytes are what was sealed.
	return false, "(same content, different encoding)"
}

func firstDifference(path string, a, b any) (string, bool) {
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok {
			return path, true
		}
		names := make([]string, 0, len(av)+len(bv))
		for k := range av {
			names = append(names, k)
		}
		for k := range bv {
			if _, dup := av[k]; !dup {
				names = append(names, k)
			}
		}
		sort.Strings(names)
		for _, k := range names {
			x, inA := av[k]
			y, inB := bv[k]
			if inA != inB {
				return path + "." + k, true
			}
			if p, diff := firstDifference(path+"."+k, x, y); diff {
				return p, true
			}
		}
		return "", false
	case []any:
		bv, ok := b.([]any)
		if !ok {
			return path, true
		}
		for i := 0; i < len(av) && i < len(bv); i++ {
			if p, diff := firstDifference(fmt.Sprintf("%s[%d]", path, i), av[i], bv[i]); diff {
				return p, true
			}
		}
		if len(av) != len(bv) {
			return fmt.Sprintf("%s[%d]", path, min(len(av), len(bv))), true
		}
		return "", false
	default:
		if a != b {
			return path, true
		}
		return "", false
	}
}
