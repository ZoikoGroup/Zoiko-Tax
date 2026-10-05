package content_test

import (
	"strings"
	"testing"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/content"
)

func vatReturn() content.ObligationDeclaration {
	return content.ObligationDeclaration{
		ID: "xa-vat-return", Version: "2026.09.1", Type: "VAT_RETURN",
		Authority: "authority:xa-revenue", Jurisdiction: "jurisdiction:xa", Duty: "TRANSACTION_MONETARY",
		Currency: "EUR", Assesses: []string{"TAX_VAT"},
		Period: content.ObligationPeriod{Kind: "QUARTER", Timezone: "UTC", YearStartMonth: 1},
		Due:    content.ObligationDue{OffsetMonths: 1, DayOfMonth: 20},
		Source: "SRC-1", Citation: "s. 1",
	}
}

func TestObligationDeclarationsAreValidated(t *testing.T) {
	if err := (content.FiscalProfile{Obligations: []content.ObligationDeclaration{vatReturn()}}).Validate(); err != nil {
		t.Fatalf("a well-formed declaration: %v", err)
	}
	for name, tc := range map[string]struct {
		edit func(*content.ObligationDeclaration)
		want string
	}{
		"no type":               {func(o *content.ObligationDeclaration) { o.Type = "" }, "no id, version or type"},
		"no authority":          {func(o *content.ObligationDeclaration) { o.Authority = "" }, "no authority"},
		"a non-monetary duty":   {func(o *content.ObligationDeclaration) { o.Duty = "REGISTRATION" }, "TRANSACTION_MONETARY"},
		"nothing assessed":      {func(o *content.ObligationDeclaration) { o.Assesses = nil }, "nothing it assesses"},
		"a repeated slot":       {func(o *content.ObligationDeclaration) { o.Assesses = []string{"A", "A"} }, "repeated slot"},
		"no citation":           {func(o *content.ObligationDeclaration) { o.Citation = "" }, "no source or citation"},
		"a rolling period":      {func(o *content.ObligationDeclaration) { o.Period.Kind = "ROLLING" }, "MONTH, QUARTER or YEAR"},
		"no year start":         {func(o *content.ObligationDeclaration) { o.Period.YearStartMonth = 0 }, "year start month"},
		"due before period":     {func(o *content.ObligationDeclaration) { o.Due.OffsetDays = -1 }, "before its period ends"},
		"an impossible due day": {func(o *content.ObligationDeclaration) { o.Due.DayOfMonth = 32 }, "due day of month"},
	} {
		o := vatReturn()
		tc.edit(&o)
		err := (content.FiscalProfile{Obligations: []content.ObligationDeclaration{o}}).Validate()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
	}
	twice := content.FiscalProfile{Obligations: []content.ObligationDeclaration{vatReturn(), vatReturn()}}
	if err := twice.Validate(); err == nil || !strings.Contains(err.Error(), "declared twice") {
		t.Errorf("a declaration repeated: %v", err)
	}
}

func TestNormalizeOrdersObligationsWithoutTouchingTheInput(t *testing.T) {
	b, a := vatReturn(), vatReturn()
	b.ID, b.Assesses = "b", []string{"Z", "A"}
	a.ID = "a"
	in := content.FiscalProfile{Obligations: []content.ObligationDeclaration{b, a}}
	out := in.Normalize()
	if out.Obligations[0].ID != "a" || out.Obligations[1].Assesses[0] != "A" {
		t.Fatalf("normalized %+v", out.Obligations)
	}
	if in.Obligations[0].ID != "b" || in.Obligations[0].Assesses[0] != "Z" {
		t.Fatalf("normalizing changed its input: %+v", in.Obligations)
	}
}
