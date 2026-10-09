package legal_test

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/legal"
)

var (
	entity = id.NewLegalEntityID(uuid.MustParse("00000000-0000-7000-8000-00000000e001"))
	other  = id.NewLegalEntityID(uuid.MustParse("00000000-0000-7000-8000-00000000e002"))
	now    = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
)

// devDoc is the development matrix's rules (content/legal), inline: the
// domain reads no files. cmd/ztax-core's test loads the file itself.
const devDoc = `{"version":"dev-2026.10-1","draft":true,"rules":[{"id":"DE-ELSTER-COMPUTE","version":1,"country":"DE","authority":"DE-ELSTER","service":"COMPUTE","status":"DIRECT_ALLOWED","provider":"Zoiko development entity","authorizationType":"NONE","funds":"NO_CUSTODY","opinionRef":"LDR-DEV-0001 — development posture, not a legal opinion","effectiveFrom":"2026-01-01T00:00:00Z"},{"id":"DE-ELSTER-PREPARE","version":1,"country":"DE","authority":"DE-ELSTER","service":"PREPARE","status":"DIRECT_ALLOWED","provider":"Zoiko development entity","authorizationType":"NONE","funds":"NO_CUSTODY","opinionRef":"LDR-DEV-0001 — development posture, not a legal opinion","effectiveFrom":"2026-01-01T00:00:00Z"},{"id":"DE-ELSTER-FILE","version":1,"country":"DE","authority":"DE-ELSTER","service":"FILE","status":"DIRECT_WITH_AUTH","provider":"Zoiko development entity","authorizationType":"FILING_MANDATE","requiresPeriods":true,"credential":"ELSTER organisation certificate","funds":"NO_CUSTODY","opinionRef":"LDR-DEV-0001 — development posture, not a legal opinion","effectiveFrom":"2026-01-01T00:00:00Z"},{"id":"DE-ELSTER-REPRESENT","version":1,"country":"DE","authority":"DE-ELSTER","service":"REPRESENT","status":"QUALIFIED_PERSON_REQUIRED","provider":"Zoiko development entity","authorizationType":"POA","qualification":"Steuerberater admitted under StBerG","funds":"NO_CUSTODY","opinionRef":"LDR-DEV-0001 — development posture, not a legal opinion","effectiveFrom":"2026-01-01T00:00:00Z"},{"id":"DE-ELSTER-ADVISE","version":1,"country":"DE","authority":"DE-ELSTER","service":"ADVISE","status":"PARTNER_REQUIRED","provider":"Approved advisory partner","authorizationType":"NONE","funds":"NO_CUSTODY","opinionRef":"LDR-DEV-0001 — development posture, not a legal opinion","effectiveFrom":"2026-01-01T00:00:00Z"},{"id":"GB-HMRC-FILE","version":1,"country":"GB","authority":"GB-HMRC","service":"FILE","status":"COUNSEL_PENDING","provider":"Zoiko development entity","authorizationType":"PORTAL_DELEGATION","funds":"NO_CUSTODY","opinionRef":"LDR-DEV-0002 — awaiting counsel","effectiveFrom":"2026-01-01T00:00:00Z"}]}`

func devMatrix(t *testing.T) legal.Matrix {
	t.Helper()
	m, err := legal.ParseMatrix([]byte(devDoc))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func mandate(mut func(*legal.Authorization)) legal.Authorization {
	a := legal.Authorization{
		ID:          id.NewAuthorizationID(uuid.Must(uuid.NewV7())),
		LegalEntity: entity, Country: "DE", Authority: "DE-ELSTER", Type: legal.AuthFilingMandate,
		Permissions: []legal.Permission{legal.PermPrepare, legal.PermSubmit},
		PeriodFrom:  "2026-01", PeriodTo: "2026-12",
		EffectiveFrom: now.AddDate(0, -1, 0), ExpiresAt: now.AddDate(1, 0, 0),
		Evidence: []string{"doc:mandate-2026.pdf"}, CredentialRef: "vault://tax/de/elster/cert", Status: legal.AuthActive,
	}
	if mut != nil {
		mut(&a)
	}
	return a
}

func file(period string) legal.Action {
	return legal.Action{Country: "DE", Authority: "DE-ELSTER", Service: legal.ServiceFile, LegalEntity: entity, Period: period}
}

// ZTAX-LEG-REQ-0001, -0003, -0008, -0056, -0057: every service state
// resolves to a state, and only DIRECT_ALLOWED, or DIRECT_WITH_AUTH with an
// authorization, lets the action through.
func TestLEGREQ0001EveryActionResolvesAndFailsClosed(t *testing.T) {
	m := devMatrix(t)
	for _, c := range []struct {
		act     legal.Action
		allowed bool
		status  legal.Status
		reason  legal.Reason
	}{
		{legal.Action{Country: "DE", Authority: "DE-ELSTER", Service: legal.ServiceCompute, LegalEntity: entity}, true, legal.StatusDirectAllowed, ""},
		{legal.Action{Country: "DE", Authority: "DE-ELSTER", Service: legal.ServicePrepare, LegalEntity: entity}, true, legal.StatusDirectAllowed, ""},
		{legal.Action{Country: "DE", Authority: "DE-ELSTER", Service: legal.ServiceRepresent, LegalEntity: entity}, false, legal.StatusQualifiedPerson, legal.ReasonQualifiedPerson},
		{legal.Action{Country: "DE", Authority: "DE-ELSTER", Service: legal.ServiceAdvise, LegalEntity: entity}, false, legal.StatusPartnerRequired, legal.ReasonPartnerRequired},
		{legal.Action{Country: "GB", Authority: "GB-HMRC", Service: legal.ServiceFile, LegalEntity: entity}, false, legal.StatusCounselPending, legal.ReasonCounselPending},
		// Filing authority is per authority, not per country (ZTAX-LEG-REQ-0059).
		{legal.Action{Country: "DE", Authority: "DE-BZST", Service: legal.ServiceFile, LegalEntity: entity}, false, legal.StatusCounselPending, legal.ReasonNoRule},
		{legal.Action{Country: "DE", Authority: "DE-ELSTER", Service: legal.ServiceInform, LegalEntity: entity}, false, legal.StatusCounselPending, legal.ReasonNoRule},
	} {
		r := legal.Resolve(m, []legal.Authorization{mandate(nil)}, c.act, now)
		if r.Allowed != c.allowed || r.Status != c.status || r.Reason != c.reason || r.MatrixDigest.IsZero() {
			t.Errorf("%s %s: %+v", c.act.Authority, c.act.Service, r)
		}
	}
	for _, s := range []legal.Status{legal.StatusProhibited, legal.StatusSuspended, legal.StatusCustomerOnly} {
		b := strings.Replace(matrixWith(string(s)), "PLACEHOLDER", string(s), 1)
		mm, err := legal.ParseMatrix([]byte(b))
		if err != nil {
			t.Fatal(err)
		}
		if r := legal.Resolve(mm, nil, file("2026-09"), now); r.Allowed || r.Status != s {
			t.Errorf("%s let the action through: %+v", s, r)
		}
	}
	if r := legal.Resolve(legal.Matrix{}, nil, file("2026-09"), now); r.Allowed || r.Reason != legal.ReasonUnsupportedMatrix {
		t.Fatalf("with no matrix: %+v", r)
	}
}

func matrixWith(status string) string {
	return `{"version":"t","rules":[{"id":"r","version":1,"country":"DE","authority":"DE-ELSTER","service":"FILE",
		"status":"PLACEHOLDER","provider":"p","authorizationType":"NONE","funds":"NO_CUSTODY","opinionRef":"memo",
		"effectiveFrom":"2026-01-01T00:00:00Z"}]}`
}

// ZTAX-LEG-REQ-0004, -0013, -0014, -0016, -0062, -0101: FILE needs an
// authorization in force, of the right type, granting submission, for the
// period, bound to a credential — and says which of those it lacked.
func TestLEGREQ0004FilingNeedsACurrentScopedAuthorization(t *testing.T) {
	m := devMatrix(t)
	r := legal.Resolve(m, []legal.Authorization{mandate(nil)}, file("2026-09"), now)
	if !r.Allowed || r.Status != legal.StatusDirectWithAuth || r.Authorization.IsZero() || r.Rule.ID != "DE-ELSTER-FILE" {
		t.Fatalf("a filing under a mandate: %+v", r)
	}
	for name, c := range map[string]struct {
		auths  []legal.Authorization
		act    legal.Action
		reason legal.Reason
	}{
		"no authorization":       {nil, file("2026-09"), legal.ReasonAuthMissing},
		"another legal entity's": {[]legal.Authorization{mandate(func(a *legal.Authorization) { a.LegalEntity = other })}, file("2026-09"), legal.ReasonAuthMissing},
		"information access only": {[]legal.Authorization{mandate(func(a *legal.Authorization) {
			a.Type = legal.AuthTaxInformation
			a.Permissions = []legal.Permission{legal.PermReadInfo}
		})}, file("2026-09"), legal.ReasonAuthMissing},
		"no submission permission": {[]legal.Authorization{mandate(func(a *legal.Authorization) { a.Permissions = []legal.Permission{legal.PermPrepare} })}, file("2026-09"), legal.ReasonAuthMissing},
		"revoked":                  {[]legal.Authorization{mandate(func(a *legal.Authorization) { a.Status = legal.AuthRevoked })}, file("2026-09"), legal.ReasonAuthRevoked},
		"superseded":               {[]legal.Authorization{mandate(func(a *legal.Authorization) { a.Status = legal.AuthSuperseded })}, file("2026-09"), legal.ReasonAuthRevoked},
		"expired":                  {[]legal.Authorization{mandate(func(a *legal.Authorization) { a.ExpiresAt = now })}, file("2026-09"), legal.ReasonAuthExpired},
		"another period":           {[]legal.Authorization{mandate(nil)}, file("2027-01"), legal.ReasonAuthOutOfScope},
		"no period on the action":  {[]legal.Authorization{mandate(nil)}, file(""), legal.ReasonAuthOutOfScope},
		"a generic grant":          {[]legal.Authorization{mandate(func(a *legal.Authorization) { a.PeriodFrom, a.PeriodTo = "", "" })}, file("2026-09"), legal.ReasonAuthOutOfScope},
		"no credential bound":      {[]legal.Authorization{mandate(func(a *legal.Authorization) { a.CredentialRef = "" })}, file("2026-09"), legal.ReasonCredentialMissing},
	} {
		if r := legal.Resolve(m, c.auths, c.act, now); r.Allowed || r.Reason != c.reason {
			t.Errorf("%s: %+v", name, r)
		}
	}
	// A filing mandate does not make Zoiko a representative
	// (ZTAX-LEG-REQ-0062).
	rep := file("2026-09")
	rep.Service = legal.ServiceRepresent
	if r := legal.Resolve(m, []legal.Authorization{mandate(nil)}, rep, now); r.Allowed {
		t.Fatalf("a filing mandate let Zoiko represent: %+v", r)
	}
}

// ZTAX-LEG-REQ-0009, -0102: rules are versioned, effective-dated and cite
// their opinion; two that disagree fail closed.
func TestLEGREQ0009RulesAreVersionedEffectiveDatedAndCited(t *testing.T) {
	base := `{"id":"r","country":"DE","authority":"A","service":"FILE","provider":"p","authorizationType":"NONE","funds":"NO_CUSTODY","opinionRef":"memo"`
	doc := `{"version":"t","rules":[` +
		base + `,"version":1,"status":"PROHIBITED","effectiveFrom":"2026-01-01T00:00:00Z"},` +
		base + `,"version":2,"status":"DIRECT_ALLOWED","effectiveFrom":"2026-07-01T00:00:00Z"}]}`
	m, err := legal.ParseMatrix([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	act := legal.Action{Country: "DE", Authority: "A", Service: legal.ServiceFile, LegalEntity: entity}
	if r := legal.Resolve(m, nil, act, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)); r.Allowed || r.Rule.Version != 1 {
		t.Fatalf("before version 2: %+v", r)
	}
	if r := legal.Resolve(m, nil, act, now); !r.Allowed || r.Rule.Version != 2 {
		t.Fatalf("under version 2: %+v", r)
	}
	conflicted := strings.Replace(doc, `"id":"r","country":"DE","authority":"A","service":"FILE","provider":"p","authorizationType":"NONE","funds":"NO_CUSTODY","opinionRef":"memo","version":2`,
		`"id":"r2","country":"DE","authority":"A","service":"FILE","provider":"p","authorizationType":"NONE","funds":"NO_CUSTODY","opinionRef":"memo","version":1`, 1)
	m2, err := legal.ParseMatrix([]byte(conflicted))
	if err != nil {
		t.Fatal(err)
	}
	if r := legal.Resolve(m2, nil, act, now); r.Allowed || r.Reason != legal.ReasonRuleConflict {
		t.Fatalf("two disagreeing rules: %+v", r)
	}
	if _, err := legal.ParseMatrix([]byte(strings.ReplaceAll(doc, `"opinionRef":"memo"`, `"opinionRef":""`))); err == nil {
		t.Fatal("a rule citing nothing parsed")
	}
	if _, err := legal.ParseMatrix([]byte(strings.Replace(doc, `"version":"t"`, `"version":"t","extra":1`, 1))); err == nil {
		t.Fatal("an unknown field parsed")
	}
	if a, b := devMatrix(t).Digest, devMatrix(t).Digest; !a.Equal(b) {
		t.Fatal("the matrix digest is not stable")
	}
}

// ZTAX-LEG-REQ-0014, -0015, -0017, -0018: an authorization says who, to
// whom, for what; its history is kept; a credential is a vault reference.
func TestLEGREQ0014AuthorizationsAreWellFormedAndKeepTheirHistory(t *testing.T) {
	if err := mandate(nil).Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*legal.Authorization){
		"no legal entity":       func(a *legal.Authorization) { a.LegalEntity = id.LegalEntityID{} },
		"no authority":          func(a *legal.Authorization) { a.Authority = "" },
		"no permission":         func(a *legal.Authorization) { a.Permissions = nil },
		"no evidence":           func(a *legal.Authorization) { a.Evidence = nil },
		"a raw secret":          func(a *legal.Authorization) { a.CredentialRef = "-----BEGIN PRIVATE KEY-----" },
		"a password":            func(a *legal.Authorization) { a.CredentialRef = "hunter2" },
		"half a period":         func(a *legal.Authorization) { a.PeriodTo = "" },
		"a backwards period":    func(a *legal.Authorization) { a.PeriodFrom, a.PeriodTo = "2026-12", "2026-01" },
		"information as agency": func(a *legal.Authorization) { a.Type = legal.AuthTaxInformation },
		"expiry before effect":  func(a *legal.Authorization) { a.ExpiresAt = a.EffectiveFrom },
	} {
		if err := mandate(mut).Validate(); err == nil {
			t.Errorf("%s validated", name)
		}
	}
	a := mandate(nil)
	successor := id.NewAuthorizationID(uuid.Must(uuid.NewV7()))
	got, err := legal.Fold(a, []legal.Event{{Seq: 1, Kind: legal.EventGranted}, {Seq: 2, Kind: legal.EventSuperseded, By: successor}})
	if err != nil || got.Status != legal.AuthSuperseded || got.InForce(now) || len(got.History) != 2 {
		t.Fatalf("a superseded grant: %v %+v", err, got)
	}
	if _, err := legal.Fold(a, []legal.Event{{Seq: 1, Kind: legal.EventGranted}, {Seq: 2, Kind: legal.EventRevoked}, {Seq: 3, Kind: legal.EventRevoked}}); err == nil {
		t.Fatal("a grant revoked twice folded")
	}
}
