package obligation

import (
	"fmt"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
)

// RegistrationState is a registration duty's state (ZTAX-OBL-REQ-0073,
// -0074).
type RegistrationState string

// The states.
const (
	RegistrationNotRequired            RegistrationState = "NOT_REQUIRED"
	RegistrationMonitor                RegistrationState = "MONITOR"
	RegistrationTriggered              RegistrationState = "TRIGGERED"
	RegistrationInProgress             RegistrationState = "IN_PROGRESS"
	RegistrationRegistered             RegistrationState = "REGISTERED"
	RegistrationRenewalDue             RegistrationState = "RENEWAL_DUE"
	RegistrationChangeRequired         RegistrationState = "CHANGE_REQUIRED"
	RegistrationDeregistrationEligible RegistrationState = "DEREGISTRATION_ELIGIBLE"
	RegistrationDeregistered           RegistrationState = "DEREGISTERED"
)

var registrationTransitions = map[RegistrationState][]RegistrationState{
	RegistrationNotRequired:            {RegistrationMonitor, RegistrationTriggered},
	RegistrationMonitor:                {RegistrationNotRequired, RegistrationTriggered},
	RegistrationTriggered:              {RegistrationInProgress},
	RegistrationInProgress:             {RegistrationRegistered, RegistrationTriggered},
	RegistrationRegistered:             {RegistrationRenewalDue, RegistrationChangeRequired, RegistrationDeregistrationEligible},
	RegistrationRenewalDue:             {RegistrationRegistered},
	RegistrationChangeRequired:         {RegistrationRegistered},
	RegistrationDeregistrationEligible: {RegistrationRegistered, RegistrationDeregistered},
	RegistrationDeregistered:           {RegistrationMonitor, RegistrationTriggered},
}

// CanTransitionRegistration reports whether a registration may move.
func CanTransitionRegistration(from, to RegistrationState) bool {
	for _, s := range registrationTransitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

// Registration is one registration with an authority.
type Registration struct {
	ID          id.RegistrationID
	TenantID    id.TenantID
	LegalEntity id.LegalEntityID
	Authority   string
	Definition  string
	State       RegistrationState
	// AuthorityIdentifier and the effective dates are stored when the
	// authority issues the registration (ZTAX-OBL-REQ-0075), verbatim.
	AuthorityIdentifier string
	EffectiveFrom       *time.Time
	EffectiveTo         *time.Time
}

// Discover moves a registration from what the triggers now say
// (ZTAX-OBL-REQ-0072).
//
// A TRUE trigger moves NOT_REQUIRED or MONITOR to TRIGGERED. An UNKNOWN
// trigger moves NOT_REQUIRED to MONITOR — unknown is never "not required".
// A FALSE trigger changes nothing once a registration exists: falling revenue
// is not a cessation rule, and only a pack-defined cessation rule makes a
// registration DEREGISTRATION_ELIGIBLE (ZTAX-OBL-REQ-0077). ceased is that
// rule's answer, evaluated by the caller from content.
func (r Registration) Discover(trigger Truth, ceased Truth) (Registration, error) {
	next := r
	switch r.State {
	case RegistrationNotRequired, RegistrationMonitor, RegistrationDeregistered:
		switch trigger {
		case True:
			next.State = RegistrationTriggered
		case Unknown:
			if r.State == RegistrationNotRequired {
				next.State = RegistrationMonitor
			}
		}
	case RegistrationRegistered:
		if ceased == True {
			next.State = RegistrationDeregistrationEligible
		}
	}
	if next.State != r.State && !CanTransitionRegistration(r.State, next.State) {
		return Registration{}, fmt.Errorf("registration cannot move from %s to %s", r.State, next.State)
	}
	return next, nil
}

// Issue records the authority's issue of the registration. The identifier
// and effective date are required: a REGISTERED state with no authority
// reference is a claim nobody can check.
func (r Registration) Issue(authorityIdentifier string, effectiveFrom time.Time) (Registration, error) {
	if r.State != RegistrationInProgress {
		return Registration{}, fmt.Errorf("registration is %s; it is issued from IN_PROGRESS", r.State)
	}
	if authorityIdentifier == "" || effectiveFrom.IsZero() {
		return Registration{}, fmt.Errorf("an issued registration records the authority's identifier and its effective date")
	}
	next := r
	next.State = RegistrationRegistered
	next.AuthorityIdentifier = authorityIdentifier
	from := effectiveFrom.UTC()
	next.EffectiveFrom = &from
	return next, nil
}
