package gateway

import (
	"testing"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/ai"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/privacy"
)

// toWire()'s own encoding behaviour for data_classes, isolated from the full
// fake-gRPC-server harness TestGRPCCarriesTheCallAndTheReply already uses.
// These pin exactly what contracts/privacy/data-class-parity-cases.json's
// request_duplicates note now says: toWire sorts but does not deduplicate,
// so a duplicate survives encoding unchanged and reaches the real Python
// Gateway, which is the wire decoder's job to reject (see
// intelligence/tests/test_gateway_contract.py's
// test_decode_call_rejects_duplicate_labels).

func wireTestCall(classes []privacy.Class) Call {
	return Call{
		Kind: KindClassificationProposal,
		Governance: ai.GovernanceContext{
			Tenant:           parityTestTenant,
			UseCase:          "parity-test",
			AuthorityOutcome: ai.AuthorityOutcomeA1,
			RiskTier:         ai.RiskTierT1,
			Region:           "eu-west-1",
			DataClasses:      classes,
		},
		SubjectRef: "sku:TEST-001",
		Input:      []byte(`{}`),
	}
}

func TestToWireSortsDataClasses(t *testing.T) {
	w := toWire(wireTestCall([]privacy.Class{"P1", "P0"}))
	got := w.Governance.DataClasses
	want := []string{"P0", "P1"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("toWire data_classes = %v, want %v (sorted)", got, want)
	}
}

func TestToWireDoesNotDeduplicateDataClasses(t *testing.T) {
	w := toWire(wireTestCall([]privacy.Class{"P0", "P0"}))
	got := w.Governance.DataClasses
	want := []string{"P0", "P0"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("toWire data_classes = %v, want %v (both entries kept, not deduplicated)", got, want)
	}
}
