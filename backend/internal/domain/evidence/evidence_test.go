package evidence_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/evidence"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/rule"
	"github.com/zoikogroup/zoikotax/backend/internal/fiscaltest"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

var (
	decisionTime = time.Date(2026, 9, 25, 10, 30, 0, 123456000, time.UTC)
	eventTime    = time.Date(2026, 9, 24, 23, 59, 59, 0, time.UTC)
)

func envelope(t *testing.T) evidence.Envelope {
	t.Helper()
	qty, err := fiscal.ParseQuantity("3", "EA")
	if err != nil {
		t.Fatal(err)
	}
	return evidence.Envelope{
		DecisionTime: decisionTime,
		EventTime:    eventTime,
		BundleID:     "eu-vat-worked-2026.09",
		BundleDigest: "zt1:be8651b55057f0f11f2a39820495b373417e5aa81b122f61b9fcb79faaf86c7b",
		IRVersion:    1,
		CanonProfile: canonical.ProfileVersion,
		Trains: evidence.Trains{App: "1.0.0", Content: "2026.09.1", AI: "0", Adapter: "0",
			Infra: "0", Schema: "1.3.1", Migration: "5"},
		Input: evidence.Input{
			Money:      map[string]fiscal.Money{"line.netAmount": fiscaltest.Money(t, "100.50", "EUR")},
			Quantities: map[string]fiscal.Quantity{"line.quantity": qty},
			Flags:      map[string]bool{"line.reducedRateApplies": false, "line.exemptCertificateHeld": false},
		},
		Accumulators: map[string]fiscal.Money{"threshold.ecoLevyYtd": fiscaltest.Money(t, "9999.99", "EUR")},
	}
}

func TestEnvelopeRoundTripsExactly(t *testing.T) {
	env := envelope(t)
	encoded, err := evidence.EncodeEnvelope(env)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := evidence.DecodeEnvelope(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	again, err := evidence.EncodeEnvelope(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encoded, again) {
		t.Fatalf("round trip changed the bytes:\n%s\n%s", encoded, again)
	}
	// Scale survives: 100.50 is not 100.5 (ADR-0011 §2.2).
	if !bytes.Contains(encoded, []byte(`"100.50"`)) {
		t.Fatalf("scale was lost: %s", encoded)
	}
	d1, _ := env.Digest()
	d2, _ := decoded.Digest()
	if !d1.Equal(d2) {
		t.Fatalf("digest changed across a round trip: %s vs %s", d1, d2)
	}
}

func TestEmptyReadSetIsRecordedNotOmitted(t *testing.T) {
	env := envelope(t)
	env.Accumulators = nil
	encoded, err := evidence.EncodeEnvelope(env)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"accumulators":{}`)) {
		t.Fatalf("an empty read set must be recorded as {}: %s", encoded)
	}
	if _, err := evidence.DecodeEnvelope(encoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
}

// Every way a stored envelope can mean something other than what canon/v1
// wrote is refused, rather than replayed as a reinterpretation.
func TestDecodeEnvelopeRefusesReinterpretation(t *testing.T) {
	encoded, err := evidence.EncodeEnvelope(envelope(t))
	if err != nil {
		t.Fatal(err)
	}
	s := string(encoded)
	cases := map[string]string{
		"a fiscal amount as a JSON number (P1)": strings.Replace(s, `"amount":"100.50"`, `"amount":100.50`, 1),
		"null (P3)":                             strings.Replace(s, `"bundleId":"eu-vat-worked-2026.09"`, `"bundleId":null`, 1),
		"an unknown member (P5)":                strings.Replace(s, `"irVersion":1`, `"irVersion":1,"extra":true`, 1),
		"an exponent form of the same number":   strings.Replace(s, `"amount":"100.50"`, `"amount":"1.0050E+2"`, 1),
		"a member ahead of the first key":       strings.Replace(s, `{"accumulators"`, `{"aaa":0,"accumulators"`, 1),
		"insignificant whitespace":              strings.Replace(s, `"irVersion":1`, `"irVersion": 1`, 1),
		"an instant at another precision":       strings.Replace(s, "10:30:00.123456Z", "10:30:00.123Z", 1),
		"another canonicalization profile":      strings.Replace(s, `"canon/v1"`, `"canon/v2"`, 1),
		"trailing content":                      s + `{}`,
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			if doc == s {
				t.Fatal("the case did not alter the document; fix the test")
			}
			if _, err := evidence.DecodeEnvelope([]byte(doc)); err == nil {
				t.Fatalf("accepted %s", doc)
			}
		})
	}
}

func TestEnvelopeRefusesSubMicrosecondInstants(t *testing.T) {
	env := envelope(t)
	env.DecisionTime = env.DecisionTime.Add(1)
	if err := env.Validate(); err == nil {
		t.Fatal("a nanosecond instant was accepted; it would evaluate at one precision and replay at another")
	}
}

func TestConcludeNeverProducesAuthoritative(t *testing.T) {
	cases := []struct {
		res  rule.Result
		want evidence.Outcome
	}{
		{rule.Result{}, evidence.OutcomeAdvisory},
		{rule.Result{Refused: true, Reason: errs.ReasonAmbiguous}, evidence.OutcomeAmbiguous},
		{rule.Result{Refused: true, Reason: errs.ReasonConflicted}, evidence.OutcomeConflicted},
		{rule.Result{Refused: true, Reason: errs.ReasonReviewRequired}, evidence.OutcomeReviewRequired},
		{rule.Result{Refused: true, Reason: errs.ReasonJurisdictionUnsupported}, evidence.OutcomeUnsupported},
	}
	for _, c := range cases {
		got, reason := evidence.Conclude(c.res)
		if got != c.want || got == evidence.OutcomeAuthoritative || reason == "" {
			t.Errorf("Conclude(%+v) = %s/%s, want %s with a reason", c.res, got, reason, c.want)
		}
	}
	if _, reason := evidence.Conclude(rule.Result{}); reason != errs.ReasonNotAuthoritative {
		t.Errorf("an advisory result must say why it is advisory, got %s", reason)
	}
}

// ---------------------------------------------------------------------------
// period seals — ADR-0011 §5.1 control 4: tamper with one record, prove the
// root changes and verification fails.
// ---------------------------------------------------------------------------

func leaves(t *testing.T, n int) []evidence.SealLeaf {
	t.Helper()
	out := make([]evidence.SealLeaf, n)
	for i := range out {
		out[i] = evidence.SealLeaf{
			DecisionID:   id.NewDecisionID(uuid.MustParse("01920000-0000-7000-8000-" + pad(i))),
			RecordedAt:   decisionTime.Add(time.Duration(i) * time.Second),
			ResultDigest: canonical.SumBytes([]byte{byte(i)}),
		}
	}
	return out
}

func pad(i int) string {
	s := "000000000000" + string(rune('0'+i%10))
	return s[len(s)-12:]
}

func TestPeriodRootDetectsTampering(t *testing.T) {
	base := leaves(t, 5)
	root, err := evidence.PeriodRoot(base)
	if err != nil {
		t.Fatal(err)
	}

	tamper := map[string]func([]evidence.SealLeaf) []evidence.SealLeaf{
		"a result changed": func(l []evidence.SealLeaf) []evidence.SealLeaf {
			l[2].ResultDigest = canonical.SumBytes([]byte("forged"))
			return l
		},
		"a record removed": func(l []evidence.SealLeaf) []evidence.SealLeaf { return append(l[:1], l[2:]...) },
		"a record added": func(l []evidence.SealLeaf) []evidence.SealLeaf {
			extra := leaves(t, 6)[5]
			return append(l, extra)
		},
		"a record moved in time": func(l []evidence.SealLeaf) []evidence.SealLeaf {
			l[3].RecordedAt = l[3].RecordedAt.Add(time.Microsecond)
			return l
		},
		"two results swapped": func(l []evidence.SealLeaf) []evidence.SealLeaf {
			l[0].ResultDigest, l[1].ResultDigest = l[1].ResultDigest, l[0].ResultDigest
			return l
		},
	}
	for name, f := range tamper {
		t.Run(name, func(t *testing.T) {
			changed := f(append([]evidence.SealLeaf(nil), base...))
			got, err := evidence.PeriodRoot(changed)
			if err != nil {
				t.Fatal(err)
			}
			if got.Equal(root) {
				t.Fatal("the root did not change")
			}
		})
	}
}

func TestPeriodRootIsIndependentOfInputOrder(t *testing.T) {
	base := leaves(t, 4)
	root, _ := evidence.PeriodRoot(base)
	reversed := []evidence.SealLeaf{base[3], base[2], base[1], base[0]}
	got, _ := evidence.PeriodRoot(reversed)
	if !got.Equal(root) {
		t.Fatal("the root depends on the order the store returned the leaves in")
	}
}

func TestEmptyPeriodStillSeals(t *testing.T) {
	root, err := evidence.PeriodRoot(nil)
	if err != nil || root.IsZero() {
		t.Fatalf("an empty period is a legitimate period and still gets a root: %v", err)
	}
}

func TestPeriodSealRoundTripsAndRefusesEdits(t *testing.T) {
	root, _ := evidence.PeriodRoot(leaves(t, 3))
	p := evidence.PeriodSeal{
		TenantID:    id.NewTenantID(uuid.MustParse("01920000-0000-7000-8000-00000000aaaa")),
		Cell:        "eu-west-1a",
		PeriodStart: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		PeriodEnd:   time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC),
		LeafCount:   3,
		MerkleRoot:  root,
		SealedAt:    time.Date(2026, 9, 2, 1, 0, 0, 0, time.UTC),
	}
	encoded, err := evidence.EncodePeriodSeal(p)
	if err != nil {
		t.Fatal(err)
	}
	back, err := evidence.DecodePeriodSeal(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if back != p {
		t.Fatalf("round trip changed the payload: %+v", back)
	}
	edited := strings.Replace(string(encoded), `"leafCount":3`, `"leafCount": 3`, 1)
	if _, err := evidence.DecodePeriodSeal([]byte(edited)); err == nil {
		t.Fatal("a non-canonical payload was accepted")
	}

	p.SealedAt = p.PeriodEnd.Add(-time.Second)
	if _, err := evidence.EncodePeriodSeal(p); err == nil {
		t.Fatal("a seal dated inside its own period was accepted")
	}
}

func TestCompareResultsNamesTheFirstDivergence(t *testing.T) {
	a := []byte(`{"emitted":{},"trace":[{"node":"a","output":"1"},{"node":"b","output":"2"}]}`)
	b := []byte(`{"emitted":{},"trace":[{"node":"a","output":"1"},{"node":"b","output":"3"}]}`)
	match, where := evidence.CompareResults(a, b)
	if match || where != "$.trace[1].output" {
		t.Fatalf("got match=%v at %q", match, where)
	}
	if match, _ := evidence.CompareResults(a, a); !match {
		t.Fatal("identical bytes did not match")
	}
}

func result(t *testing.T) evidence.Result {
	t.Helper()
	rate, err := fiscal.ParseRate("0.2100", fiscal.RateBasis("NET"))
	if err != nil {
		t.Fatal(err)
	}
	qty, err := fiscal.ParseQuantity("3", "EA")
	if err != nil {
		t.Fatal(err)
	}
	return evidence.Result{
		EnvelopeDigest: canonical.SumBytes([]byte("envelope")),
		Outcome:        evidence.OutcomeAdvisory,
		Reason:         errs.ReasonNotAuthoritative,
		Emitted: map[string]rule.Value{
			"TAX_VAT":   {Type: rule.TypeMoney, Money: fiscaltest.Money(t, "21.11", "EUR")},
			"RATE":      {Type: rule.TypeRate, Rate: rate},
			"UNITS":     {Type: rule.TypeQuantity, Quantity: qty},
			"EXEMPT":    {Type: rule.TypeBool, Bool: false},
			"LABEL":     {Type: rule.TypeString, String: "standard"},
			"WHY_NOT":   {Type: rule.TypeReason, Reason: errs.ReasonNotAuthoritative},
			"ZERO_RATE": {Type: rule.TypeMoney, Money: fiscaltest.Money(t, "0.00", "EUR")},
		},
		Trace: []rule.TraceStep{
			{Node: "vat/net", Op: rule.OpInput, Output: "100.50", OutputType: rule.TypeMoney,
				RuleVersion: "2026.09.1", RuleSemanticID: "ZTAX-RULE-WORKED-VAT"},
			{Node: "vat/vat", Op: rule.OpApplyRate, Args: []rule.NodeID{"vat/net", "vat/applicable"},
				Output: "21.11", OutputType: rule.TypeMoney, RuleVersion: "2026.09.1",
				RuleSemanticID: "ZTAX-RULE-WORKED-VAT", Policy: "line2"},
		},
	}
}

func TestResultRoundTripsExactly(t *testing.T) {
	encoded, err := result(t).Encode()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := evidence.DecodeResult(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	again, err := decoded.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encoded, again) {
		t.Fatalf("round trip changed the result:\n%s\n%s", encoded, again)
	}
	// Scale survives: a zero-rated line reads back as 0.00, not 0.
	if got := decoded.Emitted["ZERO_RATE"].Money.String(); got != "0.00" {
		t.Fatalf("0.00 read back as %s", got)
	}
}

func TestDecodeResultRefusesReinterpretation(t *testing.T) {
	encoded, err := result(t).Encode()
	if err != nil {
		t.Fatal(err)
	}
	s := string(encoded)
	cases := map[string]string{
		"a fiscal amount as a JSON number (P1)": strings.Replace(s, `"amount":"21.11"`, `"amount":21.11`, 1),
		"an unknown member (P5)":                strings.Replace(s, `"outcome":"ADVISORY"`, `"outcome":"ADVISORY","extra":true`, 1),
		"a boolean carried as a string":         strings.Replace(s, `"type":"BOOL","value":false`, `"type":"BOOL","value":"false"`, 1),
		"an unknown value type":                 strings.Replace(s, `"type":"STRING"`, `"type":"TEXT"`, 1),
		"insignificant whitespace":              strings.Replace(s, `"outcome":"ADVISORY"`, `"outcome": "ADVISORY"`, 1),
		"trailing content":                      s + `{}`,
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			if doc == s {
				t.Fatal("the case did not alter the document; fix the test")
			}
			if _, err := evidence.DecodeResult([]byte(doc)); err == nil {
				t.Fatalf("accepted %s", doc)
			}
		})
	}
}
