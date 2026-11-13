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
 * demo service that is down cannot break the page around it. Each way a call
 * can end is its own reason, so the caller can tell them apart: 'http' (the
 * service answered with a non-2xx status, which is carried), 'unreachable' (no
 * answer at all), 'timeout' (DEMO_AI_TIMEOUT_MS passed; an answer that arrives
 * after that is dropped), 'unreadable' (a 2xx whose body is not JSON) and
 * 'aborted' (the caller cancelled).
 */

/** Published to the host (loopback only) by docker-compose.live-demo.yml. */
// Deviation from ADR-0019 C4 documented: see intelligence/LIVE_DEMO.md § "Deviations".
export const DEMO_AI_BASE_URL = 'http://localhost:18090'

/** How long one request may take, reading the body included. */
export const DEMO_AI_TIMEOUT_MS = 30_000

export type DemoAiResult =
  | { readonly ok: true; readonly body: unknown }
  | { readonly ok: false; readonly reason: 'http'; readonly status: number }
  | { readonly ok: false; readonly reason: 'unreachable' | 'unreadable' | 'aborted' | 'timeout' }

export async function demoAiRequest(
  path: '/demo/fixture' | '/demo/explain' | '/demo/classify',
  options: { readonly body?: unknown; readonly signal?: AbortSignal } = {},
): Promise<DemoAiResult> {
  const init: RequestInit = { method: options.body === undefined ? 'GET' : 'POST' }
  if (options.body !== undefined) {
    init.headers = { 'Content-Type': 'application/json' }
    init.body = JSON.stringify(options.body)
  }
  // One controller for both ways out: the caller's signal (a superseded or
  // closed request: 'aborted') and the timeout (a slow service: 'timeout').
  const controller = new AbortController()
  let timedOut = false
  const abort = () => { controller.abort() }
  const onTimeout = () => { timedOut = true; controller.abort() }
  const caller = options.signal
  if (caller?.aborted === true) {
    abort()
  }
  caller?.addEventListener('abort', abort, { once: true })
  const timer = setTimeout(onTimeout, DEMO_AI_TIMEOUT_MS)
  init.signal = controller.signal
  // Read at each step, because a closure sets them while an await is pending.
  const cutOff = (): DemoAiResult | null => {
    if (caller?.aborted === true) return { ok: false, reason: 'aborted' }
    if (timedOut) return { ok: false, reason: 'timeout' }
    return null
  }
  try {
    // eslint-disable-next-line no-restricted-globals -- the single, documented exception above
    const response = await fetch(`${DEMO_AI_BASE_URL}${path}`, init)
    // An answer that comes after the timer or after a cancel is not used, even
    // if the transport delivered it: the caller has already been told.
    const lateFetch = cutOff()
    if (lateFetch !== null) return lateFetch
    if (!response.ok) {
      return { ok: false, reason: 'http', status: response.status }
    }
    let body: unknown
    try {
      body = await response.json()
    } catch {
      // Cut off while reading is a timeout or a cancel; anything else is a body that is not JSON.
      return cutOff() ?? { ok: false, reason: 'unreadable' }
    }
    return cutOff() ?? { ok: true, body }
  } catch {
    return cutOff() ?? { ok: false, reason: 'unreachable' }
  } finally {
    clearTimeout(timer)
    caller?.removeEventListener('abort', abort)
  }
}
