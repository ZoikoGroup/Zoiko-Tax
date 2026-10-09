package subledger_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/subledger"
	"github.com/zoikogroup/zoikotax/backend/internal/fiscaltest"
)

func TestLegalPeriodIsAMonth(t *testing.T) {
	if _, err := subledger.ParseLegalPeriod("2026-09"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "2026-9", "2026-13", "2026-09-01", "26-09", "2026/09"} {
		if _, err := subledger.ParseLegalPeriod(bad); err == nil {
			t.Errorf("%q is a legal period", bad)
		}
	}
}

func TestFINREQ0088PeriodLifecycle(t *testing.T) {
	ok := map[subledger.PeriodState][]subledger.PeriodState{
		subledger.PeriodOpen:            {subledger.PeriodSoftClose, subledger.PeriodHardClose},
		subledger.PeriodSoftClose:       {subledger.PeriodOpen, subledger.PeriodHardClose},
		subledger.PeriodHardClose:       {subledger.PeriodAmendmentActive, subledger.PeriodSealed},
		subledger.PeriodAmendmentActive: {subledger.PeriodHardClose},
		subledger.PeriodReopened:        {subledger.PeriodSoftClose, subledger.PeriodHardClose},
	}
	all := []subledger.PeriodState{subledger.PeriodOpen, subledger.PeriodSoftClose, subledger.PeriodHardClose,
		subledger.PeriodReopened, subledger.PeriodAmendmentActive, subledger.PeriodSealed}
	for _, from := range all {
		for _, to := range all {
			want := false
			for _, x := range ok[from] {
				want = want || x == to
			}
			if from.CanMoveTo(to) != want {
				t.Errorf("%s -> %s: %v", from, to, !want)
			}
		}
	}
	// Reopening is never a plain transition, and a sealed period is final.
	if subledger.PeriodHardClose.CanMoveTo(subledger.PeriodReopened) || !subledger.PeriodHardClose.CanReopen() || subledger.PeriodSealed.CanReopen() {
		t.Fatal("reopen")
	}
}

// ZTAX-FIN-REQ-0090, -0104: after a hard close a journal posts only inside an
// amendment window, and is marked an amendment there.
func TestFINREQ0090LateEventsPostOnlyAsAmendments(t *testing.T) {
	j := subledger.Journal{Type: subledger.JournalInvoice, LegalPeriod: "2026-09"}
	if _, err := subledger.PostableAs(j, subledger.PeriodHardClose); errs.ReasonOf(err) != errs.ReasonStateTransitionInvalid {
		t.Fatalf("into a hard-closed period: %v", err)
	}
	got, err := subledger.PostableAs(j, subledger.PeriodAmendmentActive)
	if err != nil || !got.Amendment {
		t.Fatalf("inside an amendment window: %v amendment=%v", err, got.Amendment)
	}
	if got, err := subledger.PostableAs(j, subledger.PeriodOpen); err != nil || got.Amendment {
		t.Fatalf("into an open period: %v amendment=%v", err, got.Amendment)
	}
}

// ZTAX-FIN-REQ-0089: the manifest is a function of its population, in any
// order, and changes when the population does.
func TestFINREQ0089TheCloseManifestSealsThePopulation(t *testing.T) {
	jid := func(s string) id.JournalID {
		return id.NewJournalID(uuid.MustParse("00000000-0000-7000-8000-0000000000" + s))
	}
	m := subledger.CloseManifest{
		TenantID:    id.NewTenantID(uuid.MustParse("00000000-0000-7000-8000-000000000001")),
		LegalEntity: id.NewLegalEntityID(uuid.MustParse("00000000-0000-7000-8000-000000000002")),
		Period:      "2026-09", Journals: []id.JournalID{jid("b1"), jid("a1")},
		Balances: []subledger.ManifestBalance{{Account: subledger.AccountTaxCollectedLiability, Currency: "EUR",
			Debits: fiscaltest.Money(t, "0.00", "EUR"), Credits: fiscaltest.Money(t, "21.11", "EUR")}},
		ClosedAt: time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC),
	}
	_, d1, _, err := m.Seal()
	if err != nil {
		t.Fatal(err)
	}
	m.Journals = []id.JournalID{jid("a1"), jid("b1")}
	if _, d2, _, _ := m.Seal(); !d2.Equal(d1) {
		t.Fatal("the manifest's digest depends on the order its population was read in")
	}
	m.Journals = append(m.Journals, jid("c1"))
	if _, d3, _, _ := m.Seal(); d3.Equal(d1) {
		t.Fatal("a journal added to the population left the digest unchanged")
	}
}
