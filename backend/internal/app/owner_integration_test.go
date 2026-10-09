//go:build integration

package app_test

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zoikogroup/zoikotax/backend/internal/adapter/postgres"
)

// ownerPool connects as the database login itself, without assuming
// ztax_app: the schema owner, whom neither the append-only grants nor the
// row-level-security policies bind. Tests use it for what only a DBA could
// do — plant a fixture row, tamper with a stored one, count every tenant's
// outbox rows — and never for what the application does.
func ownerPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("ZTAX_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ZTAX_TEST_DATABASE_URL is not set")
	}
	cfg := postgres.DefaultConfig(dsn)
	cfg.Role = ""
	pool, err := postgres.Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}
