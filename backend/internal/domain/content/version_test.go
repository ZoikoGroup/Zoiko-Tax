package content_test

import (
	"testing"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/content"
)

func v(s string) content.Version {
	out, err := content.ParseVersion(s)
	if err != nil {
		panic(err)
	}
	return out
}

func TestParseVersion(t *testing.T) {
	for _, bad := range []string{"", "1", "1.2", "1.2.3.4", "01.2.3", "1.02.3", "1.2.3-rc1", "1.2.x", "v1.2.3", "-1.2.3"} {
		if _, err := content.ParseVersion(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
	if got := v("10.0.7").String(); got != "10.0.7" {
		t.Errorf("round trip: %s", got)
	}
	if v("1.10.0").Compare(v("1.9.9")) != 1 {
		t.Error("1.10.0 must sort after 1.9.9 numerically, not lexically")
	}
}

func TestConstraints(t *testing.T) {
	cases := []struct {
		constraint string
		allows     []string
		refuses    []string
	}{
		{"1.2.3", []string{"1.2.3"}, []string{"1.2.4", "1.2.2"}},
		{"=1.2.3", []string{"1.2.3"}, []string{"1.3.0"}},
		{"^1.2.3", []string{"1.2.3", "1.9.0"}, []string{"1.2.2", "2.0.0"}},
		{"^1.2", []string{"1.2.0", "1.99.99"}, []string{"1.1.9", "2.0.0"}},
		{"^1", []string{"1.0.0", "1.5.0"}, []string{"0.9.9", "2.0.0"}},
		{"^0.2.3", []string{"0.2.3", "0.2.9"}, []string{"0.3.0", "0.2.2"}},
		{"^0.0.3", []string{"0.0.3"}, []string{"0.0.4"}},
		{"~1.2.3", []string{"1.2.3", "1.2.9"}, []string{"1.3.0"}},
		{"~1", []string{"1.0.0", "1.9.0"}, []string{"2.0.0"}},
		{">=1.2 <2", []string{"1.2.0", "1.9.9"}, []string{"1.1.0", "2.0.0"}},
		{">1.0.0 <=1.4.0", []string{"1.0.1", "1.4.0"}, []string{"1.0.0", "1.4.1"}},
	}
	for _, tc := range cases {
		c, err := content.ParseConstraint(tc.constraint)
		if err != nil {
			t.Fatalf("%q: %v", tc.constraint, err)
		}
		if c.String() != tc.constraint {
			t.Errorf("%q renders as %q; a manifest must re-encode to what its author wrote", tc.constraint, c.String())
		}
		for _, s := range tc.allows {
			if !c.Allows(v(s)) {
				t.Errorf("%q refuses %s", tc.constraint, s)
			}
		}
		for _, s := range tc.refuses {
			if c.Allows(v(s)) {
				t.Errorf("%q allows %s", tc.constraint, s)
			}
		}
	}
	for _, bad := range []string{"", " ^1", "^1 ", "^1  <2", "*", "1.x", "1 || 2", "^", "2", "=1.2", "^1.2.3-rc1"} {
		if _, err := content.ParseConstraint(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
	if (content.Constraint{}).Allows(v("1.0.0")) {
		t.Error("the zero constraint allowed a version; an unstated constraint is not 'any'")
	}
}
