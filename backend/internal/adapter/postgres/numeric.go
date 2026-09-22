// Package postgres is the cell's persistence adapter.
//
// ADR-0008 §2.2 puts it on pgx v5's native interface with no database/sql,
// because database/sql's Scan semantics are precisely where the float64
// narrowing risk lives: a driver that does not know what to do with a NUMERIC
// will happily hand back a float64, and nothing in the type system objects.
//
// Notice what this file does not import. The one-decimal-library rule confines
// apd to internal/domain/fiscal, so the codec converts through
// fiscal.NumericParts — an unscaled integer coefficient and a base-10 exponent.
// PostgreSQL NUMERIC and apd.Decimal are both exactly that, so the mapping is
// lossless by construction rather than by care, and there is no arithmetic in
// it for a float to hide in.
package postgres

import (
	"fmt"
	"math/big"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
)

// Numeric is the Go representation of a PostgreSQL NUMERIC column.
//
// It implements pgtype.NumericValuer and pgtype.NumericScanner, which is what
// registers it as the estate's NUMERIC mapping (ADR-0008 §2.4, discharging
// ADR-0001 control 2). Every fiscal column binds to this type; none binds to
// float64, and the conformance test in numeric_test.go asserts that no driver
// path narrows one.
type Numeric struct {
	Parts fiscal.NumericParts
	// Valid is false for SQL NULL. It is not a zero amount: a column with no
	// value and a column holding 0.00 are different facts, and collapsing them
	// is how a missing tax line becomes a zero-rated one.
	Valid bool
}

// NumericValue renders the value for the wire.
func (n Numeric) NumericValue() (pgtype.Numeric, error) {
	if !n.Valid {
		return pgtype.Numeric{}, nil
	}
	if n.Parts.Coeff == nil {
		return pgtype.Numeric{}, fmt.Errorf("postgres: numeric has no coefficient")
	}
	// pgtype.Numeric carries the sign inside Int; fiscal.NumericParts keeps it
	// beside a non-negative coefficient, matching apd. Reattach it here.
	i := new(big.Int).Set(n.Parts.Coeff)
	if n.Parts.Negative {
		i.Neg(i)
	}
	return pgtype.Numeric{Int: i, Exp: n.Parts.Exp, Valid: true}, nil
}

// ScanNumeric reads a value from the wire.
func (n *Numeric) ScanNumeric(v pgtype.Numeric) error {
	if !v.Valid {
		*n = Numeric{}
		return nil
	}
	// PostgreSQL accepts 'NaN'::numeric and, since 14, infinities. None of them
	// is a fiscal amount, and a NaN that compares false against everything is
	// far worse in a tax ledger than a refused read. Fail rather than carry it.
	if v.NaN {
		return fmt.Errorf("postgres: %w: NaN in a numeric column", fiscal.ErrNotFinite)
	}
	if v.InfinityModifier != pgtype.Finite {
		return fmt.Errorf("postgres: %w: %v in a numeric column", fiscal.ErrNotFinite, v.InfinityModifier)
	}
	if v.Int == nil {
		return fmt.Errorf("postgres: numeric is valid but carries no integer")
	}

	coeff := new(big.Int).Abs(v.Int)
	*n = Numeric{
		Parts: fiscal.NumericParts{
			Coeff:    coeff,
			Exp:      v.Exp,
			Negative: v.Int.Sign() < 0,
		},
		Valid: true,
	}
	return nil
}

// MoneyValue wraps a Money for binding to a NUMERIC parameter.
func MoneyValue(m fiscal.Money) (Numeric, error) {
	p, err := m.NumericParts()
	if err != nil {
		return Numeric{}, err
	}
	return Numeric{Parts: p, Valid: true}, nil
}

// RateValue wraps a Rate for binding to a NUMERIC parameter.
func RateValue(r fiscal.Rate) (Numeric, error) {
	p, err := r.NumericParts()
	if err != nil {
		return Numeric{}, err
	}
	return Numeric{Parts: p, Valid: true}, nil
}

// QuantityValue wraps a Quantity for binding to a NUMERIC parameter.
func QuantityValue(q fiscal.Quantity) (Numeric, error) {
	p, err := q.NumericParts()
	if err != nil {
		return Numeric{}, err
	}
	return Numeric{Parts: p, Valid: true}, nil
}

// Money rebuilds a Money from a scanned column and its currency column.
//
// The currency is a separate argument because it is a separate column: a Money
// is decimal plus ISO 4217 (ADR-0001 C1), and a schema that stored only the
// number would be storing half a value object.
func (n Numeric) Money(currency fiscal.Currency) (fiscal.Money, error) {
	if !n.Valid {
		return fiscal.Money{}, fmt.Errorf("postgres: money column is NULL")
	}
	return fiscal.MoneyFromNumeric(n.Parts, currency)
}

// Rate rebuilds a Rate from a scanned column and its basis column.
func (n Numeric) Rate(basis fiscal.RateBasis) (fiscal.Rate, error) {
	if !n.Valid {
		return fiscal.Rate{}, fmt.Errorf("postgres: rate column is NULL")
	}
	return fiscal.RateFromNumeric(n.Parts, basis)
}

// Quantity rebuilds a Quantity from a scanned column and its unit column.
func (n Numeric) Quantity(unit fiscal.Unit) (fiscal.Quantity, error) {
	if !n.Valid {
		return fiscal.Quantity{}, fmt.Errorf("postgres: quantity column is NULL")
	}
	return fiscal.QuantityFromNumeric(n.Parts, unit)
}
