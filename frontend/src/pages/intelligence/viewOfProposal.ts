/**
 * How the Intelligence screen ends, in plain words. Pure: no React, no I/O.
 *
 * Input is what the proposal call gave back (a proposal, or a failure) and how
 * long it took. Output is one of four states with a headline, a sentence that
 * says what to do next, and the time as text. A proposed code is carried by the
 * `proposed` state only, and no raw error text or reason code is ever returned.
 *
 * Display only. Nothing here changes the call, its request, its response types
 * or any deadline (the client's own limit is `timeoutMs` in platform/client.ts).
 */

/**
 * How long the call may run before the screen says it is still waiting. It is
 * below the client's real limit (10 s, platform/client.ts), so the person sees
 * the line before the call is ended. It is a display threshold only.
 */
export const SLOW_AFTER_MS = 5000

/**
 * The longest description the service accepts: `len(in.Description) > 2000` is
 * refused (backend/internal/app/classification.go:91). The box stops the person
 * before they send; the service still counts bytes, so text with many
 * multi-byte characters can reach it over the limit and gets the 400 sentence.
 */
export const DESCRIPTION_MAX = 2000

/**
 * The longest subject reference the service accepts, counted in bytes after
 * trimming: `len(in.SubjectRef) > 512` is refused
 * (backend/internal/app/classification.go:88). The box stops at this many
 * characters; text with multi-byte characters can still be over in bytes, so
 * the check below counts bytes, as the service does.
 */
export const SUBJECT_REF_MAX = 512

export const SUBJECT_REF_SENTENCE = `The subject reference can be at most ${String(SUBJECT_REF_MAX)} characters. Shorten it and try again.`

/** True when the trimmed value is longer than the service accepts (in UTF-8 bytes). */
export function subjectRefTooLong(value: string): boolean {
  return new TextEncoder().encode(value.trim()).length > SUBJECT_REF_MAX
}

/** The sentence to show under the box: when the limit is reached or passed, otherwise null. */
export function subjectRefNote(value: string): string | null {
  return value.length >= SUBJECT_REF_MAX || subjectRefTooLong(value) ? SUBJECT_REF_SENTENCE : null
}

/** The value to send (trimmed), or null when it is over the limit and nothing may be sent. */
export function subjectRefToSend(value: string): string | null {
  return subjectRefTooLong(value) ? null : value.trim()
}

export type ViewState = 'proposed' | 'review' | 'unavailable' | 'slow'

export interface ProposalView {
  readonly state: ViewState
  readonly headline: string
  /** What to do next, in one or two plain sentences. */
  readonly sentence: string
  /** "took 3 seconds", or "5 seconds so far" while the call is still running. */
  readonly elapsed: string
  /** The proposed code. Set only when `state` is `proposed`; null in every other state. */
  readonly code: string | null
}

/** The part of a classification proposal this helper reads. */
export interface ProposalLike {
  readonly disposition?: string
  readonly proposedCode?: string
  readonly provenance?: unknown
  readonly reviewReason?: string
}

export type Ending =
  | { readonly kind: 'proposal'; readonly proposal: ProposalLike }
  | { readonly kind: 'failure'; readonly error: unknown }

const UNAFFECTED = 'Deterministic processing is unaffected.'

function wholeSeconds(ms: number): number {
  return Number.isFinite(ms) && ms > 0 ? Math.floor(ms / 1000) : 0
}

function secondsText(seconds: number): string {
  return `${String(seconds)} ${seconds === 1 ? 'second' : 'seconds'}`
}

/** "took less than a second", "took 1 second", "took 5 seconds". */
export function tookText(ms: number): string {
  const s = wholeSeconds(ms)
  return s === 0 ? 'took less than a second' : `took ${secondsText(s)}`
}

/** The slow state, or null while the call is still within the threshold. */
export function viewOfWaiting(elapsedMs: number): ProposalView | null {
  if (!Number.isFinite(elapsedMs) || elapsedMs < SLOW_AFTER_MS) return null
  return {
    state: 'slow',
    headline: 'Still waiting for the AI',
    sentence: 'The AI is taking longer than usual. You can keep waiting. If it fails, classify the item by hand.',
    elapsed: `${secondsText(wholeSeconds(elapsedMs))} so far`,
    code: null,
  }
}

// Reasons that mean the AI could not be reached or failed, against reasons that
// mean it answered and the answer was set aside. Names are the SDK's
// `reviewReason` values (backend/internal/app/classification.go); they are
// read here and never shown.
const UNREACHED_REASONS: ReadonlySet<string> = new Set(['AI_GATEWAY_NOT_CONFIGURED', 'AI_GATEWAY_UNAVAILABLE'])

function statusOf(error: unknown): number | null {
  if (typeof error === 'object' && error !== null && 'status' in error) {
    const status = error.status
    return typeof status === 'number' ? status : null
  }
  return null
}

const unavailable = (elapsedMs: number): ProposalView => ({
  state: 'unavailable',
  headline: 'The AI could not be reached',
  sentence: `Try again in a moment. If it keeps happening, classify the item by hand. ${UNAFFECTED} Nothing was changed.`,
  elapsed: tookText(elapsedMs),
  code: null,
})

const review = (
  elapsedMs: number,
  sentence: string,
  headline = 'Set aside for a person to review',
): ProposalView => ({
  state: 'review',
  headline,
  sentence,
  elapsed: tookText(elapsedMs),
  code: null,
})

/**
 * Whether the "No proposal yet" note may show. Never while a call is running,
 * and never while a result (a proposal or a failure) exists, so it cannot
 * flash between the end of a call and the result appearing.
 */
export function emptyNoteVisible(busy: boolean, hasResult: boolean): boolean {
  return !busy && !hasResult
}

/** One ending of the call, as the person should see it. */
export function viewOfProposal(ending: Ending, elapsedMs: number): ProposalView {
  if (ending.kind === 'failure') {
    // A refusal by governance (kill switch, region, authority) is a 403 with a
    // reason: the AI was blocked, not down. Every other failure, including a
    // timeout or no answer at all, means the AI could not be reached.
    const status = statusOf(ending.error)
    if (status === 403) {
      return review(
        elapsedMs,
        'The AI was not allowed to answer this request. Classify the item by hand, or ask a reviewer. Nothing was changed.',
      )
    }
    // The service did not accept the request (for example a description over
    // the limit): that is for the person to fix, not an outage.
    if (status === 400 || status === 422) {
      return review(
        elapsedMs,
        `Check the fields and try again. The description can be at most ${String(DESCRIPTION_MAX)} characters. Nothing was changed.`,
        'The request was not accepted',
      )
    }
    // The session ended: signing in again fixes it, trying again does not.
    if (status === 401) {
      return {
        state: 'unavailable',
        headline: 'Your session has ended',
        sentence: 'Your session has ended. Sign in again. Nothing was changed.',
        elapsed: tookText(elapsedMs),
        code: null,
      }
    }
    return unavailable(elapsedMs)
  }

  const p = ending.proposal
  if (p.disposition === 'PROPOSED' && p.provenance !== undefined && p.provenance !== null && typeof p.proposedCode === 'string' && p.proposedCode !== '') {
    return {
      state: 'proposed',
      headline: 'The AI suggested a code',
      sentence: 'A person must review this suggestion before anything relies on it.',
      elapsed: tookText(elapsedMs),
      code: p.proposedCode,
    }
  }
  if (p.disposition === 'REVIEW_REQUIRED' && p.reviewReason !== undefined && UNREACHED_REASONS.has(p.reviewReason)) {
    return unavailable(elapsedMs)
  }
  // The AI answered and the answer was set aside, or the outcome is one this
  // screen does not know: either way there is no suggestion, so a person decides.
  return review(
    elapsedMs,
    `The AI did not give a usable suggestion for this item. Classify it by hand. ${UNAFFECTED} Nothing was changed.`,
  )
}
