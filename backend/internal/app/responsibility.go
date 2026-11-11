package app

import (
	"context"
	"fmt"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/evidence"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/identity"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/obligation"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/rule"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/taxation"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// Statutory responsibility per levy (ZTAX-OBL-001).
//
// Every levy a committed document decision assesses gets a five-role
// decision of its own: who the law places the levy on, who collects it, who
// reports it, who remits it and who bears its cost. The content declares the
// party kind for each role; the decision resolves those kinds against the
// parties the document actually names, through the same resolver obligations
// use, so a role whose party is absent or contested is recorded as AMBIGUOUS
// or CONFLICTED rather than defaulting to the seller or the customer.
//
// It is written beside the tax decision, in the commit's transaction, and
// never inside the sealed result: the result's bytes are what replay
// compares, and a decision committed before this existed must still replay.

// WithResponsibility returns the service recording levy responsibility.
// Without it a commit records none, and reading it refuses.
func (s *DeterminationService) WithResponsibility(r port.ResponsibilityRepository) *DeterminationService {
	s.responsibility = r
	return s
}

// decideResponsibility records the five-role decision for every levy of a
// committed document decision. A decision with no document, or no levies,
// records nothing.
func (s *DeterminationService) decideResponsibility(ctx context.Context, b *rule.Bundle, d evidence.Decision) error {
	if s.responsibility == nil || s.fiscal == nil {
		return nil
	}
	doc, res := d.Envelope.Document, d.Result.Document
	if doc == nil || res == nil {
		return nil
	}
	var le identity.LegalEntity
	resolvedEntity := false
	for _, item := range res.Items {
		for _, c := range item.Components {
			if !resolvedEntity {
				// Read only when there is a levy to decide for, so a document
				// that resolved nothing does not need a legal entity.
				var err error
				if le, err = s.defaultLegalEntity(ctx); err != nil {
					return err
				}
				resolvedEntity = true
			}
			r, err := s.levyResponsibility(d, b, le, *doc, item, c)
			if err != nil {
				return err
			}
			if err := s.responsibility.Append(ctx, r); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *DeterminationService) levyResponsibility(d evidence.Decision, b *rule.Bundle, le identity.LegalEntity,
	doc taxation.Document, item taxation.ItemResult, c taxation.ComponentResult) (obligation.LevyResponsibility, error) {
	rid, err := idgen.ResponsibilityDecisionID(s.ids)
	if err != nil {
		return obligation.LevyResponsibility{}, internal(err, "The levy's responsibility could not be recorded.")
	}
	rr, err := responsibilityRule(c, b)
	if err != nil {
		return obligation.LevyResponsibility{}, err
	}
	decision, err := obligation.DecideResponsibility(rr, obligation.ResponsibilityInput{
		ID: rid, TenantID: d.TenantID, LegalEntity: le.ID,
		EffectiveFrom: d.Envelope.EventTime,
		Parties:       documentParties(le, doc),
	})
	if err != nil {
		return obligation.LevyResponsibility{}, internal(err, fmt.Sprintf("The responsibility of %s could not be decided.", c.ID))
	}
	return obligation.LevyResponsibility{
		Decision: decision, TaxDecision: d.ID, Item: item.Key,
		Component: c.ID, ComponentVersion: c.Version, TaxType: c.TaxType,
		Jurisdiction: c.Jurisdiction, Authority: c.Authority, Treatment: c.Treatment,
		RecordedAt: d.Envelope.DecisionTime,
	}, nil
}

// responsibilityRule reads a levy's declared assignment as a resolver rule.
// SELLER and BUYER are the one candidate kind for their role; NONE is a role
// the law gives to nobody.
func responsibilityRule(c taxation.ComponentResult, b *rule.Bundle) (obligation.ResponsibilityRule, error) {
	r := c.Responsibility
	if r.ID == "" {
		return obligation.ResponsibilityRule{}, errs.New(errs.CategoryInternal, errs.ReasonInternal,
			fmt.Sprintf("Levy %s was assessed with no responsibility assignment.", c.ID))
	}
	out := obligation.ResponsibilityRule{
		// One rule per assignment and levy version: the same assignment
		// named by a later version of the levy is a different rule.
		ID: r.ID, Version: c.ID + "@" + c.Version,
		Provenance: obligation.ContentRef{BundleID: b.ID(), BundleDigest: b.Digest()},
	}
	for _, role := range []struct {
		role  obligation.Role
		party string
	}{
		{obligation.RoleIncidence, r.Incidence},
		{obligation.RoleCollection, r.Collection},
		{obligation.RoleReporting, r.Reporting},
		{obligation.RoleRemittance, r.Remittance},
		{obligation.RoleEconomicBearer, r.EconomicBearer},
	} {
		rr := obligation.RoleRule{Role: role.role}
		kinds, err := taxation.RoleCandidates(role.party)
		if err != nil {
			// The profile refuses this when it loads, so this is a result
			// that did not come from loaded content.
			return obligation.ResponsibilityRule{}, errs.New(errs.CategoryInternal, errs.ReasonInternal,
				fmt.Sprintf("Levy %s assigns %s to %q.", c.ID, role.role, role.party))
		}
		if kinds == nil {
			rr.NoParty = true
		}
		for _, k := range kinds {
			rr.Candidates = append(rr.Candidates, partyKind(k))
		}
		out.Roles = append(out.Roles, rr)
	}
	return out, nil
}

// partyKind maps a content party kind to the resolver's. The two vocabularies
// are spelled alike; the map keeps them from drifting silently.
func partyKind(k string) obligation.PartyKind {
	switch k {
	case taxation.PartySeller:
		return obligation.PartySeller
	case taxation.PartyBuyer:
		return obligation.PartyBuyer
	case taxation.PartyMarketplace:
		return obligation.PartyMarketplace
	case taxation.PartyWholesaler:
		return obligation.PartyWholesaler
	case taxation.PartyReseller:
		return obligation.PartyReseller
	case taxation.PartyEndUser:
		return obligation.PartyEndUser
	}
	// RoleCandidates and Document.Validate admit nothing else.
	return obligation.PartyKind(k)
}

// documentParties are the parties a document names, as facts. The seller is
// the tenant's legal entity issuing it; the customer is whoever the document
// is addressed to; each intermediary is named by the caller's own reference.
func documentParties(le identity.LegalEntity, doc taxation.Document) []obligation.Party {
	out := []obligation.Party{
		{Kind: obligation.PartySeller, LegalEntity: le.ID, Ref: doc.Seller.ID},
		{Kind: obligation.PartyBuyer, Ref: doc.Customer.ID},
	}
	for _, p := range doc.Intermediaries {
		out = append(out, obligation.Party{Kind: partyKind(p.Kind), Ref: p.ID})
	}
	return out
}

// Responsibility reads the levy responsibility recorded with a decision.
func (s *DeterminationService) Responsibility(ctx context.Context, decisionID id.DecisionID) ([]obligation.LevyResponsibility, error) {
	if _, err := requireRoleOrSystem(ctx, security.RoleOperator, security.RoleAnalyst, security.RoleAuditor); err != nil {
		return nil, err
	}
	if s.responsibility == nil {
		return nil, errs.New(errs.CategoryUnavailable, errs.ReasonUnavailable,
			"This cell does not record levy responsibility.")
	}
	// The decision is read first so an unknown or foreign decision answers
	// 404 rather than an empty list that looks like "no levies".
	if _, err := s.decisions.ByID(ctx, decisionID); err != nil {
		return nil, err
	}
	return s.responsibility.ForDecision(ctx, decisionID)
}
