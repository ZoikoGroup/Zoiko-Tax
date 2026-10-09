package app

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/subledger"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/clock"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// Subledger period close (W2 lane J; ZTAX-FIN-001 §20–§22). The rules are
// internal/domain/subledger's (period.go); this is where they meet the store.
//
// Every journal a cell posts goes through guardPosting, which takes the
// period's lock shared and refuses a journal the period's state does not
// admit. A transition takes the same lock exclusive, so a close waits for the
// postings in flight and no posting slips in behind it.

// guardPosting admits a journal into its period, or refuses it. A journal
// posted inside an amendment window comes back marked as an amendment. With
// no period store wired, every period is open.
func guardPosting(ctx context.Context, periods port.PeriodRepository, j subledger.Journal) (subledger.Journal, error) {
	if periods == nil {
		return j, nil
	}
	if err := periods.LockForPosting(ctx, j.LegalEntity, j.LegalPeriod); err != nil {
		return subledger.Journal{}, err
	}
	history, err := periods.History(ctx, j.LegalEntity, j.LegalPeriod)
	if err != nil {
		return subledger.Journal{}, err
	}
	state := subledger.PeriodOpen
	if len(history) > 0 {
		state = history[len(history)-1].State
	}
	out, err := subledger.PostableAs(j, state)
	if err != nil {
		return subledger.Journal{}, errs.Wrap(err, errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("Subledger period %s is %s; nothing posts into it by the normal path. A late change goes through an amendment window (ZTAX-FIN-REQ-0090).", j.LegalPeriod, state))
	}
	return out, nil
}

// PeriodService moves the subledger's legal periods.
type PeriodService struct {
	periods  port.PeriodRepository
	entities port.LegalEntityRepository
	tx       port.TxManager
	clock    clock.Clock
	ids      idgen.Generator
}

// NewPeriodService wires the service.
func NewPeriodService(periods port.PeriodRepository, entities port.LegalEntityRepository, tx port.TxManager, clk clock.Clock, ids idgen.Generator) *PeriodService {
	return &PeriodService{periods: periods, entities: entities, tx: tx, clock: clk, ids: ids}
}

// PeriodView is a legal period as read back.
type PeriodView struct {
	LegalEntity id.LegalEntityID
	Period      string
	State       subledger.PeriodState
	History     []subledger.PeriodEvent
	// Manifest is the latest close manifest's digest and canonical bytes,
	// when the period has ever been hard-closed.
	Manifest     canonical.Digest
	ManifestBody []byte
	// Intact reports, for a period with a manifest, whether the journals
	// and control balances posted into it today are still exactly the ones
	// the manifest sealed (ZTAX-FIN-REQ-0089). Documents and open
	// exceptions are not compared: a document is credited and a refund
	// settles after a close, legitimately, and the manifest records where
	// they stood when it was made.
	Intact *bool
}

func (s *PeriodService) scope(ctx context.Context, period string, roles ...security.Role) (security.Context, id.LegalEntityID, string, error) {
	sc, err := requireRoleOrSystem(ctx, roles...)
	if err != nil {
		return security.Context{}, id.LegalEntityID{}, "", err
	}
	p, err := subledger.ParseLegalPeriod(period)
	if err != nil {
		return security.Context{}, id.LegalEntityID{}, "", errs.Invalid("period", errs.ReasonInvalidValue, "A legal period is a month, YYYY-MM.")
	}
	le, err := s.entities.Default(ctx)
	if errs.IsCategory(err, errs.CategoryNotFound) {
		return security.Context{}, id.LegalEntityID{}, "", errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			"The tenant has no default legal entity.")
	}
	if err != nil {
		return security.Context{}, id.LegalEntityID{}, "", err
	}
	return sc, le.ID, p, nil
}

// Period reads a legal period of the tenant's default legal entity.
func (s *PeriodService) Period(ctx context.Context, period string) (PeriodView, error) {
	_, le, p, err := s.scope(ctx, period, security.RoleOperator, security.RoleAnalyst, security.RoleAuditor)
	if err != nil {
		return PeriodView{}, err
	}
	return s.view(ctx, le, p, true)
}

func (s *PeriodService) view(ctx context.Context, le id.LegalEntityID, p string, verify bool) (PeriodView, error) {
	history, err := s.periods.History(ctx, le, p)
	if err != nil {
		return PeriodView{}, err
	}
	v := PeriodView{LegalEntity: le, Period: p, State: subledger.PeriodOpen, History: history}
	if len(history) > 0 {
		v.State = history[len(history)-1].State
	}
	for _, e := range history {
		if !e.Manifest.IsZero() {
			v.Manifest = e.Manifest
		}
	}
	if v.Manifest.IsZero() {
		return v, nil
	}
	if v.ManifestBody, err = s.periods.Manifest(ctx, v.Manifest); err != nil {
		return PeriodView{}, err
	}
	if verify {
		intact, err := s.intact(ctx, le, p, v.ManifestBody)
		if err != nil {
			return PeriodView{}, err
		}
		v.Intact = &intact
	}
	return v, nil
}

// manifestWire is what intact reads back of a manifest.
type manifestWire struct {
	Journals []string `json:"journals"`
	Balances []struct {
		Account  string `json:"account"`
		Currency string `json:"currency"`
		Debits   string `json:"debits"`
		Credits  string `json:"credits"`
	} `json:"balances"`
}

// intact compares the period's journals and balances today with a manifest.
func (s *PeriodService) intact(ctx context.Context, le id.LegalEntityID, p string, body []byte) (bool, error) {
	var m manifestWire
	if err := json.Unmarshal(body, &m); err != nil {
		return false, integrity(err, "A stored close manifest cannot be read.")
	}
	pop, err := s.periods.Population(ctx, le, p)
	if err != nil {
		return false, err
	}
	now := make([]string, len(pop.Journals))
	for i, j := range pop.Journals {
		now[i] = j.String()
	}
	slices.Sort(now)
	if !slices.Equal(now, m.Journals) {
		return false, nil
	}
	sealed := map[string]string{}
	for _, b := range m.Balances {
		sealed[b.Account+"/"+b.Currency] = b.Debits + "/" + b.Credits
	}
	if len(sealed) != len(pop.Balances) {
		return false, nil
	}
	for _, b := range pop.Balances {
		if sealed[string(b.Account)+"/"+string(b.Currency)] != b.Debits.CanonicalString()+"/"+b.Credits.CanonicalString() {
			return false, nil
		}
	}
	return true, nil
}

// Transition moves a period by an operator's act: soft or hard close, back
// to open from a soft close, into and out of an amendment window, or sealed.
// A hard close — the first, or the return after an amendment window — seals
// the period's population in a new manifest naming the one before it.
func (s *PeriodService) Transition(ctx context.Context, period string, to subledger.PeriodState, reason string) (PeriodView, error) {
	sc, le, p, err := s.scope(ctx, period, security.RoleOperator)
	if err != nil {
		return PeriodView{}, err
	}
	if !to.Valid() {
		return PeriodView{}, errs.Invalid("to", errs.ReasonInvalidValue, "That is not a period state.")
	}
	if to == subledger.PeriodReopened {
		return PeriodView{}, errs.Invalid("to", errs.ReasonInvalidValue,
			"A closed period is reopened by a request and a second person's approval, not by a transition (ZTAX-FIN-REQ-0091).")
	}
	if len(reason) > 1000 {
		return PeriodView{}, errs.Invalid("reason", errs.ReasonInvalidValue, "A reason is at most 1000 characters.")
	}
	var out PeriodView
	err = s.inTx(ctx, func(ctx context.Context) error {
		if err := s.periods.LockForTransition(ctx, le, p); err != nil {
			return err
		}
		cur, err := s.view(ctx, le, p, false)
		if err != nil {
			return err
		}
		if !cur.State.CanMoveTo(to) {
			return errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
				fmt.Sprintf("Period %s is %s and cannot become %s.", p, cur.State, to))
		}
		now := s.clock.Now().UTC().Truncate(time.Microsecond)
		e := subledger.PeriodEvent{LegalEntity: le, Period: p, Seq: len(cur.History) + 1, State: to, Reason: reason, RecordedAt: now, RecordedBy: sc.Subject()}
		if to.Closes() {
			if e.Manifest, err = s.seal(ctx, sc, le, p, now, cur.Manifest); err != nil {
				return err
			}
		}
		if err := s.periods.AppendEvent(ctx, e); err != nil {
			return err
		}
		out, err = s.view(ctx, le, p, false)
		return err
	})
	return out, err
}

// seal builds, stores and digests the period's close manifest.
func (s *PeriodService) seal(ctx context.Context, sc security.Context, le id.LegalEntityID, p string, now time.Time, prior canonical.Digest) (canonical.Digest, error) {
	pop, err := s.periods.Population(ctx, le, p)
	if err != nil {
		return canonical.Digest{}, err
	}
	_, digest, body, err := subledger.CloseManifest{
		TenantID: sc.Tenant(), LegalEntity: le, Period: p, Journals: pop.Journals, Balances: pop.Balances,
		Documents: pop.Documents, Exceptions: pop.Exceptions, ClosedAt: now, ClosedBy: sc.Subject(), Supersedes: prior,
	}.Seal()
	if err != nil {
		return canonical.Digest{}, internal(err, "The close manifest could not be sealed.")
	}
	if err := s.periods.PutManifest(ctx, le, p, digest, body, now); err != nil {
		return canonical.Digest{}, err
	}
	return digest, nil
}

// RequestReopen asks to reopen a hard-closed period. Nothing moves until
// someone else approves.
func (s *PeriodService) RequestReopen(ctx context.Context, period, reason string) (port.ReopenRequest, error) {
	sc, le, p, err := s.scope(ctx, period, security.RoleOperator)
	if err != nil {
		return port.ReopenRequest{}, err
	}
	if strings.TrimSpace(reason) == "" || len(reason) > 1000 {
		return port.ReopenRequest{}, errs.Invalid("reason", errs.ReasonMissingField, "Reopening a closed period needs a reason of at most 1000 characters.")
	}
	if sc.Subject().IsZero() {
		return port.ReopenRequest{}, errs.New(errs.CategoryPolicy, errs.ReasonForbidden, "A reopen is requested by a person.")
	}
	cur, err := s.view(ctx, le, p, false)
	if err != nil {
		return port.ReopenRequest{}, err
	}
	if !cur.State.CanReopen() {
		return port.ReopenRequest{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("Period %s is %s; only a hard-closed period is reopened.", p, cur.State))
	}
	reqID, err := idgen.ReopenRequestID(s.ids)
	if err != nil {
		return port.ReopenRequest{}, internal(err, "The request could not be recorded.")
	}
	r := port.ReopenRequest{
		ID: reqID, TenantID: sc.Tenant(), LegalEntity: le, Period: p, Reason: reason,
		RequestedAt: s.clock.Now().UTC().Truncate(time.Microsecond), RequestedBy: sc.Subject(),
	}
	return r, s.periods.CreateReopenRequest(ctx, r)
}

// ApproveReopen reopens a period on a request someone else made
// (ZTAX-FIN-REQ-0091). The period's manifests stay: reopening adds an
// event, and the next hard close writes a manifest naming the last.
func (s *PeriodService) ApproveReopen(ctx context.Context, period string, requestID id.ReopenRequestID) (PeriodView, error) {
	sc, le, p, err := s.scope(ctx, period, security.RoleAdmin)
	if err != nil {
		return PeriodView{}, err
	}
	var out PeriodView
	err = s.inTx(ctx, func(ctx context.Context) error {
		req, err := s.periods.ReopenRequest(ctx, requestID)
		if err != nil {
			return err
		}
		if req.Period != p || req.LegalEntity != le {
			return errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "No such reopen request for this period.")
		}
		if req.RequestedBy == sc.Subject() || sc.Subject().IsZero() {
			return errs.New(errs.CategoryPolicy, errs.ReasonForbidden,
				"A reopen is approved by someone other than the person who requested it (ZTAX-FIN-REQ-0091).")
		}
		if err := s.periods.LockForTransition(ctx, le, p); err != nil {
			return err
		}
		cur, err := s.view(ctx, le, p, false)
		if err != nil {
			return err
		}
		for _, e := range cur.History {
			if e.Request == requestID.String() {
				return errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid, "That request has already been approved.")
			}
		}
		if !cur.State.CanReopen() {
			return errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
				fmt.Sprintf("Period %s is %s; only a hard-closed period is reopened.", p, cur.State))
		}
		if err := s.periods.AppendEvent(ctx, subledger.PeriodEvent{
			LegalEntity: le, Period: p, Seq: len(cur.History) + 1, State: subledger.PeriodReopened, Reason: req.Reason,
			RecordedAt: s.clock.Now().UTC().Truncate(time.Microsecond), RecordedBy: req.RequestedBy,
			ApprovedBy: sc.Subject(), Request: requestID.String(),
		}); err != nil {
			return err
		}
		out, err = s.view(ctx, le, p, false)
		return err
	})
	return out, err
}

func (s *PeriodService) inTx(ctx context.Context, fn func(context.Context) error) error {
	tx, txCtx, err := s.tx.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(txCtx) }()
	if err := fn(txCtx); err != nil {
		return err
	}
	return tx.Commit(txCtx)
}
