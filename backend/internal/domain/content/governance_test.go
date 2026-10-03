package content_test

import (
	"strings"
	"testing"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/content"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/sourcing"
)

var approvedAt = time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

func approval(role content.Role, principal, key string) content.Approval {
	return content.Approval{Role: role, Principal: principal, KeyID: key, At: approvedAt}
}

func TestFourEyes(t *testing.T) {
	author := approval(content.RoleAuthor, "analyst:2048", "key-a")
	approver := approval(content.RoleApprover, "counsel:113", "key-b")

	if err := content.CheckFourEyes([]content.Approval{author, approver}, "release-key"); err != nil {
		t.Fatalf("a distinct author and approver were refused: %v", err)
	}

	cases := map[string]struct {
		approvals []content.Approval
		release   string
		want      string
	}{
		"no approvals":  {nil, "release-key", "no approvals"},
		"author only":   {[]content.Approval{author}, "release-key", "no APPROVER"},
		"approver only": {[]content.Approval{approver}, "release-key", "no AUTHOR"},
		"self-approval": {[]content.Approval{author, approval(content.RoleApprover, "analyst:2048", "key-b")}, "release-key",
			"approves as both"},
		"one key, two names": {[]content.Approval{author, approval(content.RoleApprover, "counsel:113", "key-a")}, "release-key",
			"same key"},
		"approver holds the release key": {[]content.Approval{author, approval(content.RoleApprover, "counsel:113", "release-key")},
			"release-key", "release signing key"},
		"an AI approver": {[]content.Approval{author, approval(content.RoleApprover, "ai:extractor-7", "key-b")}, "release-key",
			"ZTAX-CONT-REQ-0012"},
		"a service approver": {[]content.Approval{author, approval(content.RoleApprover, "service:ci", "key-b")}, "release-key",
			"only a person"},
		"an unqualified principal": {[]content.Approval{author, approval(content.RoleApprover, "counsel", "key-b")}, "release-key",
			"kind:identifier"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := content.CheckFourEyes(tc.approvals, tc.release)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestInterpretationApproval(t *testing.T) {
	in := content.Interpretation{
		ID: "INT-TEST-0001", SourceArtifacts: []string{"ART-1"}, VerificationRefs: []string{"GOLD-1"},
		LegalValidity: content.Window{From: approvedAt},
		Status:        content.InterpretationInReview,
		Approvals: []content.Approval{
			approval(content.RoleAuthor, "analyst:1", "k1"), approval(content.RoleApprover, "counsel:1", "k2"),
		},
	}
	got, err := in.Approve()
	if err != nil || got.Status != content.InterpretationApproved {
		t.Fatalf("approve: %v, %s", err, got.Status)
	}

	risky := in
	risky.HighRiskAmbiguous = true
	if _, err := risky.Approve(); err == nil || !strings.Contains(err.Error(), "ZTAX-CONT-REQ-0014") {
		t.Errorf("a high-risk interpretation without a senior approver: %v", err)
	}
	risky.Approvals = append(risky.Approvals, approval(content.RoleSeniorApprover, "counsel:9", "k3"))
	if _, err := risky.Approve(); err != nil {
		t.Errorf("with a senior approver: %v", err)
	}

	unsourced := in
	unsourced.SourceArtifacts = nil
	if _, err := unsourced.Approve(); err == nil {
		t.Error("an interpretation citing no source was approved")
	}
}

func TestPackStatusTransitions(t *testing.T) {
	legal := [][2]content.PackStatus{
		{content.PackResearch, content.PackValidation}, {content.PackValidation, content.PackPilot},
		{content.PackPilot, content.PackProduction}, {content.PackProduction, content.PackManaged},
		{content.PackManaged, content.PackProduction}, {content.PackProduction, content.PackSuspended},
		{content.PackSuspended, content.PackValidation}, {content.PackSuspended, content.PackWithdrawn},
		{content.PackResearch, content.PackWithdrawn},
	}
	for _, p := range legal {
		if !content.CanTransitionPack(p[0], p[1]) {
			t.Errorf("%s → %s refused", p[0], p[1])
		}
	}
	illegal := [][2]content.PackStatus{
		{content.PackResearch, content.PackProduction}, {content.PackSuspended, content.PackProduction},
		{content.PackWithdrawn, content.PackValidation}, {content.PackWithdrawn, content.PackSuspended},
		{content.PackPilot, content.PackResearch},
	}
	for _, p := range illegal {
		if content.CanTransitionPack(p[0], p[1]) {
			t.Errorf("%s → %s allowed", p[0], p[1])
		}
	}
}

func packManifest() content.PackManifest {
	return content.PackManifest{
		ID: "ZTAX-CP-TEST", Version: v("1.0.0"), Level: content.LevelNational, Status: content.PackProduction,
		Capabilities:      []content.Capability{content.CapabilityDetermine},
		DeploymentModes:   []sourcing.DeploymentMode{sourcing.DeployZoikoOnly},
		Sources:           []content.SourceDependency{{Source: "SRC-TEST-0001", LicenceRef: "LIC-1", RecordDigest: "zt1:00"}},
		Owner:             "owner:1",
		WithdrawalPlanRef: "WD-1",
	}
}

func TestPackManifestValidate(t *testing.T) {
	if err := packManifest().Validate(); err != nil {
		t.Fatalf("a complete manifest was refused: %v", err)
	}
	cases := map[string]func(*content.PackManifest){
		"no sources":          func(m *content.PackManifest) { m.Sources = nil },
		"no capability":       func(m *content.PackManifest) { m.Capabilities = nil },
		"unknown level":       func(m *content.PackManifest) { m.Level = "CONTINENTAL" },
		"production no owner": func(m *content.PackManifest) { m.Owner = "" },
		"self dependency": func(m *content.PackManifest) {
			m.Dependencies = []content.Dependency{{Pack: m.ID, Constraint: content.MustConstraint("^1")}}
		},
		"depends and conflicts": func(m *content.PackManifest) {
			m.Dependencies = []content.Dependency{{Pack: "ZTAX-CP-OTHER", Constraint: content.MustConstraint("^1")}}
			m.Conflicts = []content.Conflict{{Pack: "ZTAX-CP-OTHER", Constraint: content.MustConstraint("<1")}}
		},
		"lower-case id": func(m *content.PackManifest) { m.ID = "ztax-cp-test" },
		"zero version":  func(m *content.PackManifest) { m.Version = content.Version{} },
	}
	for name, mut := range cases {
		m := packManifest()
		mut(&m)
		if err := m.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// TestLicenceExpirySuspendsDependentPacks is the pack half of the risk
// register's licence-expiry path.
func TestLicenceExpirySuspendsDependentPacks(t *testing.T) {
	rec := sourcing.SourceLicenseRecord{
		SourceID: "SRC-TEST-0001",
		Rights: sourcing.RightsProfile{Grants: map[sourcing.Right]sourcing.Grant{
			sourcing.RightHistoricalReplay: {State: sourcing.StateAllow},
		}},
		Termination: sourcing.TerminationEffect{SurvivingRights: []sourcing.Right{sourcing.RightHistoricalReplay}},
	}
	inc := sourcing.Incident{Source: "SRC-TEST-0001", Reason: sourcing.ReasonLicenceExpired, Severity: sourcing.SeverityR5, At: approvedAt}

	r, err := content.RespondToSourceIncident(packManifest(), content.PackProduction, inc, rec)
	if err != nil {
		t.Fatalf("respond: %v", err)
	}
	if !r.Affected || r.To != content.PackSuspended || r.NewOutcomesPermitted || !r.ImpactAssessment {
		t.Errorf("an affected production pack: %+v", r)
	}
	if !r.ReplayPermitted {
		t.Error("historical replay survives the licence and was refused")
	}

	rec.Termination.SurvivingRights = nil
	if r, _ := content.RespondToSourceIncident(packManifest(), content.PackProduction, inc, rec); r.ReplayPermitted {
		t.Error("replay was permitted although no right survives expiry")
	}

	other := packManifest()
	other.Sources = []content.SourceDependency{{Source: "SRC-OTHER", LicenceRef: "L", RecordDigest: "zt1:00"}}
	if r, _ := content.RespondToSourceIncident(other, content.PackProduction, inc, rec); r.Affected || r.To != content.PackProduction {
		t.Errorf("an unrelated pack was affected: %+v", r)
	}

	if r, _ := content.RespondToSourceIncident(packManifest(), content.PackWithdrawn, inc, rec); r.To != content.PackWithdrawn {
		t.Errorf("a withdrawn pack moved to %s", r.To)
	}
}

func TestObjectModel(t *testing.T) {
	if err := content.RuleSemanticID("ZTAX-RULE-WORKED-VAT").Validate(); err != nil {
		t.Errorf("the worked pack's semantic id: %v", err)
	}
	for _, bad := range []string{"", "ZTAX-RULE-", "RULE-X", "ZTAX-RULE-lower", "ZTAX-RULE-X-"} {
		if content.RuleSemanticID(bad).Validate() == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if !content.AuthorityBindingPrimary.Outranks(content.AuthorityOfficialExplanatory) ||
		content.AuthorityUnknown.Outranks(content.AuthorityUnverifiedSecondary) {
		t.Error("authority ordering")
	}
	if content.AuthorityUnknown.MayDirectlyGround() || content.AuthorityOfficialExplanatory.MayDirectlyGround() {
		t.Error("only A0 and A1 directly ground a production rule (ZTAX-CONT-REQ-0002)")
	}

	rv := content.RuleVersion{
		SemanticID: "ZTAX-RULE-WORKED-VAT", Version: "2026.09.1",
		LegalValidity:     content.Window{From: approvedAt},
		KnownAt:           approvedAt.Add(-time.Hour),
		ApprovedAt:        approvedAt,
		DeploymentFrom:    approvedAt.Add(24 * time.Hour),
		InterpretationRef: "INT-1", SourceArtifacts: []string{"ART-1"},
	}
	if err := rv.Validate(); err != nil {
		t.Fatalf("rule version: %v", err)
	}
	if rv.ActiveAt(approvedAt) {
		t.Error("a staged rule activated before its deployment instant (ZTAX-CONT-REQ-0025)")
	}
	if !rv.ActiveAt(approvedAt.Add(25 * time.Hour)) {
		t.Error("a deployed rule inside its legal window is inactive")
	}

	art := content.SourceArtifact{ID: "ART-1", SourceID: "SRC-1", AuthorityID: "AUTH-1", AuthorityClass: content.AuthorityBindingPrimary,
		RetrievedAt: approvedAt, Language: "en", Extraction: content.ExtractionReviewed}
	if art.Validate() == nil {
		t.Error("an artifact with no content hash was accepted (ZTAX-CONT-REQ-0052)")
	}
	art.ContentHash = "zt1:00"
	if err := art.Validate(); err != nil {
		t.Errorf("artifact: %v", err)
	}
}
