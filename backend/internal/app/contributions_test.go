package app_test

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/contribution"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/identity"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
)

// memContributions is the contribution ledger in memory: append-only, and
// refusing a second record of a source key or a second reversal of an
// original, as the unique indexes of migration 000031 do.
type memContributions struct {
	mu     sync.Mutex
	rows   []contribution.Contribution
	events map[id.ContributionID][]contribution.StatusEvent
}

func (m *memContributions) Lock(context.Context, string) error { return nil }

func (m *memContributions) Record(_ context.Context, c contribution.Contribution, e contribution.StatusEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.rows {
		if r.Source == c.Source || (c.Reverses != nil && r.Reverses != nil && *r.Reverses == *c.Reverses) {
			return errs.New(errs.CategoryConflict, errs.ReasonAlreadyExists, "exists")
		}
	}
	if m.events == nil {
		m.events = map[id.ContributionID][]contribution.StatusEvent{}
	}
	m.rows = append(m.rows, c)
	m.events[c.ID] = []contribution.StatusEvent{e}
	return nil
}

func (m *memContributions) AppendEvent(_ context.Context, cid id.ContributionID, e contribution.StatusEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.events[cid])+1 != e.Seq {
		return errs.New(errs.CategoryConflict, errs.ReasonAlreadyExists, "seq")
	}
	m.events[cid] = append(m.events[cid], e)
	return nil
}

func (m *memContributions) folded(match func(contribution.Contribution) bool) []contribution.Contribution {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []contribution.Contribution
	for _, r := range m.rows {
		if !match(r) {
			continue
		}
		f, err := contribution.Fold(r, m.events[r.ID])
		if err != nil {
			panic(err)
		}
		out = append(out, f)
	}
	return out
}

func (m *memContributions) Contribution(_ context.Context, cid id.ContributionID) (contribution.Contribution, error) {
	got := m.folded(func(c contribution.Contribution) bool { return c.ID == cid })
	if len(got) == 0 {
		return contribution.Contribution{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "none")
	}
	return got[0], nil
}

func (m *memContributions) BySource(_ context.Context, k contribution.SourceKey) (contribution.Contribution, bool, error) {
	got := m.folded(func(c contribution.Contribution) bool { return c.Source == k })
	if len(got) == 0 {
		return contribution.Contribution{}, false, nil
	}
	return got[0], true, nil
}

func (m *memContributions) ReversalOf(_ context.Context, k contribution.SourceKey) (contribution.Contribution, bool, error) {
	got := m.folded(func(c contribution.Contribution) bool { return c.Reverses != nil && *c.Reverses == k })
	if len(got) == 0 {
		return contribution.Contribution{}, false, nil
	}
	return got[0], true, nil
}

func (m *memContributions) List(_ context.Context, f contribution.Filter) ([]contribution.Contribution, error) {
	got := m.folded(func(c contribution.Contribution) bool {
		return (f.Period == "" || c.Period == f.Period) && (f.Regime == "" || c.Regime == f.Regime) &&
			(f.SourceType == "" || c.SourceType == f.SourceType) && (f.LegalEntity.IsZero() || c.LegalEntity == f.LegalEntity)
	})
	if f.Status != "" {
		got = slices.DeleteFunc(got, func(c contribution.Contribution) bool { return c.Status != f.Status })
	}
	slices.Reverse(got)
	return got, nil
}

func (m *memContributions) InPeriod(_ context.Context, period string) ([]contribution.Contribution, error) {
	return m.folded(func(c contribution.Contribution) bool { return c.Period == period }), nil
}

func (m *memContributions) KnownSources(_ context.Context, keys []contribution.SourceKey) (map[contribution.SourceKey]bool, error) {
	out := map[contribution.SourceKey]bool{}
	for _, k := range keys {
		if len(m.folded(func(c contribution.Contribution) bool { return c.Source == k })) > 0 {
			out[k] = true
		}
	}
	return out, nil
}

type ctbEntities struct{ def identity.LegalEntity }

func (m ctbEntities) Create(context.Context, identity.LegalEntity) error { return nil }
func (m ctbEntities) Default(context.Context) (identity.LegalEntity, error) {
	return m.def, nil
}
func (m ctbEntities) ByID(_ context.Context, le id.LegalEntityID) (identity.LegalEntity, error) {
	if le != m.def.ID {
		return identity.LegalEntity{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "none")
	}
	return m.def, nil
}

var ctbEntity = identity.LegalEntity{
	ID: id.NewLegalEntityID(uuid.MustParse("01920000-0000-7000-8000-00000000e001")), TenantID: tenantID, Name: "Acme Telecom", Default: true,
}

func ctbPerson(user string, roles ...security.Role) context.Context {
	sc := security.New(tenantID, id.NewUserID(uuid.MustParse(user)),
		id.NewSessionID(uuid.MustParse("01920000-0000-7000-8000-0000000000cc")), roles, time.Now())
	return security.Into(context.Background(), sc)
}

var (
	ctbOperator = ctbPerson("01920000-0000-7000-8000-0000000000b1", security.RoleOperator)
	ctbAnalyst  = ctbPerson("01920000-0000-7000-8000-0000000000b2", security.RoleAnalyst)
	ctbAnalyst2 = ctbPerson("01920000-0000-7000-8000-0000000000b3", security.RoleAnalyst)
	ctbAdmin    = ctbPerson("01920000-0000-7000-8000-0000000000b4", security.RoleAdmin)
)

func newContributions(t *testing.T) (*app.ProviderContributionService, *memContributions, *settableClock) {
	t.Helper()
	store := &memContributions{}
	clk := &settableClock{t: time.Date(2026, 9, 25, 9, 0, 0, 0, time.UTC)}
	return app.NewProviderContributionService(store, ctbEntities{def: ctbEntity}, noTx{}, clk, &idgen.Sequential{}), store, clk
}

func usf(source, eligible, excluded string) app.ContributionInput {
	in := app.ContributionInput{
		SourceEventID: source, SourceVersion: 1, SourceType: contribution.SourceBSSInvoice, Regime: contribution.RegimeUSUSF,
		Period: "2026-09", ClassificationRef: "usf:interstate-telecom", SitusRef: "US", SourceRef: "INV-" + source,
		RuleVersion: "usf-499a@2026.1", Currency: "USD", EligibleBase: eligible, ExcludedBase: excluded,
		SourceChecksum: "sha256:9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
	}
	if excluded != "0.00" {
		in.ExclusionReason = "International-only revenue"
	}
	return in
}

// UAT-OBL-026: a redelivered event is one contribution; the same key with a
// different payload is a conflict.
func TestARedeliveredSourceEventIsOneContribution(t *testing.T) {
	svc, store, _ := newContributions(t)
	first, created, err := svc.Record(ctbOperator, usf("E-1", "1000.00", "0.00"))
	if err != nil || !created {
		t.Fatalf("record: created %v, %v", created, err)
	}
	if first.LegalEntity != ctbEntity.ID || first.Status != contribution.StatusAccepted {
		t.Fatalf("entity %s status %s", first.LegalEntity, first.Status)
	}
	again, created, err := svc.Record(ctbOperator, usf("E-1", "1000.00", "0.00"))
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("replay: created %v id %s, %v", created, again.ID, err)
	}
	if _, _, err := svc.Record(ctbOperator, usf("E-1", "999.00", "0.00")); errs.ReasonOf(err) != contribution.ReasonSourceEventConflict {
		t.Fatalf("a changed payload: %v", err)
	}
	if len(store.rows) != 1 {
		t.Fatalf("%d rows, want 1", len(store.rows))
	}
	// A new version of the source event is a new contribution.
	v2 := usf("E-1", "999.00", "0.00")
	v2.SourceVersion = 2
	if _, created, err := svc.Record(ctbOperator, v2); err != nil || !created {
		t.Fatalf("version 2: created %v, %v", created, err)
	}
}

// UAT-OBL-027: a reversal is a negative contribution linked to its
// original, recorded once, never an edit.
func TestAReversalIsANewNegativeContribution(t *testing.T) {
	svc, _, _ := newContributions(t)
	orig, _, err := svc.Record(ctbOperator, usf("E-1", "1000.00", "100.00"))
	if err != nil {
		t.Fatal(err)
	}
	in := usf("E-1-CR", "-1000.00", "-100.00")
	in.Reverses = &contribution.SourceKey{EventID: "E-1", Version: 1}
	rev, _, err := svc.Record(ctbOperator, in)
	if err != nil {
		t.Fatal(err)
	}
	trace, err := svc.Trace(ctbAnalyst, orig.ID)
	if err != nil {
		t.Fatal(err)
	}
	if trace.Reversal == nil || trace.Reversal.ID != rev.ID || trace.Contribution.EligibleBase.CanonicalString() != "1000.00" {
		t.Fatalf("the original's trace does not show its reversal, or the original changed: %+v", trace)
	}
	back, err := svc.Trace(ctbAnalyst, rev.ID)
	if err != nil || back.Original == nil || back.Original.ID != orig.ID {
		t.Fatalf("the reversal's trace does not show its original: %v", err)
	}
	again := usf("E-1-CR2", "-1.00", "0.00")
	again.Reverses = &contribution.SourceKey{EventID: "E-1", Version: 1}
	if _, _, err := svc.Record(ctbOperator, again); errs.ReasonOf(err) != errs.ReasonAlreadyExists {
		t.Fatalf("a second reversal: %v", err)
	}
	sum, err := svc.Summary(ctbAnalyst, "2026-09")
	if err != nil {
		t.Fatal(err)
	}
	if g := sum.Groups[0]; g.Eligible.CanonicalString() != "0.00" || g.Excluded.CanonicalString() != "0.00" || sum.DataIncomplete() {
		t.Fatalf("eligible %s excluded %s incomplete %v", g.Eligible.CanonicalString(), g.Excluded.CanonicalString(), g.Incomplete)
	}
}

// A reversal arriving before its original is recorded, and the period is
// DATA_INCOMPLETE until the original arrives.
func TestAnEarlyReversalLeavesThePeriodIncomplete(t *testing.T) {
	svc, _, _ := newContributions(t)
	in := usf("E-9-CR", "-50.00", "0.00")
	in.Reverses = &contribution.SourceKey{EventID: "E-9", Version: 1}
	if _, _, err := svc.Record(ctbOperator, in); err != nil {
		t.Fatal(err)
	}
	sum, err := svc.Summary(ctbAnalyst, "2026-09")
	if err != nil {
		t.Fatal(err)
	}
	if !sum.DataIncomplete() {
		t.Fatal("an unbalanced source was reported complete")
	}
	if _, _, err := svc.Record(ctbOperator, usf("E-9", "50.00", "0.00")); err != nil {
		t.Fatal(err)
	}
	if sum, _ = svc.Summary(ctbAnalyst, "2026-09"); sum.DataIncomplete() {
		t.Fatalf("still incomplete: %v", sum.Groups[0].Incomplete)
	}
}

// OBL-06: quarantine holds an incorrect import out of the totals without
// deleting it; release is by someone other than the person who held it.
func TestQuarantineHoldsWithoutDeleting(t *testing.T) {
	svc, store, _ := newContributions(t)
	good, _, _ := svc.Record(ctbOperator, usf("E-1", "100.00", "0.00"))
	bad, _, _ := svc.Record(ctbOperator, usf("E-2", "900.00", "0.00"))
	held, err := svc.Quarantine(ctbAnalyst, bad.ID, "File 7 imported twice under new ids")
	if err != nil || held.Status != contribution.StatusQuarantined {
		t.Fatalf("quarantine: %s, %v", held.Status, err)
	}
	sum, err := svc.Summary(ctbOperator, "2026-09")
	if err != nil {
		t.Fatal(err)
	}
	g := sum.Groups[0]
	if g.Eligible.CanonicalString() != "100.00" || g.Quarantined != 1 || !sum.DataIncomplete() {
		t.Fatalf("eligible %s quarantined %d incomplete %v", g.Eligible.CanonicalString(), g.Quarantined, g.Incomplete)
	}
	listed, err := svc.Contributions(ctbOperator, contribution.Filter{Status: contribution.StatusQuarantined})
	if err != nil || len(listed) != 1 || listed[0].ID != bad.ID || len(store.rows) != 2 {
		t.Fatalf("listed %d, rows %d, %v", len(listed), len(store.rows), err)
	}
	if _, err := svc.Release(ctbAnalyst, bad.ID, "checked"); errs.ReasonOf(err) != contribution.ReasonMakerChecker {
		t.Fatalf("self-release: %v", err)
	}
	if _, err := svc.Release(ctbOperator, bad.ID, "checked"); errs.ReasonOf(err) != errs.ReasonForbidden {
		t.Fatalf("an operator released: %v", err)
	}
	back, err := svc.Release(ctbAnalyst2, bad.ID, "Checked against the billing extract; the events are distinct")
	if err != nil || back.Status != contribution.StatusAccepted || len(back.History) != 3 {
		t.Fatalf("release: %v", err)
	}
	if sum, _ = svc.Summary(ctbOperator, "2026-09"); sum.Groups[0].Eligible.CanonicalString() != "1000.00" || sum.DataIncomplete() {
		t.Fatalf("after release: %s", sum.Groups[0].Eligible.CanonicalString())
	}
	_ = good
}

// ADMIN is on no fiscal read, and only an OPERATOR records.
func TestContributionRoles(t *testing.T) {
	svc, _, _ := newContributions(t)
	if _, err := svc.Summary(ctbAdmin, "2026-09"); errs.ReasonOf(err) != errs.ReasonForbidden {
		t.Fatalf("admin read the summary: %v", err)
	}
	if _, _, err := svc.Record(ctbAnalyst, usf("E-1", "1.00", "0.00")); errs.ReasonOf(err) != errs.ReasonForbidden {
		t.Fatalf("an ctbAnalyst recorded: %v", err)
	}
}

func TestAmountsAreCanonicalDecimalStrings(t *testing.T) {
	svc, _, clk := newContributions(t)
	for _, bad := range []string{"1e3", "1,000.00", "01.00", "+1.00", ""} {
		if _, _, err := svc.Record(ctbOperator, usf("E-"+bad, bad, "0.00")); errs.ReasonOf(err) != errs.ReasonInvalidValue {
			t.Errorf("%q: %v", bad, err)
		}
	}
	// A late event is recorded in its own period as an amendment.
	clk.Set(time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC))
	late, _, err := svc.Record(ctbOperator, usf("E-late", "0.10", "0.00"))
	if err != nil || !late.Amendment {
		t.Fatalf("late: amendment %v, %v", late.Amendment, err)
	}
	if !strings.HasPrefix(late.Hash.String(), "zt1:") {
		t.Fatalf("hash %s", late.Hash)
	}
}
