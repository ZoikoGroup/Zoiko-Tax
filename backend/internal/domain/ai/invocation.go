package ai

import (
	"slices"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/privacy"
)

// Invocation is what the app layer asks the Model Gateway for
// (port.ModelGateway).
//
// Two parts of the governance context are deliberately absent. The tenant is
// taken from the request's security context by the adapter (ADR-0012 §2.7),
// so a caller cannot name a tenant it is not acting for. The region is the
// regional cell the adapter is deployed in (ADR-0006 §2.8), so a caller cannot
// claim a region to reach a model its own cell may not use. What remains is
// what only the caller knows: which registered capability, at what authority
// and risk, over which classes of data.
type Invocation struct {
	UseCase          AiUseCase
	AuthorityOutcome AuthorityOutcome
	RiskTier         RiskTier
	// DataClasses is every PRIV-001 class present in Input. The gate refuses an
	// invocation that declares none.
	DataClasses []privacy.Class

	// SubjectRef names what the call is about, in the caller's own terms. It is
	// copied onto the advisory record that comes back.
	SubjectRef string
	// Input is the use case's input in ADR-0011 canonical form. The policy gate
	// never reads it: governance is decided on the context, not the content.
	Input []byte
}

// Governance assembles the gate's input from the invocation, the tenant from
// the security context and the cell's region.
func (inv Invocation) Governance(tenant id.TenantID, region string) GovernanceContext {
	return GovernanceContext{
		Tenant:           tenant,
		UseCase:          inv.UseCase,
		AuthorityOutcome: inv.AuthorityOutcome,
		RiskTier:         inv.RiskTier,
		Region:           region,
		DataClasses:      slices.Clone(inv.DataClasses),
	}
}
