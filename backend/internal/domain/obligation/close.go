package obligation

import (
	"fmt"
	"sort"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// ClosedObligation is one obligation as the close recorded it.
type ClosedObligation struct {
	ID       id.ObligationID
	Status   Status
	Assessed *fiscal.Money
}

// ClosedAccumulator is one accumulator's position at close: the key, the last
// contribution sequence included, and the total. The sequence is what makes
// "the position at close" reconstructable from the contribution log
// (ZTAX-OBL-REQ-0091).
type ClosedAccumulator struct {
	Key     string
	LastSeq int64
	Total   fiscal.Money
}

// PeriodCloseManifest is the immutable record of a period's close
// (ZTAX-OBL-REQ-0090). It is built by SealPeriod, which sorts its contents and
// digests them; the digest is what a later event is checked against, and the
// manifest itself is never edited. A post-close event does not change it
// (ZTAX-OBL-REQ-0092) — it is routed by RouteLateChange instead.
type PeriodCloseManifest struct {
	ID           id.PeriodCloseID
	TenantID     id.TenantID
	LegalEntity  id.LegalEntityID
	Authority    string
	Period       Span
	Obligations  []ClosedObligation
	Accumulators []ClosedAccumulator
	ClosedAt     time.Time
	ClosedBy     id.UserID
	Digest       canonical.Digest
}

// SealPeriod builds and digests a manifest.
func SealPeriod(m PeriodCloseManifest) (PeriodCloseManifest, error) {
	if m.ID.IsZero() || m.TenantID.IsZero() || m.LegalEntity.IsZero() || m.Authority == "" {
		return PeriodCloseManifest{}, fmt.Errorf("obligation: a close manifest is scoped to a tenant, legal entity and authority")
	}
	if m.ClosedAt.IsZero() || m.ClosedBy.IsZero() {
		return PeriodCloseManifest{}, fmt.Errorf("obligation: a close manifest records who closed the period and when")
	}
	out := m
	out.Obligations = append([]ClosedObligation(nil), m.Obligations...)
	out.Accumulators = append([]ClosedAccumulator(nil), m.Accumulators...)
	sort.Slice(out.Obligations, func(i, j int) bool { return out.Obligations[i].ID.String() < out.Obligations[j].ID.String() })
	sort.Slice(out.Accumulators, func(i, j int) bool { return out.Accumulators[i].Key < out.Accumulators[j].Key })
	out.ClosedAt = m.ClosedAt.UTC()
	d, err := canonical.Sum(out.canonical())
	if err != nil {
		return PeriodCloseManifest{}, err
	}
	out.Digest = d
	return out, nil
}

// Verify reports whether the manifest still matches its digest.
func (m PeriodCloseManifest) Verify() error {
	d, err := canonical.Sum(m.canonical())
	if err != nil {
		return err
	}
	if !d.Equal(m.Digest) {
		return fmt.Errorf("obligation: close manifest %s does not match its digest", m.ID)
	}
	return nil
}

func (m PeriodCloseManifest) canonical() canonical.Value {
	obs := make([]canonical.Value, len(m.Obligations))
	for i, o := range m.Obligations {
		assessed := canonical.Absent()
		if o.Assessed != nil {
			assessed = canonical.Money(*o.Assessed)
		}
		obs[i] = canonical.Object(
			canonical.F("id", canonical.String(o.ID.String())),
			canonical.F("status", canonical.String(string(o.Status))),
			canonical.F("assessed", assessed),
		)
	}
	accs := make([]canonical.Value, len(m.Accumulators))
	for i, a := range m.Accumulators {
		accs[i] = canonical.Object(
			canonical.F("key", canonical.String(a.Key)),
			canonical.F("lastSeq", canonical.Integer(a.LastSeq)),
			canonical.F("total", canonical.Money(a.Total)),
		)
	}
	return canonical.Object(
		canonical.F("id", canonical.String(m.ID.String())),
		canonical.F("legalEntity", canonical.String(m.LegalEntity.String())),
		canonical.F("authority", canonical.String(m.Authority)),
		canonical.F("periodStart", canonical.Time(m.Period.Start)),
		canonical.F("periodEnd", canonical.Time(m.Period.End)),
		canonical.F("obligations", canonical.Array(obs...)),
		canonical.F("accumulators", canonical.Array(accs...)),
		canonical.F("closedAt", canonical.Time(m.ClosedAt)),
		canonical.F("closedBy", canonical.String(m.ClosedBy.String())),
	)
}

// LateRoute is where a post-close change goes.
type LateRoute struct {
	// Treatment is the approved amendment treatment, or empty when none
	// applies and the change routes to review (ZTAX-OBL-REQ-0098).
	Treatment AmendmentTreatment
	// Review is set when no treatment applies.
	Review bool
	Detail string
}

// RouteLateChange routes a change whose event time falls in a closed period
// (ZTAX-OBL-REQ-0093). The manifest is read and never written. The route is
// the first of the definition's permitted treatments that the change's timing
// allows: a current-period adjustment needs the change to arrive in the period
// immediately after the closed one; a prior-period amendment and a later
// true-up are always available where the definition permits them. A
// definition that permits none — or a change outside every window — goes to
// review, never to a default.
func RouteLateChange(m PeriodCloseManifest, def Definition, eventTime, arrivedIn time.Time, next Span) (LateRoute, error) {
	if err := m.Verify(); err != nil {
		return LateRoute{}, err
	}
	if !m.Period.Contains(eventTime) {
		return LateRoute{}, fmt.Errorf("obligation: the change's event time is not in the closed period")
	}
	for _, t := range def.Amendment {
		switch t {
		case AmendCurrentPeriod:
			if next.Contains(arrivedIn) {
				return LateRoute{Treatment: t, Detail: "adjusted in the period after the closed one"}, nil
			}
		case AmendPriorPeriod:
			return LateRoute{Treatment: t, Detail: "the closed period is amended"}, nil
		case AmendLaterTrueUp:
			return LateRoute{Treatment: t, Detail: "trued up in a later period"}, nil
		}
	}
	return LateRoute{Review: true, Detail: fmt.Sprintf("definition %s@%s approves no treatment for this change", def.ID, def.Version)}, nil
}
