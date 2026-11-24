//go:build integration

package postgres_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/adapter/postgres"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/content"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/crosswalk"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/identity"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/rulechange"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// The content workbench tables (migration 000037) are append-only to the
// application, row-level secured, and round-trip a crosswalk version and a
// rule change through their folds, for the owning tenant only.
func TestIntegrationContentWorkbench(t *testing.T) {
	dsn := os.Getenv("ZTAX_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ZTAX_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := postgres.Open(ctx, postgres.DefaultConfig(dsn))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	for _, table := range []string{"content_crosswalk", "content_crosswalk_version", "content_crosswalk_entry",
		"content_crosswalk_event", "content_rule_change", "content_rule_change_event"} {
		for privilege, want := range map[string]bool{"SELECT": true, "INSERT": true, "UPDATE": false, "DELETE": false} {
			var got bool
			if err := pool.QueryRow(ctx, `SELECT has_table_privilege('ztax_app', $1, $2)`, "ztax."+table, privilege).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Errorf("ztax_app %s on %s = %v, want %v", privilege, table, got, want)
			}
		}
	}

	store := postgres.NewStore(pool)
	tenants := make([]id.TenantID, 2)
	for i := range tenants {
		tenants[i] = id.NewTenantID(uuid.Must(uuid.NewV7()))
		if err := store.Tenants().Create(ctx, identity.Tenant{
			ID: tenants[i], Slug: "cw-" + strings.ReplaceAll(tenants[i].String(), "-", ""), DisplayName: "CW",
			ResidencyRegion: "local", HomeCell: "local-dev", Status: identity.TenantActive, CreatedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	ctxA, ctxB := security.Into(ctx, security.System(tenants[0])), security.Into(ctx, security.System(tenants[1]))
	now := time.Now().UTC().Truncate(time.Microsecond)
	by := id.NewUserID(uuid.Must(uuid.NewV7()))
	digest, err := canonical.Sum(canonical.String("x"))
	if err != nil {
		t.Fatal(err)
	}

	xw := store.Crosswalks()
	c := crosswalk.Crosswalk{ID: id.NewCrosswalkID(uuid.Must(uuid.NewV7())), TenantID: tenants[0], Vendor: "SURETAX",
		Namespace: "TRANS_TYPE", VendorRelease: "2026.10", Title: "SureTax", CreatedAt: now, CreatedBy: by}
	v := crosswalk.Version{Number: 1, Rights: crosswalk.Rights{Basis: crosswalk.RightsVendorLicence, Reference: "MSA-1",
		ExpiresOn: time.Date(2027, 12, 31, 0, 0, 0, 0, time.UTC)}, EffectiveFrom: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
		Entries: []crosswalk.Entry{{ExternalCode: "010101", Description: "Local", Match: crosswalk.MatchExact,
			ServiceTarget: "ontology:telecom/voice/fixed/local"}, {ExternalCode: "99", Description: "Adj", Match: crosswalk.MatchNoEquivalent}},
		Digest: digest, IntakeKey: "k-" + c.ID.String(), IntakeHash: digest, CreatedAt: now, CreatedBy: by,
		History: crosswalk.Open(by, now, true)}
	if err := xw.CreateCrosswalk(ctxA, c); err != nil {
		t.Fatal(err)
	}
	if err := xw.CreateVersion(ctxA, c.ID, v); err != nil {
		t.Fatal(err)
	}
	got, err := xw.Crosswalk(ctxA, c.ID)
	if err != nil || len(got.Versions) != 1 || got.Versions[0].Status != crosswalk.StatusInReview ||
		len(got.Versions[0].Entries) != 2 || !got.Versions[0].Rights.ExpiresOn.Equal(v.Rights.ExpiresOn) {
		t.Fatalf("read back: %+v %v", got, err)
	}
	if _, found, err := xw.CrosswalkByIntakeKey(ctxA, v.IntakeKey); err != nil || !found {
		t.Fatalf("by intake key: %v %v", found, err)
	}
	if _, err := xw.Crosswalk(ctxB, c.ID); errs.CategoryOf(err) != errs.CategoryNotFound {
		t.Fatalf("another tenant read it: %v", err)
	}
	dup := c
	dup.ID = id.NewCrosswalkID(uuid.Must(uuid.NewV7()))
	if err := xw.CreateCrosswalk(ctxA, dup); errs.CategoryOf(err) != errs.CategoryConflict {
		t.Fatalf("second crosswalk for one vendor release: %v", err)
	}

	rc := store.RuleChanges()
	p := rulechange.Proposal{ID: id.NewRuleChangeID(uuid.Must(uuid.NewV7())), TenantID: tenants[0], Pack: "US-NY-TELECOM",
		BaseVersion: content.Version{Major: 3, Minor: 2}, Rule: "ZTAX-RULE-NY-MCTD", Jurisdiction: "US-NY",
		ChangeType: rulechange.ChangeRate, Summary: "s", Detail: "d",
		Citation:      rulechange.Citation{Source: "NY-186-C", Reference: "§1", Edition: "2026", Authority: content.AuthorityBindingPrimary},
		EffectiveFrom: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), OriginRef: "feed:1", IntakeKey: "k-" + uuid.NewString(),
		IntakeHash: digest, CreatedAt: now, CreatedBy: by}
	if err := rc.CreateRuleChange(ctxA, p, rulechange.Open(p, &rulechange.TestEvidence{Ref: "GS-1"})); err != nil {
		t.Fatal(err)
	}
	back, err := rc.RuleChange(ctxA, p.ID)
	if err != nil || back.Revision() != 2 || back.TestEvidence == nil || back.OriginRef != "feed:1" || back.BaseVersion != p.BaseVersion {
		t.Fatalf("rule change read back: %+v %v", back, err)
	}
	// The schema refuses a release-eligible status reached by anything but
	// an approval.
	bad := rulechange.Event{Seq: 3, Kind: rulechange.EventRecommended, Status: rulechange.StatusApprovedForRelease,
		Reason: "x", RecordedAt: now, RecordedBy: by}
	if err := rc.AppendRuleChangeEvent(ctxA, p.ID, bad); err == nil {
		t.Fatal("an APPROVED_FOR_RELEASE status without an approval was stored")
	}
}
