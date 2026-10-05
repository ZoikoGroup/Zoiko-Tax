package ai

import (
	"reflect"
	"slices"
	"testing"

	"github.com/google/uuid"
	"pgregory.net/rapid"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/privacy"
)

var testTenant = id.NewTenantID(uuid.MustParse("11111111-1111-7111-8111-111111111111"))

func testPolicy(t *testing.T, killed []AiUseCase, global bool) Policy {
	t.Helper()
	p, err := NewPolicy([]UseCasePolicy{
		{
			UseCase:              "classification-review",
			Owner:                "lane-l",
			MaxAuthority:         AuthorityOutcomeA1,
			MaxRiskTier:          RiskTierT2,
			PermittedRegions:     []string{"eu-west-1", "eu-central-1"},
			PermittedDataClasses: []privacy.Class{privacy.P0, privacy.P1, privacy.P5},
		},
		{
			UseCase:              "explanation",
			Owner:                "lane-l",
			MaxAuthority:         AuthorityOutcomeA0,
			MaxRiskTier:          RiskTierT1,
			PermittedRegions:     []string{"eu-west-1"},
			PermittedDataClasses: []privacy.Class{privacy.P0},
			Suspended:            true,
		},
		{
			UseCase:              "exposure-forecast",
			Owner:                "lane-l",
			MaxAuthority:         AuthorityOutcomeA4,
			MaxRiskTier:          RiskTierT3,
			PermittedRegions:     []string{"eu-west-1"},
			PermittedDataClasses: []privacy.Class{privacy.P0, privacy.P5},
		},
	}, killed, global)
	if err != nil {
		t.Fatalf("NewPolicy: %v", err)
	}
	return p
}

func baseContext() GovernanceContext {
	return GovernanceContext{
		Tenant:           testTenant,
		UseCase:          "classification-review",
		AuthorityOutcome: AuthorityOutcomeA1,
		RiskTier:         RiskTierT2,
		Region:           "eu-west-1",
		DataClasses:      []privacy.Class{privacy.P0, privacy.P5},
	}
}

func TestEvaluate(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*GovernanceContext)
		killed []AiUseCase
		global bool
		want   errs.ReasonCode // empty means allowed
	}{
		{name: "baseline is allowed", mutate: func(*GovernanceContext) {}},
		{name: "lower authority is allowed", mutate: func(g *GovernanceContext) { g.AuthorityOutcome = AuthorityOutcomeA0 }},
		{name: "lower risk is allowed", mutate: func(g *GovernanceContext) { g.RiskTier = RiskTierT0 }},
		{name: "second permitted region", mutate: func(g *GovernanceContext) { g.Region = "eu-central-1" }},
		{name: "A4 within an A4 ceiling", mutate: func(g *GovernanceContext) {
			g.UseCase, g.AuthorityOutcome, g.RiskTier = "exposure-forecast", AuthorityOutcomeA4, RiskTierT3
		}},

		// Malformed context.
		{name: "no tenant", mutate: func(g *GovernanceContext) { g.Tenant = id.TenantID{} }, want: ReasonMalformedContext},
		{name: "empty use case", mutate: func(g *GovernanceContext) { g.UseCase = "" }, want: ReasonMalformedContext},
		{name: "unknown authority", mutate: func(g *GovernanceContext) { g.AuthorityOutcome = "A9" }, want: ReasonMalformedContext},
		{name: "empty authority", mutate: func(g *GovernanceContext) { g.AuthorityOutcome = "" }, want: ReasonMalformedContext},
		{name: "unknown risk tier", mutate: func(g *GovernanceContext) { g.RiskTier = "T5" }, want: ReasonMalformedContext},
		{name: "empty region", mutate: func(g *GovernanceContext) { g.Region = "" }, want: ReasonMalformedContext},
		{name: "no data classes", mutate: func(g *GovernanceContext) { g.DataClasses = nil }, want: ReasonMalformedContext},
		{name: "unknown data class", mutate: func(g *GovernanceContext) { g.DataClasses = []privacy.Class{"P9"} }, want: ReasonMalformedContext},

		// Kill switch, before registration.
		{name: "global kill", mutate: func(*GovernanceContext) {}, global: true, want: ReasonKillSwitchEngaged},
		{name: "global kill reports kill for an unknown use case", mutate: func(g *GovernanceContext) { g.UseCase = "nobody-registered-this" }, global: true, want: ReasonKillSwitchEngaged},
		{name: "per-use-case kill", mutate: func(*GovernanceContext) {}, killed: []AiUseCase{"classification-review"}, want: ReasonKillSwitchEngaged},
		{name: "kill of another use case does not apply", mutate: func(*GovernanceContext) {}, killed: []AiUseCase{"explanation"}},
		{name: "kill switch beats A5", mutate: func(g *GovernanceContext) { g.AuthorityOutcome = AuthorityOutcomeA5 }, global: true, want: ReasonKillSwitchEngaged},

		// Registration.
		{name: "unknown use case", mutate: func(g *GovernanceContext) { g.UseCase = "nobody-registered-this" }, want: ReasonUnknownUseCase},
		{name: "suspended use case", mutate: func(g *GovernanceContext) {
			g.UseCase, g.AuthorityOutcome, g.RiskTier, g.DataClasses = "explanation", AuthorityOutcomeA0, RiskTierT0, []privacy.Class{privacy.P0}
		}, want: ReasonUseCaseSuspended},

		// Authority.
		{name: "A5 is refused", mutate: func(g *GovernanceContext) { g.AuthorityOutcome = AuthorityOutcomeA5 }, want: ReasonAuthorityRefused},
		{name: "A5 is refused under an A4 ceiling", mutate: func(g *GovernanceContext) {
			g.UseCase, g.AuthorityOutcome, g.RiskTier = "exposure-forecast", AuthorityOutcomeA5, RiskTierT0
		}, want: ReasonAuthorityRefused},
		{name: "above the use case ceiling", mutate: func(g *GovernanceContext) { g.AuthorityOutcome = AuthorityOutcomeA2 }, want: ReasonAuthorityRefused},

		// Risk.
		{name: "above the risk ceiling", mutate: func(g *GovernanceContext) { g.RiskTier = RiskTierT3 }, want: ReasonRiskTierExceeded},
		{name: "T4 is never within a ceiling", mutate: func(g *GovernanceContext) {
			g.UseCase, g.AuthorityOutcome, g.RiskTier = "exposure-forecast", AuthorityOutcomeA0, RiskTierT4
		}, want: ReasonRiskTierExceeded},

		// Residency.
		{name: "undeclared region", mutate: func(g *GovernanceContext) { g.Region = "us-east-1" }, want: ReasonResidencyRefused},
		{name: "region match is exact", mutate: func(g *GovernanceContext) { g.Region = "EU-WEST-1" }, want: ReasonResidencyRefused},

		// Data classes.
		{name: "undeclared data class", mutate: func(g *GovernanceContext) { g.DataClasses = []privacy.Class{privacy.P3} }, want: ReasonDataClassRefused},
		{name: "one undeclared class in a set", mutate: func(g *GovernanceContext) { g.DataClasses = []privacy.Class{privacy.P0, privacy.P4} }, want: ReasonDataClassRefused},
		{name: "secrets never reach a model", mutate: func(g *GovernanceContext) { g.DataClasses = []privacy.Class{privacy.P7} }, want: ReasonDataClassRefused},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := baseContext()
			tc.mutate(&g)
			d := Evaluate(testPolicy(t, tc.killed, tc.global), g)
			if tc.want == "" {
				if !d.Allowed || d.Reason != "" || d.Err() != nil {
					t.Fatalf("want allowed, got %+v", d)
				}
				return
			}
			if d.Allowed {
				t.Fatalf("want %s, got allowed", tc.want)
			}
			if d.Reason != tc.want {
				t.Fatalf("reason = %s, want %s (%s)", d.Reason, tc.want, d.Detail)
			}
			if got := errs.ReasonOf(d.Err()); got != tc.want {
				t.Fatalf("Err() reason = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestZeroPolicyRefusesEverything(t *testing.T) {
	d := Evaluate(Policy{}, baseContext())
	if d.Allowed || d.Reason != ReasonUnknownUseCase {
		t.Fatalf("zero Policy: got %+v", d)
	}
}

func TestDecisionErrCategories(t *testing.T) {
	for _, code := range RefusalCodes {
		if !errs.Registered(code) {
			t.Errorf("%s is not registered", code)
		}
		want := errs.CategoryPolicy
		if code == ReasonMalformedContext {
			want = errs.CategoryInternal
		}
		if got := errs.CategoryOf(Decision{Reason: code, Detail: "x"}.Err()); got != want {
			t.Errorf("%s: category %s, want %s", code, got, want)
		}
	}
}

func TestNewPolicyRefuses(t *testing.T) {
	valid := UseCasePolicy{
		UseCase: "uc", Owner: "lane-l", MaxAuthority: AuthorityOutcomeA1, MaxRiskTier: RiskTierT1,
		PermittedRegions: []string{"eu-west-1"}, PermittedDataClasses: []privacy.Class{privacy.P0},
	}
	if _, err := NewPolicy([]UseCasePolicy{valid}, nil, false); err != nil {
		t.Fatalf("valid policy refused: %v", err)
	}
	cases := map[string]func(*UseCasePolicy){
		"empty identifier":   func(u *UseCasePolicy) { u.UseCase = "" },
		"no owner":           func(u *UseCasePolicy) { u.Owner = "" },
		"A5 ceiling":         func(u *UseCasePolicy) { u.MaxAuthority = AuthorityOutcomeA5 },
		"unknown authority":  func(u *UseCasePolicy) { u.MaxAuthority = "A6" },
		"T4 ceiling":         func(u *UseCasePolicy) { u.MaxRiskTier = RiskTierT4 },
		"unknown risk tier":  func(u *UseCasePolicy) { u.MaxRiskTier = "" },
		"empty region":       func(u *UseCasePolicy) { u.PermittedRegions = []string{""} },
		"unknown data class": func(u *UseCasePolicy) { u.PermittedDataClasses = []privacy.Class{"PX"} },
		"secrets permitted":  func(u *UseCasePolicy) { u.PermittedDataClasses = []privacy.Class{privacy.P0, privacy.P7} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			u := valid
			mutate(&u)
			if _, err := NewPolicy([]UseCasePolicy{u}, nil, false); err == nil {
				t.Fatal("NewPolicy accepted it")
			}
		})
	}
	t.Run("duplicate", func(t *testing.T) {
		if _, err := NewPolicy([]UseCasePolicy{valid, valid}, nil, false); err == nil {
			t.Fatal("NewPolicy accepted a duplicate")
		}
	})
}

func TestPolicyIsASnapshot(t *testing.T) {
	regions := []string{"eu-west-1"}
	p, err := NewPolicy([]UseCasePolicy{{
		UseCase: "uc", Owner: "o", MaxAuthority: AuthorityOutcomeA0, MaxRiskTier: RiskTierT0,
		PermittedRegions: regions, PermittedDataClasses: []privacy.Class{privacy.P0},
	}}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	// Widening the caller's slice afterwards must not widen the policy.
	regions[0] = "us-east-1"
	g := GovernanceContext{Tenant: testTenant, UseCase: "uc", AuthorityOutcome: AuthorityOutcomeA0,
		RiskTier: RiskTierT0, Region: "us-east-1", DataClasses: []privacy.Class{privacy.P0}}
	if d := Evaluate(p, g); d.Allowed {
		t.Fatal("mutating the input slice widened the policy")
	}
	uc, _ := p.UseCase("uc")
	uc.PermittedRegions[0] = "us-east-1"
	if d := Evaluate(p, g); d.Allowed {
		t.Fatal("mutating an accessor's result widened the policy")
	}
}

// The gate's only input about a request is GovernanceContext. Pinning its
// field set means a field that could carry model output, prompt text or tool
// output cannot be added without changing this test, which is the review
// point ADR-0006 §2.5's "regardless of what any model or tool prompt says"
// needs.
func TestGovernanceContextCarriesNoContent(t *testing.T) {
	want := []string{"Tenant", "UseCase", "AuthorityOutcome", "RiskTier", "Region", "DataClasses"}
	typ := reflect.TypeFor[GovernanceContext]()
	var got []string
	for i := range typ.NumField() {
		got = append(got, typ.Field(i).Name)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("GovernanceContext fields = %v, want %v", got, want)
	}
}

func TestHighestDataClass(t *testing.T) {
	g := GovernanceContext{DataClasses: []privacy.Class{privacy.P1, privacy.P5, privacy.P0}}
	if got := g.HighestDataClass(); got != privacy.P5 {
		t.Fatalf("HighestDataClass = %s, want P5", got)
	}
	if got := (GovernanceContext{}).HighestDataClass(); got != "" {
		t.Fatalf("HighestDataClass of none = %q, want empty", got)
	}
}

func TestInvocationGovernanceTakesTenantAndRegionFromArguments(t *testing.T) {
	inv := Invocation{
		UseCase: "classification-review", AuthorityOutcome: AuthorityOutcomeA1, RiskTier: RiskTierT2,
		DataClasses: []privacy.Class{privacy.P0},
	}
	g := inv.Governance(testTenant, "eu-west-1")
	if g.Tenant != testTenant || g.Region != "eu-west-1" {
		t.Fatalf("Governance = %+v", g)
	}
	inv.DataClasses[0] = privacy.P7
	if g.DataClasses[0] != privacy.P0 {
		t.Fatal("Governance shares the invocation's data-class slice")
	}
}

// The property AIGOV-001 §15 asks to be proven rather than sampled: no input
// that carries A5 is ever allowed — whatever the registry says, whichever
// switches are set, whatever the rest of the context is.
func TestPropertyA5IsNeverAllowed(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		p, g := drawPolicyAndContext(rt)
		g.AuthorityOutcome = AuthorityOutcomeA5
		d := Evaluate(p, g)
		if d.Allowed {
			rt.Fatalf("A5 allowed: %+v", g)
		}
		if !slices.Contains(RefusalCodes, d.Reason) {
			rt.Fatalf("refusal %q is not in the closed set", d.Reason)
		}
	})
}

// Every decision is either allowed with no reason or refused with a code from
// the closed set, and an allowed decision always satisfies every ceiling.
func TestPropertyDecisionsAreWellFormed(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		p, g := drawPolicyAndContext(rt)
		d := Evaluate(p, g)
		if !d.Allowed {
			if !slices.Contains(RefusalCodes, d.Reason) {
				rt.Fatalf("refusal %q is not in the closed set", d.Reason)
			}
			return
		}
		if d.Reason != "" {
			rt.Fatalf("allowed decision carries reason %q", d.Reason)
		}
		uc, ok := p.UseCase(g.UseCase)
		switch {
		case !ok:
			rt.Fatal("allowed an unregistered use case")
		case p.Killed(g.UseCase) || uc.Suspended:
			rt.Fatal("allowed a killed or suspended use case")
		case g.AuthorityOutcome.rank() > uc.MaxAuthority.rank():
			rt.Fatal("allowed authority above the ceiling")
		case g.RiskTier.rank() > uc.MaxRiskTier.rank():
			rt.Fatal("allowed risk above the ceiling")
		case !slices.Contains(uc.PermittedRegions, g.Region):
			rt.Fatal("allowed an undeclared region")
		}
		for _, c := range g.DataClasses {
			if !slices.Contains(uc.PermittedDataClasses, c) || c == privacy.P7 {
				rt.Fatalf("allowed data class %s", c)
			}
		}
	})
}

var (
	drawUseCases = []AiUseCase{"uc-a", "uc-b", "uc-c", ""}
	drawRegions  = []string{"eu-west-1", "eu-central-1", "us-east-1", ""}
)

func drawPolicyAndContext(rt *rapid.T) (Policy, GovernanceContext) {
	var useCases []UseCasePolicy
	for _, name := range drawUseCases[:3] {
		if !rapid.Bool().Draw(rt, "register "+string(name)) {
			continue
		}
		var classes []privacy.Class
		for _, c := range privacy.Classes {
			if c != privacy.P7 && rapid.Bool().Draw(rt, string(name)+" permits "+string(c)) {
				classes = append(classes, c)
			}
		}
		useCases = append(useCases, UseCasePolicy{
			UseCase:              name,
			Owner:                "lane-l",
			MaxAuthority:         rapid.SampledFrom(AuthorityOutcomes[:5]).Draw(rt, string(name)+" authority"),
			MaxRiskTier:          rapid.SampledFrom(RiskTiers[:4]).Draw(rt, string(name)+" risk"),
			PermittedRegions:     rapid.SliceOfDistinct(rapid.SampledFrom(drawRegions[:3]), func(r string) string { return r }).Draw(rt, string(name)+" regions"),
			PermittedDataClasses: classes,
			Suspended:            rapid.Bool().Draw(rt, string(name)+" suspended"),
		})
	}
	killed := rapid.SliceOfDistinct(rapid.SampledFrom(drawUseCases), func(u AiUseCase) AiUseCase { return u }).Draw(rt, "killed")
	p, err := NewPolicy(useCases, killed, rapid.Bool().Draw(rt, "global kill"))
	if err != nil {
		rt.Fatalf("NewPolicy: %v", err)
	}

	tenant := testTenant
	if rapid.Bool().Draw(rt, "zero tenant") {
		tenant = id.TenantID{}
	}
	allClasses := append(slices.Clone(privacy.Classes), "P9", "")
	g := GovernanceContext{
		Tenant:           tenant,
		UseCase:          rapid.SampledFrom(drawUseCases).Draw(rt, "use case"),
		AuthorityOutcome: rapid.SampledFrom(append(slices.Clone(AuthorityOutcomes), "", "A6")).Draw(rt, "authority"),
		RiskTier:         rapid.SampledFrom(append(slices.Clone(RiskTiers), "", "T5")).Draw(rt, "risk"),
		Region:           rapid.SampledFrom(drawRegions).Draw(rt, "region"),
		DataClasses:      rapid.SliceOfN(rapid.SampledFrom(allClasses), 0, 4).Draw(rt, "data classes"),
	}
	return p, g
}
