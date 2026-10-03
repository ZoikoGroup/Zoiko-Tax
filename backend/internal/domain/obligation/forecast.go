package obligation

import (
	"fmt"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
)

// ForecastSource is what produced a forecast.
type ForecastSource string

// The forecast sources.
const (
	ForecastFromRules ForecastSource = "RULES"
	// ForecastFromAI came from the Intelligence Fabric. Like every AI output
	// it is advisory and stays so.
	ForecastFromAI ForecastSource = "AI"
)

// Forecast is ForecastObligation: a projection of an obligation that has not
// been decided. It is a separate type from Obligation, with a separate
// identifier type, so there is no expression that hands a forecast's id to
// anything expecting an authoritative obligation's (ZTAX-OBL-REQ-0046,
// -0047), and no field it shares with an Obligation it could overwrite
// (ZTAX-OBL-REQ-0048).
type Forecast struct {
	ID           id.ForecastID
	TenantID     id.TenantID
	LegalEntity  id.LegalEntityID
	Authority    string
	Definition   DefinitionRef
	Period       Span
	Projected    *fiscal.Money
	Source       ForecastSource
	ForecastedAt time.Time
}

// Authoritative is false, for every forecast. It exists so that a caller that
// branches on authority has a method to call rather than a field to forget.
func (Forecast) Authoritative() bool { return false }

// Validate refuses a forecast that cannot be shown.
func (f Forecast) Validate() error {
	if f.ID.IsZero() || f.TenantID.IsZero() || f.LegalEntity.IsZero() {
		return fmt.Errorf("obligation: forecast is not scoped to a tenant and legal entity")
	}
	if f.Source != ForecastFromRules && f.Source != ForecastFromAI {
		return fmt.Errorf("obligation: forecast has source %q", f.Source)
	}
	return nil
}
