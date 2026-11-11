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

export type ClassifyResult =
  | { readonly status: 'reply'; readonly reply: ClassifyReply }
  | { readonly status: 'unavailable' }
  | { readonly status: 'timeout' }
  | { readonly status: 'aborted' }

/** POST /demo/classify. Returns 'timeout' when the 30 s timer fires. */
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
    return { status: 'unavailable' }
  }
  const reply = parseClassifyReply(res.body)
  return reply === null ? { status: 'unavailable' } : { status: 'reply', reply }
}
