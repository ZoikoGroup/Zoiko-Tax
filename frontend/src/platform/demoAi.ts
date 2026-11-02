/**
 * The ONE hand-written fetch for the DEMO AI service -- demo scope only.
 *
 * Why this file exists, and why it is not the generated SDK: the "Reports
 * (demo)" page talks to intelligence/tools/report_ai_http.py, a stdlib demo
 * server that is not part of the ZoikoTax API contract. It has no OpenAPI
 * description, so nothing generates an SDK method for it, and ADR-0019 C4
 * ("the fiscal surface is reached through the SDK") is about the fiscal
 * contract, which this is not: it serves a synthetic, labelled-as-such
 * fixture and an advisory explanation, never a determination, an obligation
 * or a decision.
 *
 * It is the only place the page touches the network. It returns the parsed
 * body as `unknown` (the caller validates its shape) and never throws, so a
 * demo service that is down cannot break the page around it. A request that
 * takes longer than DEMO_AI_TIMEOUT_MS is cut off and reported as
 * 'unreachable', which the page shows as its ordinary "unavailable" state.
 */

/** Published to the host (loopback only) by docker-compose.live-demo.yml. */
// Deviation from ADR-0019 C4 documented: see intelligence/LIVE_DEMO.md § "Deviations".
export const DEMO_AI_BASE_URL = 'http://localhost:18090'

/** How long one request may take, reading the body included. */
export const DEMO_AI_TIMEOUT_MS = 30_000

export type DemoAiResult =
  | { readonly ok: true; readonly body: unknown }
  | { readonly ok: false; readonly reason: 'unreachable' | 'rejected' | 'aborted' }

export async function demoAiRequest(
  path: '/demo/fixture' | '/demo/explain',
  options: { readonly body?: unknown; readonly signal?: AbortSignal } = {},
): Promise<DemoAiResult> {
  const init: RequestInit = { method: options.body === undefined ? 'GET' : 'POST' }
  if (options.body !== undefined) {
    init.headers = { 'Content-Type': 'application/json' }
    init.body = JSON.stringify(options.body)
  }
  // One controller for both ways out: the caller's signal (a superseded or
  // closed request: 'aborted') and the timeout (a slow service: 'unreachable').
  const controller = new AbortController()
  const abort = () => { controller.abort() }
  const caller = options.signal
  if (caller?.aborted === true) {
    abort()
  }
  caller?.addEventListener('abort', abort, { once: true })
  const timer = setTimeout(abort, DEMO_AI_TIMEOUT_MS)
  init.signal = controller.signal
  try {
    // eslint-disable-next-line no-restricted-globals -- the single, documented exception above
    const response = await fetch(`${DEMO_AI_BASE_URL}${path}`, init)
    if (!response.ok) {
      return { ok: false, reason: 'rejected' }
    }
    const body: unknown = await response.json()
    return { ok: true, body }
  } catch {
    return { ok: false, reason: caller?.aborted === true ? 'aborted' : 'unreachable' }
  } finally {
    clearTimeout(timer)
    caller?.removeEventListener('abort', abort)
  }
}
