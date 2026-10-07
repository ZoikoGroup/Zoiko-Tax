package settlement_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/settlement"
)

func refund(t *testing.T) settlement.Refund {
	t.Helper()
	return settlement.Refund{
		ID:          id.NewRefundID(uuid.MustParse("00000000-0000-7000-8000-000000000f01")),
		TenantID:    id.NewTenantID(uuid.MustParse("00000000-0000-7000-8000-000000000001")),
		LegalEntity: id.NewLegalEntityID(uuid.MustParse("00000000-0000-7000-8000-00000000e001")),
		Decision:    id.NewDecisionID(uuid.MustParse("00000000-0000-7000-8000-00000000d001")),
		Amount:      eur(t, "10.50"),
		PaymentRef:  "psp:ch_123",
		RequestedAt: paid,
	}
}

func TestFINREQ0059RefundHeaderValidates(t *testing.T) {
	if err := refund(t).Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*settlement.Refund){
		"no decision":        func(r *settlement.Refund) { r.Decision = id.DecisionID{} },
		"no payment ref":     func(r *settlement.Refund) { r.PaymentRef = "  " },
		"zero amount":        func(r *settlement.Refund) { r.Amount = eur(t, "0.00") },
		"negative amount":    func(r *settlement.Refund) { r.Amount = eur(t, "-1.00") },
		"no request time":    func(r *settlement.Refund) { r.RequestedAt = time.Time{} },
		"no legal entity":    func(r *settlement.Refund) { r.LegalEntity = id.LegalEntityID{} },
		"overlong reference": func(r *settlement.Refund) { r.PaymentRef = string(make([]byte, 256)) },
	} {
		r := refund(t)
		mutate(&r)
		if err := r.Validate(); err == nil {
			t.Errorf("%s: validated", name)
		}
	}
}

func TestFINREQ0058ARefundTimeoutIsUncertainNeverSettled(t *testing.T) {
	for o, want := range map[settlement.RefundOutcome]settlement.RefundStatus{
		settlement.RefundAccepted:  settlement.RefundPending,
		settlement.RefundSucceeded: settlement.RefundCompleted,
		settlement.RefundDeclined:  settlement.RefundFailed,
		settlement.RefundTimedOut:  settlement.RefundUncertain,
		settlement.RefundUnknown:   settlement.RefundUncertain,
	} {
		if got := settlement.RefundStatusFrom(o); got != want {
			t.Errorf("%s -> %s, want %s", o, got, want)
		}
	}
}

func TestFINREQ0059RefundLifecycleIsItsOwn(t *testing.T) {
	r := refund(t)
	ev := settlement.Requested(r)
	if ev.Seq != 1 || ev.Status != settlement.RefundRequested {
		t.Fatalf("first event %+v", ev)
	}
	// Requested -> uncertain -> pending -> completed is a lawful history:
	// a timeout, then the provider confirms it took the refund, then paid it.
	var err error
	for _, step := range []struct {
		o    settlement.RefundOutcome
		want settlement.RefundStatus
	}{
		{settlement.RefundTimedOut, settlement.RefundUncertain},
		{settlement.RefundAccepted, settlement.RefundPending},
		{settlement.RefundSucceeded, settlement.RefundCompleted},
	} {
		if ev, err = settlement.Report(ev, step.o, "psp:re_1", paid, id.UserID{}); err != nil {
			t.Fatalf("%s: %v", step.o, err)
		}
		if ev.Status != step.want {
			t.Fatalf("%s -> %s, want %s", step.o, ev.Status, step.want)
		}
	}
	if ev.Seq != 4 {
		t.Fatalf("seq %d after three reports", ev.Seq)
	}
	// Nothing leaves a terminal status, not even a repeat of the same report.
	for _, o := range []settlement.RefundOutcome{settlement.RefundSucceeded, settlement.RefundDeclined, settlement.RefundUnknown} {
		if _, err := settlement.Report(ev, o, "", paid, id.UserID{}); err == nil {
			t.Errorf("a completed refund accepted %s", o)
		}
	}
	// A pending refund that is pending again has not moved.
	pending := settlement.RefundEvent{Refund: r.ID, Seq: 2, Status: settlement.RefundPending}
	if _, err := settlement.Report(pending, settlement.RefundAccepted, "", paid, id.UserID{}); err == nil {
		t.Error("a repeated pending report was recorded as a move")
	}
	if _, err := settlement.Report(pending, "REVERSED", "", paid, id.UserID{}); err == nil {
		t.Error("an unknown outcome was accepted")
	}
}

func TestFINREQ0059OnlyAFailedRefundReleasesTheTax(t *testing.T) {
	for _, s := range []settlement.RefundStatus{settlement.RefundRequested, settlement.RefundPending, settlement.RefundUncertain, settlement.RefundCompleted} {
		if !s.Reserves() {
			t.Errorf("%s does not reserve the refunded tax", s)
		}
	}
	if settlement.RefundFailed.Reserves() {
		t.Error("a failed refund still reserves the tax")
	}
}
