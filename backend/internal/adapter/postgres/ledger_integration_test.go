//go:build integration

package postgres_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/adapter/postgres"
)

// Migrations 000011 and 000012 against a real PostgreSQL: posted journals,
// legal entities, obligation rows and their contributions are append-only to
// the application (ZTAX-FIN-REQ-0036, -0038; ADR-0003 §2.2).
func TestIntegrationLedgerTablesAreAppendOnly(t *testing.T) {
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
	for _, table := range []string{"legal_entity", "tcsl_journal", "tcsl_journal_line", "obligation", "obligation_contribution"} {
		for privilege, want := range map[string]bool{
			"SELECT": true, "INSERT": true, "UPDATE": false, "DELETE": false, "TRUNCATE": false,
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
