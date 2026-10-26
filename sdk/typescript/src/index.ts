/**
 * ZoikoTax TypeScript SDK.
 *
 * The types in `./schema` are generated from the contract and are not edited;
 * `npm run generate:check` fails the build on any drift, which is the same
 * mechanism ADR-0010 §2.1 applies to the generated server. What is hand-written
 * is this file: a thin typed layer over `fetch`, with no runtime dependencies.
 *
 * Three decisions are worth stating, because each is a thing an SDK usually
 * does that this one deliberately does not.
 *
 * **An error is a value, not an exception.** Every method returns a discriminated
 * result rather than throwing on a 4xx. A `ZoikoTaxError` carries the Problem
 * Details document whole, and the field to branch on is `reasonCode` — a closed,
 * registered vocabulary that means the same thing in an error, in a decision and
 * in evidence (ADR-0016 §2.4). Nothing here matches on a title or a message, and
 * neither should a caller: both may be reworded without notice.
 *
 * **Nothing retries by itself.** `retryable` says whether retrying unchanged
 * could succeed, and the caller decides. An SDK that retried on its own would,
 * on the endpoints this surface is about to grow, submit a transaction twice —
 * and the thing that makes that safe is an `Idempotency-Key` the caller chose
 * (ADR-0013), not a backoff this library picked.
 *
 * **No token handling.** The session is an opaque `HttpOnly` cookie the browser
 * sends automatically (ADR-0020). There is nothing to store, nothing to refresh
 * and nothing to leak, so this SDK has no credential store — it sets
 * `credentials: "include"` and gets out of the way.
 *
 * @packageDocumentation
 */

import type { components, operations } from "./schema.js";

export type { components, operations, paths } from "./schema.js";

/** The Problem Details document every error response carries (RFC 9457). */
export type Problem = components["schemas"]["Problem"];

/**
 * A registered reason code.
 *
 * Deliberately `string` rather than a union: the register grows by addition
 * (ADR-0010 §2.6), and a union would make an SDK release a prerequisite for
 * every new code — so an integration would break on an additive change, which
 * is the one thing additive-only versioning promises will not happen.
 */
export type ReasonCode = components["schemas"]["ReasonCode"];

export type Capabilities = components["schemas"]["Capabilities"];
export type Session = components["schemas"]["Session"];
export type Tenant = components["schemas"]["Tenant"];
export type User = components["schemas"]["User"];
export type SessionSummary = components["schemas"]["SessionSummary"];
export type AuditRecord = components["schemas"]["AuditRecord"];
export type Role = components["schemas"]["Role"];
export type UserStatus = components["schemas"]["UserStatus"];
export type Quote = components["schemas"]["Quote"];
export type Decision = components["schemas"]["Decision"];
export type ReplayReport = components["schemas"]["ReplayReport"];
export type Dashboard = components["schemas"]["Dashboard"];
export type DashboardAction = components["schemas"]["DashboardAction"];
export type MoneyValue = components["schemas"]["MoneyValue"];

/** A schema of the contract by name: `S<"Obligation">`. */
export type S<K extends keyof components["schemas"]> = components["schemas"][K];

/**
 * A failed request.
 *
 * It is an `Error` subclass so a stack trace survives, and it carries the
 * Problem document whole so nothing a caller might need is lost in translation.
 */
export class ZoikoTaxError extends Error {
  /** The registered reason code. This is the field to branch on. */
  readonly reasonCode: ReasonCode;
  /** The HTTP status, for logging and for the cases where it is the clearer signal. */
  readonly status: number;
  /**
   * Whether retrying this request unchanged could succeed. Only a transient
   * failure is retryable; everything else fails identically.
   */
  readonly retryable: boolean;
  /** This request's identifier. Quote it in a support request. */
  readonly requestId: string | undefined;
  /** The contract field at fault, for a validation failure. */
  readonly field: string | undefined;
  /** Seconds to wait before retrying, where the server said. */
  readonly retryAfter: number | undefined;
  /** The Problem Details document as received. */
  readonly problem: Problem;

  constructor(problem: Problem, retryAfter?: number) {
    super(problem.detail ?? problem.title);
    this.name = "ZoikoTaxError";
    this.problem = problem;
    this.reasonCode = problem.ztx_reason_code;
    this.status = problem.status;
    this.retryable = problem.ztx_retryable;
    this.requestId = problem.ztx_request_id;
    this.field = problem.ztx_field;
    this.retryAfter = retryAfter;
  }
}

/**
 * A failure that produced no Problem document at all: a DNS failure, a TLS
 * failure, a proxy that returned HTML, a cancelled request.
 *
 * It is a separate type because the caller's options differ. A `ZoikoTaxError`
 * is the service answering; this is not reaching it, and the reason is outside
 * anything the contract describes.
 */
export class ZoikoTaxTransportError extends Error {
  override readonly cause: unknown;
  /** The HTTP status, where there was a response at all. */
  readonly status: number | undefined;

  constructor(message: string, options: { cause?: unknown; status?: number } = {}) {
    super(message);
    this.name = "ZoikoTaxTransportError";
    this.cause = options.cause;
    this.status = options.status;
  }
}

/** The result of a call: the value, or the reason there is none. */
export type Result<T> = { ok: true; data: T } | { ok: false; error: ZoikoTaxError | ZoikoTaxTransportError };

/** How to reach a cell. */
export interface ClientOptions {
  /**
   * The cell's base URL, such as `https://eu-west-1.zoikotax.com`. A trailing
   * slash is tolerated.
   */
  baseUrl: string;
  /**
   * The fetch implementation. Defaults to the global one. Supplying it is how a
   * test drives this client without a network, and how a server-side caller
   * substitutes an instrumented client.
   */
  fetch?: typeof globalThis.fetch;
  /**
   * Headers added to every request. Useful for a correlation header a platform
   * requires; it is not where a credential goes, because there is no credential.
   */
  headers?: Record<string, string>;
  /**
   * Per-request timeout in milliseconds. Unset means no client-side timeout and
   * the platform's default applies.
   */
  timeoutMs?: number;
}

type Json = Record<string, unknown> | undefined;

/**
 * A client for one cell.
 *
 * One client per cell, because a cell is a residency boundary: data never
 * leaves its region, and a client that transparently failed over to another
 * cell would be moving a tenant's data across one.
 */
export class ZoikoTaxClient {
  readonly #baseUrl: string;
  readonly #fetch: typeof globalThis.fetch;
  readonly #headers: Record<string, string>;
  readonly #timeoutMs: number | undefined;

  constructor(options: ClientOptions) {
    if (!options.baseUrl) throw new TypeError("ZoikoTaxClient: baseUrl is required");
    this.#baseUrl = options.baseUrl.replace(/\/+$/, "");
    this.#fetch = options.fetch ?? globalThis.fetch;
    this.#headers = { ...options.headers };
    this.#timeoutMs = options.timeoutMs;
    if (typeof this.#fetch !== "function") {
      throw new TypeError("ZoikoTaxClient: no fetch implementation; supply options.fetch");
    }
  }

  // --- discovery ----------------------------------------------------------

  /**
   * Report this deployment's effective capability.
   *
   * Call it before treating any figure as authoritative: `authoritative` is
   * false until A4, and a deployment that reports false produces advisory
   * figures that must not be filed.
   */
  getCapabilities(signal?: AbortSignal): Promise<Result<Capabilities>> {
    return this.#send<Capabilities>("GET", "/v1/capabilities", undefined, signal);
  }

  // --- authentication -----------------------------------------------------

  /** Open a session. The response carries no token; the cookie is the session. */
  signIn(
    body: operations["signIn"]["requestBody"]["content"]["application/json"],
    signal?: AbortSignal,
  ): Promise<Result<Session>> {
    return this.#send<Session>("POST", "/v1/auth/sign-in", body, signal);
  }

  /** End the current session. */
  signOut(signal?: AbortSignal): Promise<Result<void>> {
    return this.#send<void>("POST", "/v1/auth/sign-out", undefined, signal);
  }

  /** Who am I, and until when. */
  getSession(signal?: AbortSignal): Promise<Result<Session>> {
    return this.#send<Session>("GET", "/v1/auth/session", undefined, signal);
  }

  /**
   * Change the current subject's password.
   *
   * Every session is revoked, including this one. Expect the next call to fail
   * with `UNAUTHENTICATED`, and send the user to sign in.
   */
  changePassword(
    body: operations["changePassword"]["requestBody"]["content"]["application/json"],
    signal?: AbortSignal,
  ): Promise<Result<void>> {
    return this.#send<void>("POST", "/v1/auth/password", body, signal);
  }

  // --- administration -----------------------------------------------------

  /** The authenticated subject's tenant. */
  getTenant(signal?: AbortSignal): Promise<Result<Tenant>> {
    return this.#send<Tenant>("GET", "/v1/admin/tenant", undefined, signal);
  }

  /** List the tenant's users. */
  listUsers(params: { limit?: number } = {}, signal?: AbortSignal): Promise<Result<{ users: User[] }>> {
    return this.#send<{ users: User[] }>("GET", `/v1/admin/users${query(params)}`, undefined, signal);
  }

  /** Create a user. Omitting the password creates an `INVITED` user. */
  createUser(
    body: operations["createUser"]["requestBody"]["content"]["application/json"],
    signal?: AbortSignal,
  ): Promise<Result<User>> {
    return this.#send<User>("POST", "/v1/admin/users", body, signal);
  }

  /** Enable or disable a user. Disabling revokes their sessions. */
  setUserStatus(userId: string, status: UserStatus, signal?: AbortSignal): Promise<Result<void>> {
    return this.#send<void>("POST", `/v1/admin/users/${encodeURIComponent(userId)}/status`, { status }, signal);
  }

  /** Grant a role. */
  grantRole(userId: string, role: Role, signal?: AbortSignal): Promise<Result<void>> {
    return this.#send<void>("POST", `/v1/admin/users/${encodeURIComponent(userId)}/roles`, { role }, signal);
  }

  /** Revoke a role. Revoking the last `ADMIN` is refused. */
  revokeRole(userId: string, role: Role, signal?: AbortSignal): Promise<Result<void>> {
    return this.#send<void>(
      "DELETE",
      `/v1/admin/users/${encodeURIComponent(userId)}/roles/${encodeURIComponent(role)}`,
      undefined,
      signal,
    );
  }

  /** List the tenant's sessions. `current` marks the caller's own. */
  listSessions(
    params: { limit?: number } = {},
    signal?: AbortSignal,
  ): Promise<Result<{ sessions: SessionSummary[] }>> {
    return this.#send<{ sessions: SessionSummary[] }>(
      "GET",
      `/v1/admin/sessions${query(params)}`,
      undefined,
      signal,
    );
  }

  /** Revoke a session. It is marked revoked, never deleted. */
  revokeSession(sessionId: string, signal?: AbortSignal): Promise<Result<void>> {
    return this.#send<void>("DELETE", `/v1/admin/sessions/${encodeURIComponent(sessionId)}`, undefined, signal);
  }

  /** Read the tenant's audit trail, most recent first. */
  listAudit(
    params: { limit?: number } = {},
    signal?: AbortSignal,
  ): Promise<Result<{ records: AuditRecord[] }>> {
    return this.#send<{ records: AuditRecord[] }>("GET", `/v1/admin/audit${query(params)}`, undefined, signal);
  }

  // --- determination ------------------------------------------------------

  /**
   * Report what the active content determines by location: the countries
   * covered, the services in each, and every component in force at `at`
   * (now by default). A country absent from it is `UNSUPPORTED`.
   */
  getCoverage(
    params: { at?: string } = {},
    signal?: AbortSignal,
  ): Promise<Result<S<"Coverage">>> {
    return this.#send<S<"Coverage">>("GET", `/v1/coverage${query(params)}`, undefined, signal);
  }

  /**
   * Quote a transaction without recording it.
   *
   * A quote is an estimate, never a decision: it is always
   * `authoritative: false` and must not be filed or invoiced from.
   */
  createQuote(
    body: operations["createQuote"]["requestBody"]["content"]["application/json"],
    signal?: AbortSignal,
  ): Promise<Result<Quote>> {
    return this.#send<Quote>("POST", "/v1/quotes", body, signal);
  }

  /**
   * Commit a transaction and record its decision.
   *
   * `idempotencyKey` is mandatory. Mint it before the first attempt and reuse
   * it, unchanged, on every retry of the same request (ADR-0013): that is what
   * makes a retry safe, and this client never retries on its own.
   */
  commitTransaction(
    idempotencyKey: string,
    body: operations["commitTransaction"]["requestBody"]["content"]["application/json"],
    signal?: AbortSignal,
  ): Promise<Result<Decision>> {
    return this.#send<Decision>("POST", "/v1/transactions:commit", body, signal, {
      "Idempotency-Key": idempotencyKey,
    });
  }

  /** Read a recorded decision. */
  getDecision(decisionId: string, signal?: AbortSignal): Promise<Result<Decision>> {
    return this.#send<Decision>("GET", `/v1/decisions/${encodeURIComponent(decisionId)}`, undefined, signal);
  }

  /**
   * Replay a recorded decision and compare it byte for byte. The verdict is
   * data, not an error; nothing is recorded.
   */
  replayDecision(decisionId: string, signal?: AbortSignal): Promise<Result<ReplayReport>> {
    return this.#send<ReplayReport>("POST", `/v1/replay/${encodeURIComponent(decisionId)}`, undefined, signal);
  }

  // --- determination ---------------------------------------------------------
  /** Adjust transaction. `POST /v1/transactions:adjust`. */
  adjustTransaction(idempotencyKey: string, body: S<"CommitRequest">, signal?: AbortSignal): Promise<Result<S<"Decision">>> {
    return this.#send<S<"Decision">>("POST", `/v1/transactions:adjust`, body, signal, { "Idempotency-Key": idempotencyKey });
  }

  /** Refund transaction. `POST /v1/transactions:refund`. */
  refundTransaction(idempotencyKey: string, body: S<"RefundRequest">, signal?: AbortSignal): Promise<Result<S<"Refund">>> {
    return this.#send<S<"Refund">>("POST", `/v1/transactions:refund`, body, signal, { "Idempotency-Key": idempotencyKey });
  }

  /** Get refund. `GET /v1/refunds/{refundId}`. */
  getRefund(refundId: string, signal?: AbortSignal): Promise<Result<S<"Refund">>> {
    return this.#send<S<"Refund">>("GET", `/v1/refunds/${encodeURIComponent(refundId)}`, undefined, signal);
  }

  /** Report refund. `POST /v1/refunds/{refundId}/reports`. */
  reportRefund(refundId: string, body: S<"RefundReportRequest">, signal?: AbortSignal): Promise<Result<S<"Refund">>> {
    return this.#send<S<"Refund">>("POST", `/v1/refunds/${encodeURIComponent(refundId)}/reports`, body, signal);
  }

  /** Submit batch. `POST /v1/batches`. */
  submitBatch(idempotencyKey: string, body: S<"BatchRequest">, signal?: AbortSignal): Promise<Result<S<"Job">>> {
    return this.#send<S<"Job">>("POST", `/v1/batches`, body, signal, { "Idempotency-Key": idempotencyKey });
  }

  /** Get job. `GET /v1/jobs/{jobId}`. */
  getJob(jobId: string, signal?: AbortSignal): Promise<Result<S<"Job">>> {
    return this.#send<S<"Job">>("GET", `/v1/jobs/${encodeURIComponent(jobId)}`, undefined, signal);
  }

  /** List decision journals. `GET /v1/decisions/{decisionId}/journals`. */
  listDecisionJournals(decisionId: string, signal?: AbortSignal): Promise<Result<S<"JournalList">>> {
    return this.#send<S<"JournalList">>("GET", `/v1/decisions/${encodeURIComponent(decisionId)}/journals`, undefined, signal);
  }

  /** Get decision inclusion. `GET /v1/decisions/{decisionId}/inclusion`. */
  getDecisionInclusion(decisionId: string, signal?: AbortSignal): Promise<Result<S<"InclusionProof">>> {
    return this.#send<S<"InclusionProof">>("GET", `/v1/decisions/${encodeURIComponent(decisionId)}/inclusion`, undefined, signal);
  }

  // --- subledger -------------------------------------------------------------
  /** Get subledger balances. `GET /v1/subledger/balances`. */
  getSubledgerBalances(signal?: AbortSignal): Promise<Result<S<"SubledgerBalances">>> {
    return this.#send<S<"SubledgerBalances">>("GET", `/v1/subledger/balances`, undefined, signal);
  }

  /** Get subledger period. `GET /v1/subledger/periods/{period}`. */
  getSubledgerPeriod(period: string, signal?: AbortSignal): Promise<Result<S<"SubledgerPeriod">>> {
    return this.#send<S<"SubledgerPeriod">>("GET", `/v1/subledger/periods/${encodeURIComponent(period)}`, undefined, signal);
  }

  /** Transition subledger period. `POST /v1/subledger/periods/{period}/transitions`. */
  transitionSubledgerPeriod(period: string, body: S<"PeriodTransitionRequest">, signal?: AbortSignal): Promise<Result<S<"SubledgerPeriod">>> {
    return this.#send<S<"SubledgerPeriod">>("POST", `/v1/subledger/periods/${encodeURIComponent(period)}/transitions`, body, signal);
  }

  /** Request period reopen. `POST /v1/subledger/periods/{period}/reopen-requests`. */
  requestPeriodReopen(period: string, body: S<"ReopenRequestBody">, signal?: AbortSignal): Promise<Result<S<"PeriodReopenRequest">>> {
    return this.#send<S<"PeriodReopenRequest">>("POST", `/v1/subledger/periods/${encodeURIComponent(period)}/reopen-requests`, body, signal);
  }

  /** Approve period reopen. `POST /v1/subledger/periods/{period}/reopen-requests/{requestId}/approval`. */
  approvePeriodReopen(period: string, requestId: string, signal?: AbortSignal): Promise<Result<S<"SubledgerPeriod">>> {
    return this.#send<S<"SubledgerPeriod">>("POST", `/v1/subledger/periods/${encodeURIComponent(period)}/reopen-requests/${encodeURIComponent(requestId)}/approval`, undefined, signal);
  }

  // --- obligations -----------------------------------------------------------
  /** List obligations. `GET /v1/obligations`. */
  listObligations(params: { status?: string; limit?: number } = {}, signal?: AbortSignal): Promise<Result<S<"ObligationList">>> {
    return this.#send<S<"ObligationList">>("GET", `/v1/obligations${query(params)}`, undefined, signal);
  }

  /** Get obligation. `GET /v1/obligations/{obligationId}`. */
  getObligation(obligationId: string, signal?: AbortSignal): Promise<Result<S<"Obligation">>> {
    return this.#send<S<"Obligation">>("GET", `/v1/obligations/${encodeURIComponent(obligationId)}`, undefined, signal);
  }

  /** Transition obligation. `POST /v1/obligations/{obligationId}/transitions`. */
  transitionObligation(obligationId: string, body: S<"ObligationTransitionRequest">, signal?: AbortSignal): Promise<Result<S<"Obligation">>> {
    return this.#send<S<"Obligation">>("POST", `/v1/obligations/${encodeURIComponent(obligationId)}/transitions`, body, signal);
  }

  // --- documents -------------------------------------------------------------
  /** Issue document. `POST /v1/documents`. */
  issueDocument(idempotencyKey: string, body: S<"DocumentRequest">, signal?: AbortSignal): Promise<Result<S<"FiscalDocument">>> {
    return this.#send<S<"FiscalDocument">>("POST", `/v1/documents`, body, signal, { "Idempotency-Key": idempotencyKey });
  }

  /** Get document. `GET /v1/documents/{documentId}`. */
  getDocument(documentId: string, signal?: AbortSignal): Promise<Result<S<"FiscalDocument">>> {
    return this.#send<S<"FiscalDocument">>("GET", `/v1/documents/${encodeURIComponent(documentId)}`, undefined, signal);
  }

  /** Correct document. `POST /v1/documents/{documentId}/corrections`. */
  correctDocument(documentId: string, idempotencyKey: string, body: S<"CorrectionRequest">, signal?: AbortSignal): Promise<Result<S<"FiscalDocument">>> {
    return this.#send<S<"FiscalDocument">>("POST", `/v1/documents/${encodeURIComponent(documentId)}/corrections`, body, signal, { "Idempotency-Key": idempotencyKey });
  }

  /** Get document lineage. `GET /v1/documents/{documentId}/lineage`. */
  getDocumentLineage(documentId: string, signal?: AbortSignal): Promise<Result<S<"DocumentLineage">>> {
    return this.#send<S<"DocumentLineage">>("GET", `/v1/documents/${encodeURIComponent(documentId)}/lineage`, undefined, signal);
  }

  // --- reconciliation --------------------------------------------------------
  /** Run reconciliation. `POST /v1/reconciliations`. */
  runReconciliation(body: S<"ReconciliationRequest">, signal?: AbortSignal): Promise<Result<S<"ReconciliationRun">>> {
    return this.#send<S<"ReconciliationRun">>("POST", `/v1/reconciliations`, body, signal);
  }

  /** Get reconciliation. `GET /v1/reconciliations/{runId}`. */
  getReconciliation(runId: string, signal?: AbortSignal): Promise<Result<S<"ReconciliationRun">>> {
    return this.#send<S<"ReconciliationRun">>("GET", `/v1/reconciliations/${encodeURIComponent(runId)}`, undefined, signal);
  }

  /** Resolve reconciliation item. `POST /v1/reconciliations/{runId}/items/{itemId}/resolution`. */
  resolveReconciliationItem(runId: string, itemId: string, body: S<"ResolutionRequest">, signal?: AbortSignal): Promise<Result<S<"ReconciliationItem">>> {
    return this.#send<S<"ReconciliationItem">>("POST", `/v1/reconciliations/${encodeURIComponent(runId)}/items/${encodeURIComponent(itemId)}/resolution`, body, signal);
  }

  // --- evidence --------------------------------------------------------------
  /** List seals. `GET /v1/seals`. */
  listSeals(params: { limit?: number } = {}, signal?: AbortSignal): Promise<Result<S<"SealList">>> {
    return this.#send<S<"SealList">>("GET", `/v1/seals${query(params)}`, undefined, signal);
  }

  /** Seal period. `POST /v1/seals`. */
  sealPeriod(body: S<"SealRequest">, signal?: AbortSignal): Promise<Result<S<"Seal">>> {
    return this.#send<S<"Seal">>("POST", `/v1/seals`, body, signal);
  }

  /** Get seal. `GET /v1/seals/{sealId}`. */
  getSeal(sealId: string, signal?: AbortSignal): Promise<Result<S<"SealDocument">>> {
    return this.#send<S<"SealDocument">>("GET", `/v1/seals/${encodeURIComponent(sealId)}`, undefined, signal);
  }

  /** Verify seal. `GET /v1/seals/{sealId}/verification`. */
  verifySeal(sealId: string, signal?: AbortSignal): Promise<Result<S<"SealVerification">>> {
    return this.#send<S<"SealVerification">>("GET", `/v1/seals/${encodeURIComponent(sealId)}/verification`, undefined, signal);
  }

  // --- retention -------------------------------------------------------------
  /** List retention policies. `GET /v1/retention/policies`. */
  listRetentionPolicies(signal?: AbortSignal): Promise<Result<S<"RetentionPolicyList">>> {
    return this.#send<S<"RetentionPolicyList">>("GET", `/v1/retention/policies`, undefined, signal);
  }

  /** Record retention policy. `POST /v1/retention/policies`. */
  recordRetentionPolicy(body: S<"RetentionPolicyRequest">, signal?: AbortSignal): Promise<Result<S<"RetentionPolicy">>> {
    return this.#send<S<"RetentionPolicy">>("POST", `/v1/retention/policies`, body, signal);
  }

  /** List legal holds. `GET /v1/legal-holds`. */
  listLegalHolds(signal?: AbortSignal): Promise<Result<S<"LegalHoldList">>> {
    return this.#send<S<"LegalHoldList">>("GET", `/v1/legal-holds`, undefined, signal);
  }

  /** Place legal hold. `POST /v1/legal-holds`. */
  placeLegalHold(body: S<"LegalHoldRequest">, signal?: AbortSignal): Promise<Result<S<"LegalHold">>> {
    return this.#send<S<"LegalHold">>("POST", `/v1/legal-holds`, body, signal);
  }

  /** Get legal hold. `GET /v1/legal-holds/{holdId}`. */
  getLegalHold(holdId: string, signal?: AbortSignal): Promise<Result<S<"LegalHold">>> {
    return this.#send<S<"LegalHold">>("GET", `/v1/legal-holds/${encodeURIComponent(holdId)}`, undefined, signal);
  }

  /** Change legal hold scope. `POST /v1/legal-holds/{holdId}/scope`. */
  changeLegalHoldScope(holdId: string, body: S<"LegalHoldScopeRequest">, signal?: AbortSignal): Promise<Result<S<"LegalHold">>> {
    return this.#send<S<"LegalHold">>("POST", `/v1/legal-holds/${encodeURIComponent(holdId)}/scope`, body, signal);
  }

  /** Release legal hold. `POST /v1/legal-holds/{holdId}/release`. */
  releaseLegalHold(holdId: string, body: S<"LegalHoldReleaseRequest">, signal?: AbortSignal): Promise<Result<S<"LegalHold">>> {
    return this.#send<S<"LegalHold">>("POST", `/v1/legal-holds/${encodeURIComponent(holdId)}/release`, body, signal);
  }

  /** Get decision retention. `GET /v1/decisions/{decisionId}/retention`. */
  getDecisionRetention(decisionId: string, signal?: AbortSignal): Promise<Result<S<"RetentionVerdict">>> {
    return this.#send<S<"RetentionVerdict">>("GET", `/v1/decisions/${encodeURIComponent(decisionId)}/retention`, undefined, signal);
  }

  // --- legal -----------------------------------------------------------------
  /** Get legal matrix. `GET /v1/legal/matrix`. */
  getLegalMatrix(signal?: AbortSignal): Promise<Result<S<"LegalMatrix">>> {
    return this.#send<S<"LegalMatrix">>("GET", `/v1/legal/matrix`, undefined, signal);
  }

  /** List authorizations. `GET /v1/legal/authorizations`. */
  listAuthorizations(signal?: AbortSignal): Promise<Result<S<"AuthorizationList">>> {
    return this.#send<S<"AuthorizationList">>("GET", `/v1/legal/authorizations`, undefined, signal);
  }

  /** Grant authorization. `POST /v1/legal/authorizations`. */
  grantAuthorization(body: S<"AuthorizationRequest">, signal?: AbortSignal): Promise<Result<S<"CustomerAuthorization">>> {
    return this.#send<S<"CustomerAuthorization">>("POST", `/v1/legal/authorizations`, body, signal);
  }

  /** Get authorization. `GET /v1/legal/authorizations/{authorizationId}`. */
  getAuthorization(authorizationId: string, signal?: AbortSignal): Promise<Result<S<"CustomerAuthorization">>> {
    return this.#send<S<"CustomerAuthorization">>("GET", `/v1/legal/authorizations/${encodeURIComponent(authorizationId)}`, undefined, signal);
  }

  /** Revoke authorization. `POST /v1/legal/authorizations/{authorizationId}/revocation`. */
  revokeAuthorization(authorizationId: string, body: S<"RevocationRequest">, signal?: AbortSignal): Promise<Result<S<"CustomerAuthorization">>> {
    return this.#send<S<"CustomerAuthorization">>("POST", `/v1/legal/authorizations/${encodeURIComponent(authorizationId)}/revocation`, body, signal);
  }

  /** Check authorization. `POST /v1/legal/authorization-checks`. */
  checkAuthorization(body: S<"AuthorizationCheckRequest">, signal?: AbortSignal): Promise<Result<S<"AuthorizationCheck">>> {
    return this.#send<S<"AuthorizationCheck">>("POST", `/v1/legal/authorization-checks`, body, signal);
  }

  // --- compliance ------------------------------------------------------------
  /** List compliance packages, in queue order. `GET /v1/compliance/packages`. */
  listCompliancePackages(params: { status?: string; limit?: number } = {}, signal?: AbortSignal): Promise<Result<S<"CompliancePackageList">>> {
    return this.#send<S<"CompliancePackageList">>("GET", `/v1/compliance/packages${query(params)}`, undefined, signal);
  }

  /** Create compliance package; `200` returns the one already made. `POST /v1/compliance/packages`. */
  createCompliancePackage(body: S<"CreateCompliancePackageRequest">, signal?: AbortSignal): Promise<Result<S<"CompliancePackage">>> {
    return this.#send<S<"CompliancePackage">>("POST", `/v1/compliance/packages`, body, signal);
  }

  /** Get compliance package. `GET /v1/compliance/packages/{packageId}`. */
  getCompliancePackage(packageId: string, signal?: AbortSignal): Promise<Result<S<"CompliancePackage">>> {
    return this.#send<S<"CompliancePackage">>("GET", `/v1/compliance/packages/${encodeURIComponent(packageId)}`, undefined, signal);
  }

  /** Prepare compliance package. `POST /v1/compliance/packages/{packageId}/preparation`. */
  prepareCompliancePackage(packageId: string, body: S<"PreparationRequest">, signal?: AbortSignal): Promise<Result<S<"CompliancePackage">>> {
    return this.#send<S<"CompliancePackage">>("POST", `/v1/compliance/packages/${encodeURIComponent(packageId)}/preparation`, body, signal);
  }

  /** Review compliance package. `POST /v1/compliance/packages/{packageId}/review`. */
  reviewCompliancePackage(packageId: string, body: S<"ComplianceReviewRequest">, signal?: AbortSignal): Promise<Result<S<"CompliancePackage">>> {
    return this.#send<S<"CompliancePackage">>("POST", `/v1/compliance/packages/${encodeURIComponent(packageId)}/review`, body, signal);
  }

  /** Approve compliance package. `POST /v1/compliance/packages/{packageId}/approval`. */
  approveCompliancePackage(packageId: string, body: S<"ComplianceApprovalRequest">, signal?: AbortSignal): Promise<Result<S<"CompliancePackage">>> {
    return this.#send<S<"CompliancePackage">>("POST", `/v1/compliance/packages/${encodeURIComponent(packageId)}/approval`, body, signal);
  }

  /** Submit compliance package. `POST /v1/compliance/packages/{packageId}/submission`. */
  submitCompliancePackage(packageId: string, body: S<"ComplianceSubmissionRequest">, signal?: AbortSignal): Promise<Result<S<"CompliancePackage">>> {
    return this.#send<S<"CompliancePackage">>("POST", `/v1/compliance/packages/${encodeURIComponent(packageId)}/submission`, body, signal);
  }

  /** Acknowledge compliance package. `POST /v1/compliance/packages/{packageId}/acknowledgement`. */
  acknowledgeCompliancePackage(packageId: string, body: S<"ComplianceAcknowledgementRequest">, signal?: AbortSignal): Promise<Result<S<"CompliancePackage">>> {
    return this.#send<S<"CompliancePackage">>("POST", `/v1/compliance/packages/${encodeURIComponent(packageId)}/acknowledgement`, body, signal);
  }

  /** Resolve compliance package. `POST /v1/compliance/packages/{packageId}/resolution`. */
  resolveCompliancePackage(packageId: string, body: S<"ComplianceResolutionRequest">, signal?: AbortSignal): Promise<Result<S<"CompliancePackage">>> {
    return this.#send<S<"CompliancePackage">>("POST", `/v1/compliance/packages/${encodeURIComponent(packageId)}/resolution`, body, signal);
  }

  /** Cancel compliance package. `POST /v1/compliance/packages/{packageId}/cancellation`. */
  cancelCompliancePackage(packageId: string, body: S<"ComplianceCancellationRequest">, signal?: AbortSignal): Promise<Result<S<"CompliancePackage">>> {
    return this.#send<S<"CompliancePackage">>("POST", `/v1/compliance/packages/${encodeURIComponent(packageId)}/cancellation`, body, signal);
  }

  // --- remittance ------------------------------------------------------------
  /** List payee versions. `GET /v1/remittances/beneficiaries`. */
  listBeneficiaries(signal?: AbortSignal): Promise<Result<S<"BeneficiaryList">>> {
    return this.#send<S<"BeneficiaryList">>("GET", `/v1/remittances/beneficiaries`, undefined, signal);
  }

  /** Record a payee version. `POST /v1/remittances/beneficiaries`. */
  createBeneficiary(body: S<"BeneficiaryRequest">, signal?: AbortSignal): Promise<Result<S<"Beneficiary">>> {
    return this.#send<S<"Beneficiary">>("POST", `/v1/remittances/beneficiaries`, body, signal);
  }

  /** Verify a payee version. `POST /v1/remittances/beneficiaries/{beneficiaryId}/verification`. */
  verifyBeneficiary(beneficiaryId: string, body: S<"BeneficiaryVerificationRequest">, signal?: AbortSignal): Promise<Result<S<"Beneficiary">>> {
    return this.#send<S<"Beneficiary">>("POST", `/v1/remittances/beneficiaries/${encodeURIComponent(beneficiaryId)}/verification`, body, signal);
  }

  /** Suspend a payee version. `POST /v1/remittances/beneficiaries/{beneficiaryId}/suspension`. */
  suspendBeneficiary(beneficiaryId: string, body: S<"BeneficiarySuspensionRequest">, signal?: AbortSignal): Promise<Result<S<"Beneficiary">>> {
    return this.#send<S<"Beneficiary">>("POST", `/v1/remittances/beneficiaries/${encodeURIComponent(beneficiaryId)}/suspension`, body, signal);
  }

  /** List payments, overdue first. `GET /v1/remittances`. */
  listRemittances(params: { status?: string; limit?: number } = {}, signal?: AbortSignal): Promise<Result<S<"RemittanceList">>> {
    return this.#send<S<"RemittanceList">>("GET", `/v1/remittances${query(params)}`, undefined, signal);
  }

  /** Make a payment instruction; `200` returns the live one already made. `POST /v1/remittances`. */
  createRemittance(body: S<"RemittanceRequest">, signal?: AbortSignal): Promise<Result<S<"Remittance">>> {
    return this.#send<S<"Remittance">>("POST", `/v1/remittances`, body, signal);
  }

  /** Get payment. `GET /v1/remittances/{remittanceId}`. */
  getRemittance(remittanceId: string, signal?: AbortSignal): Promise<Result<S<"Remittance">>> {
    return this.#send<S<"Remittance">>("GET", `/v1/remittances/${encodeURIComponent(remittanceId)}`, undefined, signal);
  }

  /** Approve payment. `POST /v1/remittances/{remittanceId}/approval`. */
  approveRemittance(remittanceId: string, body: S<"RemittanceApprovalRequest">, signal?: AbortSignal): Promise<Result<S<"Remittance">>> {
    return this.#send<S<"Remittance">>("POST", `/v1/remittances/${encodeURIComponent(remittanceId)}/approval`, body, signal);
  }

  /** Submit payment. `POST /v1/remittances/{remittanceId}/submission`. */
  submitRemittance(remittanceId: string, body: S<"RemittanceSubmissionRequest">, signal?: AbortSignal): Promise<Result<S<"Remittance">>> {
    return this.#send<S<"Remittance">>("POST", `/v1/remittances/${encodeURIComponent(remittanceId)}/submission`, body, signal);
  }

  /** Record provider status. `POST /v1/remittances/{remittanceId}/provider-status`. */
  recordRemittanceStatus(remittanceId: string, body: S<"RemittanceStatusRequest">, signal?: AbortSignal): Promise<Result<S<"Remittance">>> {
    return this.#send<S<"Remittance">>("POST", `/v1/remittances/${encodeURIComponent(remittanceId)}/provider-status`, body, signal);
  }

  /** Resolve uncertain payment. `POST /v1/remittances/{remittanceId}/resolution`. */
  resolveRemittance(remittanceId: string, body: S<"RemittanceResolutionRequest">, signal?: AbortSignal): Promise<Result<S<"Remittance">>> {
    return this.#send<S<"Remittance">>("POST", `/v1/remittances/${encodeURIComponent(remittanceId)}/resolution`, body, signal);
  }

  /** Record returned payment. `POST /v1/remittances/{remittanceId}/return`. */
  returnRemittance(remittanceId: string, body: S<"RemittanceReturnRequest">, signal?: AbortSignal): Promise<Result<S<"Remittance">>> {
    return this.#send<S<"Remittance">>("POST", `/v1/remittances/${encodeURIComponent(remittanceId)}/return`, body, signal);
  }

  /** Cancel payment. `POST /v1/remittances/{remittanceId}/cancellation`. */
  cancelRemittance(remittanceId: string, body: S<"RemittanceCancellationRequest">, signal?: AbortSignal): Promise<Result<S<"Remittance">>> {
    return this.#send<S<"Remittance">>("POST", `/v1/remittances/${encodeURIComponent(remittanceId)}/cancellation`, body, signal);
  }

  // --- shadow ----------------------------------------------------------------
  /** List comparator profiles. `GET /v1/shadow/comparators`. */
  listComparators(signal?: AbortSignal): Promise<Result<S<"ComparatorList">>> {
    return this.#send<S<"ComparatorList">>("GET", `/v1/shadow/comparators`, undefined, signal);
  }

  /** Record a comparator profile. `POST /v1/shadow/comparators`. */
  createComparator(body: S<"ComparatorRequest">, signal?: AbortSignal): Promise<Result<S<"Comparator">>> {
    return this.#send<S<"Comparator">>("POST", `/v1/shadow/comparators`, body, signal);
  }

  /** List shadow runs, newest first, as summaries. `GET /v1/shadow/runs`. */
  listShadowRuns(params: { limit?: number } = {}, signal?: AbortSignal): Promise<Result<S<"ShadowRunList">>> {
    return this.#send<S<"ShadowRunList">>("GET", `/v1/shadow/runs${query(params)}`, undefined, signal);
  }

  /** Compare a comparator's results with ZoikoTax's decisions. `POST /v1/shadow/runs`. */
  executeShadowRun(body: S<"ShadowRunRequest">, signal?: AbortSignal): Promise<Result<S<"ShadowRun">>> {
    return this.#send<S<"ShadowRun">>("POST", `/v1/shadow/runs`, body, signal);
  }

  /** Get shadow run, with its cases. `GET /v1/shadow/runs/{shadowRunId}`. */
  getShadowRun(shadowRunId: string, signal?: AbortSignal): Promise<Result<S<"ShadowRun">>> {
    return this.#send<S<"ShadowRun">>("GET", `/v1/shadow/runs/${encodeURIComponent(shadowRunId)}`, undefined, signal);
  }

  /** Move a discrepancy's investigation. `POST /v1/shadow/discrepancies/{discrepancyId}/transitions`. */
  moveDiscrepancy(discrepancyId: string, body: S<"DiscrepancyTransitionRequest">, signal?: AbortSignal): Promise<Result<S<"Discrepancy">>> {
    return this.#send<S<"Discrepancy">>("POST", `/v1/shadow/discrepancies/${encodeURIComponent(discrepancyId)}/transitions`, body, signal);
  }

  // --- migration -------------------------------------------------------------
  /** List migration programs. `GET /v1/migrations/programs`. */
  listMigrationPrograms(signal?: AbortSignal): Promise<Result<S<"MigrationProgramList">>> {
    return this.#send<S<"MigrationProgramList">>("GET", `/v1/migrations/programs`, undefined, signal);
  }

  /** Create a migration program. `POST /v1/migrations/programs`. */
  createMigrationProgram(body: S<"MigrationProgramRequest">, signal?: AbortSignal): Promise<Result<S<"MigrationProgram">>> {
    return this.#send<S<"MigrationProgram">>("POST", `/v1/migrations/programs`, body, signal);
  }

  /** Get a program with its evidence and readiness. `GET /v1/migrations/programs/{migrationProgramId}`. */
  getMigrationProgram(migrationProgramId: string, signal?: AbortSignal): Promise<Result<S<"MigrationProgramDetail">>> {
    return this.#send<S<"MigrationProgramDetail">>("GET", `/v1/migrations/programs/${encodeURIComponent(migrationProgramId)}`, undefined, signal);
  }

  /** Register a source snapshot. `POST /v1/migrations/programs/{migrationProgramId}/snapshots`. */
  registerMigrationSnapshot(migrationProgramId: string, body: S<"MigrationSnapshotRequest">, signal?: AbortSignal): Promise<Result<S<"MigrationSnapshot">>> {
    return this.#send<S<"MigrationSnapshot">>("POST", `/v1/migrations/programs/${encodeURIComponent(migrationProgramId)}/snapshots`, body, signal);
  }

  /** Record the next mapping version. `POST /v1/migrations/programs/{migrationProgramId}/mappings`. */
  createMigrationMapping(migrationProgramId: string, body: S<"MigrationMappingRequest">, signal?: AbortSignal): Promise<Result<S<"MigrationMapping">>> {
    return this.#send<S<"MigrationMapping">>("POST", `/v1/migrations/programs/${encodeURIComponent(migrationProgramId)}/mappings`, body, signal);
  }

  /** Convert a snapshot under a mapping. `POST /v1/migrations/programs/{migrationProgramId}/runs`. */
  executeMigrationRun(migrationProgramId: string, body: S<"MigrationRunRequest">, signal?: AbortSignal): Promise<Result<S<"MigrationRun">>> {
    return this.#send<S<"MigrationRun">>("POST", `/v1/migrations/programs/${encodeURIComponent(migrationProgramId)}/runs`, body, signal);
  }

  /** Move the cutover. `POST /v1/migrations/programs/{migrationProgramId}/cutover`. */
  moveMigrationCutover(migrationProgramId: string, body: S<"CutoverRequest">, signal?: AbortSignal): Promise<Result<S<"MigrationProgramDetail">>> {
    return this.#send<S<"MigrationProgramDetail">>("POST", `/v1/migrations/programs/${encodeURIComponent(migrationProgramId)}/cutover`, body, signal);
  }

  /** Get a run with its records. `GET /v1/migrations/runs/{migrationRunId}`. */
  getMigrationRun(migrationRunId: string, signal?: AbortSignal): Promise<Result<S<"MigrationRun">>> {
    return this.#send<S<"MigrationRun">>("GET", `/v1/migrations/runs/${encodeURIComponent(migrationRunId)}`, undefined, signal);
  }

  /** Decide an exception. `POST /v1/migrations/exceptions/{migrationExceptionId}/decision`. */
  decideMigrationException(migrationExceptionId: string, body: S<"ExceptionDecisionRequest">, signal?: AbortSignal): Promise<Result<S<"MigrationException">>> {
    return this.#send<S<"MigrationException">>("POST", `/v1/migrations/exceptions/${encodeURIComponent(migrationExceptionId)}/decision`, body, signal);
  }

  // --- classification --------------------------------------------------------
  /** Propose classification. `POST /v1/classifications:propose`. */
  proposeClassification(body: S<"ClassificationProposalRequest">, signal?: AbortSignal): Promise<Result<S<"ClassificationProposal">>> {
    return this.#send<S<"ClassificationProposal">>("POST", `/v1/classifications:propose`, body, signal);
  }

  // --- dashboard ----------------------------------------------------------

  /**
   * The Tax Command Center for one legal period, `YYYY-MM`; the current month
   * in UTC when omitted. Read-only aggregates, amounts per currency and never
   * summed across currencies.
   */
  getDashboard(params: { period?: string } = {}, signal?: AbortSignal): Promise<Result<Dashboard>> {
    return this.#send<Dashboard>("GET", `/v1/dashboard${query(params)}`, undefined, signal);
  }

  // --- transport ----------------------------------------------------------

  async #send<T>(
    method: string,
    path: string,
    body: Json,
    signal?: AbortSignal,
    extraHeaders?: Record<string, string>,
  ): Promise<Result<T>> {
    const headers: Record<string, string> = {
      // Problem Details first, because an error is the response whose shape
      // this client most needs to be sure of.
      Accept: "application/problem+json, application/json",
      ...this.#headers,
      ...extraHeaders,
    };
    if (body !== undefined) headers["Content-Type"] = "application/json";

    const controller = this.#timeoutMs === undefined ? undefined : new AbortController();
    const timer =
      controller === undefined
        ? undefined
        : setTimeout(() => controller.abort(new Error(`timed out after ${this.#timeoutMs}ms`)), this.#timeoutMs);
    if (controller && signal) {
      signal.addEventListener("abort", () => controller.abort(signal.reason), { once: true });
    }

    // Called as a plain function, never as `this.#fetch(...)`: a browser's
    // fetch throws "Illegal invocation" when `this` is anything but the
    // window or undefined, and the client is neither. Node's fetch does not
    // care, which is how this once passed every test and failed every call
    // in a browser.
    const fetchImpl = this.#fetch;
    let response: Response;
    try {
      response = await fetchImpl(`${this.#baseUrl}${path}`, {
        method,
        headers,
        body: body === undefined ? null : JSON.stringify(body),
        // The session is an HttpOnly cookie. Without this a browser omits it
        // cross-origin and every call is UNAUTHENTICATED for a reason that
        // looks like a server problem.
        credentials: "include",
        signal: controller?.signal ?? signal ?? null,
      });
    } catch (cause) {
      return { ok: false, error: new ZoikoTaxTransportError(`${method} ${path} did not reach the service`, { cause }) };
    } finally {
      if (timer !== undefined) clearTimeout(timer);
    }

    if (response.status === 204 || response.status === 205) {
      return { ok: true, data: undefined as T };
    }

    const text = await response.text().catch(() => "");
    let parsed: unknown;
    try {
      parsed = text === "" ? undefined : JSON.parse(text);
    } catch (cause) {
      return {
        ok: false,
        error: new ZoikoTaxTransportError(
          `${method} ${path} returned ${response.status} with a body that is not JSON`,
          { cause, status: response.status },
        ),
      };
    }

    if (response.ok) {
      return { ok: true, data: parsed as T };
    }

    if (isProblem(parsed)) {
      const retryAfter = Number.parseInt(response.headers.get("Retry-After") ?? "", 10);
      return {
        ok: false,
        error: new ZoikoTaxError(parsed, Number.isNaN(retryAfter) ? undefined : retryAfter),
      };
    }

    // A non-Problem error body is something between the client and the cell: a
    // load balancer, a proxy, a WAF. Reporting it as a ZoikoTaxError would
    // attribute it to the service and give it a reason code nobody registered.
    return {
      ok: false,
      error: new ZoikoTaxTransportError(
        `${method} ${path} returned ${response.status} without a Problem Details body; ` +
          `something between this client and the cell answered`,
        { status: response.status },
      ),
    };
  }
}

/**
 * Narrow an unknown body to a Problem.
 *
 * It checks the two fields a caller acts on rather than validating the whole
 * document. A stricter check would reject a Problem carrying an extension this
 * SDK release predates, and ADR-0010 §2.6 makes additions the normal case.
 */
function isProblem(value: unknown): value is Problem {
  if (typeof value !== "object" || value === null) return false;
  const p = value as Record<string, unknown>;
  return typeof p["ztx_reason_code"] === "string" && typeof p["status"] === "number";
}

function query(params: Record<string, string | number | undefined>): string {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined) search.set(key, String(value));
  }
  const rendered = search.toString();
  return rendered === "" ? "" : `?${rendered}`;
}

/**
 * Unwrap a result, throwing on failure.
 *
 * For callers that prefer exceptions, and for tests. It is a free function
 * rather than a client option, so that the client has one behaviour and the
 * choice is visible at the call site.
 *
 * @example
 * const capabilities = unwrap(await client.getCapabilities());
 */
export function unwrap<T>(result: Result<T>): T {
  if (result.ok) return result.data;
  throw result.error;
}
