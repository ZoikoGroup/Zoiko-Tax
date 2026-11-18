//go:build integration

package postgres_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/adapter/postgres"
)

// Migration 000034 against a real PostgreSQL: the ERP bridge's records —
// mappings, sealed batches, their delivery events, the ERP's reported
// balances and the reconciliations — are append-only to the application.
func TestIntegrationERPTablesAreAppendOnly(t *testing.T) {
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
	for _, table := range []string{"erp_bridge", "erp_account_mapping", "erp_account_mapping_line", "erp_export_batch",
		"erp_export_line", "erp_export_event", "erp_gl_snapshot", "erp_gl_snapshot_line", "erp_gl_reconciliation",
		"erp_gl_reconciliation_item"} {
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
		var rls bool
		if err := pool.QueryRow(ctx, `SELECT relrowsecurity FROM pg_class WHERE oid = $1::regclass`, "ztax."+table).Scan(&rls); err != nil {
			t.Fatal(err)
		}
		if !rls {
			t.Errorf("ztax.%s has no row-level security", table)
		}
	}
}
