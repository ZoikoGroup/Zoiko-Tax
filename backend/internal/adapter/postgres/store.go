package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// A note on sqlc.
//
// ADR-0008 §2.3 has SQL written by hand and sqlc generating the Go from it, so
// that the artifact under review is the .sql file. The queries below are
// hand-written and the Go around them is too — the generator is not wired up
// yet, and that is a registered gap rather than a decision. What the ADR is
// protecting is preserved in the meantime: every query is a named const holding
// literal SQL, so a reviewer reads SQL rather than a builder expression whose
// emitted plan they have to imagine, and no query is assembled from fragments.
//
// The rule that survives regardless: every statement here carries a tenant_id
// predicate taken from the security context, never from a parameter a caller
// supplies (ADR-0012 §2.7, ADR-0008 control 4).

// querier is the subset of pgx both a pool and a transaction provide. Repos
// depend on it so that the same method works inside or outside a transaction
// without a second code path.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Store owns the pool and hands out repositories.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore wraps a pool.
func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// Pool exposes the underlying pool for health checks and for the outbox relay,
// which claims rows outside any request's transaction.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// txKey carries a transaction on a context.
type txKey struct{}

// db returns the transaction on ctx if there is one, and the pool otherwise.
//
// This is what lets a repository method be called from inside a use case's
// transaction and from outside it without the caller choosing. The alternative
// — passing a handle through every signature — puts the transaction in the
// domain's field of view, which ADR-0009 §2.4 rules out.
func (s *Store) db(ctx context.Context) querier {
	if tx, ok := ctx.Value(txKey{}).(pgx.Tx); ok {
		return tx
	}
	return s.pool
}

// Begin opens a transaction and returns a context carrying it.
//
// ADR-0008 §2.10 leaves isolation at READ COMMITTED, with explicit locks where
// a stronger guarantee is required — the accumulator pattern takes a row lock
// (ADR-0004 §2.2) rather than escalating the whole transaction.
func (s *Store) Begin(ctx context.Context) (port.Tx, context.Context, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, ctx, mapError(err, "begin transaction")
	}
	return &pgTx{tx: tx}, context.WithValue(ctx, txKey{}, tx), nil
}

type pgTx struct {
	tx   pgx.Tx
	done bool
}

func (t *pgTx) Commit(ctx context.Context) error {
	if t.done {
		return nil
	}
	t.done = true
	if err := t.tx.Commit(ctx); err != nil {
		return mapError(err, "commit")
	}
	return nil
}

// Rollback after a commit is a no-op, so `defer tx.Rollback(ctx)` is the
// correct idiom and needs no flag at the call site.
func (t *pgTx) Rollback(ctx context.Context) error {
	if t.done {
		return nil
	}
	t.done = true
	if err := t.tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		return mapError(err, "rollback")
	}
	return nil
}

// tenantOf extracts the tenant predicate for a query.
//
// Every read and write in this package starts here. A caller without a security
// context gets a policy error rather than a query with no tenant predicate,
// which is the failure mode ADR-0008 control 4 exists to prevent.
func tenantOf(ctx context.Context) (id.TenantID, error) {
	t, ok := security.MustTenant(ctx)
	if !ok {
		return id.TenantID{}, errs.New(errs.CategoryPolicy, errs.ReasonUnauthenticated,
			"The request reached the data layer with no tenant in scope.")
	}
	return t, nil
}

// PostgreSQL error codes worth distinguishing. Matching on the code rather than
// on the message is the same discipline ADR-0016 §2.7 applies to our own errors.
const (
	codeUniqueViolation       = "23505"
	codeForeignKeyViolation   = "23503"
	codeCheckViolation        = "23514"
	codeInsufficientPrivilege = "42501"
	codeLockNotAvailable      = "55P03"
	codeSerializationFailure  = "40001"
	codeDeadlockDetected      = "40P01"
	codeQueryCanceled         = "57014"
)

// mapError turns a driver error into the estate's taxonomy (ADR-0016 §2.3).
//
// The distinction that matters most is which failures are CategoryUnavailable:
// ADR-0013 §2.7 deletes a PENDING idempotency record for a transient failure
// and records a terminal FAILED for a deterministic one, so classifying a lock
// timeout as permanent would leave a client unable to ever complete a
// legitimate request with that key.
func mapError(err error, op string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return errs.Wrap(err, errs.CategoryNotFound, errs.ReasonNotFound,
			"The referenced resource does not exist, or is outside the caller's tenant.")
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case codeUniqueViolation:
			return errs.Wrap(err, errs.CategoryConflict, errs.ReasonAlreadyExists,
				"A resource with that identity already exists.")
		case codeForeignKeyViolation:
			return errs.Wrap(err, errs.CategoryValidation, errs.ReasonInvalidValue,
				"The request referenced something that does not exist.")
		case codeCheckViolation:
			return errs.Wrap(err, errs.CategoryValidation, errs.ReasonInvalidValue,
				"A field carried a value outside its permitted domain.")
		case codeInsufficientPrivilege:
			// The append-only grants of ADR-0003 §2.1 refusing an UPDATE or a
			// DELETE. That is a defect in us, not something the caller did, and
			// it should page rather than look like a validation error.
			return errs.Wrap(err, errs.CategoryInternal, errs.ReasonInternal,
				"The service attempted an operation its database role does not permit.")
		case codeLockNotAvailable, codeSerializationFailure, codeDeadlockDetected, codeQueryCanceled:
			return errs.Wrap(err, errs.CategoryUnavailable, errs.ReasonUnavailable,
				"The request could not be completed in time. It was not applied and may be retried unchanged.")
		}
	}

	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return errs.Wrap(err, errs.CategoryUnavailable, errs.ReasonUnavailable,
			"The request timed out. It was not applied and may be retried unchanged.")
	}

	return errs.Wrap(fmt.Errorf("postgres: %s: %w", op, err),
		errs.CategoryUnavailable, errs.ReasonDatabaseUnavailable,
		"The cell database was unreachable. The request was not applied and may be retried unchanged.")
}
