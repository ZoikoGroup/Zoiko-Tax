//go:build integration

package postgres_test

import (
	"context"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zoikogroup/zoikotax/backend/internal/adapter/postgres"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/accumulator"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/identity"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
)

// ADR-0004 against PostgreSQL. The property tests in internal/domain/accumulator
// prove the arithmetic composes for commits that arrive one at a time; these
// prove the part only the database can — that they do arrive one at a time.
//
//   - concurrent commits on one key serialize: every transaction observes a
//     distinct predecessor, the log is dense, the total is the sum, and the
//     threshold fires once (§2.2, §2.6);
//   - a retried contribution is not double counted, concurrently or not, and
//     the transaction that met it stays usable (§2.4);
//   - commits naming the same keys in opposite orders do not deadlock (§2.3);
//   - a held lock degrades as a timed, retryable error (control 4);
//   - the grants are what migration 000007 says (ADR-0008 §2.8).
//
// Tier 3 (ADR-0018 §2.4). Skipped without ZTAX_TEST_DATABASE_URL, and run by
// `make test-integration` against a migrated database.

const accCurrency fiscal.Currency = "XTS"

type accCell struct {
	pool   *pgxpool.Pool
	store  *postgres.Store
	repo   *postgres.AccumulatorRepo
	tenant id.TenantID
	ctx    context.Context
}

func openAccumulators(t *testing.T) *accCell {
	t.Helper()
	dsn := os.Getenv("ZTAX_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ZTAX_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)

	cfg := postgres.DefaultConfig(dsn)
	cfg.MaxConns = 24
	pool, err := postgres.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(pool.Close)
	store := postgres.NewStore(pool)
	return &accCell{pool: pool, store: store, repo: store.Accumulators(), ctx: ctx, tenant: newTenant(t, ctx, store)}
}

// newTenant creates a tenant of its own, so tests share a database without
// sharing any rows.
func newTenant(t *testing.T, ctx context.Context, store *postgres.Store) id.TenantID {
	t.Helper()
	tenant := id.NewTenantID(uuid.Must(uuid.NewV7()))
	if err := store.Tenants().Create(ctx, identity.Tenant{
		ID: tenant, Slug: "acc-" + strings.ReplaceAll(tenant.String(), "-", ""),
		DisplayName: "Accumulator integration", ResidencyRegion: "local", HomeCell: "local-dev",
		Status: identity.TenantActive, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("tenant: %v", err)
	}
	return tenant
}

func (c *accCell) scoped() context.Context { return security.Into(c.ctx, security.System(c.tenant)) }

// decision records a committed tax_decision row for a contribution to name.
// contribution_event's foreign key to it is deferred to commit (migration
// 000007), so it must exist by then; writing it up front keeps these tests
// about the accumulator rather than about the decision index.
func (c *accCell) decision(t *testing.T) id.DecisionID {
	t.Helper()
	d := id.NewDecisionID(uuid.Must(uuid.NewV7()))
	digest := "zt1:" + strings.Repeat("0", 64)
	now := time.Now().UTC()
	_, err := ownerPool(t).Exec(c.ctx, `
		INSERT INTO ztax.tax_decision (
			tenant_id, decision_id, business_key, valid_from, recorded_at, event_time, outcome,
			bundle_id, bundle_digest, ir_version, canon_profile,
			train_app, train_content, train_ai, train_adapter, train_infra, train_schema, train_migration,
			input_digest, input_canonical, trace, envelope_digest, result_digest)
		VALUES ($1, $2, 'acc-test', $3, $3, $3, 'AUTHORITATIVE',
			'b', $4, 1, 'zt1', 't', 't', 't', 't', 't', 't', 't',
			$4, '{}'::jsonb, '[]'::jsonb, $4, $4)`,
		c.tenant.UUID(), d.UUID(), now, digest)
	if err != nil {
		t.Fatalf("decision: %v", err)
	}
	return d
}

func accMoney(t *testing.T, s string) fiscal.Money {
	t.Helper()
	m, err := fiscal.ParseMoney(s, accCurrency)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

type committed struct {
	applied   bool
	observed  int64 // the predecessor's last_seq, as the locked snapshot showed it
	seq       int64
	crossings []accumulator.Crossing
}

// commit is one ADR-0004 §2.2 transaction, contributing amount from decision
// to every key in refs, in the order the use case in W2 lane I will perform
// it. It is spelled out here, not shared with internal/app, because wiring it
// into the commit path is that lane's work; what this file proves is that the
// repository makes the sequence safe.
func (c *accCell) commit(repo *postgres.AccumulatorRepo, refs []accumulator.Ref, decision id.DecisionID, amount fiscal.Money, thresholds []accumulator.Threshold) ([]committed, error) {
	ctx := c.scoped()
	tx, txCtx, err := c.store.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(txCtx) }()

	snaps, err := repo.LockAll(txCtx, refs)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	var out []committed
	for _, snap := range snaps {
		contribution := accumulator.Contribution{
			Key: snap.Key, SourceDecisionID: decision, Amount: amount, EventTime: now, RecordedAt: now,
		}
		next, crossings, err := accumulator.Apply(snap, contribution, thresholds)
		if err != nil {
			return nil, err
		}
		applied, err := repo.AppendContribution(txCtx, accumulator.Event{Contribution: contribution, Seq: next.LastSeq})
		if err != nil {
			return nil, err
		}
		if !applied {
			// §2.4: success-already-applied. Read the original and return it;
			// the transaction is still usable because the constraint answered
			// with DO NOTHING rather than an abort.
			prior, err := repo.Contribution(txCtx, snap.Key, decision)
			if err != nil {
				return nil, err
			}
			out = append(out, committed{applied: false, observed: snap.LastSeq, seq: prior.Seq})
			continue
		}
		if err := repo.SaveSnapshot(txCtx, next); err != nil {
			return nil, err
		}
		for _, x := range crossings {
			if err := repo.AppendCrossing(txCtx, x); err != nil {
				return nil, err
			}
		}
		out = append(out, committed{applied: true, observed: snap.LastSeq, seq: next.LastSeq, crossings: crossings})
	}
	return out, tx.Commit(txCtx)
}

func accRef(t *testing.T, name string) accumulator.Ref {
	t.Helper()
	k, err := accumulator.ParseKey(name + "." + uuid.Must(uuid.NewV7()).String())
	if err != nil {
		t.Fatal(err)
	}
	return accumulator.Ref{Key: k, Currency: accCurrency}
}

func TestIntegrationConcurrentCommitsOnOneKeySerialize(t *testing.T) {
	c := openAccumulators(t)
	ref := accRef(t, "serialize")
	cap50 := []accumulator.Threshold{{ID: "cap", Limit: accMoney(t, "50.00"), Comparison: accumulator.AtOrAbove}}

	const callers = 12
	ten := accMoney(t, "10.00")
	decisions := make([]id.DecisionID, callers)
	for i := range decisions {
		decisions[i] = c.decision(t)
	}

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		observed []int64
		emitted  int
		failures []error
	)
	start := make(chan struct{})
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			res, err := c.commit(c.repo, []accumulator.Ref{ref}, decisions[i], ten, cap50)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failures = append(failures, err)
				return
			}
			observed = append(observed, res[0].observed)
			emitted += len(res[0].crossings)
		}()
	}
	close(start)
	wg.Wait()
	if len(failures) > 0 {
		t.Fatalf("concurrent commits failed: %v", failures)
	}

	// Serialization, stated directly: every transaction saw a different
	// predecessor, and together they saw every one. Two that both saw seq 3
	// would be two commits each told they were below a cap they jointly
	// exceeded — the failure ADR-0004 §1 exists to prevent.
	slices.Sort(observed)
	for i, seq := range observed {
		if seq != int64(i) {
			t.Fatalf("transactions observed predecessors %v; each must see a distinct one", observed)
		}
	}
	if emitted != 1 {
		t.Fatalf("the cap was announced %d times by %d serialized commits", emitted, callers)
	}

	ctx := c.scoped()
	obs, err := c.repo.ReadUnlocked(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if obs.Total.String() != "120.00" || obs.ObservedSeq != callers {
		t.Fatalf("total %s at seq %d after %d contributions of 10.00", obs.Total, obs.ObservedSeq, callers)
	}

	events, err := c.repo.Events(ctx, ref.Key)
	if err != nil {
		t.Fatal(err)
	}
	crossings, err := c.repo.Crossings(ctx, ref.Key)
	if err != nil {
		t.Fatal(err)
	}
	if len(crossings) != 1 || crossings[0].Seq != 5 || crossings[0].Before.String() != "40.00" || crossings[0].After.String() != "50.00" {
		t.Fatalf("crossings %+v; want one, at the fifth contribution, from 40.00 to 50.00", crossings)
	}

	// §2.5 against the stored rows: the snapshot is the log, summarised.
	rebuilt, err := accumulator.Rebuild(ref, events, crossings)
	if err != nil {
		t.Fatal(err)
	}
	tx, txCtx, err := c.store.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(txCtx) }()
	locked, err := c.repo.LockAll(txCtx, []accumulator.Ref{ref})
	if err != nil {
		t.Fatal(err)
	}
	s := locked[0]
	if s.Total.String() != rebuilt.Total.String() || s.LastSeq != rebuilt.LastSeq ||
		!slices.Equal(s.Crossed, rebuilt.Crossed) || !s.UpdatedAt.Equal(rebuilt.UpdatedAt) {
		t.Fatalf("stored snapshot %+v differs from its rebuild %+v", s, rebuilt)
	}
}

func TestIntegrationRetriedContributionIsNotDoubleCounted(t *testing.T) {
	c := openAccumulators(t)
	ref := accRef(t, "retry")
	d := c.decision(t)

	first, err := c.commit(c.repo, []accumulator.Ref{ref}, d, accMoney(t, "25.00"), nil)
	if err != nil || !first[0].applied {
		t.Fatalf("first commit: %+v, %v", first, err)
	}
	again, err := c.commit(c.repo, []accumulator.Ref{ref}, d, accMoney(t, "25.00"), nil)
	if err != nil {
		t.Fatalf("a retry must succeed as already-applied, not fail: %v", err)
	}
	if again[0].applied || again[0].seq != first[0].seq {
		t.Fatalf("retry %+v; want already-applied, naming seq %d", again[0], first[0].seq)
	}

	// Concurrent retries of the same decision on a fresh key: exactly one
	// applies, however the locks are granted.
	fresh := accRef(t, "retry-race")
	d2 := c.decision(t)
	quarter := accMoney(t, "25.00")
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		applied  int
		failures []error
	)
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := c.commit(c.repo, []accumulator.Ref{fresh}, d2, quarter, nil)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failures = append(failures, err)
			} else if res[0].applied {
				applied++
			}
		}()
	}
	wg.Wait()
	if len(failures) > 0 || applied != 1 {
		t.Fatalf("%d of 6 concurrent retries applied (failures %v); exactly one may", applied, failures)
	}

	ctx := c.scoped()
	for _, r := range []accumulator.Ref{ref, fresh} {
		obs, err := c.repo.ReadUnlocked(ctx, r)
		if err != nil {
			t.Fatal(err)
		}
		events, err := c.repo.Events(ctx, r.Key)
		if err != nil {
			t.Fatal(err)
		}
		if obs.Total.String() != "25.00" || obs.ObservedSeq != 1 || len(events) != 1 {
			t.Fatalf("%s: total %s at seq %d with %d events; a retried decision counted twice", r.Key, obs.Total, obs.ObservedSeq, len(events))
		}
	}
}

func TestIntegrationOppositeKeyOrdersDoNotDeadlock(t *testing.T) {
	// ADR-0004 §2.3. Half the commits name (a, b), half (b, a). Without the
	// canonical order this is the textbook deadlock; with it, PostgreSQL
	// never has to detect one.
	c := openAccumulators(t)
	a, b := accRef(t, "a"), accRef(t, "b")

	const perOrder = 8
	one := accMoney(t, "1")
	decisions := make([]id.DecisionID, 2*perOrder)
	for i := range decisions {
		decisions[i] = c.decision(t)
	}
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		failures []error
	)
	start := make(chan struct{})
	for i, d := range decisions {
		refs := []accumulator.Ref{a, b}
		if i%2 == 1 {
			refs = []accumulator.Ref{b, a}
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, err := c.commit(c.repo, refs, d, one, nil); err != nil {
				mu.Lock()
				failures = append(failures, err)
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()
	if len(failures) > 0 {
		t.Fatalf("commits over the same keys in opposite orders failed: %v", failures)
	}
	for _, r := range []accumulator.Ref{a, b} {
		obs, err := c.repo.ReadUnlocked(c.scoped(), r)
		if err != nil {
			t.Fatal(err)
		}
		if obs.ObservedSeq != int64(len(decisions)) {
			t.Fatalf("%s at seq %d after %d commits", r.Key, obs.ObservedSeq, len(decisions))
		}
	}
}

func TestIntegrationHeldLockTimesOut(t *testing.T) {
	// Control 4: a pathological key degrades as a timed error rather than a
	// hang, and the error is the retryable kind — the commit was not applied.
	c := openAccumulators(t)
	ref := accRef(t, "held")

	holder, holderCtx, err := c.store.Begin(c.scoped())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback(holderCtx) }()
	if _, err := c.repo.LockAll(holderCtx, []accumulator.Ref{ref}); err != nil {
		t.Fatal(err)
	}

	impatient := c.repo.WithLockTimeout(200 * time.Millisecond)
	began := time.Now()
	_, err = c.commit(impatient, []accumulator.Ref{ref}, c.decision(t), accMoney(t, "1"), nil)
	waited := time.Since(began)
	if !errs.IsCategory(err, errs.CategoryUnavailable) {
		t.Fatalf("a commit behind a held lock returned %v; want a retryable timeout", err)
	}
	if waited > 2*time.Second {
		t.Fatalf("waited %s behind a 200ms lock_timeout", waited)
	}
}

func TestIntegrationLockAllRefusesAnotherCurrency(t *testing.T) {
	c := openAccumulators(t)
	ref := accRef(t, "currency")
	if _, err := c.commit(c.repo, []accumulator.Ref{ref}, c.decision(t), accMoney(t, "1"), nil); err != nil {
		t.Fatal(err)
	}
	other := accumulator.Ref{Key: ref.Key, Currency: "XXX"}
	tx, txCtx, err := c.store.Begin(c.scoped())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(txCtx) }()
	if _, err := c.repo.LockAll(txCtx, []accumulator.Ref{other}); errs.ReasonOf(err) != errs.ReasonCurrencyMismatch {
		t.Fatalf("locked an XTS accumulator as XXX: %v", err)
	}
	if _, err := c.repo.LockAll(c.scoped(), []accumulator.Ref{ref}); err == nil {
		t.Fatal("LockAll outside a transaction succeeded; its locks would already be released")
	}
}

func TestIntegrationAccumulatorsAreTenantScoped(t *testing.T) {
	c := openAccumulators(t)
	ref := accRef(t, "tenant")
	if _, err := c.commit(c.repo, []accumulator.Ref{ref}, c.decision(t), accMoney(t, "7.00"), nil); err != nil {
		t.Fatal(err)
	}
	other := security.Into(c.ctx, security.System(newTenant(t, c.ctx, c.store)))
	obs, err := c.repo.ReadUnlocked(other, ref)
	if err != nil {
		t.Fatal(err)
	}
	if obs.ObservedSeq != 0 || !obs.Total.IsZero() {
		t.Fatalf("another tenant sees %s at seq %d under the same key", obs.Total, obs.ObservedSeq)
	}
}

func TestIntegrationAccumulatorGrants(t *testing.T) {
	// ADR-0008 §2.8 and ADR-0003 control 1, asserted against the catalogue
	// rather than read from the migration: the log and the crossings are
	// insert-only for the application role, and the snapshot is updatable in
	// exactly the four derived columns and never deletable.
	c := openAccumulators(t)
	for _, tc := range []struct {
		table, column, privilege string
		want                     bool
	}{
		{"contribution_event", "", "INSERT", true},
		{"contribution_event", "", "SELECT", true},
		{"contribution_event", "", "UPDATE", false},
		{"contribution_event", "", "DELETE", false},
		{"threshold_crossing", "", "INSERT", true},
		{"threshold_crossing", "", "UPDATE", false},
		{"threshold_crossing", "", "DELETE", false},
		{"accumulator_snapshot", "", "INSERT", true},
		{"accumulator_snapshot", "", "DELETE", false},
		{"accumulator_snapshot", "running_total", "UPDATE", true},
		{"accumulator_snapshot", "last_seq", "UPDATE", true},
		{"accumulator_snapshot", "crossed_thresholds", "UPDATE", true},
		{"accumulator_snapshot", "updated_at", "UPDATE", true},
		{"accumulator_snapshot", "currency", "UPDATE", false},
		{"accumulator_snapshot", "accumulator_key", "UPDATE", false},
		{"accumulator_snapshot", "tenant_id", "UPDATE", false},
	} {
		var got bool
		var err error
		if tc.column == "" {
			err = c.pool.QueryRow(c.ctx, `SELECT has_table_privilege('ztax_app', $1, $2)`,
				"ztax."+tc.table, tc.privilege).Scan(&got)
		} else {
			err = c.pool.QueryRow(c.ctx, `SELECT has_column_privilege('ztax_app', $1, $2, $3)`,
				"ztax."+tc.table, tc.column, tc.privilege).Scan(&got)
		}
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("ztax_app %s on %s %s = %v, want %v", tc.privilege, tc.table, tc.column, got, tc.want)
		}
	}
}
