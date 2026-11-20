//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

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

// The row-level-security policy itself, not the repository's own query, keeps
// one tenant's decisions from another. Every statement here is raw SQL run as
// the application role inside a transaction that is rolled back, so no
// repository code can be the thing that refuses.
func TestIntegrationClassificationDecisionPolicyBlocksOtherTenants(t *testing.T) {
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
			ID: tenants[i], Slug: "cp-" + strings.ReplaceAll(tenants[i].String(), "-", ""), DisplayName: "CP",
			ResidencyRegion: "local", HomeCell: "local-dev", Status: identity.TenantActive, CreatedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	a, b := tenants[0], tenants[1]

	// 1. A decision for tenant A, saved through the repository.
	d := classification.ReviewDecision{
		TenantID: a, ProposalID: uuid.Must(uuid.NewV7()), SubjectRef: "sku:PLAN-UNL-5G",
		ProposedCode: "ontology:telecom/voice/mobile", Action: classification.ActionAccept, Reason: "Matches the catalog",
		ReviewerID: id.NewUserID(uuid.Must(uuid.NewV7())), DecidedAt: time.Now().UTC().Truncate(time.Microsecond),
	}
	if err := store.ClassificationDecisions().Create(security.Into(ctx, security.System(a)), d); err != nil {
		t.Fatalf("create: %v", err)
	}

	// asApp runs fn in a transaction as the application role with the tenant
	// setting given ("" leaves it unset) and always rolls the transaction back.
	asApp := func(tenant string, fn func(tx pgx.Tx)) {
		t.Helper()
		tx, err := app.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if tenant != "" {
			if _, err := tx.Exec(ctx, `SELECT set_config('ztax.tenant_id', $1, true)`, tenant); err != nil {
				t.Fatal(err)
			}
		}
		fn(tx)
	}
	count := func(tx pgx.Tx, query string, args ...any) int {
		t.Helper()
		var n int
		if err := tx.QueryRow(ctx, query, args...).Scan(&n); err != nil {
			t.Fatalf("select: %v", err)
		}
		return n
	}
	const byProposal = `SELECT count(*) FROM ztax.classification_decision WHERE proposal_id = $1`
	const byTenant = `SELECT count(*) FROM ztax.classification_decision WHERE tenant_id = $1 AND proposal_id = $2`
	const insert = `INSERT INTO ztax.classification_decision
		(tenant_id, proposal_id, subject_ref, proposed_code, action, reason, reviewer_id, decided_at)
		VALUES ($1, $2, 'sku:X', 'ontology:a/b', 'ACCEPT', 'synthetic', $3, now())`
	refused := func(err error) bool {
		var pgErr *pgconn.PgError
		return errors.As(err, &pgErr) && pgErr.Code == "42501" && strings.Contains(pgErr.Message, "row-level security")
	}
	reviewer := uuid.Must(uuid.NewV7())

	// 2. Tenant B's setting: the row is invisible, by proposal id and by tenant id.
	asApp(b.String(), func(tx pgx.Tx) {
		if n := count(tx, byProposal, d.ProposalID); n != 0 {
			t.Fatalf("tenant B sees %d row(s) by proposal id, want 0", n)
		}
		if n := count(tx, byTenant, a.UUID(), d.ProposalID); n != 0 {
			t.Fatalf("tenant B sees %d row(s) by tenant A's id, want 0", n)
		}
	})
	// 3. Tenant B's setting cannot write a row carrying tenant A's id.
	asApp(b.String(), func(tx pgx.Tx) {
		if _, err := tx.Exec(ctx, insert, a.UUID(), uuid.Must(uuid.NewV7()), reviewer); !refused(err) {
			t.Fatalf("B inserting a row for A: %v, want a row-level security refusal (42501)", err)
		}
	})
	// 4. Tenant A's setting cannot write a row carrying tenant B's id.
	asApp(a.String(), func(tx pgx.Tx) {
		if _, err := tx.Exec(ctx, insert, b.UUID(), uuid.Must(uuid.NewV7()), reviewer); !refused(err) {
			t.Fatalf("A inserting a row for B: %v, want a row-level security refusal (42501)", err)
		}
	})
	// 5. No tenant setting at all: nothing is visible.
	asApp("", func(tx pgx.Tx) {
		if n := count(tx, byProposal, d.ProposalID); n != 0 {
			t.Fatalf("with no tenant setting %d row(s) are visible, want 0", n)
		}
	})
	// 6. Tenant A's setting sees the row, so the checks above can see data when they should.
	asApp(a.String(), func(tx pgx.Tx) {
		if n := count(tx, byProposal, d.ProposalID); n != 1 {
			t.Fatalf("tenant A sees %d row(s), want 1", n)
		}
	})
}
