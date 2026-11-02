/** The page's two calls to the demo AI service, decoded. See platform/demoAi.ts. */
import { demoAiRequest } from '../../platform/demoAi'
import { parseExplainReply, parseFixture, type ExplainReply, type ExplainTarget, type Fixture } from './model'

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
