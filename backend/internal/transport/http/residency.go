package http

import (
	"log/slog"
	"net/http"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
)

// withResidency refuses, fail-closed, every authenticated request whose
// tenant's home cell is not this cell (ADR-0009 §2.6, ADR-0010 §2.4's residency
// stage, SEC-REQ-0037).
//
// It sits immediately after authentication because that is the first point the
// tenant is known, and before authorization because a tenant that is not
// resident here has no role in this cell worth checking. The home cell rides on
// the security context, set from the tenant row in the same lookup that
// authenticated the session, so this stage costs a string comparison rather
// than a query.
//
// An unauthenticated request passes through untouched: it names no tenant, so
// there is no residency to check, and the endpoints that accept one (sign-in,
// discovery, probes) either have their own check or touch no tenant data.
// Sign-in's check is in handleSignIn, because the tenant is first resolved
// there.
//
// cell is this process's ZTAX_CELL. Empty refuses every authenticated request:
// a process that does not know where it is cannot conclude that a tenant lives
// there.
func withResidency(cell string, log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sc, ok := security.From(r.Context())
			if !ok || !sc.Authenticated() {
				next.ServeHTTP(w, r)
				return
			}
			if err := residencyCheck(r, log, sc.Tenant().String(), sc.HomeCell(), cell); err != nil {
				writeProblem(w, r, log, err)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// residencyCheck applies security.CheckResidency and, on refusal, records the
// security event and returns the Problem's error.
//
// The event is logged at error level, separately from the access log, because
// a refusal here means a tenant's data is present in a cell it does not belong
// to — that is a residency incident for an operator, not a client mistake. The
// log names the tenant and both cells; the response names neither cell.
func residencyCheck(r *http.Request, log *slog.Logger, tenant, home, here string) error {
	err := security.CheckResidency(home, here)
	if err == nil {
		return nil
	}
	log.ErrorContext(r.Context(), "residency refusal",
		"security.event", "RESIDENCY_REFUSED",
		"ztx.tenant_id", tenant,
		"ztx.home_cell", home,
		"ztx.cell", here,
		"error", err.Error(),
	)
	return errs.Wrap(err, errs.CategoryPolicy, errs.ReasonTenantNotResident,
		"The tenant is not resident in the cell that received the request. The request was not applied.")
}
