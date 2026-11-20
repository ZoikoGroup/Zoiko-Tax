//go:build integration

package postgres_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/adapter/postgres"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/classification"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/identity"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
)

// A reviewer decision is saved once per proposal per tenant, read back for the
// same tenant only, and has no UPDATE or DELETE path (migration 000035).
func TestIntegrationClassificationDecisionIsAppendOnlyAndTenantScoped(t *testing.T) {
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

	tenants := make([]id.TenantID, 2)
	for i := range tenants {
		tenants[i] = id.NewTenantID(uuid.Must(uuid.NewV7()))
		if err := store.Tenants().Create(ctx, identity.Tenant{
			ID: tenants[i], Slug: "cd-" + strings.ReplaceAll(tenants[i].String(), "-", ""), DisplayName: "CD",
			ResidencyRegion: "local", HomeCell: "local-dev", Status: identity.TenantActive, CreatedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	a, b := tenants[0], tenants[1]
	ctxA, ctxB := security.Into(ctx, security.System(a)), security.Into(ctx, security.System(b))
	repo := store.ClassificationDecisions()

	d := classification.ReviewDecision{
		TenantID: a, ProposalID: uuid.Must(uuid.NewV7()), SubjectRef: "sku:PLAN-UNL-5G",
		ProposedCode: "ontology:telecom/voice/mobile", Action: classification.ActionModify, Reason: "The plan is data only",
		Note: "checked", ModifiedCode: "ontology:telecom/data/mobile", EvidenceID: "call-id-1",
		ReviewerID: id.NewUserID(uuid.Must(uuid.NewV7())), DecidedAt: time.Now().UTC().Truncate(time.Microsecond),
	}
	if err := repo.Create(ctxA, d); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := repo.Decision(ctxA, d.ProposalID)
	if err != nil || got != d {
		t.Fatalf("read back: %+v, %v; want %+v", got, err, d)
	}
	if err := repo.Create(ctxA, d); errs.CategoryOf(err) != errs.CategoryConflict {
		t.Fatalf("a second decision for the same proposal: %v, want a conflict", err)
	}
	if _, err := repo.Decision(ctxB, d.ProposalID); errs.CategoryOf(err) != errs.CategoryNotFound {
		t.Fatalf("another tenant read the decision: %v, want not found", err)
	}

	// The application role holds no UPDATE or DELETE on the table.
	tx, err := app.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT set_config('ztax.tenant_id', $1, true)`, a.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE ztax.classification_decision SET reason = 'changed' WHERE tenant_id = $1`, a.UUID()); err == nil ||
		!strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("an UPDATE was not refused: %v", err)
	}
}
