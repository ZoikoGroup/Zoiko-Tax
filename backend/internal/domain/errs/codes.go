package errs

// The registered reason-code vocabulary (ADR-0016 §2.4).
//
// Every code is declared here with a category, an owner and a description.
// Registration happens at package init, so a duplicate or a malformed code
// panics before the process serves a request rather than producing an
// unrecognisable code at a customer months later.
//
// Governance: Lane A owns the register. A new code is a reviewed addition to
// this file, and the description is the text that reaches documentation — it is
// what a customer integration engineer reads when deciding what to do, so it
// says what happened and what to do about it, not what the Go code did.
//
// Codes are never renumbered, never reused and never redefined. Retiring one
// means it stops being emitted; the constant stays so that historical evidence
// referencing it still resolves (ADR-0012 §2.9 applies the same reasoning to
// identifiers).
var (
	// ---- platform -------------------------------------------------------

	// ReasonInternal is the fallback for an unclassified failure.
	ReasonInternal = Register("INTERNAL_ERROR", CategoryInternal,
		"lane-a",
		"An unexpected condition prevented the request from completing. The failure is recorded and alerted; the request was not applied.")

	// ReasonUnregisteredReasonCode is what New returns when asked for a code
	// that is not in this register. It is deliberately visible rather than
	// silently substituted, because a code nobody registered is a defect in us.
	ReasonUnregisteredReasonCode = Register("UNREGISTERED_REASON_CODE", CategoryInternal,
		"lane-a",
		"The service attempted to report an error using an unregistered reason code. This is a defect in ZoikoTax.")

	// ReasonPanicRecovered is set by the recovery middleware (ADR-0016 §2.8).
	ReasonPanicRecovered = Register("PANIC_RECOVERED", CategoryInternal,
		"lane-a",
		"A defect interrupted request handling. The request was not applied.")

	// ReasonUnavailable is a transient failure where retry is safe.
	ReasonUnavailable = Register("TEMPORARILY_UNAVAILABLE", CategoryUnavailable,
		"lane-b",
		"A dependency was temporarily unavailable. The request was not applied and may be retried unchanged.")

	// ReasonDatabaseUnavailable narrows ReasonUnavailable to the cell store.
	ReasonDatabaseUnavailable = Register("DATABASE_UNAVAILABLE", CategoryUnavailable,
		"lane-b",
		"The cell database was unreachable. The request was not applied and may be retried unchanged.")

	// ---- request shape --------------------------------------------------

	// ReasonMalformedRequest is a body that is not valid JSON, or not the
	// declared media type.
	ReasonMalformedRequest = Register("MALFORMED_REQUEST", CategoryValidation,
		"lane-k",
		"The request body could not be parsed. Check that it is valid JSON and that Content-Type is application/json.")

	// ReasonUnknownField is ADR-0011 P5 at the transport boundary: an
	// undeclared field is rejected rather than ignored, because silently
	// dropping it would let two different requests digest identically.
	ReasonUnknownField = Register("UNKNOWN_FIELD", CategoryValidation,
		"lane-k",
		"The request contained a field the contract does not declare. Unknown fields are rejected rather than ignored.")

	// ReasonMissingField is a required field with no value.
	ReasonMissingField = Register("MISSING_FIELD", CategoryValidation,
		"lane-k",
		"A required field was absent.")

	// ReasonInvalidValue is a field whose value is outside its declared domain.
	ReasonInvalidValue = Register("INVALID_VALUE", CategoryValidation,
		"lane-k",
		"A field carried a value outside its permitted domain.")

	// ReasonInvalidDecimal is a fiscal quantity that is not in the canonical
	// decimal string form. ADR-0010 §2.9 types every fiscal amount as a string
	// precisely so this is detectable rather than silently coerced.
	ReasonInvalidDecimal = Register("INVALID_DECIMAL", CategoryValidation,
		"lane-d",
		"A fiscal amount was not a canonical decimal string. Amounts are strings such as \"12.50\"; JSON numbers are rejected because they cannot carry decimal precision.")

	// ReasonCurrencyMismatch is an operation combining two currencies.
	ReasonCurrencyMismatch = Register("CURRENCY_MISMATCH", CategoryValidation,
		"lane-d",
		"The request combined amounts in different currencies.")

	// ---- authentication and authorization -------------------------------

	// ReasonUnauthenticated is a request with no usable credential.
	ReasonUnauthenticated = Register("UNAUTHENTICATED", CategoryPolicy,
		"lane-c",
		"The request carried no valid session. Sign in and retry.")

	// ReasonSessionExpired distinguishes an expired session from an absent
	// one, because the client action differs: re-authenticate, rather than
	// check how the credential is being sent.
	ReasonSessionExpired = Register("SESSION_EXPIRED", CategoryPolicy,
		"lane-c",
		"The session has expired. Sign in again.")

	// ReasonSessionRevoked is a session ended by an administrator or by the
	// user signing out elsewhere.
	ReasonSessionRevoked = Register("SESSION_REVOKED", CategoryPolicy,
		"lane-c",
		"The session was revoked. Sign in again.")

	// ReasonInvalidCredentials is a failed sign-in. It deliberately does not
	// distinguish an unknown subject from a wrong secret — that difference is
	// an account-enumeration oracle, and the caller can do nothing with it.
	ReasonInvalidCredentials = Register("INVALID_CREDENTIALS", CategoryPolicy,
		"lane-c",
		"The credentials supplied were not valid.")

	// ReasonForbidden is an authenticated subject without the required role.
	ReasonForbidden = Register("FORBIDDEN", CategoryPolicy,
		"lane-c",
		"The authenticated subject does not hold a role permitting this action.")

	// ReasonTenantMismatch is a request naming a tenant the subject does not
	// belong to. It is a policy refusal rather than a not-found, and it is
	// logged as a security event.
	ReasonTenantMismatch = Register("TENANT_MISMATCH", CategoryPolicy,
		"lane-c",
		"The request referenced a tenant outside the authenticated subject's scope.")

	// ReasonUserDisabled is a subject whose access has been withdrawn.
	ReasonUserDisabled = Register("USER_DISABLED", CategoryPolicy,
		"lane-c",
		"The user account is disabled.")

	// ReasonTenantSuspended is a tenant whose access has been withdrawn.
	ReasonTenantSuspended = Register("TENANT_SUSPENDED", CategoryPolicy,
		"lane-c",
		"The tenant is suspended. Contact your administrator.")

	// ReasonRateLimited is a caller exceeding a rate limit. It is a policy
	// refusal, but retry is safe after the interval, so it carries Retry-After.
	ReasonRateLimited = Register("RATE_LIMITED", CategoryPolicy,
		"lane-c",
		"Too many requests. Retry after the interval given in Retry-After.")

	// ReasonPasswordTooWeak refuses a credential below policy.
	ReasonPasswordTooWeak = Register("PASSWORD_TOO_WEAK", CategoryValidation,
		"lane-c",
		"The password does not meet the minimum length or complexity policy.")

	// ---- resources -------------------------------------------------------

	// ReasonNotFound is a reference to something that does not exist within
	// the caller's tenant.
	ReasonNotFound = Register("NOT_FOUND", CategoryNotFound,
		"lane-k",
		"The referenced resource does not exist, or is outside the caller's tenant.")

	// ReasonAlreadyExists is a uniqueness violation on a caller-supplied key.
	ReasonAlreadyExists = Register("ALREADY_EXISTS", CategoryConflict,
		"lane-k",
		"A resource with that identity already exists.")

	// ReasonOptimisticConflict is a concurrent modification.
	ReasonOptimisticConflict = Register("OPTIMISTIC_CONFLICT", CategoryConflict,
		"lane-d",
		"The resource changed while the request was in flight. Re-read and retry.")

	// ReasonStateTransitionInvalid is a state-machine violation.
	ReasonStateTransitionInvalid = Register("STATE_TRANSITION_INVALID", CategoryConflict,
		"lane-d",
		"The requested transition is not permitted from the resource's current state.")

	// ---- idempotency (ADR-0013) ------------------------------------------

	// ReasonIdempotencyKeyRequired is a mandatory-key endpoint called without
	// one. Rejected before any work begins (ADR-0013 §2.1).
	ReasonIdempotencyKeyRequired = Register("IDEMPOTENCY_KEY_REQUIRED", CategoryValidation,
		"lane-i",
		"This endpoint requires an Idempotency-Key header. The request was not applied.")

	// ReasonIdempotencyKeyReuse is the same key with a different body
	// (ADR-0013 §2.4). The request is not executed.
	ReasonIdempotencyKeyReuse = Register("IDEMPOTENCY_KEY_REUSE", CategoryConflict,
		"lane-i",
		"This idempotency key was already used for a different request. The request was not applied. Use a new key, or resend the original request unchanged.")

	// ReasonRequestInProgress is a concurrent retry of a key still PENDING
	// (ADR-0013 §2.4). The request is not executed.
	ReasonRequestInProgress = Register("REQUEST_IN_PROGRESS", CategoryConflict,
		"lane-i",
		"A request with this idempotency key is still being processed. The request was not applied. Retry after the interval given in Retry-After.")

	// ---- determination and content ---------------------------------------

	// ReasonNoContentBundle is a determination attempted with no bundle
	// loaded. It is CategoryUnavailable rather than internal: a cell that has
	// not yet loaded content will load it, and retry is genuinely safe.
	ReasonNoContentBundle = Register("NO_CONTENT_BUNDLE", CategoryUnavailable,
		"lane-f",
		"The cell has no active content bundle. The request was not applied and may be retried.")

	// ReasonJurisdictionUnsupported is a fact about our coverage, which
	// ADR-0016 §2.1 says a customer is entitled to have recorded.
	ReasonJurisdictionUnsupported = Register("JURISDICTION_UNSUPPORTED", CategoryUnsupported,
		"lane-f",
		"No active country pack covers the jurisdiction resolved for this transaction.")

	// ReasonJurisdictionUnresolved is an address or geography that did not
	// resolve to a jurisdiction at all.
	ReasonJurisdictionUnresolved = Register("JURISDICTION_UNRESOLVED", CategoryValidation,
		"lane-g",
		"The supplied location did not resolve to a jurisdiction. Check the address or supply coordinates.")

	// ReasonNoRoundingPolicy is content that applies a rate or a division
	// without naming a rounding policy. ADR-0002 §5.1 control 5 makes this a
	// content-schema rejection; this code is what the runtime reports if one
	// reaches it anyway.
	ReasonNoRoundingPolicy = Register("NO_ROUNDING_POLICY", CategoryInternal,
		"lane-f",
		"A rule applied a rate without naming a rounding policy. The content bundle is invalid.")

	// ReasonIRVersionUnsupported is a bundle the runtime cannot execute
	// (ADR-0005 §2.8). The runtime stays on the previous bundle.
	ReasonIRVersionUnsupported = Register("IR_VERSION_UNSUPPORTED", CategoryUnsupported,
		"lane-h",
		"The content bundle requires a rule-IR version this runtime does not support.")

	// ---- determination outcomes (ADR-0016 §2.1) --------------------------
	//
	// These are decisions, not errors. They are registered here because a
	// ReasonCode means the same thing wherever it appears, and these appear on
	// a 200 response with a non-authoritative marker rather than on a Problem.

	// ReasonAmbiguous is content that admits more than one answer.
	ReasonAmbiguous = Register("AMBIGUOUS", CategoryUnsupported,
		"lane-g",
		"More than one rule applied and the content does not resolve the precedence. Recorded as a decision with full evidence; not authoritative.")

	// ReasonConflicted is content whose applicable rules disagree.
	ReasonConflicted = Register("CONFLICTED", CategoryUnsupported,
		"lane-g",
		"Applicable rules produced conflicting outcomes. Recorded as a decision with full evidence; not authoritative.")

	// ReasonReviewRequired is an outcome held for human review.
	ReasonReviewRequired = Register("REVIEW_REQUIRED", CategoryUnsupported,
		"lane-g",
		"The outcome requires human review before it can be treated as authoritative.")

	// ReasonUnsupportedTransaction is a shape no active rule covers.
	ReasonUnsupportedTransaction = Register("UNSUPPORTED", CategoryUnsupported,
		"lane-g",
		"No active rule covers this transaction shape. Recorded as a decision; not authoritative.")

	// ---- authorization level (Build Plan A0–A4) --------------------------

	// ReasonNotAuthoritative is the gate that matters most in this estate: no
	// authoritative fiscal output is permitted until A4, and a deployment below
	// that refuses rather than emitting something a customer might file.
	ReasonNotAuthoritative = Register("NOT_AUTHORITATIVE", CategoryPolicy,
		"lane-a",
		"This deployment is not authorized to produce authoritative fiscal output. Results are advisory and must not be filed.")
)
