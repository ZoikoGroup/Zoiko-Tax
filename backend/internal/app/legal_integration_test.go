//go:build integration

package app_test

import (
	"os"
	"testing"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/legal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
)

// LegalAuthorization against PostgreSQL, under the development matrix.

func (c *fiscalCell) legal(t *testing.T, withMatrix bool) *app.LegalService {
	t.Helper()
	var m legal.Matrix
	if withMatrix {
		b, err := os.ReadFile("../../../content/legal/authorization-matrix.dev.json")
		if err != nil {
			t.Fatal(err)
		}
		if m, err = legal.ParseMatrix(b); err != nil {
			t.Fatal(err)
		}
	}
	return app.NewLegalService(m, c.store.Authorizations(), c.store.LegalEntities(), c.store.Audit(), c.store, c.clock, idgen.V7{})
}

func mandateInput() app.GrantInput {
	return app.GrantInput{
		Country: "DE", Authority: "DE-ELSTER", Type: legal.AuthFilingMandate,
		Permissions: []legal.Permission{legal.PermSubmit, legal.PermPrepare},
		PeriodFrom:  "2026-01", PeriodTo: "2026-12",
		EffectiveFrom: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), ExpiresAt: time.Date(2027, 6, 30, 0, 0, 0, 0, time.UTC),
		Evidence: []string{"doc:mandate-2026.pdf"}, CredentialRef: "vault://tax/de/elster/cert",
	}
}

// ZTAX-LEG-REQ-0001, -0004, -0007, -0013 to -0016, -0044, -0101.
func TestIntegrationLEGREQ0001TheGateResolvesGrantsAndFailsClosed(t *testing.T) {
	c := openFiscalCell(t)
	svc := c.legal(t, true)
	admin := c.as(security.RoleAdmin)
	sys := security.Into(c.ctx, security.System(c.tenant))
	sept := legal.Action{Country: "DE", Authority: "DE-ELSTER", Service: legal.ServiceFile, Period: "2026-09"}

	// No grant yet: blocked, and says so.
	if r, _, err := svc.Gate(c.ctx, sept); err != nil || r.Allowed || r.Reason != legal.ReasonAuthMissing {
		t.Fatalf("with no grant: %v %+v", err, r)
	}

	// A grant is a person's act (ZTAX-LEG-REQ-0044), and a credential is a
	// vault reference, never the credential (ZTAX-LEG-REQ-0017, -0018).
	if _, err := svc.Grant(sys, mandateInput()); errs.ReasonOf(err) != errs.ReasonUnauthenticated {
		t.Fatalf("system work recorded a grant: %v", err)
	}
	if _, err := svc.Grant(c.ctx, mandateInput()); errs.ReasonOf(err) != errs.ReasonForbidden {
		t.Fatalf("an operator recorded a grant: %v", err)
	}
	raw := mandateInput()
	raw.CredentialRef = "-----BEGIN PRIVATE KEY-----"
	if _, err := svc.Grant(admin, raw); errs.ReasonOf(err) != errs.ReasonInvalidValue {
		t.Fatalf("a raw credential: %v", err)
	}
	first, err := svc.Grant(admin, mandateInput())
	if err != nil || first.Status != legal.AuthActive || first.LegalEntity.IsZero() {
		t.Fatalf("granting: %v %+v", err, first)
	}

	// The gate now lets September through under it, for system work too —
	// the filing job of W4 — and blocks a period it does not reach.
	r, _, err := svc.Gate(sys, sept)
	if err != nil || !r.Allowed || r.Authorization != first.ID || r.Rule.ID != "DE-ELSTER-FILE" || r.MatrixDigest.IsZero() {
		t.Fatalf("filing September: %v %+v", err, r)
	}
	jan := sept
	jan.Period = "2027-01"
	if r, _, _ := svc.Gate(c.ctx, jan); r.Allowed || r.Reason != legal.ReasonAuthOutOfScope {
		t.Fatalf("a period the grant does not reach: %+v", r)
	}
	// Filing is not representation (ZTAX-LEG-REQ-0062).
	rep := sept
	rep.Service = legal.ServiceRepresent
	if r, _, _ := svc.Gate(c.ctx, rep); r.Allowed {
		t.Fatalf("a filing mandate represented: %+v", r)
	}

	// Superseding closes the old grant in the same act (ZTAX-LEG-REQ-0015).
	next := mandateInput()
	next.PeriodTo, next.Supersedes = "2027-12", first.ID
	second, err := svc.Grant(admin, next)
	if err != nil {
		t.Fatal(err)
	}
	old, err := svc.Authorization(c.ctx, first.ID)
	if err != nil || old.Status != legal.AuthSuperseded || len(old.History) != 2 || old.History[1].By != second.ID {
		t.Fatalf("the superseded grant: %v %+v", err, old)
	}
	if _, err := svc.Grant(admin, next); errs.ReasonOf(err) != errs.ReasonStateTransitionInvalid {
		t.Fatalf("superseding a superseded grant: %v", err)
	}
	if r, _, _ := svc.Gate(c.ctx, jan); !r.Allowed || r.Authorization != second.ID {
		t.Fatalf("January under the successor: %+v", r)
	}

	// Revocation blocks at once (ZTAX-LEG-REQ-0016), and is final.
	if _, err := svc.Revoke(admin, second.ID, " "); errs.ReasonOf(err) != errs.ReasonMissingField {
		t.Fatalf("a revocation with no reason: %v", err)
	}
	if _, err := svc.Revoke(admin, second.ID, "mandate withdrawn"); err != nil {
		t.Fatal(err)
	}
	if r, _, _ := svc.Gate(c.ctx, sept); r.Allowed || r.Reason != legal.ReasonAuthRevoked {
		t.Fatalf("after revocation: %+v", r)
	}
	if _, err := svc.Revoke(admin, second.ID, "again"); errs.ReasonOf(err) != errs.ReasonStateTransitionInvalid {
		t.Fatalf("a second revocation: %v", err)
	}
	all, err := svc.Authorizations(c.ctx)
	if err != nil || len(all) != 2 || all[0].ID != second.ID {
		t.Fatalf("the list: %v %d", err, len(all))
	}
	trail, err := c.store.Audit().ListForSubject(c.ctx, "customer_authorization", second.ID.String(), 10)
	if err != nil || len(trail) != 2 {
		t.Fatalf("the grant's audit trail: %v %+v", err, trail)
	}

	// Counsel pending blocks (ZTAX-LEG-REQ-0007); with no matrix, so does
	// everything.
	gb := legal.Action{Country: "GB", Authority: "GB-HMRC", Service: legal.ServiceFile, Period: "2026-09"}
	if r, _, _ := svc.Gate(c.ctx, gb); r.Allowed || r.Reason != legal.ReasonCounselPending {
		t.Fatalf("a counsel-pending posture: %+v", r)
	}
	none := c.legal(t, false)
	compute := legal.Action{Country: "DE", Authority: "DE-ELSTER", Service: legal.ServiceCompute}
	if r, _, _ := none.Gate(c.ctx, compute); r.Allowed || r.Reason != legal.ReasonUnsupportedMatrix {
		t.Fatalf("with no matrix: %+v", r)
	}
}
