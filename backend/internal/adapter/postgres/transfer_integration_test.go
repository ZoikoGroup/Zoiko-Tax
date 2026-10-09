//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zoikogroup/zoikotax/backend/internal/adapter/postgres"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/identity"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/privacy"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// Migration 000008 against a real PostgreSQL (ADR-0018 §2.4 tier 3): the home
// cell is stamped and read back, a tenant cannot be created without one, and
// the cross-cell transfer log is insert-only and tenant-scoped.

type residencyCell struct {
	ctx    context.Context
	pool   *pgxpool.Pool
	store  *postgres.Store
	tenant id.TenantID
}

func openResidency(t *testing.T) *residencyCell {
	t.Helper()
	dsn := os.Getenv("ZTAX_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ZTAX_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	pool, err := postgres.Open(ctx, postgres.DefaultConfig(dsn))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(pool.Close)
	store := postgres.NewStore(pool)
	c := &residencyCell{ctx: ctx, pool: pool, store: store}
	c.tenant = c.newHomedTenant(t, "eu-west-1")
	return c
}

func (c *residencyCell) newHomedTenant(t *testing.T, cell string) id.TenantID {
	t.Helper()
	tenant := id.NewTenantID(uuid.Must(uuid.NewV7()))
	if err := c.store.Tenants().Create(c.ctx, identity.Tenant{
		ID: tenant, Slug: "res-" + strings.ReplaceAll(tenant.String(), "-", ""),
		DisplayName: "Residency integration", ResidencyRegion: "eu-west", HomeCell: cell,
		Status: identity.TenantActive, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("tenant: %v", err)
	}
	return tenant
}

func (c *residencyCell) scoped(tenant id.TenantID) context.Context {
	return security.Into(c.ctx, security.System(tenant))
}

func TestIntegrationHomeCellIsStampedAndRequired(t *testing.T) {
	c := openResidency(t)
	got, err := c.store.Tenants().ByID(c.scoped(c.tenant), c.tenant)
	if err != nil {
		t.Fatal(err)
	}
	if got.HomeCell != "eu-west-1" {
		t.Fatalf("home cell %q", got.HomeCell)
	}

	// No home, and a home that is not a cell name, are both refused by the
	// database whatever the application does.
	for _, cell := range []string{"", "EU WEST"} {
		tenant := id.NewTenantID(uuid.Must(uuid.NewV7()))
		err := c.store.Tenants().Create(c.ctx, identity.Tenant{
			ID: tenant, Slug: "res-" + strings.ReplaceAll(tenant.String(), "-", ""),
			DisplayName: "x", ResidencyRegion: "eu-west", HomeCell: cell,
			Status: identity.TenantActive, CreatedAt: time.Now().UTC(),
		})
		if !errs.IsCategory(err, errs.CategoryValidation) {
			t.Errorf("home cell %q: want a validation refusal, got %v", cell, err)
		}
	}
}

func integrationTransfer(t *testing.T, tenant id.TenantID) privacy.CrossCellTransfer {
	t.Helper()
	profile := privacy.TransferProfile{
		ID: "TP-IT", Version: 1,
		Exporter:      privacy.Party{Name: "Zoiko EU", Role: privacy.RoleProcessor},
		Importer:      privacy.Party{Name: "Zoiko EU", Role: privacy.RoleProcessor},
		Origin:        privacy.Location{Region: "eu-west", Cell: "eu-west-1"},
		Destination:   privacy.Location{Region: "eu-central", Cell: "eu-central-1"},
		Mechanism:     privacy.MechanismNotRestricted,
		Purposes:      []privacy.Purpose{privacy.PurposeEvid},
		DataClasses:   []privacy.Class{privacy.P0, privacy.P5},
		EffectiveFrom: time.Now().Add(-time.Hour), ReviewDue: time.Now().Add(24 * time.Hour),
	}
	tr, err := privacy.NewCrossCellTransfer(privacy.TransferRequest{
		ID:              id.NewTransferID(uuid.Must(uuid.NewV7())),
		TenantID:        tenant,
		SourceCell:      "eu-west-1",
		DestinationCell: "eu-central-1",
		Purpose:         privacy.PurposeEvid,
		DataClasses:     []privacy.Class{privacy.P5, privacy.P0},
		ContentDigest:   canonical.SumBytes([]byte(`{"records":[]}`)),
		RequestedBy:     id.NewUserID(uuid.Must(uuid.NewV7())),
		ApprovedBy:      id.NewUserID(uuid.Must(uuid.NewV7())),
		At:              time.Now().UTC().Truncate(time.Microsecond),
	}, profile)
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

func TestIntegrationTransferLogRoundTripsAndIsTenantScoped(t *testing.T) {
	c := openResidency(t)
	log := c.store.Transfers()
	tr := integrationTransfer(t, c.tenant)
	if err := log.Append(c.scoped(c.tenant), tr); err != nil {
		t.Fatal(err)
	}
	got, err := log.ByID(c.scoped(c.tenant), tr.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ProfileID != tr.ProfileID || !got.ContentDigest.Equal(tr.ContentDigest) ||
		len(got.DataClasses) != 2 || got.ApprovedBy != tr.ApprovedBy || !got.RecordedAt.Equal(tr.RecordedAt) {
		t.Fatalf("round trip: got %+v, want %+v", got, tr)
	}

	other := c.newHomedTenant(t, "eu-west-1")
	if _, err := log.ByID(c.scoped(other), tr.ID); !errs.IsCategory(err, errs.CategoryNotFound) {
		t.Fatalf("another tenant read the transfer: %v", err)
	}
	if err := log.Append(c.scoped(other), tr); !errs.IsCategory(err, errs.CategoryPolicy) {
		t.Fatalf("another tenant appended the transfer: %v", err)
	}
	list, err := log.List(c.scoped(c.tenant), 10)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v, %d rows", err, len(list))
	}
}

func TestIntegrationTransferLogRefusesWhatTheDomainRefuses(t *testing.T) {
	c := openResidency(t)
	tr := integrationTransfer(t, c.tenant)
	tr.ApprovedBy = tr.RequestedBy
	err := c.store.Transfers().Append(c.scoped(c.tenant), tr)
	if !errors.Is(err, privacy.ErrInvalidTransfer) {
		t.Fatalf("self-approved transfer: want ErrInvalidTransfer, got %v", err)
	}

	// And the table itself, for a writer that is not the adapter: P7 is not a
	// transferable class.
	_, err = ownerPool(t).Exec(c.ctx, `
		INSERT INTO ztax.cross_cell_transfer (tenant_id, transfer_id, source_cell, destination_cell,
			profile_id, profile_version, mechanism, purpose, data_classes, content_digest,
			requested_by, approved_by, recorded_at)
		VALUES ($1, $2, 'eu-west-1', 'eu-central-1', 'TP', 1, 'NOT_RESTRICTED', 'PURP-EVID',
			ARRAY['P7'], 'zt1:x', $3, $4, now())`,
		c.tenant.UUID(), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()))
	if err == nil {
		t.Fatal("the table accepted a P7 transfer")
	}
}

func TestIntegrationResidencyGrants(t *testing.T) {
	c := openResidency(t)
	for _, tc := range []struct {
		table, column, privilege string
		want                     bool
	}{
		{"cross_cell_transfer", "", "INSERT", true},
		{"cross_cell_transfer", "", "SELECT", true},
		{"cross_cell_transfer", "", "UPDATE", false},
		{"cross_cell_transfer", "", "DELETE", false},
		{"cross_cell_transfer", "", "TRUNCATE", false},
		// Rehoming a tenant is a migration, never an application update.
		{"tenant", "home_cell", "UPDATE", false},
		{"tenant", "status", "UPDATE", true},
	} {
		var got bool
		var err error
		if tc.column == "" {
			err = c.pool.QueryRow(c.ctx, `SELECT has_table_privilege('ztax_app', $1, $2)`,
				"ztax."+tc.table, tc.privilege).Scan(&got)
		} else {
			err = c.pool.QueryRow(c.ctx, `SELECT has_column_privilege('ztax_app', $1, $2, $3)`,
				"ztax."+tc.table, tc.column, tc.privilege).Scan(&got)
		}
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("ztax_app %s on %s %s = %v, want %v", tc.privilege, tc.table, tc.column, got, tc.want)
		}
	}
}
