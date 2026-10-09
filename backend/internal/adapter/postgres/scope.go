package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
)

// Data scope and row-level security (migration 000019; ZTAX-SEC-REQ-0027).
//
// Every statement a repository runs carries a tenant_id predicate. The
// database also refuses to show or accept another tenant's row: each tenant
// table has a policy keyed on two session settings, ztax.tenant_id and
// ztax.scope. This file is what sets them, before every statement, from the
// context the statement runs under:
//
//   - a security context's tenant — every request, and system work scoped to
//     one tenant;
//   - an explicit tenant, for the few lookups that know their tenant before
//     anyone is authenticated (sign-in by email, a tenant by id);
//   - the cell-wide scope, which a caller takes by name with CellScope — the
//     outbox relay, the webhook dispatcher, the batch claim, the session
//     lookup by token digest — because their work is the whole cell's;
//   - nothing, which the policies turn into no rows at all.
//
// The settings are transaction-local inside a transaction, and set per
// statement on a pooled connection outside one, and cleared when the
// connection returns to the pool — so no setting outlives the statement or
// transaction it was made for.

const sqlSetScope = `SELECT set_config('ztax.tenant_id', $1, $3), set_config('ztax.scope', $2, $3)`

// scopeKey carries an explicit scope on a context.
type scopeKey struct{}

// dataScope is what the session settings are set to.
type dataScope struct {
	tenant string
	cell   bool
}

// CellScope marks ctx for work across every tenant in the cell. It is for
// the cell's own machinery, and each use names why at its call site: a
// statement under it sees every tenant's rows, so a repository method that
// takes it filters by whatever it was asked for, not by tenant.
func CellScope(ctx context.Context) context.Context {
	return context.WithValue(ctx, scopeKey{}, dataScope{cell: true})
}

// tenantScope marks ctx for one tenant named explicitly, for a lookup made
// before there is a security context to carry it.
func tenantScope(ctx context.Context, t id.TenantID) context.Context {
	return context.WithValue(ctx, scopeKey{}, dataScope{tenant: t.String()})
}

// scopeOf is the scope statements under ctx run with: an explicit scope
// first, then the security context's tenant, then none.
func scopeOf(ctx context.Context) dataScope {
	if s, ok := ctx.Value(scopeKey{}).(dataScope); ok {
		return s
	}
	if t, ok := security.MustTenant(ctx); ok {
		return dataScope{tenant: t.String()}
	}
	return dataScope{}
}

func (s dataScope) args(local bool) []any {
	scope := ""
	if s.cell {
		scope = "cell"
	}
	return []any{s.tenant, scope, local}
}

// txScope is the transaction's querier: it brings the transaction-local
// settings in line with each statement's context, re-setting them only when
// the scope changes — a relay transaction claims cell-wide and then fans out
// as each event's tenant.
type txScope struct {
	tx      pgx.Tx
	applied *dataScope
}

func (q *txScope) ensure(ctx context.Context) error {
	want := scopeOf(ctx)
	if q.applied != nil && *q.applied == want {
		return nil
	}
	if _, err := q.tx.Exec(ctx, sqlSetScope, want.args(true)...); err != nil {
		return err
	}
	q.applied = &want
	return nil
}

func (q *txScope) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if err := q.ensure(ctx); err != nil {
		return nil, err
	}
	return q.tx.Query(ctx, sql, args...)
}

func (q *txScope) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if err := q.ensure(ctx); err != nil {
		return errRow{err}
	}
	return q.tx.QueryRow(ctx, sql, args...)
}

func (q *txScope) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if err := q.ensure(ctx); err != nil {
		return pgconn.CommandTag{}, err
	}
	return q.tx.Exec(ctx, sql, args...)
}

// poolScope runs each statement on a connection of its own, with the
// session settings set for that statement first. The connection is released
// when the statement is done with it: after Exec, after a row is scanned,
// after rows are closed.
type poolScope struct{ pool *pgxpool.Pool }

func (q poolScope) acquire(ctx context.Context) (*pgxpool.Conn, error) {
	conn, err := q.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := conn.Exec(ctx, sqlSetScope, scopeOf(ctx).args(false)...); err != nil {
		conn.Release()
		return nil, err
	}
	return conn, nil
}

func (q poolScope) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	conn, err := q.acquire(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := conn.Query(ctx, sql, args...)
	if err != nil {
		conn.Release()
		return nil, err
	}
	return &releasingRows{Rows: rows, conn: conn}, nil
}

func (q poolScope) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	conn, err := q.acquire(ctx)
	if err != nil {
		return errRow{err}
	}
	return &releasingRow{row: conn.QueryRow(ctx, sql, args...), conn: conn}
}

func (q poolScope) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	conn, err := q.acquire(ctx)
	if err != nil {
		return pgconn.CommandTag{}, err
	}
	defer conn.Release()
	return conn.Exec(ctx, sql, args...)
}

// releasingRows returns its connection to the pool when closed.
type releasingRows struct {
	pgx.Rows
	conn     *pgxpool.Conn
	released bool
}

func (r *releasingRows) Close() {
	r.Rows.Close()
	if !r.released {
		r.released = true
		r.conn.Release()
	}
}

func (r *releasingRows) Next() bool {
	if r.Rows.Next() {
		return true
	}
	// Exhausted rows close themselves in pgx; release with them, so a caller
	// that reads to the end and forgets Close does not hold a connection.
	r.Close()
	return false
}

// releasingRow returns its connection to the pool once scanned.
type releasingRow struct {
	row  pgx.Row
	conn *pgxpool.Conn
}

func (r *releasingRow) Scan(dest ...any) error {
	defer r.conn.Release()
	return r.row.Scan(dest...)
}

// errRow is a row whose statement never ran.
type errRow struct{ err error }

func (r errRow) Scan(...any) error { return r.err }

// clearScope is the pool's AfterRelease hook: a connection goes back with no
// scope, so nothing that bypasses this file inherits the last one.
func clearScope(conn *pgx.Conn) bool {
	_, err := conn.Exec(context.Background(), sqlSetScope, "", "", false)
	return err == nil
}
