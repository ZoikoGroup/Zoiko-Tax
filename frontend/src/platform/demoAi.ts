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
 * demo service that is down cannot break the page around it.
 */

/** Published to the host by docker-compose.live-demo.yml. */
export const DEMO_AI_BASE_URL = 'http://localhost:18090'

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
  if (options.signal !== undefined) {
    init.signal = options.signal
  }
  try {
    // eslint-disable-next-line no-restricted-globals -- the single, documented exception above
    const response = await fetch(`${DEMO_AI_BASE_URL}${path}`, init)
    if (!response.ok) {
      return { ok: false, reason: 'rejected' }
    }
    const body: unknown = await response.json()
    return { ok: true, body }
  } catch {
    return { ok: false, reason: options.signal?.aborted === true ? 'aborted' : 'unreachable' }
  }
}
