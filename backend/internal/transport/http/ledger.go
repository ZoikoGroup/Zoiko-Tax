package http

import (
	"net/http"
	"sort"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/subledger"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/transport/http/gen"
)

// The subledger and classification surfaces (W2 lanes J, G and L).

func toJournal(j subledger.Journal) gen.Journal {
	out := gen.Journal{
		ID: j.ID.String(), Type: gen.JournalType(j.Type), LegalEntityID: j.LegalEntity.String(),
		SourceKind: j.Source.Kind, SourceID: j.Source.ID, PostingDate: canonical.FormatTime(j.PostingDate),
		LegalPeriod: j.LegalPeriod, Currency: string(j.Currency),
		Profile: gen.Journal_Profile{ID: j.Profile.ID, Version: j.Profile.Version},
		Lines:   make([]gen.JournalLine, len(j.Lines)),
	}
	if j.ReversalOf != nil {
		r := j.ReversalOf.String()
		out.ReversalOf = &r
	}
	for i, l := range j.Lines {
		line := gen.JournalLine{
			Account: gen.ControlAccount(l.Account), Side: gen.JournalLineSide(l.Side),
			Amount: l.Amount.CanonicalString(), Currency: string(l.Amount.Currency()),
		}
		if l.Decision != nil {
			d := l.Decision.String()
			line.DecisionID = &d
		}
		out.Lines[i] = line
	}
	return out
}

func (rt *Router) handleDecisionJournals(w http.ResponseWriter, r *http.Request) {
	if rt.determinationUnavailable(w, r) {
		return
	}
	decisionID, ok := rt.decisionID(w, r)
	if !ok {
		return
	}
	journals, err := rt.Determination.DecisionJournals(r.Context(), decisionID)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	out := gen.JournalList{Journals: make([]gen.Journal, len(journals))}
	for i, j := range journals {
		out.Journals[i] = toJournal(j)
	}
	writeJSON(w, r, rt.log, http.StatusOK, out)
}

func (rt *Router) handleSubledgerBalances(w http.ResponseWriter, r *http.Request) {
	if rt.determinationUnavailable(w, r) {
		return
	}
	le, balances, err := rt.Determination.Balances(r.Context())
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	keys := make([]subledger.BalanceKey, 0, len(balances))
	for k := range balances {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Account != keys[j].Account {
			return keys[i].Account < keys[j].Account
		}
		return keys[i].Currency < keys[j].Currency
	})
	out := gen.SubledgerBalances{LegalEntityID: le.String(), Balances: make([]gen.ControlBalance, len(keys))}
	for i, k := range keys {
		b := balances[k]
		out.Balances[i] = gen.ControlBalance{
			Account: gen.ControlAccount(k.Account), Currency: string(k.Currency),
			Debits: b.Debits.CanonicalString(), Credits: b.Credits.CanonicalString(),
		}
	}
	writeJSON(w, r, rt.log, http.StatusOK, out)
}

func (rt *Router) handleProposeClassification(w http.ResponseWriter, r *http.Request) {
	if rt.Classification == nil {
		writeProblem(w, r, rt.log, errs.New(errs.CategoryUnavailable, errs.ReasonUnavailable,
			"This cell is not configured for classification assistance."))
		return
	}
	var req gen.ClassificationProposalRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	p, err := rt.Classification.Propose(r.Context(), app.ProposeInput{SubjectRef: req.SubjectRef, Description: req.Description})
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	prov := p.Provenance()
	out := gen.ClassificationProposal{
		ID: p.ID().String(), SubjectRef: p.SubjectRef(), ProposedCode: p.ProposedCode(),
		// Advisory by type: there is no value of this field the service can
		// produce other than false (ADR-0006 §2.6).
		Authoritative: false,
		Provenance: gen.AiProvenance{
			UseCase: string(prov.UseCase), ModelProfile: prov.ModelProfile, ProviderProfile: prov.ProviderProfile,
			PromptProfile: prov.PromptProfile, AiTrainVersion: prov.AiTrainVersion,
		},
	}
	if c := p.Confidence(); c != "" {
		out.Confidence = &c
	}
	writeJSON(w, r, rt.log, http.StatusOK, out)
}
