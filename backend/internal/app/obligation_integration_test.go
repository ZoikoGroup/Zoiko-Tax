//go:build integration

package app_test

import (
	"testing"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/obligation"
)

// Obligations assessed by commit, against the real store. The worked pack
// declares a quarterly VAT return due on the 20th of the month after the
// quarter; the fiscal cell's event time, 2026-09-24, is in Q3.

func (c *fiscalCell) onlyObligation(t *testing.T) app.ObligationView {
	t.Helper()
	views, err := c.svc.Obligations(c.ctx, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 {
		t.Fatalf("%d obligations, want 1: %+v", len(views), views)
	}
	return views[0]
}

func TestIntegrationCommitAssessesThePeriodsObligation(t *testing.T) {
	c := openFiscalCell(t)
	c.mustCommit(t, "k-o1", "INV-O1/1", "100.00", "3", nil)
	c.mustCommit(t, "k-o2", "INV-O2/1", "100.00", "3", nil)

	o := c.onlyObligation(t)
	if o.Status != obligation.StatusOpen || o.EffectiveStatus != obligation.StatusOpen {
		t.Fatalf("status %s, effective %s", o.Status, o.EffectiveStatus)
	}
	// Two commits of VAT 21.00 each, into one obligation.
	if o.Assessed == nil || o.Assessed.CanonicalString() != "42.00" || o.Assessed.Currency() != "EUR" {
		t.Fatalf("assessed %v", o.Assessed)
	}
	if got := o.Period.Start.Format(time.DateOnly) + ".." + o.Period.End.Format(time.DateOnly); got != "2026-07-01..2026-09-30" {
		t.Fatalf("period %s", got)
	}
	if o.Period.Due.Format(time.DateOnly) != "2026-10-20" {
		t.Fatalf("due %s", o.Period.Due.Format(time.DateOnly))
	}
	if o.Definition.ID != "xa-vat-return" || o.Authority != "authority:xa-revenue" || o.LegalEntity.IsZero() ||
		o.Content.BundleDigest == "" || o.Duty != obligation.DutyTransactionMonetary || !o.RecordedBy.IsZero() {
		t.Fatalf("decision fields %+v", o.Obligation)
	}

	// Due on the 20th: not overdue that day in the legal calendar, overdue the
	// day after. Derived as of now, never stored.
	c.clock.Set(time.Date(2026, 10, 20, 23, 0, 0, 0, time.UTC))
	if o = c.onlyObligation(t); o.EffectiveStatus != obligation.StatusOpen {
		t.Fatalf("on the due date: %s", o.EffectiveStatus)
	}
	c.clock.Set(time.Date(2026, 10, 21, 0, 30, 0, 0, time.UTC))
	if o = c.onlyObligation(t); o.EffectiveStatus != obligation.StatusOverdue || o.Status != obligation.StatusOpen {
		t.Fatalf("the day after: %s (stored %s)", o.EffectiveStatus, o.Status)
	}
	overdue, err := c.svc.Obligations(c.ctx, obligation.StatusOverdue, 0)
	if err != nil || len(overdue) != 1 {
		t.Fatalf("filtering on OVERDUE: %v %d", err, len(overdue))
	}
}

func TestIntegrationAdjustmentReassessesTheObligation(t *testing.T) {
	c := openFiscalCell(t)
	original := c.mustCommit(t, "k-oa1", "INV-OA/1", "100.00", "3", nil)
	c.mustCommit(t, "k-oa2", "INV-OA/1", "50.00", "3", &original)

	// The correction withdraws the original's 21.00 and assesses its 10.50.
	if o := c.onlyObligation(t); o.Assessed == nil || o.Assessed.CanonicalString() != "10.50" {
		t.Fatalf("assessed after correction %v", o.Assessed)
	}
}

func TestIntegrationObligationLifecycle(t *testing.T) {
	c := openFiscalCell(t)
	c.mustCommit(t, "k-ol1", "INV-OL1/1", "100.00", "3", nil)
	open := c.onlyObligation(t)

	ready, err := c.svc.TransitionObligation(c.ctx, open.ID, obligation.StatusReady)
	if err != nil {
		t.Fatal(err)
	}
	if ready.Status != obligation.StatusReady || ready.Supersedes == nil || *ready.Supersedes != open.ID || ready.RecordedBy.IsZero() {
		t.Fatalf("ready %+v", ready.Obligation)
	}

	// A move against the row READY superseded is stale.
	if _, err := c.svc.TransitionObligation(c.ctx, open.ID, obligation.StatusSuspended); errs.ReasonOf(err) != errs.ReasonOptimisticConflict {
		t.Fatalf("a stale move: %v", err)
	}
	// FILED is the submission boundary's to set.
	if _, err := c.svc.TransitionObligation(c.ctx, ready.ID, obligation.StatusFiled); errs.ReasonOf(err) != errs.ReasonInvalidValue {
		t.Fatalf("a manual filing: %v", err)
	}

	// New figures undo readiness.
	c.mustCommit(t, "k-ol2", "INV-OL2/1", "100.00", "3", nil)
	reopened := c.onlyObligation(t)
	if reopened.Status != obligation.StatusOpen || reopened.Supersedes == nil || *reopened.Supersedes != ready.ID ||
		!reopened.RecordedBy.IsZero() || reopened.Assessed.CanonicalString() != "42.00" {
		t.Fatalf("after a commit into a READY obligation: %+v", reopened.Obligation)
	}

	// History reads as written.
	was, err := c.svc.Obligation(c.ctx, ready.ID)
	if err != nil {
		t.Fatal(err)
	}
	if was.SupersededBy == nil || *was.SupersededBy != reopened.ID || was.Assessed.CanonicalString() != "21.00" {
		t.Fatalf("the superseded READY row: %+v superseded by %v", was.Obligation, was.SupersededBy)
	}

	// A closed period refuses a commit rather than reopening itself.
	if _, err := c.svc.TransitionObligation(c.ctx, reopened.ID, obligation.StatusClosed); err != nil {
		t.Fatal(err)
	}
	s, err := c.commit(t, "k-ol3", "INV-OL3/1", "100.00", "3", nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.Status != 400 || string(s.Body) != string(errs.ReasonStateTransitionInvalid) {
		t.Fatalf("a commit into a closed period: %d %s", s.Status, s.Body)
	}
}
