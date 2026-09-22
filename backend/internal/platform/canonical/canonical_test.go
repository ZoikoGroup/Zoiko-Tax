package canonical_test

import (
	"strings"
	"testing"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

func money(t *testing.T, amount, currency string) fiscal.Money {
	t.Helper()
	m, err := fiscal.ParseMoney(amount, fiscal.Currency(currency))
	if err != nil {
		t.Fatalf("ParseMoney(%q): %v", amount, err)
	}
	return m
}

func encode(t *testing.T, v canonical.Value) string {
	t.Helper()
	b, err := canonical.Encode(v)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	return string(b)
}

// JCS orders object members by name, whatever order the caller wrote them in.
// This is the property that lets two services building the same document from
// different structs agree on a digest.
func TestObjectMembersAreOrderedByName(t *testing.T) {
	v := canonical.Object(
		canonical.F("zeta", canonical.String("z")),
		canonical.F("alpha", canonical.String("a")),
		canonical.F("Mike", canonical.String("m")),
	)
	// Uppercase sorts before lowercase in UTF-16 code-unit order.
	want := `{"Mike":"m","alpha":"a","zeta":"z"}`
	if got := encode(t, v); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

// P3 — absent means absent. A field with no value is omitted, and the document
// digests as though it was never mentioned.
func TestAbsentFieldsAreDropped(t *testing.T) {
	with := canonical.Object(
		canonical.F("present", canonical.String("x")),
		canonical.F("missing", canonical.Absent()),
	)
	without := canonical.Object(
		canonical.F("present", canonical.String("x")),
	)
	if encode(t, with) != encode(t, without) {
		t.Errorf("an absent field changed the encoding: %s vs %s", encode(t, with), encode(t, without))
	}
	if strings.Contains(encode(t, with), "null") {
		t.Error("null was emitted; P3 forbids it")
	}
}

// P1 and ADR-0011 §2.2 — scale is semantic. 1.50 and 1.5 are different
// assertions about precision and must not digest identically.
func TestTrailingZerosAreSignificant(t *testing.T) {
	a, err := canonical.Sum(canonical.Money(money(t, "1.50", "USD")))
	if err != nil {
		t.Fatalf("Sum: %v", err)
	}
	b, err := canonical.Sum(canonical.Money(money(t, "1.5", "USD")))
	if err != nil {
		t.Fatalf("Sum: %v", err)
	}
	if a.Equal(b) {
		t.Errorf("1.50 and 1.5 digested identically as %s", a)
	}
}

// P1 — a fiscal amount is a string in the output, never a JSON number.
func TestFiscalAmountsSerializeAsStrings(t *testing.T) {
	got := encode(t, canonical.Object(canonical.F("amount", canonical.Money(money(t, "12.50", "USD")))))
	want := `{"amount":"12.50"}`
	if got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

// A credit that rounds to nothing and a debit that round to nothing are the
// same amount. apd renders one of them "-0"; the profile forbids it.
func TestNegativeZeroIsNormalised(t *testing.T) {
	neg := money(t, "-0.00", "USD")
	pos := money(t, "0.00", "USD")
	if got := neg.CanonicalString(); got != "0.00" {
		t.Errorf("negative zero rendered as %q, want %q", got, "0.00")
	}
	if neg.CanonicalString() != pos.CanonicalString() {
		t.Errorf("signed zeros disagree: %q vs %q", neg.CanonicalString(), pos.CanonicalString())
	}
}

// P2 — exactly six fractional digits, literal Z, whatever the input offset or
// precision was.
func TestTimestampsCarryExactlySixFractionalDigits(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   time.Time
		want string
	}{
		{"whole second", time.Date(2026, 9, 22, 11, 2, 31, 0, time.UTC), "2026-09-22T11:02:31.000000Z"},
		{"nanoseconds truncated", time.Date(2026, 9, 22, 11, 2, 31, 442110999, time.UTC), "2026-09-22T11:02:31.442110Z"},
		{"offset converted", time.Date(2026, 9, 22, 13, 2, 31, 0, time.FixedZone("CEST", 2*60*60)), "2026-09-22T11:02:31.000000Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := canonical.FormatTime(tc.in); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// P4 — an array's order is the caller's, and dropping an element would shift
// every ordinal after it. Ordinals are what allocation ties break on
// (ADR-0002 §2.6), so this fails loudly rather than silently renumbering.
func TestAbsentArrayElementIsAnError(t *testing.T) {
	_, err := canonical.Encode(canonical.Array(canonical.String("a"), canonical.Absent()))
	if err == nil {
		t.Fatal("an absent array element was accepted")
	}
	if !strings.Contains(err.Error(), "absent") {
		t.Errorf("unhelpful error: %v", err)
	}
}

func TestArraysPreserveCallerOrder(t *testing.T) {
	v := canonical.Array(canonical.String("c"), canonical.String("a"), canonical.String("b"))
	if got, want := encode(t, v), `["c","a","b"]`; got != want {
		t.Errorf("got %s, want %s — arrays are not sorted", got, want)
	}
}

func TestDuplicateFieldsAreRejected(t *testing.T) {
	_, err := canonical.Encode(canonical.Object(
		canonical.F("k", canonical.String("1")),
		canonical.F("k", canonical.String("2")),
	))
	if err == nil {
		t.Fatal("a duplicate field name was accepted")
	}
}

// RFC 8785 §3.2.2.2 — the two-character escapes where they exist, \u00XX for
// remaining control characters, and nothing else. Notably / is not escaped and
// non-ASCII travels as UTF-8.
func TestStringEscaping(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`a"b`, `"a\"b"`},
		{`a\b`, `"a\\b"`},
		{"a\nb", `"a\nb"`},
		{"a\tb", `"a\tb"`},
		{"a\x00b", `"a\u0000b"`},
		{"a\x1fb", `"a\u001fb"`},
		{"a/b", `"a/b"`},
		{"café", `"café"`},
	} {
		if got := encode(t, canonical.String(tc.in)); got != tc.want {
			t.Errorf("%q: got %s, want %s", tc.in, got, tc.want)
		}
	}
}

func TestDigestCarriesItsProfilePrefix(t *testing.T) {
	d, err := canonical.Sum(canonical.Object(canonical.F("a", canonical.String("b"))))
	if err != nil {
		t.Fatalf("Sum: %v", err)
	}
	if !strings.HasPrefix(d.String(), canonical.DigestPrefix) {
		t.Errorf("digest %q lacks the %s prefix", d, canonical.DigestPrefix)
	}
	round, err := canonical.ParseDigest(d.String())
	if err != nil {
		t.Fatalf("ParseDigest(%q): %v", d, err)
	}
	if !round.Equal(d) {
		t.Errorf("digest did not round-trip: %s vs %s", round, d)
	}
}

func TestParseDigestRejections(t *testing.T) {
	for _, tc := range []struct{ name, in string }{
		{"no prefix", "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"},
		{"wrong prefix", "sha256:9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"},
		{"too short", "zt1:9f86d0"},
		{"uppercase", "zt1:9F86D081884C7D659A2FEAA0C55AD015A3BF4F1B2B0B822CD15D6C15B0F00A08"},
		{"not hex", "zt1:zzzzd081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := canonical.ParseDigest(tc.in); err == nil {
				t.Errorf("accepted %q", tc.in)
			}
		})
	}
}

// RFC 6962 domain separation: a one-leaf tree is not the leaf's own digest,
// because the leaf is hashed under the 0x00 prefix first. Without that, an
// interior node can be presented as a leaf and a seal becomes forgeable.
func TestMerkleLeavesAreDomainSeparated(t *testing.T) {
	leaf := canonical.SumBytes([]byte("a"))
	root, err := canonical.MerkleRoot([]canonical.Digest{leaf})
	if err != nil {
		t.Fatalf("MerkleRoot: %v", err)
	}
	if root.Equal(leaf) {
		t.Error("a one-leaf root equals its leaf; the 0x00 prefix is missing")
	}
}

func TestMerkleRootIsOrderSensitive(t *testing.T) {
	a, b := canonical.SumBytes([]byte("a")), canonical.SumBytes([]byte("b"))
	ab, err := canonical.MerkleRoot([]canonical.Digest{a, b})
	if err != nil {
		t.Fatalf("MerkleRoot: %v", err)
	}
	ba, err := canonical.MerkleRoot([]canonical.Digest{b, a})
	if err != nil {
		t.Fatalf("MerkleRoot: %v", err)
	}
	if ab.Equal(ba) {
		t.Error("the Merkle root did not change when leaf order did")
	}
}

// An odd node is promoted unchanged rather than duplicated. Duplicating it is
// the Bitcoin variant and admits two distinct trees with one root.
func TestMerkleOddNodeIsPromotedNotDuplicated(t *testing.T) {
	a := canonical.SumBytes([]byte("a"))
	three := []canonical.Digest{a, canonical.SumBytes([]byte("b")), canonical.SumBytes([]byte("c"))}
	four := append(append([]canonical.Digest{}, three...), canonical.SumBytes([]byte("c")))

	r3, err := canonical.MerkleRoot(three)
	if err != nil {
		t.Fatalf("MerkleRoot: %v", err)
	}
	r4, err := canonical.MerkleRoot(four)
	if err != nil {
		t.Fatalf("MerkleRoot: %v", err)
	}
	if r3.Equal(r4) {
		t.Error("[a b c] and [a b c c] produced one root; the odd node was duplicated")
	}
}

func TestMerkleEmptyTreeIsTheEmptyDigest(t *testing.T) {
	root, err := canonical.MerkleRoot(nil)
	if err != nil {
		t.Fatalf("MerkleRoot: %v", err)
	}
	if !root.Equal(canonical.SumBytes(nil)) {
		t.Errorf("empty root is %s, want the digest of no bytes", root)
	}
}

// The whole point of the package: the same document built in a different field
// order digests identically, and a changed value does not.
func TestDigestIsStableAcrossFieldOrder(t *testing.T) {
	build := func(reversed bool) canonical.Value {
		amount := canonical.F("amount", canonical.Money(money(t, "10.00", "EUR")))
		currency := canonical.F("currency", canonical.String("EUR"))
		if reversed {
			return canonical.Object(currency, amount)
		}
		return canonical.Object(amount, currency)
	}
	a, err := canonical.Sum(build(false))
	if err != nil {
		t.Fatalf("Sum: %v", err)
	}
	b, err := canonical.Sum(build(true))
	if err != nil {
		t.Fatalf("Sum: %v", err)
	}
	if !a.Equal(b) {
		t.Errorf("field order changed the digest: %s vs %s", a, b)
	}
}
