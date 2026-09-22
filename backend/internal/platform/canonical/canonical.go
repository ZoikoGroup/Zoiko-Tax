// Package canonical is the only code in the estate that serializes for
// digesting (ADR-0011 §2.7).
//
// encoding/json is not used for evidence, ever. Go's struct-field ordering,
// omitempty semantics and float formatting are all wrong for this purpose, and
// all wrong in ways that look right — an omitempty field and an absent field
// produce the same bytes, which is exactly the collapse ADR-0011 P3 exists to
// prevent.
//
// So a canonical document is built explicitly as a Value tree rather than
// reflected out of a struct. That is more typing at the call site, and it buys
// the property that matters: what gets digested is what somebody wrote down,
// not what a tag happened to say.
//
// The profile is canon/v1 (ADR-0011 §2.1), RFC 8785 JCS plus five rules:
//
//	P1  Fiscal quantities are strings, never numbers.
//	P2  Timestamps are RFC 3339 UTC, exactly six fractional digits, literal Z.
//	P3  Absent means absent. null is never emitted and is an error on input.
//	P4  Arrays are order-significant and explicitly ordered.
//	P5  Unknown fields are rejected, not ignored.
//
// P4 and P5 are properties of the schema that builds a Value, not of this
// serializer: an array is ordered because the caller ordered it, and a field is
// unknown relative to a schema this package does not have. What this package
// enforces is P1, P2 and P3, and it does so structurally — there is no way to
// express a fiscal number or a null in the Value type at all.
package canonical

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
)

// ProfileVersion is recorded in every digest-bearing record (ADR-0011 §2.6).
// canon/v1 is never redefined: a change to any profile rule is canon/v2, and v1
// stays implemented for as long as any record references it, which under
// statutory retention is a long time.
const ProfileVersion = "canon/v1"

// kind discriminates a Value. It is unexported so the set stays closed.
type kind uint8

const (
	kindAbsent kind = iota // P3: dropped from any object that contains it
	kindString
	kindDecimal
	kindInteger
	kindBool
	kindObject
	kindArray
	kindRaw
)

// Value is one node of a canonical document.
//
// There is no null constructor and no float constructor. P3 and P1 are
// therefore not rules somebody has to remember — they are shapes the type
// system does not provide.
type Value struct {
	k   kind
	s   string
	i   int64
	b   bool
	obj []Field
	arr []Value
}

// Field is one member of an object.
type Field struct {
	Name  string
	Value Value
}

// Absent is a value that is not present. An object drops it (P3). It exists so
// that a builder can write a field unconditionally and let the presence of the
// data decide, rather than branching at every call site and getting one wrong.
func Absent() Value { return Value{k: kindAbsent} }

// IsAbsent reports whether v will be dropped from an enclosing object.
func (v Value) IsAbsent() bool { return v.k == kindAbsent }

// String is a text value.
func String(s string) Value { return Value{k: kindString, s: s} }

// OptString is String, or Absent when the string is empty. Empty and absent are
// different facts in general, so this is only for fields where the schema says
// they coincide; where they do not, the caller writes the branch.
func OptString(s string) Value {
	if s == "" {
		return Absent()
	}
	return String(s)
}

// Money is a fiscal amount. It serializes as a JSON string in the §2.2 normal
// form, never as a number (P1).
//
// P1 is structural here rather than a rule to remember: this package has no
// constructor that takes a float or an apd.Decimal, so there is no way to put a
// fiscal number in a canonical document. The normal form itself comes from
// internal/domain/fiscal, which is the only package permitted to hold apd
// (.golangci.yml, rule one-decimal-library).
func Money(m fiscal.Money) Value { return Value{k: kindDecimal, s: m.CanonicalString()} }

// Rate is a proportion. It serializes as a string, for the same reason Money
// does.
func Rate(r fiscal.Rate) Value { return Value{k: kindDecimal, s: r.CanonicalString()} }

// Quantity is an amount of something. It serializes as a string, for the same
// reason Money does.
func Quantity(q fiscal.Quantity) Value { return Value{k: kindDecimal, s: q.CanonicalString()} }

// Raw carries bytes that are already in canonical form.
//
// It exists for exactly two callers, and neither of them should re-encode:
//
//   - the outbox relay, republishing a payload that was canonical when the
//     transaction that produced it committed;
//   - the golden-vector loader, holding reviewed bytes on disk.
//
// In both cases re-encoding would mean the delivered or digested bytes attest
// to this build's serializer rather than to what was written and reviewed. A
// serializer change would then silently alter historical payloads, which is the
// opposite of what canon/v1 never being redefined is supposed to guarantee.
//
// It is deliberately not validated here. These bytes came out of Encode, and a
// validator would be a second implementation of the format whose disagreement
// with the first would be the actual bug.
func Raw(canonicalBytes []byte) Value {
	return Value{k: kindRaw, s: string(canonicalBytes)}
}

// Integer is a non-fiscal count — a sequence number, a line ordinal, a page
// size. It serializes as a JSON number, which is safe because it is not a
// fiscal quantity and carries no scale.
//
// A fiscal amount never reaches this constructor: it is a Money, and Money
// canonicalizes through Decimal.
func Integer(i int64) Value { return Value{k: kindInteger, i: i} }

// Bool is a boolean.
func Bool(b bool) Value { return Value{k: kindBool, b: b} }

// Time is an instant. It serializes as RFC 3339 UTC with exactly six fractional
// digits and a literal Z (P2) — no offsets, no variable precision, because two
// encodings of one instant must not produce two digests.
func Time(t time.Time) Value {
	return Value{k: kindString, s: FormatTime(t)}
}

// FormatTime renders the P2 timestamp form. It is exported because the
// transport layer emits the same form in responses, and two implementations of
// one format is one too many.
func FormatTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000000Z")
}

// Object builds an object. Fields whose value is Absent are dropped (P3).
// Duplicate names are a programming error and are reported by Encode rather
// than silently resolved, because either resolution would be a guess.
func Object(fields ...Field) Value {
	kept := make([]Field, 0, len(fields))
	for _, f := range fields {
		if f.Value.IsAbsent() {
			continue
		}
		kept = append(kept, f)
	}
	return Value{k: kindObject, obj: kept}
}

// F is shorthand for a field, so that a document reads as a document.
func F(name string, v Value) Field { return Field{Name: name, Value: v} }

// Array builds an array. Order is significant and is the caller's (P4): this
// function does not sort, because the ordering key is a property of the schema
// and the caller is the only one that knows it.
func Array(items ...Value) Value {
	// An absent element is a programming error rather than something to drop:
	// dropping it would shift every subsequent ordinal, and ordinals are part
	// of the canonical input that allocation ties break on (ADR-0002 §2.6).
	return Value{k: kindArray, arr: items}
}

// Encode renders v in canonical form.
func Encode(v Value) ([]byte, error) {
	var b strings.Builder
	if err := encode(&b, v); err != nil {
		return nil, err
	}
	return []byte(b.String()), nil
}

// ErrAbsentRoot is returned when the whole document is absent, which is never a
// document — it is a caller that built nothing and did not notice.
var ErrAbsentRoot = errors.New("canonical: document is absent")

func encode(b *strings.Builder, v Value) error {
	switch v.k {
	case kindAbsent:
		return ErrAbsentRoot

	case kindString:
		writeString(b, v.s)
		return nil

	case kindDecimal:
		// P1: a fiscal quantity is a string. The normal form preserves trailing
		// zeros, because 1.50 and 1.5 are different assertions about precision
		// and must digest differently (ADR-0011 §2.2).
		writeString(b, v.s)
		return nil

	case kindRaw:
		if v.s == "" {
			return fmt.Errorf("canonical: raw value is empty")
		}
		b.WriteString(v.s)
		return nil

	case kindInteger:
		fmt.Fprintf(b, "%d", v.i)
		return nil

	case kindBool:
		if v.b {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
		return nil

	case kindObject:
		return encodeObject(b, v.obj)

	case kindArray:
		b.WriteByte('[')
		for i, item := range v.arr {
			if item.IsAbsent() {
				return fmt.Errorf("canonical: array element %d is absent; P4 makes order significant, so an element cannot be dropped", i)
			}
			if i > 0 {
				b.WriteByte(',')
			}
			if err := encode(b, item); err != nil {
				return err
			}
		}
		b.WriteByte(']')
		return nil
	}
	return fmt.Errorf("canonical: unknown value kind %d", v.k)
}

func encodeObject(b *strings.Builder, fields []Field) error {
	// JCS orders members by their names' UTF-16 code units. Sorting a copy
	// leaves the caller's slice alone, which matters because a Value is passed
	// by value but shares its backing array.
	ordered := make([]Field, len(fields))
	copy(ordered, fields)
	sort.SliceStable(ordered, func(i, j int) bool {
		return lessUTF16(ordered[i].Name, ordered[j].Name)
	})
	for i := 1; i < len(ordered); i++ {
		if ordered[i].Name == ordered[i-1].Name {
			return fmt.Errorf("canonical: duplicate field %q", ordered[i].Name)
		}
	}

	b.WriteByte('{')
	for i, f := range ordered {
		if i > 0 {
			b.WriteByte(',')
		}
		writeString(b, f.Name)
		b.WriteByte(':')
		if err := encode(b, f.Value); err != nil {
			return fmt.Errorf("field %q: %w", f.Name, err)
		}
	}
	b.WriteByte('}')
	return nil
}

// lessUTF16 compares two strings by UTF-16 code unit, which is what RFC 8785
// specifies and what a JavaScript implementation will do.
//
// For ASCII this is byte order, so it looks like an expensive way to write
// s < t. It is not: above the BMP, UTF-16 surrogate pairs sort differently from
// UTF-8 bytes, so a key containing an emoji would order one way here and
// another way in a client that follows the specification. That divergence would
// produce a different digest for the same document, which is the one failure
// this whole package exists to prevent.
func lessUTF16(a, b string) bool {
	if isASCII(a) && isASCII(b) {
		return a < b
	}
	ua, ub := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	for i := 0; i < len(ua) && i < len(ub); i++ {
		if ua[i] != ub[i] {
			return ua[i] < ub[i]
		}
	}
	return len(ua) < len(ub)
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// writeString emits a JSON string using RFC 8785 §3.2.2.2 escaping: the
// two-character forms where they exist, \u00XX for the remaining control
// characters, and nothing else. Notably / is not escaped and non-ASCII is
// emitted as UTF-8 rather than as \u sequences.
func writeString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(b, `\u%04x`, r)
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
}
