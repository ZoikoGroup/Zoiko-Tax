package telemetry_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/privacy"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/telemetry"
)

func logger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(telemetry.NewRedactingHandler(slog.NewJSONHandler(&buf, nil))), &buf
}

func lines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("not JSON: %s", l)
		}
		out = append(out, m)
	}
	return out
}

func TestDenylistedKeysAreWithheld(t *testing.T) {
	log, buf := logger()
	log.Info("bootstrap",
		"admin_email", "ada@acme.example",
		"client-ip", "203.0.113.9",
		"user_agent", "Mozilla/5.0",
		"password", "correct horse battery staple",
		"session.token", "abc123",
		"tenant", "acme",
		"emailed_count", 3,
	)
	l := lines(t, buf)[0]
	want := map[string]any{
		"admin_email":   "[redacted:email]",
		"client-ip":     "[redacted:network]",
		"user_agent":    "[redacted:network]",
		"password":      "[redacted:secret]",
		"session.token": "[redacted:secret]",
		"tenant":        "acme",
		"emailed_count": float64(3),
	}
	for k, v := range want {
		if l[k] != v {
			t.Errorf("%s = %v, want %v", k, l[k], v)
		}
	}
}

// ADR-0015 §2.3: fiscal amounts never appear in telemetry.
func TestFiscalAmountsAreWithheld(t *testing.T) {
	log, buf := logger()
	log.Info("decision", "line.tax", "21.00", "total_tax", "21.00", "decision.id", "d-1", "tax_rule_id", "r-7")
	l := lines(t, buf)[0]
	if l["line.tax"] != "[redacted:fiscal]" || l["total_tax"] != "[redacted:fiscal]" {
		t.Fatalf("a fiscal amount reached the log: %v", l)
	}
	if l["decision.id"] != "d-1" || l["tax_rule_id"] != "r-7" {
		t.Fatalf("an identifier was withheld as though it were an amount: %v", l)
	}
}

// The denylist protects keys; this protects the error string that carries an
// address past every key.
func TestValuesAreScrubbedWhateverTheKey(t *testing.T) {
	log, buf := logger()
	log.Warn("request failed", "error", errors.New("user grace@acme.example already exists"),
		"detail", "sent Authorization: Bearer eyJhbGciOi.payload.sig")
	l := lines(t, buf)[0]
	for _, k := range []string{"error", "detail"} {
		s, _ := l[k].(string)
		if strings.Contains(s, "grace@acme.example") || strings.Contains(s, "eyJhbGciOi") {
			t.Fatalf("%s leaked: %q", k, s)
		}
	}
	if !strings.Contains(l["error"].(string), "[redacted:email]") {
		t.Fatalf("error lost its shape: %v", l["error"])
	}
}

func TestBoundAndGroupedAttributesAreRedactedToo(t *testing.T) {
	log, buf := logger()
	log.With("owner_email", "a@b.example").Info("x", slog.Group("user", "email", "c@d.example", "role", "ADMIN"))
	l := lines(t, buf)[0]
	if l["owner_email"] != "[redacted:email]" {
		t.Fatalf("a With attribute leaked: %v", l)
	}
	user, _ := l["user"].(map[string]any)
	if user["email"] != "[redacted:email]" || user["role"] != "ADMIN" {
		t.Fatalf("a grouped attribute leaked or was over-redacted: %v", user)
	}
}

// The structural mechanism and the backstop together: a classified value keeps
// its own, more informative, redacted form.
func TestClassifiedValuesKeepTheirOwnRendering(t *testing.T) {
	log, buf := logger()
	log.Info("x", "admin_email", privacy.Email("ada@acme.example"), "contact", privacy.Email("b@c.example"))
	l := lines(t, buf)[0]
	if l["admin_email"] != "[redacted:email:P1]" || l["contact"] != "[redacted:email:P1]" {
		t.Fatalf("got %v", l)
	}
}

func TestMessagesAreScrubbed(t *testing.T) {
	log, buf := logger()
	log.Info("could not reach ada@acme.example")
	if strings.Contains(buf.String(), "ada@acme.example") {
		t.Fatalf("a message leaked: %s", buf.String())
	}
}
