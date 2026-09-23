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

  // --- transport ----------------------------------------------------------

  async #send<T>(method: string, path: string, body: Json, signal?: AbortSignal): Promise<Result<T>> {
    const headers: Record<string, string> = {
      // Problem Details first, because an error is the response whose shape
      // this client most needs to be sure of.
      Accept: "application/problem+json, application/json",
      ...this.#headers,
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

    let response: Response;
    try {
      response = await this.#fetch(`${this.#baseUrl}${path}`, {
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
