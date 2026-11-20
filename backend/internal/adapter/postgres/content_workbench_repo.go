package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/classification"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/content"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/crosswalk"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/rulechange"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/sourcing"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// ---------------------------------------------------------------------------
// content_crosswalk(_version, _entry, _event), content_rule_change(_event) —
// migration 000037
// ---------------------------------------------------------------------------

const (
	sqlCrosswalkInsert = `INSERT INTO ztax.content_crosswalk (tenant_id, crosswalk_id, vendor, namespace, vendor_release,
		title, created_at, created_by) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`
	sqlCrosswalkSelect = `SELECT crosswalk_id, vendor, namespace, vendor_release, title, created_at, created_by
		FROM ztax.content_crosswalk WHERE tenant_id = $1`

	sqlCrosswalkVersionInsert = `INSERT INTO ztax.content_crosswalk_version (tenant_id, crosswalk_id, version, rights_basis,
		rights_reference, rights_expires_on, rights_evidence_sha256, effective_from, effective_to, content_digest, intake_key,
		intake_hash, created_at, created_by) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`
	sqlCrosswalkVersions = `SELECT crosswalk_id, version, rights_basis, rights_reference, rights_expires_on,
		rights_evidence_sha256, effective_from, effective_to, content_digest, intake_key, intake_hash, created_at, created_by
		FROM ztax.content_crosswalk_version WHERE tenant_id = $1 AND crosswalk_id = ANY($2) ORDER BY crosswalk_id, version`

	sqlCrosswalkEntryInsert = `INSERT INTO ztax.content_crosswalk_entry (tenant_id, crosswalk_id, version, ordinal,
		external_code, description, match_quality, service_target, levy_hint, predicate, note)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`
	sqlCrosswalkEntries = `SELECT crosswalk_id, version, external_code, description, match_quality, service_target,
		levy_hint, predicate, note FROM ztax.content_crosswalk_entry
		WHERE tenant_id = $1 AND crosswalk_id = ANY($2) ORDER BY crosswalk_id, version, ordinal`

	sqlCrosswalkEventInsert = `INSERT INTO ztax.content_crosswalk_event (tenant_id, crosswalk_id, version, seq, kind,
		status_after, reason, recorded_at, recorded_by) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`
	sqlCrosswalkEvents = `SELECT crosswalk_id, version, seq, kind, status_after, reason, recorded_at, recorded_by
		FROM ztax.content_crosswalk_event WHERE tenant_id = $1 AND crosswalk_id = ANY($2)
		ORDER BY crosswalk_id, version, seq`

	sqlRuleChangeColumns = `change_id, pack_id, base_version, rule_id, jurisdiction, change_type, summary, detail, impact,
		source_id, source_reference, source_edition, authority_class, published_on, effective_from, effective_to,
		origin_ref, supersedes, intake_key, intake_hash, created_at, created_by`
	sqlRuleChangeInsert = `INSERT INTO ztax.content_rule_change (tenant_id, ` + sqlRuleChangeColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23)`
	sqlRuleChangeSelect = `SELECT ` + sqlRuleChangeColumns + ` FROM ztax.content_rule_change WHERE tenant_id = $1`

	sqlRuleChangeEventInsert = `INSERT INTO ztax.content_rule_change_event (tenant_id, change_id, seq, kind, status_after,
		reason, test_evidence_ref, test_evidence_sha256, recorded_at, recorded_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`
	sqlRuleChangeEvents = `SELECT change_id, seq, kind, status_after, reason, test_evidence_ref, test_evidence_sha256,
		recorded_at, recorded_by FROM ztax.content_rule_change_event WHERE tenant_id = $1 AND change_id = ANY($2)
		ORDER BY change_id, seq`

	sqlContentLock = `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`

	contentListCap = 500
)

// The predicates a read takes. Fixed strings from this file, never caller
// text.
const (
	whereCrosswalkID       = ` AND crosswalk_id = $2`
	whereCrosswalkIdentity = ` AND vendor = $2 AND namespace = $3 AND vendor_release = $4`
	whereCrosswalkIntake   = ` AND crosswalk_id IN (SELECT crosswalk_id FROM ztax.content_crosswalk_version
		WHERE tenant_id = $1 AND intake_key = $2)`
	whereCrosswalkRecent  = ` ORDER BY created_at DESC, crosswalk_id DESC LIMIT $2`
	whereRuleChangeID     = ` AND change_id = $2`
	whereRuleChangeIntake = ` AND intake_key = $2`
	whereRuleChangeRecent = ` ORDER BY created_at DESC, change_id DESC LIMIT $2`
)

// CrosswalkRepo implements port.CrosswalkRepository.
type CrosswalkRepo struct{ s *Store }

// Crosswalks returns the vendor crosswalk repository.
func (s *Store) Crosswalks() *CrosswalkRepo { return &CrosswalkRepo{s: s} }

var _ port.CrosswalkRepository = (*CrosswalkRepo)(nil)

// RuleChangeRepo implements port.RuleChangeRepository.
type RuleChangeRepo struct{ s *Store }

// RuleChanges returns the rule approval queue repository.
func (s *Store) RuleChanges() *RuleChangeRepo { return &RuleChangeRepo{s: s} }

var _ port.RuleChangeRepository = (*RuleChangeRepo)(nil)

func contentLock(ctx context.Context, s *Store, scope, key string) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	_, err = s.db(ctx).Exec(ctx, sqlContentLock, tenant.String()+"/"+scope+"/"+key)
	return mapError(err, "lock "+scope)
}

func contentOptUUID(u uuid.UUID, zero bool) *uuid.UUID {
	if zero {
		return nil
	}
	return &u
}

// Lock takes one of the tenant's crosswalk locks for the transaction.
func (r *CrosswalkRepo) Lock(ctx context.Context, key string) error {
	return contentLock(ctx, r.s, "crosswalk", key)
}

// CreateCrosswalk writes a crosswalk's identity.
func (r *CrosswalkRepo) CreateCrosswalk(ctx context.Context, c crosswalk.Crosswalk) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if c.TenantID != tenant {
		return errs.New(errs.CategoryPolicy, errs.ReasonTenantMismatch, "The crosswalk belongs to a tenant outside the caller's scope.")
	}
	if err := c.Validate(); err != nil {
		return obligationFault(err, "The crosswalk is not well-formed.")
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlCrosswalkInsert, tenant.UUID(), c.ID.UUID(), c.Vendor, c.Namespace, c.VendorRelease,
		c.Title, c.CreatedAt.UTC(), c.CreatedBy.UUID())
	return mapError(err, "insert crosswalk")
}

// CreateVersion writes a version, its entries and its first events.
func (r *CrosswalkRepo) CreateVersion(ctx context.Context, crosswalkID id.CrosswalkID, v crosswalk.Version) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if err := v.Validate(); err != nil {
		return obligationFault(err, "The crosswalk version is not well-formed.")
	}
	if v.Digest.IsZero() || v.IntakeHash.IsZero() || len(v.History) == 0 || v.History[0].Kind != crosswalk.EventCreated {
		return obligationFault(nil, "A crosswalk version is created by its CREATED event, with its digests.")
	}
	db := r.s.db(ctx)
	if _, err := db.Exec(ctx, sqlCrosswalkVersionInsert, tenant.UUID(), crosswalkID.UUID(), v.Number, string(v.Rights.Basis),
		v.Rights.Reference, optDate(v.Rights.ExpiresOn), optString(v.Rights.EvidenceSHA256), civilDate(v.EffectiveFrom),
		optDate(v.EffectiveTo), v.Digest.String(), v.IntakeKey, v.IntakeHash.String(), v.CreatedAt.UTC(),
		v.CreatedBy.UUID()); err != nil {
		return mapError(err, "insert crosswalk version")
	}
	for i, e := range v.Entries {
		if _, err := db.Exec(ctx, sqlCrosswalkEntryInsert, tenant.UUID(), crosswalkID.UUID(), v.Number, i+1, e.ExternalCode,
			e.Description, string(e.Match), optString(string(e.ServiceTarget)), optString(e.LevyHint), optString(e.Predicate),
			optString(e.Note)); err != nil {
			return mapError(err, "insert crosswalk entry")
		}
	}
	for _, e := range v.History {
		if err := r.AppendEvent(ctx, crosswalkID, v.Number, e); err != nil {
			return err
		}
	}
	return nil
}

// AppendEvent writes one version event.
func (r *CrosswalkRepo) AppendEvent(ctx context.Context, crosswalkID id.CrosswalkID, version int, e crosswalk.Event) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlCrosswalkEventInsert, tenant.UUID(), crosswalkID.UUID(), version, e.Seq, string(e.Kind),
		string(e.Status), optString(e.Reason), e.RecordedAt.UTC(), e.RecordedBy.UUID())
	return mapError(err, "insert crosswalk event")
}

// Crosswalk returns one crosswalk.
func (r *CrosswalkRepo) Crosswalk(ctx context.Context, crosswalkID id.CrosswalkID) (crosswalk.Crosswalk, error) {
	got, err := r.read(ctx, whereCrosswalkID, crosswalkID.UUID())
	if err != nil {
		return crosswalk.Crosswalk{}, err
	}
	if len(got) == 0 {
		return crosswalk.Crosswalk{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "No such crosswalk for this tenant.")
	}
	return got[0], nil
}

// CrosswalkByIdentity returns the crosswalk of a vendor namespace and release.
func (r *CrosswalkRepo) CrosswalkByIdentity(ctx context.Context, vendor, namespace, release string) (crosswalk.Crosswalk, bool, error) {
	got, err := r.read(ctx, whereCrosswalkIdentity, vendor, namespace, release)
	if err != nil || len(got) == 0 {
		return crosswalk.Crosswalk{}, false, err
	}
	return got[0], true, nil
}

// CrosswalkByIntakeKey returns the crosswalk a version was written into
// under key.
func (r *CrosswalkRepo) CrosswalkByIntakeKey(ctx context.Context, key string) (crosswalk.Crosswalk, bool, error) {
	got, err := r.read(ctx, whereCrosswalkIntake, key)
	if err != nil || len(got) == 0 {
		return crosswalk.Crosswalk{}, false, err
	}
	return got[0], true, nil
}

// Crosswalks returns the most recently created crosswalks.
func (r *CrosswalkRepo) Crosswalks(ctx context.Context, limit int) ([]crosswalk.Crosswalk, error) {
	if limit <= 0 || limit > contentListCap {
		limit = contentListCap
	}
	return r.read(ctx, whereCrosswalkRecent, limit)
}

type versionKey struct {
	c id.CrosswalkID
	n int
}

func (r *CrosswalkRepo) read(ctx context.Context, where string, args ...any) ([]crosswalk.Crosswalk, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	db := r.s.db(ctx)
	rows, err := db.Query(ctx, sqlCrosswalkSelect+where, append([]any{tenant.UUID()}, args...)...)
	if err != nil {
		return nil, mapError(err, "read crosswalks")
	}
	var out []crosswalk.Crosswalk
	for rows.Next() {
		var (
			c       = crosswalk.Crosswalk{TenantID: tenant}
			cid, by uuid.UUID
		)
		if err := rows.Scan(&cid, &c.Vendor, &c.Namespace, &c.VendorRelease, &c.Title, &c.CreatedAt, &by); err != nil {
			rows.Close()
			return nil, mapError(err, "scan crosswalk")
		}
		c.ID, c.CreatedBy, c.CreatedAt = id.NewCrosswalkID(cid), id.NewUserID(by), c.CreatedAt.UTC()
		out = append(out, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, mapError(err, "read crosswalks")
	}
	if len(out) == 0 {
		return nil, nil
	}
	ids := make([]uuid.UUID, len(out))
	for i, c := range out {
		ids[i] = c.ID.UUID()
	}

	versions := map[id.CrosswalkID][]crosswalk.Version{}
	rows, err = db.Query(ctx, sqlCrosswalkVersions, tenant.UUID(), ids)
	if err != nil {
		return nil, mapError(err, "read crosswalk versions")
	}
	for rows.Next() {
		var (
			v                    crosswalk.Version
			cid, by              uuid.UUID
			basis, digest, hash  string
			evidence             *string
			expires, from, until pgtype.Date
		)
		if err := rows.Scan(&cid, &v.Number, &basis, &v.Rights.Reference, &expires, &evidence, &from, &until, &digest,
			&v.IntakeKey, &hash, &v.CreatedAt, &by); err != nil {
			rows.Close()
			return nil, mapError(err, "scan crosswalk version")
		}
		v.Rights.Basis, v.Rights.EvidenceSHA256 = crosswalk.RightsBasis(basis), deref(evidence)
		if expires.Valid {
			v.Rights.ExpiresOn = inLocation(expires, time.UTC)
		}
		v.EffectiveFrom = inLocation(from, time.UTC)
		if until.Valid {
			v.EffectiveTo = inLocation(until, time.UTC)
		}
		v.CreatedAt, v.CreatedBy = v.CreatedAt.UTC(), id.NewUserID(by)
		if v.Digest, err = canonical.ParseDigest(digest); err == nil {
			v.IntakeHash, err = canonical.ParseDigest(hash)
		}
		if err != nil {
			rows.Close()
			return nil, obligationFault(err, "A stored crosswalk version's digest does not parse.")
		}
		k := id.NewCrosswalkID(cid)
		versions[k] = append(versions[k], v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, mapError(err, "read crosswalk versions")
	}

	entries := map[versionKey][]crosswalk.Entry{}
	rows, err = db.Query(ctx, sqlCrosswalkEntries, tenant.UUID(), ids)
	if err != nil {
		return nil, mapError(err, "read crosswalk entries")
	}
	for rows.Next() {
		var (
			e                        crosswalk.Entry
			cid                      uuid.UUID
			n                        int
			match                    string
			target, levy, pred, note *string
		)
		if err := rows.Scan(&cid, &n, &e.ExternalCode, &e.Description, &match, &target, &levy, &pred, &note); err != nil {
			rows.Close()
			return nil, mapError(err, "scan crosswalk entry")
		}
		e.Match, e.ServiceTarget = crosswalk.Match(match), classification.OntologyID(deref(target))
		e.LevyHint, e.Predicate, e.Note = deref(levy), deref(pred), deref(note)
		k := versionKey{id.NewCrosswalkID(cid), n}
		entries[k] = append(entries[k], e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, mapError(err, "read crosswalk entries")
	}

	events := map[versionKey][]crosswalk.Event{}
	rows, err = db.Query(ctx, sqlCrosswalkEvents, tenant.UUID(), ids)
	if err != nil {
		return nil, mapError(err, "read crosswalk events")
	}
	defer rows.Close()
	for rows.Next() {
		var (
			e            crosswalk.Event
			cid, by      uuid.UUID
			n            int
			kind, status string
			reason       *string
		)
		if err := rows.Scan(&cid, &n, &e.Seq, &kind, &status, &reason, &e.RecordedAt, &by); err != nil {
			return nil, mapError(err, "scan crosswalk event")
		}
		e.Kind, e.Status, e.Reason = crosswalk.EventKind(kind), crosswalk.Status(status), deref(reason)
		e.RecordedAt, e.RecordedBy = e.RecordedAt.UTC(), id.NewUserID(by)
		k := versionKey{id.NewCrosswalkID(cid), n}
		events[k] = append(events[k], e)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError(err, "read crosswalk events")
	}

	for i := range out {
		for _, v := range versions[out[i].ID] {
			k := versionKey{out[i].ID, v.Number}
			v.Entries = entries[k]
			folded, err := crosswalk.Fold(v, events[k])
			if err != nil {
				return nil, obligationFault(err, "A stored crosswalk version's history does not fold.")
			}
			out[i].Versions = append(out[i].Versions, folded)
		}
	}
	return out, nil
}

// Lock takes one of the tenant's rule-change locks for the transaction.
func (r *RuleChangeRepo) Lock(ctx context.Context, key string) error {
	return contentLock(ctx, r.s, "rule-change", key)
}

// CreateRuleChange writes a proposal and its first events.
func (r *RuleChangeRepo) CreateRuleChange(ctx context.Context, p rulechange.Proposal, events []rulechange.Event) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if p.TenantID != tenant {
		return errs.New(errs.CategoryPolicy, errs.ReasonTenantMismatch, "The proposal belongs to a tenant outside the caller's scope.")
	}
	if err := p.Validate(); err != nil {
		return obligationFault(err, "The proposal is not well-formed.")
	}
	if p.IntakeHash.IsZero() || len(events) == 0 || events[0].Kind != rulechange.EventProposed || events[0].Seq != 1 {
		return obligationFault(nil, "A proposal is created by its PROPOSED event, with its intake hash.")
	}
	if _, err := r.s.db(ctx).Exec(ctx, sqlRuleChangeInsert, tenant.UUID(), p.ID.UUID(), string(p.Pack), p.BaseVersion.String(),
		optString(string(p.Rule)), p.Jurisdiction, string(p.ChangeType), p.Summary, p.Detail, optString(p.Impact),
		string(p.Citation.Source), p.Citation.Reference, p.Citation.Edition, string(p.Citation.Authority),
		optDate(p.PublishedOn), civilDate(p.EffectiveFrom), optDate(p.EffectiveTo), optString(p.OriginRef),
		contentOptUUID(p.Supersedes.UUID(), p.Supersedes.IsZero()), p.IntakeKey, p.IntakeHash.String(), p.CreatedAt.UTC(),
		p.CreatedBy.UUID()); err != nil {
		return mapError(err, "insert rule change")
	}
	for _, e := range events {
		if err := r.AppendRuleChangeEvent(ctx, p.ID, e); err != nil {
			return err
		}
	}
	return nil
}

// AppendRuleChangeEvent writes one proposal event.
func (r *RuleChangeRepo) AppendRuleChangeEvent(ctx context.Context, changeID id.RuleChangeID, e rulechange.Event) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	var ref, sha *string
	if e.TestEvidence != nil {
		ref, sha = optString(e.TestEvidence.Ref), optString(e.TestEvidence.SHA256)
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlRuleChangeEventInsert, tenant.UUID(), changeID.UUID(), e.Seq, string(e.Kind),
		string(e.Status), optString(e.Reason), ref, sha, e.RecordedAt.UTC(), e.RecordedBy.UUID())
	return mapError(err, "insert rule change event")
}

// RuleChange returns one proposal.
func (r *RuleChangeRepo) RuleChange(ctx context.Context, changeID id.RuleChangeID) (rulechange.Proposal, error) {
	got, err := r.read(ctx, whereRuleChangeID, changeID.UUID())
	if err != nil {
		return rulechange.Proposal{}, err
	}
	if len(got) == 0 {
		return rulechange.Proposal{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "No such rule change for this tenant.")
	}
	return got[0], nil
}

// RuleChangeByIntakeKey returns the proposal recorded under key.
func (r *RuleChangeRepo) RuleChangeByIntakeKey(ctx context.Context, key string) (rulechange.Proposal, bool, error) {
	got, err := r.read(ctx, whereRuleChangeIntake, key)
	if err != nil || len(got) == 0 {
		return rulechange.Proposal{}, false, err
	}
	return got[0], true, nil
}

// RuleChanges returns the most recent proposals.
func (r *RuleChangeRepo) RuleChanges(ctx context.Context, limit int) ([]rulechange.Proposal, error) {
	if limit <= 0 || limit > contentListCap {
		limit = contentListCap
	}
	return r.read(ctx, whereRuleChangeRecent, limit)
}

func (r *RuleChangeRepo) read(ctx context.Context, where string, args ...any) ([]rulechange.Proposal, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	db := r.s.db(ctx)
	rows, err := db.Query(ctx, sqlRuleChangeSelect+where, append([]any{tenant.UUID()}, args...)...)
	if err != nil {
		return nil, mapError(err, "read rule changes")
	}
	var out []rulechange.Proposal
	for rows.Next() {
		var (
			p                                   = rulechange.Proposal{TenantID: tenant}
			cid, by                             uuid.UUID
			supersedes                          *uuid.UUID
			pack, base, kind, source, authority string
			hash                                string
			rule, impact, origin                *string
			published, from, until              pgtype.Date
		)
		if err := rows.Scan(&cid, &pack, &base, &rule, &p.Jurisdiction, &kind, &p.Summary, &p.Detail, &impact, &source,
			&p.Citation.Reference, &p.Citation.Edition, &authority, &published, &from, &until, &origin, &supersedes,
			&p.IntakeKey, &hash, &p.CreatedAt, &by); err != nil {
			rows.Close()
			return nil, mapError(err, "scan rule change")
		}
		p.ID, p.CreatedBy, p.CreatedAt = id.NewRuleChangeID(cid), id.NewUserID(by), p.CreatedAt.UTC()
		p.Pack, p.Rule, p.ChangeType = content.PackID(pack), content.RuleSemanticID(deref(rule)), rulechange.ChangeType(kind)
		p.Impact, p.OriginRef = deref(impact), deref(origin)
		p.Citation.Source, p.Citation.Authority = sourcing.SourceID(source), content.AuthorityClass(authority)
		if published.Valid {
			p.PublishedOn = inLocation(published, time.UTC)
		}
		p.EffectiveFrom = inLocation(from, time.UTC)
		if until.Valid {
			p.EffectiveTo = inLocation(until, time.UTC)
		}
		if supersedes != nil {
			p.Supersedes = id.NewRuleChangeID(*supersedes)
		}
		if p.BaseVersion, err = content.ParseVersion(base); err == nil {
			p.IntakeHash, err = canonical.ParseDigest(hash)
		}
		if err != nil {
			rows.Close()
			return nil, obligationFault(err, "A stored rule change does not parse.")
		}
		out = append(out, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, mapError(err, "read rule changes")
	}
	if len(out) == 0 {
		return nil, nil
	}
	ids := make([]uuid.UUID, len(out))
	for i, p := range out {
		ids[i] = p.ID.UUID()
	}
	rows, err = db.Query(ctx, sqlRuleChangeEvents, tenant.UUID(), ids)
	if err != nil {
		return nil, mapError(err, "read rule change events")
	}
	defer rows.Close()
	events := map[id.RuleChangeID][]rulechange.Event{}
	for rows.Next() {
		var (
			e                   rulechange.Event
			cid, by             uuid.UUID
			kind, status        string
			reason, ref, digest *string
		)
		if err := rows.Scan(&cid, &e.Seq, &kind, &status, &reason, &ref, &digest, &e.RecordedAt, &by); err != nil {
			return nil, mapError(err, "scan rule change event")
		}
		e.Kind, e.Status, e.Reason = rulechange.EventKind(kind), rulechange.Status(status), deref(reason)
		e.RecordedAt, e.RecordedBy = e.RecordedAt.UTC(), id.NewUserID(by)
		if ref != nil {
			e.TestEvidence = &rulechange.TestEvidence{Ref: *ref, SHA256: deref(digest)}
		}
		k := id.NewRuleChangeID(cid)
		events[k] = append(events[k], e)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError(err, "read rule change events")
	}
	for i := range out {
		folded, err := rulechange.Fold(out[i], events[out[i].ID])
		if err != nil {
			return nil, obligationFault(err, "A stored rule change's history does not fold.")
		}
		out[i] = folded
	}
	return out, nil
}
