//go:build integration

package app_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/adapter/postgres"
	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/identity"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/regchange"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/registration"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
)

// Doc 03 WF-05 -> WF-09 -> Sidebar WF-02 against PostgreSQL: a change is
// recorded once per source edition, triaged, assessed against the content
// and the tenant's own registration as stored, recommended by one person
// and accepted by another, every record read back and folded from the
// store and the assessment verified against its digest.
func TestIntegrationRegulatoryChangeIsAssessedRecommendedAndHandedOff(t *testing.T) {
	dsn := os.Getenv("ZTAX_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ZTAX_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := postgres.Open(ctx, postgres.DefaultConfig(dsn))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store := postgres.NewStore(pool)
	tenant := id.NewTenantID(uuid.Must(uuid.NewV7()))
	if err := store.Tenants().Create(ctx, identity.Tenant{
		ID: tenant, Slug: "regchange-" + strings.ReplaceAll(tenant.String(), "-", ""), DisplayName: "Regulatory change integration",
		ResidencyRegion: "local", HomeCell: "local-dev", Status: identity.TenantActive, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	sys := security.Into(ctx, security.System(tenant))
	if err := store.LegalEntities().Create(sys, identity.LegalEntity{
		ID: id.NewLegalEntityID(uuid.Must(uuid.NewV7())), TenantID: tenant, Name: "Regulatory change Ltd", Default: true, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	as := func(roles ...security.Role) context.Context {
		return security.Into(ctx, security.New(tenant, id.NewUserID(uuid.Must(uuid.NewV7())), id.NewSessionID(uuid.Must(uuid.NewV7())), roles, time.Now()))
	}
	operator, analyst, recommender, reviewer := as(security.RoleOperator), as(security.RoleAnalyst), as(security.RoleAnalyst), as(security.RoleAnalyst)
	clk := &settableClock{t: time.Now().UTC()}

	regs := app.NewRegistrationService(store.Registrations(), store.LegalEntities(), store, clk, idgen.V7{})
	if _, _, err := regs.CreateRegistration(operator, app.RegistrationInput{IntakeKey: "reg-1", Scheme: registration.SchemeUSStateTelecom,
		Jurisdiction: "US-NY", Authority: "NY PSC", Number: "NY-TEL-0042", EffectiveFrom: day("2026-01-01"), Submit: true}); err != nil {
		t.Fatal(err)
	}

	snap := &regchange.ContentSnapshot{BundleID: "global-telecom", BundleDigest: "zt1:" + strings.Repeat("c", 64),
		Components: []regchange.ContentComponent{{ID: "us-ny-excise", Regime: "us", Jurisdiction: "jurisdiction:us-ny",
			Authority: "authority:us-ny-dtf", TaxType: "TELECOM_EXCISE", Name: "NY excise", From: "2020-01-01", Citation: "NY Tax Law 186-e"}}}
	svc := app.NewRegulatoryChangeService(store.RegulatoryChanges(), fixedCoverage{snap: snap}, store.Registrations(), store.Obligations(),
		store, clk, idgen.V7{})

	in := nyChange("chg-1")
	in.PublishedOn = clk.t.AddDate(0, 0, -5)
	in.EffectiveFrom = clk.t.AddDate(0, 2, 0)
	c, created, err := svc.RecordChange(operator, in)
	if err != nil || !created {
		t.Fatalf("record: %v %v", created, err)
	}
	dup := in
	dup.IntakeKey = "chg-2"
	if again, created, err := svc.RecordChange(operator, dup); err != nil || created || again.ID != c.ID {
		t.Fatalf("dedupe: %v %v", created, err)
	}
	if _, err := svc.TransitionChange(analyst, c.ID, regchange.Act{To: regchange.StatusTriaged, Priority: regchange.PriorityUrgent, Reason: "Covered levy", Version: 1}); err != nil {
		t.Fatal(err)
	}
	a, created, err := svc.AssessChange(analyst, c.ID)
	if err != nil || !created || len(a.Items) != 2 {
		t.Fatalf("assess: %v %v %+v", created, err, a.Items)
	}
	stored, err := svc.Assessment(analyst, a.ID)
	if err != nil || !stored.Current || !stored.AssessmentDigest.Equal(a.AssessmentDigest) {
		t.Fatalf("read back: %v", err)
	}
	if again, created, err := svc.AssessChange(analyst, c.ID); err != nil || created || again.ID != a.ID {
		t.Fatalf("reassess: %v %v", created, err)
	}

	r, _, err := svc.DraftRecommendation(analyst, app.RecommendationInput{IntakeKey: "rec-1", Assessment: a.ID, Origin: regchange.OriginAIAssisted,
		AI: &regchange.AIProvenance{UseCase: "regulatory-change-recommendation", ModelProfile: "m", ProviderProfile: "p", PromptProfile: "rcr-v1",
			AiTrainVersion: "2026.11", AuthorityOutcome: regchange.AuthorityPropose, Draft: "Update the rate."},
		Content: regchange.Content{Action: regchange.ActionUpdateRule, Rationale: "Rate rises",
			Targets: []regchange.Target{{Kind: regchange.ItemContentComponent, Ref: "us-ny-excise"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RecommendRecommendation(recommender, r.ID, "Checked", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ReviewRecommendation(recommender, r.ID, regchange.DecisionAccept, "ok", 2); errs.ReasonOf(err) != regchange.ReasonMakerChecker {
		t.Fatalf("recommender reviews: %v", err)
	}
	done, err := svc.ReviewRecommendation(reviewer, r.ID, regchange.DecisionAccept, "Agreed", 2)
	if err != nil {
		t.Fatal(err)
	}
	if ref, ok := done.HandoffRef(); !ok || done.AI == nil || done.AI.Draft != "Update the rate." || !strings.HasPrefix(ref, "ztx-rcr:") {
		t.Fatalf("%+v", done.Recommendation)
	}
	closed, err := svc.TransitionChange(analyst, c.ID, regchange.Act{To: regchange.StatusClosed, Disposition: regchange.DispositionChangeNeeded, Reason: "Handed off", Version: 3})
	if err != nil || closed.Status != regchange.StatusClosed || closed.Accepted != 1 {
		t.Fatalf("close: %v", err)
	}
}
