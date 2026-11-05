package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/identity"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// credential_token — migration 000026

const (
	sqlLinkInsert = `
		INSERT INTO ztax.credential_token (tenant_id, token_digest, user_id, purpose, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)`

	sqlLinkSupersede = `
		UPDATE ztax.credential_token SET used_at = $4
		WHERE tenant_id = $1 AND user_id = $2 AND purpose = $3 AND used_at IS NULL`

	// No tenant predicate, and there cannot be one: the caller has a link and
	// nothing else. token_digest is unique across the cell, so this resolves
	// to exactly one token or to none, and the tenant is read from the row.
	sqlLinkByDigest = `
		SELECT tenant_id, token_digest, user_id, purpose, created_at, expires_at, used_at
		FROM ztax.credential_token WHERE token_digest = $1`

	sqlLinkSpend = `
		UPDATE ztax.credential_token SET used_at = $3
		WHERE tenant_id = $1 AND token_digest = $2 AND used_at IS NULL`
)

// CredentialTokenRepo implements port.CredentialTokenRepository.
type CredentialTokenRepo struct{ s *Store }

// CredentialTokens returns the credential-token repository.
func (s *Store) CredentialTokens() *CredentialTokenRepo { return &CredentialTokenRepo{s: s} }

var _ port.CredentialTokenRepository = (*CredentialTokenRepo)(nil)

// Issue stores t and spends the user's earlier unspent tokens of its purpose.
func (r *CredentialTokenRepo) Issue(ctx context.Context, t identity.CredentialToken) error {
	ctx = tenantScope(ctx, t.TenantID)
	db := r.s.db(ctx)
	if _, err := db.Exec(ctx, sqlLinkSupersede,
		t.TenantID.UUID(), t.UserID.UUID(), string(t.Purpose), t.CreatedAt); err != nil {
		return mapError(err, "supersede credential tokens")
	}
	_, err := db.Exec(ctx, sqlLinkInsert,
		t.TenantID.UUID(), t.Digest, t.UserID.UUID(), string(t.Purpose), t.CreatedAt, t.ExpiresAt)
	return mapError(err, "insert credential token")
}

// ByDigest resolves a presented link.
func (r *CredentialTokenRepo) ByDigest(ctx context.Context, digest []byte) (identity.CredentialToken, error) {
	// Cell-wide: a link names no tenant, and a digest resolves to exactly one
	// token or none — the row says whose it is.
	ctx = CellScope(ctx)
	var (
		tenantUUID uuid.UUID
		userUUID   uuid.UUID
		purpose    string
		t          identity.CredentialToken
	)
	err := r.s.db(ctx).QueryRow(ctx, sqlLinkByDigest, digest).Scan(
		&tenantUUID, &t.Digest, &userUUID, &purpose, &t.CreatedAt, &t.ExpiresAt, &t.UsedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// A link that matches nothing is the same answer as one that has
			// expired: a 404 would tell a prober which digests exist.
			return identity.CredentialToken{}, errs.Wrap(err, errs.CategoryPolicy, errs.ReasonCredentialLinkInvalid,
				"The link is not valid, has expired or has already been used. Ask for a new one.")
		}
		return identity.CredentialToken{}, mapError(err, "read credential token")
	}
	t.TenantID = id.NewTenantID(tenantUUID)
	t.UserID = id.NewUserID(userUUID)
	t.Purpose = identity.TokenPurpose(purpose)
	return t, nil
}

// Spend marks a token used, and reports whether this call was the one that
// spent it.
func (r *CredentialTokenRepo) Spend(ctx context.Context, digest []byte, at time.Time) (bool, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return false, err
	}
	tag, err := r.s.db(ctx).Exec(ctx, sqlLinkSpend, tenant.UUID(), digest, at)
	if err != nil {
		return false, mapError(err, "spend credential token")
	}
	return tag.RowsAffected() == 1, nil
}
