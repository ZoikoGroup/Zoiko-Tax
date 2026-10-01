package sourcing_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/sourcing"
)

var (
	termStart = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	termEnd   = time.Date(2028, 1, 1, 0, 0, 0, 0, time.UTC)
	reviewDue = time.Date(2027, 6, 30, 0, 0, 0, 0, time.UTC)
	buildAt   = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
)

func allow() sourcing.Grant { return sourcing.Grant{State: sourcing.StateAllow} }

// record is a licence record that permits a hosted build and nothing beyond it.
func record() sourcing.SourceLicenseRecord {
	return sourcing.SourceLicenseRecord{
		SourceID:          "SRC-TEST-0001",
		ProviderLegalName: "Example Licensor Ltd",
		SourceName:        "Example rate table",
		SourceVersion:     "2026.1",
		Class:             sourcing.ClassLicensedSpecialist,
		Acquisition:       sourcing.AcquisitionBulk,
		LicenceRef:        "LIC-0001",
		Rights: sourcing.RightsProfile{
			Grants: map[sourcing.Right]sourcing.Grant{
				sourcing.RightInternalUse:      allow(),
				sourcing.RightTransform:        allow(),
				sourcing.RightDerivedOutput:    allow(),
				sourcing.RightCommercialUse:    allow(),
				sourcing.RightHistoricalReplay: allow(),
				sourcing.RightPrivateBundle:    {State: sourcing.StateDeny},
				sourcing.RightCustomerDisplay: {State: sourcing.StateConditional, Conditions: []sourcing.Condition{
					{Kind: sourcing.ConditionTerritory, Values: []string{"T1", "T2"}},
				}},
			},
			AttributionRequired: sourcing.ObligationNotRequired,
			ShareAlikeTrigger:   sourcing.ObligationNotRequired,
		},
		Environments:     []sourcing.DeploymentMode{sourcing.DeployZoikoOnly, sourcing.DeployPrivateRuntime},
		Term:             sourcing.Term{Start: termStart, End: termEnd},
		Termination:      sourcing.TerminationEffect{SurvivingRights: []sourcing.Right{sourcing.RightHistoricalReplay}},
		CostModel:        sourcing.CostFixed,
		LegalOwner:       "counsel:1",
		TechnicalOwner:   "engineer:1",
		ReviewDue:        reviewDue,
		LicenceTermsHash: "zt1:00",
		State:            sourcing.StateProduction,
	}
}

func register(rs ...sourcing.SourceLicenseRecord) map[sourcing.SourceID]sourcing.SourceLicenseRecord {
	out := map[sourcing.SourceID]sourcing.SourceLicenseRecord{}
	for _, r := range rs {
		out[r.SourceID] = r
	}
	return out
}

func gate(t *testing.T, rec sourcing.SourceLicenseRecord, mode sourcing.DeploymentMode, at time.Time) error {
	t.Helper()
	_, err := sourcing.Gate([]sourcing.SourceID{rec.SourceID}, register(rec), nil, []sourcing.DeploymentMode{mode}, nil, at)
	return err
}

func TestGateAdmitsACurrentLicenceForAHostedBuild(t *testing.T) {
	if err := gate(t, record(), sourcing.DeployZoikoOnly, buildAt); err != nil {
		t.Fatalf("a current licence permitting every build right was refused: %v", err)
	}
}

// TestGateFailsClosed is SRC-RIGHTS-CONF in miniature: every way a right can
// fail to be established is a refusal, and none of them is a skip.
func TestGateFailsClosed(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*sourcing.SourceLicenseRecord)
		mode sourcing.DeploymentMode
		at   time.Time
		want string
	}{
		{"a silent right is UNKNOWN", func(r *sourcing.SourceLicenseRecord) {
			delete(r.Rights.Grants, sourcing.RightHistoricalReplay)
		}, sourcing.DeployZoikoOnly, buildAt, "historical_replay is UNKNOWN"},
		{"an explicit UNKNOWN", func(r *sourcing.SourceLicenseRecord) {
			r.Rights.Grants[sourcing.RightTransform] = sourcing.Grant{State: sourcing.StateUnknown}
		}, sourcing.DeployZoikoOnly, buildAt, "transform is UNKNOWN"},
		{"private bundle denied for P2", func(*sourcing.SourceLicenseRecord) {}, sourcing.DeployPrivateRuntime, buildAt, "private_bundle is DENY"},
		{"environment not licensed", func(*sourcing.SourceLicenseRecord) {}, sourcing.DeployDisconnectedEdge, buildAt, "not P3_DISCONNECTED_EDGE"},
		{"before the term", func(*sourcing.SourceLicenseRecord) {}, sourcing.DeployZoikoOnly, termStart.Add(-time.Second), "does not cover"},
		{"at the end of the term", func(r *sourcing.SourceLicenseRecord) { r.ReviewDue = termEnd.Add(time.Hour) },
			sourcing.DeployZoikoOnly, termEnd, "does not cover"},
		{"review overdue", func(*sourcing.SourceLicenseRecord) {}, sourcing.DeployZoikoOnly, reviewDue, "review was due"},
		{"still onboarding", func(r *sourcing.SourceLicenseRecord) { r.State = sourcing.StateContentValidation },
			sourcing.DeployZoikoOnly, buildAt, "only a PRODUCTION or REVIEW_RENEW source"},
		{"suspended", func(r *sourcing.SourceLicenseRecord) { r.State = sourcing.StateSuspended },
			sourcing.DeployZoikoOnly, buildAt, "is SUSPENDED"},
		{"discovery web", func(r *sourcing.SourceLicenseRecord) { r.Class = sourcing.ClassDiscoveryWeb },
			sourcing.DeployZoikoOnly, buildAt, "ZTAX-SRC-REQ-0037"},
		{"territorially bounded licence, unbounded pack", func(r *sourcing.SourceLicenseRecord) { r.Territories = []string{"T1"} },
			sourcing.DeployZoikoOnly, buildAt, "not territorially bounded"},
		{"no environments", func(r *sourcing.SourceLicenseRecord) { r.Environments = nil },
			sourcing.DeployZoikoOnly, buildAt, "missing environments"},
		{"no review date", func(r *sourcing.SourceLicenseRecord) { r.ReviewDue = time.Time{} },
			sourcing.DeployZoikoOnly, buildAt, "missing reviewDue"},
		{"conditional on something no machine can check", func(r *sourcing.SourceLicenseRecord) {
			r.Rights.Grants[sourcing.RightCommercialUse] = sourcing.Grant{State: sourcing.StateConditional,
				Conditions: []sourcing.Condition{{Kind: sourcing.ConditionOther, Text: "no more than 10 customers"}}}
		}, sourcing.DeployZoikoOnly, buildAt, "which a build cannot check"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := record()
			tc.mut(&r)
			err := gate(t, r, tc.mode, tc.at)
			if err == nil {
				t.Fatal("the gate admitted it")
			}
			if !errors.Is(err, sourcing.ErrRightsGate) {
				t.Errorf("error does not wrap ErrRightsGate: %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("got %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestGateRefusesWhatIsNotDeclared(t *testing.T) {
	r := record()
	if _, err := sourcing.Gate(nil, register(r), nil, []sourcing.DeploymentMode{sourcing.DeployZoikoOnly}, nil, buildAt); err == nil {
		t.Error("a pack with no sources passed")
	}
	if _, err := sourcing.Gate([]sourcing.SourceID{"SRC-ABSENT"}, register(r), nil, []sourcing.DeploymentMode{sourcing.DeployZoikoOnly}, nil, buildAt); err == nil ||
		!strings.Contains(err.Error(), "ZTAX-SRC-REQ-0001") {
		t.Errorf("a source with no record: got %v", err)
	}
	if _, err := sourcing.Gate([]sourcing.SourceID{r.SourceID}, register(r), nil, nil, nil, buildAt); err == nil {
		t.Error("a pack with no deployment mode passed")
	}
}

func TestConditionalGrants(t *testing.T) {
	p := record().Rights
	u := sourcing.Use{Mode: sourcing.DeployZoikoOnly, Territories: []string{"T1"}}
	if d := p.Evaluate(sourcing.RightCustomerDisplay, u); !d.Permitted {
		t.Errorf("T1 within T1,T2 refused: %s", d.Reason)
	}
	u.Territories = []string{"T1", "T3"}
	if d := p.Evaluate(sourcing.RightCustomerDisplay, u); d.Permitted {
		t.Error("T3 outside T1,T2 permitted")
	}

	p.Grants[sourcing.RightQuote] = sourcing.Grant{State: sourcing.StateConditional, Conditions: []sourcing.Condition{
		{Kind: sourcing.ConditionAttribution, Text: "Source: Example"},
		{Kind: sourcing.ConditionDeploymentMode, Values: []string{string(sourcing.DeployZoikoOnly)}},
	}}
	d := p.Evaluate(sourcing.RightQuote, sourcing.Use{Mode: sourcing.DeployZoikoOnly})
	if !d.Permitted || len(d.Obligations) != 1 || d.Obligations[0] != "Source: Example" {
		t.Errorf("attribution-conditional quote: %+v", d)
	}
	if d := p.Evaluate(sourcing.RightQuote, sourcing.Use{Mode: sourcing.DeployCustomerVPC}); d.Permitted {
		t.Error("a P0-only grant was permitted for P1")
	}
}

func TestRightsProfileShape(t *testing.T) {
	cases := map[string]sourcing.RightsProfile{
		"conditional without conditions": {Grants: map[sourcing.Right]sourcing.Grant{sourcing.RightCache: {State: sourcing.StateConditional}}},
		"allow with conditions": {Grants: map[sourcing.Right]sourcing.Grant{sourcing.RightCache: {State: sourcing.StateAllow,
			Conditions: []sourcing.Condition{{Kind: sourcing.ConditionOther, Text: "x"}}}}},
		"right outside the taxonomy": {Grants: map[sourcing.Right]sourcing.Grant{"hosted_use": allow()}},
		"obligations unstated":       {Grants: map[sourcing.Right]sourcing.Grant{}},
	}
	for name, p := range cases {
		if name != "obligations unstated" {
			p.AttributionRequired, p.ShareAlikeTrigger = sourcing.ObligationUnknown, sourcing.ObligationUnknown
		}
		if err := p.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestBuildRightsByMode(t *testing.T) {
	has := func(m sourcing.DeploymentMode, r sourcing.Right) bool {
		for _, x := range sourcing.BuildRights(m) {
			if x == r {
				return true
			}
		}
		return false
	}
	if has(sourcing.DeployZoikoOnly, sourcing.RightPrivateBundle) || !has(sourcing.DeployPrivateRuntime, sourcing.RightPrivateBundle) {
		t.Error("private_bundle must be required for P2 and only beyond P0/P1")
	}
	if !has(sourcing.DeployDisconnectedEdge, sourcing.RightEdgeBundle) {
		t.Error("edge_bundle must be required for P3")
	}
	if sourcing.BuildRights("P9") != nil {
		t.Error("an unknown mode must require-and-permit nothing")
	}
}

// TestOnboardingLifecycleHasOnlyLegalTransitions enumerates every pair of
// states and checks the machine against the table in CanTransition's comment.
func TestOnboardingLifecycleHasOnlyLegalTransitions(t *testing.T) {
	states := []sourcing.OnboardingState{
		sourcing.StateDiscover, sourcing.StateRightsScreen, sourcing.StateLegalReview, sourcing.StateSecurityPrivacyReview,
		sourcing.StateCommercialApproval, sourcing.StateTechnicalQualification, sourcing.StateRightsProfileApproved,
		sourcing.StateIngestionEnabled, sourcing.StateContentValidation, sourcing.StateProduction,
		sourcing.StateReviewRenew, sourcing.StateSuspended, sourcing.StateWithdrawn,
	}
	legal := map[[2]sourcing.OnboardingState]bool{}
	for i := 0; i < 9; i++ {
		legal[[2]sourcing.OnboardingState{states[i], states[i+1]}] = true
		legal[[2]sourcing.OnboardingState{states[i], sourcing.StateWithdrawn}] = true
		if i >= 2 {
			legal[[2]sourcing.OnboardingState{states[i], sourcing.StateRightsScreen}] = true
		}
	}
	for _, p := range [][2]sourcing.OnboardingState{
		{sourcing.StateProduction, sourcing.StateReviewRenew}, {sourcing.StateProduction, sourcing.StateSuspended},
		{sourcing.StateProduction, sourcing.StateWithdrawn}, {sourcing.StateReviewRenew, sourcing.StateProduction},
		{sourcing.StateReviewRenew, sourcing.StateSuspended}, {sourcing.StateReviewRenew, sourcing.StateWithdrawn},
		{sourcing.StateSuspended, sourcing.StateReviewRenew}, {sourcing.StateSuspended, sourcing.StateWithdrawn},
	} {
		legal[p] = true
	}
	for _, from := range states {
		for _, to := range states {
			want := legal[[2]sourcing.OnboardingState{from, to}]
			if got := sourcing.CanTransition(from, to); got != want {
				t.Errorf("%s → %s: got %v, want %v", from, to, got, want)
			}
		}
	}
}

func TestTransitionsCarryReasonsOnlyWhenLeavingUse(t *testing.T) {
	at := buildAt
	ok := sourcing.Transition{From: sourcing.StateProduction, To: sourcing.StateSuspended, Reason: sourcing.ReasonLicenceExpired, At: at, Actor: "counsel:1"}
	if err := ok.Validate(); err != nil {
		t.Fatalf("a complete suspension was refused: %v", err)
	}
	noReason := ok
	noReason.Reason = ""
	if noReason.Validate() == nil {
		t.Error("a suspension without a reason was accepted")
	}
	promotion := sourcing.Transition{From: sourcing.StateDiscover, To: sourcing.StateRightsScreen, Reason: sourcing.ReasonLegalIssue, At: at, Actor: "x:1"}
	if promotion.Validate() == nil {
		t.Error("a promotion carrying a reason was accepted")
	}
}

// TestLicenceExpiryIsAnIncident is the source half of the risk register's
// path: a term that ends while the record still says PRODUCTION is detected,
// the record is suspended with a reason, and only surviving rights continue.
func TestLicenceExpiryIsAnIncident(t *testing.T) {
	r := record()
	if _, lapsed := r.DetectLapse(buildAt); lapsed {
		t.Fatal("a current licence was reported lapsed")
	}
	inc, lapsed := r.DetectLapse(termEnd)
	if !lapsed || inc.Reason != sourcing.ReasonLicenceExpired || !inc.At.Equal(termEnd) {
		t.Fatalf("expiry not detected: %+v", inc)
	}
	suspended, tr, err := r.Apply(inc, "counsel:1")
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if suspended.State != sourcing.StateSuspended || tr.From != sourcing.StateProduction {
		t.Errorf("state %s via %+v", suspended.State, tr)
	}
	if err := gate(t, suspended, sourcing.DeployZoikoOnly, buildAt); err == nil {
		t.Error("a suspended source still feeds a build")
	}
	if !suspended.RightAfter(sourcing.RightHistoricalReplay, inc.Reason) {
		t.Error("historical replay survives by the record's termination effect and was lost")
	}
	if suspended.RightAfter(sourcing.RightDerivedOutput, inc.Reason) {
		t.Error("derived output does not survive expiry and was kept")
	}
}
