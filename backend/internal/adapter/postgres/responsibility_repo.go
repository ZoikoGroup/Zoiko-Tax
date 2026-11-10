package postgres

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/obligation"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// levy_responsibility — migration 000027

const (
	sqlResponsibilityColumns = `responsibility_decision_id, decision_id, item_key, component_id, component_version,
		tax_type, jurisdiction, authority, treatment, legal_entity_id, rule_id, rule_version, bundle_id, bundle_digest,
		effective_from, status, assignments, recorded_at`

	sqlResponsibilityInsert = `
		INSERT INTO ztax.levy_responsibility (tenant_id, ` + sqlResponsibilityColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19)`

	sqlResponsibilityForDecision = `
		SELECT ` + sqlResponsibilityColumns + `
		FROM ztax.levy_responsibility
		WHERE tenant_id = $1 AND decision_id = $2
		ORDER BY item_key, component_id`
)

// ResponsibilityRepo implements port.ResponsibilityRepository.
type ResponsibilityRepo struct{ s *Store }

// Responsibilities returns the levy-responsibility repository.
func (s *Store) Responsibilities() *ResponsibilityRepo { return &ResponsibilityRepo{s: s} }

var _ port.ResponsibilityRepository = (*ResponsibilityRepo)(nil)

// partyRow and assignmentRow are the assignments column's JSON shape.
type partyRow struct {
	Kind        string `json:"kind"`
	LegalEntity string `json:"legalEntity,omitempty"`
	Ref         string `json:"ref,omitempty"`
}

type assignmentRow struct {
	Role      string    `json:"role"`
	Status    string    `json:"status"`
	Statutory *partyRow `json:"statutory,omitempty"`
	Agent     *partyRow `json:"agent,omitempty"`
	Detail    string    `json:"detail,omitempty"`
}

func toPartyRow(p *obligation.Party) *partyRow {
	if p == nil {
		return nil
	}
	out := &partyRow{Kind: string(p.Kind), Ref: p.Ref}
	if !p.LegalEntity.IsZero() {
		out.LegalEntity = p.LegalEntity.String()
	}
	return out
}

func fromPartyRow(p *partyRow) (*obligation.Party, error) {
	if p == nil {
		return nil, nil
	}
	out := &obligation.Party{Kind: obligation.PartyKind(p.Kind), Ref: p.Ref}
	if p.LegalEntity != "" {
		le, err := id.ParseLegalEntityID(p.LegalEntity)
		if err != nil {
			return nil, err
		}
		out.LegalEntity = le
	}
	return out, nil
}

// Append records one levy's decision.
func (r *ResponsibilityRepo) Append(ctx context.Context, lr obligation.LevyResponsibility) error {
	d := lr.Decision
	rows := make([]assignmentRow, len(d.Assignments))
	for i, a := range d.Assignments {
		rows[i] = assignmentRow{
			Role: string(a.Role), Status: string(a.Status),
			Statutory: toPartyRow(a.Statutory), Agent: toPartyRow(a.Agent), Detail: a.Detail,
		}
	}
	assignments, err := json.Marshal(rows)
	if err != nil {
		return errs.Wrap(err, errs.CategoryInternal, errs.ReasonInternal, "The responsibility assignments could not be encoded.")
	}
	ctx = tenantScope(ctx, d.TenantID)
	_, err = r.s.db(ctx).Exec(ctx, sqlResponsibilityInsert,
		d.TenantID.UUID(), d.ID.UUID(), lr.TaxDecision.UUID(), lr.Item, lr.Component, lr.ComponentVersion,
		lr.TaxType, lr.Jurisdiction, lr.Authority, lr.Treatment, d.LegalEntity.UUID(), d.RuleID, d.RuleVersion,
		d.Provenance.BundleID, d.Provenance.BundleDigest, d.EffectiveFrom, string(d.Status), assignments, lr.RecordedAt)
	return mapError(err, "insert levy responsibility")
}

// ForDecision lists a tax decision's levies.
func (r *ResponsibilityRepo) ForDecision(ctx context.Context, decision id.DecisionID) ([]obligation.LevyResponsibility, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := r.s.db(ctx).Query(ctx, sqlResponsibilityForDecision, tenant.UUID(), decision.UUID())
	if err != nil {
		return nil, mapError(err, "list levy responsibility")
	}
	defer rows.Close()
	out := []obligation.LevyResponsibility{}
	for rows.Next() {
		var (
			lr                  obligation.LevyResponsibility
			rid, did, le        uuid.UUID
			status              string
			assignments         []byte
			effective, recorded time.Time
		)
		if err := rows.Scan(&rid, &did, &lr.Item, &lr.Component, &lr.ComponentVersion,
			&lr.TaxType, &lr.Jurisdiction, &lr.Authority, &lr.Treatment, &le,
			&lr.Decision.RuleID, &lr.Decision.RuleVersion, &lr.Decision.Provenance.BundleID, &lr.Decision.Provenance.BundleDigest,
			&effective, &status, &assignments, &recorded); err != nil {
			return nil, mapError(err, "read levy responsibility")
		}
		var as []assignmentRow
		if err := json.Unmarshal(assignments, &as); err != nil {
			return nil, errs.Wrap(err, errs.CategoryInternal, errs.ReasonInternal, "A recorded responsibility cannot be read.")
		}
		lr.Decision.ID = id.NewResponsibilityDecisionID(rid)
		lr.Decision.TenantID = tenant
		lr.Decision.LegalEntity = id.NewLegalEntityID(le)
		lr.Decision.EffectiveFrom = effective.UTC()
		lr.Decision.Status = obligation.ResponsibilityStatus(status)
		lr.TaxDecision = id.NewDecisionID(did)
		lr.RecordedAt = recorded.UTC()
		for _, a := range as {
			statutory, err := fromPartyRow(a.Statutory)
			if err != nil {
				return nil, errs.Wrap(err, errs.CategoryInternal, errs.ReasonInternal, "A recorded responsibility names an unreadable party.")
			}
			agent, err := fromPartyRow(a.Agent)
			if err != nil {
				return nil, errs.Wrap(err, errs.CategoryInternal, errs.ReasonInternal, "A recorded responsibility names an unreadable agent.")
			}
			lr.Decision.Assignments = append(lr.Decision.Assignments, obligation.Assignment{
				Role: obligation.Role(a.Role), Status: obligation.ResponsibilityStatus(a.Status),
				Statutory: statutory, Agent: agent, Detail: a.Detail,
			})
		}
		out = append(out, lr)
	}
	return out, mapError(rows.Err(), "list levy responsibility")
}
