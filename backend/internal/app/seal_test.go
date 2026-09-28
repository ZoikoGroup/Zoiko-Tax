package app_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/evidence"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/kms"
)

var (
	keyFrom  = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	keyUntil = time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	dayStart = time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC)
	dayEnd   = dayStart.Add(24 * time.Hour)
)

// An evidence-seal key pair. Development-only by construction: NewLocalSigner
// refuses any environment but development, so this cannot be what signs a
// cell's seals (ADR-0017 §2.6).
func sealKey(t testing.TB, keyID string) (kms.Signer, *kms.Keyring) {
	t.Helper()
	pem, err := kms.GenerateDevelopmentKey("development")
	if err != nil {
		t.Fatal(err)
	}
	signer, err := kms.NewLocalSigner("development", pem, keyID, keyFrom, keyUntil)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := signer.PublicKeyEntry()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := kms.MarshalKeyring([]kms.PublicKey{entry})
	if err != nil {
		t.Fatal(err)
	}
	ring, err := kms.ParseKeyring(raw)
	if err != nil {
		t.Fatal(err)
	}
	return signer, ring
}

type sealHarness struct {
	*harness
	seals  *memSeals
	sealer *app.SealService
}

// newSealHarness records three decisions during dayStart's day, then moves the
// clock past the settle window so the day may be sealed.
func newSealHarness(t testing.TB) (*sealHarness, []evidence.Decision) {
	t.Helper()
	h := newHarness(t)
	signer, ring := sealKey(t, "evidence-seal-test-2026")
	sh := &sealHarness{harness: h, seals: &memSeals{}}
	sh.sealer = app.NewSealService(h.decisions, sh.seals, h.store, noTx{}, signer, ring, h.clock,
		&idgen.Sequential{}, "eu-west-1a", 0)

	var ds []evidence.Decision
	for i, at := range []time.Duration{9 * time.Hour, 12 * time.Hour, 23*time.Hour + 59*time.Minute} {
		h.clock.Set(dayStart.Add(at))
		ds = append(ds, h.determine(t, "line-"+string(rune('a'+i))))
	}
	// One decision after the period, which must not be sealed into it.
	h.clock.Set(dayEnd.Add(time.Minute))
	h.determine(t, "line-next-day")

	h.clock.Set(dayEnd.Add(app.DefaultSettleWindow + time.Minute))
	return sh, ds
}

func (sh *sealHarness) seal(t testing.TB) evidence.SealRecord {
	t.Helper()
	rec, err := sh.sealer.SealPeriod(system(), dayStart, dayEnd)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	return rec
}

func (sh *sealHarness) verify(t testing.TB, rec evidence.SealRecord) app.SealVerification {
	t.Helper()
	v, err := sh.sealer.VerifySeal(as(security.RoleAuditor), rec.SealID)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	return v
}

func TestSealThenVerify(t *testing.T) {
	sh, _ := newSealHarness(t)
	rec := sh.seal(t)
	if rec.LeafCount != 3 {
		t.Fatalf("sealed %d decisions, want the 3 recorded inside the period", rec.LeafCount)
	}
	v := sh.verify(t, rec)
	if v.Verdict != evidence.SealValid {
		t.Fatalf("verdict %s: %s", v.Verdict, v.Detail)
	}
	if !v.RecomputedRoot.Equal(rec.MerkleRoot) {
		t.Fatal("the recomputed root is not the sealed one")
	}
}

// ADR-0011 §5.1 control 4, end to end: tamper with one record and prove
// verification fails.
func TestTamperedDecisionFailsVerification(t *testing.T) {
	sh, ds := newSealHarness(t)
	rec := sh.seal(t)
	sh.decisions.rewrite(ds[1].ID, func(r *evidence.Record) { r.ResultDigest = canonical.SumBytes([]byte("forged")) })

	v := sh.verify(t, rec)
	if v.Verdict != evidence.SealRootMismatch {
		t.Fatalf("verdict %s, want ROOT_MISMATCH", v.Verdict)
	}
}

func TestRecordSlippedIntoASealedPeriodFailsVerification(t *testing.T) {
	sh, _ := newSealHarness(t)
	rec := sh.seal(t)
	sh.clock.Set(dayStart.Add(15 * time.Hour)) // a backdated decision time
	sh.determine(t, "line-backdated")

	v := sh.verify(t, rec)
	if v.Verdict != evidence.SealRootMismatch {
		t.Fatalf("verdict %s, want ROOT_MISMATCH", v.Verdict)
	}
	if !strings.Contains(v.Detail, "4 decision(s)") {
		t.Fatalf("detail should say the period now holds one more: %q", v.Detail)
	}
}

func TestAlteredResultObjectFailsVerification(t *testing.T) {
	sh, ds := newSealHarness(t)
	rec := sh.seal(t)
	sh.store.tamper(ds[0].ResultDigest, []byte(`{"outcome":"AUTHORITATIVE"}`))

	v := sh.verify(t, rec)
	if v.Verdict != evidence.SealEvidenceMissing {
		t.Fatalf("verdict %s, want EVIDENCE_MISSING", v.Verdict)
	}
}

func TestSealIndexThatDisagreesWithTheSignedPayload(t *testing.T) {
	sh, _ := newSealHarness(t)
	rec := sh.seal(t)
	sh.seals.rewrite(rec.SealID, func(r *evidence.SealRecord) { r.LeafCount = 2 })

	v := sh.verify(t, rec)
	if v.Verdict != evidence.SealRecordMismatch {
		t.Fatalf("verdict %s, want RECORD_MISMATCH", v.Verdict)
	}
}

func TestSealSignedByAnUntrustedKey(t *testing.T) {
	sh, _ := newSealHarness(t)
	rec := sh.seal(t)
	_, otherRing := sealKey(t, "someone-else")
	verifier := app.NewSealService(sh.decisions, sh.seals, sh.store, noTx{}, nil, otherRing, sh.clock,
		&idgen.Sequential{}, "eu-west-1a", 0)

	v, err := verifier.VerifySeal(system(), rec.SealID)
	if err != nil {
		t.Fatal(err)
	}
	if v.Verdict != evidence.SealSignatureInvalid {
		t.Fatalf("verdict %s, want SIGNATURE_INVALID", v.Verdict)
	}
}

func TestPeriodMustSettleBeforeSealing(t *testing.T) {
	sh, _ := newSealHarness(t)
	sh.clock.Set(dayEnd.Add(time.Minute))
	_, err := sh.sealer.SealPeriod(system(), dayStart, dayEnd)
	if !errs.IsCategory(err, errs.CategoryValidation) {
		t.Fatalf("sealed an unsettled period: %v", err)
	}
}

func TestOverlappingPeriodsAreRefused(t *testing.T) {
	sh, _ := newSealHarness(t)
	sh.seal(t)
	_, err := sh.sealer.SealPeriod(system(), dayStart.Add(-12*time.Hour), dayStart.Add(12*time.Hour))
	if !errs.IsCategory(err, errs.CategoryConflict) {
		t.Fatalf("sealed an overlapping period: %v", err)
	}
}

func TestEmptyPeriodSealsAndVerifies(t *testing.T) {
	sh, _ := newSealHarness(t)
	earlier := dayStart.Add(-48 * time.Hour)
	rec, err := sh.sealer.SealPeriod(system(), earlier, earlier.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if rec.LeafCount != 0 {
		t.Fatalf("leaf count %d, want 0", rec.LeafCount)
	}
	if v := sh.verify(t, rec); v.Verdict != evidence.SealValid {
		t.Fatalf("verdict %s: %s", v.Verdict, v.Detail)
	}
}

func TestSealAuthorization(t *testing.T) {
	sh, _ := newSealHarness(t)
	if _, err := sh.sealer.SealPeriod(as(security.RoleOperator), dayStart, dayEnd); errs.ReasonOf(err) != errs.ReasonForbidden {
		t.Fatalf("an operator sealed: %v", err)
	}
	if _, err := sh.sealer.SealPeriod(context.Background(), dayStart, dayEnd); errs.ReasonOf(err) != errs.ReasonUnauthenticated {
		t.Fatalf("an unauthenticated caller sealed: %v", err)
	}
}
