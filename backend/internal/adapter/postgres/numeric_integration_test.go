//go:build integration

package postgres_test

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zoikogroup/zoikotax/backend/internal/adapter/postgres"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
)

// This file discharges ADR-0001 §5.1 control 2 and ADR-0008 §2.4: NUMERIC binds
// to apd.Decimal through a registered pgx type, and no driver path narrows a
// numeric to float64.
//
// It is tier 3 (ADR-0018 §2.4) and runs against a real PostgreSQL, because the
// claim is about a driver's behaviour and a mock of the driver would be a mock
// of the thing under test. Skipped without a DSN so that `go test ./...` on a
// laptop with no database still passes, and wired into `make test-integration`
// and the compose stack where one exists.

func connect(t *testing.T) *pgx.Conn {
	t.Helper()
	dsn := os.Getenv("ZTAX_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ZTAX_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	// Exactly what the pool's AfterConnect does, so the test exercises the
	// registration rather than a parallel arrangement that happens to work.
	postgres.RegisterTypes(conn)
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

// The boundary values. Each is here because it breaks a specific wrong
// implementation, and the comment says which.
var roundTripCases = []struct {
	name   string
	amount string
}{
	// Scale is semantic (ADR-0011 §2.2). A codec that normalises through a
	// canonical numeric form, or that trims, loses the distinction between
	// "1.50 exactly" and "1.5".
	{"trailing zeros survive", "1.50"},
	{"many trailing zeros survive", "1.500000"},
	{"integer keeps zero scale", "42"},

	// 34 significant digits is the decimal128 coefficient width and the
	// precision the estate's context carries (ADR-0002 §2.1). Anything that
	// round-trips through a float64 has 15-17 digits and mangles this.
	{"34 significant digits", "1234567890123456789012345678901234"},
	{"34 digits with a fractional part", "12345678901234567890123456789.01234"},

	// 0.1 is not representable in binary floating point. If any path in the
	// driver touched a float64, this comes back as 0.1000000000000000055511151231257827.
	{"one tenth", "0.1"},
	{"two tenths", "0.2"},
	{"a third of a cent", "0.0033333333333333333333333333333333"},

	// A rate below the currency minor unit, which ADR-0002 §4.2 gives as the
	// reason scaled integers were rejected.
	{"sub-minor-unit rate", "0.06375"},
	{"apportionment factor", "0.0416667"},

	// Signs and zero.
	{"negative", "-19.99"},
	{"negative with scale", "-0.01"},
	{"zero", "0"},
	{"zero with scale", "0.00"},

	// Large magnitude with a fractional part, which is where a fixed-point
	// int64 in minor units would overflow.
	{"large with cents", "99999999999999999999.99"},
}

func TestNumericRoundTripsExactly(t *testing.T) {
	conn := connect(t)
	ctx := context.Background()

	for _, tc := range roundTripCases {
		t.Run(tc.name, func(t *testing.T) {
			in, err := fiscal.ParseMoney(tc.amount, "USD")
			if err != nil {
				t.Fatalf("ParseMoney(%q): %v", tc.amount, err)
			}
			param, err := postgres.MoneyValue(in)
			if err != nil {
				t.Fatalf("MoneyValue: %v", err)
			}

			var out postgres.Numeric
			// Cast explicitly so the parameter is sent and returned as NUMERIC
			// rather than inferred as something more convenient.
			if err := conn.QueryRow(ctx, "SELECT $1::numeric", param).Scan(&out); err != nil {
				t.Fatalf("round trip: %v", err)
			}

			got, err := out.Money("USD")
			if err != nil {
				t.Fatalf("Money: %v", err)
			}
			// CanonicalString rather than a decimal comparison: this asserts
			// the value AND its scale, which is the property that matters.
			if got.CanonicalString() != in.CanonicalString() {
				t.Errorf("round trip changed the value: sent %q, got %q",
					in.CanonicalString(), got.CanonicalString())
			}
		})
	}
}

// The control as stated: no driver path narrows a numeric to float64.
//
// The test is worth reading carefully, because the obvious version of it proves
// nothing. Asserting that our own codec is exact says nothing about what would
// happen if somebody scanned into a float64 instead — so this scans the same
// column both ways and asserts they disagree. If they ever agree, either the
// value stopped being a boundary value or something normalised it, and both
// mean this test has stopped guarding anything.
func TestScanningIntoFloat64LosesPrecisionAndOurCodecDoesNot(t *testing.T) {
	conn := connect(t)
	ctx := context.Background()

	const exact = "0.1"

	var viaFloat float64
	if err := conn.QueryRow(ctx, "SELECT $1::numeric", exact).Scan(&viaFloat); err != nil {
		t.Fatalf("scan into float64: %v", err)
	}

	var viaCodec postgres.Numeric
	if err := conn.QueryRow(ctx, "SELECT $1::numeric", exact).Scan(&viaCodec); err != nil {
		t.Fatalf("scan into Numeric: %v", err)
	}
	got, err := viaCodec.Money("USD")
	if err != nil {
		t.Fatalf("Money: %v", err)
	}

	if got.CanonicalString() != exact {
		t.Errorf("the registered codec lost precision: got %q, want %q", got.CanonicalString(), exact)
	}

	// 0.1 as a float64 is 0.1000000000000000055511151231257827. Formatting it
	// at full precision is what makes the loss visible; %v would print "0.1"
	// and hide exactly the defect this test exists to find.
	if formatted := formatFull(viaFloat); formatted == exact {
		t.Errorf("float64 round-tripped %q exactly, which it cannot do; the test is no longer guarding anything", exact)
	}
}

// A NUMERIC column can legally hold NaN, and since PostgreSQL 14, infinities.
// None is a fiscal amount, and a NaN that compares false against everything is
// far worse in a tax ledger than a refused read.
func TestNonFiniteNumericsAreRefused(t *testing.T) {
	conn := connect(t)
	ctx := context.Background()

	for _, literal := range []string{"NaN", "Infinity", "-Infinity"} {
		t.Run(literal, func(t *testing.T) {
			var out postgres.Numeric
			err := conn.QueryRow(ctx, "SELECT $1::numeric", literal).Scan(&out)
			if err == nil {
				t.Fatalf("%s was accepted into a fiscal column", literal)
			}
		})
	}
}

// A 35-digit value is outside the estate's precision. It has to be refused on
// the way out of the database as well as on the way in, because a column can
// hold more than the context can operate on and the failure would otherwise
// surface several operations later in an unrelated Inexact trap.
func TestValuesAbovePrecisionAreRefusedOnRead(t *testing.T) {
	conn := connect(t)
	ctx := context.Background()

	const thirtyFive = "12345678901234567890123456789012345"

	var out postgres.Numeric
	if err := conn.QueryRow(ctx, "SELECT $1::numeric", thirtyFive).Scan(&out); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if _, err := out.Money("USD"); err == nil {
		t.Errorf("a %d-digit value was accepted at precision %d", len(thirtyFive), fiscal.Precision)
	}
}

// NULL and zero are different facts. A column with no value and a column
// holding 0.00 must not collapse, because that is how a missing tax line
// becomes a zero-rated one.
func TestNullIsNotZero(t *testing.T) {
	conn := connect(t)
	ctx := context.Background()

	var out postgres.Numeric
	if err := conn.QueryRow(ctx, "SELECT NULL::numeric").Scan(&out); err != nil {
		t.Fatalf("scan NULL: %v", err)
	}
	if out.Valid {
		t.Error("NULL scanned as a valid value")
	}
	if _, err := out.Money("USD"); err == nil {
		t.Error("a NULL numeric produced a Money")
	}
}

// The codec has to survive the pool, not just a bare connection: AfterConnect
// runs per connection, and a registration that happened once would work in
// development and narrow an amount the first time the pool grew.
func TestEveryPooledConnectionRegistersTheCodec(t *testing.T) {
	dsn := os.Getenv("ZTAX_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ZTAX_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()

	cfg := postgres.DefaultConfig(dsn)
	cfg.MaxConns = 4
	pool, err := postgres.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer pool.Close()

	// Hold several connections open at once so the pool is forced to establish
	// more than one, then use each. Acquiring them all before using any is the
	// point: using them one at a time would be served by a single connection
	// returned to the pool each time, and would prove nothing.
	const conns = 4
	held := make([]*pgxpool.Conn, 0, conns)
	for i := 0; i < conns; i++ {
		c, err := pool.Acquire(ctx)
		if err != nil {
			t.Fatalf("acquire %d: %v", i, err)
		}
		held = append(held, c)
	}
	for i, c := range held {
		var out postgres.Numeric
		if err := c.QueryRow(ctx, "SELECT $1::numeric", "0.1").Scan(&out); err != nil {
			t.Fatalf("connection %d: %v", i, err)
		}
		got, err := out.Money("USD")
		if err != nil {
			t.Fatalf("connection %d: %v", i, err)
		}
		if got.CanonicalString() != "0.1" {
			t.Errorf("connection %d returned %q, want %q", i, got.CanonicalString(), "0.1")
		}
		c.Release()
	}
}

// formatFull renders a float64 at enough precision to show what it actually
// holds. strconv's shortest form would print 0.1 for the float64 nearest 0.1,
// which is the whole illusion this test is trying to dispel.
func formatFull(f float64) string {
	return strconv.FormatFloat(f, 'f', 34, 64)
}
