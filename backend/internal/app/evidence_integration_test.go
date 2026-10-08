//go:build integration

package app_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	adapterevidence "github.com/zoikogroup/zoikotax/backend/internal/adapter/evidence"
	"github.com/zoikogroup/zoikotax/backend/internal/adapter/postgres"
	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/evidence"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/identity"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/rule"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
)

// Tier 3 (ADR-0018 §2.4): the W1 exit criterion against the real persistence
// path — PostgreSQL for the index, the filesystem object store for the
// evidence, the real migrations for the schema. The unit tests prove the logic;
// this proves nothing between the logic and the disk changes a byte.

type cell struct {
	pool      *pgxpool.Pool
	store     *postgres.Store
	evidence  *adapterevidence.FileStore
	tenant    id.TenantID
	ctx       context.Context
	clock     *settableClock
	decisions *app.DeterminationService
	sealer    *app.SealService
}

func openCell(t *testing.T) *cell {
	t.Helper()
	dsn := os.Getenv("ZTAX_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ZTAX_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	pool, err := postgres.Open(ctx, postgres.DefaultConfig(dsn))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(pool.Close)
	store := postgres.NewStore(pool)

	// A tenant of its own per test, so tests share a database without sharing
	// any rows — every read below is tenant-scoped, which is also the point.
	tenant := id.NewTenantID(uuid.Must(uuid.NewV7()))
	if err := store.Tenants().Create(ctx, identity.Tenant{
		ID: tenant, Slug: "evidence-" + strings.ReplaceAll(tenant.String(), "-", ""),
		DisplayName: "Evidence integration", ResidencyRegion: "local", HomeCell: "local-dev",
		Status: identity.TenantActive, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("tenant: %v", err)
	}

	files, err := adapterevidence.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	holder, library := &rule.Holder{}, &rule.Library{}
	b := workedBundle(t)
	holder.Publish(b)
	library.Add(b)

	clk := &settableClock{t: time.Now().UTC().Add(-72 * time.Hour).Truncate(time.Hour)}
	signer, ring := sealKey(t, "evidence-seal-integration")

	c := &cell{
		pool: pool, store: store, evidence: files, tenant: tenant, clock: clk,
		ctx: security.Into(ctx, security.System(tenant)),
	}
	c.decisions = app.NewDeterminationService(holder, library, store.Decisions(), files, store, clk, idgen.V7{}, trains)
	c.sealer = app.NewSealService(store.Decisions(), store.Seals(), files, store, signer, ring, clk, idgen.V7{}, "local-dev", 0)
	return c
}

func (c *cell) determine(t *testing.T, key string, supersedes *id.DecisionID, reduced bool) evidence.Decision {
	t.Helper()
	d, err := c.decisions.Determine(c.ctx, app.DetermineInput{
		BusinessKey:  key,
		Supersedes:   supersedes,
		EventTime:    c.clock.Now().Add(-time.Hour),
		Input:        lineInput(t, "100.00", "3", reduced),
		Accumulators: readSet(t, "9999.99"),
	})
	if err != nil {
		t.Fatalf("determine: %v", err)
	}
	return d
}

func TestIntegrationDecisionReplaysExactlyThroughTheStore(t *testing.T) {
	c := openCell(t)
	d := c.determine(t, "line-1", nil, false)

	rec, err := c.store.Decisions().ByID(c.ctx, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Every instant and digest survives the database exactly — the NUMERIC
	// codec bug of 22 September was precisely a value that did not.
	if !rec.RecordedAt.Equal(d.Envelope.DecisionTime) || !rec.EnvelopeDigest.Equal(d.EnvelopeDigest) ||
		!rec.ResultDigest.Equal(d.ResultDigest) {
		t.Fatalf("the index did not round-trip: %+v", rec)
	}

	report, err := c.decisions.Replay(c.ctx, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Verdict != evidence.ReplayMatch {
		t.Fatalf("verdict %s: %s", report.Verdict, report.Divergence)
	}
}

// ADR-0003 §2.3 against the real query: history by ordering, no row closed.
func TestIntegrationAsOfReadsTheVersionCurrentAtDecisionTime(t *testing.T) {
	c := openCell(t)
	first := c.determine(t, "line-1", nil, false)
	c.clock.Advance(time.Hour)
	second := c.determine(t, "line-1", &first.ID, true)

	eventTime := first.Envelope.EventTime
	between := first.Envelope.DecisionTime.Add(time.Minute)
	got, err := c.store.Decisions().AsOf(c.ctx, "line-1", between, eventTime.Add(time.Hour))
	if err != nil || got.DecisionID != first.ID {
		t.Fatalf("as of %s: got %v, %v; want the original", between, got.DecisionID, err)
	}
	got, err = c.store.Decisions().AsOf(c.ctx, "line-1", second.Envelope.DecisionTime, second.Envelope.EventTime)
	if err != nil || got.DecisionID != second.ID {
		t.Fatalf("as of now: got %v, %v; want the correction", got.DecisionID, err)
	}

	history, err := c.store.Decisions().History(c.ctx, "line-1")
	if err != nil || len(history) != 2 || history[1].Supersedes == nil || *history[1].Supersedes != first.ID {
		t.Fatalf("history = %+v, %v", history, err)
	}

	// The unique index refuses a second successor even if the service's own
	// check were bypassed.
	c.clock.Advance(time.Hour)
	_, err = c.decisions.Determine(c.ctx, app.DetermineInput{
		BusinessKey: "line-1", Supersedes: &first.ID, EventTime: eventTime,
		Input: lineInput(t, "1.00", "1", false), Accumulators: readSet(t, "0.00"),
	})
	if !errs.IsCategory(err, errs.CategoryConflict) {
		t.Fatalf("a second successor: got %v, want a conflict", err)
	}
}

func TestIntegrationDecisionsAreTenantScoped(t *testing.T) {
	a, b := openCell(t), openCell(t)
	d := a.determine(t, "line-1", nil, false)
	if _, err := b.store.Decisions().ByID(b.ctx, d.ID); !errs.IsCategory(err, errs.CategoryNotFound) {
		t.Fatalf("tenant B read tenant A's decision: %v", err)
	}
}

// The period seal end to end, and ADR-0011 §5.1 control 4 against the real
// store: alter one sealed row and verification fails.
//
// The alteration is an UPDATE, which a cell's application role cannot issue —
// ADR-0003 §2.1 withholds the grant. The test connects as the database owner,
// which is what makes the tamper possible here and is exactly the actor the
// seal exists to catch.
func TestIntegrationSealDetectsATamperedRow(t *testing.T) {
	c := openCell(t)
	start := c.clock.Now()
	var ds []evidence.Decision
	for i := range 3 {
		c.clock.Advance(time.Minute)
		ds = append(ds, c.determine(t, "line-"+string(rune('a'+i)), nil, false))
	}
	end := c.clock.Now().Add(time.Minute)
	c.clock.Set(end.Add(app.DefaultSettleWindow + time.Minute))

	seal, err := c.sealer.SealPeriod(c.ctx, start, end)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if seal.LeafCount != 3 {
		t.Fatalf("sealed %d, want 3", seal.LeafCount)
	}
	v, err := c.sealer.VerifySeal(c.ctx, seal.SealID)
	if err != nil || v.Verdict != evidence.SealValid {
		t.Fatalf("fresh seal: %+v, %v", v, err)
	}

	if _, err := c.sealer.SealPeriod(c.ctx, start, end); !errs.IsCategory(err, errs.CategoryConflict) {
		t.Fatalf("resealing the same period: got %v, want a conflict", err)
	}

	forged := ds[0].ResultDigest.String()[:len(ds[0].ResultDigest.String())-1] + "0"
	if forged == ds[0].ResultDigest.String() {
		forged = forged[:len(forged)-1] + "1"
	}
	if _, err := ownerPool(t).Exec(c.ctx,
		`UPDATE ztax.tax_decision SET result_digest = $1 WHERE tenant_id = $2 AND decision_id = $3`,
		forged, c.tenant.UUID(), ds[1].ID.UUID()); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	v, err = c.sealer.VerifySeal(c.ctx, seal.SealID)
	if err != nil {
		t.Fatal(err)
	}
	if v.Verdict != evidence.SealRootMismatch {
		t.Fatalf("verdict %s after tampering, want ROOT_MISMATCH", v.Verdict)
	}
}
