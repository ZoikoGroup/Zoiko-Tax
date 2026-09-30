package app_test

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/evidence"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/rule"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
)

// The W1 exit criterion, stated as a test: one decision type — the worked
// pack's evaluator output — recorded, then rebuilt from its envelope alone,
// byte for byte.
func TestDecisionReplaysExactly(t *testing.T) {
	h := newHarness(t)
	d := h.determine(t, "line-1")

	if got := emitted(d, "TAX_VAT"); got != "21.00 EUR" {
		t.Fatalf("TAX_VAT = %s, want 21.00 EUR", got)
	}
	// 3 × 0.0350 = 0.105, rounded HALF_UP at the line to 0.11, then HALF_EVEN
	// at the document to 0.11. Below the threshold, so the levy is due.
	if got := emitted(d, "TAX_ECO_LEVY"); got != "0.11 EUR" {
		t.Fatalf("TAX_ECO_LEVY = %s, want 0.11 EUR", got)
	}
	if d.Result.Outcome != evidence.OutcomeAdvisory || d.Result.Reason != errs.ReasonNotAuthoritative {
		t.Fatalf("outcome = %s/%s; nothing before A4 is authoritative", d.Result.Outcome, d.Result.Reason)
	}

	report, err := h.svc.Replay(as(security.RoleAuditor), d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Verdict != evidence.ReplayMatch {
		t.Fatalf("verdict %s: %s", report.Verdict, report.Divergence)
	}
	if !report.ReplayedResult.Equal(d.ResultDigest) {
		t.Fatalf("replayed %s, recorded %s", report.ReplayedResult, d.ResultDigest)
	}
}

// A replica, or the same cell after a restart, holds a separately loaded copy
// of the bundle. The replay must not depend on which process made the decision.
func TestReplayOnAFreshRuntimeMatches(t *testing.T) {
	h := newHarness(t)
	d := h.determine(t, "line-1")

	fresh := &rule.Library{}
	fresh.Add(workedBundle(t))
	other := app.NewDeterminationService(&rule.Holder{}, fresh, h.decisions, h.store, noTx{}, h.clock, &idgen.Sequential{}, trains)

	report, err := other.Replay(system(), d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Verdict != evidence.ReplayMatch {
		t.Fatalf("verdict %s: %s", report.Verdict, report.Divergence)
	}
}

// ZTAX-DET-REQ-0030's reasoning, applied to replay: a decision replays under
// the content that made it, not the content active now.
func TestReplayUsesTheBundleTheDecisionNames(t *testing.T) {
	h := newHarness(t)
	d := h.determine(t, "line-1")

	src, err := os.ReadFile(workedPack)
	if err != nil {
		t.Fatal(err)
	}
	later := strings.Replace(string(src), `const standard rate  "0.2100"`, `const standard rate  "0.2300"`, 1)
	if later == string(src) {
		t.Fatal("the pack no longer has the line this test edits; update the test")
	}
	m := compileManifest(t, later)
	next, err := rule.Load(m)
	if err != nil {
		t.Fatal(err)
	}
	h.holder.Publish(next)
	h.library.Add(next)

	report, err := h.svc.Replay(system(), d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Verdict != evidence.ReplayMatch {
		t.Fatalf("a content release changed the replay of an earlier decision: %s %s", report.Verdict, report.Divergence)
	}

	// Without the original bundle there is nothing to replay against, and
	// that is reported as what it is rather than as a divergence.
	only := &rule.Library{}
	only.Add(next)
	bare := app.NewDeterminationService(h.holder, only, h.decisions, h.store, noTx{}, h.clock, &idgen.Sequential{}, trains)
	report, err = bare.Replay(system(), d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Verdict != evidence.ReplayBundleUnavailable {
		t.Fatalf("verdict %s, want BUNDLE_UNAVAILABLE", report.Verdict)
	}
}

// The replay has to be able to fail. A runtime that answers differently for
// the same envelope — here, content that claims the original digest and
// carries a different rate — is reported as DIVERGED, with where.
func TestReplayDetectsDivergence(t *testing.T) {
	h := newHarness(t)
	d := h.determine(t, "line-1")

	src, _ := os.ReadFile(workedPack)
	forged := strings.Replace(string(src), `const standard rate  "0.2100"`, `const standard rate  "0.2101"`, 1)
	m := compileManifest(t, forged)
	m.Digest = d.Envelope.BundleDigest
	impostor, err := rule.Load(m)
	if err != nil {
		t.Fatal(err)
	}
	lib := &rule.Library{}
	lib.Add(impostor)
	svc := app.NewDeterminationService(&rule.Holder{}, lib, h.decisions, h.store, noTx{}, h.clock, &idgen.Sequential{}, trains)

	report, err := svc.Replay(system(), d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Verdict != evidence.ReplayDiverged {
		t.Fatalf("verdict %s, want DIVERGED", report.Verdict)
	}
	if !strings.HasPrefix(report.Divergence, "$.") {
		t.Fatalf("the divergence should name where, got %q", report.Divergence)
	}
}

func TestTamperedEvidenceIsAnIntegrityFailure(t *testing.T) {
	h := newHarness(t)
	d := h.determine(t, "line-1")
	h.store.tamper(d.ResultDigest, []byte(`{"outcome":"AUTHORITATIVE"}`))

	_, err := h.svc.Replay(system(), d.ID)
	if errs.ReasonOf(err) != errs.ReasonEvidenceIntegrity {
		t.Fatalf("got %v, want EVIDENCE_INTEGRITY", err)
	}
}

func TestRecordThatDisagreesWithItsEnvelopeIsRefused(t *testing.T) {
	h := newHarness(t)
	d := h.determine(t, "line-1")
	h.decisions.rewrite(d.ID, func(r *evidence.Record) { r.BundleDigest = "zt1:" + strings.Repeat("0", 64) })

	_, err := h.svc.Replay(system(), d.ID)
	if errs.ReasonOf(err) != errs.ReasonEvidenceIntegrity {
		t.Fatalf("got %v, want EVIDENCE_INTEGRITY", err)
	}
}

// ZTAX-DET-REQ-0002: the read set is exactly what the bundle reads.
func TestReadSetMustMatchTheBundle(t *testing.T) {
	h := newHarness(t)
	for name, acc := range map[string]map[string]string{
		"missing": {},
		"extra":   {"threshold.ecoLevyYtd": "1.00", "threshold.somethingElse": "1.00"},
	} {
		t.Run(name, func(t *testing.T) {
			in := app.DetermineInput{
				BusinessKey: "line-1", EventTime: time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC),
				Input: lineInput(t, "10.00", "1", false),
			}
			in.Accumulators = readSet(t, "0.00")
			delete(in.Accumulators, "threshold.ecoLevyYtd")
			for k, v := range acc {
				in.Accumulators[k] = readSet(t, v)["threshold.ecoLevyYtd"]
			}
			if _, err := h.svc.Determine(as(security.RoleOperator), in); !errs.IsCategory(err, errs.CategoryValidation) {
				t.Fatalf("got %v, want a validation refusal", err)
			}
		})
	}
}

// Input the content reads and the request omits is refused, not defaulted —
// and refused before anything is recorded.
func TestMissingInputIsRefusedBeforeAnythingIsRecorded(t *testing.T) {
	h := newHarness(t)
	in := lineInput(t, "10.00", "1", false)
	delete(in.Flags, "line.exemptCertificateHeld")
	_, err := h.svc.Determine(as(security.RoleOperator), app.DetermineInput{
		BusinessKey: "line-1", EventTime: time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC),
		Input: in, Accumulators: readSet(t, "0.00"),
	})
	if !errs.IsCategory(err, errs.CategoryValidation) {
		t.Fatalf("got %v, want a validation refusal", err)
	}
	if len(h.decisions.rows) != 0 {
		t.Fatal("a refused request left a decision behind")
	}
}

// ZTAX-DET-REQ-0032: a correction supersedes; the original is never modified,
// and a decision is superseded at most once.
func TestCorrectionSupersedes(t *testing.T) {
	h := newHarness(t)
	first := h.determine(t, "line-1")
	h.clock.Advance(time.Minute)

	correct := func(prior evidence.Decision) (evidence.Decision, error) {
		return h.svc.Determine(as(security.RoleOperator), app.DetermineInput{
			BusinessKey: "line-1", Supersedes: &prior.ID,
			EventTime:    time.Date(2026, 9, 24, 18, 0, 0, 0, time.UTC),
			Input:        lineInput(t, "100.00", "3", true),
			Accumulators: readSet(t, "9999.99"),
		})
	}
	second, err := correct(first)
	if err != nil {
		t.Fatal(err)
	}
	if emitted(second, "TAX_VAT") != "9.00 EUR" {
		t.Fatalf("the correction applied the reduced rate: got %s", emitted(second, "TAX_VAT"))
	}
	if _, err := correct(first); !errs.IsCategory(err, errs.CategoryConflict) {
		t.Fatalf("superseding an already-superseded decision: got %v, want a conflict", err)
	}
	// Both versions still replay: correction added a row and touched nothing.
	for _, d := range []evidence.Decision{first, second} {
		report, err := h.svc.Replay(system(), d.ID)
		if err != nil || report.Verdict != evidence.ReplayMatch {
			t.Fatalf("%s: %v %+v", d.ID, err, report)
		}
	}
}

func TestAuthorization(t *testing.T) {
	h := newHarness(t)
	in := app.DetermineInput{
		BusinessKey: "line-1", EventTime: time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC),
		Input: lineInput(t, "10.00", "1", false), Accumulators: readSet(t, "0.00"),
	}
	if _, err := h.svc.Determine(as(security.RoleAnalyst), in); errs.ReasonOf(err) != errs.ReasonForbidden {
		t.Fatalf("an analyst determined: %v", err)
	}
	if _, err := h.svc.Determine(t.Context(), in); errs.ReasonOf(err) != errs.ReasonUnauthenticated {
		t.Fatalf("an unauthenticated caller determined: %v", err)
	}
	d := h.determine(t, "line-2")
	if _, err := h.svc.Replay(as(security.RoleAdmin), d.ID); errs.ReasonOf(err) != errs.ReasonForbidden {
		t.Fatalf("an admin with no read role replayed: %v", err)
	}
}

func TestNoContentNoDecision(t *testing.T) {
	h := newHarness(t)
	svc := app.NewDeterminationService(&rule.Holder{}, h.library, h.decisions, h.store, noTx{}, h.clock, &idgen.Sequential{}, trains)
	_, err := svc.Determine(as(security.RoleOperator), app.DetermineInput{
		BusinessKey: "line-1", EventTime: time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC),
		Input: lineInput(t, "10.00", "1", false), Accumulators: readSet(t, "0.00"),
	})
	if errs.ReasonOf(err) != errs.ReasonNoContentBundle {
		t.Fatalf("got %v, want NO_CONTENT_BUNDLE", err)
	}
}
