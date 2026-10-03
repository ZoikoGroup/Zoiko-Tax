package settlement_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/settlement"
	"github.com/zoikogroup/zoikotax/backend/internal/fiscaltest"
)

var paid = time.Date(2027, 4, 2, 0, 0, 0, 0, time.UTC)

func eur(t *testing.T, s string) fiscal.Money { return fiscaltest.Money(t, s, "EUR") }

func doc(n byte) id.FiscalDocumentID {
	return id.NewFiscalDocumentID(uuid.MustParse("00000000-0000-7000-8000-0000000004" + string([]byte{'0' + n/10, '0' + n%10})))
}

func TestFINREQ0058TimeoutIsNeverCollected(t *testing.T) {
	for o, want := range map[settlement.ProcessorOutcome]settlement.CollectionStatus{
		settlement.ProcessorSucceeded: settlement.CollectionSettled, settlement.ProcessorDeclined: settlement.CollectionFailed,
		settlement.ProcessorTimedOut: settlement.CollectionUncertain, settlement.ProcessorUnknown: settlement.CollectionUncertain,
	} {
		if got := settlement.StatusFrom(o); got != want {
			t.Errorf("%s -> %s, want %s", o, got, want)
		}
	}
}

func TestFINREQ0055And0056CollectionIsItsOwnEventWithSourceAndStatus(t *testing.T) {
	c := settlement.Collection{ID: "col-1", Kind: settlement.KindCollection, PaymentRef: "psp:ch_123", Source: "psp",
		Amount: eur(t, "121.00"), Status: settlement.CollectionSettled, Date: paid}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.PaymentRef = ""
	if err := c.Validate(); err == nil {
		t.Fatal("a collection with no payment reference validated")
	}
}

func TestFINREQ0057PartialCollectionAllocatesByExplicitPolicy(t *testing.T) {
	exp := []settlement.Exposure{
		{Document: doc(1), IssuedAt: paid.AddDate(0, -2, 0), Open: eur(t, "100.00")},
		{Document: doc(2), IssuedAt: paid.AddDate(0, -1, 0), Open: eur(t, "50.00")},
	}
	p := fiscaltest.LinePolicy(t, fiscal.RoundHalfUp, 2)
	oldest, err := settlement.Allocate(eur(t, "120.00"), exp, settlement.AllocateOldestFirst, p)
	if err != nil || len(oldest) != 2 || oldest[0].Amount.String() != "100.00" || oldest[1].Amount.String() != "20.00" {
		t.Fatalf("oldest-first %+v %v", oldest, err)
	}
	pro, err := settlement.Allocate(eur(t, "120.00"), exp, settlement.AllocateProRata, p)
	if err != nil || pro[0].Amount.String() != "80.00" || pro[1].Amount.String() != "40.00" {
		t.Fatalf("pro-rata %+v %v", pro, err)
	}
	if _, err := settlement.Allocate(eur(t, "120.00"), exp, "", p); err == nil {
		t.Fatal("an allocation with no declared method ran")
	}
}

func TestFINREQ0060ChargebackReopensReconciliation(t *testing.T) {
	if !(settlement.Collection{Kind: settlement.KindChargeback}).ReopensReconciliation() {
		t.Fatal("a chargeback does not reopen reconciliation")
	}
	if (settlement.Collection{Kind: settlement.KindCollection}).ReopensReconciliation() {
		t.Fatal("an ordinary collection reopens reconciliation")
	}
}

func filed(t *testing.T) settlement.ReturnVersion {
	return settlement.ReturnVersion{ReturnID: "ret-xa-2027-03", Version: 1, Authority: "authority:xa-revenue", Period: "2027-03",
		Population: []string{"dec-2", "dec-1"}, Amount: eur(t, "1210.00"), Status: settlement.ReturnFiled}
}

func TestFINREQ0062And0063FiledReturnIsImmutableAndAmendmentIsANewVersion(t *testing.T) {
	r := filed(t)
	a, err := r.Amend([]string{"dec-1", "dec-2", "dec-3"}, eur(t, "1331.00"))
	if err != nil {
		t.Fatal(err)
	}
	if a.Version != 2 || a.Amends == nil || *a.Amends != 1 || r.Amount.String() != "1210.00" || len(r.Population) != 2 {
		t.Fatalf("amendment %+v, original %+v", a, r)
	}
	if _, err := (settlement.ReturnVersion{Status: settlement.ReturnDraft}).Amend(nil, eur(t, "0")); err == nil {
		t.Fatal("a draft was amended")
	}
}

func TestFINREQ0061And0065PopulationIsImmutableIDsWithAStableHash(t *testing.T) {
	a, err := filed(t).PopulationHash()
	if err != nil {
		t.Fatal(err)
	}
	r := filed(t)
	r.Population = []string{"dec-1", "dec-2"}
	b, _ := r.PopulationHash()
	if !a.Equal(b) {
		t.Fatal("the population hash depends on order")
	}
}

func TestFINREQ0066RemittanceDerivesFromAnApprovedReturn(t *testing.T) {
	draft := filed(t)
	draft.Status = settlement.ReturnDraft
	if _, err := settlement.Instruct(draft, nil); err == nil {
		t.Fatal("a draft return instructed a remittance")
	}
	r, err := settlement.Instruct(filed(t), nil)
	if err != nil || r.Amount.String() != "1210.00" {
		t.Fatalf("instruct %v %+v", err, r)
	}
}

func TestFINREQ0069PartialRemittanceAndOffsets(t *testing.T) {
	r, err := settlement.Instruct(filed(t), []settlement.Offset{{Reference: "credit:xa-2026-q4", Amount: eur(t, "210.00")}})
	if err != nil || r.Amount.String() != "1000.00" {
		t.Fatalf("offset remittance %v %+v", err, r.Amount)
	}
}

func TestFINREQ0068And0070UncertainRemittanceNeverClearsAndEvidenceIsComplete(t *testing.T) {
	r, _ := settlement.Instruct(filed(t), nil)
	r.Status = settlement.RemittanceUncertain
	if r.Clears() {
		t.Fatal("an uncertain remittance cleared")
	}
	if _, err := r.Confirm(settlement.Evidence{Authority: "authority:xa-revenue", Amount: eur(t, "1210.00"), Date: paid}); err == nil {
		t.Fatal("evidence with no reference confirmed")
	}
	c, err := r.Confirm(settlement.Evidence{Authority: "authority:xa-revenue", Amount: eur(t, "1210.00"), Date: paid, Reference: "XA-PAY-77"})
	if err != nil || !c.Clears() {
		t.Fatalf("confirm %v clears=%t", err, c.Clears())
	}
}
