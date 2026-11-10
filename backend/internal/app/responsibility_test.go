package app_test

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/identity"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/obligation"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// Levy responsibility (ZTAX-ENG-007; G-018), at the use case, against the
// shipped training pack: every levy a committed document assesses gets a
// five-role decision resolved against the document's own parties.

var sellerEntity = id.NewLegalEntityID(uuid.MustParse("01920000-0000-7000-8000-00000000e001"))

type memEntities struct{}

func (memEntities) Create(context.Context, identity.LegalEntity) error { return nil }
func (memEntities) Default(context.Context) (identity.LegalEntity, error) {
	return identity.LegalEntity{ID: sellerEntity, TenantID: tenantID, Name: "Telco A", Default: true}, nil
}
func (memEntities) ByID(context.Context, id.LegalEntityID) (identity.LegalEntity, error) {
	return memEntities{}.Default(context.Background())
}

type memResponsibility struct {
	mu   sync.Mutex
	rows []obligation.LevyResponsibility
}

func (m *memResponsibility) Append(_ context.Context, r obligation.LevyResponsibility) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows = append(m.rows, r)
	return nil
}

func (m *memResponsibility) ForDecision(_ context.Context, d id.DecisionID) ([]obligation.LevyResponsibility, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []obligation.LevyResponsibility
	for _, r := range m.rows {
		if r.TaxDecision == d {
			out = append(out, r)
		}
	}
	return out, nil
}

var _ port.ResponsibilityRepository = (*memResponsibility)(nil)

func responsibilityHarness(t *testing.T) (*harness, *memResponsibility) {
	t.Helper()
	h := locationHarness(t)
	repo := &memResponsibility{}
	// Only the legal entity is wired: the training bundle has no fiscal
	// profile to post, and responsibility needs nothing else.
	h.svc.WithFiscal(app.FiscalStores{LegalEntities: memEntities{}}).WithResponsibility(repo)
	return h, repo
}

func commitSectionSeven(t *testing.T, h *harness) id.DecisionID {
	t.Helper()
	s, err := h.svc.Commit(as(security.RoleOperator), documentCommit("resp-1", sectionSeven(t, "40.00")))
	if err != nil || s.Status != 201 || s.ResultRef == nil {
		t.Fatalf("commit: %v %d %s", err, s.Status, s.Body)
	}
	return *s.ResultRef
}

func TestEveryCommittedLevyHasAFiveRoleDecision(t *testing.T) {
	h, repo := responsibilityHarness(t)
	d := commitSectionSeven(t, h)
	levies, err := h.svc.Responsibility(as(security.RoleAuditor), d)
	if err != nil {
		t.Fatal(err)
	}
	if len(levies) == 0 || len(levies) != len(repo.rows) {
		t.Fatalf("%d levies read, %d recorded", len(levies), len(repo.rows))
	}
	rec, err := h.svc.Decision(as(security.RoleAuditor), d)
	if err != nil {
		t.Fatal(err)
	}
	want := 0
	for _, item := range rec.Result.Document.Items {
		want += len(item.Components)
	}
	if len(levies) != want {
		t.Fatalf("%d levies decided for %d assessed components", len(levies), want)
	}
	for _, l := range levies {
		if len(l.Decision.Assignments) != len(obligation.Roles) {
			t.Fatalf("%s/%s has %d roles", l.Item, l.Component, len(l.Decision.Assignments))
		}
		if l.Decision.LegalEntity != sellerEntity || l.Decision.Provenance.BundleDigest == "" {
			t.Fatalf("%s is not scoped to the seller entity and pinned to its content: %+v", l.Component, l.Decision)
		}
	}
}

func TestRolesNameTheDocumentsOwnParties(t *testing.T) {
	h, _ := responsibilityHarness(t)
	levies, err := h.svc.Responsibility(as(security.RoleOperator), commitSectionSeven(t, h))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, l := range levies {
		seen[l.Treatment] = true
		if l.Decision.Status != obligation.ResponsibilityResolved {
			t.Fatalf("%s is %s: both parties are on the document", l.Component, l.Decision.Status)
		}
		for _, a := range l.Decision.Assignments {
			switch {
			case a.Status == obligation.ResponsibilityNotApplicable:
				if a.Role != obligation.RoleCollection || l.Treatment == "CUSTOMER_CHARGE" {
					t.Fatalf("%s: %s has no holder, but a customer charge is always collected", l.Component, a.Role)
				}
			case a.Statutory == nil:
				t.Fatalf("%s: %s resolved to nobody", l.Component, a.Role)
			case a.Statutory.Kind == obligation.PartySeller && (a.Statutory.Ref != "TELCO-A" || a.Statutory.LegalEntity != sellerEntity):
				t.Fatalf("%s: seller holds %s as %+v", l.Component, a.Role, a.Statutory)
			case a.Statutory.Kind == obligation.PartyBuyer && (a.Statutory.Ref != "C1" || !a.Statutory.LegalEntity.IsZero()):
				t.Fatalf("%s: customer holds %s as %+v", l.Component, a.Role, a.Statutory)
			}
		}
		if l.Treatment == "PROVIDER_COST" {
			// Borne by the provider: the customer pays nothing and nobody
			// collects anything from them.
			bearer, _ := l.Decision.Assignment(obligation.RoleEconomicBearer)
			collect, _ := l.Decision.Assignment(obligation.RoleCollection)
			if bearer.Statutory == nil || bearer.Statutory.Kind != obligation.PartySeller ||
				collect.Status != obligation.ResponsibilityNotApplicable {
				t.Fatalf("provider-borne %s: bearer %+v, collection %s", l.Component, bearer.Statutory, collect.Status)
			}
		}
	}
	if !seen["CUSTOMER_CHARGE"] || !seen["PROVIDER_COST"] {
		t.Fatalf("the worked example should carry both customer and provider levies; saw %v", seen)
	}
}

func TestAQuoteDecidesNoResponsibility(t *testing.T) {
	h, repo := responsibilityHarness(t)
	if _, err := h.svc.Quote(as(security.RoleOperator), app.QuoteInput{EventTime: taxPoint, Document: sectionSeven(t, "40.00")}); err != nil {
		t.Fatal(err)
	}
	if len(repo.rows) != 0 {
		t.Fatalf("a quote recorded %d responsibility decisions", len(repo.rows))
	}
}

func TestARetriedCommitDecidesResponsibilityOnce(t *testing.T) {
	h, repo := responsibilityHarness(t)
	commitSectionSeven(t, h)
	n := len(repo.rows)
	commitSectionSeven(t, h) // same idempotency key and body: replayed, not re-executed
	if len(repo.rows) != n {
		t.Fatalf("a retried commit recorded responsibility again: %d then %d", n, len(repo.rows))
	}
}

func TestResponsibilityIsReadByTheDecisionsReaders(t *testing.T) {
	h, _ := responsibilityHarness(t)
	d := commitSectionSeven(t, h)
	if _, err := h.svc.Responsibility(as(security.RoleAdmin), d); errs.ReasonOf(err) != errs.ReasonForbidden {
		t.Fatalf("an admin read responsibility: %v", err)
	}
	unknown := id.NewDecisionID(uuid.MustParse("01920000-0000-7000-8000-0000000000ff"))
	if _, err := h.svc.Responsibility(as(security.RoleAuditor), unknown); !errs.IsCategory(err, errs.CategoryNotFound) {
		t.Fatalf("an unknown decision: %v", err)
	}
}
