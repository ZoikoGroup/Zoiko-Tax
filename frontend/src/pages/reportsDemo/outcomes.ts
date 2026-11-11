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
 */
import type { ClassifyReply } from './model'

export type ViewKind = 'proposed' | 'abstain' | 'blocked' | 'not_configured' | 'needs_review'

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
}

const HEADLINES: Readonly<Record<ViewKind, string>> = {
  proposed: 'Proposed',
  abstain: 'Set aside — not classified',
  blocked: 'Blocked by governance',
  not_configured: 'Not configured',
  needs_review: 'Result could not be interpreted',
}

const PROPOSED_SENTENCE = 'A person should review this proposal before it is used.'
const NOT_CONFIGURED_SENTENCE = 'The approved-list check is not set up on this demo service.'
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
  [
    'AI_GATEWAY_UNAVAILABLE',
    'The AI service could not answer. Deterministic processing is unaffected. A person should check this.',
  ],
  ['AI_GATEWAY_NOT_CONFIGURED', 'The AI model is not set up on this demo service. A person should check this.'],
])

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
  }
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
      return view('blocked', BLOCKED_SENTENCES.get(reply.reason_code) ?? BLOCKED_DEFAULT, null, listVersion)
    case 'not_configured':
      return view('not_configured', NOT_CONFIGURED_SENTENCE, null, listVersion)
    case null:
      return view('needs_review', NEEDS_REVIEW_SENTENCE, null, listVersion)
  }
}
