package subledger

import (
	"fmt"
	"sort"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// Period close (FIN-001 §20–§22).
//
// A legal period of the subledger — a calendar month, "2026-09", per legal
// entity — moves through PeriodState. Its history is a sequence of events,
// never an edited status: a period that was closed, reopened and closed again
// says so. A hard close seals the period's population in a close manifest
// (ZTAX-FIN-REQ-0089); after it nothing posts by the normal path
// (ZTAX-FIN-REQ-0104), and a late event posts only inside an explicit
// amendment window, marked as an amendment (ZTAX-FIN-REQ-0090). Reopening
// takes a request and someone else's approval, and leaves every earlier
// manifest in place (ZTAX-FIN-REQ-0091).

// ParseLegalPeriod validates a legal period: a month, YYYY-MM.
func ParseLegalPeriod(s string) (string, error) {
	t, err := time.Parse("2006-01", s)
	if err != nil || t.Format("2006-01") != s {
		return "", fmt.Errorf("subledger: %q is not a legal period; a period is a month, YYYY-MM", s)
	}
	return s, nil
}

// Valid reports whether s is a known state.
func (s PeriodState) Valid() bool {
	switch s {
	case PeriodOpen, PeriodSoftClose, PeriodHardClose, PeriodReopened, PeriodAmendmentActive, PeriodSealed:
		return true
	}
	return false
}

// CanMoveTo reports whether a period may move from s to next by an
// operator's transition. REOPENED is not reachable this way: it takes an
// approved reopen request (CanReopen).
func (s PeriodState) CanMoveTo(next PeriodState) bool {
	switch s {
	case PeriodOpen, PeriodReopened:
		return next == PeriodSoftClose || next == PeriodHardClose
	case PeriodSoftClose:
		return next == PeriodOpen || next == PeriodHardClose
	case PeriodHardClose:
		return next == PeriodAmendmentActive || next == PeriodSealed
	case PeriodAmendmentActive:
		return next == PeriodHardClose
	}
	return false
}

// CanReopen reports whether a period in s may be reopened by an approved
// request. A sealed period is final.
func (s PeriodState) CanReopen() bool { return s == PeriodHardClose }

// Closes reports whether moving into s writes a close manifest: the hard
// close itself, and the return to it after an amendment window, whose
// postings the new manifest must include.
func (s PeriodState) Closes() bool { return s == PeriodHardClose }

// PeriodEvent is one entry in a legal period's history. A period with no
// events is OPEN.
type PeriodEvent struct {
	LegalEntity id.LegalEntityID
	Period      string
	Seq         int
	State       PeriodState
	Reason      string
	RecordedAt  time.Time
	RecordedBy  id.UserID
	// ApprovedBy and Request are set on a REOPENED event: who approved the
	// reopen, and the request they approved.
	ApprovedBy id.UserID
	Request    string
	// Manifest is the close manifest a closing event wrote.
	Manifest canonical.Digest
}

// ManifestBalance is one control balance in a close manifest.
type ManifestBalance struct {
	Account  Account
	Currency fiscal.Currency
	Debits   fiscal.Money
	Credits  fiscal.Money
}

// ManifestDocument is one fiscal document with a tax point in the period, and
// where it stood at close.
type ManifestDocument struct {
	Document id.FiscalDocumentID
	Type     string
	Status   string
}

// ManifestException is an item still open at close (ZTAX-FIN-REQ-0092): a
// refund whose money has not been confirmed either way, say.
type ManifestException struct {
	Kind   string
	Ref    string
	Status string
}

// CloseManifest is the sealed population of a closed period
// (ZTAX-FIN-REQ-0089, -0093): the journals posted into it, the balances they
// sum to, the documents dated in it, and what was still open.
type CloseManifest struct {
	TenantID    id.TenantID
	LegalEntity id.LegalEntityID
	Period      string
	Journals    []id.JournalID
	Balances    []ManifestBalance
	Documents   []ManifestDocument
	Exceptions  []ManifestException
	ClosedAt    time.Time
	ClosedBy    id.UserID
	// Supersedes is the manifest of the period's previous close, kept and
	// named rather than replaced (ZTAX-FIN-REQ-0091).
	Supersedes canonical.Digest
}

// Seal orders the manifest's contents and digests it. The digest is the
// manifest's identity: the period's closing event names it.
func (m CloseManifest) Seal() (CloseManifest, canonical.Digest, []byte, error) {
	if m.TenantID.IsZero() || m.LegalEntity.IsZero() || m.Period == "" || m.ClosedAt.IsZero() {
		return CloseManifest{}, canonical.Digest{}, nil, fmt.Errorf("subledger: a close manifest is scoped and dated")
	}
	out := m
	out.Journals = append([]id.JournalID(nil), m.Journals...)
	sort.Slice(out.Journals, func(i, j int) bool { return out.Journals[i].String() < out.Journals[j].String() })
	out.Balances = append([]ManifestBalance(nil), m.Balances...)
	sort.Slice(out.Balances, func(i, j int) bool {
		if out.Balances[i].Account != out.Balances[j].Account {
			return out.Balances[i].Account < out.Balances[j].Account
		}
		return out.Balances[i].Currency < out.Balances[j].Currency
	})
	out.Documents = append([]ManifestDocument(nil), m.Documents...)
	sort.Slice(out.Documents, func(i, j int) bool { return out.Documents[i].Document.String() < out.Documents[j].Document.String() })
	out.Exceptions = append([]ManifestException(nil), m.Exceptions...)
	sort.Slice(out.Exceptions, func(i, j int) bool {
		if out.Exceptions[i].Kind != out.Exceptions[j].Kind {
			return out.Exceptions[i].Kind < out.Exceptions[j].Kind
		}
		return out.Exceptions[i].Ref < out.Exceptions[j].Ref
	})
	out.ClosedAt = m.ClosedAt.UTC()
	v := out.Canonical()
	body, err := canonical.Encode(v)
	if err != nil {
		return CloseManifest{}, canonical.Digest{}, nil, err
	}
	return out, canonical.SumBytes(body), body, nil
}

// Canonical renders the manifest.
func (m CloseManifest) Canonical() canonical.Value {
	journals := make([]canonical.Value, len(m.Journals))
	for i, j := range m.Journals {
		journals[i] = canonical.String(j.String())
	}
	balances := make([]canonical.Value, len(m.Balances))
	for i, b := range m.Balances {
		balances[i] = canonical.Object(
			canonical.F("account", canonical.String(string(b.Account))),
			canonical.F("currency", canonical.String(string(b.Currency))),
			canonical.F("debits", canonical.Money(b.Debits)),
			canonical.F("credits", canonical.Money(b.Credits)),
		)
	}
	docs := make([]canonical.Value, len(m.Documents))
	for i, d := range m.Documents {
		docs[i] = canonical.Object(
			canonical.F("documentId", canonical.String(d.Document.String())),
			canonical.F("type", canonical.String(d.Type)),
			canonical.F("status", canonical.String(d.Status)),
		)
	}
	exceptions := make([]canonical.Value, len(m.Exceptions))
	for i, e := range m.Exceptions {
		exceptions[i] = canonical.Object(
			canonical.F("kind", canonical.String(e.Kind)),
			canonical.F("ref", canonical.String(e.Ref)),
			canonical.F("status", canonical.String(e.Status)),
		)
	}
	supersedes := canonical.Absent()
	if !m.Supersedes.IsZero() {
		supersedes = canonical.String(m.Supersedes.String())
	}
	closedBy := canonical.Absent()
	if !m.ClosedBy.IsZero() {
		closedBy = canonical.String(m.ClosedBy.String())
	}
	return canonical.Object(
		canonical.F("artifact", canonical.String("tcsl-close-manifest")),
		canonical.F("tenantId", canonical.String(m.TenantID.String())),
		canonical.F("legalEntityId", canonical.String(m.LegalEntity.String())),
		canonical.F("period", canonical.String(m.Period)),
		canonical.F("journals", canonical.Array(journals...)),
		canonical.F("balances", canonical.Array(balances...)),
		canonical.F("documents", canonical.Array(docs...)),
		canonical.F("openExceptions", canonical.Array(exceptions...)),
		canonical.F("closedAt", canonical.Time(m.ClosedAt)),
		canonical.F("closedBy", closedBy),
		canonical.F("supersedes", supersedes),
	)
}

// PostableAs returns the journal as it posts into a period in state: marked
// an amendment inside an amendment window, which is the only route a late
// event has into a closed period (ZTAX-FIN-REQ-0090) — and checked.
func PostableAs(j Journal, state PeriodState) (Journal, error) {
	if state == PeriodAmendmentActive {
		j.Amendment = true
	}
	return j, CheckPostable(j, state)
}
