package app_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/adapter/gateway"
	"github.com/zoikogroup/zoikotax/backend/internal/adapter/gateway/gatewaytest"
	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/ai"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/obligation"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/privacy"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/fiscaltest"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/clock"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
)

// B1: the obligation explanation service. Every test here runs the real
// gateway.Client over gatewaytest.Fake, so the policy gate, the breaker and the
// provenance check are the real ones. Synthetic rows only. Nothing here prints
// an explanation or an obligation field.

const (
	explainCell = "eu-west-1"
	// markerText is what a misbehaving model would put where only a fixed code
	// may go. It must never reach an outcome.
	markerText = "MODEL-TEXT-MARKER-7"
)

var explainOtherTenant = id.NewTenantID(uuid.MustParse("01920000-0000-7000-8000-0000000000dd"))

// explainReader is a ObligationReader that reads under the session's tenant,
// as the store does: another tenant's row is not found.
type explainReader struct {
	rows map[id.ObligationID]obligation.Obligation
}

func (r *explainReader) Obligation(ctx context.Context, oid id.ObligationID) (app.ObligationView, error) {
	sc, _ := security.From(ctx)
	o, ok := r.rows[oid]
	if !ok || o.TenantID != sc.Tenant() {
		return app.ObligationView{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "not found")
	}
	return app.ObligationView{Obligation: o, EffectiveStatus: o.Status}, nil
}

// ignoringReader returns the row whatever the tenant: a reader that does not
// scope, which the service must not trust.
type ignoringReader struct{ o obligation.Obligation }

func (r ignoringReader) Obligation(context.Context, id.ObligationID) (app.ObligationView, error) {
	return app.ObligationView{Obligation: r.o, EffectiveStatus: r.o.Status}, nil
}

func explainRow(t *testing.T, tenant id.TenantID) obligation.Obligation {
	t.Helper()
	amount := fiscaltest.Money(t, "42.00", fiscal.Currency("EUR"))
	return obligation.Obligation{
		ID: id.NewObligationID(uuid.Must(uuid.NewV7())), TenantID: tenant, BusinessKey: "k",
		LegalEntity: id.NewLegalEntityID(uuid.Must(uuid.NewV7())), RecordedBy: id.NewUserID(uuid.Must(uuid.NewV7())),
		JurisdictionID: "jurisdiction:xa", Type: "VAT_RETURN", Authority: "authority:xa-revenue",
		Duty: obligation.DutyTransactionMonetary, Status: obligation.StatusOpen, Timezone: "UTC",
		Period: obligation.Period{
			Start: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC),
			Due: time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC),
		},
		Assessed: &amount,
	}
}

// explainPolicy approves the explainer for the classes it declares, so the
// tests about the answer can reach the transport. The real registry's refusal
// is tested apart.
func explainPolicy(t *testing.T) ai.Policy {
	t.Helper()
	p, err := ai.NewPolicy([]ai.UseCasePolicy{{
		UseCase: app.ExplainObligationUseCase, Owner: "lane-l",
		MaxAuthority: ai.AuthorityOutcomeA1, MaxRiskTier: ai.RiskTierT1,
		PermittedRegions: []string{explainCell}, PermittedDataClasses: []privacy.Class{privacy.P0, privacy.P5},
	}}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func explainClient(t *testing.T, transport gateway.Transport, pol ai.Policy, region string, breaker *gateway.Breaker) *gateway.Client {
	t.Helper()
	c, err := gateway.New(gateway.Config{
		Transport: transport, Policy: gateway.StaticPolicy{P: pol}, Recorder: &gatewaytest.Recorder{},
		Clock: clock.System{}, IDs: &idgen.Sequential{}, Region: region, Deadline: 50 * time.Millisecond, Breaker: breaker,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

type explainRig struct {
	svc       *app.ObligationExplanationService
	transport *gatewaytest.Fake
	row       obligation.Obligation
}

func newExplainRig(t *testing.T) *explainRig {
	t.Helper()
	row := explainRow(t, tenantID)
	transport := &gatewaytest.Fake{Reply: answeredReply(row.ID.String())}
	client := explainClient(t, transport, explainPolicy(t), explainCell, nil)
	reader := &explainReader{rows: map[id.ObligationID]obligation.Obligation{row.ID: row}}
	return &explainRig{svc: app.NewObligationExplanationService(client, reader), transport: transport, row: row}
}

func goodRouting() gateway.Reply {
	return gateway.Reply{
		ModelProfile: "model:explain-test", ProviderProfile: "provider:test",
		PromptProfile: "prompt:explain-obligation-row-v1", AiTrainVersion: "ai-test-1",
	}
}

func replyWith(fields map[string]string) gateway.Reply {
	r := goodRouting()
	r.Fields = fields
	return r
}

func answeredReply(rowID string) gateway.Reply {
	return replyWith(map[string]string{
		"status": "ANSWERED", "cited_ids": `["` + rowID + `"]`, "validator_version": "explanation_guard@1",
		"explanation": "Synthetic explanation text for a test row.",
	})
}

func explainAs(ctx context.Context, rig *explainRig) (app.ExplanationOutcome, error) {
	return rig.svc.Explain(ctx, rig.row.ID)
}

func TestAnAnsweredExplanationThatCitesTheRowIsReturned(t *testing.T) {
	rig := newExplainRig(t)
	o, err := explainAs(as(security.RoleAnalyst), rig)
	if err != nil {
		t.Fatal(err)
	}
	if o.Status != app.ExplanationAnswered || o.ReviewReason != "" || o.Explanation == "" {
		t.Fatalf("outcome = %+v, want ANSWERED with text and no reason", o)
	}
	if len(o.CitedIDs) != 1 || o.CitedIDs[0] != rig.row.ID.String() || o.ValidatorVersion != "explanation_guard@1" {
		t.Fatalf("cited ids / validator = %v / %q", o.CitedIDs, o.ValidatorVersion)
	}
	if !o.HasProvenance || o.Provenance.UseCase != app.ExplainObligationUseCase || o.Provenance.ModelProfile == "" {
		t.Fatalf("provenance = %+v", o.Provenance)
	}
}

func TestAbstainedAndRefusedAnswersGiveNoText(t *testing.T) {
	for status, want := range map[string]errs.ReasonCode{
		"ABSTAINED": ai.ReasonExplanationAbstained, "REFUSED": ai.ReasonExplanationRefused,
	} {
		t.Run(status, func(t *testing.T) {
			rig := newExplainRig(t)
			// Everything else in the reply looks perfect, including text and a
			// citation: the status alone decides.
			rig.transport.Reply = replyWith(map[string]string{
				"status": status, "cited_ids": `["` + rig.row.ID.String() + `"]`, "explanation": "text that must not come back",
				"reason_code": markerText,
			})
			o, err := explainAs(as(security.RoleOperator), rig)
			if err != nil {
				t.Fatal(err)
			}
			if o.Status != app.ExplanationAbstained || o.ReviewReason != want || o.Explanation != "" || len(o.CitedIDs) != 0 {
				t.Fatalf("outcome = %+v, want ABSTAINED / %s with no text", o, want)
			}
		})
	}
}

func TestAnUnusableAnswerGivesNoTextAndReviewRequired(t *testing.T) {
	rowID := func(rig *explainRig) string { return rig.row.ID.String() }
	foreign := uuid.Must(uuid.NewV7()).String()
	cases := map[string]func(rig *explainRig) map[string]string{
		"no status": func(rig *explainRig) map[string]string {
			return map[string]string{"cited_ids": `["` + rowID(rig) + `"]`, "explanation": "text"}
		},
		"empty status": func(rig *explainRig) map[string]string {
			return map[string]string{"status": "", "cited_ids": `["` + rowID(rig) + `"]`, "explanation": "text"}
		},
		"unknown status": func(rig *explainRig) map[string]string {
			return map[string]string{"status": "PASSED", "cited_ids": `["` + rowID(rig) + `"]`, "explanation": "text"}
		},
		"lower-case status": func(rig *explainRig) map[string]string {
			return map[string]string{"status": "answered", "cited_ids": `["` + rowID(rig) + `"]`, "explanation": "text"}
		},
		"padded status": func(rig *explainRig) map[string]string {
			return map[string]string{"status": " ANSWERED", "cited_ids": `["` + rowID(rig) + `"]`, "explanation": "text"}
		},
		"no cited ids": func(*explainRig) map[string]string {
			return map[string]string{"status": "ANSWERED", "explanation": "text"}
		},
		"cited ids not json": func(*explainRig) map[string]string {
			return map[string]string{"status": "ANSWERED", "cited_ids": "not json", "explanation": "text"}
		},
		"cited ids an object": func(*explainRig) map[string]string {
			return map[string]string{"status": "ANSWERED", "cited_ids": `{"a":1}`, "explanation": "text"}
		},
		"cited ids not strings": func(*explainRig) map[string]string {
			return map[string]string{"status": "ANSWERED", "cited_ids": `[1]`, "explanation": "text"}
		},
		"cited ids empty": func(*explainRig) map[string]string {
			return map[string]string{"status": "ANSWERED", "cited_ids": `[]`, "explanation": "text"}
		},
		"cited id blank": func(*explainRig) map[string]string {
			return map[string]string{"status": "ANSWERED", "cited_ids": `[""]`, "explanation": "text"}
		},
		"cited id not supplied": func(*explainRig) map[string]string {
			return map[string]string{"status": "ANSWERED", "cited_ids": `["` + foreign + `"]`, "explanation": "text"}
		},
		"one cited id not supplied": func(rig *explainRig) map[string]string {
			return map[string]string{"status": "ANSWERED", "cited_ids": `["` + rowID(rig) + `","` + foreign + `"]`, "explanation": "text"}
		},
		"cited ids with trailing text": func(rig *explainRig) map[string]string {
			return map[string]string{"status": "ANSWERED", "cited_ids": `["` + rowID(rig) + `"] x`, "explanation": "text"}
		},
		"no explanation": func(rig *explainRig) map[string]string {
			return map[string]string{"status": "ANSWERED", "cited_ids": `["` + rowID(rig) + `"]`}
		},
		"blank explanation": func(rig *explainRig) map[string]string {
			return map[string]string{"status": "ANSWERED", "cited_ids": `["` + rowID(rig) + `"]`, "explanation": "  \n "}
		},
		"explanation too long": func(rig *explainRig) map[string]string {
			return map[string]string{"status": "ANSWERED", "cited_ids": `["` + rowID(rig) + `"]`, "explanation": strings.Repeat("x", 8*1024+1)}
		},
		"no fields at all": func(*explainRig) map[string]string { return nil },
	}
	for name, fields := range cases {
		t.Run(name, func(t *testing.T) {
			rig := newExplainRig(t)
			f := fields(rig)
			if f != nil {
				f["reason_code"] = markerText // a model-written reason, which must never be echoed
			}
			rig.transport.Reply = replyWith(f)
			o, err := explainAs(as(security.RoleAnalyst), rig)
			if err != nil {
				t.Fatal(err)
			}
			if o.Status != app.ExplanationReviewRequired || o.ReviewReason != ai.ReasonExplanationReplyUnusable {
				t.Fatalf("outcome status/reason = %s / %s, want REVIEW_REQUIRED / %s", o.Status, o.ReviewReason, ai.ReasonExplanationReplyUnusable)
			}
			if o.Explanation != "" || len(o.CitedIDs) != 0 {
				t.Fatal("an unusable answer returned text or citations")
			}
			if text, _ := json.Marshal(o); strings.Contains(string(text), markerText) {
				t.Fatal("model text reached the outcome")
			}
		})
	}
}

func TestOnlyAFixedCodeEverReachesTheReason(t *testing.T) {
	rig := newExplainRig(t)
	allowed := map[errs.ReasonCode]bool{
		ai.ReasonExplanationAbstained: true, ai.ReasonExplanationRefused: true, ai.ReasonExplanationReplyUnusable: true,
	}
	for _, status := range []string{"ABSTAINED", "REFUSED", "ANSWERED", "??", ""} {
		rig.transport.Reply = replyWith(map[string]string{
			"status": status, "reason": markerText, "reason_code": markerText, "cited_ids": "[]", "explanation": markerText,
		})
		o, err := explainAs(as(security.RoleAnalyst), rig)
		if err != nil {
			t.Fatal(err)
		}
		if !allowed[o.ReviewReason] || !errs.Registered(o.ReviewReason) {
			t.Fatalf("status %q gave reason %q, which is not one of our fixed codes", status, o.ReviewReason)
		}
	}
}

func TestTheGatewayBeingAbsentDownHungOrUnevidencedGivesReviewRequired(t *testing.T) {
	row := explainRow(t, tenantID)
	reader := &explainReader{rows: map[id.ObligationID]obligation.Obligation{row.ID: row}}
	unevidenced := goodRouting()
	unevidenced.ProviderProfile = ""
	unevidenced.Fields = map[string]string{"status": "ANSWERED"}
	cases := []struct {
		name      string
		transport gateway.Transport
		want      errs.ReasonCode
	}{
		{"down", &gatewaytest.Fake{Err: downErr()}, gateway.ReasonGatewayUnavailable},
		{"hangs", &gatewaytest.Fake{Respond: func(ctx context.Context, _ gateway.Call) (gateway.Reply, error) {
			<-ctx.Done()
			return gateway.Reply{}, ctx.Err()
		}}, gateway.ReasonGatewayUnavailable},
		{"not configured", gateway.Unconfigured{}, ai.ReasonGatewayNotConfigured},
		{"answer without provenance", &gatewaytest.Fake{Reply: unevidenced}, ai.ReasonGatewayReplyUnevidenced},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc := app.NewObligationExplanationService(explainClient(t, c.transport, explainPolicy(t), explainCell, nil), reader)
			o, err := svc.Explain(bounded(t), row.ID)
			if err != nil {
				t.Fatalf("a degraded AI plane is an outcome, not an error: %v", err)
			}
			if o.Status != app.ExplanationReviewRequired || o.ReviewReason != c.want || o.Explanation != "" || o.HasProvenance {
				t.Fatalf("outcome = %+v, want REVIEW_REQUIRED / %s with nothing else", o, c.want)
			}
		})
	}
}

func TestAbstainsAndUnusableAnswersNeverTouchTheBreaker(t *testing.T) {
	const threshold = 5
	row := explainRow(t, tenantID)
	transport := &gatewaytest.Fake{}
	client := explainClient(t, transport, explainPolicy(t), explainCell, gateway.NewBreaker(threshold, time.Hour))
	svc := app.NewObligationExplanationService(client, &explainReader{rows: map[id.ObligationID]obligation.Obligation{row.ID: row}})

	// Consecutive answers of each kind, more than the threshold: a breaker that
	// counted any of them would be open by now.
	sent := 0
	for _, reply := range []gateway.Reply{
		replyWith(map[string]string{"status": "ABSTAINED"}),
		replyWith(map[string]string{"status": "REFUSED"}),
		replyWith(map[string]string{"status": "no such status"}),                                  // unusable
		replyWith(map[string]string{"status": "ANSWERED", "cited_ids": `[]`, "explanation": "x"}), // unusable
	} {
		transport.Reply = reply
		for range 3 * threshold {
			if _, err := svc.Explain(as(security.RoleAnalyst), row.ID); err != nil {
				t.Fatal(err)
			}
			sent++
		}
		if n := len(transport.Calls()); n != sent {
			t.Fatalf("the transport saw %d calls, want %d: the breaker opened on answers", n, sent)
		}
	}
	// And the next real answer still goes through.
	transport.Reply = answeredReply(row.ID.String())
	o, err := svc.Explain(as(security.RoleAnalyst), row.ID)
	if err != nil || o.Status != app.ExplanationAnswered {
		t.Fatalf("after the abstains: %+v, %v", o, err)
	}
}

func TestAGovernanceRefusalIsTheGatewaysErrorNotAnOutcome(t *testing.T) {
	rig := newExplainRig(t)
	rig.transport.Err = errs.New(errs.CategoryPolicy, ai.ReasonKillSwitchEngaged, "refused by the Gateway")
	_, err := explainAs(as(security.RoleOperator), rig)
	if errs.ReasonOf(err) != ai.ReasonKillSwitchEngaged || errs.CategoryOf(err) != errs.CategoryPolicy {
		t.Fatalf("err = %v, want the Gateway's kill-switch refusal", err)
	}
}

// The registry the base stack really loads permits this use case P0 only. The
// row carries the assessed amount, so the call is refused at the gate, in this
// process, and nothing is sent. That is the intended safe state until the use
// case is approved for P5.
func TestTheRealRegistriesRefuseTheRowForItsAmount(t *testing.T) {
	for _, name := range []string{"registry.dev.json", "registry.live-demo.json"} {
		t.Run(name, func(t *testing.T) {
			pol, err := gateway.LoadPolicy(filepath.Join(repoRoot(t), "intelligence", "config", name))
			if err != nil {
				t.Fatal(err)
			}
			row := explainRow(t, tenantID)
			transport := &gatewaytest.Fake{Reply: answeredReply(row.ID.String())}
			client := explainClient(t, transport, pol, "local", nil)
			svc := app.NewObligationExplanationService(client, &explainReader{rows: map[id.ObligationID]obligation.Obligation{row.ID: row}})

			_, err = svc.Explain(as(security.RoleAnalyst), row.ID)
			if errs.ReasonOf(err) != ai.ReasonDataClassRefused || errs.CategoryOf(err) != errs.CategoryPolicy {
				t.Fatalf("err = %v, want %s", err, ai.ReasonDataClassRefused)
			}
			if n := len(transport.Calls()); n != 0 {
				t.Fatalf("a refused row reached the transport %d time(s)", n)
			}
		})
	}
}

func TestTheRowSentHoldsOnlyTheTableFieldsAndDeclaresTheirClasses(t *testing.T) {
	rig := newExplainRig(t)
	if _, err := explainAs(as(security.RoleAnalyst), rig); err != nil {
		t.Fatal(err)
	}
	calls := rig.transport.Calls()
	if len(calls) != 1 {
		t.Fatalf("calls = %d", len(calls))
	}
	call := calls[0]
	g := call.Governance
	if g.UseCase != app.ExplainObligationUseCase || g.AuthorityOutcome != "A1" || g.RiskTier != "T1" || g.Region != explainCell {
		t.Fatalf("governance = %+v", g)
	}
	var sent struct {
		Kind string                       `json:"kind"`
		Rows []map[string]json.RawMessage `json:"rows"`
	}
	if err := json.Unmarshal(call.Input, &sent); err != nil || sent.Kind != "obligation-row" || len(sent.Rows) != 1 {
		t.Fatalf("input shape: %v / %+v", err, sent)
	}
	table := app.ExplanationFieldClasses()
	for name := range sent.Rows[0] {
		if _, ok := table[name]; !ok {
			t.Errorf("the row sent holds %q, which is not in the field table", name)
		}
	}
	if _, ok := sent.Rows[0]["assessedAmount"]; !ok {
		t.Error("the amount was not sent, so this test does not test the interesting case")
	}
	for _, withheld := range []string{"legalEntityId", "recordedBy", "recordedAt", "content", "definition", "supersedes"} {
		if _, ok := sent.Rows[0][withheld]; ok {
			t.Errorf("%q was sent", withheld)
		}
	}
	if raw := string(call.Input); strings.Contains(raw, rig.row.RecordedBy.String()) || strings.Contains(raw, rig.row.LegalEntity.String()) {
		t.Error("an id that identifies a person or the legal entity was sent")
	}
	// The classes declared cover the highest class of the fields sent.
	if got := g.HighestDataClass(); got != privacy.P5 {
		t.Fatalf("the highest class declared is %s, want P5 for the amount", got)
	}
}

func TestTheDeclaredClassesCoverEveryClassInTheFieldTable(t *testing.T) {
	declared := map[privacy.Class]bool{}
	for _, c := range app.ExplanationDataClasses() {
		declared[c] = true
	}
	for name, class := range app.ExplanationFieldClasses() {
		if !declared[class] {
			t.Errorf("field %s is %s but the call does not declare it", name, class)
		}
	}
	if !declared[privacy.P5] {
		t.Fatal("the amount is P5 and the call does not declare P5")
	}
	if len(declared) != 2 {
		t.Fatalf("declared = %v, want exactly the classes in the table (P0 and P5)", declared)
	}
}

// The classes in the code's field table are the contract's x-ztax-privacy tags
// for the Obligation schema, so the table cannot drift from the contract.
func TestTheFieldTableMatchesTheContractsPrivacyTags(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "contracts", "openapi", "ztax.v1.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for name, want := range app.ExplanationFieldClasses() {
		got, ok := contractClass(text, "Obligation", name)
		if !ok {
			t.Errorf("Obligation.%s has no class in the contract", name)
			continue
		}
		if got != want {
			t.Errorf("Obligation.%s is %s in the contract and %s in the field table", name, got, want)
		}
	}
	// And the two fields the model is never sent really are personal in the contract.
	if c, _ := contractClass(text, "Obligation", "recordedBy"); c != privacy.P1 {
		t.Errorf("Obligation.recordedBy is %s, want P1", c)
	}
}

var (
	classRE = regexp.MustCompile(`x-ztax-privacy:\s*\{\s*class:\s*(P[0-7])`)
	refRE   = regexp.MustCompile(`\$ref:\s*"#/components/schemas/([A-Za-z0-9]+)"`)
)

// block returns the lines of every component entry named name: from
// "    Name:" to the next entry at the same indent. A name can appear under both
// parameters and schemas, so all of them are returned.
func block(text, name string) []string {
	lines := strings.Split(text, "\n")
	var out []string
	in := false
	for _, l := range lines {
		if l == "    "+name+":" {
			in = true
			continue
		}
		if in && len(l) > 4 && l[:4] == "    " && l[4] != ' ' {
			in = false
		}
		if in {
			out = append(out, l)
		}
	}
	return out
}

// contractClass reads the class of schema.property: an inline tag, or the tag
// on the schema it refers to.
func contractClass(text, schema, property string) (privacy.Class, bool) {
	lines := block(text, schema)
	for i, l := range lines {
		if !strings.HasPrefix(l, "        "+property+":") || strings.HasPrefix(l, "         ") {
			continue
		}
		if m := refRE.FindStringSubmatch(l); m != nil {
			return schemaClass(text, m[1])
		}
		if m := classRE.FindStringSubmatch(l); m != nil {
			return privacy.Class(m[1]), true
		}
		if i+1 < len(lines) {
			if m := classRE.FindStringSubmatch(lines[i+1]); m != nil {
				return privacy.Class(m[1]), true
			}
		}
	}
	return "", false
}

func schemaClass(text, name string) (privacy.Class, bool) {
	for _, l := range block(text, name) {
		if m := classRE.FindStringSubmatch(l); m != nil {
			return privacy.Class(m[1]), true
		}
	}
	return "", false
}

func TestAnotherTenantsObligationIsNotFound(t *testing.T) {
	rig := newExplainRig(t)
	foreign := explainRow(t, explainOtherTenant)
	reader := &explainReader{rows: map[id.ObligationID]obligation.Obligation{foreign.ID: foreign}}
	svc := app.NewObligationExplanationService(clientFor(t, rig), reader)

	for name, oid := range map[string]id.ObligationID{
		"another tenant's": foreign.ID,
		"unknown":          id.NewObligationID(uuid.Must(uuid.NewV7())),
	} {
		_, err := svc.Explain(as(security.RoleAnalyst), oid)
		if errs.CategoryOf(err) != errs.CategoryNotFound {
			t.Errorf("%s: %v, want not found", name, err)
		}
	}
	if n := len(rig.transport.Calls()); n != 0 {
		t.Fatalf("a row the caller cannot read reached the gateway %d time(s)", n)
	}
}

// clientFor gives a rig's transport a gateway client of its own, for a service
// that has a different reader.
func clientFor(t *testing.T, r *explainRig) *gateway.Client {
	t.Helper()
	return explainClient(t, r.transport, explainPolicy(t), explainCell, nil)
}

func TestAReaderThatDoesNotScopeByTenantStillGivesNotFound(t *testing.T) {
	rig := newExplainRig(t)
	foreign := explainRow(t, explainOtherTenant)
	svc := app.NewObligationExplanationService(clientFor(t, rig), ignoringReader{o: foreign})
	_, err := svc.Explain(as(security.RoleAnalyst), foreign.ID)
	if errs.CategoryOf(err) != errs.CategoryNotFound {
		t.Fatalf("err = %v, want not found", err)
	}
	if n := len(rig.transport.Calls()); n != 0 {
		t.Fatalf("another tenant's row reached the gateway %d time(s)", n)
	}
}

func TestOnlyOperatorsAndAnalystsMayExplain(t *testing.T) {
	for role, ok := range map[security.Role]bool{
		security.RoleOperator: true, security.RoleAnalyst: true, security.RoleAuditor: false, security.RoleAdmin: false,
	} {
		rig := newExplainRig(t)
		_, err := explainAs(as(role), rig)
		if ok && err != nil {
			t.Errorf("%s refused: %v", role, err)
		}
		if !ok && (errs.ReasonOf(err) != errs.ReasonForbidden || len(rig.transport.Calls()) != 0) {
			t.Errorf("%s: %v, want forbidden before anything is read or sent", role, err)
		}
	}
	rig := newExplainRig(t)
	if _, err := explainAs(context.Background(), rig); errs.ReasonOf(err) != errs.ReasonUnauthenticated {
		t.Errorf("no session: %v, want unauthenticated", err)
	}
}
