package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Schema is the named schema this cell's tables live in. ADR-0008 §2.9 pins
// search_path to named schemas so that a role-configuration change cannot
// silently redirect a query.
const Schema = "ztax"

// SearchPath is what every connection runs with.
//
// public trails ztax because PostGIS installs its types there and relocating
// the extension is not something PostGIS supports cleanly. The application role
// holds USAGE on public and not CREATE (migration 000001), so public can supply
// the geography type and cannot receive a table — which is the guarantee §2.9
// is protecting. Order matters: ztax leads, so an unqualified name resolves to
// an application table rather than to anything an extension happens to have
// called the same thing.
const SearchPath = Schema + ", public"

// Config is what the pool needs. It is built from the process configuration and
// never carries a secret in a field that outlives the call: the DSN is resolved
// from the credential reference at startup (ADR-0017 §2.4) and handed here once.
type Config struct {
	// DSN is the resolved connection string.
	DSN string
	// MaxConns bounds the pool.
	MaxConns int32
	// StatementTimeout bounds any one statement. ADR-0008 §2.10 sets it
	// explicitly so that a pathological query degrades as a timed error rather
	// than as an occupied connection.
	StatementTimeout time.Duration
	// LockTimeout bounds waiting for a lock. The accumulator pattern
	// (ADR-0004 §2.2) takes row locks in the commit path, and a request that
	// cannot get one should fail fast rather than hold a connection.
	LockTimeout time.Duration
	// IdleInTransactionTimeout bounds an open transaction doing nothing, which
	// is how a leaked transaction blocks vacuum and, eventually, everything.
	IdleInTransactionTimeout time.Duration
}

// DefaultConfig returns the settings a cell runs with. They are deliberately
// modest: a cell is one region's workload, not the whole estate's.
func DefaultConfig(dsn string) Config {
	return Config{
		DSN:                      dsn,
		MaxConns:                 16,
		StatementTimeout:         10 * time.Second,
		LockTimeout:              3 * time.Second,
		IdleInTransactionTimeout: 15 * time.Second,
	}
}

// Open builds the pool.
//
// The AfterConnect hook is where ADR-0008 §2.4 lands: every connection, not
// just the first, registers the NUMERIC mapping. A pool that registered on one
// connection would work in development and narrow a fiscal amount the first
// time it grew a second connection under load, which is the worst possible
// place to discover it.
func Open(ctx context.Context, cfg Config) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("postgres: parse dsn: %w", err)
	}
	poolCfg.MaxConns = cfg.MaxConns

	// Session settings travel as connection parameters rather than as SET
	// statements, so they are established before the first query rather than by
	// one, and a connection cannot be handed out in a half-configured state.
	if poolCfg.ConnConfig.RuntimeParams == nil {
		poolCfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	poolCfg.ConnConfig.RuntimeParams["search_path"] = SearchPath
	poolCfg.ConnConfig.RuntimeParams["statement_timeout"] = millis(cfg.StatementTimeout)
	poolCfg.ConnConfig.RuntimeParams["lock_timeout"] = millis(cfg.LockTimeout)
	poolCfg.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] = millis(cfg.IdleInTransactionTimeout)

	poolCfg.AfterConnect = func(_ context.Context, conn *pgx.Conn) error {
		RegisterTypes(conn)
		return nil
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("postgres: open pool: %w", err)
	}
	return pool, nil
}

// textNumericCodec is pgx's NUMERIC codec pinned to the text wire format.
//
// This exists because of a defect the conformance test found, and it is worth
// recording precisely, because the failure is invisible in every value except
// one.
//
// PostgreSQL's binary NUMERIC representation carries its display scale in a
// dscale field, and the server sets it correctly for 0.00. pgx's binary decoder
// short-circuits when the value has no significant digits — which is only true
// of zero — and returns exponent 0, so 0.00 comes back as 0. Every non-zero
// value round-trips exactly; zero loses its scale.
//
// That matters here more than it would elsewhere. Scale is semantic
// (ADR-0011 §2.2): 0.00 and 0 are different assertions about precision and
// digest differently. A zero-tax line read back from the database would
// therefore canonicalize differently from the one that was written, and the
// decision would fail to replay against its own evidence — the exact guarantee
// C3 exists to provide, broken by the one value most likely to appear in a
// zero-rated or exempt line.
//
// The text format carries the scale in the digits themselves, so it has nowhere
// to lose it. The cost is parsing decimal text instead of a packed binary form,
// on a type where correctness is worth more than the microseconds.
type textNumericCodec struct{ pgtype.NumericCodec }

// FormatSupported refuses binary, which is what makes the server send text.
func (textNumericCodec) FormatSupported(format int16) bool {
	return format == pgtype.TextFormatCode
}

// PreferredFormat asks for text.
func (textNumericCodec) PreferredFormat() int16 { return pgtype.TextFormatCode }

// RegisterTypes installs the estate's type mappings on a connection.
//
// It is exported so the conformance test can register against a bare
// connection and assert the mapping directly, rather than asserting it through
// the pool and hoping the pool is the reason it worked.
func RegisterTypes(conn *pgx.Conn) {
	m := conn.TypeMap()

	// No default mapping is trusted (ADR-0008 §2.3): the codec is replaced
	// outright rather than relied on.
	m.RegisterType(&pgtype.Type{
		Name:  "numeric",
		OID:   pgtype.NumericOID,
		Codec: textNumericCodec{},
	})
	m.RegisterType(&pgtype.Type{
		Name:  "_numeric",
		OID:   pgtype.NumericArrayOID,
		Codec: &pgtype.ArrayCodec{ElementType: &pgtype.Type{Name: "numeric", OID: pgtype.NumericOID, Codec: textNumericCodec{}}},
	})

	// Numeric implements pgtype.NumericValuer and pgtype.NumericScanner, so
	// this makes it the default Go representation of every NUMERIC column.
	m.RegisterDefaultPgType(Numeric{}, "numeric")
	m.RegisterDefaultPgType(&Numeric{}, "numeric")
}

func millis(d time.Duration) string {
	return fmt.Sprintf("%d", d.Milliseconds())
}
