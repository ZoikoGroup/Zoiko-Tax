package app

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/legal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/clock"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// LegalAuthorization (ZTAX-LEG-001 §3, §4, §9; ADR-LEG-001, -003). The rules
// are internal/domain/legal's; this is where the matrix the cell loaded meets
// the customer's grants in the store.
//
// Gate is what every legally sensitive action calls before it executes
// (ZTAX-LEG-REQ-0001); the check endpoint is the same call, made by a person
// asking. A cell with no matrix loaded blocks every such action rather than
// guessing a posture.
//
// A customer authorization is recorded, superseded and revoked by an
// administrator acting as themselves — never by system work, and never by a
// model, which may not sign or approve a grant (ZTAX-LEG-REQ-0044). Each act
// is in the administrative audit trail.

// LegalService holds the matrix and the customer's authorizations.
type LegalService struct {
	matrix   legal.Matrix
	auths    port.AuthorizationRepository
	entities port.LegalEntityRepository
	audit    port.AuditRepository
	tx       port.TxManager
	clock    clock.Clock
	ids      idgen.Generator
}

// NewLegalService wires the service over a loaded matrix; the zero Matrix is
// none, and blocks everything.
func NewLegalService(matrix legal.Matrix, auths port.AuthorizationRepository, entities port.LegalEntityRepository,
	audit port.AuditRepository, tx port.TxManager, clk clock.Clock, ids idgen.Generator) *LegalService {
	return &LegalService{matrix: matrix, auths: auths, entities: entities, audit: audit, tx: tx, clock: clk, ids: ids}
}

func (s *LegalService) now() time.Time { return s.clock.Now().UTC().Truncate(time.Microsecond) }

// Now is the service's clock: the instant a read describes "in force" at.
func (s *LegalService) Now() time.Time { return s.now() }

// Matrix returns the authorization matrix the cell resolves against.
func (s *LegalService) Matrix(ctx context.Context) (legal.Matrix, error) {
	if _, err := requireRoleOrSystem(ctx, security.RoleAdmin, security.RoleOperator, security.RoleAnalyst, security.RoleAuditor); err != nil {
		return legal.Matrix{}, err
	}
	return s.matrix, nil
}

// GrantInput is an authorization to record.
type GrantInput struct {
	// LegalEntity is the grantor; zero is the tenant's default.
	LegalEntity    id.LegalEntityID
	Country        string
	Authority      string
	Type           legal.AuthorizationType
	Permissions    []legal.Permission
	Matters        []string
	PeriodFrom     string
	PeriodTo       string
	Representative string
	EffectiveFrom  time.Time
	ExpiresAt      time.Time
	Evidence       []string
	CredentialRef  string
	// Supersedes is the grant this one replaces, which is closed in the
	// same act.
	Supersedes id.AuthorizationID
}

// Grant records a customer authorization.
func (s *LegalService) Grant(ctx context.Context, in GrantInput) (legal.Authorization, error) {
	sc, err := requirePerson(ctx, security.RoleAdmin)
	if err != nil {
		return legal.Authorization{}, err
	}
	authID, err := idgen.AuthorizationID(s.ids)
	if err != nil {
		return legal.Authorization{}, internal(err, "The authorization could not be recorded.")
	}
	now := s.now()
	a := legal.Authorization{
		ID: authID, TenantID: sc.Tenant(), LegalEntity: in.LegalEntity, Country: in.Country, Authority: strings.TrimSpace(in.Authority),
		Type: in.Type, Permissions: slices.Compact(slices.Sorted(slices.Values(in.Permissions))), Matters: in.Matters,
		PeriodFrom: in.PeriodFrom, PeriodTo: in.PeriodTo, Representative: strings.TrimSpace(in.Representative),
		EffectiveFrom: in.EffectiveFrom.UTC(), ExpiresAt: in.ExpiresAt.UTC(), Evidence: in.Evidence,
		CredentialRef: in.CredentialRef, Supersedes: in.Supersedes, RecordedAt: now, RecordedBy: sc.Subject(),
	}
	if in.ExpiresAt.IsZero() {
		a.ExpiresAt = time.Time{}
	}
	var out legal.Authorization
	err = s.inTx(ctx, func(ctx context.Context) error {
		if a.LegalEntity.IsZero() {
			le, err := s.entities.Default(ctx)
			if err != nil {
				return err
			}
			a.LegalEntity = le.ID
		} else if _, err := s.entities.ByID(ctx, a.LegalEntity); err != nil {
			return err
		}
		if err := a.Validate(); err != nil {
			return errs.Wrap(err, errs.CategoryValidation, errs.ReasonInvalidValue, strings.TrimPrefix(err.Error(), "legal: ")+".")
		}
		if err := s.auths.Lock(ctx); err != nil {
			return err
		}
		if !a.Supersedes.IsZero() {
			old, err := s.auths.Authorization(ctx, a.Supersedes)
			if err != nil {
				return err
			}
			if old.Status != legal.AuthActive {
				return errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
					fmt.Sprintf("The authorization it supersedes is %s; only an active grant is superseded.", old.Status))
			}
			if old.LegalEntity != a.LegalEntity || old.Country != a.Country || old.Authority != a.Authority {
				return errs.Invalid("supersedes", errs.ReasonInvalidValue,
					"A grant supersedes one from the same legal entity to the same authority.")
			}
		}
		if err := s.auths.Create(ctx, a, legal.Event{Seq: 1, Kind: legal.EventGranted, RecordedAt: now, RecordedBy: sc.Subject()}); err != nil {
			return err
		}
		if !a.Supersedes.IsZero() {
			if err := s.auths.AppendEvent(ctx, a.Supersedes, legal.Event{Seq: 2, Kind: legal.EventSuperseded,
				Reason: "superseded by " + a.ID.String(), By: a.ID, RecordedAt: now, RecordedBy: sc.Subject()}); err != nil {
				return err
			}
		}
		if out, err = s.auths.Authorization(ctx, a.ID); err != nil {
			return err
		}
		return s.record(ctx, sc, "AUTHORIZATION_GRANTED", a.ID.String(),
			canonical.F("authority", canonical.String(a.Authority)),
			canonical.F("country", canonical.String(a.Country)),
			canonical.F("type", canonical.String(string(a.Type))),
			canonical.F("supersedes", canonical.String(optID(a.Supersedes))))
	})
	return out, err
}

// Revoke revokes a grant. Every action that depended on it is blocked from
// the moment it commits (ZTAX-LEG-REQ-0016).
func (s *LegalService) Revoke(ctx context.Context, authorizationID id.AuthorizationID, reason string) (legal.Authorization, error) {
	sc, err := requirePerson(ctx, security.RoleAdmin)
	if err != nil {
		return legal.Authorization{}, err
	}
	if strings.TrimSpace(reason) == "" || len(reason) > 1000 {
		return legal.Authorization{}, errs.Invalid("reason", errs.ReasonMissingField, "A revocation gives a reason of at most 1000 characters.")
	}
	var out legal.Authorization
	err = s.inTx(ctx, func(ctx context.Context) error {
		if err := s.auths.Lock(ctx); err != nil {
			return err
		}
		cur, err := s.auths.Authorization(ctx, authorizationID)
		if err != nil {
			return err
		}
		if cur.Status != legal.AuthActive {
			return errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
				fmt.Sprintf("The authorization is already %s.", cur.Status))
		}
		if err := s.auths.AppendEvent(ctx, authorizationID, legal.Event{Seq: len(cur.History) + 1, Kind: legal.EventRevoked,
			Reason: reason, RecordedAt: s.now(), RecordedBy: sc.Subject()}); err != nil {
			return err
		}
		if out, err = s.auths.Authorization(ctx, authorizationID); err != nil {
			return err
		}
		return s.record(ctx, sc, "AUTHORIZATION_REVOKED", authorizationID.String())
	})
	return out, err
}

// Authorization reads one grant.
func (s *LegalService) Authorization(ctx context.Context, authorizationID id.AuthorizationID) (legal.Authorization, error) {
	if _, err := requireRoleOrSystem(ctx, security.RoleAdmin, security.RoleOperator, security.RoleAuditor); err != nil {
		return legal.Authorization{}, err
	}
	return s.auths.Authorization(ctx, authorizationID)
}

// Authorizations lists the tenant's grants, newest first.
func (s *LegalService) Authorizations(ctx context.Context) ([]legal.Authorization, error) {
	if _, err := requireRoleOrSystem(ctx, security.RoleAdmin, security.RoleOperator, security.RoleAuditor); err != nil {
		return nil, err
	}
	return s.auths.List(ctx)
}

// Gate resolves whether an action may execute now. The legal entity is the
// tenant's default when the action names none.
func (s *LegalService) Gate(ctx context.Context, act legal.Action) (legal.Resolution, time.Time, error) {
	if _, err := requireRoleOrSystem(ctx, security.RoleAdmin, security.RoleOperator, security.RoleAnalyst); err != nil {
		return legal.Resolution{}, time.Time{}, err
	}
	if act.LegalEntity.IsZero() {
		le, err := s.entities.Default(ctx)
		if err != nil {
			return legal.Resolution{}, time.Time{}, err
		}
		act.LegalEntity = le.ID
	}
	auths, err := s.auths.List(ctx)
	if err != nil {
		return legal.Resolution{}, time.Time{}, err
	}
	now := s.now()
	return legal.Resolve(s.matrix, auths, act, now), now, nil
}

func optID(a id.AuthorizationID) string {
	if a.IsZero() {
		return ""
	}
	return a.String()
}

func (s *LegalService) record(ctx context.Context, sc security.Context, action, subjectID string, detail ...canonical.Field) error {
	auditID, err := idgen.AuditID(s.ids)
	if err != nil {
		return internal(err, "The action could not be recorded.")
	}
	body, err := canonical.Encode(canonical.Object(detail...))
	if err != nil {
		return internal(err, "The action could not be recorded.")
	}
	actor := sc.Subject()
	return s.audit.Append(ctx, port.AuditRecord{
		ID: auditID, TenantID: sc.Tenant(), ActorUserID: &actor, Action: action,
		SubjectType: "customer_authorization", SubjectID: subjectID, Detail: body, RecordedAt: s.now(),
	})
}

func (s *LegalService) inTx(ctx context.Context, fn func(context.Context) error) error {
	tx, txCtx, err := s.tx.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(txCtx) }()
	if err := fn(txCtx); err != nil {
		return err
	}
	return tx.Commit(txCtx)
}
