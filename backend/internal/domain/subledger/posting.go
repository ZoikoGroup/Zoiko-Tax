package subledger

import (
	"fmt"
	"sort"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
)

// ProfileRef pins the posting profile version a journal was produced by.
type ProfileRef struct {
	ID      string
	Version string
}

// PostingRule is one line a profile posts for an event: which amount of the
// event, to which account, on which side.
type PostingRule struct {
	Account Account
	Side    Side
	// Amount names one of the event's amounts: "TAX", "NET", "GROSS", or any
	// name the event kind defines.
	Amount string
}

// PostingProfile is FIN-001 §13's versioned, effective-dated mapping from
// fiscal events to TCSL journals (ZTAX-FIN-REQ-0052).
type PostingProfile struct {
	Ref           ProfileRef
	TenantID      id.TenantID
	LegalEntity   id.LegalEntityID
	EffectiveFrom time.Time
	// EffectiveTo is exclusive; nil is open-ended.
	EffectiveTo *time.Time
	// Owner approved the profile.
	Owner id.UserID
	Rules map[string]PostingProfileEntry
}

// PostingProfileEntry is what a profile posts for one event kind.
type PostingProfileEntry struct {
	Type  JournalType
	Lines []PostingRule
}

// PostingEvent is an authoritative fiscal event to post.
type PostingEvent struct {
	Source       SourceEvent
	EventTime    time.Time
	LegalPeriod  string
	Currency     fiscal.Currency
	Amounts      map[string]fiscal.Money
	Authority    string
	Jurisdiction string
	Decision     *id.DecisionID
	Document     *id.FiscalDocumentID
}

// EffectiveAt reports whether the profile governs an event at t.
func (p PostingProfile) EffectiveAt(t time.Time) bool {
	return !t.Before(p.EffectiveFrom) && (p.EffectiveTo == nil || t.Before(*p.EffectiveTo))
}

// Post produces the journal a profile posts for an event. The profile must be
// in effect at the event time, which is what lets a replay post the 2027
// event under the 2027 profile.
func (p PostingProfile) Post(ev PostingEvent, journalID id.JournalID, postingDate time.Time) (Journal, error) {
	if p.Ref.ID == "" || p.Ref.Version == "" || p.Owner.IsZero() {
		return Journal{}, fmt.Errorf("subledger: posting profile is unversioned or unapproved")
	}
	if !p.EffectiveAt(ev.EventTime) {
		return Journal{}, fmt.Errorf("subledger: profile %s@%s is not in effect at %s", p.Ref.ID, p.Ref.Version, ev.EventTime.Format(time.RFC3339))
	}
	entry, ok := p.Rules[ev.Source.Kind]
	if !ok {
		return Journal{}, fmt.Errorf("subledger: profile %s@%s posts nothing for %s events", p.Ref.ID, p.Ref.Version, ev.Source.Kind)
	}
	j := Journal{
		ID: journalID, TenantID: p.TenantID, LegalEntity: p.LegalEntity, Type: entry.Type, Source: ev.Source,
		PostingDate: postingDate.UTC(), LegalPeriod: ev.LegalPeriod, Currency: ev.Currency, Profile: p.Ref,
	}
	for _, r := range entry.Lines {
		amt, ok := ev.Amounts[r.Amount]
		if !ok {
			return Journal{}, fmt.Errorf("subledger: %s event %s carries no %s amount", ev.Source.Kind, ev.Source.ID, r.Amount)
		}
		if amt.IsZero() {
			continue
		}
		side := r.Side
		if amt.Sign() < 0 {
			// A negative event amount — a credit — posts its magnitude on the
			// opposite side, so every posted line stays positive.
			amt = amt.Neg()
			if side == Debit {
				side = Credit
			} else {
				side = Debit
			}
		}
		j.Lines = append(j.Lines, JournalLine{
			Account: r.Account, Side: side, Amount: amt, Authority: ev.Authority,
			Jurisdiction: ev.Jurisdiction, Decision: ev.Decision, Document: ev.Document,
		})
	}
	if err := j.Validate(); err != nil {
		return Journal{}, err
	}
	return j, nil
}

// GLMapping is customer configuration layered over the canonical accounts
// (ZTAX-FIN-REQ-0051): each TCSL account the customer exports maps to one of
// their own account codes.
type GLMapping struct {
	Profile  ProfileRef
	Accounts map[Account]string
}

// GLLine is one exported line, carrying the trace back to the canonical
// journal, document and decision (ZTAX-FIN-REQ-0053).
type GLLine struct {
	CustomerAccount string
	Canonical       Account
	Side            Side
	Amount          fiscal.Money
	Journal         id.JournalID
	Line            int
	Document        *id.FiscalDocumentID
	Decision        *id.DecisionID
}

// Export maps a journal onto the customer's chart. An account with no mapping
// is an error: ZoikoTax never creates or changes a customer account to make an
// export succeed (ZTAX-FIN-REQ-0054).
func (m GLMapping) Export(j Journal) ([]GLLine, error) {
	var unmapped []string
	out := make([]GLLine, 0, len(j.Lines))
	for i, l := range j.Lines {
		code, ok := m.Accounts[l.Account]
		if !ok || code == "" {
			unmapped = append(unmapped, string(l.Account))
			continue
		}
		out = append(out, GLLine{
			CustomerAccount: code, Canonical: l.Account, Side: l.Side, Amount: l.Amount,
			Journal: j.ID, Line: i, Document: l.Document, Decision: l.Decision,
		})
	}
	if len(unmapped) > 0 {
		sort.Strings(unmapped)
		return nil, fmt.Errorf("subledger: the customer GL mapping has no account for %v; nothing was exported", unmapped)
	}
	return out, nil
}
