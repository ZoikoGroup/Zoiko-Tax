package pack_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/content/pack"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/content"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/sourcing"
)

var at = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

const register = `{
  "sources": [{
    "sourceId": "SRC-TEST-0001",
    "providerLegalName": "Example Licensor Ltd",
    "sourceName": "Example rate table",
    "class": "S2",
    "acquisition": "BULK",
    "licenceRef": "LIC-0001",
    "rights": {
      "internal_use": "ALLOW", "transform": "ALLOW", "derived_output": "ALLOW",
      "commercial_use": "ALLOW", "historical_replay": "ALLOW",
      "quote": {"state": "CONDITIONAL", "conditions": [{"kind": "ATTRIBUTION", "text": "Source: Example"}]},
      "private_bundle": "DENY"
    },
    "attributionRequired": "REQUIRED",
    "shareAlikeTrigger": "NOT_REQUIRED",
    "environments": ["P0_ZOIKO_ONLY", "P2_PRIVATE_RUNTIME"],
    "term": {"start": "2026-01-01T00:00:00Z", "end": "2028-01-01T00:00:00Z", "autoRenew": false, "noticeDays": 90},
    "termination": {"survivingRights": ["historical_replay"], "deleteRawWithinDays": 30},
    "costModel": "FIXED",
    "legalOwner": "counsel:1",
    "technicalOwner": "engineer:1",
    "reviewDue": "2027-06-30T00:00:00Z",
    "licenceTermsHash": "zt1:0000000000000000000000000000000000000000000000000000000000000000",
    "state": "PRODUCTION"
  }]
}`

const declaration = `{
  "bundleId": "fixture",
  "packId": "ZTAX-CP-TEST",
  "version": "1.2.0",
  "level": "NATIONAL",
  "status": "RESEARCH",
  "capabilities": ["DETERMINE"],
  "deploymentModes": ["P0_ZOIKO_ONLY"],
  "dependencies": [{"packId": "ZTAX-CP-GLOBAL", "version": "^1.0"}],
  "sources": [{"sourceId": "SRC-TEST-0001", "uses": ["quote"]}]
}`

func decode(t *testing.T) (pack.Declaration, pack.Register) {
	t.Helper()
	d, err := pack.DecodeDeclaration([]byte(declaration))
	if err != nil {
		t.Fatalf("declaration: %v", err)
	}
	r, err := pack.DecodeRegister([]byte(register))
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	return d, r
}

func global() content.PackNode {
	return content.PackNode{ID: "ZTAX-CP-GLOBAL", Version: content.Version{Major: 1, Minor: 3}, Level: content.LevelGlobal}
}

func TestAssembleProducesASignablePackSection(t *testing.T) {
	d, reg := decode(t)
	res, err := pack.Assemble(d, "fixture", []content.PackNode{d.Node(), global()}, reg, at)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if len(res.LoadOrder) != 2 || res.LoadOrder[0].ID != "ZTAX-CP-GLOBAL" || res.LoadOrder[1].ID != "ZTAX-CP-TEST" {
		t.Errorf("load order %+v", res.LoadOrder)
	}
	src := res.Pack.Sources
	if len(src) != 1 || src[0].LicenceRef != "LIC-0001" || !strings.HasPrefix(src[0].RecordDigest, "zt1:") {
		t.Errorf("sources %+v", src)
	}
	var obligations []string
	for _, e := range res.Evaluations {
		for _, dec := range e.Decisions {
			obligations = append(obligations, dec.Obligations...)
		}
	}
	if len(obligations) != 1 || obligations[0] != "Source: Example" {
		t.Errorf("the attribution condition on quote was not reported as an obligation: %v", obligations)
	}
}

func TestAssembleRefuses(t *testing.T) {
	cases := map[string]struct {
		mut  func(*pack.Declaration, pack.Register)
		want string
	}{
		"a declaration for another bundle": {func(d *pack.Declaration, _ pack.Register) { d.BundleID = "other" }, "describes bundle"},
		"a suspended pack":                 {func(d *pack.Declaration, _ pack.Register) { d.Status = content.PackSuspended }, "ZTAX-CONT-REQ-0042"},
		"a private-runtime build without private_bundle": {func(d *pack.Declaration, _ pack.Register) {
			d.DeploymentModes = append(d.DeploymentModes, sourcing.DeployPrivateRuntime)
		}, "private_bundle is DENY"},
		"a source with no record": {func(d *pack.Declaration, _ pack.Register) {
			d.Sources = append(d.Sources, pack.DeclaredSource{Source: "SRC-ABSENT"})
		}, "no SourceLicenseRecord"},
		"an expired licence": {func(_ *pack.Declaration, r pack.Register) {
			rec := r["SRC-TEST-0001"]
			rec.Term.End = at
			r["SRC-TEST-0001"] = rec
		}, "does not cover"},
		"an unsatisfied dependency": {func(d *pack.Declaration, _ pack.Register) {
			d.Dependencies[0].Constraint = content.MustConstraint("^2")
		}, "UNSATISFIED_VERSION"},
		"an extra use the licence is silent on": {func(d *pack.Declaration, _ pack.Register) {
			d.Sources[0].Uses = append(d.Sources[0].Uses, sourcing.RightCustomerExport)
		}, "customer_export is UNKNOWN"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			d, reg := decode(t)
			tc.mut(&d, reg)
			_, err := pack.Assemble(d, "fixture", []content.PackNode{d.Node(), global()}, reg, at)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestRightsGateFailuresAreDistinguishable(t *testing.T) {
	d, reg := decode(t)
	d.DeploymentModes = []sourcing.DeploymentMode{sourcing.DeployDisconnectedEdge}
	_, err := pack.Assemble(d, "fixture", []content.PackNode{d.Node(), global()}, reg, at)
	if !errors.Is(err, sourcing.ErrRightsGate) {
		t.Fatalf("got %v, want an error wrapping sourcing.ErrRightsGate", err)
	}
}

func TestDecodingIsStrict(t *testing.T) {
	if _, err := pack.DecodeDeclaration([]byte(strings.Replace(declaration, `"status"`, `"stauts"`, 1))); err == nil {
		t.Error("a misspelled declaration field was accepted")
	}
	if _, err := pack.DecodeRegister([]byte(strings.Replace(register, `"costModel"`, `"cost"`, 1))); err == nil {
		t.Error("a misspelled register field was accepted")
	}
	if _, err := pack.DecodeRegister([]byte(strings.Replace(register, `"internal_use": "ALLOW"`, `"internal_use": "MAYBE"`, 1))); err == nil {
		t.Error("a state outside ALLOW/DENY/CONDITIONAL/UNKNOWN was accepted")
	}
	if _, err := pack.DecodeRegister([]byte(strings.Replace(register, `"internal_use"`, `"hosted_use"`, 1))); err == nil {
		t.Error("a right outside the taxonomy was accepted")
	}
	if _, err := pack.DecodeDeclaration([]byte(strings.Replace(declaration, `"^1.0"`, `"1.x"`, 1))); err == nil {
		t.Error("a malformed version constraint was accepted")
	}
}

// TestRecordDigestPinsTheRecord: the digest the manifest carries changes with
// any field of the record and with nothing else — in particular not with the
// order rights were written in, which a map does not have.
func TestRecordDigestPinsTheRecord(t *testing.T) {
	_, reg := decode(t)
	rec := reg["SRC-TEST-0001"]
	a, err := pack.RecordDigest(rec)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	for i := 0; i < 20; i++ {
		b, _ := pack.RecordDigest(rec)
		if a != b {
			t.Fatal("the digest of one record varies between calls; map order leaked")
		}
	}
	rec.Rights.Grants[sourcing.RightCache] = sourcing.Grant{State: sourcing.StateAllow}
	if b, _ := pack.RecordDigest(rec); a == b {
		t.Error("granting a right did not change the record digest")
	}
}

// TestTheWorkedPackPassesItsGates runs the committed declaration and register
// through the gates at a fixed instant, so a defect in either is found by the
// unit tests rather than by the first `make content` after it lands. The CI
// content build runs the same gates at the real instant, which is what catches
// a licence or review date that has since passed.
func TestTheWorkedPackPassesItsGates(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..", "content")
	declBytes, err := os.ReadFile(filepath.Join(root, "packs", "eu-vat", pack.DeclarationFile)) // #nosec G304 -- a fixed repository path
	if err != nil {
		t.Fatalf("read declaration: %v", err)
	}
	regBytes, err := os.ReadFile(filepath.Join(root, "sources", "register.json")) // #nosec G304 -- a fixed repository path
	if err != nil {
		t.Fatalf("read register: %v", err)
	}
	d, err := pack.DecodeDeclaration(declBytes)
	if err != nil {
		t.Fatalf("declaration: %v", err)
	}
	reg, err := pack.DecodeRegister(regBytes)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := pack.Assemble(d, "eu-vat-worked-2026.09", []content.PackNode{d.Node()}, reg, at); err != nil {
		t.Fatalf("the worked pack fails its own gates: %v", err)
	}
}
