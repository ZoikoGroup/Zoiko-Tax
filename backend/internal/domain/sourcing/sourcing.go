// Package sourcing is ZTAX-SRC-001 as types: what a source is, what rights the
// estate holds in it, and where in its onboarding life it stands.
//
// The constitutional rule it encodes is SRC-001's first sentence: ZoikoTax may
// use a source only for rights that have been affirmatively established.
// UNKNOWN, SILENT or DISPUTED rights resolve to DENY for the affected use. Every
// evaluation in this package is therefore written from the denial side — a
// right that is not stated is UNKNOWN, an UNKNOWN right is a refusal, and a
// CONDITIONAL right is a refusal unless every one of its conditions can be
// checked by a machine and is met (ZTAX-SRC-REQ-0005, ZTAX-SRC-REQ-0103).
//
// Why a domain package, and why separate from internal/domain/content:
//
//   - It is pure. Rights evaluation is a function of a record, an intended use
//     and an instant, and the instant is a parameter (ADR-0003 §2.5) rather than
//     a clock read, so a build's rights decision can be re-run later and give
//     the same answer — which is what ZTAX-SRC-REQ-0084 asks release evidence
//     to preserve.
//   - It has more than one consumer. The content build plane (ztax-contentc)
//     evaluates it before signing (ZTAX-SRC-REQ-0050); the runtime licence
//     guard, the export service and the AI corpus builder of SRC-001 §16 will
//     evaluate the same profile for their own uses. A copy of the taxonomy in
//     each would be four taxonomies within a year.
//   - Authority and licence are separate questions (SRC-001 §7,
//     ZTAX-SRC-REQ-0006). internal/domain/content answers "is this source
//     authoritative enough to ground a rule"; this package answers "may we use
//     it for this". Keeping them in separate packages keeps either from
//     quietly answering the other's question.
//
// Nothing here knows about files. The on-disk register a pack build reads is
// decoded by internal/content/pack, on the Z4 content plane, into these types.
package sourcing

import (
	"fmt"
	"sort"
)

// SourceID is the canonical Zoiko identity of a source (SRC-001 §5 source_id).
// It is immutable: a new version of a dataset is a new SourceVersion on the
// same SourceID, never a new identity, so a pack's dependency on it survives
// the dataset's own release cadence.
type SourceID string

// SourceClass is SRC-001 §3's classification. It is a rights classification,
// not an authority one — internal/domain/content.AuthorityClass is the other
// axis, and ZTAX-SRC-REQ-0006 requires the two to be evaluated independently.
type SourceClass string

// The source classes (ZTAX-SRC-REQ-0003: every source is assigned one).
const (
	ClassPrimaryAuthority       SourceClass = "S0"
	ClassOfficialAdministrative SourceClass = "S1"
	ClassLicensedSpecialist     SourceClass = "S2"
	ClassOpenPublicData         SourceClass = "S3"
	ClassInstitutionalSecondary SourceClass = "S4"
	ClassDiscoveryWeb           SourceClass = "S5"
	ClassCustomerProvided       SourceClass = "S6"
	ClassZoikoAuthored          SourceClass = "S7"
)

// Valid reports whether c is one of the eight classes.
func (c SourceClass) Valid() bool {
	switch c {
	case ClassPrimaryAuthority, ClassOfficialAdministrative, ClassLicensedSpecialist, ClassOpenPublicData,
		ClassInstitutionalSecondary, ClassDiscoveryWeb, ClassCustomerProvided, ClassZoikoAuthored:
		return true
	}
	return false
}

// MayGroundProduction reports whether a source of this class may feed a
// production pack at all.
//
// S5 may not: discovery-web material "cannot become production legal content
// without authority and rights promotion" (SRC-001 §12, ZTAX-SRC-REQ-0037), and
// promotion is a reclassification of the record, not a flag on the pack. Every
// other class may, subject to its RightsProfile.
func (c SourceClass) MayGroundProduction() bool { return c.Valid() && c != ClassDiscoveryWeb }

// AcquisitionMethod is how the source is obtained (SRC-001 §5). It matters to
// the ingestion gateway, which blocks methods the record does not name
// (ZTAX-SRC-REQ-0036); the build gate records it and does not otherwise read it.
type AcquisitionMethod string

// The acquisition methods SRC-001 §5 enumerates.
const (
	AcquisitionManual       AcquisitionMethod = "MANUAL"
	AcquisitionAPI          AcquisitionMethod = "API"
	AcquisitionBulk         AcquisitionMethod = "BULK"
	AcquisitionFeed         AcquisitionMethod = "FEED"
	AcquisitionPartner      AcquisitionMethod = "PARTNER"
	AcquisitionCustomer     AcquisitionMethod = "CUSTOMER"
	AcquisitionOpenDownload AcquisitionMethod = "OPEN_DOWNLOAD"
)

// Valid reports whether m is a known method.
func (m AcquisitionMethod) Valid() bool {
	switch m {
	case AcquisitionManual, AcquisitionAPI, AcquisitionBulk, AcquisitionFeed,
		AcquisitionPartner, AcquisitionCustomer, AcquisitionOpenDownload:
		return true
	}
	return false
}

// DeploymentMode is SRC-001 §15's deployment class: where a compiled bundle is
// going to run, and therefore who ends up holding it.
//
// It is the axis on which hosted use is distinguished from private, on-premise
// and edge redistribution (ZTAX-SRC-REQ-0008). The wire value keeps the spec's
// P-code as a prefix so a reviewer can map it to §15 without a table, and
// spells out the meaning so a pack declaration reads without one either.
type DeploymentMode string

// The deployment classes.
const (
	// DeployZoikoOnly (P0): every raw and compiled licensed artefact stays in
	// Zoiko SaaS; the customer receives decisions only.
	DeployZoikoOnly DeploymentMode = "P0_ZOIKO_ONLY"
	// DeployCustomerVPC (P1): the bundle executes in the customer's cloud under
	// Zoiko control. The licence must permit the location.
	DeployCustomerVPC DeploymentMode = "P1_CUSTOMER_VPC"
	// DeployPrivateRuntime (P2): the customer possesses the executable bundle.
	DeployPrivateRuntime DeploymentMode = "P2_PRIVATE_RUNTIME"
	// DeployDisconnectedEdge (P3): the customer can retain and run the bundle
	// offline.
	DeployDisconnectedEdge DeploymentMode = "P3_DISCONNECTED_EDGE"
	// DeployCustomerOwned (P4): the customer supplied the licensed dataset.
	DeployCustomerOwned DeploymentMode = "P4_CUSTOMER_OWNED"
)

// Valid reports whether d is one of the five classes.
func (d DeploymentMode) Valid() bool {
	switch d {
	case DeployZoikoOnly, DeployCustomerVPC, DeployPrivateRuntime, DeployDisconnectedEdge, DeployCustomerOwned:
		return true
	}
	return false
}

// baseBuildRights are the rights every compiled bundle needs from every source
// it was derived from, whatever the deployment.
//
// A rule bundle is a transformation of its sources (transform) into executable
// behaviour whose output is a derived result (derived_output), used internally
// to author and certify (internal_use), sold (commercial_use), and retained so
// that a decision made under it can be reproduced for the statutory horizon
// (historical_replay — ZTAX-SRC-REQ-0027 requires replay rights to be resolved
// before a source becomes critical to authoritative decisions, and a bundle in
// a cell is exactly that).
var baseBuildRights = []Right{
	RightInternalUse, RightTransform, RightDerivedOutput, RightCommercialUse, RightHistoricalReplay,
}

// BuildRights is the set of rights a bundle built for mode needs from each of
// its sources, sorted.
//
// P2 and P3 add the redistribution rights SRC-001 §15 names: "build pipeline
// must refuse P2/P3 artifacts if any required source has private_bundle /
// edge_bundle != ALLOW / CONDITIONAL-satisfied" (ZTAX-SRC-REQ-0051). P1 adds no
// right, because what P1 needs is permission for the *location*, and that is
// the record's environment list rather than a right in the profile. A mode
// this build does not know returns nil, and a caller treats nil as "nothing is
// permitted", never as "nothing is required".
func BuildRights(mode DeploymentMode) []Right {
	if !mode.Valid() {
		return nil
	}
	out := append([]Right(nil), baseBuildRights...)
	switch mode {
	case DeployPrivateRuntime:
		out = append(out, RightPrivateBundle)
	case DeployDisconnectedEdge:
		out = append(out, RightEdgeBundle)
	}
	sortRights(out)
	return out
}

func sortRights(rs []Right) { sort.Slice(rs, func(i, j int) bool { return rs[i] < rs[j] }) }

// errorf keeps every error from this package prefixed the same way.
func errorf(format string, args ...any) error { return fmt.Errorf("sourcing: "+format, args...) }
