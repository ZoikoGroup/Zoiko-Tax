package gateway

import (
	"context"
	"log/slog"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/ai"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/privacy"
)

// TransferOutcome is how a crossing ended.
type TransferOutcome string

// The outcomes.
const (
	// OutcomeCompleted is a call the Gateway answered with an evidenced result.
	OutcomeCompleted TransferOutcome = "COMPLETED"
	// OutcomeRefused is a governance refusal, by the Go gate (nothing was
	// sent) or by the Gateway. Recorded because "we considered and refused"
	// is itself auditable (ADR-0006 §2.7).
	OutcomeRefused TransferOutcome = "REFUSED"
	// OutcomeFailed is a deadline, an open circuit or a transport failure.
	OutcomeFailed TransferOutcome = "FAILED"
	// OutcomeDiscarded is a reply that arrived without its provenance and was
	// thrown away rather than returned.
	OutcomeDiscarded TransferOutcome = "DISCARDED"
)

// Transfer is ADR-0006 §2.7's evidenced-transfer record: every crossing of the
// boundary, attempted or completed, logged with use case, model profile,
// provider profile, prompt profile, region and data classification.
//
// Note what is absent: no input, no prompt, no model output and no subject
// reference. The record says that a crossing happened and under what
// governance, which is what an audit asks; the content is fetched through an
// audited path when it is genuinely needed (the Python provenance.redacted()
// makes the same cut, and ADR-0016 §2.6 applies the same reasoning to error
// responses).
type Transfer struct {
	Tenant id.TenantID
	Kind   Kind

	UseCase   ai.AiUseCase
	Region    string
	DataClass privacy.Class // the highest class present
	// DataClasses is every class present.
	DataClasses []privacy.Class
	RiskTier    ai.RiskTier
	Authority   ai.AuthorityOutcome

	// Routing, known only once the Gateway has answered. Empty on a refusal
	// before the call.
	ModelProfile    string
	ProviderProfile string
	PromptProfile   string
	AiTrainVersion  string

	Outcome TransferOutcome
	// Reason is the refusal or failure code; empty when completed.
	Reason errs.ReasonCode
	At     time.Time
}

// TransferRecorder writes evidenced-transfer records. The Client refuses to
// return an output whose transfer it could not record.
type TransferRecorder interface {
	RecordTransfer(ctx context.Context, t Transfer) error
}

// SlogRecorder writes transfers to the structured log (ADR-0015 §2.7), which
// is redacted structurally (§2.2). It is the W1 recorder; the evidence-ledger
// recorder replaces it when AI-influenced evidence records are written, and
// the interface is the seam for that.
type SlogRecorder struct {
	Logger *slog.Logger
}

// RecordTransfer logs the transfer. It never fails: a log write that cannot
// be made is the logger's concern, not a reason to fail the call.
func (r SlogRecorder) RecordTransfer(ctx context.Context, t Transfer) error {
	l := r.Logger
	if l == nil {
		l = slog.Default()
	}
	l.LogAttrs(ctx, slog.LevelInfo, "ai.gateway.transfer",
		slog.String("tenant", t.Tenant.String()),
		slog.String("kind", string(t.Kind)),
		slog.String("use_case", string(t.UseCase)),
		slog.String("region", t.Region),
		slog.String("data_class", string(t.DataClass)),
		slog.Any("data_classes", dataClassesString(t.DataClasses)),
		slog.String("risk_tier", string(t.RiskTier)),
		slog.String("authority_outcome", string(t.Authority)),
		slog.String("model_profile", t.ModelProfile),
		slog.String("provider_profile", t.ProviderProfile),
		slog.String("prompt_profile", t.PromptProfile),
		slog.String("ai_train_version", t.AiTrainVersion),
		slog.String("outcome", string(t.Outcome)),
		slog.String("reason", string(t.Reason)),
		slog.Time("at", t.At),
	)
	return nil
}

// dataClassesString is used by the slog recorder.
func dataClassesString(cs []privacy.Class) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = string(c)
	}
	return out
}
