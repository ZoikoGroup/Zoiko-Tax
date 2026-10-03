package security_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
)

func TestCheckResidencyFailsClosed(t *testing.T) {
	cases := []struct {
		name, home, here string
		want             error
	}{
		{"resident", "eu-west-1", "eu-west-1", nil},
		{"homed elsewhere", "us-east-1", "eu-west-1", security.ErrNotResident},
		{"tenant has no home", "", "eu-west-1", security.ErrNoHomeCell},
		{"process has no cell", "eu-west-1", "", security.ErrCellUnknown},
		// Two unknowns are not agreement.
		{"both unknown", "", "", security.ErrCellUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := security.CheckResidency(c.home, c.here)
			if c.want == nil {
				if err != nil {
					t.Fatalf("want resident, got %v", err)
				}
				return
			}
			if !errors.Is(err, c.want) {
				t.Fatalf("want %v, got %v", c.want, err)
			}
		})
	}
}

func TestValidateCellID(t *testing.T) {
	for _, ok := range []string{"local-dev", "eu-west-1", "us1"} {
		if err := security.ValidateCellID(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "a", "-eu", "EU-west", "eu_west", "eu west", string(make([]byte, 64))} {
		if err := security.ValidateCellID(bad); !errors.Is(err, security.ErrInvalidCellID) {
			t.Errorf("%q: want ErrInvalidCellID, got %v", bad, err)
		}
	}
}

// The home cell travels on a copy; the context it was derived from is
// unchanged, so nothing holding the original can have its residency rewritten.
func TestWithHomeCellIsACopy(t *testing.T) {
	tenant := id.NewTenantID(uuid.MustParse("01920000-0000-7000-8000-000000000001"))
	user := id.NewUserID(uuid.MustParse("01920000-0000-7000-8000-000000000002"))
	session := id.NewSessionID(uuid.MustParse("01920000-0000-7000-8000-000000000003"))
	base := security.New(tenant, user, session, []security.Role{security.RoleAdmin}, time.Unix(0, 0))
	homed := base.WithHomeCell("eu-west-1")
	if base.HomeCell() != "" {
		t.Fatalf("original context gained a home cell: %q", base.HomeCell())
	}
	if homed.HomeCell() != "eu-west-1" || homed.Tenant() != tenant || !homed.HasRole(security.RoleAdmin) {
		t.Fatalf("copy lost something: %+v", homed)
	}
	if (security.Context{}).HomeCell() != "" {
		t.Fatal("the zero context names a home cell")
	}
}
