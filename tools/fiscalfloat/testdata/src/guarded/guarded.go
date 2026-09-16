// Package guarded exercises R1: no binary float may be declared at all in a
// guarded package, whether or not anything fiscal is nearby.
package guarded

type Ratio float64 // want `type Ratio declares binary floating-point`

var factor float64 // want `declaration factor declares binary floating-point`

// An untyped float constant is a finding too. Go keeps it at arbitrary precision
// until it is used, and then it defaults to float64 — so in the domain it is
// almost always a rate or a tolerance that should have been a decimal string.
const tolerance = 0.5 // want `constant tolerance declares binary floating-point`

const scaleDigits = 2 // integer constant — no finding

type Line struct { // want `type Line declares binary floating-point`
	Weight float64 // want `field Weight declares binary floating-point`
}

func Total() float64 { // want `signature of Total declares binary floating-point`
	return 0
}

// Everything below is what the domain should look like: decimal carried as
// strings and integers, no binary float anywhere.

type Amount string

var label string

type Document struct {
	Reference string
	Ordinal   int
}

func Describe(d Document) string { return d.Reference }
