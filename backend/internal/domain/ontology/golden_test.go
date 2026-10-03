package ontology_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/ontology"
	"github.com/zoikogroup/zoikotax/backend/internal/fiscaltest"
	"github.com/zoikogroup/zoikotax/backend/internal/goldentest"
)

// The registered launch-critical classification corpus
// (testdata/golden/classification): every launch family resolves through a
// catalog, and the bundle, mixed, conflicting, unsupported and review cases
// come out as ZTAX-CLS-001 requires (ZTAX-CLS-REQ-0120 to -0123).

const classificationCorpus = "../../../testdata/golden/classification"

type corpusFixtures struct {
	OntologyVersion string `json:"ontologyVersion"`
	Nodes           []struct {
		ID     string `json:"id"`
		Family string `json:"family"`
	} `json:"nodes"`
	Mappings []struct {
		ID       string `json:"id"`
		Tenant   string `json:"tenant"`
		Code     string `json:"code"`
		Target   string `json:"target"`
		Source   string `json:"source"`
		Promoted bool   `json:"promoted"`
	} `json:"mappings"`
}

type corpusCase struct {
	ID     string   `json:"id"`
	Kind   string   `json:"kind"`
	Oracle string   `json:"oracle"`
	Reqs   []string `json:"requirements"`
	Tenant string   `json:"tenant"`
	Code   string   `json:"code"`
	Line   string   `json:"line"`
	Amount string   `json:"amount"`
	Parts  []struct {
		Node       string `json:"node"`
		Standalone string `json:"standalone"`
	} `json:"parts"`
	Expect struct {
		Status     string   `json:"status"`
		Target     string   `json:"target"`
		Candidates int      `json:"candidates"`
		Amounts    []string `json:"amounts"`
	} `json:"expect"`
}

func catalogSystem() (string, string) { return "tenant-catalog", "2027.1" }

func TestGoldenClassificationLaunchCorpus(t *testing.T) {
	for _, set := range goldentest.Load(t, classificationCorpus) {
		var fx corpusFixtures
		if err := strictDecode(set.Fixtures, &fx); err != nil {
			t.Fatalf("fixtures: %v", err)
		}
		var nodes []ontology.Node
		for _, n := range fx.Nodes {
			nodes = append(nodes, ontology.Node{
				ID: ontology.ID(n.ID), Version: 1, Family: ontology.Family(n.Family), Name: n.ID,
				Definition: "Launch corpus node " + n.ID, Owner: "lane-g", Lifecycle: ontology.LifecycleActive,
				Nature: ontology.NatureService, ValidFrom: t2026, RecordedAt: t2026,
			})
		}
		o, err := ontology.New(fx.OntologyVersion, nodes, nil)
		if err != nil {
			t.Fatalf("fixture ontology: %v", err)
		}
		system, version := catalogSystem()
		var maps []ontology.CatalogMapping
		for _, m := range fx.Mappings {
			cm := ontology.CatalogMapping{
				ID: m.ID, Version: 1, External: ontology.ExternalCode{System: system, SystemVersion: version, Code: m.Code},
				Target: ontology.ID(m.Target), Source: ontology.MappingSource(m.Source), Status: ontology.MappingActive,
				EffectiveFrom: t2026, Promoted: m.Promoted, Charge: ontology.ChargeRecurringAccess,
			}
			if m.Tenant != "" {
				cm.Tenant = id.NewTenantID(uuid.MustParse(m.Tenant))
			}
			if cm.Source == ontology.SourceAIProposal {
				cm.Evidence = "airm:corpus"
			}
			if err := cm.Validate(o); err != nil {
				t.Fatalf("fixture mapping %s: %v", m.ID, err)
			}
			maps = append(maps, cm)
		}

		t.Run(set.Set, func(t *testing.T) {
			for _, raw := range set.Cases {
				var c corpusCase
				if err := strictDecode(raw, &c); err != nil {
					t.Fatalf("case: %v", err)
				}
				t.Run(c.ID, func(t *testing.T) {
					switch c.Kind {
					case "resolve":
						runResolve(t, c, maps)
					case "bundle":
						runBundle(t, c)
					default:
						t.Fatalf("case kind %q", c.Kind)
					}
				})
			}
		})
	}
}

func runResolve(t *testing.T, c corpusCase, maps []ontology.CatalogMapping) {
	system, version := catalogSystem()
	tenant := id.NewTenantID(uuid.MustParse(c.Tenant))
	r := ontology.Resolve(tenant, ontology.ExternalCode{System: system, SystemVersion: version, Code: c.Code}, when, maps)
	if string(r.Status) != c.Expect.Status {
		t.Fatalf("status %s, want %s", r.Status, c.Expect.Status)
	}
	if c.Expect.Target != "" && (r.Chosen == nil || string(r.Chosen.Target) != c.Expect.Target) {
		t.Fatalf("chose %+v, want %s", r.Chosen, c.Expect.Target)
	}
	if c.Expect.Candidates != 0 && len(r.Candidates) != c.Expect.Candidates {
		t.Fatalf("%d candidates kept, want %d", len(r.Candidates), c.Expect.Candidates)
	}
}

func runBundle(t *testing.T, c corpusCase) {
	amount := fiscaltest.Money(t, c.Amount, "EUR")
	b := ontology.Bundle{LineRef: c.Line, Amount: amount, Method: "STANDALONE_SELLING_PRICE", Provenance: "corpus"}
	for i, p := range c.Parts {
		v := fiscaltest.Money(t, p.Standalone, "EUR")
		b.Parts = append(b.Parts, ontology.BundlePart{
			Component:  ontology.ComponentInstance{ID: c.Line + "/" + string(rune('a'+i)), Node: ontology.ID(p.Node)},
			Standalone: &v,
		})
	}
	lines, err := ontology.Decompose(b, fiscaltest.LinePolicy(t, fiscal.RoundHalfUp, 2))
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != len(c.Expect.Amounts) {
		t.Fatalf("%d lines, want %d", len(lines), len(c.Expect.Amounts))
	}
	sum := amount.Zero()
	for i, l := range lines {
		if l.Amount.CanonicalString() != c.Expect.Amounts[i] || l.LineRef != c.Line {
			t.Errorf("part %d: %s on %s, want %s on %s", i, l.Amount.CanonicalString(), l.LineRef, c.Expect.Amounts[i], c.Line)
		}
		sum, _ = sum.Add(l.Amount)
	}
	if cmp, _ := sum.Cmp(amount); cmp != 0 {
		t.Fatalf("parts sum to %s; the line was %s", sum, amount)
	}
}

func strictDecode(raw json.RawMessage, into any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode(into)
}
