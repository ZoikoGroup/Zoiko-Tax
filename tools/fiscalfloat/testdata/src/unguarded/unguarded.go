// Package unguarded exercises R2, R3 and R4 — the routes that reach around R1
// from packages that are not themselves guarded, such as transport, adapters
// and telemetry. Declaring a float here is legal; touching a fiscal value with
// one is not.
package unguarded

import "fiscalfixture"

// Legal: this package may hold binary floats of its own.
var latencySeconds float64

func average(xs []float64) float64 {
	var sum float64
	for _, x := range xs {
		sum += x
	}
	return sum
}

// R2 — converting a fiscal value to a float.
func toFloat(m fiscalfixture.Money) float64 {
	return float64(m) // want `conversion of fiscal value`
}

func rateToFloat(r fiscalfixture.Rate) float64 {
	return float64(r) // want `conversion of fiscal value`
}

// R3 — a method on a fiscal type that hands back a float.
func viaMethod(m fiscalfixture.Money) float64 {
	return m.Float64() // want `returns binary floating-point from a fiscal type`
}

// R4 — a struct holding both. This is the DTO and log-payload shape: the
// conversion between the two fields has not been written yet, and it will be.
type payload struct {
	Amount fiscalfixture.Money
	Approx float64 // want `struct holds both a fiscal value and binary floating-point`
}

// Safe: fiscal values leave as strings.
type response struct {
	Amount   string
	Currency string
}

func render(m fiscalfixture.Money) response {
	return response{Amount: m.Text(), Currency: "USD"}
}

// Safe: a float-only struct in an unguarded package is fine.
type timing struct {
	Seconds float64
}
