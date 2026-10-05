package classification_test

import (
	"testing"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/classification"
)

var (
	from    = time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	content = classification.ContentRef{BundleID: "xa-2027.1", BundleDigest: "zt1:00", RuleID: "cls-voice"}
)

func taxability(status classification.Status, category string, candidates ...string) classification.TaxabilityDecision {
	return classification.TaxabilityDecision{
		ComponentInstance: "c1", Node: "ontology:telecom/voice/mobile", OntologyVersion: "ont-2027.1",
		Jurisdiction: "jurisdiction:xa", Category: category, Status: status, Provenance: content,
		Effective: classification.Effective{From: from}, Candidates: candidates,
	}
}

func revenue(t classification.RevenueTreatment) classification.RegulatoryRevenueDecision {
	return classification.RegulatoryRevenueDecision{
		ComponentInstance: "c1", Node: "ontology:telecom/voice/mobile", OntologyVersion: "ont-2027.1",
		Regime: "regime:xa-universal-service", Treatment: t, Status: classification.StatusResolved, Provenance: content,
		Effective: classification.Effective{From: from},
	}
}

func TestCLSREQ0004To0006DualDecisionsAreSeparateAndEachEffectiveDated(t *testing.T) {
	tax := taxability(classification.StatusResolved, "STANDARD")
	rev := revenue(classification.RevenueIncluded)
	if err := tax.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := rev.Validate(); err != nil {
		t.Fatal(err)
	}
	// Different effective periods for one component are fine — they are
	// different questions.
	later := from.AddDate(0, 6, 0)
	rev.Effective.From = later
	if err := rev.Validate(); err != nil {
		t.Fatal(err)
	}
	tax.Effective = classification.Effective{}
	if err := tax.Validate(); err == nil {
		t.Fatal("a taxability decision with no effective date validated")
	}
}

func TestCLSREQ0063To0066TaxabilityPinsComponentOntologyAndContent(t *testing.T) {
	for name, mutate := range map[string]func(*classification.TaxabilityDecision){
		"component":        func(d *classification.TaxabilityDecision) { d.ComponentInstance = "" },
		"ontology version": func(d *classification.TaxabilityDecision) { d.OntologyVersion = "" },
		"content":          func(d *classification.TaxabilityDecision) { d.Provenance = classification.ContentRef{} },
		"status":           func(d *classification.TaxabilityDecision) { d.Status = "MAYBE" },
	} {
		d := taxability(classification.StatusResolved, "STANDARD")
		mutate(&d)
		if err := d.Validate(); err == nil {
			t.Errorf("a decision with no %s validated", name)
		}
	}
}

func TestCLSREQ0067To0070RegulatoryRevenueTreatments(t *testing.T) {
	for _, tr := range []classification.RevenueTreatment{classification.RevenueIncluded, classification.RevenueExcluded} {
		if err := revenue(tr).Validate(); err != nil {
			t.Errorf("%s: %v", tr, err)
		}
	}
	partial := revenue(classification.RevenuePartial)
	if err := partial.Validate(); err == nil {
		t.Fatal("PARTIAL with no allocation basis validated")
	}
	partial.AllocationBasis = "SAFE_HARBOR_PERCENTAGE"
	if err := partial.Validate(); err != nil {
		t.Fatal(err)
	}
	amb := revenue(classification.RevenueAmbiguous)
	if err := amb.Validate(); err == nil {
		t.Fatal("an AMBIGUOUS treatment marked RESOLVED validated")
	}
	noRegime := revenue(classification.RevenueIncluded)
	noRegime.Regime = ""
	if err := noRegime.Validate(); err == nil {
		t.Fatal("a regulatory-revenue decision with no regime validated")
	}
}

func TestCLSREQ0073To0078StatusesAndOnlyResolvedFeedsAuthority(t *testing.T) {
	for _, s := range []classification.Status{classification.StatusResolved, classification.StatusAmbiguous,
		classification.StatusConflicted, classification.StatusUnsupported, classification.StatusReviewRequired} {
		if !s.Valid() {
			t.Errorf("%s missing", s)
		}
		if s.FeedsAuthoritative() != (s == classification.StatusResolved) {
			t.Errorf("%s feeds authoritative %t", s, s.FeedsAuthoritative())
		}
	}
}

func TestCLSREQ0079And0081UnresolvedDecisionsCarryNoPickAndKeepCandidates(t *testing.T) {
	if err := taxability(classification.StatusAmbiguous, "STANDARD").Validate(); err == nil {
		t.Fatal("an AMBIGUOUS decision carried the top candidate as its category")
	}
	if err := taxability(classification.StatusConflicted, "", "STANDARD").Validate(); err == nil {
		t.Fatal("a CONFLICTED decision kept one candidate")
	}
	if err := taxability(classification.StatusConflicted, "", "STANDARD", "REDUCED").Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestCLSREQ0083To0089A3AutoMappingIsBoundedAndAbstains(t *testing.T) {
	exists := func(id classification.OntologyID) bool { return id == "ontology:telecom/voice/mobile" }
	policy := classification.A3Policy{UseCase: "sku-to-ontology/mobile", Approved: true, Threshold: "0.97"}
	p := classification.Proposal{Target: "ontology:telecom/voice/mobile", Confidence: "0.99", Manifest: "airm:cls@2027.03"}
	for name, c := range map[string]struct {
		p      classification.Proposal
		policy classification.A3Policy
		want   classification.AutoMapOutcome
	}{
		"accepted":            {p, policy, classification.AutoMapAccept},
		"below threshold":     {func() classification.Proposal { q := p; q.Confidence = "0.96"; return q }(), policy, classification.AutoMapReview},
		"out of distribution": {func() classification.Proposal { q := p; q.OutOfDistribution = true; return q }(), policy, classification.AutoMapAbstain},
		"conflict":            {func() classification.Proposal { q := p; q.Conflict = true; return q }(), policy, classification.AutoMapAbstain},
		"unapproved":          {p, classification.A3Policy{UseCase: "x", Threshold: "0.5"}, classification.AutoMapReview},
		"new node":            {func() classification.Proposal { q := p; q.Target = "ontology:new/category"; return q }(), policy, classification.AutoMapReview},
	} {
		got, err := classification.AutoMap(c.p, c.policy, exists)
		if err != nil || got != c.want {
			t.Errorf("%s: %s %v, want %s", name, got, err, c.want)
		}
	}
	if _, err := classification.AutoMap(classification.Proposal{Target: "ontology:telecom/voice/mobile", Confidence: "1"}, policy, exists); err == nil {
		t.Fatal("a proposal with no release manifest was considered")
	}
}
