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
	"github.com/zoikogroup/zoikotax/backend/internal/domain/entitlement"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/identity"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/subscription"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// Grants and plans round-trip through migration 000039, are tenant-scoped
// and append-only; and a meter's count and digest, computed in SQL, are the
// ones subscription.BasisDigest gives over the listed rows.
func TestIntegrationEntitlementsAndUsage(t *testing.T) {
	dsn := os.Getenv("ZTAX_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ZTAX_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := postgres.Open(ctx, postgres.DefaultConfig(dsn))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store := postgres.NewStore(pool)

	tenants := make([]id.TenantID, 2)
	entities := make([]id.LegalEntityID, 2)
	for i := range tenants {
		tenants[i] = id.NewTenantID(uuid.Must(uuid.NewV7()))
		if err := store.Tenants().Create(ctx, identity.Tenant{
			ID: tenants[i], Slug: "ent-" + strings.ReplaceAll(tenants[i].String(), "-", ""), DisplayName: "ENT",
			ResidencyRegion: "local", HomeCell: "local-dev", Status: identity.TenantActive, CreatedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatal(err)
		}
		entities[i] = id.NewLegalEntityID(uuid.Must(uuid.NewV7()))
		if err := store.LegalEntities().Create(security.Into(ctx, security.System(tenants[i])), identity.LegalEntity{
			ID: entities[i], TenantID: tenants[i], Name: "ENT Ltd", Default: true, CreatedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	ctxA, ctxB := security.Into(ctx, security.System(tenants[0])), security.Into(ctx, security.System(tenants[1]))
	repo := store.Entitlements()
	now := time.Now().UTC().Truncate(time.Microsecond)
	maker, checker := id.NewUserID(uuid.Must(uuid.NewV7())), id.NewUserID(uuid.Must(uuid.NewV7()))

	// A grant: created, submitted, approved by a second person.
	g := entitlement.Grant{
		ID: id.NewEntitlementID(uuid.Must(uuid.NewV7())), TenantID: tenants[0], LegalEntity: entities[0], Jurisdiction: "GB",
		Service: "MOBILE_VOICE", Mode: entitlement.ModeNative, Operation: entitlement.OpFile, EffectiveFrom: entitlement.Day(now),
		ContractRef: "MSA-1", ContractRevision: "rev 2", IntakeKey: "k-" + uuid.NewString(), CreatedAt: now, CreatedBy: maker,
	}
	if g.IntakeHash, err = canonical.Sum(g.Payload(true)); err != nil {
		t.Fatal(err)
	}
	events, err := entitlement.Open(g, true, maker, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateGrant(ctxA, g, events); err != nil {
		t.Fatalf("create grant: %v", err)
	}
	got, err := repo.Grant(ctxA, g.ID)
	if err != nil || got.Lifecycle != entitlement.LifecycleInReview || got.ContractRevision != "rev 2" || !got.IntakeHash.Equal(g.IntakeHash) {
		t.Fatalf("read back: %+v %v", got, err)
	}
	e, err := got.Apply(entitlement.Act{Action: entitlement.ActApprove, ExpectedVersion: 2, Reason: "signed"}, nil, checker, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.AppendGrantEvents(ctxA, g.ID, []entitlement.Event{e}); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if err := repo.AppendGrantEvents(ctxA, g.ID, []entitlement.Event{e}); errs.CategoryOf(err) != errs.CategoryConflict {
		t.Fatalf("a second event at the same seq: %v", err)
	}
	if got, _ = repo.Grant(ctxA, g.ID); got.StatusOn(entitlement.Day(now)) != entitlement.StatusActive {
		t.Fatalf("approved grant reads %s", got.StatusOn(entitlement.Day(now)))
	}
	if _, found, err := repo.GrantByIntakeKey(ctxA, g.IntakeKey); err != nil || !found {
		t.Fatalf("by intake key: %v %v", found, err)
	}
	if _, err := repo.Grant(ctxB, g.ID); errs.CategoryOf(err) != errs.CategoryNotFound {
		t.Fatalf("another tenant read the grant: %v", err)
	}

	// A plan with two quotas.
	p := subscription.Plan{
		ID: id.NewSubscriptionPlanID(uuid.Must(uuid.NewV7())), TenantID: tenants[0], PlanCode: "PILOT", PlanName: "Pilot",
		ContractRef: "MSA-1", TermStart: entitlement.Day(now), IntakeKey: "p-" + uuid.NewString(), CreatedAt: now, CreatedBy: maker,
		Quotas: []subscription.Quota{
			{Meter: subscription.MeterCommittedDecisions, Included: 2, Overage: subscription.OverageAllowed},
			{Meter: subscription.MeterShadowRuns, Included: 0, Overage: subscription.OverageHardLimit},
		},
	}
	if p.IntakeHash, err = canonical.Sum(p.Payload()); err != nil {
		t.Fatal(err)
	}
	pe, err := subscription.Open(p, maker, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CreatePlan(ctxA, p, pe); err != nil {
		t.Fatalf("create plan: %v", err)
	}
	gp, err := repo.Plan(ctxA, p.ID)
	if err != nil || len(gp.Quotas) != 2 || gp.Status != subscription.StatusProposed {
		t.Fatalf("plan read back: %+v %v", gp, err)
	}
	ae, err := gp.Apply(subscription.ActApprove, 1, "", checker, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.AppendPlanEvent(ctxA, p.ID, ae); err != nil {
		t.Fatalf("approve plan: %v", err)
	}
	if plans, err := repo.Plans(ctxA, 10); err != nil || len(plans) != 1 || plans[0].Status != subscription.StatusApproved {
		t.Fatalf("plans: %+v %v", plans, err)
	}

	// Usage: three decisions for tenant A in a far-past month, one for B.
	month := time.Date(2001, 3, 1, 0, 0, 0, 0, time.UTC)
	owner := ownerPool(t)
	var want []string
	insert := func(tenant id.TenantID, at time.Time) string {
		d := uuid.Must(uuid.NewV7())
		digest := "zt1:" + strings.Repeat("0", 64)
		if _, err := owner.Exec(ctx, `
			INSERT INTO ztax.tax_decision (
				tenant_id, decision_id, business_key, valid_from, recorded_at, event_time, outcome,
				bundle_id, bundle_digest, ir_version, canon_profile,
				train_app, train_content, train_ai, train_adapter, train_infra, train_schema, train_migration,
				input_digest, input_canonical, trace, envelope_digest, result_digest)
			VALUES ($1, $2, 'ent-test', $3, $3, $3, 'AUTHORITATIVE',
				'b', $4, 1, 'zt1', 't', 't', 't', 't', 't', 't', 't',
				$4, '{}'::jsonb, '[]'::jsonb, $4, $4)`, tenant.UUID(), d, at, digest); err != nil {
			t.Fatalf("decision: %v", err)
		}
		return d.String()
	}
	for i := range 3 {
		want = append(want, insert(tenants[0], month.Add(time.Duration(i+1)*time.Hour)))
	}
	insert(tenants[0], month.AddDate(0, 1, 0)) // next month: not counted
	insert(tenants[1], month.Add(time.Hour))   // other tenant: not counted
	usage := store.Usage()
	c, rows, err := usage.Basis(ctxA, subscription.MeterCommittedDecisions, month, month.AddDate(0, 1, 0), 2)
	if err != nil {
		t.Fatal(err)
	}
	if c.Count != 3 || c.Digest != subscription.BasisDigest(want) || len(rows) != 2 {
		t.Fatalf("basis: %+v rows %d; want digest %s", c, len(rows), subscription.BasisDigest(want))
	}
	if rows[0].ID > rows[1].ID {
		t.Fatalf("rows not in digest order: %v", rows)
	}
	counts, err := usage.Counts(ctxA, month, month.AddDate(0, 1, 0))
	if err != nil || len(counts) != len(subscription.Definitions) {
		t.Fatalf("counts: %v %v", counts, err)
	}
	for _, c := range counts {
		if c.Meter != subscription.MeterCommittedDecisions && (c.Count != 0 || c.Digest != subscription.BasisDigest(nil)) {
			t.Errorf("%s: %+v", c.Meter, c)
		}
	}

	// The application role holds no UPDATE or DELETE on the registers.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT set_config('ztax.tenant_id', $1, true)`, tenants[0].String()); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE ztax.entitlement_grant SET contract_ref = 'x' WHERE tenant_id = $1`, tenants[0].UUID()); err == nil ||
		!strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("an UPDATE was not refused: %v", err)
	}
}
