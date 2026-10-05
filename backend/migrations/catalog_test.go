package migrations

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/privacy"
)

// Schema-review CI for field-level privacy on the database (PRIV-001 §33's
// second P0 item; G-PRIV-01; PRIV-REQ-0001, -0002, -0003).
//
// The contract lint holds every API field to a privacy classification. This
// holds every database column to the same thing, because the column is where
// the data actually lives and is retained: a field the API never exposes is
// still personal data on disk. privacy_catalog.json classifies every column of
// every table the migrations create, and this test fails when
//
//   - a migration creates or adds a column the catalogue does not classify —
//     the review this enforces is "say what it is before it ships";
//   - the catalogue classifies a column no migration creates — a stale entry is
//     a classification that describes nothing, and the next column to take the
//     name would inherit it unreviewed;
//   - a classification is not valid privacy.Metadata — the same rules the
//     contract lint and privacy.Metadata.Validate apply, so a personal-class
//     column without a purpose, a retention policy and a redaction policy
//     fails here exactly as an API field would.
//
// The schema is derived by replaying the up migrations in order — CREATE TABLE
// columns, then ALTER TABLE ADD/DROP/RENAME COLUMN — so a column one migration
// adds and a later one drops is not demanded, and a column replaced in place
// (000006's response_body) is demanded once.

const catalogFile = "privacy_catalog.json"

type catalogEntry struct {
	Class          privacy.Class          `json:"class"`
	Purposes       []privacy.Purpose      `json:"purposes"`
	Retention      privacy.Retention      `json:"retention"`
	Redaction      privacy.Redaction      `json:"redaction"`
	EvidencePolicy privacy.EvidencePolicy `json:"evidencePolicy"`
	AIAllowed      privacy.AIAllowed      `json:"aiAllowed"`
	Note           string                 `json:"note"`
}

func (e catalogEntry) metadata() privacy.Metadata {
	return privacy.Metadata{
		Class: e.Class, Purposes: e.Purposes, Retention: e.Retention, Redaction: e.Redaction,
		EvidencePolicy: e.EvidencePolicy, AIAllowed: e.AIAllowed,
	}
}

func TestEveryColumnIsPrivacyClassified(t *testing.T) {
	schema, err := replaySchema(FS)
	if err != nil {
		t.Fatal(err)
	}
	if len(schema) == 0 {
		t.Fatal("no tables found in the migrations; the parser is broken, not the schema")
	}

	raw, err := os.ReadFile(catalogFile)
	if err != nil {
		t.Fatalf("read %s: %v", catalogFile, err)
	}
	var catalog struct {
		Comment string                             `json:"$comment"`
		Tables  map[string]map[string]catalogEntry `json:"tables"`
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&catalog); err != nil {
		t.Fatalf("decode %s: %v", catalogFile, err)
	}

	for _, table := range sortedKeys(schema) {
		entries, ok := catalog.Tables[table]
		if !ok {
			t.Errorf("table ztax.%s is created by a migration and absent from %s", table, catalogFile)
			continue
		}
		for _, col := range schema[table] {
			e, ok := entries[col]
			if !ok {
				t.Errorf("column ztax.%s.%s is not privacy-classified in %s", table, col, catalogFile)
				continue
			}
			if err := e.metadata().Validate(); err != nil {
				t.Errorf("column ztax.%s.%s: %v", table, col, err)
			}
		}
	}
	for _, table := range sortedKeys(catalog.Tables) {
		cols, ok := schema[table]
		if !ok {
			t.Errorf("%s classifies table %s, which no migration creates", catalogFile, table)
			continue
		}
		for _, col := range sortedKeys(catalog.Tables[table]) {
			if !slices.Contains(cols, col) {
				t.Errorf("%s classifies column %s.%s, which no migration creates", catalogFile, table, col)
			}
		}
	}
}

// The parser is tested on the shapes the migrations actually use, so that a
// parser regression shows up as itself rather than as a wall of "unclassified
// column" failures.
func TestReplaySchemaUnderstandsTheMigrationShapes(t *testing.T) {
	sql := `
-- a comment; with a semicolon
CREATE TABLE ztax.a (
  tenant_id uuid NOT NULL REFERENCES ztax.tenant (tenant_id),
  amount    numeric(12, 2) NOT NULL, -- trailing comment, with a comma
  boundary  geography(MULTIPOLYGON, 4326) NULL,
  PRIMARY KEY (tenant_id),
  CONSTRAINT a_ok CHECK (amount IN (1, 2)),
  FOREIGN KEY (tenant_id) REFERENCES ztax.tenant (tenant_id),
  UNIQUE (tenant_id, amount)
);
DO $$ BEGIN PERFORM 1; END $$;
ALTER TABLE ztax.a ADD COLUMN extra text NULL, ADD CONSTRAINT x CHECK (extra <> '');
ALTER TABLE ztax.a DROP COLUMN boundary;
ALTER TABLE ztax.a ADD COLUMN boundary bytea NULL;
ALTER TABLE ztax.a RENAME COLUMN extra TO more;
CREATE INDEX a_idx ON ztax.a (tenant_id);
`
	schema := map[string][]string{}
	if err := apply(schema, sql); err != nil {
		t.Fatal(err)
	}
	want := []string{"tenant_id", "amount", "more", "boundary"}
	if got := schema["a"]; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// replaySchema applies every *.up.sql in version order and returns the
// resulting columns per table.
func replaySchema(fsys fs.FS) (map[string][]string, error) {
	names, err := fs.Glob(fsys, "*.up.sql")
	if err != nil {
		return nil, err
	}
	sort.Strings(names) // the six-digit prefix makes lexical order version order
	schema := map[string][]string{}
	for _, name := range names {
		b, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, err
		}
		if err := apply(schema, string(b)); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
	}
	return schema, nil
}

var (
	lineComment = regexp.MustCompile(`--[^\n]*`)
	dollarBlock = regexp.MustCompile(`(?s)\$\$.*?\$\$`)
	createTable = regexp.MustCompile(`(?is)^CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?ztax\.(\w+)\s*\((.*)\)\s*$`)
	alterTable  = regexp.MustCompile(`(?is)^ALTER\s+TABLE\s+(?:IF\s+EXISTS\s+)?(?:ONLY\s+)?ztax\.(\w+)\s+(.*)$`)
	dropTable   = regexp.MustCompile(`(?is)^DROP\s+TABLE\s+(?:IF\s+EXISTS\s+)?ztax\.(\w+)`)
	addColumn   = regexp.MustCompile(`(?is)^ADD\s+COLUMN\s+(?:IF\s+NOT\s+EXISTS\s+)?(\w+)\s`)
	dropColumn  = regexp.MustCompile(`(?is)^DROP\s+COLUMN\s+(?:IF\s+EXISTS\s+)?(\w+)`)
	renameCol   = regexp.MustCompile(`(?is)^RENAME\s+COLUMN\s+(\w+)\s+TO\s+(\w+)`)
	notAColumn  = regexp.MustCompile(`(?i)^(CONSTRAINT|PRIMARY|FOREIGN|UNIQUE|CHECK|EXCLUDE|LIKE)\b`)
)

func apply(schema map[string][]string, sql string) error {
	sql = lineComment.ReplaceAllString(sql, "")
	sql = dollarBlock.ReplaceAllString(sql, "")
	for _, stmt := range strings.Split(sql, ";") {
		stmt = strings.TrimSpace(stmt)
		switch {
		case createTable.MatchString(stmt):
			m := createTable.FindStringSubmatch(stmt)
			table := strings.ToLower(m[1])
			if _, exists := schema[table]; exists {
				return fmt.Errorf("table %s created twice", table)
			}
			var cols []string
			for _, item := range splitTopLevel(m[2]) {
				if item == "" || notAColumn.MatchString(item) {
					continue
				}
				cols = append(cols, strings.ToLower(strings.Fields(item)[0]))
			}
			schema[table] = cols
		case alterTable.MatchString(stmt):
			m := alterTable.FindStringSubmatch(stmt)
			table := strings.ToLower(m[1])
			cols, ok := schema[table]
			if !ok {
				return fmt.Errorf("ALTER TABLE on unknown table %s", table)
			}
			for _, action := range splitTopLevel(m[2]) {
				switch {
				case addColumn.MatchString(action):
					c := strings.ToLower(addColumn.FindStringSubmatch(action)[1])
					if slices.Contains(cols, c) {
						return fmt.Errorf("column %s.%s added twice", table, c)
					}
					cols = append(cols, c)
				case dropColumn.MatchString(action):
					c := strings.ToLower(dropColumn.FindStringSubmatch(action)[1])
					cols = slices.DeleteFunc(cols, func(x string) bool { return x == c })
				case renameCol.MatchString(action):
					r := renameCol.FindStringSubmatch(action)
					from, to := strings.ToLower(r[1]), strings.ToLower(r[2])
					for i := range cols {
						if cols[i] == from {
							cols[i] = to
						}
					}
				}
			}
			schema[table] = cols
		case dropTable.MatchString(stmt):
			delete(schema, strings.ToLower(dropTable.FindStringSubmatch(stmt)[1]))
		}
	}
	return nil
}

// splitTopLevel splits on commas outside parentheses and quotes.
func splitTopLevel(s string) []string {
	var (
		out   []string
		depth int
		quote bool
		start int
	)
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\'':
			quote = !quote
		case quote:
		case c == '(':
			depth++
		case c == ')':
			depth--
		case c == ',' && depth == 0:
			out = append(out, strings.TrimSpace(s[start:i]))
			start = i + 1
		}
	}
	return append(out, strings.TrimSpace(s[start:]))
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
