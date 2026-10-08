package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/legal"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// ---------------------------------------------------------------------------
// customer_authorization, customer_authorization_event — migration 000021
// ---------------------------------------------------------------------------

const (
	sqlAuthColumns = `authorization_id, legal_entity_id, country_code, authority, auth_type, permissions, matters,
		period_from, period_to, representative, effective_from, expires_at, evidence, credential_ref, supersedes,
		recorded_at, recorded_by`
	sqlAuthInsert = `INSERT INTO ztax.customer_authorization (tenant_id, ` + sqlAuthColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)`
	sqlAuthEventInsert = `INSERT INTO ztax.customer_authorization_event
		(tenant_id, authorization_id, seq, kind, reason, superseded_by, recorded_at, recorded_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`
	sqlAuthSelect = `SELECT ` + sqlAuthColumns + ` FROM ztax.customer_authorization WHERE tenant_id = $1`
	sqlAuthEvents = `SELECT authorization_id, seq, kind, reason, superseded_by, recorded_at, recorded_by
		FROM ztax.customer_authorization_event WHERE tenant_id = $1`
	sqlAuthLock = `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`
)

// AuthorizationRepo implements port.AuthorizationRepository.
type AuthorizationRepo struct{ s *Store }

// Authorizations returns the customer authorization repository.
func (s *Store) Authorizations() *AuthorizationRepo { return &AuthorizationRepo{s: s} }

var _ port.AuthorizationRepository = (*AuthorizationRepo)(nil)

// Lock takes the tenant's authorization lock for the transaction.
func (r *AuthorizationRepo) Lock(ctx context.Context) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlAuthLock, tenant.String()+"/customer-authorization")
	return mapError(err, "lock customer authorizations")
}

// Create records an authorization and its first event.
func (r *AuthorizationRepo) Create(ctx context.Context, a legal.Authorization, granted legal.Event) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if a.TenantID != tenant {
		return errs.New(errs.CategoryPolicy, errs.ReasonTenantMismatch, "The authorization belongs to a tenant outside the caller's scope.")
	}
	if err := a.Validate(); err != nil {
		return authFault(err, "The authorization is not well-formed.")
	}
	if granted.Kind != legal.EventGranted || granted.Seq != 1 {
		return authFault(nil, "An authorization is created by its GRANTED event.")
	}
	perms := make([]string, len(a.Permissions))
	for i, p := range a.Permissions {
		perms[i] = string(p)
	}
	matters := a.Matters
	if matters == nil {
		matters = []string{}
	}
	var expires *time.Time
	if !a.ExpiresAt.IsZero() {
		e := a.ExpiresAt.UTC()
		expires = &e
	}
	var supersedes *uuid.UUID
	if !a.Supersedes.IsZero() {
		u := a.Supersedes.UUID()
		supersedes = &u
	}
	db := r.s.db(ctx)
	if _, err := db.Exec(ctx, sqlAuthInsert, tenant.UUID(), a.ID.UUID(), a.LegalEntity.UUID(), a.Country, a.Authority,
		string(a.Type), perms, matters, optString(a.PeriodFrom), optString(a.PeriodTo), optString(a.Representative),
		a.EffectiveFrom.UTC(), expires, a.Evidence, optString(a.CredentialRef), supersedes,
		a.RecordedAt.UTC(), a.RecordedBy.UUID()); err != nil {
		return mapError(err, "insert customer authorization")
	}
	return r.AppendEvent(ctx, a.ID, granted)
}

// AppendEvent records one event.
func (r *AuthorizationRepo) AppendEvent(ctx context.Context, authorizationID id.AuthorizationID, e legal.Event) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	var by *uuid.UUID
	if !e.By.IsZero() {
		u := e.By.UUID()
		by = &u
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlAuthEventInsert, tenant.UUID(), authorizationID.UUID(), e.Seq, string(e.Kind),
		optString(e.Reason), by, e.RecordedAt.UTC(), e.RecordedBy.UUID())
	return mapError(err, "insert customer authorization event")
}

// Authorization returns one authorization.
func (r *AuthorizationRepo) Authorization(ctx context.Context, authorizationID id.AuthorizationID) (legal.Authorization, error) {
	got, err := r.read(ctx, &authorizationID)
	if err != nil {
		return legal.Authorization{}, err
	}
	if len(got) == 0 {
		return legal.Authorization{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "No such authorization.")
	}
	return got[0], nil
}

// List returns every authorization of the tenant, newest first.
func (r *AuthorizationRepo) List(ctx context.Context) ([]legal.Authorization, error) {
	return r.read(ctx, nil)
}

func (r *AuthorizationRepo) read(ctx context.Context, only *id.AuthorizationID) ([]legal.Authorization, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	authSQL, eventSQL := sqlAuthSelect+` ORDER BY recorded_at DESC, authorization_id DESC`, sqlAuthEvents+` ORDER BY authorization_id, seq`
	args := []any{tenant.UUID()}
	if only != nil {
		authSQL, eventSQL = sqlAuthSelect+` AND authorization_id = $2`, sqlAuthEvents+` AND authorization_id = $2 ORDER BY seq`
		args = append(args, only.UUID())
	}
	db := r.s.db(ctx)

	var out []legal.Authorization
	rows, err := db.Query(ctx, authSQL, args...)
	if err != nil {
		return nil, mapError(err, "read customer authorizations")
	}
	for rows.Next() {
		var (
			a                                     = legal.Authorization{TenantID: tenant}
			authUUID, leUUID, by                  uuid.UUID
			authType                              string
			perms                                 []string
			periodFrom, periodTo, rep, credential *string
			expires                               *time.Time
			supersedes                            *uuid.UUID
		)
		if err := rows.Scan(&authUUID, &leUUID, &a.Country, &a.Authority, &authType, &perms, &a.Matters,
			&periodFrom, &periodTo, &rep, &a.EffectiveFrom, &expires, &a.Evidence, &credential, &supersedes,
			&a.RecordedAt, &by); err != nil {
			rows.Close()
			return nil, mapError(err, "scan customer authorization")
		}
		a.ID, a.LegalEntity, a.RecordedBy = id.NewAuthorizationID(authUUID), id.NewLegalEntityID(leUUID), id.NewUserID(by)
		a.Type = legal.AuthorizationType(authType)
		for _, p := range perms {
			a.Permissions = append(a.Permissions, legal.Permission(p))
		}
		if len(a.Matters) == 0 {
			a.Matters = nil
		}
		a.PeriodFrom, a.PeriodTo, a.Representative, a.CredentialRef = deref(periodFrom), deref(periodTo), deref(rep), deref(credential)
		a.EffectiveFrom, a.RecordedAt = a.EffectiveFrom.UTC(), a.RecordedAt.UTC()
		if expires != nil {
			a.ExpiresAt = expires.UTC()
		}
		if supersedes != nil {
			a.Supersedes = id.NewAuthorizationID(*supersedes)
		}
		out = append(out, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, mapError(err, "read customer authorizations")
	}

	events := map[id.AuthorizationID][]legal.Event{}
	rows, err = db.Query(ctx, eventSQL, args...)
	if err != nil {
		return nil, mapError(err, "read customer authorization events")
	}
	defer rows.Close()
	for rows.Next() {
		var (
			e            legal.Event
			authUUID, by uuid.UUID
			kind         string
			reason       *string
			supersededBy *uuid.UUID
		)
		if err := rows.Scan(&authUUID, &e.Seq, &kind, &reason, &supersededBy, &e.RecordedAt, &by); err != nil {
			return nil, mapError(err, "scan customer authorization event")
		}
		e.Kind, e.Reason, e.RecordedAt, e.RecordedBy = legal.EventKind(kind), deref(reason), e.RecordedAt.UTC(), id.NewUserID(by)
		if supersededBy != nil {
			e.By = id.NewAuthorizationID(*supersededBy)
		}
		k := id.NewAuthorizationID(authUUID)
		events[k] = append(events[k], e)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError(err, "read customer authorization events")
	}
	for i := range out {
		folded, err := legal.Fold(out[i], events[out[i].ID])
		if err != nil {
			return nil, authFault(err, "A stored authorization's history does not fold.")
		}
		out[i] = folded
	}
	return out, nil
}

// authFault is an internal error: an authorization record no correct writer
// produces.
func authFault(err error, msg string) error { return obligationFault(err, msg) }
