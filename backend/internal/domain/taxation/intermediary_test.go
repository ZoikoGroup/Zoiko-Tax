package taxation_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/taxation"
	"github.com/zoikogroup/zoikotax/backend/internal/goldentest"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// Intermediaries (ZTAX-OBL-REQ-0020): parties beside the seller and the
// customer, carried as facts for responsibility and nothing else.

func withIntermediaries(t *testing.T, ps ...taxation.Intermediary) taxation.Document {
	d := bundle(t)
	d.Intermediaries = ps
	return d
}

func TestIntermediariesRoundTripAndDoNotChangeTheTax(t *testing.T) {
	e := engine(t)
	d := withIntermediaries(t,
		taxation.Intermediary{Kind: taxation.PartyMarketplace, ID: "MKT-1"},
		taxation.Intermediary{Kind: taxation.PartyReseller, ID: "RES-9"})
	data, err := canonical.Encode(d.Canonical())
	if err != nil {
		t.Fatal(err)
	}
	back, err := taxation.DecodeDocument(data)
	if err != nil {
		t.Fatalf("a document with intermediaries does not round-trip: %v", err)
	}
	if len(back.Intermediaries) != 2 || back.Intermediaries[1] != d.Intermediaries[1] {
		t.Fatalf("intermediaries read back as %+v", back.Intermediaries)
	}
	with, err := canonical.Encode(determine(t, e, d, taxPoint).Canonical())
	if err != nil {
		t.Fatal(err)
	}
	without, err := canonical.Encode(determine(t, e, bundle(t), taxPoint).Canonical())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(with, without) {
		t.Fatal("naming intermediaries changed the determination; they decide who holds a role, not what is owed")
	}
}

func TestADocumentWithoutIntermediariesEncodesAsBefore(t *testing.T) {
	// Every envelope recorded before intermediaries existed must encode, and
	// so replay, byte for byte as it did.
	data, err := canonical.Encode(bundle(t).Canonical())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("intermediaries")) {
		t.Fatalf("an empty list is encoded: %s", data)
	}
}

func TestIntermediariesAreValidated(t *testing.T) {
	many := make([]taxation.Intermediary, taxation.MaxIntermediaries+1)
	for i := range many {
		many[i] = taxation.Intermediary{Kind: taxation.PartyReseller, ID: "R" + strings.Repeat("x", i+1)}
	}
	for name, ps := range map[string][]taxation.Intermediary{
		"a seller is not an intermediary": {{Kind: taxation.PartySeller, ID: "S"}},
		"an unknown kind":                 {{Kind: "AGENT", ID: "A"}},
		"no reference":                    {{Kind: taxation.PartyMarketplace}},
		"a reference too long":            {{Kind: taxation.PartyMarketplace, ID: strings.Repeat("m", 129)}},
		"the same party twice":            {{Kind: taxation.PartyReseller, ID: "R"}, {Kind: taxation.PartyReseller, ID: "R"}},
		"too many":                        many,
	} {
		t.Run(name, func(t *testing.T) {
			if err := withIntermediaries(t, ps...).Validate(); err == nil {
				t.Fatal("the document was accepted")
			}
		})
	}
	two := withIntermediaries(t,
		taxation.Intermediary{Kind: taxation.PartyReseller, ID: "R1"},
		taxation.Intermediary{Kind: taxation.PartyReseller, ID: "R2"})
	if err := two.Validate(); err != nil {
		// Two resellers are facts; which one holds a role is responsibility's
		// question, answered CONFLICTED, not a malformed document.
		t.Fatalf("two different resellers were refused: %v", err)
	}
}

func TestRoleCandidatesReadPrecedence(t *testing.T) {
	for in, want := range map[string]string{
		"NONE":                      "",
		"SELLER":                    "SELLER",
		"MARKETPLACE|SELLER":        "MARKETPLACE,SELLER",
		"RESELLER|WHOLESALER|BUYER": "RESELLER,WHOLESALER,BUYER",
	} {
		got, err := taxation.RoleCandidates(in)
		if err != nil || strings.Join(got, ",") != want {
			t.Fatalf("%q read as %v, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "SUPPLIER", "SELLER|", "NONE|SELLER", "SELLER|SELLER", "seller"} {
		if _, err := taxation.RoleCandidates(bad); err == nil {
			t.Fatalf("%q was accepted", bad)
		}
	}
}

func TestAProfileMayNotPlaceIncidenceOrBurdenOnNobody(t *testing.T) {
	raw := string(goldentest.ReadFile(t, trainingProfile))
	for _, role := range []string{"incidence", "economicBearer"} {
		t.Run(role, func(t *testing.T) {
			i := strings.Index(raw, `"`+role+`"`)
			if i < 0 {
				t.Fatalf("the training profile has no %s", role)
			}
			end := i + strings.Index(raw[i:], ",")
			if c := strings.Index(raw[i:], "}"); c >= 0 && i+c < end {
				end = i + c
			}
			edited := raw[:i] + `"` + role + `": "NONE"` + raw[end:]
			p, err := taxation.DecodeProfile([]byte(edited))
			if err == nil {
				_, err = taxation.Compile(p)
			}
			if err == nil || !strings.Contains(err.Error(), "on no party") {
				t.Fatalf("a profile placing %s on nobody: %v; want a refusal saying so", role, err)
			}
		})
	}
}
