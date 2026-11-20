//go:build integration

package postgres_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/adapter/postgres"
)

// Migration 000038 against a real PostgreSQL: the regulatory change feed,
// its assessments and its recommendations are append-only to the
// application and isolated per tenant.
func TestIntegrationRegulatoryChangeTablesAreAppendOnly(t *testing.T) {
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
	for _, table := range []string{"regulatory_change", "regulatory_change_event", "impact_assessment",
		"review_recommendation", "review_recommendation_event"} {
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
