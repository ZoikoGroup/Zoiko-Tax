package privacy_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/privacy"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

var (
	t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t1 = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
)

func activity() privacy.ProcessingActivity {
	return privacy.ProcessingActivity{
		ID:                 "PA-TAX-CALC-001",
		Purpose:            privacy.PurposeTaxCalc,
		Role:               privacy.RoleProcessor,
		CustomerScope:      "all tenants of the cell",
		DataSubjects:       []string{"subscribers"},
		DataCategories:     []privacy.Class{privacy.P0, privacy.P5},
		CollectionSources:  []privacy.CollectionSource{privacy.SourceCustomer},
		LegalBasis:         privacy.LegalBasisRef{Category: privacy.BasisCustomerInstruction, Reference: "DPA-STD-2026"},
		NecessityRationale: "the transaction amount and parties are the tax base",
		Recipients:         []string{"customer users"},
		Regions:            privacy.Regions{Processing: []string{"eu-west"}, Storage: []string{"eu-west"}},
		RetentionPolicyID:  "RP-FISCAL-PACK",
		DPIA:               privacy.DPIANotRequired,
		Owner:              "privacy-office",
		ReviewDue:          t1,
	}
}

func TestProcessingActivityRequirements(t *testing.T) {
	if err := activity().Validate(); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	broken := map[string]func(*privacy.ProcessingActivity){
		"PRIV-REQ-0004 role":             func(a *privacy.ProcessingActivity) { a.Role = "" },
		"PRIV-REQ-0005 subjects":         func(a *privacy.ProcessingActivity) { a.DataSubjects = nil },
		"PRIV-REQ-0005 recipients":       func(a *privacy.ProcessingActivity) { a.Recipients = nil },
		"PRIV-REQ-0006 regions":          func(a *privacy.ProcessingActivity) { a.Regions.Storage = nil },
		"PRIV-REQ-0007 legal basis":      func(a *privacy.ProcessingActivity) { a.LegalBasis.Reference = "" },
		"PRIV-REQ-0009 necessity":        func(a *privacy.ProcessingActivity) { a.NecessityRationale = "" },
		"unapproved purpose":             func(a *privacy.ProcessingActivity) { a.Purpose = "PURP-MARKETING" },
		"unknown class":                  func(a *privacy.ProcessingActivity) { a.DataCategories = []privacy.Class{"P9"} },
		"no retention":                   func(a *privacy.ProcessingActivity) { a.RetentionPolicyID = "" },
		"unknown DPIA status":            func(a *privacy.ProcessingActivity) { a.DPIA = "MAYBE" },
		"P6 without documented need":     func(a *privacy.ProcessingActivity) { a.DataCategories = []privacy.Class{privacy.P6} },
		"no review date":                 func(a *privacy.ProcessingActivity) { a.ReviewDue = time.Time{} },
		"unknown collection source":      func(a *privacy.ProcessingActivity) { a.CollectionSources = []privacy.CollectionSource{"SCRAPED"} },
		"free-text basis with no anchor": func(a *privacy.ProcessingActivity) { a.LegalBasis.Category = "GDPR consent" },
	}
	for name, mutate := range broken {
		a := activity()
		mutate(&a)
		if err := a.Validate(); !errors.Is(err, privacy.ErrInvalidRecord) {
			t.Errorf("%s: want ErrInvalidRecord, got %v", name, err)
		}
	}

	// P6 with an approved assessment and a legal-obligation basis is the
	// documented exception, and passes.
	a := activity()
	a.DataCategories = []privacy.Class{privacy.P6}
	a.DPIA = privacy.DPIAApproved
	a.LegalBasis = privacy.LegalBasisRef{Category: privacy.BasisLegalObligation, Reference: "XX-TAX-ACT-s12"}
	if err := a.Validate(); err != nil {
		t.Fatalf("documented P6: %v", err)
	}
}

func TestRetentionPolicy(t *testing.T) {
	ok := privacy.RetentionPolicy{ID: "RP-FISCAL-PACK", Family: privacy.RetentionFiscal, Trigger: "filing period close", Rule: "pack record-retention rule"}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	transient := privacy.RetentionPolicy{ID: "RP-RAW-IP", Family: privacy.RetentionTransient, Trigger: "situs derived"}
	if err := transient.Validate(); err != nil {
		t.Fatalf("transient needs no period rule: %v", err)
	}
	for _, bad := range []privacy.RetentionPolicy{
		{Family: privacy.RetentionFiscal, Trigger: "x", Rule: "y"},
		{ID: "a", Family: "RET-7Y", Trigger: "x", Rule: "y"},
		{ID: "a", Family: privacy.RetentionFiscal, Rule: "y"},
		{ID: "a", Family: privacy.RetentionFiscal, Trigger: "x"},
	} {
		if err := bad.Validate(); !errors.Is(err, privacy.ErrInvalidRecord) {
			t.Errorf("%+v: want ErrInvalidRecord, got %v", bad, err)
		}
	}
}

func profile() privacy.PrivacyProfile {
	return privacy.PrivacyProfile{
		LawID: "GDPR", LawVersion: "2016/679", Jurisdiction: "EU",
		LawfulBases:   []privacy.LegalBasisCategory{privacy.BasisContract, privacy.BasisLegalObligation},
		Rights:        []privacy.RightRule{{Right: "ACCESS", DeadlineDays: 30, Appeal: true}},
		DPIATriggers:  []string{"systematic monitoring"},
		TransferRules: []privacy.TransferMechanism{privacy.MechanismAdequacy, privacy.MechanismContractualSafeguards},
		BreachRules:   []privacy.BreachRule{{Threshold: "risk to rights", Clock: 72 * time.Hour, Recipients: []string{"lead supervisory authority"}}},
		EffectiveFrom: t0, KnownAt: t0,
	}
}

func TestPrivacyProfileRequirements(t *testing.T) {
	if err := profile().Validate(); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	broken := map[string]func(*privacy.PrivacyProfile){
		"PRIV-REQ-0104 effective date": func(p *privacy.PrivacyProfile) { p.EffectiveFrom = time.Time{} },
		"PRIV-REQ-0104 known date":     func(p *privacy.PrivacyProfile) { p.KnownAt = time.Time{} },
		"PRIV-REQ-0105 rights":         func(p *privacy.PrivacyProfile) { p.Rights = nil },
		"PRIV-REQ-0105 transfer":       func(p *privacy.PrivacyProfile) { p.TransferRules = nil },
		"PRIV-REQ-0105 breach":         func(p *privacy.PrivacyProfile) { p.BreachRules = nil },
		"PRIV-REQ-0105 DPIA":           func(p *privacy.PrivacyProfile) { p.DPIATriggers = nil },
		"ends before it begins":        func(p *privacy.PrivacyProfile) { p.EffectiveTo = t0 },
		"localization and transfers":   func(p *privacy.PrivacyProfile) { p.Localization = true },
		"breach rule with no clock":    func(p *privacy.PrivacyProfile) { p.BreachRules[0].Clock = 0 },
	}
	for name, mutate := range broken {
		p := profile()
		p.BreachRules = append([]privacy.BreachRule(nil), p.BreachRules...)
		mutate(&p)
		if err := p.Validate(); !errors.Is(err, privacy.ErrInvalidRecord) {
			t.Errorf("%s: want ErrInvalidRecord, got %v", name, err)
		}
	}
	// Localization with no transfer rules is a complete statement.
	p := profile()
	p.Localization, p.TransferRules = true, nil
	if err := p.Validate(); err != nil {
		t.Fatalf("localized profile: %v", err)
	}
	if !profile().EffectiveAt(t1) || profile().EffectiveAt(t0.Add(-time.Second)) {
		t.Fatal("EffectiveAt is wrong at the boundaries")
	}
}

func transferProfile() privacy.TransferProfile {
	return privacy.TransferProfile{
		ID: "TP-EU1-EU2", Version: 3,
		Exporter:      privacy.Party{Name: "Zoiko EU", Role: privacy.RoleProcessor},
		Importer:      privacy.Party{Name: "Zoiko EU", Role: privacy.RoleProcessor},
		Origin:        privacy.Location{Region: "eu-west", Cell: "eu-west-1"},
		Destination:   privacy.Location{Region: "eu-central", Cell: "eu-central-1"},
		Mechanism:     privacy.MechanismNotRestricted,
		Purposes:      []privacy.Purpose{privacy.PurposeEvid},
		DataClasses:   []privacy.Class{privacy.P0, privacy.P1, privacy.P5},
		EffectiveFrom: t0, ReviewDue: t1,
	}
}

func TestTransferProfileRequirements(t *testing.T) {
	if err := transferProfile().Validate(); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	broken := map[string]func(*privacy.TransferProfile){
		"PRIV-REQ-0066 exporter":       func(p *privacy.TransferProfile) { p.Exporter.Name = "" },
		"PRIV-REQ-0066 importer role":  func(p *privacy.TransferProfile) { p.Importer.Role = "" },
		"PRIV-REQ-0066 destination":    func(p *privacy.TransferProfile) { p.Destination.Cell = "" },
		"PRIV-REQ-0066 mechanism":      func(p *privacy.TransferProfile) { p.Mechanism = "" },
		"PRIV-REQ-0066 purpose":        func(p *privacy.TransferProfile) { p.Purposes = nil },
		"PRIV-REQ-0066 data":           func(p *privacy.TransferProfile) { p.DataClasses = nil },
		"PRIV-REQ-0067 review date":    func(p *privacy.TransferProfile) { p.ReviewDue = time.Time{} },
		"PRIV-REQ-0067 effective date": func(p *privacy.TransferProfile) { p.EffectiveFrom = time.Time{} },
		"same cell":                    func(p *privacy.TransferProfile) { p.Destination.Cell = p.Origin.Cell },
		"P7 is never transferable":     func(p *privacy.TransferProfile) { p.DataClasses = append(p.DataClasses, privacy.P7) },
		"safeguards mechanism without safeguards": func(p *privacy.TransferProfile) {
			p.Mechanism = privacy.MechanismContractualSafeguards
		},
	}
	for name, mutate := range broken {
		p := transferProfile()
		mutate(&p)
		if err := p.Validate(); !errors.Is(err, privacy.ErrInvalidRecord) {
			t.Errorf("%s: want ErrInvalidRecord, got %v", name, err)
		}
	}
}

func uid(s string) id.UserID { return id.NewUserID(uuid.MustParse(s)) }

func request() privacy.TransferRequest {
	return privacy.TransferRequest{
		ID:              id.NewTransferID(uuid.MustParse("01920000-0000-7000-8000-0000000000f1")),
		TenantID:        id.NewTenantID(uuid.MustParse("01920000-0000-7000-8000-0000000000f2")),
		SourceCell:      "eu-west-1",
		DestinationCell: "eu-central-1",
		Purpose:         privacy.PurposeEvid,
		DataClasses:     []privacy.Class{privacy.P5, privacy.P0, privacy.P5},
		ContentDigest:   canonical.SumBytes([]byte(`{"records":["d1","d2"]}`)),
		RequestedBy:     uid("01920000-0000-7000-8000-0000000000f3"),
		ApprovedBy:      uid("01920000-0000-7000-8000-0000000000f4"),
		At:              time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC),
	}
}

func TestCrossCellTransferPermitted(t *testing.T) {
	tr, err := privacy.NewCrossCellTransfer(request(), transferProfile())
	if err != nil {
		t.Fatal(err)
	}
	if tr.ProfileID != "TP-EU1-EU2" || tr.ProfileVersion != 3 || tr.Mechanism != privacy.MechanismNotRestricted {
		t.Fatalf("the record does not name its authorization: %+v", tr)
	}
	if len(tr.DataClasses) != 2 || tr.DataClasses[0] != privacy.P0 || tr.DataClasses[1] != privacy.P5 {
		t.Fatalf("classes not sorted and unique: %v", tr.DataClasses)
	}
}

// SEC-REQ-0042 and the lane brief: a transfer its profile does not permit is
// refused, and refused as not-permitted rather than as malformed.
func TestCrossCellTransferRefusedOutsideItsProfile(t *testing.T) {
	refused := map[string]func(*privacy.TransferRequest){
		"class not covered":       func(r *privacy.TransferRequest) { r.DataClasses = []privacy.Class{privacy.P0, privacy.P4} },
		"P7":                      func(r *privacy.TransferRequest) { r.DataClasses = []privacy.Class{privacy.P7} },
		"no classes stated":       func(r *privacy.TransferRequest) { r.DataClasses = nil },
		"other destination":       func(r *privacy.TransferRequest) { r.DestinationCell = "us-east-1" },
		"other source":            func(r *privacy.TransferRequest) { r.SourceCell = "eu-north-1" },
		"purpose not covered":     func(r *privacy.TransferRequest) { r.Purpose = privacy.PurposeAnalytics },
		"before effective":        func(r *privacy.TransferRequest) { r.At = t0.Add(-time.Hour) },
		"at or after review date": func(r *privacy.TransferRequest) { r.At = t1 },
	}
	for name, mutate := range refused {
		r := request()
		mutate(&r)
		if _, err := privacy.NewCrossCellTransfer(r, transferProfile()); !errors.Is(err, privacy.ErrTransferNotPermitted) {
			t.Errorf("%s: want ErrTransferNotPermitted, got %v", name, err)
		}
	}
	// An invalid profile permits nothing.
	p := transferProfile()
	p.Mechanism = ""
	if _, err := privacy.NewCrossCellTransfer(request(), p); !errors.Is(err, privacy.ErrTransferNotPermitted) {
		t.Errorf("invalid profile: want ErrTransferNotPermitted, got %v", err)
	}
}

func TestCrossCellTransferShape(t *testing.T) {
	malformed := map[string]func(*privacy.TransferRequest){
		"self-approved": func(r *privacy.TransferRequest) { r.ApprovedBy = r.RequestedBy },
		"no approver":   func(r *privacy.TransferRequest) { r.ApprovedBy = id.UserID{} },
		"no digest":     func(r *privacy.TransferRequest) { r.ContentDigest = canonical.Digest{} },
		"no tenant":     func(r *privacy.TransferRequest) { r.TenantID = id.TenantID{} },
		"no id":         func(r *privacy.TransferRequest) { r.ID = id.TransferID{} },
	}
	for name, mutate := range malformed {
		r := request()
		mutate(&r)
		if _, err := privacy.NewCrossCellTransfer(r, transferProfile()); !errors.Is(err, privacy.ErrInvalidTransfer) {
			t.Errorf("%s: want ErrInvalidTransfer, got %v", name, err)
		}
	}
}
