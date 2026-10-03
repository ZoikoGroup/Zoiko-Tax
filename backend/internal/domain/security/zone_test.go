package security_test

import (
	"errors"
	"testing"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
)

// The policy table is data, so its integrity is tested as data: every flow
// names real zones, crosses a real boundary, is justified, and appears once.
func TestFlowTableIsWellFormed(t *testing.T) {
	if len(security.Zones) != 8 {
		t.Fatalf("SEC-001 §3 defines eight zones, Z0 to Z7; have %d", len(security.Zones))
	}
	seen := map[[2]security.Zone]bool{}
	for _, f := range security.Flows {
		if !f.From.Valid() || !f.To.Valid() {
			t.Errorf("flow %s -> %s names an unknown zone", f.From, f.To)
		}
		if f.From == f.To {
			t.Errorf("flow %s -> %s is not a crossing; same-zone calls need no entry", f.From, f.To)
		}
		if f.Why == "" {
			t.Errorf("flow %s -> %s states no reason", f.From, f.To)
		}
		k := [2]security.Zone{f.From, f.To}
		if seen[k] {
			t.Errorf("flow %s -> %s is listed twice", f.From, f.To)
		}
		seen[k] = true
	}
}

// The absences that are the point. Each is a shortcut that will be proposed,
// and each would breach a boundary statement in SEC-001 §3 or an ADR.
func TestForbiddenCrossingsStayForbidden(t *testing.T) {
	forbidden := []struct {
		from, to security.Zone
		why      string
	}{
		{security.ZoneContentBuild, security.ZoneRegionalCell, "content reaches a cell only through signed distribution (ADR-0005)"},
		{security.ZoneAI, security.ZoneRegionalCell, "the AI plane has no path to authoritative state (ADR-0006 §2.6)"},
		{security.ZoneAI, security.ZoneGlobalControl, "the AI plane answers calls; it does not initiate them"},
		{security.ZonePublicEdge, security.ZoneAdminSupport, "workforce access does not arrive through the public edge"},
		{security.ZonePublicEdge, security.ZoneContentBuild, "the build plane is not internet-facing"},
		{security.ZonePrivateRuntime, security.ZoneRegionalCell, "a customer runtime is outside the cell's trust boundary"},
		{security.ZoneSecurity, security.ZoneRegionalCell, "the security plane is called; it does not reach into data planes"},
	}
	for _, f := range forbidden {
		if security.FlowPermitted(f.from, f.to) {
			t.Errorf("%s -> %s is permitted, and must not be: %s", f.from, f.to, f.why)
		}
		err := security.CheckCall(security.Call{From: f.from, To: f.to, WorkloadIdentity: "spiffe://ztax.prod/x"})
		if !errors.Is(err, security.ErrFlowNotPermitted) {
			t.Errorf("%s -> %s: want ErrFlowNotPermitted even with a workload identity, got %v", f.from, f.to, err)
		}
	}
}

func TestCheckCall(t *testing.T) {
	const svid = "spiffe://ztax.prod/cell/eu-1/ztax-core"
	cases := []struct {
		name string
		call security.Call
		want error
	}{
		{"cell to gateway with identity", security.Call{From: security.ZoneRegionalCell, To: security.ZoneAI, WorkloadIdentity: svid}, nil},
		{"cell to gateway without identity", security.Call{From: security.ZoneRegionalCell, To: security.ZoneAI}, security.ErrNoWorkloadIdentity},
		// The identity check comes before the table, so a caller with no
		// identity learns nothing about which flows exist.
		{"forbidden flow without identity", security.Call{From: security.ZoneAI, To: security.ZoneRegionalCell}, security.ErrNoWorkloadIdentity},
		{"same zone", security.Call{From: security.ZoneRegionalCell, To: security.ZoneRegionalCell}, nil},
		{"unknown source", security.Call{From: "Z8", To: security.ZoneRegionalCell, WorkloadIdentity: svid}, security.ErrUnknownZone},
		{"unknown destination", security.Call{From: security.ZoneRegionalCell, To: "", WorkloadIdentity: svid}, security.ErrUnknownZone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := security.CheckCall(c.call)
			if c.want == nil {
				if err != nil {
					t.Fatalf("want permitted, got %v", err)
				}
				return
			}
			if !errors.Is(err, c.want) {
				t.Fatalf("want %v, got %v", c.want, err)
			}
		})
	}
}

// Every permitted flow is a crossing that needs an identity: there is no entry
// in the table that a caller without one can use.
func TestEveryPermittedFlowRequiresWorkloadIdentity(t *testing.T) {
	for _, f := range security.Flows {
		if err := security.CheckCall(security.Call{From: f.From, To: f.To}); !errors.Is(err, security.ErrNoWorkloadIdentity) {
			t.Errorf("%s -> %s without identity: want ErrNoWorkloadIdentity, got %v", f.From, f.To, err)
		}
		if err := security.CheckCall(security.Call{From: f.From, To: f.To, WorkloadIdentity: "spiffe://ztax.prod/w"}); err != nil {
			t.Errorf("%s -> %s with identity: want permitted, got %v", f.From, f.To, err)
		}
	}
}
