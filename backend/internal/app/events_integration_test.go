//go:build integration

package app_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/obligation"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/settlement"
)

// Every event a real commit, correction, obligation move and refund writes to
// the outbox validates against the schema it names (ADR-0014 §2.6). This is
// the producer half of the event contract; events_test.go is the registry
// half.

// eventSchema is the subset of JSON Schema 2020-12 the event schemas use.
// Anything else in a schema is reported rather than silently ignored, so a
// schema that starts using a keyword this does not check fails here first.
type eventSchema struct {
	Type                 string                  `json:"type"`
	Required             []string                `json:"required"`
	Properties           map[string]*eventSchema `json:"properties"`
	AdditionalProperties *bool                   `json:"additionalProperties"`
	DependentRequired    map[string][]string     `json:"dependentRequired"`
	Const                any                     `json:"const"`
	Enum                 []any                   `json:"enum"`
	Pattern              string                  `json:"pattern"`
	Format               string                  `json:"format"`
	MinLength            *int                    `json:"minLength"`
	MaxLength            *int                    `json:"maxLength"`
	Minimum              *float64                `json:"minimum"`
	Items                *eventSchema            `json:"items"`
}

var knownSchemaKeywords = []string{
	"$schema", "title", "description", "$comment", "type", "required", "properties", "additionalProperties",
	"dependentRequired", "const", "enum", "pattern", "format", "minLength", "maxLength", "minimum", "items",
}

func checkKeywords(t *testing.T, where string, raw json.RawMessage) {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("%s: %v", where, err)
	}
	for k, v := range m {
		if !slices.Contains(knownSchemaKeywords, k) {
			t.Fatalf("%s uses %q, which this check does not evaluate", where, k)
		}
		if k == "items" {
			checkKeywords(t, where+"[]", v)
		}
		if k == "properties" {
			var props map[string]json.RawMessage
			if err := json.Unmarshal(v, &props); err != nil {
				t.Fatal(err)
			}
			for name, p := range props {
				checkKeywords(t, where+"."+name, p)
			}
		}
	}
}

func loadEventSchema(t *testing.T, ref string) *eventSchema {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(eventSchemaDir, schemaName(t, ref)+".schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	checkKeywords(t, ref, raw)
	var s eventSchema
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	return &s
}

var (
	uuidForm     = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	dateTimeForm = "2006-01-02T15:04:05.000000Z"
)

// validate reports every way v fails s.
func (s *eventSchema) validate(path string, v any) []string {
	var out []string
	bad := func(format string, args ...any) { out = append(out, path+": "+fmt.Sprintf(format, args...)) }
	if s.Const != nil && fmt.Sprint(v) != fmt.Sprint(s.Const) {
		bad("is %v, want the constant %v", v, s.Const)
	}
	if len(s.Enum) > 0 && !slices.ContainsFunc(s.Enum, func(e any) bool { return fmt.Sprint(e) == fmt.Sprint(v) }) {
		bad("%v is not one of %v", v, s.Enum)
	}
	switch s.Type {
	case "object":
		obj, ok := v.(map[string]any)
		if !ok {
			bad("is not an object")
			return out
		}
		for _, r := range s.Required {
			if _, ok := obj[r]; !ok {
				bad("lacks required %q", r)
			}
		}
		for k, deps := range s.DependentRequired {
			if _, ok := obj[k]; ok {
				for _, d := range deps {
					if _, ok := obj[d]; !ok {
						bad("has %q without %q", k, d)
					}
				}
			}
		}
		keys := make([]string, 0, len(obj))
		for k := range obj {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			p, ok := s.Properties[k]
			if !ok {
				if s.AdditionalProperties != nil && !*s.AdditionalProperties {
					bad("has undeclared member %q", k)
				}
				continue
			}
			out = append(out, p.validate(path+"."+k, obj[k])...)
		}
	case "string":
		str, ok := v.(string)
		if !ok {
			bad("is not a string")
			return out
		}
		if s.MinLength != nil && len(str) < *s.MinLength || s.MaxLength != nil && len(str) > *s.MaxLength {
			bad("length %d is out of bounds", len(str))
		}
		if s.Pattern != "" && !regexp.MustCompile(s.Pattern).MatchString(str) {
			bad("%q does not match %s", str, s.Pattern)
		}
		switch s.Format {
		case "uuid":
			if !uuidForm.MatchString(str) {
				bad("%q is not a lowercase UUID", str)
			}
		case "date-time":
			if _, err := time.Parse(dateTimeForm, str); err != nil {
				bad("%q is not a canon/v1 timestamp", str)
			}
		}
	case "array":
		arr, ok := v.([]any)
		if !ok {
			bad("is not an array")
			return out
		}
		if s.Items != nil {
			for i, item := range arr {
				out = append(out, s.Items.validate(fmt.Sprintf("%s[%d]", path, i), item)...)
			}
		}
	case "integer":
		n, ok := v.(float64)
		if !ok || n != float64(int64(n)) {
			bad("is not an integer")
			return out
		}
		if s.Minimum != nil && n < *s.Minimum {
			bad("%v is below %v", n, *s.Minimum)
		}
	}
	return out
}

func TestIntegrationEveryEmittedEventMatchesItsSchema(t *testing.T) {
	c := openFiscalCell(t)
	refunds := c.refunds()

	// A commit, a correction of it, a user's move on the obligation, and a
	// refund taken through to completion: every event kind the cell emits
	// bar the threshold crossing, which takes 10000.00 of levy to reach and
	// is validated here whenever a run produces one.
	original := c.mustCommit(t, "k-ev1", "INV-EV/1", "100.00", "3", nil)
	corrected := c.mustCommit(t, "k-ev2", "INV-EV/1", "50.00", "3", &original)
	views, err := c.svc.Obligations(c.ctx, "", 0)
	if err != nil || len(views) != 1 {
		t.Fatalf("obligations: %v %d", err, len(views))
	}
	if _, err := c.svc.TransitionObligation(c.ctx, views[0].ID, obligation.StatusReady); err != nil {
		t.Fatal(err)
	}
	rf := c.mustRefund(t, refunds, "ev-r1", corrected, "1.00")
	if _, err := refunds.Report(c.ctx, rf, app.ReportInput{Outcome: settlement.RefundSucceeded, ExternalRef: "psp:re_ev"}); err != nil {
		t.Fatal(err)
	}
	mustDoc(t)(c.issue(t, c.documents(), "ev-doc", c.docLine(t, corrected, "50.00", "10.50", "0.11")))

	rows, err := ownerPool(t).Query(c.ctx,
		`SELECT event_type, schema_ref, payload::text FROM ztax.outbox WHERE tenant_id = $1 ORDER BY created_at, id`,
		c.tenant.UUID())
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	registered := map[string]string{}
	for _, e := range app.Events() {
		registered[e.Type] = e.SchemaRef
	}
	seen := map[string]int{}
	for rows.Next() {
		var typ, ref, payload string
		if err := rows.Scan(&typ, &ref, &payload); err != nil {
			t.Fatal(err)
		}
		seen[typ]++
		if registered[typ] != ref {
			t.Errorf("%s was written with schema %s; the catalog registers %q", typ, ref, registered[typ])
			continue
		}
		var v any
		if err := json.Unmarshal([]byte(payload), &v); err != nil {
			t.Fatal(err)
		}
		for _, problem := range loadEventSchema(t, ref).validate(typ, v) {
			t.Error(problem)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for typ, want := range map[string]int{
		app.EventDecisionCommitted:       1,
		app.EventDecisionCorrected:       1,
		app.EventObligationStatusChanged: 2, // created OPEN, then READY
		app.EventRefundRequested:         1,
		app.EventRefundStatusChanged:     1,
		app.EventDocumentCommitted:       1,
	} {
		if seen[typ] != want {
			t.Errorf("%d %s events, want %d", seen[typ], typ, want)
		}
	}
}
