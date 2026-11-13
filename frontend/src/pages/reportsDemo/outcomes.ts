/**
 * The ONLY file that compares a classify wire outcome, verdict or reason code.
 *
 * ClassifyPanel switches on `kind` from `viewOf` and never on a wire string.
 * Nothing the server sends is ever shown: every sentence below is fixed text,
 * and a code this file does not know gets one generic sentence.
 *
 * Wire contract (intelligence/tools/report_ai_http.py, POST /demo/classify):
 * outcome PROPOSED, ABSTAIN, REFUSED or NOT_CONFIGURED. The aliases ABSTAINED,
 * BLOCKED and BLOCK are accepted too, so a renamed backend word cannot break
 * the page. Anything else is 'needs_review'.
 *
 * It also owns every text for a screen that did not get a usable answer
 * (`viewOfFailure`), so the panel holds no sentence of its own. Each of those
 * says what the person should do next, and each cause has its own words: an
 * input the service refused, a service that is down or slow, a network that
 * is not there, and a reply that cannot be read are never shown alike. A
 * refusal by the governance layer ("blocked") is a decision of the service, a
 * service problem is not, and the two must not look the same.
 */
import { DEMO_AI_TIMEOUT_MS } from '../../platform/demoAi'
import type { ClassifyReply } from './model'

/** Ways a request can end without a reply this page can use. */
export type FailureKind = 'rejected' | 'not_found' | 'timeout' | 'network' | 'service' | 'unreadable'

export type ViewKind =
  | 'proposed'
  | 'abstain'
  | 'blocked'
  | 'not_configured'
  | 'needs_review'
  | FailureKind

export interface ClassifyView {
  readonly kind: ViewKind
  /** Fixed headline for this kind. */
  readonly headline: string
  /** Fixed sentence for this kind and reason. */
  readonly sentence: string
  /** The proposed ontology code. Set only for kind 'proposed'. */
  readonly code: string | null
  /** True only for kinds that really went through the list check (proposed, abstain) and carry a list_version. */
  readonly listChecked: boolean
  /** True when sending the same form again can help (slow, unreachable, failing or unreadable service). */
  readonly canRetry: boolean
}

const HEADLINES: Readonly<Record<ViewKind, string>> = {
  proposed: 'Proposed',
  abstain: 'Set aside — not classified',
  blocked: 'Blocked by governance',
  not_configured: 'Not configured',
  needs_review: 'Result could not be interpreted',
  rejected: 'Request not accepted',
  not_found: 'Route not found on the AI service',
  timeout: 'Taking too long',
  network: 'AI service not reachable',
  service: 'AI service could not answer',
  unreadable: 'Reply could not be read',
}

// Fixed labels and notes the panel shows around a view. The panel holds no text of its own.
export const BUSY_LABEL = 'Classifying…'
export const SUBMIT_LABEL = 'Classify'
export const RETRY_LABEL = 'Try again'
export const DATA_NOTE = 'Synthetic data only. Do not enter real customer data.'
export const SYNTHETIC_BADGE = 'Synthetic demo data'
export const SYNTHETIC_ONLY_NOTE = 'Synthetic demo data only.'
/** Shown before and after the list version: "<BEFORE> <version><AFTER>". */
export const LIST_NOTE_BEFORE = 'Synthetic demo. Checked against the SYNTHETIC list'
export const LIST_NOTE_AFTER = '. This is not the approved ontology list (pending D-05).'
export const EVIDENCE_LABEL = 'Evidence:'
export const EVIDENCE_MISSING = 'Evidence id not available'
export const CONFIDENCE_NOTE = 'Confidence: not calibrated'

const TIMEOUT_SECONDS = Math.round(DEMO_AI_TIMEOUT_MS / 1000)
const UNAFFECTED = 'Deterministic processing is unaffected.'
const ASK_A_REVIEWER = 'If it keeps happening, ask a reviewer.'

const PROPOSED_SENTENCE = 'A person should review this proposal before it is used.'
const NOT_CONFIGURED_SENTENCE =
  'The approved-list check is not set up on this demo service. Ask whoever runs the demo to set it up.'
const MODEL_NOT_CONFIGURED_SENTENCE =
  'The AI model is not set up on this demo service. Ask whoever runs the demo to set it up.'
const NEEDS_REVIEW_SENTENCE = 'This result could not be interpreted. A person should check it.'
const ABSTAIN_DEFAULT = 'The proposal was set aside. A person should check this.'
const BLOCKED_DEFAULT = 'This request was blocked. A person should check this.'

// Maps, not plain objects: a reason code such as "constructor" must not find
// an inherited property.
const ABSTAIN_SENTENCES: ReadonlyMap<string, string> = new Map([
  [
    'GUARD_ONTOLOGY_UNKNOWN',
    'The proposed ontology code is not recognised in the approved list for this demo service. A person should check this.',
  ],
  [
    'GUARD_ONTOLOGY_EXPIRED',
    'The proposed ontology code has expired and is no longer valid in the approved list. A person should check this.',
  ],
  [
    'GUARD_ONTOLOGY_NOT_YET_EFFECTIVE',
    'The proposed ontology code is not yet effective in the approved list. A person should check this.',
  ],
])

const BLOCKED_SENTENCES: ReadonlyMap<string, string> = new Map([
  ['AI_KILL_SWITCH_ENGAGED', 'This AI capability has been switched off. A person should check this.'],
  ['AI_USE_CASE_SUSPENDED', 'This AI use case is suspended. A person should check this.'],
  ['AI_UNKNOWN_USE_CASE', 'This AI use case is not registered. A person should check this.'],
  ['AI_AUTHORITY_REFUSED', 'The authority level asked for is not permitted. A person should check this.'],
  ['AI_RISK_TIER_EXCEEDED', 'The risk level asked for is not permitted. A person should check this.'],
  ['AI_RESIDENCY_REFUSED', 'The request does not meet the region rule. A person should check this.'],
  ['AI_MALFORMED_CONTEXT', 'The request could not be checked. A person should check this.'],
  ['AI_DATA_CLASS_REFUSED', 'The data in the request is not permitted for this use. A person should check this.'],
])

// Not decisions of the governance layer, so never shown as "blocked".
const GATEWAY_UNAVAILABLE = 'AI_GATEWAY_UNAVAILABLE'
const GATEWAY_NOT_CONFIGURED = 'AI_GATEWAY_NOT_CONFIGURED'

const FAILURE_SENTENCES: Readonly<Record<FailureKind, string>> = {
  rejected:
    'The service did not accept the request. Check that both fields are filled and not too long, shorten them if needed, then press Classify.',
  not_found:
    'The AI service does not have this route. Ask whoever runs the demo to check that the service is up to date.',
  timeout: `The AI service did not answer within ${String(TIMEOUT_SECONDS)} seconds. It may still finish in the background. Try again. ${ASK_A_REVIEWER} ${UNAFFECTED}`,
  network: `This page could not reach the AI service. Check that the demo service is running and that your connection works, then try again. ${UNAFFECTED}`,
  service: `The AI service reported a problem and did not answer. Try again in a moment. ${ASK_A_REVIEWER} ${UNAFFECTED}`,
  unreadable: `The AI service sent a reply this page could not read. Try again. ${ASK_A_REVIEWER} ${UNAFFECTED}`,
}

type KnownKind = 'proposed' | 'abstain' | 'blocked' | 'not_configured'

function kindOfOutcome(outcome: string): KnownKind | null {
  switch (outcome) {
    case 'PROPOSED':
      return 'proposed'
    case 'ABSTAIN':
    case 'ABSTAINED':
      return 'abstain'
    case 'REFUSED':
    case 'BLOCKED':
    case 'BLOCK':
      return 'blocked'
    case 'NOT_CONFIGURED':
      return 'not_configured'
    default:
      return null
  }
}

function view(kind: ViewKind, sentence: string, code: string | null, listVersion: string | null): ClassifyView {
  return {
    kind,
    headline: HEADLINES[kind],
    sentence,
    code,
    // Only a proposal or a set-aside went through the list check. A refused call never reached it.
    listChecked: listVersion !== null && (kind === 'proposed' || kind === 'abstain'),
    canRetry: kind === 'timeout' || kind === 'network' || kind === 'service' || kind === 'unreadable',
  }
}

/** What the panel shows when the request ended without a usable reply. Never throws. */
export function viewOfFailure(failure: FailureKind): ClassifyView {
  return view(failure, FAILURE_SENTENCES[failure], null, null)
}

/**
 * Turns a classify reply into what the panel may show. Never throws.
 * A PROPOSED outcome counts only with verdict PASS and a non-empty code;
 * otherwise it is 'needs_review' and the code is not shown.
 */
export function viewOf(reply: ClassifyReply): ClassifyView {
  const kind = kindOfOutcome(reply.outcome)
  const listVersion = reply.list_version
  switch (kind) {
    case 'proposed': {
      const code = reply.proposed_code
      if (reply.verdict !== 'PASS' || code === undefined || code.trim() === '') {
        return view('needs_review', NEEDS_REVIEW_SENTENCE, null, listVersion)
      }
      return view('proposed', PROPOSED_SENTENCE, code, listVersion)
    }
    case 'abstain':
      return view('abstain', ABSTAIN_SENTENCES.get(reply.reason_code) ?? ABSTAIN_DEFAULT, null, listVersion)
    case 'blocked':
      if (reply.reason_code === GATEWAY_UNAVAILABLE) return viewOfFailure('service')
      if (reply.reason_code === GATEWAY_NOT_CONFIGURED) {
        return view('not_configured', MODEL_NOT_CONFIGURED_SENTENCE, null, listVersion)
      }
      return view('blocked', BLOCKED_SENTENCES.get(reply.reason_code) ?? BLOCKED_DEFAULT, null, listVersion)
    case 'not_configured':
      return view('not_configured', NOT_CONFIGURED_SENTENCE, null, listVersion)
    case null:
      return view('needs_review', NEEDS_REVIEW_SENTENCE, null, listVersion)
  }
}
