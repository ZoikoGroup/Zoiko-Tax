package app_test

import (
	"os"
	"testing"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/content/bundle"
	"github.com/zoikogroup/zoikotax/backend/internal/content/compile"
	"github.com/zoikogroup/zoikotax/backend/internal/content/dsl"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/evidence"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/rule"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/taxation"
	"github.com/zoikogroup/zoikotax/backend/internal/fiscaltest"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
)

// Playbook §26 steps 3 to 6 and 9, at the use case, against the shipped
// training pack: a quote that records nothing, a commit and its concurrent
// and conflicting retries, refusals that are never a silent zero, and a
// replay that reproduces the decision from its envelope alone.

const trainingPack = "../../../content/packs/training-telecom"

// trainingBundle builds the taxation-only bundle the way ztax-contentc does:
// parse the rule-less source, attach the profile, encode, decode, load.
func trainingBundle(t testing.TB) *rule.Bundle {
	t.Helper()
	src, err := os.ReadFile(trainingPack + "/training-telecom.ztax")
	if err != nil {
		t.Fatal(err)
	}
	prog, err := dsl.Parse("training-telecom.ztax", string(src))
	if err != nil {
		t.Fatal(err)
	}
	m, err := compile.Compile(prog)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(trainingPack + "/taxation.json")
	if err != nil {
		t.Fatal(err)
	}
	p, err := taxation.DecodeProfile(raw)
	if err != nil {
		t.Fatal(err)
	}
	m.Taxation = &p
	data, err := bundle.EncodeManifest(m)
	if err != nil {
		t.Fatal(err)
	}
	decoded, digest, err := bundle.DecodeManifest(data)
	if err != nil {
		t.Fatalf("the manifest does not round-trip: %v", err)
	}
	decoded.Digest = digest.String()
	b, err := rule.Load(decoded)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func locationHarness(t testing.TB) *harness {
	t.Helper()
	h := &harness{
		holder: &rule.Holder{}, library: &rule.Library{}, store: newMemStore(),
		decisions: &memDecisions{}, idem: newMemIdempotency(),
		clock: &settableClock{t: time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)},
	}
	b := trainingBundle(t)
	h.holder.Publish(b)
	h.library.Add(b)
	h.svc = app.NewDeterminationService(h.holder, h.library, h.decisions, h.store, noTx{}, h.clock, &idgen.Sequential{}, trains).
		WithIdempotency(app.NewIdempotency(h.idem, noTx{}, h.clock))
	return h
}

var taxPoint = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

func usd(t testing.TB, amount string) fiscal.Money { return fiscaltest.Money(t, amount, "USD") }

func sectionSeven(t testing.TB, voice string) *taxation.Document {
	t.Helper()
	q, err := fiscal.ParseQuantity("1", "LINE")
	if err != nil {
		t.Fatal(err)
	}
	total, _ := usd(t, voice).Add(usd(t, "60.00"))
	return &taxation.Document{
		Type: taxation.DocumentInvoice, Currency: "USD", PriceMode: taxation.PriceExclusive,
		Seller:   taxation.Party{ID: "TELCO-A", Establishment: &taxation.Place{Country: "XT"}},
		Customer: taxation.Party{ID: "C1"},
		Evidence: []taxation.Evidence{{Type: "SERVICE_ADDRESS", Source: "SELLER_RECORDED", Place: taxation.Place{Country: "XT"}}},
		Lines: []taxation.Line{{
			Key: "L1", SKU: "BUNDLE-1", Amount: total,
			Parts: []taxation.Part{
				{Key: "voice", Service: "MOBILE_VOICE", Amount: usd(t, voice), Quantity: &q},
				{Key: "data", Service: "MOBILE_DATA", Amount: usd(t, "50.00")},
				{Key: "device", Service: "EQUIPMENT_SALE", Amount: usd(t, "10.00")},
			},
		}},
	}
}

func documentCommit(key string, doc *taxation.Document) app.CommitInput {
	return app.CommitInput{
		IdempotencyKey: key,
		Determination:  app.DetermineInput{BusinessKey: "INV-001", EventTime: taxPoint, Document: doc},
		Render: func(d evidence.Decision) ([]byte, error) {
			return []byte(`{"id":"` + d.ID.String() + `"}`), nil
		},
		RenderFailure: func(err error) app.Response {
			return app.Response{Status: 400, Body: []byte(`{"reason":"` + string(errs.ReasonOf(err)) + `"}`)}
		},
	}
}

func TestDocumentQuoteRecordsNothing(t *testing.T) {
	h := locationHarness(t)
	q, err := h.svc.Quote(as(security.RoleOperator), app.QuoteInput{EventTime: taxPoint, Document: sectionSeven(t, "40.00")})
	if err != nil {
		t.Fatal(err)
	}
	doc := q.Result.Document
	if q.Result.Outcome != evidence.OutcomeAdvisory || doc == nil || doc.Totals.CustomerTotal.CanonicalString() != "104.45" ||
		doc.Totals.ProviderAccrual.CanonicalString() != "0.80" {
		t.Fatalf("quote: %s, %+v", q.Result.Outcome, doc)
	}
	if h.rows() != 0 || h.idem.count() != 0 {
		t.Fatal("a quote recorded a decision or an idempotency record")
	}
}

func TestDocumentCommitIsIdempotentAndReplays(t *testing.T) {
	h := locationHarness(t)
	first, err := h.svc.Commit(as(security.RoleOperator), documentCommit("demo-001", sectionSeven(t, "40.00")))
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != 201 || first.ResultRef == nil {
		t.Fatalf("commit: %+v", first)
	}
	again, err := h.svc.Commit(as(security.RoleOperator), documentCommit("demo-001", sectionSeven(t, "40.00")))
	if err != nil || !again.Replayed || *again.ResultRef != *first.ResultRef || h.rows() != 1 {
		t.Fatalf("retry: %+v, %v; rows %d", again, err, h.rows())
	}
	// §26 step 5: the same key with a changed amount is a deterministic
	// conflict, and the original decision is unchanged.
	if _, err := h.svc.Commit(as(security.RoleOperator), documentCommit("demo-001", sectionSeven(t, "41.00"))); errs.ReasonOf(err) != errs.ReasonIdempotencyKeyReuse {
		t.Fatalf("changed amount under a used key: %v", err)
	}

	rd, err := h.svc.Decision(as(security.RoleAuditor), *first.ResultRef)
	if err != nil {
		t.Fatal(err)
	}
	if rd.Result.Document == nil || rd.Result.Document.Totals.CustomerTax.CanonicalString() != "4.45" {
		t.Fatalf("read back: %+v", rd.Result.Document)
	}
	if v := rd.Result.Emitted[taxation.SlotProviderAccrual]; v.Money.CanonicalString() != "0.80" {
		t.Errorf("emitted provider accrual %s", v.Money.CanonicalString())
	}

	report, err := h.svc.Replay(as(security.RoleAuditor), *first.ResultRef)
	if err != nil {
		t.Fatal(err)
	}
	if report.Verdict != evidence.ReplayMatch {
		t.Fatalf("replay %s: %s", report.Verdict, report.Divergence)
	}
}

func TestDocumentWithoutLocationIsRecordedNotZero(t *testing.T) {
	h := locationHarness(t)
	doc := sectionSeven(t, "40.00")
	doc.Evidence = nil
	settled, err := h.svc.Commit(as(security.RoleOperator), documentCommit("demo-002", doc))
	if err != nil {
		t.Fatal(err)
	}
	rd, err := h.svc.Decision(as(security.RoleAuditor), *settled.ResultRef)
	if err != nil {
		t.Fatal(err)
	}
	if rd.Result.Outcome != evidence.OutcomeReviewRequired || rd.Result.Reason != errs.ReasonSitusEvidenceInsufficient {
		t.Fatalf("outcome %s (%s)", rd.Result.Outcome, rd.Result.Reason)
	}
	if len(rd.Result.Emitted) != 0 || rd.Result.Document.Totals != nil {
		t.Fatal("a decision with no place of supply emitted amounts; it would post and assess a zero")
	}
}

func TestNamedInputsAgainstATaxationOnlyBundleAreRefused(t *testing.T) {
	h := locationHarness(t)
	_, err := h.svc.Quote(as(security.RoleOperator), app.QuoteInput{EventTime: taxPoint, Input: lineInput(t, "100.00", "3", false)})
	if errs.CategoryOf(err) != errs.CategoryValidation {
		t.Fatalf("named inputs against a taxation-only bundle: %v", err)
	}
}

func TestDocumentAgainstARuleOnlyBundleIsRefused(t *testing.T) {
	h := newHarness(t)
	_, err := h.svc.Quote(as(security.RoleOperator), app.QuoteInput{EventTime: taxPoint, Document: sectionSeven(t, "40.00")})
	if errs.CategoryOf(err) != errs.CategoryValidation {
		t.Fatalf("a document against the worked rule bundle: %v", err)
	}
}
