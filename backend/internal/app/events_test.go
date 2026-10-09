package app_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
)

// The event registrations: the producer's catalog, the payload schemas and
// the AsyncAPI document must name the same events (contracts/asyncapi).

const (
	eventSchemaDir = "../../../contracts/schemas/events"
	asyncAPIDoc    = "../../../contracts/asyncapi/ztax.events.v1.yaml"
)

// schemaName is the file a schema reference names: ztax:events/<name>/<ver>.
func schemaName(t *testing.T, ref string) string {
	t.Helper()
	parts := strings.Split(ref, "/")
	if len(parts) != 3 || parts[0] != "ztax:events" || parts[1] == "" || parts[2] != "1.0.0" {
		t.Fatalf("schema reference %q is not ztax:events/<name>/1.0.0", ref)
	}
	return parts[1]
}

func TestEveryEmittedEventIsRegisteredInAllThreePlaces(t *testing.T) {
	asyncapi, err := os.ReadFile(asyncAPIDoc)
	if err != nil {
		t.Fatal(err)
	}
	doc := string(asyncapi)
	seenTypes, seenSchemas := map[string]bool{}, map[string]bool{}
	for _, e := range app.Events() {
		if seenTypes[e.Type] || seenSchemas[e.SchemaRef] {
			t.Errorf("%s / %s is registered twice", e.Type, e.SchemaRef)
		}
		seenTypes[e.Type], seenSchemas[e.SchemaRef] = true, true

		name := schemaName(t, e.SchemaRef)
		raw, err := os.ReadFile(filepath.Join(eventSchemaDir, name+".schema.json"))
		if err != nil {
			t.Errorf("%s: no payload schema: %v", e.Type, err)
			continue
		}
		var schema struct {
			Comment              string `json:"$comment"`
			AdditionalProperties *bool  `json:"additionalProperties"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		// The schema says who produces it, under which type and reference,
		// so a reader of the contract can find the code.
		if !strings.Contains(schema.Comment, e.Type) || !strings.Contains(schema.Comment, e.SchemaRef) {
			t.Errorf("%s.schema.json does not name its event type %s and reference %s", name, e.Type, e.SchemaRef)
		}
		// A payload schema that admits unknown members is one a producer can
		// drift away from without failing anything.
		if schema.AdditionalProperties == nil || *schema.AdditionalProperties {
			t.Errorf("%s.schema.json admits additional properties", name)
		}
		if !strings.Contains(doc, "name: "+e.Type+"\n") {
			t.Errorf("the AsyncAPI document has no message named %s", e.Type)
		}
		if !strings.Contains(doc, `"../schemas/events/`+name+`.schema.json"`) {
			t.Errorf("the AsyncAPI document does not reference %s.schema.json", name)
		}
	}

	// And the other way: no schema, and no message, for an event nothing
	// emits.
	files, err := filepath.Glob(filepath.Join(eventSchemaDir, "*.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".schema.json")
		if !seenSchemas["ztax:events/"+name+"/1.0.0"] {
			t.Errorf("%s has a schema and no producer", name)
		}
	}
	if got, want := strings.Count(doc, "name: com.zoikotax."), len(app.Events()); got != want {
		t.Errorf("the AsyncAPI document declares %d messages; the producer emits %d events", got, want)
	}
}

// The API contract's EventType enum is what a webhook subscription is
// validated against by every SDK, so it names exactly the emitted events.
func TestTheContractsEventTypesAreTheEmittedEvents(t *testing.T) {
	raw, err := os.ReadFile("../../../contracts/openapi/ztax.v1.yaml")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	start := strings.Index(doc, "\n    EventType:\n")
	if start < 0 {
		t.Fatal("the contract has no EventType schema")
	}
	block := doc[start:]
	block = block[strings.Index(block, "      enum:\n")+len("      enum:\n"):]
	var listed []string
	for _, line := range strings.Split(block, "\n") {
		item, ok := strings.CutPrefix(line, "        - ")
		if !ok {
			break
		}
		listed = append(listed, item)
	}
	if len(listed) != len(app.Events()) {
		t.Fatalf("the contract lists %d event types %v; the producer emits %d", len(listed), listed, len(app.Events()))
	}
	for _, e := range app.Events() {
		if !slices.Contains(listed, e.Type) {
			t.Errorf("the contract's EventType does not list %s", e.Type)
		}
	}
}
