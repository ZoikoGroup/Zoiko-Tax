//go:build integration

package postgres_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/adapter/postgres"
)

// Migration 000009 against a real PostgreSQL: boundary datasets are reference
// content the application reads and never writes (ZTAX-JUR-REQ-0005).
func TestIntegrationBoundaryDatasetsAreReadOnlyToTheApplication(t *testing.T) {
	dsn := os.Getenv("ZTAX_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ZTAX_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := postgres.Open(ctx, postgres.DefaultConfig(dsn))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer pool.Close()

	for _, table := range []string{"boundary_dataset", "jurisdiction_boundary"} {
		for privilege, want := range map[string]bool{
			"SELECT": true, "INSERT": false, "UPDATE": false, "DELETE": false, "TRUNCATE": false,
		} {
			var got bool
			if err := pool.QueryRow(ctx, `SELECT has_table_privilege('ztax_app', $1, $2)`, "ztax."+table, privilege).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Errorf("ztax_app %s on %s = %v, want %v", privilege, table, got, want)
			}
		}
	}
}
