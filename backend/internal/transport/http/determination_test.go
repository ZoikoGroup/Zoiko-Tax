package http

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/transport/http/gen"
)

// commitBody is the worked pack's line with one substitution at the amount.
func commitBody(amount string) string {
	return `{"businessKey":"INV-0001/1","eventTime":"2026-09-24T18:00:00.000000Z",` +
		`"input":{"money":{"line.netAmount":{"amount":` + amount + `,"currency":"EUR"}},` +
		`"quantities":{"line.quantity":{"value":"3","unit":"EA"}},` +
		`"flags":{"line.reducedRateApplies":false,"line.exemptCertificateHeld":false}},` +
		`"accumulators":{"threshold.ecoLevyYtd":{"amount":"9999.99","currency":"EUR"}}}`
}

func reasonOf(t *testing.T, err error) errs.ReasonCode {
	t.Helper()
	var e *errs.Error
	if !errors.As(err, &e) {
		t.Fatalf("want a refusal, got %v", err)
	}
	return e.Reason
}

// decodeCommit runs a body through exactly what handleCommit does before the
// service: strict decoding, then ingress parsing.
func decodeCommit(body string) error {
	var req gen.CommitRequest
	if err := decodeJSON(httptest.NewRequest("POST", "/v1/transactions:commit", strings.NewReader(body)), &req); err != nil {
		return err
	}
	_, err := commitInput(req)
	return err
}

func TestCommitIngressAcceptsTheContract(t *testing.T) {
	if err := decodeCommit(commitBody(`"100.00"`)); err != nil {
		t.Fatalf("the contract's own example was refused: %v", err)
	}
}

// ADR-0010 §2.9 and ADR-0011 P1, P3, P5 at the one boundary where a fiscal
// amount enters the cell.
func TestCommitIngressRefusesEveryOtherSpelling(t *testing.T) {
	cases := []struct {
		name string
		body string
		want errs.ReasonCode
	}{
		{"an amount as a JSON number", commitBody(`100.00`), errs.ReasonMalformedRequest},
		{"an amount as null", commitBody(`null`), errs.ReasonInvalidValue},
		{"an exponent", commitBody(`"1.0E+2"`), errs.ReasonInvalidValue},
		{"a leading plus", commitBody(`"+100.00"`), errs.ReasonInvalidValue},
		{"a leading zero", commitBody(`"0100.00"`), errs.ReasonInvalidValue},
		{"infinity", commitBody(`"Infinity"`), errs.ReasonInvalidValue},
		{"a lower-case currency", strings.Replace(commitBody(`"100.00"`), `"EUR"}}`, `"eur"}}`, 1), errs.ReasonInvalidValue},
		{"a miscased field inside an amount", commitBody(`"100.00","Currency":"USD"`), errs.ReasonUnknownField},
		{"an undeclared field inside the input", strings.Replace(commitBody(`"100.00"`), `"input":{`, `"input":{"dates":{},`, 1), errs.ReasonUnknownField},
		{"a repeated pack name", strings.Replace(commitBody(`"100.00"`), `"flags":{`, `"flags":{"line.exemptCertificateHeld":true,`, 1), errs.ReasonUnknownField},
		{"an instant at another precision", strings.Replace(commitBody(`"100.00"`), "18:00:00.000000Z", "18:00:00Z", 1), errs.ReasonInvalidValue},
		{"an instant with an offset", strings.Replace(commitBody(`"100.00"`), "18:00:00.000000Z", "18:00:00.000000+00:00", 1), errs.ReasonInvalidValue},
		{"a malformed supersedes", strings.Replace(commitBody(`"100.00"`), `{"businessKey"`, `{"supersedes":"ztd_1","businessKey"`, 1), errs.ReasonInvalidValue},
		{"no input values at all", strings.Replace(commitBody(`"100.00"`), `"input":{"money"`, `"input":{},"x":{"money"`, 1), errs.ReasonUnknownField},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := reasonOf(t, decodeCommit(tc.body)); got != tc.want {
				t.Fatalf("refused with %s, want %s\n%s", got, tc.want, tc.body)
			}
		})
	}
}

func TestEmptyInputIsRefused(t *testing.T) {
	body := `{"businessKey":"k","eventTime":"2026-09-24T18:00:00.000000Z","input":{}}`
	if got := reasonOf(t, decodeCommit(body)); got != errs.ReasonMissingField {
		t.Fatalf("an empty input: %s", got)
	}
}
