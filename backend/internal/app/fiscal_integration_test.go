//go:build integration

package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	adapterevidence "github.com/zoikogroup/zoikotax/backend/internal/adapter/evidence"
	"github.com/zoikogroup/zoikotax/backend/internal/adapter/postgres"
	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/content/bundle"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/accumulator"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/content"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/evidence"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/identity"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/rule"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/subledger"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
)

// A commit's fiscal effects against the real store: the worked pack with its
// fiscal profile (content/packs/eu-vat/fiscal.json) binds the eco-levy
// year-to-date accumulator and posts VAT and levy to the Tax Control
// Subledger.

const workedFiscal = "../../../content/packs/eu-vat/fiscal.json"

func fiscalBundle(t *testing.T) *rule.Bundle {
	t.Helper()
	src, err := os.ReadFile(workedPack)
	if err != nil {
		t.Fatal(err)
	}
	m := compileManifest(t, string(src))
	raw, err := os.ReadFile(workedFiscal)
	if err != nil {
		t.Fatal(err)
	}
	var f content.FiscalProfile
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		t.Fatal(err)
	}
	m.Fiscal, m.Digest = &f, ""
	encoded, err := bundle.EncodeManifest(m)
	if err != nil {
		t.Fatal(err)
	}
	decoded, digest, err := bundle.DecodeManifest(encoded)
	if err != nil {
		t.Fatal(err)
	}
	decoded.Digest = digest.String()
	b, err := rule.Load(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if b.Fiscal() == nil {
		t.Fatal("the worked bundle lost its fiscal profile")
	}
	return b
}

type fiscalCell struct {
	store  *postgres.Store
	ctx    context.Context
	tenant id.TenantID
	svc    *app.DeterminationService
	event  time.Time
}

func openFiscalCell(t *testing.T) *fiscalCell {
	t.Helper()
	dsn := os.Getenv("ZTAX_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ZTAX_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	pool, err := postgres.Open(ctx, postgres.DefaultConfig(dsn))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store := postgres.NewStore(pool)

	tenant := id.NewTenantID(uuid.Must(uuid.NewV7()))
	if err := store.Tenants().Create(ctx, identity.Tenant{
		ID: tenant, Slug: "fiscal-" + strings.ReplaceAll(tenant.String(), "-", ""), DisplayName: "Fiscal integration",
		ResidencyRegion: "local", HomeCell: "local-dev", Status: identity.TenantActive, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	sys := security.Into(ctx, security.System(tenant))
	if err := store.LegalEntities().Create(sys, identity.LegalEntity{
		ID: id.NewLegalEntityID(uuid.Must(uuid.NewV7())), TenantID: tenant, Name: "Fiscal integration Ltd",
		Default: true, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	files, err := adapterevidence.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	holder, library := &rule.Holder{}, &rule.Library{}
	b := fiscalBundle(t)
	holder.Publish(b)
	library.Add(b)
	clk := &settableClock{t: time.Date(2026, 9, 25, 9, 0, 0, 0, time.UTC)}
	svc := app.NewDeterminationService(holder, library, store.Decisions(), files, store, clk, idgen.V7{}, trains).
		WithIdempotency(app.NewIdempotency(store.Idempotency(), store, clk)).
		WithFiscal(app.FiscalStores{
			Accumulators: store.Accumulators(), Journals: store.Journals(),
			LegalEntities: store.LegalEntities(), Outbox: store.Outbox(),
		})
	operator := security.New(tenant, id.NewUserID(uuid.Must(uuid.NewV7())), id.NewSessionID(uuid.Must(uuid.NewV7())),
		[]security.Role{security.RoleOperator}, time.Now())
	return &fiscalCell{
		store: store, ctx: security.Into(ctx, operator), tenant: tenant, svc: svc,
		event: time.Date(2026, 9, 24, 18, 0, 0, 0, time.UTC),
	}
}

func (c *fiscalCell) commit(t *testing.T, key, businessKey, net, units string, supersedes *id.DecisionID) (app.Settled, error) {
	t.Helper()
	in := app.CommitInput{
		IdempotencyKey: key,
		Determination: app.DetermineInput{
			BusinessKey: businessKey, Supersedes: supersedes, EventTime: c.event, Input: lineInput(t, net, units, false),
		},
		Render: func(d evidence.Decision) ([]byte, error) { return []byte(d.ID.String()), nil },
		RenderFailure: func(err error) app.Response {
			return app.Response{Status: 400, Body: []byte(errs.ReasonOf(err))}
		},
	}
	if supersedes != nil {
		return c.svc.Adjust(c.ctx, in)
	}
	return c.svc.Commit(c.ctx, in)
}

func (c *fiscalCell) mustCommit(t *testing.T, key, businessKey, net, units string, supersedes *id.DecisionID) id.DecisionID {
	t.Helper()
	s, err := c.commit(t, key, businessKey, net, units, supersedes)
	if err != nil || s.Status != 201 || s.ResultRef == nil {
		t.Fatalf("commit %s: %v %d %s", key, err, s.Status, s.Body)
	}
	return *s.ResultRef
}

var ytdKey = accumulator.Key("threshold.ecoLevyYtd@2026-01-01")

func (c *fiscalCell) ytd(t *testing.T) string {
	t.Helper()
	o, err := c.store.Accumulators().ReadUnlocked(c.ctx, accumulator.Ref{Key: ytdKey, Currency: "EUR"})
	if err != nil {
		t.Fatal(err)
	}
	return o.Total.CanonicalString()
}

func TestIntegrationCommitAccumulatesAndPosts(t *testing.T) {
	c := openFiscalCell(t)
	// Worked pack, 100.00 net, 3 units: VAT 21.00; levy 3 x 0.035 = 0.105,
	// 0.11 at the line and 0.11 at the document.
	d1 := c.mustCommit(t, "k-1", "INV-1/1", "100.00", "3", nil)
	c.mustCommit(t, "k-2", "INV-2/1", "100.00", "3", nil)
	if got := c.ytd(t); got != "0.22" {
		t.Fatalf("eco levy year-to-date %s after two commits, want 0.22", got)
	}

	journals, err := c.store.Journals().BySource(c.ctx, app.SourceDecisionCommitted, d1.String())
	if err != nil {
		t.Fatal(err)
	}
	if len(journals) != 1 || len(journals[0].Lines) != 4 || journals[0].Currency != "EUR" {
		t.Fatalf("journals %+v", journals)
	}
	if err := journals[0].Validate(); err != nil {
		t.Fatalf("the posted journal does not balance: %v", err)
	}

	le, err := c.store.LegalEntities().Default(c.ctx)
	if err != nil {
		t.Fatal(err)
	}
	bal, err := c.store.Journals().Balances(c.ctx, le.ID)
	if err != nil {
		t.Fatal(err)
	}
	liability := bal[subledger.BalanceKey{Account: subledger.AccountTaxCollectedLiability, Currency: "EUR"}]
	if liability.Credits.CanonicalString() != "42.22" {
		t.Fatalf("collected liability credits %s, want 42.22 (2 x (21.00 + 0.11))", liability.Credits.CanonicalString())
	}

	// A retry of the same key replays and posts nothing more.
	again, err := c.commit(t, "k-1", "INV-1/1", "100.00", "3", nil)
	if err != nil || !again.Replayed {
		t.Fatalf("retry: %v %+v", err, again)
	}
	if got := c.ytd(t); got != "0.22" {
		t.Fatalf("a replayed commit contributed again: %s", got)
	}
}

func TestIntegrationCommitRefusesCallerSuppliedBoundTotals(t *testing.T) {
	c := openFiscalCell(t)
	settled, err := c.svc.Commit(c.ctx, app.CommitInput{
		IdempotencyKey: "k-x",
		Determination: app.DetermineInput{BusinessKey: "INV-9/1", EventTime: c.event,
			Input: lineInput(t, "100.00", "3", false), Accumulators: readSet(t, "0.00")},
		Render:        func(evidence.Decision) ([]byte, error) { return nil, nil },
		RenderFailure: func(err error) app.Response { return app.Response{Status: 400, Body: []byte(errs.ReasonOf(err))} },
	})
	// A validation failure is recorded against the key and returned as the
	// response, not as an error (ADR-0013 §2.7).
	if err != nil || settled.Status != 400 || string(settled.Body) != string(errs.ReasonInvalidValue) {
		t.Fatalf("a commit supplying a bound total: %v %d %s", err, settled.Status, settled.Body)
	}
	if got := c.ytd(t); got != "0" {
		t.Fatalf("a refused commit contributed: %s", got)
	}
}

func TestIntegrationCrossingIsRecordedWithItsEvent(t *testing.T) {
	c := openFiscalCell(t)
	// 300000 units x 0.035 = 10500.00 of levy: one commit takes the total
	// from 0 past the 10000.00 cap.
	c.mustCommit(t, "k-big", "INV-BIG/1", "100.00", "300000", nil)
	crossings, err := c.store.Accumulators().Crossings(c.ctx, ytdKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(crossings) != 1 || crossings[0].Threshold.ID != "eco-levy-cap" {
		t.Fatalf("crossings %+v", crossings)
	}
	var events int
	if err := c.store.Pool().QueryRow(c.ctx,
		`SELECT count(*) FROM ztax.outbox WHERE tenant_id = $1 AND event_type = $2`,
		c.tenant.UUID(), app.EventThresholdCrossed).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("%d threshold-crossed events, want 1", events)
	}
	// Over the cap the levy is suppressed: the next commit contributes zero
	// and crosses nothing again.
	c.mustCommit(t, "k-after", "INV-AFTER/1", "100.00", "3", nil)
	if again, _ := c.store.Accumulators().Crossings(c.ctx, ytdKey); len(again) != 1 {
		t.Fatalf("the cap was crossed twice: %+v", again)
	}
}

func TestIntegrationAdjustmentNetsTheAccumulatorAndReversesThePosting(t *testing.T) {
	c := openFiscalCell(t)
	original := c.mustCommit(t, "k-o", "INV-A/1", "100.00", "3", nil)
	// The correction carries 10 units: levy 0.35. The total is the
	// correction's, not the sum of both.
	corrected := c.mustCommit(t, "k-c", "INV-A/1", "100.00", "10", &original)
	if got := c.ytd(t); got != "0.35" {
		t.Fatalf("year-to-date after correction %s, want 0.35", got)
	}
	reversals, err := c.store.Journals().BySource(c.ctx, app.SourceDecisionSuperseded, original.String())
	if err != nil {
		t.Fatal(err)
	}
	if len(reversals) != 1 || reversals[0].ReversalOf == nil {
		t.Fatalf("reversal journals %+v", reversals)
	}
	posted, err := c.store.Journals().BySource(c.ctx, app.SourceDecisionCommitted, corrected.String())
	if err != nil || len(posted) != 1 {
		t.Fatalf("correction journals %+v %v", posted, err)
	}
	report, err := c.svc.Replay(c.ctx, corrected)
	if err != nil || report.Verdict != evidence.ReplayMatch {
		t.Fatalf("the correction does not replay: %v %+v", err, report)
	}
}

func TestIntegrationQuoteReadsTheStoredTotal(t *testing.T) {
	c := openFiscalCell(t)
	c.mustCommit(t, "k-q", "INV-Q/1", "100.00", "3", nil)
	q, err := c.svc.Quote(c.ctx, app.QuoteInput{EventTime: c.event, Input: lineInput(t, "100.00", "3", false)})
	if err != nil {
		t.Fatal(err)
	}
	if q.Result.Emitted["TAX_ECO_LEVY"].Money.CanonicalString() != "0.11" {
		t.Fatalf("quote levy %+v", q.Result.Emitted["TAX_ECO_LEVY"])
	}
}
