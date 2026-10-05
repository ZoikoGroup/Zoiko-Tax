package gateway_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/adapter/gateway"
	"github.com/zoikogroup/zoikotax/backend/internal/adapter/gateway/gatewaytest"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/ai"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/privacy"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

var _ port.ModelGateway = (*gateway.Client)(nil)

var (
	tenant = id.NewTenantID(uuid.MustParse("22222222-2222-7222-8222-222222222222"))
	t0     = time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
)

const cell = "eu-west-1"

// movableClock is a clock a test can advance.
type movableClock struct{ now atomic.Int64 }

func (c *movableClock) Now() time.Time          { return time.Unix(0, c.now.Load()).UTC() }
func (c *movableClock) set(t time.Time)         { c.now.Store(t.UnixNano()) }
func (c *movableClock) advance(d time.Duration) { c.now.Add(int64(d)) }

// switchablePolicy is a PolicySource whose snapshot an operator can replace,
// which is how a kill switch reaches a running process.
type switchablePolicy struct{ p atomic.Pointer[ai.Policy] }

func (s *switchablePolicy) Policy() ai.Policy { return *s.p.Load() }

func policy(t *testing.T, killed []ai.AiUseCase) ai.Policy {
	t.Helper()
	p, err := ai.NewPolicy([]ai.UseCasePolicy{{
		UseCase:              "classification-review",
		Owner:                "lane-l",
		MaxAuthority:         ai.AuthorityOutcomeA1,
		MaxRiskTier:          ai.RiskTierT2,
		PermittedRegions:     []string{cell},
		PermittedDataClasses: []privacy.Class{privacy.P0, privacy.P1, privacy.P5},
	}}, killed, false)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func invocation() ai.Invocation {
	return ai.Invocation{
		UseCase:          "classification-review",
		AuthorityOutcome: ai.AuthorityOutcomeA1,
		RiskTier:         ai.RiskTierT2,
		DataClasses:      []privacy.Class{privacy.P0, privacy.P5},
		SubjectRef:       "doc-123",
		Input:            []byte(`{"sku":"VOICE-INTL"}`),
	}
}

func goodReply() gateway.Reply {
	return gateway.Reply{
		ModelProfile:    "model:classifier",
		ProviderProfile: "provider:eu-hosted",
		PromptProfile:   "prompt:classification-review",
		AiTrainVersion:  "0.5.0",
		Text:            "Consider classifying this as international voice.",
		Fields:          map[string]string{"currency": "EUR"},
		ProposedCode:    "TEL.VOICE.INTL",
		Confidence:      "0.91",
	}
}

type harness struct {
	client    *gateway.Client
	transport *gatewaytest.Fake
	recorder  *gatewaytest.Recorder
	clock     *movableClock
	policy    *switchablePolicy
}

func newHarness(t *testing.T, cfg func(*gateway.Config)) *harness {
	t.Helper()
	h := &harness{
		transport: &gatewaytest.Fake{Reply: goodReply()},
		recorder:  &gatewaytest.Recorder{},
		clock:     &movableClock{},
		policy:    &switchablePolicy{},
	}
	h.clock.set(t0)
	p := policy(t, nil)
	h.policy.p.Store(&p)
	c := gateway.Config{
		Transport: h.transport,
		Policy:    h.policy,
		Recorder:  h.recorder,
		Clock:     h.clock,
		IDs:       &idgen.Sequential{},
		Region:    cell,
		Deadline:  time.Second,
	}
	if cfg != nil {
		cfg(&c)
	}
	client, err := gateway.New(c)
	if err != nil {
		t.Fatal(err)
	}
	h.client = client
	return h
}

func tenantCtx() context.Context {
	return security.Into(context.Background(), security.System(tenant))
}

func wantReason(t *testing.T, err error, reason errs.ReasonCode, category errs.Category) {
	t.Helper()
	if err == nil {
		t.Fatalf("want %s, got no error", reason)
	}
	if got := errs.ReasonOf(err); got != reason {
		t.Fatalf("reason = %s, want %s (%v)", got, reason, err)
	}
	if got := errs.CategoryOf(err); got != category {
		t.Fatalf("category = %s, want %s", got, category)
	}
}

func TestSuggestCarriesGovernanceAndEvidencesTheTransfer(t *testing.T) {
	h := newHarness(t, nil)
	s, err := h.client.Suggest(tenantCtx(), invocation())
	if err != nil {
		t.Fatal(err)
	}

	calls := h.transport.Calls()
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	g := calls[0].Governance
	if g.Tenant != tenant || g.Region != cell || g.UseCase != "classification-review" ||
		g.AuthorityOutcome != ai.AuthorityOutcomeA1 || g.RiskTier != ai.RiskTierT2 {
		t.Fatalf("governance on the wire = %+v", g)
	}
	if calls[0].Kind != gateway.KindSuggestion {
		t.Fatalf("kind = %s", calls[0].Kind)
	}

	want := ai.Provenance{
		UseCase: "classification-review", ModelProfile: "model:classifier",
		ProviderProfile: "provider:eu-hosted", PromptProfile: "prompt:classification-review",
		Region: cell, DataClass: "P5", RiskTier: ai.RiskTierT2,
		AuthorityOutcome: ai.AuthorityOutcomeA1, AiTrainVersion: "0.5.0",
	}
	if s.Provenance() != want {
		t.Fatalf("provenance = %+v, want %+v", s.Provenance(), want)
	}
	if s.SubjectRef() != "doc-123" || !s.ReceivedAt().Equal(t0) || s.ID().String() == "" {
		t.Fatalf("suggestion = %+v", s)
	}

	tr := h.recorder.Transfers()
	if len(tr) != 1 || tr[0].Outcome != gateway.OutcomeCompleted || tr[0].Reason != "" {
		t.Fatalf("transfers = %+v", tr)
	}
	if tr[0].ModelProfile != "model:classifier" || tr[0].DataClass != privacy.P5 || tr[0].Tenant != tenant {
		t.Fatalf("transfer = %+v", tr[0])
	}
}

func TestExtractAndProposeClassification(t *testing.T) {
	h := newHarness(t, nil)
	e, err := h.client.Extract(tenantCtx(), invocation())
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := e.Field("currency"); v != "EUR" {
		t.Fatalf("extraction fields = %v", e.Fields())
	}
	p, err := h.client.ProposeClassification(tenantCtx(), invocation())
	if err != nil {
		t.Fatal(err)
	}
	if p.ProposedCode() != "TEL.VOICE.INTL" || p.Confidence() != "0.91" {
		t.Fatalf("proposal = %+v", p)
	}
	calls := h.transport.Calls()
	if calls[0].Kind != gateway.KindExtraction || calls[1].Kind != gateway.KindClassificationProposal {
		t.Fatalf("kinds = %s, %s", calls[0].Kind, calls[1].Kind)
	}
}

// A refusal by the Go gate never leaves the process: the transport sees
// nothing. That is the property the pre-check exists for.
func TestRefusalsNeverReachTheTransport(t *testing.T) {
	cases := []struct {
		name   string
		ctx    context.Context
		mutate func(*ai.Invocation)
		want   errs.ReasonCode
		cat    errs.Category
	}{
		{"A5", tenantCtx(), func(i *ai.Invocation) { i.AuthorityOutcome = ai.AuthorityOutcomeA5 }, ai.ReasonAuthorityRefused, errs.CategoryPolicy},
		{"no security context", context.Background(), func(*ai.Invocation) {}, ai.ReasonMalformedContext, errs.CategoryInternal},
		{"unknown use case", tenantCtx(), func(i *ai.Invocation) { i.UseCase = "nobody-registered-this" }, ai.ReasonUnknownUseCase, errs.CategoryPolicy},
		{"risk above ceiling", tenantCtx(), func(i *ai.Invocation) { i.RiskTier = ai.RiskTierT4 }, ai.ReasonRiskTierExceeded, errs.CategoryPolicy},
		{"secrets", tenantCtx(), func(i *ai.Invocation) { i.DataClasses = []privacy.Class{privacy.P7} }, ai.ReasonDataClassRefused, errs.CategoryPolicy},
		{"no data classes", tenantCtx(), func(i *ai.Invocation) { i.DataClasses = nil }, ai.ReasonMalformedContext, errs.CategoryInternal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, nil)
			inv := invocation()
			tc.mutate(&inv)
			_, err := h.client.Suggest(tc.ctx, inv)
			wantReason(t, err, tc.want, tc.cat)
			if n := len(h.transport.Calls()); n != 0 {
				t.Fatalf("transport saw %d calls", n)
			}
			tr := h.recorder.Transfers()
			if len(tr) != 1 || tr[0].Outcome != gateway.OutcomeRefused || tr[0].Reason != tc.want {
				t.Fatalf("transfers = %+v", tr)
			}
		})
	}
}

// The region is the cell's, so a model reply cannot relocate a call, and an
// invocation has no field with which to claim another region.
func TestRegionIsTheCells(t *testing.T) {
	h := newHarness(t, func(c *gateway.Config) { c.Region = "us-east-1" })
	_, err := h.client.Suggest(tenantCtx(), invocation())
	wantReason(t, err, ai.ReasonResidencyRefused, errs.CategoryPolicy)
	if len(h.transport.Calls()) != 0 {
		t.Fatal("a call from an undeclared cell reached the transport")
	}
}

func TestKillSwitchTakesEffectOnTheNextCall(t *testing.T) {
	h := newHarness(t, nil)
	if _, err := h.client.Suggest(tenantCtx(), invocation()); err != nil {
		t.Fatal(err)
	}
	killed := policy(t, []ai.AiUseCase{"classification-review"})
	h.policy.p.Store(&killed)
	_, err := h.client.Suggest(tenantCtx(), invocation())
	wantReason(t, err, ai.ReasonKillSwitchEngaged, errs.CategoryPolicy)
	if len(h.transport.Calls()) != 1 {
		t.Fatal("a killed use case reached the transport")
	}
}

func TestGatewayRefusalPassesThroughAndDoesNotTripTheBreaker(t *testing.T) {
	h := newHarness(t, func(c *gateway.Config) { c.Breaker = gateway.NewBreaker(1, time.Minute) })
	h.transport.Err = errs.New(errs.CategoryPolicy, ai.ReasonUnknownUseCase, "refused by the Gateway")
	for range 3 {
		_, err := h.client.Suggest(tenantCtx(), invocation())
		wantReason(t, err, ai.ReasonUnknownUseCase, errs.CategoryPolicy)
	}
	if len(h.transport.Calls()) != 3 {
		t.Fatal("a Gateway refusal opened the breaker")
	}
	if tr := h.recorder.Transfers(); tr[0].Outcome != gateway.OutcomeRefused {
		t.Fatalf("outcome = %s, want REFUSED", tr[0].Outcome)
	}
}

func TestDeadlineIsHard(t *testing.T) {
	h := newHarness(t, func(c *gateway.Config) { c.Deadline = 20 * time.Millisecond })
	h.transport.Respond = func(ctx context.Context, _ gateway.Call) (gateway.Reply, error) {
		<-ctx.Done()
		return gateway.Reply{}, ctx.Err()
	}
	done := make(chan error, 1)
	go func() {
		_, err := h.client.Suggest(tenantCtx(), invocation())
		done <- err
	}()
	select {
	case err := <-done:
		wantReason(t, err, gateway.ReasonGatewayUnavailable, errs.CategoryUnavailable)
	case <-time.After(5 * time.Second):
		t.Fatal("the deadline did not bound the call")
	}
	if tr := h.recorder.Transfers(); len(tr) != 1 || tr[0].Outcome != gateway.OutcomeFailed {
		t.Fatalf("transfers = %+v", tr)
	}
}

func TestBreakerOpensAndProbes(t *testing.T) {
	h := newHarness(t, func(c *gateway.Config) { c.Breaker = gateway.NewBreaker(2, 30*time.Second) })
	h.transport.Err = errors.New("connection refused")

	for range 2 {
		_, err := h.client.Suggest(tenantCtx(), invocation())
		wantReason(t, err, gateway.ReasonGatewayUnavailable, errs.CategoryUnavailable)
	}
	// Open: refused without calling.
	_, err := h.client.Suggest(tenantCtx(), invocation())
	wantReason(t, err, gateway.ReasonGatewayUnavailable, errs.CategoryUnavailable)
	if n := len(h.transport.Calls()); n != 2 {
		t.Fatalf("calls = %d, want 2 (breaker open)", n)
	}

	// After the cooldown one probe goes through; it succeeds and closes it.
	h.clock.advance(31 * time.Second)
	h.transport.Err = nil
	if _, err := h.client.Suggest(tenantCtx(), invocation()); err != nil {
		t.Fatalf("probe: %v", err)
	}
	if _, err := h.client.Suggest(tenantCtx(), invocation()); err != nil {
		t.Fatalf("after recovery: %v", err)
	}
	if n := len(h.transport.Calls()); n != 4 {
		t.Fatalf("calls = %d, want 4", n)
	}
}

func TestUnevidencedReplyIsDiscarded(t *testing.T) {
	h := newHarness(t, nil)
	r := goodReply()
	r.ProviderProfile = ""
	h.transport.Reply = r
	_, err := h.client.Suggest(tenantCtx(), invocation())
	wantReason(t, err, gateway.ReasonGatewayReplyUnevidenced, errs.CategoryInternal)
	if tr := h.recorder.Transfers(); tr[0].Outcome != gateway.OutcomeDiscarded {
		t.Fatalf("outcome = %s, want DISCARDED", tr[0].Outcome)
	}
}

func TestInvalidAdvisoryContentIsRefused(t *testing.T) {
	h := newHarness(t, nil)
	r := goodReply()
	r.Text = ""
	h.transport.Reply = r
	_, err := h.client.Suggest(tenantCtx(), invocation())
	wantReason(t, err, gateway.ReasonGatewayReplyUnevidenced, errs.CategoryInternal)
}

// An output whose crossing could not be recorded is not returned.
func TestNoEvidenceNoResult(t *testing.T) {
	h := newHarness(t, nil)
	h.recorder.Err = errors.New("log sink down")
	_, err := h.client.Suggest(tenantCtx(), invocation())
	wantReason(t, err, gateway.ReasonGatewayUnavailable, errs.CategoryUnavailable)
}

func TestUnconfiguredCellRefusesVisibly(t *testing.T) {
	h := newHarness(t, func(c *gateway.Config) { c.Transport = gateway.Unconfigured{} })
	_, err := h.client.Suggest(tenantCtx(), invocation())
	wantReason(t, err, gateway.ReasonGatewayNotConfigured, errs.CategoryUnsupported)
	if tr := h.recorder.Transfers(); len(tr) != 1 || tr[0].Outcome != gateway.OutcomeFailed {
		t.Fatalf("transfers = %+v", tr)
	}
}

func TestNewRefusesIncompleteWiring(t *testing.T) {
	full := gateway.Config{
		Transport: gateway.Unconfigured{},
		Policy:    gateway.StaticPolicy{},
		Recorder:  gateway.SlogRecorder{},
		Clock:     &movableClock{},
		IDs:       &idgen.Sequential{},
		Region:    cell,
		Deadline:  time.Second,
	}
	if _, err := gateway.New(full); err != nil {
		t.Fatalf("full config refused: %v", err)
	}
	for name, mutate := range map[string]func(*gateway.Config){
		"transport": func(c *gateway.Config) { c.Transport = nil },
		"policy":    func(c *gateway.Config) { c.Policy = nil },
		"recorder":  func(c *gateway.Config) { c.Recorder = nil },
		"clock":     func(c *gateway.Config) { c.Clock = nil },
		"ids":       func(c *gateway.Config) { c.IDs = nil },
		"region":    func(c *gateway.Config) { c.Region = "" },
		"deadline":  func(c *gateway.Config) { c.Deadline = 0 },
	} {
		c := full
		mutate(&c)
		if _, err := gateway.New(c); err == nil {
			t.Errorf("missing %s accepted", name)
		}
	}
}

func TestStaticZeroPolicyRefusesEverything(t *testing.T) {
	h := newHarness(t, func(c *gateway.Config) { c.Policy = gateway.StaticPolicy{} })
	_, err := h.client.Suggest(tenantCtx(), invocation())
	wantReason(t, err, ai.ReasonUnknownUseCase, errs.CategoryPolicy)
}
