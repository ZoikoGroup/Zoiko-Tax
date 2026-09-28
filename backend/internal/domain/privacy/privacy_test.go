package privacy_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/privacy"
)

func TestMetadataRules(t *testing.T) {
	ok := []privacy.Metadata{
		{Class: privacy.P0},
		{Class: privacy.P1, Purposes: []privacy.Purpose{privacy.PurposeSec}, Retention: privacy.RetentionOperational, Redaction: privacy.RedactNone},
		{Class: privacy.P7, Purposes: []privacy.Purpose{privacy.PurposeSec}, Retention: privacy.RetentionTransient, Redaction: privacy.RedactNoLog},
	}
	for _, m := range ok {
		if err := m.Validate(); err != nil {
			t.Errorf("%+v: %v", m, err)
		}
	}
	bad := map[string]privacy.Metadata{
		"unknown class":        {Class: "P9"},
		"personal, no purpose": {Class: privacy.P1, Retention: privacy.RetentionOperational, Redaction: privacy.RedactValue},
		"personal, no retention": {Class: privacy.P2, Purposes: []privacy.Purpose{privacy.PurposeSec},
			Redaction: privacy.RedactValue},
		"P3 logged as-is": {Class: privacy.P3, Purposes: []privacy.Purpose{privacy.PurposeSec},
			Retention: privacy.RetentionSecurity, Redaction: privacy.RedactNone},
		"secret redacted but logged": {Class: privacy.P7, Purposes: []privacy.Purpose{privacy.PurposeSec},
			Retention: privacy.RetentionTransient, Redaction: privacy.RedactValue},
		"special category": {Class: privacy.P6, Purposes: []privacy.Purpose{privacy.PurposeLegal},
			Retention: privacy.RetentionLegalHold, Redaction: privacy.RedactNoLog},
		"unapproved purpose": {Class: privacy.P1, Purposes: []privacy.Purpose{"PURP-MARKETING"},
			Retention: privacy.RetentionOperational, Redaction: privacy.RedactValue},
	}
	for name, m := range bad {
		if err := m.Validate(); err == nil {
			t.Errorf("%s: accepted %+v", name, m)
		}
	}
}

// ADR-0015 §2.2: logging a classified value emits the redacted form because
// that is the only form it has — through slog, through fmt, and through JSON.
func TestClassifiedValueHasNoUnredactedRendering(t *testing.T) {
	const secret = "ada@acme.example"
	v := privacy.Email(secret)

	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	log.Info("user created", "email", v)
	log.Info("nested", slog.Group("user", "email", v))

	rendered := []string{
		buf.String(),
		fmt.Sprintf("%v %s %q %+v %#v", v, v, v, v, v),
		fmt.Sprint(v),
		mustJSON(t, map[string]any{"email": v}),
		mustJSON(t, struct{ Email privacy.Value }{v}),
	}
	for _, r := range rendered {
		if strings.Contains(r, secret) {
			t.Fatalf("the content escaped: %s", r)
		}
		if !strings.Contains(r, "[redacted:email:P1]") {
			t.Fatalf("the redacted form is missing: %s", r)
		}
	}
	if v.Reveal() != secret {
		t.Fatal("Reveal must return the content")
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
