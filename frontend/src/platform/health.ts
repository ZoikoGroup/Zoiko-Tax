/**
 * The only module in this application that calls fetch.
 *
 * ADR-0019 C4 makes the generated TypeScript SDK the API client and prohibits
 * hand-written calls against ZoikoTax endpoints. The SDK is generated from
 * contracts/openapi, which opens in W2 lane K and does not exist yet.
 *
 * The narrow exception taken here is the platform health surface — /healthz and
 * /readyz are operational endpoints, not part of the fiscal contract, and they
 * are the only thing this application calls. Every other call waits for the
 * SDK. The eslint config restricts fetch to this file so the exception cannot
 * quietly widen into a habit.
 */

/** ADR-0016 §2.2: an uncertain state is not an error and not a success. Each
 *  of these renders as itself (C3), and none of them renders as a spinner. */
export type CellState = 'checking' | 'live' | 'degraded' | 'unreachable'

export interface CellHealth {
  readonly state: CellState
  /** What the cell said, verbatim, when it said anything. */
  readonly detail: string
  readonly checkedAt: string
}

const TIMEOUT_MS = 5000

async function probe(path: string): Promise<{ ok: boolean; body: string }> {
  const controller = new AbortController()
  const timer = setTimeout(() => { controller.abort() }, TIMEOUT_MS)
  try {
    const response = await fetch(path, { signal: controller.signal, headers: { accept: 'application/json' } })
    return { ok: response.ok, body: (await response.text()).trim() }
  } finally {
    clearTimeout(timer)
  }
}

/**
 * Liveness and readiness are separate questions, so they are asked separately.
 * A cell that is live but not ready is degraded, and saying so is the whole
 * point of C3 — reporting it as "loading" would tell the user to wait for
 * something that is not coming on its own.
 */
export async function readCellHealth(): Promise<CellHealth> {
  const checkedAt = new Date().toISOString()
  try {
    const [live, ready] = await Promise.all([probe('/api/healthz'), probe('/api/readyz')])
    if (live.ok && ready.ok) {
      return { state: 'live', detail: ready.body || live.body, checkedAt }
    }
    if (live.ok) {
      return { state: 'degraded', detail: ready.body || 'live, not ready', checkedAt }
    }
    return { state: 'degraded', detail: live.body || 'not live', checkedAt }
  } catch (cause) {
    return {
      state: 'unreachable',
      detail: cause instanceof Error ? cause.message : 'no response',
      checkedAt,
    }
  }
}
