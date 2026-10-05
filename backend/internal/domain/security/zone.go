package security

import (
	"errors"
	"fmt"
	"slices"
)

// Zone is a ZTAX-SEC-001 §3 security zone.
//
// The zones are the trust model's unit: a boundary across which no request is
// trusted because of where it came from. SEC-001 §3's rule for crossing one is
// that "cross-zone communication requires authenticated workload identity,
// explicit policy and encrypted transport. Shared network membership is never
// authorization." This file is the "explicit policy" part, as data, so that the
// set of permitted crossings is something a reviewer reads in one table rather
// than something reconstructed from network rules after the fact.
//
// It answers one question — may a workload in zone A call a service in zone B
// at all — and nothing finer. Which service may call which endpoint, for which
// tenant, is authorization (SEC-001 §8) and is decided per request on top of
// this. A flow permitted here is necessary, never sufficient.
type Zone string

// The zones. The comments are SEC-001 §3's boundary statements, abridged.
const (
	// ZonePublicEdge is internet-facing DNS, CDN, WAF and API ingress. It holds
	// no authoritative tenant datastore.
	ZonePublicEdge Zone = "Z0"
	// ZoneGlobalControl holds tenant metadata, routing, entitlement metadata
	// and signed-content distribution — and no unrestricted raw transactional
	// centralization.
	ZoneGlobalControl Zone = "Z1"
	// ZoneRegionalCell is a regional execution cell: authoritative tenant
	// transaction, tax, obligation, filing, evidence and operational data for
	// its residency scopes. ztax-core runs here.
	ZoneRegionalCell Zone = "Z2"
	// ZoneSecurity holds identity, policy, KMS/HSM, secrets, SIEM and security
	// administration.
	ZoneSecurity Zone = "Z3"
	// ZoneContentBuild is legal-content ingestion, rule compilation, signing
	// and pack release under four-eyes governance (ADR-0005 §2.1).
	ZoneContentBuild Zone = "Z4"
	// ZoneAI is the model gateway, RAG and evaluation, with explicit
	// data-policy mediation and no bypass of authoritative fiscal controls
	// (ADR-0006).
	ZoneAI Zone = "Z5"
	// ZoneAdminSupport is hardened workforce access, JIT elevation and audited
	// support tooling.
	ZoneAdminSupport Zone = "Z6"
	// ZonePrivateRuntime is customer VPC, on-premise or edge execution under a
	// separately defined trust and operational responsibility.
	ZonePrivateRuntime Zone = "Z7"
)

// Zones is the closed set, in order.
var Zones = []Zone{
	ZonePublicEdge, ZoneGlobalControl, ZoneRegionalCell, ZoneSecurity,
	ZoneContentBuild, ZoneAI, ZoneAdminSupport, ZonePrivateRuntime,
}

// Valid reports whether z is a known zone.
func (z Zone) Valid() bool { return slices.Contains(Zones, z) }

// Flow is one permitted direction of call: a workload in From may open a
// connection to a service in To. Flows are directed, because the risk is: a
// cell calling the model gateway is a request mediated by the gateway's data
// policy, and the model gateway calling into a cell would be an AI subsystem
// with a path to authoritative state, which SEC-001 §3 rules out for Z5.
type Flow struct {
	From, To Zone
	// Why is the reason the crossing exists. A flow nobody can justify in a
	// sentence is a flow that should not be in the table.
	Why string
}

// Flows is the cross-zone policy: every permitted crossing, and nothing else.
// A pair absent from this table is refused (SEC-REQ-0011, deny by default).
//
// SEC-001 §3 fixes the zones and the rule for crossing them; it does not
// enumerate the crossings. These are derived from each zone's boundary
// statement and from the ADRs that place components in zones, and each entry
// says which. Adding one is a lane-C review, for the same reason adding a
// purpose is a privacy review: the table is the policy.
//
// Some absences are the point and are worth naming, because each is a
// shortcut somebody will one day propose:
//
//   - Nothing calls into Z2 from Z4. Compiled content reaches a cell through
//     signed distribution in Z1 (ADR-0005 §2.1, §2.6); a build plane with a
//     route into a cell is a build plane that can change a running cell's law.
//   - Z5 calls nothing but Z3. The AI plane answers calls; it never initiates
//     one towards authoritative state (ADR-0006 §2.6).
//   - Z0 reaches no datastore-bearing zone except the cell's API, and Z6 is not
//     reachable from Z0 at all: workforce access does not arrive through the
//     public edge.
//   - Z7 does not call Z2. A customer-operated runtime is outside the SaaS
//     cell's trust boundary and fetches what it needs from Z1.
//   - Z3 initiates nothing towards a data plane. Identity, policy and keys are
//     called; SIEM ingestion is a flow *into* Z3.
var Flows = []Flow{
	{ZonePublicEdge, ZoneGlobalControl, "ingress resolves which cell serves a tenant (routing metadata)"},
	{ZonePublicEdge, ZoneRegionalCell, "ingress forwards an API request to the tenant's home cell"},
	{ZonePublicEdge, ZoneSecurity, "ingress delegates authentication and token validation to identity"},

	{ZoneGlobalControl, ZoneRegionalCell, "signed-content distribution and entitlement metadata to a cell"},
	{ZoneGlobalControl, ZoneSecurity, "control-plane workloads obtain identity, policy and keys"},

	{ZoneRegionalCell, ZoneGlobalControl, "a cell fetches signed content and publishes approved summaries only"},
	{ZoneRegionalCell, ZoneSecurity, "a cell uses KMS, secrets and policy, and ships security telemetry"},
	{ZoneRegionalCell, ZoneAI, "a cell calls the Governed Model Gateway (ADR-0006)"},

	{ZoneContentBuild, ZoneGlobalControl, "a released, signed pack is published for distribution"},
	{ZoneContentBuild, ZoneSecurity, "content signing uses HSM-held keys"},
	{ZoneContentBuild, ZoneAI, "approved AI-assisted content use cases (PURP-AI-ASSIST)"},

	{ZoneAI, ZoneSecurity, "the gateway uses identity, policy and keys, and ships security telemetry"},

	{ZoneAdminSupport, ZoneGlobalControl, "audited tenant-metadata administration"},
	{ZoneAdminSupport, ZoneRegionalCell, "JIT, case-bound support access to a cell (SEC-REQ-0021, -0022)"},
	{ZoneAdminSupport, ZoneSecurity, "workforce identity, JIT elevation and approval"},

	{ZonePrivateRuntime, ZoneGlobalControl, "a private runtime fetches signed content and entitlement"},
	{ZonePrivateRuntime, ZoneSecurity, "a private runtime attests and obtains workload identity"},
}

// Call is one attempted crossing, as the caller presents it.
type Call struct {
	From, To Zone
	// WorkloadIdentity is the caller's verified workload identity — a
	// SPIFFE-style ID taken from an mTLS peer certificate the platform has
	// already verified (SEC-001 §7). Empty means none was presented. It is a
	// verified identity, never a name the caller asserted in a header.
	WorkloadIdentity string
}

// The refusals. They are sentinels so a caller can tell them apart with
// errors.Is, and so the security log can count each kind separately.
var (
	ErrUnknownZone        = errors.New("security: unknown zone")
	ErrNoWorkloadIdentity = errors.New("security: cross-zone call without workload identity")
	ErrFlowNotPermitted   = errors.New("security: cross-zone flow not permitted")
)

// CheckCall applies the zone policy to one call. It is pure: the caller has
// already verified the workload identity, and this decides only whether that
// identity may cross this boundary at all.
//
// A call within one zone passes this check, and that is not trust — SEC-001 §1
// says network position confers none, and the per-service authorization on top
// still applies. It passes because there is no zone boundary for this function
// to police. Cross-cell traffic is a separate matter even though every cell is
// Z2: a cell is the unit of residency (ADR-0009 §2.6), and cell-to-cell data
// movement is an evidenced transfer, never a call (see privacy.CrossCellTransfer).
func CheckCall(c Call) error {
	if !c.From.Valid() || !c.To.Valid() {
		return fmt.Errorf("%w: %q -> %q", ErrUnknownZone, c.From, c.To)
	}
	if c.From == c.To {
		return nil
	}
	if c.WorkloadIdentity == "" {
		return fmt.Errorf("%w: %s -> %s", ErrNoWorkloadIdentity, c.From, c.To)
	}
	if !FlowPermitted(c.From, c.To) {
		return fmt.Errorf("%w: %s -> %s", ErrFlowNotPermitted, c.From, c.To)
	}
	return nil
}

// FlowPermitted reports whether the policy table contains from -> to.
func FlowPermitted(from, to Zone) bool {
	return slices.ContainsFunc(Flows, func(f Flow) bool { return f.From == from && f.To == to })
}
