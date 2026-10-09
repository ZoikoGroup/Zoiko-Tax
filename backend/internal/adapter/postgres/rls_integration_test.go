//go:build integration

package postgres_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zoikogroup/zoikotax/backend/internal/adapter/postgres"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/identity"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
)

// ZTAX-SEC-REQ-0027: the database itself refuses another tenant's rows.
// These statements are written by hand, through the application's own role,
// and name the other tenant outright — exactly the query a repository bug
// would issue — so what stops them is the policy, not a predicate.
func TestIntegrationSECREQ0027RowLevelSecurityIsolatesTenants(t *testing.T) {
	dsn := os.Getenv("ZTAX_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ZTAX_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	app, err := postgres.Open(ctx, postgres.DefaultConfig(dsn))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	store := postgres.NewStore(app)

	// Two tenants, each with a legal entity, made through the adapter.
	tenants := make([]id.TenantID, 2)
	for i := range tenants {
		tenants[i] = id.NewTenantID(uuid.Must(uuid.NewV7()))
		if err := store.Tenants().Create(ctx, identity.Tenant{
			ID: tenants[i], Slug: "rls-" + strings.ReplaceAll(tenants[i].String(), "-", ""), DisplayName: "RLS",
			ResidencyRegion: "local", HomeCell: "local-dev", Status: identity.TenantActive, CreatedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatal(err)
		}
		if err := store.LegalEntities().Create(security.Into(ctx, security.System(tenants[i])), identity.LegalEntity{
			ID: id.NewLegalEntityID(uuid.Must(uuid.NewV7())), TenantID: tenants[i], Name: "RLS Ltd", Default: true, CreatedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	a, b := tenants[0], tenants[1]

	count := func(tenantSetting, scopeSetting string, of id.TenantID) int {
		t.Helper()
		tx, err := app.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, `SELECT set_config('ztax.tenant_id', $1, true), set_config('ztax.scope', $2, true)`,
			tenantSetting, scopeSetting); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM ztax.legal_entity WHERE tenant_id = $1`, of.UUID()).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(a.String(), "", a); n != 1 {
		t.Fatalf("tenant A sees %d of its own legal entities", n)
	}
	if n := count(a.String(), "", b); n != 0 {
		t.Fatalf("tenant A, naming tenant B outright, sees %d of B's legal entities", n)
	}
	if n := count("", "", a); n != 0 {
		t.Fatalf("a statement with no scope sees %d rows; it must see none", n)
	}
	if n := count("", "cell", b); n != 1 {
		t.Fatalf("the cell-wide scope sees %d of B's rows", n)
	}

	// And it cannot write one either.
	tx, err := app.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT set_config('ztax.tenant_id', $1, true)`, a.String()); err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO ztax.legal_entity (tenant_id, legal_entity_id, name, is_default, created_at)
		VALUES ($1, $2, 'planted', false, now())`, b.UUID(), uuid.Must(uuid.NewV7()))
	if err == nil || !strings.Contains(err.Error(), "row-level security") {
		t.Fatalf("tenant A planted a row in tenant B: %v", err)
	}
}

// The application's role holds no privilege the policies do not bind: it
// owns no table and cannot bypass row-level security.
func TestIntegrationTheAppRoleCannotBypassRowLevelSecurity(t *testing.T) {
	dsn := os.Getenv("ZTAX_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ZTAX_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var bypass bool
	var owned int
	if err := pool.QueryRow(ctx, `SELECT rolbypassrls FROM pg_roles WHERE rolname = 'ztax_app'`).Scan(&bypass); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_tables WHERE schemaname = 'ztax' AND tableowner = 'ztax_app'`).Scan(&owned); err != nil {
		t.Fatal(err)
	}
	if bypass || owned != 0 {
		t.Fatalf("ztax_app bypasses RLS=%v and owns %d tables", bypass, owned)
	}
	var unprotected []string
	rows, err := pool.Query(ctx, `
		SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		JOIN pg_attribute a ON a.attrelid = c.oid AND a.attname = 'tenant_id' AND NOT a.attisdropped
		WHERE n.nspname = 'ztax' AND c.relkind = 'r' AND NOT c.relrowsecurity`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		unprotected = append(unprotected, name)
	}
	rows.Close()
	if len(unprotected) > 0 {
		t.Fatalf("tenant tables without row-level security: %v — a new table needs its policy in the migration that creates it", unprotected)
	}
}
