package erp_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/erp"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/reconciliation"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/subledger"
)

var (
	tenant   = id.NewTenantID(uuid.MustParse("01920000-0000-7000-8000-0000000000aa"))
	entity   = id.NewLegalEntityID(uuid.MustParse("01920000-0000-7000-8000-00000000e001"))
	maker    = id.NewUserID(uuid.MustParse("01920000-0000-7000-8000-0000000000b1"))
	checker  = id.NewUserID(uuid.MustParse("01920000-0000-7000-8000-0000000000b2"))
	bridgeID = id.NewERPBridgeID(uuid.MustParse("01920000-0000-7000-8000-0000000000c1"))
	at       = time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)
)

func usd(t *testing.T, s string) fiscal.Money {
	t.Helper()
	m, err := fiscal.ParseMoney(s, "USD")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func uid(n int) uuid.UUID { return uuid.MustParse(fmt.Sprintf("01920000-0000-7000-8000-%012d", n)) }

// journal is a balanced invoice journal: the customer GL bridge debited and
// the collected liability credited by the tax.
func journal(t *testing.T, n int, tax string) subledger.Journal {
	t.Helper()
	j := subledger.Journal{
		ID: id.NewJournalID(uid(n)), TenantID: tenant, LegalEntity: entity, Type: subledger.JournalInvoice,
		Source: subledger.SourceEvent{Kind: "DECISION_COMMITTED", ID: fmt.Sprint(n)}, PostingDate: at, LegalPeriod: "2026-09",
		Currency: "USD", Profile: subledger.ProfileRef{ID: "p", Version: "1"},
		Lines: []subledger.JournalLine{
			{Account: subledger.AccountCustomerGLBridge, Side: subledger.Debit, Amount: usd(t, tax)},
			{Account: subledger.AccountTaxCollectedLiability, Side: subledger.Credit, Amount: usd(t, tax), Authority: "US-NY-DTF", Jurisdiction: "US-NY"},
		},
	}
	if err := j.Validate(); err != nil {
		t.Fatal(err)
	}
	return j
}

func mapping(t *testing.T, version int, accounts ...erp.AccountMap) erp.Mapping {
	t.Helper()
	if accounts == nil {
		accounts = []erp.AccountMap{
			{Account: subledger.AccountTaxCollectedLiability, GLAccount: "2310-00"},
			{Account: subledger.AccountCustomerGLBridge, GLAccount: "1990-00"},
		}
	}
	m := erp.Mapping{Version: version, ChartVersion: "COA-2026.2", Accounts: accounts, RecordedAt: at, RecordedBy: maker}
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	m, err := m.Seal(bridgeID)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func bridge(t *testing.T, mappings ...erp.Mapping) erp.Bridge {
	t.Helper()
	b := erp.Bridge{ID: bridgeID, TenantID: tenant, LegalEntity: entity, Name: "SAP", Vendor: erp.VendorSAP, CompanyCode: "AC01",
		CreatedAt: at, CreatedBy: maker, Mappings: mappings}
	if err := b.Validate(); err != nil {
		t.Fatal(err)
	}
	return b
}

func prepare(t *testing.T, n int, m erp.Mapping, journals ...subledger.Journal) erp.Batch {
	t.Helper()
	b, err := erp.Prepare(erp.PrepareInput{
		ID: id.NewERPExportID(uid(1000 + n)), TenantID: tenant, Bridge: bridge(t, m), Mapping: m, Period: "2026-09",
		Currency: "USD", PostingDate: time.Date(2026, 10, 3, 15, 0, 0, 0, time.UTC), Journals: journals, PreparedAt: at, PreparedBy: maker,
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err = erp.Fold(b, []erp.Event{erp.Prepared(maker, at)})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// apply appends an event and folds again, as the store does.
func apply(t *testing.T, b erp.Batch, e erp.Event) erp.Batch {
	t.Helper()
	out, err := erp.Fold(b, append(append([]erp.Event{}, b.History...), e))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func dispatch(t *testing.T, b erp.Batch, attempt int) erp.Batch {
	t.Helper()
	e, ok, err := b.Dispatch(erp.DispatchInput{ExpectedDigest: b.Digest.String(), CurrentMapping: b.MappingVersion,
		Attempt: id.NewERPAttemptID(uid(2000 + attempt)), By: checker, At: at})
	if err != nil || !ok {
		t.Fatalf("dispatch: %v %v", ok, err)
	}
	return apply(t, b, e)
}

func respond(t *testing.T, b erp.Batch, r erp.Response) erp.Batch {
	t.Helper()
	e, ok, err := b.Respond(r, maker, at)
	if err != nil || !ok {
		t.Fatalf("respond %s: %v %v", r.Kind, ok, err)
	}
	return apply(t, b, e)
}

func TestAMappingDigestDoesNotDependOnOrder(t *testing.T) {
	a := mapping(t, 1)
	b := mapping(t, 1,
		erp.AccountMap{Account: subledger.AccountCustomerGLBridge, GLAccount: "1990-00"},
		erp.AccountMap{Account: subledger.AccountTaxCollectedLiability, GLAccount: "2310-00"})
	if !a.Digest.Equal(b.Digest) {
		t.Fatalf("%s != %s", a.Digest, b.Digest)
	}
	if a.Accounts[0].Account != subledger.AccountCustomerGLBridge {
		t.Fatalf("accounts are not sorted: %v", a.Accounts)
	}
	for name, bad := range map[string][]erp.AccountMap{
		"not a TCSL account": {{Account: "REVENUE_GL", GLAccount: "4000"}},
		"mapped twice": {{Account: subledger.AccountFXControl, GLAccount: "1"},
			{Account: subledger.AccountFXControl, GLAccount: "2"}},
		"bad GL code": {{Account: subledger.AccountFXControl, GLAccount: "has space"}},
	} {
		m := erp.Mapping{Version: 1, ChartVersion: "C", Accounts: bad}
		if err := m.Validate(); !errs.IsCategory(err, errs.CategoryValidation) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// QA-LED-067: preparing seals the exact journal ID set, every mapped line
// and the mapping version under one digest.
func TestPrepareSealsTheJournalSetAndMapping(t *testing.T) {
	m := mapping(t, 1)
	b := prepare(t, 1, m, journal(t, 2, "7.50"), journal(t, 1, "10.00"))
	if len(b.Journals) != 2 || b.Journals[0] != id.NewJournalID(uid(1)) {
		t.Fatalf("journals %v are not the sorted set", b.Journals)
	}
	if len(b.Lines) != 4 || b.Lines[1].GLAccount != "2310-00" || b.Lines[1].JournalLine != 2 {
		t.Fatalf("lines %+v", b.Lines)
	}
	if b.TotalDebits.CanonicalString() != "17.50" || b.TotalCredits.CanonicalString() != "17.50" {
		t.Fatalf("totals %s/%s", b.TotalDebits.CanonicalString(), b.TotalCredits.CanonicalString())
	}
	if b.MappingVersion != 1 || !b.MappingDigest.Equal(m.Digest) || b.CompanyCode != "AC01" {
		t.Fatalf("export mapping %d %s %s", b.MappingVersion, b.MappingDigest, b.CompanyCode)
	}
	if b.PostingDate != time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC) {
		t.Fatalf("posting date %s", b.PostingDate)
	}
	if err := b.Verify(); err != nil {
		t.Fatal(err)
	}
	tampered := b
	tampered.Lines = append([]erp.Line{}, b.Lines...)
	tampered.Lines[0].Amount = usd(t, "7.51")
	if tampered.Verify() == nil {
		t.Fatal("a changed line still verifies against the sealed digest")
	}
	if b.ExternalRef() != "ztax-erp-export:"+b.ID.String() {
		t.Fatalf("external ref %s", b.ExternalRef())
	}
}

// LED-03 "Missing mapping": a line on an unmapped account holds the whole
// export; nothing goes to suspense.
func TestAnUnmappedAccountHoldsTheExport(t *testing.T) {
	m := mapping(t, 1, erp.AccountMap{Account: subledger.AccountTaxCollectedLiability, GLAccount: "2310-00"})
	_, err := erp.Prepare(erp.PrepareInput{ID: id.NewERPExportID(uid(1001)), TenantID: tenant, Bridge: bridge(t, m), Mapping: m,
		Period: "2026-09", Currency: "USD", PostingDate: at, Journals: []subledger.Journal{journal(t, 1, "10.00")}, PreparedAt: at, PreparedBy: maker})
	if errs.ReasonOf(err) != erp.ReasonMappingIncomplete {
		t.Fatalf("unmapped account: %v", err)
	}
	_, err = erp.Prepare(erp.PrepareInput{ID: id.NewERPExportID(uid(1001)), TenantID: tenant, Bridge: bridge(t, m), Mapping: m,
		Period: "2026-09", Currency: "USD", PostingDate: at, PreparedAt: at, PreparedBy: maker})
	if errs.ReasonOf(err) != erp.ReasonNothingToExport {
		t.Fatalf("no journals: %v", err)
	}
}

// QA-LED-068 and four eyes: the checker names the digest they reviewed, is
// not the preparer, and the mapping is still current.
func TestDispatchIsAuthorisedOnTheSealedBatch(t *testing.T) {
	b := prepare(t, 1, mapping(t, 1), journal(t, 1, "10.00"))
	attempt := id.NewERPAttemptID(uid(2001))
	in := erp.DispatchInput{ExpectedDigest: b.Digest.String(), CurrentMapping: 1, Attempt: attempt, By: maker, At: at}
	if _, _, err := b.Dispatch(in); errs.ReasonOf(err) != erp.ReasonMakerChecker {
		t.Fatalf("the preparer dispatched: %v", err)
	}
	in.By = checker
	in.ExpectedDigest = "zt1:" + fmt.Sprintf("%064d", 0)
	if _, _, err := b.Dispatch(in); errs.ReasonOf(err) != erp.ReasonBatchDigestMismatch {
		t.Fatalf("a different digest: %v", err)
	}
	in.ExpectedDigest = b.Digest.String()
	in.CurrentMapping = 2
	if _, _, err := b.Dispatch(in); errs.ReasonOf(err) != erp.ReasonMappingChanged {
		t.Fatalf("a superseded mapping: %v", err)
	}
	in.CurrentMapping = 1
	e, ok, err := b.Dispatch(in)
	if err != nil || !ok || e.Kind != erp.EventDispatched || e.Status != erp.StatusTransmitted || e.Attempt != attempt {
		t.Fatalf("dispatch: %+v %v %v", e, ok, err)
	}
}

// QA-LED-069: the same batch retried is idempotent — no second attempt, so
// no second GL journal.
func TestARetriedDispatchIsNoNewAttempt(t *testing.T) {
	b := dispatch(t, prepare(t, 1, mapping(t, 1), journal(t, 1, "10.00")), 1)
	for _, s := range []erp.Status{erp.StatusTransmitted, erp.StatusAcknowledged, erp.StatusERPPosted} {
		if b.Status != s {
			t.Fatalf("status %s, want %s", b.Status, s)
		}
		_, ok, err := b.Dispatch(erp.DispatchInput{ExpectedDigest: b.Digest.String(), CurrentMapping: 1,
			Attempt: id.NewERPAttemptID(uid(2999)), By: checker, At: at})
		if err != nil || ok {
			t.Fatalf("retry from %s: dispatched %v, %v", s, ok, err)
		}
		switch s {
		case erp.StatusTransmitted:
			b = respond(t, b, erp.Response{Kind: erp.EventAcknowledged, Attempt: id.NewERPAttemptID(uid(2001)), RemoteCorrelation: "MSG-1"})
		case erp.StatusAcknowledged:
			b = respond(t, b, erp.Response{Kind: erp.EventPosted, Attempt: id.NewERPAttemptID(uid(2001)), ERPReference: "AC01/2026/1"})
		}
	}
	if len(b.Attempts()) != 1 {
		t.Fatalf("%d attempts", len(b.Attempts()))
	}
}

// LED-23: transport receipt is kept apart from the GL posting.
func TestAReceiptIsNotAPosting(t *testing.T) {
	b := dispatch(t, prepare(t, 1, mapping(t, 1), journal(t, 1, "10.00")), 1)
	attempt := id.NewERPAttemptID(uid(2001))
	if _, _, err := b.Respond(erp.Response{Kind: erp.EventAcknowledged, Attempt: attempt}, maker, at); !errs.IsCategory(err, errs.CategoryValidation) {
		t.Fatalf("a receipt with no correlation: %v", err)
	}
	b = respond(t, b, erp.Response{Kind: erp.EventAcknowledged, Attempt: attempt, RemoteCorrelation: "MSG-1"})
	if b.Status != erp.StatusAcknowledged {
		t.Fatalf("status %s", b.Status)
	}
	if _, _, err := b.Respond(erp.Response{Kind: erp.EventPosted, Attempt: attempt}, maker, at); !errs.IsCategory(err, errs.CategoryValidation) {
		t.Fatalf("a posting with no ERP reference: %v", err)
	}
	if _, _, err := b.Respond(erp.Response{Kind: erp.EventPosted, Attempt: id.NewERPAttemptID(uid(2999)), ERPReference: "X"}, maker, at); !errs.IsCategory(err, errs.CategoryNotFound) {
		t.Fatalf("an attempt never made: %v", err)
	}
	posted := erp.Response{Kind: erp.EventPosted, Attempt: attempt, ERPReference: "AC01/2026/1900004411", ResponseDigest: "sha256:ab"}
	b = respond(t, b, posted)
	if b.Status != erp.StatusERPPosted {
		t.Fatalf("status %s", b.Status)
	}
	if _, ok, err := b.Respond(posted, maker, at); err != nil || ok {
		t.Fatalf("the same posting again: %v %v", ok, err)
	}
	if _, _, err := b.Respond(erp.Response{Kind: erp.EventRejected, Attempt: attempt, Detail: "late"}, maker, at); errs.ReasonOf(err) != errs.ReasonStateTransitionInvalid {
		t.Fatalf("a rejection after posting: %v", err)
	}
}

// LED-23 "External status unknown": never send again without a status
// query that found nothing.
func TestAnUnknownOutcomeIsNotResentBlind(t *testing.T) {
	b := dispatch(t, prepare(t, 1, mapping(t, 1), journal(t, 1, "10.00")), 1)
	first := id.NewERPAttemptID(uid(2001))
	b = respond(t, b, erp.Response{Kind: erp.EventOutcomeUnknown, Attempt: first, Detail: "gateway timeout after 30s"})
	retry := erp.DispatchInput{ExpectedDigest: b.Digest.String(), CurrentMapping: 1, Attempt: id.NewERPAttemptID(uid(2002)), By: checker, At: at}
	if _, _, err := b.Dispatch(retry); errs.ReasonOf(err) != erp.ReasonOutcomeUnknown {
		t.Fatalf("a blind resend: %v", err)
	}
	if _, _, err := b.Respond(erp.Response{Kind: erp.EventStatusNotFound, Attempt: first}, maker, at); !errs.IsCategory(err, errs.CategoryValidation) {
		t.Fatalf("a status query with no correlation: %v", err)
	}
	b = respond(t, b, erp.Response{Kind: erp.EventStatusNotFound, Attempt: first, RemoteCorrelation: "QUERY-77"})
	e, ok, err := b.Dispatch(retry)
	if err != nil || !ok {
		t.Fatalf("resend after the query: %v %v", ok, err)
	}
	b = apply(t, b, e)
	if b.Status != erp.StatusTransmitted || len(b.Attempts()) != 2 {
		t.Fatalf("status %s, %d attempts", b.Status, len(b.Attempts()))
	}
	// The first attempt may still turn out to have posted; that is recorded
	// against it, and the batch is posted once.
	b = respond(t, b, erp.Response{Kind: erp.EventPosted, Attempt: first, ERPReference: "AC01/2026/1"})
	if b.Status != erp.StatusERPPosted {
		t.Fatalf("status %s", b.Status)
	}
}

// LED-23 "ERP posting failure": a rejection opens a correction task, open
// until the journals are in a batch the ERP posted.
func TestARejectionOpensACorrectionTask(t *testing.T) {
	j := journal(t, 1, "10.00")
	rejected := dispatch(t, prepare(t, 1, mapping(t, 1), j), 1)
	rejected = respond(t, rejected, erp.Response{Kind: erp.EventRejected, Attempt: id.NewERPAttemptID(uid(2001)), Detail: "account blocked"})
	if _, _, err := rejected.Dispatch(erp.DispatchInput{ExpectedDigest: rejected.Digest.String(), CurrentMapping: 1,
		Attempt: id.NewERPAttemptID(uid(2002)), By: checker, At: at}); errs.ReasonOf(err) != errs.ReasonStateTransitionInvalid {
		t.Fatalf("a rejected batch was sent again: %v", err)
	}
	tasks := erp.CorrectionTasks([]erp.Batch{rejected})
	if len(tasks) != 1 || !tasks[0].Open() || tasks[0].Reason != "account blocked" || len(tasks[0].Outstanding) != 1 {
		t.Fatalf("tasks %+v", tasks)
	}
	replacement := dispatch(t, prepare(t, 2, mapping(t, 1), j), 3)
	replacement = respond(t, replacement, erp.Response{Kind: erp.EventPosted, Attempt: id.NewERPAttemptID(uid(2003)), ERPReference: "AC01/2026/2"})
	tasks = erp.CorrectionTasks([]erp.Batch{replacement, rejected})
	if len(tasks) != 1 || tasks[0].Open() || len(tasks[0].PostedBy) != 1 || tasks[0].PostedBy[0] != replacement.ID {
		t.Fatalf("tasks after reposting %+v", tasks)
	}
}

func TestOnlyAPreparedBatchIsWithdrawn(t *testing.T) {
	b := prepare(t, 1, mapping(t, 1), journal(t, 1, "10.00"))
	if _, err := b.Withdraw(" ", maker, at); !errs.IsCategory(err, errs.CategoryValidation) {
		t.Fatalf("no reason: %v", err)
	}
	e, err := b.Withdraw("mapping v2", maker, at)
	if err != nil {
		t.Fatal(err)
	}
	w := apply(t, b, e)
	if w.Status != erp.StatusWithdrawn || w.Status.Live() {
		t.Fatalf("status %s", w.Status)
	}
	sent := dispatch(t, b, 1)
	if _, err := sent.Withdraw("too late", maker, at); errs.ReasonOf(err) != errs.ReasonStateTransitionInvalid {
		t.Fatalf("withdrawing a sent batch: %v", err)
	}
}

func TestFoldRefusesAnImpossibleHistory(t *testing.T) {
	b := prepare(t, 1, mapping(t, 1), journal(t, 1, "10.00"))
	posted := erp.Event{Seq: 2, Kind: erp.EventPosted, Status: erp.StatusERPPosted, Attempt: id.NewERPAttemptID(uid(2001)),
		ERPReference: "X", RecordedAt: at, RecordedBy: maker}
	if _, err := erp.Fold(b, append(b.History, posted)); err == nil {
		t.Fatal("a posting with no dispatch folded")
	}
	if _, err := erp.Fold(b, nil); err == nil {
		t.Fatal("an empty history folded")
	}
}

func snapshot(t *testing.T, lines ...erp.GLBalance) erp.GLSnapshot {
	t.Helper()
	s := erp.GLSnapshot{ID: id.NewGLSnapshotID(uid(3001)), TenantID: tenant, Bridge: bridgeID, Period: "2026-09", Currency: "USD",
		AsOf: at, SourceRef: "FAGLB03 AC01 2026/09", Lines: lines, RecordedAt: at, RecordedBy: maker}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	s, err := s.Seal()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func gl(t *testing.T, account, debits, credits string) erp.GLBalance {
	return erp.GLBalance{GLAccount: account, Debits: usd(t, debits), Credits: usd(t, credits)}
}

func item(t *testing.T, r erp.GLReconciliation, key string) erp.GLItem {
	t.Helper()
	for _, it := range r.Items {
		if string(it.Key) == key {
			return it
		}
	}
	t.Fatalf("no item %s in %+v", key, r.Items)
	return erp.GLItem{}
}

func posted(t *testing.T, journals ...subledger.Journal) erp.Batch {
	t.Helper()
	b := dispatch(t, prepare(t, 1, mapping(t, 1), journals...), 1)
	return respond(t, b, erp.Response{Kind: erp.EventPosted, Attempt: id.NewERPAttemptID(uid(2001)), ERPReference: "AC01/2026/1"})
}

// R6: the subledger ties to the GL account by account, exactly.
func TestTheSubledgerTiesToTheGL(t *testing.T) {
	j1, j2 := journal(t, 1, "10.00"), journal(t, 2, "7.50")
	r, err := erp.Reconcile(erp.ReconcileInput{Mapping: mapping(t, 1), Journals: []subledger.Journal{j1, j2},
		Batches:  []erp.Batch{posted(t, j1, j2)},
		Snapshot: snapshot(t, gl(t, "1990-00", "17.50", "0.00"), gl(t, "2310-00", "0.00", "17.50"))})
	if err != nil {
		t.Fatal(err)
	}
	if r.Outcome != erp.OutcomeReconciled || len(r.Holds) != 0 || r.PostedJournals != 2 || r.Exceptions() != 0 {
		t.Fatalf("outcome %s holds %v posted %d", r.Outcome, r.Holds, r.PostedJournals)
	}
	liability := item(t, r, "gl:2310-00")
	if liability.Expected.CanonicalString() != "-17.50" || liability.Variance.CanonicalString() != "0.00" ||
		liability.TCSLAccounts[0] != subledger.AccountTaxCollectedLiability {
		t.Fatalf("liability %+v", liability)
	}
	if r.PopulationDigest.IsZero() {
		t.Fatal("no population digest")
	}
}

// A variance is exact and kept; an ERP account nothing maps to, and a TCSL
// account with no mapping, are items of their own.
func TestVariancesAreExactAndNothingIsHidden(t *testing.T) {
	j := journal(t, 1, "10.00")
	j.Lines = append(j.Lines,
		subledger.JournalLine{Account: subledger.AccountRoundingControl, Side: subledger.Debit, Amount: usd(t, "0.01")},
		subledger.JournalLine{Account: subledger.AccountTaxCollectedLiability, Side: subledger.Credit, Amount: usd(t, "0.01")})
	r, err := erp.Reconcile(erp.ReconcileInput{Mapping: mapping(t, 1), Journals: []subledger.Journal{j},
		Batches:  []erp.Batch{{Status: erp.StatusERPPosted, Journals: []id.JournalID{j.ID}}},
		Snapshot: snapshot(t, gl(t, "1990-00", "10.00", "0.00"), gl(t, "2310-00", "0.00", "10.00"), gl(t, "9999-99", "1.00", "0.00"))})
	if err != nil {
		t.Fatal(err)
	}
	if r.Outcome != erp.OutcomeVariances {
		t.Fatalf("outcome %s", r.Outcome)
	}
	if it := item(t, r, "gl:2310-00"); it.Variance.CanonicalString() != "0.01" || it.Status != reconciliation.StatusPartial {
		t.Fatalf("liability %s %s", it.Variance.CanonicalString(), it.Status)
	}
	if it := item(t, r, "gl:9999-99"); it.Expected != nil || it.Status != reconciliation.StatusUnmatched {
		t.Fatalf("unexpected GL account %+v", it)
	}
	if it := item(t, r, "tcsl:ROUNDING_CONTROL"); it.Observed != nil || it.Status != reconciliation.StatusMissing ||
		it.Expected.CanonicalString() != "0.01" {
		t.Fatalf("unmapped TCSL account %+v", it)
	}
	if it := item(t, r, "gl:1990-00"); it.Status != reconciliation.StatusMatched {
		t.Fatalf("bridge %s", it.Status)
	}
}

// LED-18 "uses ERP posted ledger IDs, not transport ACK": a period with a
// journal not in a posted batch is HELD, whatever its items say.
func TestAPeriodNotYetPostedIsHeld(t *testing.T) {
	j1, j2, j3, j4 := journal(t, 1, "1.00"), journal(t, 2, "2.00"), journal(t, 3, "3.00"), journal(t, 4, "4.00")
	acked := dispatch(t, prepare(t, 1, mapping(t, 1), j2), 1)
	acked = respond(t, acked, erp.Response{Kind: erp.EventAcknowledged, Attempt: id.NewERPAttemptID(uid(2001)), RemoteCorrelation: "M"})
	rejected := dispatch(t, prepare(t, 2, mapping(t, 1), j3), 2)
	rejected = respond(t, rejected, erp.Response{Kind: erp.EventRejected, Attempt: id.NewERPAttemptID(uid(2002)), Detail: "blocked"})
	waiting := prepare(t, 3, mapping(t, 1), j4)
	r, err := erp.Reconcile(erp.ReconcileInput{Mapping: mapping(t, 1), Journals: []subledger.Journal{j1, j2, j3, j4},
		Batches:  []erp.Batch{acked, rejected, waiting},
		Snapshot: snapshot(t, gl(t, "1990-00", "10.00", "0.00"), gl(t, "2310-00", "0.00", "10.00"))})
	if err != nil {
		t.Fatal(err)
	}
	if r.Outcome != erp.OutcomeHeld || r.PostedJournals != 0 {
		t.Fatalf("outcome %s posted %d", r.Outcome, r.PostedJournals)
	}
	want := []erp.Hold{{Reason: erp.HoldUnexported, Journals: 1}, {Reason: erp.HoldAwaitingDispatch, Journals: 1},
		{Reason: erp.HoldAwaitingPosting, Journals: 1}, {Reason: erp.HoldRejected, Journals: 1}}
	if fmt.Sprint(r.Holds) != fmt.Sprint(want) {
		t.Fatalf("holds %v, want %v", r.Holds, want)
	}
	// Every item tied, and it is still not reconciled.
	if r.Exceptions() != 0 {
		t.Fatalf("%d exceptions", r.Exceptions())
	}
}

func TestASnapshotIsValidated(t *testing.T) {
	for name, s := range map[string]erp.GLSnapshot{
		"no lines": {Period: "2026-09", Currency: "USD", AsOf: at, SourceRef: "R"},
		"twice": {Period: "2026-09", Currency: "USD", AsOf: at, SourceRef: "R",
			Lines: []erp.GLBalance{gl(t, "1", "1", "0"), gl(t, "1", "0", "1")}},
		"no source": {Period: "2026-09", Currency: "USD", AsOf: at, Lines: []erp.GLBalance{gl(t, "1", "1", "0")}},
		"negative": {Period: "2026-09", Currency: "USD", AsOf: at, SourceRef: "R",
			Lines: []erp.GLBalance{{GLAccount: "1", Debits: usd(t, "-1"), Credits: usd(t, "0")}}},
	} {
		if err := s.Validate(); !errs.IsCategory(err, errs.CategoryValidation) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := erp.ParseAmount("debits", "-1.00", "USD"); err == nil {
		t.Error("a negative reported amount parsed")
	}
	if _, err := erp.ParseAmount("debits", "1e3", "USD"); err == nil {
		t.Error("an exponent parsed")
	}
}
