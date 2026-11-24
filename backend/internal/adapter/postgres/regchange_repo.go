package postgres

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/regchange"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// ---------------------------------------------------------------------------
// regulatory_change(_event), impact_assessment, review_recommendation(_event)
// — migration 000038
// ---------------------------------------------------------------------------

const (
	sqlRegChangeColumns = `change_id, jurisdiction, kind, title, summary, source_authority, source_citation, source_url,
		source_edition, source_sha256, published_on, effective_from, discovered_at, detected_by, monitor_ref,
		service_families, tax_types, source_key, dedupe_key, intake_key, intake_hash, created_at, created_by`
	sqlRegChangeInsert = `INSERT INTO ztax.regulatory_change (tenant_id, ` + sqlRegChangeColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24)`
	sqlRegChangeSelect = `SELECT ` + sqlRegChangeColumns + ` FROM ztax.regulatory_change WHERE tenant_id = $1`

	sqlRegChangeEventColumns = `change_id, seq, kind, status_after, priority, disposition, hold_reason, reason,
		assessment_id, recorded_at, recorded_by`
	sqlRegChangeEventInsert = `INSERT INTO ztax.regulatory_change_event (tenant_id, ` + sqlRegChangeEventColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`
	sqlRegChangeEvents = `SELECT ` + sqlRegChangeEventColumns + ` FROM ztax.regulatory_change_event
		WHERE tenant_id = $1 AND change_id = ANY($2) ORDER BY change_id, seq`

	sqlAssessmentColumns = `assessment_id, change_id, bundle_id, bundle_digest, as_of, severity, retroactive, items,
		inputs_digest, assessment_digest, components_read, registrations_read, obligations_read, created_at, created_by`
	sqlAssessmentInsert = `INSERT INTO ztax.impact_assessment (tenant_id, ` + sqlAssessmentColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)`
	sqlAssessmentSelect = `SELECT ` + sqlAssessmentColumns + ` FROM ztax.impact_assessment WHERE tenant_id = $1`

	sqlRecColumns = `recommendation_id, change_id, assessment_id, assessment_digest, origin, ai_use_case, ai_model_profile,
		ai_provider_profile, ai_prompt_profile, ai_train_version, ai_authority_outcome, ai_invocation_id, ai_draft,
		intake_key, intake_hash, created_at, created_by`
	sqlRecInsert = `INSERT INTO ztax.review_recommendation (tenant_id, ` + sqlRecColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)`
	sqlRecSelect = `SELECT ` + sqlRecColumns + ` FROM ztax.review_recommendation WHERE tenant_id = $1`

	sqlRecEventColumns = `recommendation_id, seq, kind, status_after, action, rationale, targets, reason, handoff_ref,
		recorded_at, recorded_by`
	sqlRecEventInsert = `INSERT INTO ztax.review_recommendation_event (tenant_id, ` + sqlRecEventColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`
	sqlRecEvents = `SELECT ` + sqlRecEventColumns + ` FROM ztax.review_recommendation_event
		WHERE tenant_id = $1 AND recommendation_id = ANY($2) ORDER BY recommendation_id, seq`

	sqlRegChangeLock = `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`

	regChangeListCap = 500
)

// The predicates a read takes. Fixed strings from this file, never caller
// text.
const (
	whereRegChangeID      = ` AND change_id = $2`
	whereRegChangeIntake  = ` AND intake_key = $2`
	whereRegChangeDedupe  = ` AND dedupe_key = $2`
	whereRegChangeSource  = ` AND source_key = $2 ORDER BY created_at, change_id`
	whereRegChangeRecent  = ` ORDER BY created_at DESC, change_id DESC LIMIT $2`
	whereAssessmentID     = ` AND assessment_id = $2`
	whereAssessmentOf     = ` AND change_id = $2 ORDER BY created_at DESC, assessment_id DESC`
	whereAssessmentRecent = ` ORDER BY created_at DESC, assessment_id DESC LIMIT $2`
	whereRecID            = ` AND recommendation_id = $2`
	whereRecIntake        = ` AND intake_key = $2`
	whereRecOf            = ` AND change_id = $2 ORDER BY created_at DESC, recommendation_id DESC`
	whereRecRecent        = ` ORDER BY created_at DESC, recommendation_id DESC LIMIT $2`
)

// RegulatoryChangeRepo implements port.RegulatoryChangeRepository.
type RegulatoryChangeRepo struct{ s *Store }

// RegulatoryChanges returns the regulatory change intelligence repository.
func (s *Store) RegulatoryChanges() *RegulatoryChangeRepo { return &RegulatoryChangeRepo{s: s} }

var _ port.RegulatoryChangeRepository = (*RegulatoryChangeRepo)(nil)

// impactItemRow is the stored shape of an impacted item.
type impactItemRow struct {
	Kind          string `json:"kind"`
	Ref           string `json:"ref"`
	Label         string `json:"label"`
	Jurisdiction  string `json:"jurisdiction"`
	Severity      string `json:"severity"`
	Basis         string `json:"basis"`
	Retrospective bool   `json:"retrospective"`
}

// recTargetRow is the stored shape of a recommendation target.
type recTargetRow struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
}

// Lock takes one of the tenant's regulatory change locks for the
// transaction.
func (r *RegulatoryChangeRepo) Lock(ctx context.Context, key string) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlRegChangeLock, tenant.String()+"/regchange/"+key)
	return mapError(err, "lock regulatory change")
}

func regChangeCodes(list []string) []string {
	if list == nil {
		return []string{}
	}
	return list
}

func regChangeUUID(u uuid.UUID, zero bool) *uuid.UUID {
	if zero {
		return nil
	}
	return &u
}

// CreateChange writes a change and its first events.
func (r *RegulatoryChangeRepo) CreateChange(ctx context.Context, c regchange.Change, events []regchange.Event) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if c.TenantID != tenant {
		return errs.New(errs.CategoryPolicy, errs.ReasonTenantMismatch, "The change belongs to a tenant outside the caller's scope.")
	}
	if c.IntakeHash.IsZero() || c.DedupeKey.IsZero() || c.SourceKey.IsZero() || len(events) == 0 ||
		events[0].Kind != regchange.EventRecorded || events[0].Seq != 1 {
		return obligationFault(nil, "A change is created by its RECORDED event, with its keys and intake hash.")
	}
	if _, err := r.s.db(ctx).Exec(ctx, sqlRegChangeInsert, tenant.UUID(), c.ID.UUID(), c.Jurisdiction, string(c.Kind), c.Title,
		optString(c.Summary), c.Source.Authority, c.Source.Citation, optString(c.Source.URL), c.Source.Edition,
		c.Source.DocumentSHA256, civilDate(c.PublishedOn), civilDate(c.EffectiveFrom), c.DiscoveredAt.UTC(),
		string(c.DetectedBy), optString(c.MonitorRef), regChangeCodes(c.ServiceFamilies), regChangeCodes(c.TaxTypes),
		c.SourceKey.String(), c.DedupeKey.String(), c.IntakeKey, c.IntakeHash.String(), c.CreatedAt.UTC(),
		c.CreatedBy.UUID()); err != nil {
		return mapError(err, "insert regulatory change")
	}
	return r.AppendChangeEvents(ctx, c.ID, events)
}

// AppendChangeEvents writes events in order.
func (r *RegulatoryChangeRepo) AppendChangeEvents(ctx context.Context, changeID id.RegulatoryChangeID, events []regchange.Event) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	for _, e := range events {
		if _, err := r.s.db(ctx).Exec(ctx, sqlRegChangeEventInsert, tenant.UUID(), changeID.UUID(), e.Seq, string(e.Kind),
			string(e.Status), optString(string(e.Priority)), optString(string(e.Disposition)), optString(string(e.Hold)),
			optString(e.Reason), regChangeUUID(e.Assessment.UUID(), e.Assessment.IsZero()), e.RecordedAt.UTC(),
			e.RecordedBy.UUID()); err != nil {
			return mapError(err, "insert regulatory change event")
		}
	}
	return nil
}

// Change returns one change.
func (r *RegulatoryChangeRepo) Change(ctx context.Context, changeID id.RegulatoryChangeID) (regchange.Change, error) {
	got, err := r.readChanges(ctx, whereRegChangeID, changeID.UUID())
	if err != nil {
		return regchange.Change{}, err
	}
	if len(got) == 0 {
		return regchange.Change{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "No such regulatory change.")
	}
	return got[0], nil
}

// ChangeByIntakeKey returns the change recorded under a key.
func (r *RegulatoryChangeRepo) ChangeByIntakeKey(ctx context.Context, key string) (regchange.Change, bool, error) {
	got, err := r.readChanges(ctx, whereRegChangeIntake, key)
	if err != nil || len(got) == 0 {
		return regchange.Change{}, false, err
	}
	return got[0], true, nil
}

// ChangeByDedupeKey returns the change of a dedupe key.
func (r *RegulatoryChangeRepo) ChangeByDedupeKey(ctx context.Context, key canonical.Digest) (regchange.Change, bool, error) {
	got, err := r.readChanges(ctx, whereRegChangeDedupe, key.String())
	if err != nil || len(got) == 0 {
		return regchange.Change{}, false, err
	}
	return got[0], true, nil
}

// ChangesBySource returns the changes of one source edition.
func (r *RegulatoryChangeRepo) ChangesBySource(ctx context.Context, key canonical.Digest) ([]regchange.Change, error) {
	return r.readChanges(ctx, whereRegChangeSource, key.String())
}

// Changes returns the most recent changes.
func (r *RegulatoryChangeRepo) Changes(ctx context.Context, limit int) ([]regchange.Change, error) {
	if limit <= 0 || limit > regChangeListCap {
		limit = regChangeListCap
	}
	return r.readChanges(ctx, whereRegChangeRecent, limit)
}

func regChangeDigest(s, what string) (canonical.Digest, error) {
	d, err := canonical.ParseDigest(s)
	if err != nil {
		return canonical.Digest{}, obligationFault(err, "A stored "+what+" does not parse.")
	}
	return d, nil
}

func (r *RegulatoryChangeRepo) readChanges(ctx context.Context, where string, args ...any) ([]regchange.Change, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	db := r.s.db(ctx)
	rows, err := db.Query(ctx, sqlRegChangeSelect+where, append([]any{tenant.UUID()}, args...)...)
	if err != nil {
		return nil, mapError(err, "read regulatory changes")
	}
	var out []regchange.Change
	for rows.Next() {
		var (
			c                                = regchange.Change{TenantID: tenant}
			cid, by                          uuid.UUID
			kind, detected                   string
			summary, url, monitor            *string
			published, effective             pgtype.Date
			sourceKey, dedupeKey, intakeHash string
		)
		if err := rows.Scan(&cid, &c.Jurisdiction, &kind, &c.Title, &summary, &c.Source.Authority, &c.Source.Citation, &url,
			&c.Source.Edition, &c.Source.DocumentSHA256, &published, &effective, &c.DiscoveredAt, &detected, &monitor,
			&c.ServiceFamilies, &c.TaxTypes, &sourceKey, &dedupeKey, &c.IntakeKey, &intakeHash, &c.CreatedAt, &by); err != nil {
			rows.Close()
			return nil, mapError(err, "scan regulatory change")
		}
		c.ID, c.CreatedBy, c.Kind, c.DetectedBy = id.NewRegulatoryChangeID(cid), id.NewUserID(by), regchange.Kind(kind), regchange.Detector(detected)
		c.Summary, c.Source.URL, c.MonitorRef = deref(summary), deref(url), deref(monitor)
		c.PublishedOn, c.EffectiveFrom = inLocation(published, time.UTC), inLocation(effective, time.UTC)
		c.DiscoveredAt, c.CreatedAt = c.DiscoveredAt.UTC(), c.CreatedAt.UTC()
		if c.SourceKey, err = regChangeDigest(sourceKey, "change source key"); err == nil {
			if c.DedupeKey, err = regChangeDigest(dedupeKey, "change dedupe key"); err == nil {
				c.IntakeHash, err = regChangeDigest(intakeHash, "change intake hash")
			}
		}
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, mapError(err, "read regulatory changes")
	}
	if len(out) == 0 {
		return nil, nil
	}

	ids := make([]uuid.UUID, len(out))
	for i, c := range out {
		ids[i] = c.ID.UUID()
	}
	rows, err = db.Query(ctx, sqlRegChangeEvents, tenant.UUID(), ids)
	if err != nil {
		return nil, mapError(err, "read regulatory change events")
	}
	defer rows.Close()
	events := map[id.RegulatoryChangeID][]regchange.Event{}
	for rows.Next() {
		var (
			e                                   regchange.Event
			cid, by                             uuid.UUID
			kind, status                        string
			priority, disposition, hold, reason *string
			assessment                          *uuid.UUID
		)
		if err := rows.Scan(&cid, &e.Seq, &kind, &status, &priority, &disposition, &hold, &reason, &assessment,
			&e.RecordedAt, &by); err != nil {
			return nil, mapError(err, "scan regulatory change event")
		}
		e.Kind, e.Status, e.Reason = regchange.EventKind(kind), regchange.Status(status), deref(reason)
		e.Priority, e.Disposition, e.Hold = regchange.Priority(deref(priority)), regchange.Disposition(deref(disposition)), regchange.HoldReason(deref(hold))
		if assessment != nil {
			e.Assessment = id.NewImpactAssessmentID(*assessment)
		}
		e.RecordedAt, e.RecordedBy = e.RecordedAt.UTC(), id.NewUserID(by)
		k := id.NewRegulatoryChangeID(cid)
		events[k] = append(events[k], e)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError(err, "read regulatory change events")
	}
	for i := range out {
		folded, err := regchange.Fold(out[i], events[out[i].ID])
		if err != nil {
			return nil, obligationFault(err, "A stored regulatory change's history does not fold.")
		}
		out[i] = folded
	}
	return out, nil
}

// CreateAssessment writes an assessment.
func (r *RegulatoryChangeRepo) CreateAssessment(ctx context.Context, a regchange.Assessment) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if a.TenantID != tenant {
		return errs.New(errs.CategoryPolicy, errs.ReasonTenantMismatch, "The assessment belongs to a tenant outside the caller's scope.")
	}
	if err := a.Verify(); err != nil || len(a.Items) == 0 {
		return obligationFault(err, "An assessment is written with its items and the digest over them.")
	}
	rows := make([]impactItemRow, len(a.Items))
	for i, it := range a.Items {
		rows[i] = impactItemRow{Kind: string(it.Kind), Ref: it.Ref, Label: it.Label, Jurisdiction: it.Jurisdiction,
			Severity: string(it.Severity), Basis: it.Basis, Retrospective: it.Retrospective}
	}
	items, err := json.Marshal(rows)
	if err != nil {
		return obligationFault(err, "The assessment's items could not be encoded.")
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlAssessmentInsert, tenant.UUID(), a.ID.UUID(), a.Change.UUID(), a.BundleID, a.BundleDigest,
		civilDate(a.AsOf), string(a.Severity), a.Retroactive, items, a.InputsDigest.String(), a.AssessmentDigest.String(),
		a.ComponentsRead, a.RegistrationsRead, a.ObligationsRead, a.CreatedAt.UTC(), a.CreatedBy.UUID())
	return mapError(err, "insert impact assessment")
}

// Assessment returns one assessment.
func (r *RegulatoryChangeRepo) Assessment(ctx context.Context, assessmentID id.ImpactAssessmentID) (regchange.Assessment, error) {
	got, err := r.readAssessments(ctx, whereAssessmentID, assessmentID.UUID())
	if err != nil {
		return regchange.Assessment{}, err
	}
	if len(got) == 0 {
		return regchange.Assessment{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "No such impact assessment.")
	}
	return got[0], nil
}

// AssessmentsOf returns a change's assessments, newest first.
func (r *RegulatoryChangeRepo) AssessmentsOf(ctx context.Context, changeID id.RegulatoryChangeID) ([]regchange.Assessment, error) {
	return r.readAssessments(ctx, whereAssessmentOf, changeID.UUID())
}

// Assessments returns the most recent assessments.
func (r *RegulatoryChangeRepo) Assessments(ctx context.Context, limit int) ([]regchange.Assessment, error) {
	if limit <= 0 || limit > regChangeListCap {
		limit = regChangeListCap
	}
	return r.readAssessments(ctx, whereAssessmentRecent, limit)
}

func (r *RegulatoryChangeRepo) readAssessments(ctx context.Context, where string, args ...any) ([]regchange.Assessment, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := r.s.db(ctx).Query(ctx, sqlAssessmentSelect+where, append([]any{tenant.UUID()}, args...)...)
	if err != nil {
		return nil, mapError(err, "read impact assessments")
	}
	defer rows.Close()
	var out []regchange.Assessment
	for rows.Next() {
		var (
			a                = regchange.Assessment{TenantID: tenant}
			aid, cid, by     uuid.UUID
			asOf             pgtype.Date
			severity         string
			items            []byte
			inputs, assessed string
		)
		if err := rows.Scan(&aid, &cid, &a.BundleID, &a.BundleDigest, &asOf, &severity, &a.Retroactive, &items, &inputs,
			&assessed, &a.ComponentsRead, &a.RegistrationsRead, &a.ObligationsRead, &a.CreatedAt, &by); err != nil {
			return nil, mapError(err, "scan impact assessment")
		}
		a.ID, a.Change, a.CreatedBy = id.NewImpactAssessmentID(aid), id.NewRegulatoryChangeID(cid), id.NewUserID(by)
		a.AsOf, a.Severity, a.CreatedAt = inLocation(asOf, time.UTC), regchange.Severity(severity), a.CreatedAt.UTC()
		var stored []impactItemRow
		if err := json.Unmarshal(items, &stored); err != nil {
			return nil, obligationFault(err, "A stored impact assessment's items do not decode.")
		}
		for _, it := range stored {
			a.Items = append(a.Items, regchange.Item{Kind: regchange.ItemKind(it.Kind), Ref: it.Ref, Label: it.Label,
				Jurisdiction: it.Jurisdiction, Severity: regchange.Severity(it.Severity), Basis: it.Basis, Retrospective: it.Retrospective})
		}
		if a.InputsDigest, err = regChangeDigest(inputs, "assessment inputs digest"); err != nil {
			return nil, err
		}
		if a.AssessmentDigest, err = regChangeDigest(assessed, "assessment digest"); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, mapError(rows.Err(), "read impact assessments")
}

// CreateRecommendation writes a recommendation and its DRAFTED event.
func (r *RegulatoryChangeRepo) CreateRecommendation(ctx context.Context, rec regchange.Recommendation, drafted regchange.RecEvent) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if rec.TenantID != tenant {
		return errs.New(errs.CategoryPolicy, errs.ReasonTenantMismatch, "The recommendation belongs to a tenant outside the caller's scope.")
	}
	if err := rec.Validate(); err != nil {
		return obligationFault(err, "The recommendation is not well-formed.")
	}
	if rec.IntakeHash.IsZero() || drafted.Kind != regchange.RecEventDrafted || drafted.Seq != 1 {
		return obligationFault(nil, "A recommendation is created by its DRAFTED event, with its intake hash.")
	}
	var useCase, model, provider, prompt, train, outcome, invocation, draft *string
	if p := rec.AI; p != nil {
		o := string(p.AuthorityOutcome)
		useCase, model, provider, prompt = &p.UseCase, &p.ModelProfile, &p.ProviderProfile, &p.PromptProfile
		train, outcome, invocation, draft = &p.AiTrainVersion, &o, optString(p.InvocationID), &p.Draft
	}
	if _, err := r.s.db(ctx).Exec(ctx, sqlRecInsert, tenant.UUID(), rec.ID.UUID(), rec.Change.UUID(), rec.Assessment.UUID(),
		rec.AssessmentDigest.String(), string(rec.Origin), useCase, model, provider, prompt, train, outcome, invocation, draft,
		rec.IntakeKey, rec.IntakeHash.String(), rec.CreatedAt.UTC(), rec.CreatedBy.UUID()); err != nil {
		return mapError(err, "insert review recommendation")
	}
	return r.AppendRecommendationEvent(ctx, rec.ID, drafted)
}

// AppendRecommendationEvent writes a recommendation's next event.
func (r *RegulatoryChangeRepo) AppendRecommendationEvent(ctx context.Context, recommendationID id.ReviewRecommendationID, e regchange.RecEvent) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	var (
		action, rationale *string
		targets           []byte
	)
	if c := e.Content; c != nil {
		a := string(c.Action)
		action, rationale = &a, &c.Rationale
		rows := make([]recTargetRow, len(c.Targets))
		for i, t := range c.Targets {
			rows[i] = recTargetRow{Kind: string(t.Kind), Ref: t.Ref}
		}
		if targets, err = json.Marshal(rows); err != nil {
			return obligationFault(err, "The recommendation's targets could not be encoded.")
		}
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlRecEventInsert, tenant.UUID(), recommendationID.UUID(), e.Seq, string(e.Kind),
		string(e.Status), action, rationale, targets, optString(e.Reason), optString(e.HandoffRef), e.RecordedAt.UTC(),
		e.RecordedBy.UUID())
	return mapError(err, "insert review recommendation event")
}

// Recommendation returns one recommendation.
func (r *RegulatoryChangeRepo) Recommendation(ctx context.Context, recommendationID id.ReviewRecommendationID) (regchange.Recommendation, error) {
	got, err := r.readRecommendations(ctx, whereRecID, recommendationID.UUID())
	if err != nil {
		return regchange.Recommendation{}, err
	}
	if len(got) == 0 {
		return regchange.Recommendation{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "No such review recommendation.")
	}
	return got[0], nil
}

// RecommendationByIntakeKey returns the recommendation drafted under a key.
func (r *RegulatoryChangeRepo) RecommendationByIntakeKey(ctx context.Context, key string) (regchange.Recommendation, bool, error) {
	got, err := r.readRecommendations(ctx, whereRecIntake, key)
	if err != nil || len(got) == 0 {
		return regchange.Recommendation{}, false, err
	}
	return got[0], true, nil
}

// RecommendationsOf returns a change's recommendations, newest first.
func (r *RegulatoryChangeRepo) RecommendationsOf(ctx context.Context, changeID id.RegulatoryChangeID) ([]regchange.Recommendation, error) {
	return r.readRecommendations(ctx, whereRecOf, changeID.UUID())
}

// Recommendations returns the most recent recommendations.
func (r *RegulatoryChangeRepo) Recommendations(ctx context.Context, limit int) ([]regchange.Recommendation, error) {
	if limit <= 0 || limit > regChangeListCap {
		limit = regChangeListCap
	}
	return r.readRecommendations(ctx, whereRecRecent, limit)
}

func (r *RegulatoryChangeRepo) readRecommendations(ctx context.Context, where string, args ...any) ([]regchange.Recommendation, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	db := r.s.db(ctx)
	rows, err := db.Query(ctx, sqlRecSelect+where, append([]any{tenant.UUID()}, args...)...)
	if err != nil {
		return nil, mapError(err, "read review recommendations")
	}
	var out []regchange.Recommendation
	for rows.Next() {
		var (
			rec                                                   = regchange.Recommendation{TenantID: tenant}
			rid, cid, aid, by                                     uuid.UUID
			digest, origin, intakeHash                            string
			useCase, model, provider, prompt, train, outcome, inv *string
			draft                                                 *string
		)
		if err := rows.Scan(&rid, &cid, &aid, &digest, &origin, &useCase, &model, &provider, &prompt, &train, &outcome, &inv,
			&draft, &rec.IntakeKey, &intakeHash, &rec.CreatedAt, &by); err != nil {
			rows.Close()
			return nil, mapError(err, "scan review recommendation")
		}
		rec.ID, rec.Change, rec.Assessment = id.NewReviewRecommendationID(rid), id.NewRegulatoryChangeID(cid), id.NewImpactAssessmentID(aid)
		rec.Origin, rec.CreatedBy, rec.CreatedAt = regchange.Origin(origin), id.NewUserID(by), rec.CreatedAt.UTC()
		if useCase != nil {
			rec.AI = &regchange.AIProvenance{UseCase: *useCase, ModelProfile: deref(model), ProviderProfile: deref(provider),
				PromptProfile: deref(prompt), AiTrainVersion: deref(train), AuthorityOutcome: regchange.AuthorityOutcome(deref(outcome)),
				InvocationID: deref(inv), Draft: deref(draft)}
		}
		if rec.AssessmentDigest, err = regChangeDigest(digest, "recommendation assessment digest"); err == nil {
			rec.IntakeHash, err = regChangeDigest(intakeHash, "recommendation intake hash")
		}
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, rec)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, mapError(err, "read review recommendations")
	}
	if len(out) == 0 {
		return nil, nil
	}

	ids := make([]uuid.UUID, len(out))
	for i, rec := range out {
		ids[i] = rec.ID.UUID()
	}
	rows, err = db.Query(ctx, sqlRecEvents, tenant.UUID(), ids)
	if err != nil {
		return nil, mapError(err, "read review recommendation events")
	}
	defer rows.Close()
	events := map[id.ReviewRecommendationID][]regchange.RecEvent{}
	for rows.Next() {
		var (
			e                                  regchange.RecEvent
			rid, by                            uuid.UUID
			kind, status                       string
			action, rationale, reason, handoff *string
			targets                            []byte
		)
		if err := rows.Scan(&rid, &e.Seq, &kind, &status, &action, &rationale, &targets, &reason, &handoff,
			&e.RecordedAt, &by); err != nil {
			return nil, mapError(err, "scan review recommendation event")
		}
		e.Kind, e.Status, e.Reason, e.HandoffRef = regchange.RecEventKind(kind), regchange.RecStatus(status), deref(reason), deref(handoff)
		e.RecordedAt, e.RecordedBy = e.RecordedAt.UTC(), id.NewUserID(by)
		if action != nil {
			var stored []recTargetRow
			if err := json.Unmarshal(targets, &stored); err != nil {
				return nil, obligationFault(err, "A stored recommendation's targets do not decode.")
			}
			c := regchange.Content{Action: regchange.Action(*action), Rationale: deref(rationale), Targets: []regchange.Target{}}
			for _, t := range stored {
				c.Targets = append(c.Targets, regchange.Target{Kind: regchange.ItemKind(t.Kind), Ref: t.Ref})
			}
			e.Content = &c
		}
		k := id.NewReviewRecommendationID(rid)
		events[k] = append(events[k], e)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError(err, "read review recommendation events")
	}
	for i := range out {
		folded, err := regchange.FoldRecommendation(out[i], events[out[i].ID])
		if err != nil {
			return nil, obligationFault(err, "A stored recommendation's history does not fold.")
		}
		out[i] = folded
	}
	return out, nil
}
