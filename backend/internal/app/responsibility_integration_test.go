//go:build integration

package app_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/obligation"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/rule"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
)

// Levy responsibility against PostgreSQL (migration 000027): recorded in the
// commit's transaction, read back exactly, and invisible to another tenant.

func TestIntegrationLevyResponsibilityIsRecordedWithTheCommit(t *testing.T) {
	c := openFiscalCell(t)
	holder, library := &rule.Holder{}, &rule.Library{}
	b := trainingBundle(t)
	holder.Publish(b)
	library.Add(b)
	svc := app.NewDeterminationService(holder, library, c.store.Decisions(), c.files, c.store, c.clock, idgen.V7{}, trains).
		WithIdempotency(app.NewIdempotency(c.store.Idempotency(), c.store, c.clock)).
		WithFiscal(app.FiscalStores{
			Accumulators: c.store.Accumulators(), Journals: c.store.Journals(),
			LegalEntities: c.store.LegalEntities(), Outbox: c.store.Outbox(), Obligations: c.store.Obligations(),
		}).
		WithResponsibility(c.store.Responsibilities())

	in := documentCommit("resp-int-"+uuid.NewString(), sectionSeven(t, "40.00"))
	in.Determination.BusinessKey = "INV-RESP-" + uuid.NewString()
	s, err := svc.Commit(c.ctx, in)
	if err != nil || s.Status != 201 || s.ResultRef == nil {
		t.Fatalf("commit: %v %d %s", err, s.Status, s.Body)
	}
	levies, err := svc.Responsibility(c.ctx, *s.ResultRef)
	if err != nil {
		t.Fatal(err)
	}
	if len(levies) == 0 {
		t.Fatal("the commit recorded no levy responsibility")
	}
	le, err := c.store.LegalEntities().Default(c.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range levies {
		d := l.Decision
		if l.TaxDecision != *s.ResultRef || d.TenantID != c.tenant || d.LegalEntity != le.ID ||
			d.Status != obligation.ResponsibilityResolved || len(d.Assignments) != len(obligation.Roles) {
			t.Fatalf("%s/%s read back as %+v", l.Item, l.Component, l)
		}
		if !d.EffectiveFrom.Equal(taxPoint) || d.Provenance.BundleDigest != b.Digest() || l.RecordedAt.IsZero() {
			t.Fatalf("%s is not dated to the tax point and pinned to its bundle: %+v", l.Component, d)
		}
		for i, a := range d.Assignments {
			if a.Role != obligation.Roles[i] {
				t.Fatalf("%s: role %d is %s, want %s", l.Component, i, a.Role, obligation.Roles[i])
			}
			if a.Statutory != nil && a.Statutory.Kind == obligation.PartySeller && a.Statutory.LegalEntity != le.ID {
				t.Fatalf("%s: the seller's legal entity did not survive the round trip: %+v", l.Component, a.Statutory)
			}
		}
	}

	// Another tenant cannot see the decision, let alone its levies.
	stranger := security.Into(c.ctx, security.New(id.NewTenantID(uuid.Must(uuid.NewV7())),
		id.NewUserID(uuid.Must(uuid.NewV7())), id.NewSessionID(uuid.Must(uuid.NewV7())),
		[]security.Role{security.RoleAuditor}, time.Now()))
	if _, err := svc.Responsibility(stranger, *s.ResultRef); !errs.IsCategory(err, errs.CategoryNotFound) {
		t.Fatalf("another tenant read the decision's responsibility: %v", err)
	}
	if rows, err := c.store.Responsibilities().ForDecision(stranger, *s.ResultRef); err != nil || len(rows) != 0 {
		t.Fatalf("row-level security let another tenant read %d rows (%v)", len(rows), err)
	}
}
