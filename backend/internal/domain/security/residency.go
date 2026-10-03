package security

import (
	"errors"
	"fmt"
)

// Residency: a tenant is homed in exactly one cell.
//
// ADR-0009 §2.6 makes the cell the unit of residency, and SEC-001 §11 makes
// residency metadata authoritative, change-controlled configuration: "moving an
// entity between cells is a migration, not a UI toggle." The tenant row carries
// its home cell (migration 000008), the process knows which cell it is
// (ZTAX_CELL, ADR-0017 §2.10), and every authenticated request is checked for
// the two agreeing before any handler runs (SEC-REQ-0037).
//
// The mismatch this catches is not a request routed to the wrong cell by an
// honest edge — that request finds no tenant here at all and fails to
// authenticate. It is a tenant row present in a database it does not belong
// to: a backup restored into the wrong cell, a failover that moved data to a
// region it may not be in (SEC-REQ-0043), a database shared between two cells
// by a deployment mistake. In each of those the data is already somewhere it
// should not be, and the one thing the process can still do is refuse to
// serve from it.

// The residency refusals.
var (
	// ErrCellUnknown is a process that cannot name its own cell. config.Load
	// refuses to start without ZTAX_CELL, so reaching this is a wiring defect,
	// and it fails closed like every other.
	ErrCellUnknown = errors.New("security: this process does not know its cell")
	// ErrNoHomeCell is a tenant with no recorded home cell — a row from before
	// home cells were recorded, not yet assigned by a reviewed migration.
	ErrNoHomeCell = errors.New("security: tenant has no home cell")
	// ErrNotResident is a tenant homed in a different cell.
	ErrNotResident = errors.New("security: tenant is not resident in this cell")
	// ErrInvalidCellID is a cell identifier outside the grammar.
	ErrInvalidCellID = errors.New("security: invalid cell id")
)

// ValidateCellID applies the cell identifier grammar: lowercase letters,
// digits and hyphens, 2 to 63 characters, not starting with a hyphen — the
// shape of a DNS label, because a cell name ends up in hostnames, bucket names
// and trust-domain paths. The database CHECK on tenant.home_cell is the same
// rule.
func ValidateCellID(cell string) error {
	if len(cell) < 2 || len(cell) > 63 {
		return fmt.Errorf("%w: %q is %d characters, want 2 to 63", ErrInvalidCellID, cell, len(cell))
	}
	for i := 0; i < len(cell); i++ {
		c := cell[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '-' && i != 0:
		default:
			return fmt.Errorf("%w: %q may hold only lowercase letters, digits and hyphens", ErrInvalidCellID, cell)
		}
	}
	return nil
}

// CheckResidency decides whether a tenant homed in home may be served by the
// process running as cell here. Every unknown is a refusal: a process that
// does not know where it is, or a tenant that does not say where it lives, is
// not evidence that the two agree.
func CheckResidency(home, here string) error {
	switch {
	case here == "":
		return ErrCellUnknown
	case home == "":
		return ErrNoHomeCell
	case home != here:
		// The cells are not in the message. This error reaches a log line,
		// and the operator reading it has the tenant id; naming the cell a
		// tenant lives in, to whoever triggered the refusal, is a disclosure
		// with no use to them.
		return ErrNotResident
	}
	return nil
}
