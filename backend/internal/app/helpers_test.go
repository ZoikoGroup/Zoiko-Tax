package app_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/content/bundle"
	"github.com/zoikogroup/zoikotax/backend/internal/content/compile"
	"github.com/zoikogroup/zoikotax/backend/internal/content/dsl"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/evidence"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/rule"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/fiscaltest"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// The worked pack, compiled from its committed source by the same compiler and
// bundle encoder ztax-contentc uses. The decision type the W1 gate names is
// this pack's evaluator output, so the tests run the real content rather than a
// fixture shaped to pass.
const workedPack = "../../../content/packs/eu-vat/eu-vat.ztax"

func compileManifest(t testing.TB, source string) rule.Manifest {
	t.Helper()
	prog, err := dsl.Parse(filepath.Base(workedPack), source)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	m, err := compile.Compile(prog)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	raw, err := bundle.EncodeManifest(m)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	decoded, digest, err := bundle.DecodeManifest(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	decoded.Digest = digest.String()
	return decoded
}

func workedBundle(t testing.TB) *rule.Bundle {
	t.Helper()
	src, err := os.ReadFile(workedPack)
	if err != nil {
		t.Fatalf("read the worked pack: %v", err)
	}
	b, err := rule.Load(compileManifest(t, string(src)))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return b
}

var trains = evidence.Trains{App: "0.0.0-test", Content: "2026.09.1", AI: "0", Adapter: "0",
	Infra: "0", Schema: "1.3.1", Migration: "5"}

var tenantID = id.NewTenantID(uuid.MustParse("01920000-0000-7000-8000-0000000000aa"))

func as(roles ...security.Role) context.Context {
	sc := security.New(tenantID, id.NewUserID(uuid.MustParse("01920000-0000-7000-8000-0000000000bb")),
		id.NewSessionID(uuid.MustParse("01920000-0000-7000-8000-0000000000cc")), roles, time.Now())
	return security.Into(context.Background(), sc)
}

func system() context.Context {
	return security.Into(context.Background(), security.System(tenantID))
}

// lineInput is one line for the worked pack: a net amount, whether the reduced
// rate applies, whether an exemption is held, and a unit count for the levy.
func lineInput(t testing.TB, net string, units string, reduced bool) evidence.Input {
	t.Helper()
	q, err := fiscal.ParseQuantity(units, "EA")
	if err != nil {
		t.Fatal(err)
	}
	return evidence.Input{
		Money:      map[string]fiscal.Money{"line.netAmount": fiscaltest.Money(t, net, "EUR")},
		Quantities: map[string]fiscal.Quantity{"line.quantity": q},
		Flags:      map[string]bool{"line.reducedRateApplies": reduced, "line.exemptCertificateHeld": false},
	}
}

func readSet(t testing.TB, ytd string) map[string]fiscal.Money {
	t.Helper()
	return map[string]fiscal.Money{"threshold.ecoLevyYtd": fiscaltest.Money(t, ytd, "EUR")}
}

// settableClock is a clock a test moves explicitly.
type settableClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *settableClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *settableClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

func (c *settableClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// ---------------------------------------------------------------------------
// in-memory ports. Each keeps the one property the real adapter guarantees
// that a test would otherwise be able to cheat: the store verifies digests on
// read, the repositories are tenant-scoped and append-only.
// ---------------------------------------------------------------------------

type memStore struct {
	mu sync.Mutex
	m  map[string][]byte
}

var _ port.EvidenceStore = (*memStore)(nil)

func newMemStore() *memStore { return &memStore{m: map[string][]byte{}} }

func (s *memStore) key(ctx context.Context, d canonical.Digest) (string, error) {
	t, ok := security.MustTenant(ctx)
	if !ok {
		return "", errs.New(errs.CategoryPolicy, errs.ReasonUnauthenticated, "no tenant")
	}
	return t.String() + "/" + d.String(), nil
}

func (s *memStore) Put(ctx context.Context, data []byte) (canonical.Digest, error) {
	d := canonical.SumBytes(data)
	k, err := s.key(ctx, d)
	if err != nil {
		return canonical.Digest{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.m[k]; !ok {
		s.m[k] = append([]byte(nil), data...)
	}
	return d, nil
}

func (s *memStore) Get(ctx context.Context, d canonical.Digest) ([]byte, error) {
	k, err := s.key(ctx, d)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.m[k]
	if !ok {
		return nil, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "no such object")
	}
	if !canonical.SumBytes(data).Equal(d) {
		return nil, errs.New(errs.CategoryInternal, errs.ReasonEvidenceIntegrity, "object does not hash to its key")
	}
	return append([]byte(nil), data...), nil
}

// tamper overwrites an object in place, which the real store makes impossible
// through its interface — the point is to prove the reader notices.
func (s *memStore) tamper(d canonical.Digest, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[tenantID.String()+"/"+d.String()] = data
}

type memDecisions struct {
	mu   sync.Mutex
	rows []evidence.Record
}

var _ port.DecisionRepository = (*memDecisions)(nil)

func (r *memDecisions) Append(ctx context.Context, rec evidence.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, x := range r.rows {
		if x.DecisionID == rec.DecisionID {
			return errs.New(errs.CategoryConflict, errs.ReasonAlreadyExists, "duplicate")
		}
		if rec.Supersedes != nil && x.Supersedes != nil && *x.Supersedes == *rec.Supersedes {
			return errs.New(errs.CategoryConflict, errs.ReasonAlreadyExists, "second successor")
		}
	}
	r.rows = append(r.rows, rec)
	return nil
}

func (r *memDecisions) ByID(_ context.Context, decisionID id.DecisionID) (evidence.Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, x := range r.rows {
		if x.DecisionID == decisionID {
			return x, nil
		}
	}
	return evidence.Record{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "no such decision")
}

func (r *memDecisions) AsOf(_ context.Context, key string, decisionTime, eventTime time.Time) (evidence.Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var best *evidence.Record
	for i, x := range r.rows {
		if x.BusinessKey == key && !x.RecordedAt.After(decisionTime) && !x.ValidFrom.After(eventTime) {
			if best == nil || x.RecordedAt.After(best.RecordedAt) {
				best = &r.rows[i]
			}
		}
	}
	if best == nil {
		return evidence.Record{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "none")
	}
	return *best, nil
}

func (r *memDecisions) History(_ context.Context, key string) ([]evidence.Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []evidence.Record
	for _, x := range r.rows {
		if x.BusinessKey == key {
			out = append(out, x)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RecordedAt.Before(out[j].RecordedAt) })
	return out, nil
}

func (r *memDecisions) SealLeaves(_ context.Context, from, to time.Time) ([]evidence.SealLeaf, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []evidence.SealLeaf
	for _, x := range r.rows {
		if !x.RecordedAt.Before(from) && x.RecordedAt.Before(to) {
			out = append(out, x.Leaf())
		}
	}
	evidence.OrderLeaves(out)
	return out, nil
}

// rewrite changes a stored row, which the real repository cannot do.
func (r *memDecisions) rewrite(decisionID id.DecisionID, f func(*evidence.Record)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.rows {
		if r.rows[i].DecisionID == decisionID {
			f(&r.rows[i])
		}
	}
}

type memSeals struct {
	mu   sync.Mutex
	rows []evidence.SealRecord
}

var _ port.SealRepository = (*memSeals)(nil)

func (r *memSeals) LockSealing(context.Context) error { return nil }

func (r *memSeals) Overlapping(_ context.Context, from, to time.Time) ([]evidence.SealRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []evidence.SealRecord
	for _, x := range r.rows {
		if x.PeriodStart.Before(to) && x.PeriodEnd.After(from) {
			out = append(out, x)
		}
	}
	return out, nil
}

func (r *memSeals) Append(_ context.Context, rec evidence.SealRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows = append(r.rows, rec)
	return nil
}

func (r *memSeals) ByID(_ context.Context, sealID id.SealID) (evidence.SealRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, x := range r.rows {
		if x.SealID == sealID {
			return x, nil
		}
	}
	return evidence.SealRecord{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "no such seal")
}

func (r *memSeals) rewrite(sealID id.SealID, f func(*evidence.SealRecord)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.rows {
		if r.rows[i].SealID == sealID {
			f(&r.rows[i])
		}
	}
}

type noTx struct{}

func (noTx) Begin(ctx context.Context) (port.Tx, context.Context, error) { return noTx{}, ctx, nil }
func (noTx) Commit(context.Context) error                                { return nil }
func (noTx) Rollback(context.Context) error                              { return nil }

// harness is a determination service over in-memory ports with the worked
// pack active.
type harness struct {
	svc       *app.DeterminationService
	holder    *rule.Holder
	library   *rule.Library
	store     *memStore
	decisions *memDecisions
	clock     *settableClock
}

func newHarness(t testing.TB) *harness {
	t.Helper()
	h := &harness{
		holder:    &rule.Holder{},
		library:   &rule.Library{},
		store:     newMemStore(),
		decisions: &memDecisions{},
		clock:     &settableClock{t: time.Date(2026, 9, 25, 9, 0, 0, 0, time.UTC)},
	}
	b := workedBundle(t)
	h.holder.Publish(b)
	h.library.Add(b)
	h.svc = app.NewDeterminationService(h.holder, h.library, h.decisions, h.store, noTx{}, h.clock, &idgen.Sequential{}, trains)
	return h
}

func (h *harness) determine(t testing.TB, key string) evidence.Decision {
	t.Helper()
	d, err := h.svc.Determine(as(security.RoleOperator), app.DetermineInput{
		BusinessKey:  key,
		EventTime:    time.Date(2026, 9, 24, 18, 0, 0, 0, time.UTC),
		Input:        lineInput(t, "100.00", "3", false),
		Accumulators: readSet(t, "9999.99"),
	})
	if err != nil {
		t.Fatalf("determine %s: %v", key, err)
	}
	return d
}

func emitted(d evidence.Decision, slot string) string {
	v, ok := d.Result.Emitted[slot]
	if !ok {
		return "(absent)"
	}
	return fmt.Sprintf("%s %s", v.Money.CanonicalString(), v.Money.Currency())
}
