/** The page's two calls to the demo AI service, decoded. See platform/demoAi.ts. */
import { demoAiRequest } from '../../platform/demoAi'
import { parseClassifyReply, parseExplainReply, parseFixture, type ClassifyReply, type ExplainReply, type ExplainTarget, type Fixture } from './model'

export type FixtureResult = { readonly ok: true; readonly fixture: Fixture } | { readonly ok: false }

/** GET /demo/fixture. A body of the wrong shape counts as unavailable. */
export async function loadFixture(signal: AbortSignal): Promise<FixtureResult> {
  const res = await demoAiRequest('/demo/fixture', { signal })
  if (!res.ok) {
    return { ok: false }
  }
  const fixture = parseFixture(res.body)
  return fixture === null ? { ok: false } : { ok: true, fixture }
}

export type ExplainResult =
  | { readonly status: 'reply'; readonly reply: ExplainReply }
  | { readonly status: 'unavailable' }
  | { readonly status: 'aborted' }

/** POST /demo/explain. The service being down is a result, not an exception. */
export async function requestExplain(
  target: Pick<ExplainTarget, 'kind' | 'ids'>,
  signal: AbortSignal,
): Promise<ExplainResult> {
  const res = await demoAiRequest('/demo/explain', { body: { kind: target.kind, ids: target.ids }, signal })
  if (!res.ok) {
    return res.reason === 'aborted' ? { status: 'aborted' } : { status: 'unavailable' }
  }
  const reply = parseExplainReply(res.body)
  return reply === null ? { status: 'unavailable' } : { status: 'reply', reply }
}

/**
 * Request limits of POST /demo/classify, in characters. They must match
 * _SUBJECT_REF_MAX and _DESCRIPTION_MAX in intelligence/tools/report_ai_http.py.
 */
export const SUBJECT_REF_MAX = 512
export const DESCRIPTION_MAX = 1000

export type ClassifyResult =
  | { readonly status: 'reply'; readonly reply: ClassifyReply }
  | { readonly status: 'rejected' }
  | { readonly status: 'not_found' }
  | { readonly status: 'service' }
  | { readonly status: 'network' }
  | { readonly status: 'unreadable' }
  | { readonly status: 'timeout' }
  | { readonly status: 'aborted' }

/** The one status the route uses for input it will not take (fixed 400 bodies). */
const HTTP_BAD_INPUT = 400
/** The route is missing: another service on the port, or an older demo service without /demo/classify. */
const HTTP_NOT_FOUND = 404

/**
 * POST /demo/classify. Each ending is its own status: 'rejected' is the 400 for bad
 * input only; 404 is 'not_found' (the service has no such route, retrying cannot help);
 * any other non-2xx answer is 'service' (the service is failing, not the
 * input); 'network' is no answer at all; 'timeout' is the platform timer
 * (DEMO_AI_TIMEOUT_MS) firing; 'unreadable' is a 2xx body that is not a classify reply.
 */
export async function requestClassify(
  params: { readonly subjectRef: string; readonly description: string },
  signal: AbortSignal,
): Promise<ClassifyResult> {
  const res = await demoAiRequest('/demo/classify', {
    body: { subject_ref: params.subjectRef, description: params.description },
    signal,
  })
  if (!res.ok) {
    if (res.reason === 'aborted') return { status: 'aborted' }
    if (res.reason === 'timeout') return { status: 'timeout' }
    if (res.reason === 'unreadable') return { status: 'unreadable' }
    if (res.reason === 'http') {
      if (res.status === HTTP_BAD_INPUT) return { status: 'rejected' }
      return { status: res.status === HTTP_NOT_FOUND ? 'not_found' : 'service' }
    }
    return { status: 'network' }
  }
  const reply = parseClassifyReply(res.body)
  return reply === null ? { status: 'unreadable' } : { status: 'reply', reply }
}
