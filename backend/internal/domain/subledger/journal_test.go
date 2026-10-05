package subledger_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/subledger"
	"github.com/zoikogroup/zoikotax/backend/internal/fiscaltest"
)

var (
	tenant = id.NewTenantID(uuid.MustParse("00000000-0000-7000-8000-000000000001"))
	entity = id.NewLegalEntityID(uuid.MustParse("00000000-0000-7000-8000-000000000002"))
	owner  = id.NewUserID(uuid.MustParse("00000000-0000-7000-8000-000000000004"))
	posted = time.Date(2027, 3, 31, 0, 0, 0, 0, time.UTC)
)

func jid(n byte) id.JournalID {
	return id.NewJournalID(uuid.MustParse("00000000-0000-7000-8000-0000000003" + string([]byte{'0' + n/10, '0' + n%10})))
}

func eur(t *testing.T, s string) fiscal.Money { return fiscaltest.Money(t, s, "EUR") }

func invoiceJournal(t *testing.T) subledger.Journal {
	return subledger.Journal{
		ID: jid(1), TenantID: tenant, LegalEntity: entity, Type: subledger.JournalInvoice,
		Source: subledger.SourceEvent{Kind: "DOCUMENT_COMMITTED", ID: "doc-1"}, PostingDate: posted, LegalPeriod: "2027-03",
		Currency: "EUR",
		Lines: []subledger.JournalLine{
			{Account: subledger.AccountTaxReceivableControl, Side: subledger.Debit, Amount: eur(t, "21.00")},
			{Account: subledger.AccountTaxCollectedLiability, Side: subledger.Credit, Amount: eur(t, "21.00")},
		},
	}
}

func TestFINREQ0035And0043And0096UnbalancedJournalIsBlocked(t *testing.T) {
	if err := invoiceJournal(t).Validate(); err != nil {
		t.Fatalf("a balanced journal was refused: %v", err)
	}
	j := invoiceJournal(t)
	j.Lines[1].Amount = eur(t, "20.99")
	if err := j.Validate(); err == nil {
		t.Fatal("a journal one cent out of balance validated")
	}
}

func TestFINREQ0039To0042JournalCarriesScopeCurrencySourceAndAccounts(t *testing.T) {
	for name, mutate := range map[string]func(*subledger.Journal){
		"no legal entity":   func(j *subledger.Journal) { j.LegalEntity = id.LegalEntityID{} },
		"mixed currency":    func(j *subledger.Journal) { j.Lines[1].Amount = fiscaltest.Money(t, "21.00", "USD") },
		"no source event":   func(j *subledger.Journal) { j.Source = subledger.SourceEvent{} },
		"non-TCSL account":  func(j *subledger.Journal) { j.Lines[0].Account = "REVENUE" },
		"customer GL code":  func(j *subledger.Journal) { j.Lines[0].Account = "4000-SALES" },
		"no legal period":   func(j *subledger.Journal) { j.LegalPeriod = "" },
		"no tenant":         func(j *subledger.Journal) { j.TenantID = id.TenantID{} },
		"single-line entry": func(j *subledger.Journal) { j.Lines = j.Lines[:1] },
	} {
		j := invoiceJournal(t)
		mutate(&j)
		if err := j.Validate(); err == nil {
			t.Errorf("%s: journal validated", name)
		}
	}
}

func TestFINREQ0044And0108SignSemanticsAreUnambiguous(t *testing.T) {
	j := invoiceJournal(t)
	j.Lines[0].Amount = eur(t, "-21.00")
	j.Lines[1].Amount = eur(t, "-21.00")
	if err := j.Validate(); err == nil {
		t.Fatal("negative journal amounts were accepted; the side carries direction")
	}
}

func TestFINREQ0036To0038CorrectionIsAReversalNotAnEdit(t *testing.T) {
	j := invoiceJournal(t)
	r, err := j.Reverse(jid(2), posted.AddDate(0, 0, 1), subledger.SourceEvent{Kind: "DOCUMENT_VOIDED", ID: "doc-2"})
	if err != nil {
		t.Fatal(err)
	}
	if r.ReversalOf == nil || *r.ReversalOf != j.ID || r.Lines[0].Side != subledger.Credit || j.Lines[0].Side != subledger.Debit {
		t.Fatalf("reversal %+v", r)
	}
	bal, err := subledger.Balances([]subledger.Journal{j, r})
	if err != nil {
		t.Fatal(err)
	}
	for k, b := range bal {
		if c, _ := b.Debits.Cmp(b.Credits); c != 0 {
			t.Errorf("%v does not net to zero after reversal", k)
		}
	}
}

func TestFINREQ0047To0050AccountTaxonomyCoversTheControls(t *testing.T) {
	for _, a := range []subledger.Account{
		subledger.AccountTaxCollectedLiability, subledger.AccountTaxAccruedLiability, subledger.AccountTaxRecoverable,
		subledger.AccountTaxReturnClearing, subledger.AccountTaxRemittanceClearing, subledger.AccountFXControl,
		subledger.AccountRoundingControl, subledger.AccountTaxAdjustmentControl,
	} {
		if !a.Canonical() {
			t.Errorf("%s is not a TCSL account", a)
		}
	}
}

func TestFINREQ0046SuspenseHasOwnerAndAge(t *testing.T) {
	s := subledger.SuspenseItem{Journal: jid(1), OpenedAt: posted, SLA: 72 * time.Hour}
	if err := s.Validate(); err == nil {
		t.Fatal("unowned suspense was accepted")
	}
	s.Owner = owner
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	if s.Aged(posted.Add(48*time.Hour)) || !s.Aged(posted.Add(96*time.Hour)) {
		t.Fatal("suspense aging is wrong")
	}
}

func profile(t *testing.T) subledger.PostingProfile {
	return subledger.PostingProfile{
		Ref: subledger.ProfileRef{ID: "tcsl-default", Version: "2027.1"}, TenantID: tenant, LegalEntity: entity,
		EffectiveFrom: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), Owner: owner,
		Rules: map[string]subledger.PostingProfileEntry{"DOCUMENT_COMMITTED": {Type: subledger.JournalInvoice, Lines: []subledger.PostingRule{
			{Account: subledger.AccountTaxReceivableControl, Side: subledger.Debit, Amount: "TAX"},
			{Account: subledger.AccountTaxCollectedLiability, Side: subledger.Credit, Amount: "TAX"},
		}}},
	}
}

func TestFINREQ0052PostingProfileIsVersionedAndEffectiveDated(t *testing.T) {
	p := profile(t)
	ev := subledger.PostingEvent{Source: subledger.SourceEvent{Kind: "DOCUMENT_COMMITTED", ID: "doc-1"}, EventTime: posted,
		LegalPeriod: "2027-03", Currency: "EUR", Amounts: map[string]fiscal.Money{"TAX": eur(t, "-21.00")}}
	j, err := p.Post(ev, jid(3), posted)
	if err != nil {
		t.Fatal(err)
	}
	// A credit (negative tax) posts its magnitude on the opposite sides.
	if j.Profile.Version != "2027.1" || j.Lines[0].Side != subledger.Credit || j.Lines[0].Amount.String() != "21.00" {
		t.Fatalf("journal %+v", j)
	}
	ev.EventTime = time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)
	if _, err := p.Post(ev, jid(4), posted); err == nil {
		t.Fatal("a profile posted an event from before it took effect")
	}
}

func TestFINREQ0051And0053And0054GLExportIsLayeredTraceableAndNeverInventsAccounts(t *testing.T) {
	m := subledger.GLMapping{Profile: profile(t).Ref, Accounts: map[subledger.Account]string{
		subledger.AccountTaxReceivableControl: "1210", subledger.AccountTaxCollectedLiability: "2310",
	}}
	lines, err := m.Export(invoiceJournal(t))
	if err != nil {
		t.Fatal(err)
	}
	if lines[0].CustomerAccount != "1210" || lines[0].Canonical != subledger.AccountTaxReceivableControl || lines[0].Journal != jid(1) {
		t.Fatalf("export %+v", lines[0])
	}
	delete(m.Accounts, subledger.AccountTaxCollectedLiability)
	if _, err := m.Export(invoiceJournal(t)); err == nil {
		t.Fatal("an export to an unmapped account succeeded")
	}
}

func TestFINREQ0088And0104HardClosedPeriodBlocksNormalPosting(t *testing.T) {
	j := invoiceJournal(t)
	for state, allowed := range map[subledger.PeriodState]bool{
		subledger.PeriodOpen: true, subledger.PeriodSoftClose: false, subledger.PeriodHardClose: false,
		subledger.PeriodSealed: false, subledger.PeriodAmendmentActive: false, subledger.PeriodReopened: true,
	} {
		if err := subledger.CheckPostable(j, state); (err == nil) != allowed {
			t.Errorf("%s: posting allowed=%t", state, err == nil)
		}
	}
	j.Amendment = true
	if err := subledger.CheckPostable(j, subledger.PeriodAmendmentActive); err != nil {
		t.Fatalf("an amendment journal was refused by an active amendment: %v", err)
	}
}

func TestFINREQ0099OrphanJournalsAreDetectable(t *testing.T) {
	j := invoiceJournal(t)
	orphans := subledger.Orphans([]subledger.Journal{j}, map[subledger.SourceEvent]bool{})
	if len(orphans) != 1 || orphans[0] != j.ID {
		t.Fatalf("orphans %v", orphans)
	}
}

func TestFINREQ0114MigrationJournalsCarryProvenance(t *testing.T) {
	j := invoiceJournal(t)
	j.Type = subledger.JournalMigration
	if err := j.Validate(); err == nil {
		t.Fatal("a migration journal with no source provenance validated")
	}
	cut := posted
	j.MigrationSource, j.Cutover = "legacy-ledger:export-2027-03-31", &cut
	if err := j.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestFINREQ0121ControlBalancesReconcileToLines(t *testing.T) {
	bal, err := subledger.Balances([]subledger.Journal{invoiceJournal(t), invoiceJournal(t)})
	if err != nil {
		t.Fatal(err)
	}
	b := bal[subledger.BalanceKey{Account: subledger.AccountTaxCollectedLiability, Currency: "EUR"}]
	if b.Credits.String() != "42.00" || !b.Debits.IsZero() {
		t.Fatalf("liability balance %+v", b)
	}
}
