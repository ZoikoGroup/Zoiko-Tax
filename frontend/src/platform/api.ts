/**
 * The administration client.
 *
 * ADR-0019 C4 makes the generated TypeScript SDK the API client and prohibits
 * hand-written calls against ZoikoTax endpoints. That rule is about the fiscal
 * contract — endpoints that carry money, that have an error taxonomy to bypass,
 * and that the five mandated SDKs must expose consistently. The SDK is generated
 * from contracts/openapi, which opens in W2 lane K.
 *
 * This module calls the administration and session surface, which is not the
 * fiscal contract: no endpoint here carries a fiscal amount, and the types below
 * have no way to express one. The eslint exception is widened to this one file
 * rather than to a directory, for the same reason platform/health.ts was pinned
 * to a file — so the exception cannot quietly become a habit. When the generator
 * lands, this module is replaced by generated code and the exception goes away.
 *
 * Two properties this file is responsible for:
 *
 *  - Credentials travel as an httpOnly cookie, so nothing here reads or writes
 *    a token. `credentials: 'same-origin'` is the whole of the auth code, and
 *    ADR-0019 C8 holds for free: there is nothing to put in browser storage,
 *    and a cross-site scripting flaw has no session to steal.
 *  - Every failure arrives as an RFC 9457 Problem (ADR-0016 §2.5) and is
 *    surfaced as a typed ApiError carrying the reason code, so a caller
 *    branches on `reasonCode` and never on a message string.
 */

/** An RFC 9457 Problem Details document, with the ztx_ extensions. */
export interface Problem {
  readonly type: string
  readonly title: string
  readonly status: number
  readonly detail?: string | undefined
  readonly instance?: string | undefined
  readonly ztx_reason_code: string
  readonly ztx_request_id?: string | undefined
  readonly ztx_field?: string | undefined
  readonly ztx_retryable: boolean
}

/**
 * A failed request.
 *
 * `reasonCode` is the registered vocabulary from ADR-0016 §2.4 and is what
 * callers branch on. `retryable` is carried rather than inferred from the
 * status, because the service states it.
 */
export class ApiError extends Error {
  readonly status: number
  readonly reasonCode: string
  // `| undefined` explicitly: tsconfig sets exactOptionalPropertyTypes, which
  // makes "absent" and "present and undefined" different types. These are
  // copied from a Problem where the key may genuinely be absent, so they have
  // to admit both.
  readonly field: string | undefined
  readonly retryable: boolean
  readonly requestId: string | undefined

  constructor(problem: Problem) {
    super(problem.detail ?? problem.title)
    this.name = 'ApiError'
    this.status = problem.status
    this.reasonCode = problem.ztx_reason_code
    this.field = problem.ztx_field
    this.retryable = problem.ztx_retryable
    this.requestId = problem.ztx_request_id
  }

  /** True when the caller is not signed in, or no longer is. */
  get isUnauthenticated(): boolean {
    return (
      this.status === 401 ||
      this.reasonCode === 'UNAUTHENTICATED' ||
      this.reasonCode === 'SESSION_EXPIRED' ||
      this.reasonCode === 'SESSION_REVOKED'
    )
  }
}

/** Raised when the cell could not be reached at all, which is not the same as
 *  the cell refusing the request — C3 renders the two differently. */
export class UnreachableError extends Error {
  constructor(cause: unknown) {
    super(cause instanceof Error ? cause.message : 'the cell did not respond')
    this.name = 'UnreachableError'
  }
}

const TIMEOUT_MS = 10000

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const controller = new AbortController()
  const timer = setTimeout(() => { controller.abort() }, TIMEOUT_MS)

  let response: Response
  try {
    response = await fetch(`/api${path}`, {
      method,
      signal: controller.signal,
      // The session cookie. Nothing else authenticates this request, and there
      // is no header to forget to set.
      credentials: 'same-origin',
      headers: body === undefined
        ? { accept: 'application/json' }
        : { accept: 'application/json', 'content-type': 'application/json' },
      // Spread rather than an undefined value: under exactOptionalPropertyTypes
      // a body of undefined is not the same as no body.
      ...(body === undefined ? {} : { body: JSON.stringify(body) }),
    })
  } catch (cause) {
    throw new UnreachableError(cause)
  } finally {
    clearTimeout(timer)
  }

  if (response.status === 204) {
    return undefined as T
  }

  const text = await response.text()
  if (!response.ok) {
    try {
      throw new ApiError(JSON.parse(text) as Problem)
    } catch (cause) {
      if (cause instanceof ApiError) throw cause
      // A non-Problem error body means something in front of the cell answered
      // — a proxy, a load balancer. Reporting it as unreachable is honest:
      // the cell did not refuse this, it may never have seen it.
      throw new UnreachableError(new Error(`HTTP ${String(response.status)}`))
    }
  }
  return text === '' ? (undefined as T) : (JSON.parse(text) as T)
}

// ---------------------------------------------------------------------------
// Types. They mirror the wire, not the domain.
// ---------------------------------------------------------------------------

export type Role = 'ADMIN' | 'OPERATOR' | 'ANALYST' | 'AUDITOR'
export const ALL_ROLES: readonly Role[] = ['ADMIN', 'OPERATOR', 'ANALYST', 'AUDITOR']

export type UserStatus = 'ACTIVE' | 'INVITED' | 'DISABLED'

export interface Tenant {
  readonly id: string
  readonly slug: string
  readonly displayName: string
  readonly residencyRegion: string
  readonly status: string
}

export interface User {
  readonly id: string
  readonly email: string
  readonly displayName: string
  readonly status: UserStatus
  readonly roles: readonly Role[]
  readonly createdAt: string
}

export interface Session {
  readonly id: string
  readonly userId: string
  readonly createdAt: string
  readonly expiresAt: string
  readonly userAgent?: string | undefined
  readonly clientIp?: string | undefined
  readonly revoked: boolean
  /** True for the session making this request, so the UI can warn before
   *  revoking the one it is using. */
  readonly current: boolean
}

export interface AuditRecord {
  readonly id: string
  readonly actorUserId?: string | undefined
  readonly action: string
  readonly subjectType: string
  readonly subjectId: string
  readonly detail: string
  readonly recordedAt: string
}

export interface SessionInfo {
  readonly tenant: Tenant
  readonly user: User
  readonly expiresAt: string
}

export interface Capabilities {
  readonly cell: string
  readonly region: string
  readonly environment: string
  readonly trains: Readonly<Record<string, string>>
  readonly canonProfile: string
  /** False until A4. The UI says so rather than letting anyone assume. */
  readonly authoritative: boolean
  readonly reasonCodes: readonly string[]
}

// ---------------------------------------------------------------------------
// Operations
// ---------------------------------------------------------------------------

export const api = {
  signIn: (tenant: string, email: string, password: string): Promise<SessionInfo> =>
    request<SessionInfo>('POST', '/v1/auth/sign-in', { tenant, email, password }),

  signOut: (): Promise<void> => request<void>('POST', '/v1/auth/sign-out'),

  session: (): Promise<SessionInfo> => request<SessionInfo>('GET', '/v1/auth/session'),

  changePassword: (currentPassword: string, newPassword: string): Promise<void> =>
    request<void>('POST', '/v1/auth/password', { currentPassword, newPassword }),

  capabilities: (): Promise<Capabilities> => request<Capabilities>('GET', '/v1/capabilities'),

  listUsers: (): Promise<{ users: User[] }> => request<{ users: User[] }>('GET', '/v1/admin/users'),

  // password is omitted rather than undefined for an invited user, which is
  // the distinction the server reads: absent means "no credential yet", and
  // the request schema rejects an unknown or null field outright.
  createUser: (input: {
    email: string
    displayName: string
    roles: Role[]
    password?: string | undefined
  }): Promise<User> => request<User>('POST', '/v1/admin/users', input),

  setUserStatus: (userId: string, status: UserStatus): Promise<void> =>
    request<void>('POST', `/v1/admin/users/${encodeURIComponent(userId)}/status`, { status }),

  grantRole: (userId: string, role: Role): Promise<void> =>
    request<void>('POST', `/v1/admin/users/${encodeURIComponent(userId)}/roles`, { role }),

  revokeRole: (userId: string, role: Role): Promise<void> =>
    request<void>(
      'DELETE',
      `/v1/admin/users/${encodeURIComponent(userId)}/roles/${encodeURIComponent(role)}`,
    ),

  listSessions: (): Promise<{ sessions: Session[] }> =>
    request<{ sessions: Session[] }>('GET', '/v1/admin/sessions'),

  revokeSession: (sessionId: string): Promise<void> =>
    request<void>('DELETE', `/v1/admin/sessions/${encodeURIComponent(sessionId)}`),

  listAudit: (): Promise<{ records: AuditRecord[] }> =>
    request<{ records: AuditRecord[] }>('GET', '/v1/admin/audit?limit=100'),
}

/** Renders an unknown thrown value as something safe to show a user. */
export function describeError(error: unknown): string {
  if (error instanceof ApiError) return error.message
  if (error instanceof UnreachableError) return `The cell did not respond: ${error.message}`
  return 'Something went wrong.'
}
