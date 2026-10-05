package content

import (
	"strconv"
	"strings"
)

// Version is a pack's semantic version, MAJOR.MINOR.PATCH (CONT-001 §11:
// "stable package ID + semantic version").
//
// Pre-release and build-metadata suffixes are refused. A pack version names a
// released, signed set of law; "2.1.0-rc1" is a version of something that was
// not released, and SemVer's pre-release precedence rules would let a
// constraint resolve to it in ways an author reading "^2.1" does not expect.
// The CONTENT train's own version (the seal's contentVersion, which can be
// 0.0.0-dev) is a different identifier and is not constrained by this type.
type Version struct {
	Major, Minor, Patch int
}

// ParseVersion reads MAJOR.MINOR.PATCH with no leading zeros.
func ParseVersion(s string) (Version, error) {
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return Version{}, errorf("version %q is not MAJOR.MINOR.PATCH", s)
	}
	var n [3]int
	for i, p := range parts {
		v, err := component(p)
		if err != nil {
			return Version{}, errorf("version %q: %v", s, err)
		}
		n[i] = v
	}
	return Version{Major: n[0], Minor: n[1], Patch: n[2]}, nil
}

func component(p string) (int, error) {
	if p == "" {
		return 0, errorf("empty component")
	}
	if len(p) > 1 && p[0] == '0' {
		// SemVer forbids it, and allowing it would give 1.02.0 and 1.2.0 two
		// spellings and two canonical encodings.
		return 0, errorf("component %q has a leading zero", p)
	}
	for _, c := range p {
		if c < '0' || c > '9' {
			return 0, errorf("component %q is not a whole number", p)
		}
	}
	v, err := strconv.Atoi(p)
	if err != nil {
		return 0, errorf("component %q is out of range", p)
	}
	return v, nil
}

// String renders the version.
func (v Version) String() string {
	return strconv.Itoa(v.Major) + "." + strconv.Itoa(v.Minor) + "." + strconv.Itoa(v.Patch)
}

// Compare returns -1, 0 or +1.
func (v Version) Compare(o Version) int {
	for _, d := range [3]int{v.Major - o.Major, v.Minor - o.Minor, v.Patch - o.Patch} {
		if d < 0 {
			return -1
		}
		if d > 0 {
			return 1
		}
	}
	return 0
}

// IsZero reports whether v is the zero value, which is never a parsed version
// a pack may carry (0.0.0 is refused by PackManifest.Validate).
func (v Version) IsZero() bool { return v == Version{} }

// MarshalText renders the version for the manifest.
func (v Version) MarshalText() ([]byte, error) { return []byte(v.String()), nil }

// UnmarshalText parses the version from the manifest.
func (v *Version) UnmarshalText(b []byte) error {
	p, err := ParseVersion(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// Constraint is a version range a dependency accepts (CONT-001 §11:
// "exact compatible package/version ranges").
//
// The grammar is deliberately small — one or more comparators separated by
// single spaces, all of which must hold:
//
//	1.2.3   =1.2.3        exactly that version
//	>1.2.3  >=1.2  <2  <=1.4.0
//	^1.2.3  ^1.2  ^1      same major (same minor below 1.0.0, same patch below 0.1.0)
//	~1.2.3  ~1.2          same major and minor
//
// No "||", no wildcards, no hyphen ranges. A pack dependency is a statement
// about which law this pack was certified against, and every additional
// operator is another way for two readers to disagree about what was certified.
// The text is kept as written, so a manifest records what its author wrote and
// re-encodes to the same bytes.
type Constraint struct {
	text  string
	terms []bound
}

// bound is one comparator in its lowered form: a version and an operator.
type bound struct {
	op string // "=", ">", ">=", "<", "<="
	v  Version
}

// ParseConstraint reads a constraint.
func ParseConstraint(s string) (Constraint, error) {
	if s == "" || strings.TrimSpace(s) != s || strings.Contains(s, "  ") {
		return Constraint{}, errorf("constraint %q must be comparators separated by single spaces", s)
	}
	c := Constraint{text: s}
	for _, f := range strings.Split(s, " ") {
		bs, err := lower(f)
		if err != nil {
			return Constraint{}, errorf("constraint %q: %v", s, err)
		}
		c.terms = append(c.terms, bs...)
	}
	return c, nil
}

// MustConstraint is ParseConstraint for literals in tests and fixtures.
func MustConstraint(s string) Constraint {
	c, err := ParseConstraint(s)
	if err != nil {
		panic(err)
	}
	return c
}

func lower(f string) ([]bound, error) {
	switch {
	case strings.HasPrefix(f, "^"):
		v, n, err := partial(f[1:])
		if err != nil {
			return nil, err
		}
		upper := caretUpper(v, n)
		return []bound{{">=", v}, {"<", upper}}, nil
	case strings.HasPrefix(f, "~"):
		v, n, err := partial(f[1:])
		if err != nil {
			return nil, err
		}
		upper := Version{Major: v.Major + 1}
		if n >= 2 {
			upper = Version{Major: v.Major, Minor: v.Minor + 1}
		}
		return []bound{{">=", v}, {"<", upper}}, nil
	}
	for _, op := range []string{">=", "<=", ">", "<", "="} {
		if rest, ok := strings.CutPrefix(f, op); ok {
			if op == "=" {
				v, err := ParseVersion(rest)
				if err != nil {
					return nil, err
				}
				return []bound{{"=", v}}, nil
			}
			v, _, err := partial(rest)
			if err != nil {
				return nil, err
			}
			return []bound{{op, v}}, nil
		}
	}
	// A bare version is an exact pin, and it must be complete: "2" as an exact
	// pin would read as "2.x" to half the people who see it.
	v, err := ParseVersion(f)
	if err != nil {
		return nil, err
	}
	return []bound{{"=", v}}, nil
}

// partial reads 1, 1.2 or 1.2.3 and reports how many components were given.
func partial(s string) (Version, int, error) {
	parts := strings.Split(s, ".")
	if len(parts) == 0 || len(parts) > 3 {
		return Version{}, 0, errorf("%q is not a version", s)
	}
	var n [3]int
	for i, p := range parts {
		v, err := component(p)
		if err != nil {
			return Version{}, 0, err
		}
		n[i] = v
	}
	return Version{Major: n[0], Minor: n[1], Patch: n[2]}, len(parts), nil
}

// caretUpper is the exclusive upper bound of ^v: the next version that changes
// the left-most non-zero component that was written.
func caretUpper(v Version, given int) Version {
	switch {
	case v.Major > 0 || given == 1:
		return Version{Major: v.Major + 1}
	case v.Minor > 0 || given == 2:
		return Version{Minor: v.Minor + 1}
	default:
		return Version{Patch: v.Patch + 1}
	}
}

// Allows reports whether v satisfies every comparator.
func (c Constraint) Allows(v Version) bool {
	if len(c.terms) == 0 {
		// The zero Constraint allows nothing. It is what a dependency declared
		// without a constraint decodes to, and "any version" is not something
		// CONT-001 lets a pack say.
		return false
	}
	for _, b := range c.terms {
		cmp := v.Compare(b.v)
		ok := false
		switch b.op {
		case "=":
			ok = cmp == 0
		case ">":
			ok = cmp > 0
		case ">=":
			ok = cmp >= 0
		case "<":
			ok = cmp < 0
		case "<=":
			ok = cmp <= 0
		}
		if !ok {
			return false
		}
	}
	return true
}

// String returns the constraint as written.
func (c Constraint) String() string { return c.text }

// IsZero reports whether c was never parsed.
func (c Constraint) IsZero() bool { return c.text == "" }

// MarshalText renders the constraint for the manifest.
func (c Constraint) MarshalText() ([]byte, error) { return []byte(c.text), nil }

// UnmarshalText parses the constraint from the manifest.
func (c *Constraint) UnmarshalText(b []byte) error {
	p, err := ParseConstraint(string(b))
	if err != nil {
		return err
	}
	*c = p
	return nil
}
