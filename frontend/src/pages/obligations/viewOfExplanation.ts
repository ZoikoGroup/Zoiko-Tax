/**
 * How asking the AI to explain one obligation ends, in plain words. Pure: no
 * React, no I/O.
 *
 * Every ending has its own headline and its own sentence saying what to do
 * next. Nothing the service sent is ever shown as it came: not a status word,
 * not a reason code, not an error message. The explanation text is returned
 * only when the status is exactly ANSWERED and the reply is whole; for every
 * other status it is null, even if the reply carries text.
 */
import { evidenceIdText } from '../../components/ai/evidenceId'
import { SLOW_AFTER_MS, tookText } from '../intelligence/viewOfProposal'

export type ExplanationState = 'answered' | 'abstained' | 'review' | 'refused' | 'failed' | 'slow'

export interface ExplanationView {
  readonly state: ExplanationState
  readonly headline: string
  /** What to do next, in one or two plain sentences. */
  readonly sentence: string
  /** "took 3 seconds", or "5 seconds so far" while the call is still running. */
  readonly elapsed: string
  /** True only where asking again can help. */
  readonly retry: boolean
  /** The explanation. Set only when `state` is `answered`; null in every other state. */
  readonly explanation: string | null
  /** The ids the explanation cites. Empty unless `state` is `answered`. */
  readonly citedIds: readonly string[]
  /** The Gateway's evidence record id, when the reply has a valid one. Null unless `state` is `answered`. */
  readonly evidenceId: string | null
}

export type ExplanationEnding =
  | { readonly kind: 'reply'; readonly reply: unknown }
  | { readonly kind: 'failure'; readonly error: unknown }

const SHAPE = /^[A-Za-z0-9-]{1,64}$/

const ROW = 'A person should read the row.'

const SENTENCES = {
  answered: 'Read it against the row. The figures come from the register, not from the AI.',
  abstained: `The AI set this aside. ${ROW}`,
  unreachable: 'Try again in a moment. If it keeps happening, a person should read the row.',
  notConfigured: `The AI is not set up in this cell. ${ROW}`,
  unevidenced: `The AI answered without its record, so the answer was not used. ${ROW}`,
  unusable: `The AI answered, but the answer could not be used. ${ROW}`,
  generic: `The AI did not give a usable explanation. ${ROW}`,
  refused: `An AI explanation is not available for this item yet. ${ROW}`,
  forbidden: 'Only an operator or an analyst can ask for an explanation. Ask one of them.',
  notFound: 'This obligation could not be found. Refresh the list and try again.',
  badRequest: 'The request was not accepted. Refresh the list and try again.',
  signedOut: 'Your session has ended. Sign in again.',
  notHere: `Explanations are not available in this cell. ${ROW}`,
  timeout: 'The AI did not answer in time. Try again, or a person should read the row.',
  network: 'This page could not reach the service. Check your connection and try again.',
  unreadable: 'The service sent a reply this page could not read. Try again.',
  service: 'The service had a problem. Try again in a moment.',
  slow: 'The AI is taking longer than usual. You can keep waiting, or close this and read the row.',
} as const

function wholeSeconds(ms: number): number {
  return Number.isFinite(ms) && ms > 0 ? Math.floor(ms / 1000) : 0
}

function secondsText(seconds: number): string {
  return `${String(seconds)} ${seconds === 1 ? 'second' : 'seconds'}`
}

function ending(
  state: ExplanationState, headline: string, sentence: string, elapsedMs: number, retry = false,
): ExplanationView {
  return { state, headline, sentence, elapsed: tookText(elapsedMs), retry, explanation: null, citedIds: [], evidenceId: null }
}

/** The slow state, or null while the call is still within the threshold. */
export function viewOfExplanationWaiting(elapsedMs: number): ExplanationView | null {
  if (!Number.isFinite(elapsedMs) || elapsedMs < SLOW_AFTER_MS) return null
  return {
    state: 'slow', headline: 'Still waiting for the AI', sentence: SENTENCES.slow,
    elapsed: `${secondsText(wholeSeconds(elapsedMs))} so far`, retry: false, explanation: null, citedIds: [], evidenceId: null,
  }
}

function isRecord(value: unknown): value is Readonly<Record<string, unknown>> {
  return typeof value === 'object' && value !== null
}

/** The cited ids, when the value is an array of strings that look like ids; otherwise null. */
function idsOf(value: unknown): readonly string[] | null {
  if (!Array.isArray(value)) return null
  const ids: string[] = []
  for (const item of value as unknown[]) {
    if (typeof item !== 'string' || !SHAPE.test(item)) return null
    ids.push(item)
  }
  return ids
}

// The reasons a REVIEW_REQUIRED reply can carry (backend app/obligation_explanation.go
// and domain/ai/degraded.go). They are read here and never shown. A Map, so a
// word such as "constructor" finds nothing.
const REVIEW: ReadonlyMap<string, readonly [string, string, boolean]> = new Map([
  ['AI_GATEWAY_UNAVAILABLE', ['The AI could not be reached', SENTENCES.unreachable, true]],
  ['AI_GATEWAY_NOT_CONFIGURED', ['The AI is not set up here', SENTENCES.notConfigured, false]],
  ['AI_GATEWAY_REPLY_UNEVIDENCED', ['The answer was not used', SENTENCES.unevidenced, true]],
  ['AI_EXPLANATION_REPLY_UNUSABLE', ['The answer could not be used', SENTENCES.unusable, true]],
])

const reviewGeneric = (elapsedMs: number) => ending('review', 'No usable explanation', SENTENCES.generic, elapsedMs)

function viewOfReply(reply: unknown, elapsedMs: number, rowId: string | undefined): ExplanationView {
  if (!isRecord(reply)) return ending('failed', 'The reply could not be read', SENTENCES.unreadable, elapsedMs, true)
  const status = reply.status
  if (status === 'ANSWERED') {
    const text = reply.explanation
    const cited = idsOf(reply.citedIds)
    // Whole or nothing: text, at least one cited id, and no id other than the row asked about.
    if (
      typeof text !== 'string' || text.trim() === '' || cited === null || cited.length === 0 ||
      (rowId !== undefined && cited.some((c) => c !== rowId))
    ) {
      return ending('review', 'The answer could not be used', SENTENCES.unusable, elapsedMs, true)
    }
    return {
      state: 'answered', headline: 'The AI explained this row', sentence: SENTENCES.answered,
      elapsed: tookText(elapsedMs), retry: false, explanation: text, citedIds: cited,
      evidenceId: evidenceIdText(isRecord(reply.provenance) ? reply.provenance : null),
    }
  }
  if (status === 'ABSTAINED') return ending('abstained', 'The AI set this aside', SENTENCES.abstained, elapsedMs)
  if (status === 'REVIEW_REQUIRED') {
    const reason = reply.reviewReason
    const known = typeof reason === 'string' ? REVIEW.get(reason) : undefined
    if (known === undefined) return reviewGeneric(elapsedMs)
    return ending('review', known[0], known[1], elapsedMs, known[2])
  }
  // A missing or unknown status is never read as an answer.
  return reviewGeneric(elapsedMs)
}

function statusOf(error: unknown): number | null {
  if (isRecord(error) && typeof error.status === 'number') return error.status
  return null
}

function reasonOf(error: unknown): string | null {
  return isRecord(error) && typeof error.reasonCode === 'string' ? error.reasonCode : null
}

/** The SDK's own timeout aborts with an Error whose message starts "timed out". */
function timedOut(error: unknown): boolean {
  const cause = isRecord(error) ? error.cause : null
  return cause instanceof Error && cause.message.startsWith('timed out')
}

function viewOfFailure(error: unknown, elapsedMs: number): ExplanationView {
  const status = statusOf(error)
  const reason = reasonOf(error)
  if (status === null) {
    return timedOut(error)
      ? ending('failed', 'The AI did not answer in time', SENTENCES.timeout, elapsedMs, true)
      : ending('failed', 'The service could not be reached', SENTENCES.network, elapsedMs, true)
  }
  // A refusal by the AI rules (data class, kill switch, region, authority) is
  // not a fault and not a wrong role: the AI is not allowed to explain this yet.
  if (status === 403) {
    return reason !== null && reason.startsWith('AI_')
      ? ending('refused', 'Not available yet', SENTENCES.refused, elapsedMs)
      : ending('failed', 'You are not allowed to ask', SENTENCES.forbidden, elapsedMs)
  }
  if (status === 404) return ending('failed', 'This obligation was not found', SENTENCES.notFound, elapsedMs)
  if (status === 400 || status === 422) return ending('failed', 'The request was not accepted', SENTENCES.badRequest, elapsedMs)
  if (status === 401) return ending('failed', 'Your session has ended', SENTENCES.signedOut, elapsedMs)
  if (status === 503) return ending('failed', 'Explanations are not available here', SENTENCES.notHere, elapsedMs)
  // A status with no registered reason code is something between this page and
  // the cell, or a body that is not JSON: a reply that cannot be read.
  if (reason === null) return ending('failed', 'The reply could not be read', SENTENCES.unreadable, elapsedMs, true)
  return ending('failed', 'The service had a problem', SENTENCES.service, elapsedMs, true)
}

/**
 * One ending of the call, as the person should see it. `rowId` is the
 * obligation that was asked about: an answer that cites any other id is not
 * used.
 */
export function viewOfExplanation(result: ExplanationEnding, elapsedMs: number, rowId?: string): ExplanationView {
  return result.kind === 'reply' ? viewOfReply(result.reply, elapsedMs, rowId) : viewOfFailure(result.error, elapsedMs)
}
