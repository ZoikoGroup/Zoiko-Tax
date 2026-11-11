/**
 * The ONLY place that maps wire outcome names to UI display states.
 * No other file may compare outcome strings directly.
 *
 * Target names from AIGOV-001 §11: ABSTAIN, REVIEW_REQUIRED, BLOCKED,
 * PROPOSED, NOT_CONFIGURED.
 * Legacy backend aliases: ABSTAINED → ABSTAIN, REFUSED → BLOCKED, BLOCK → BLOCKED.
 */

export type OutcomeTone = 'neutral' | 'caution'

export interface OutcomeEntry {
  /** UI headline for this outcome. */
  readonly headline: string
  /** Visual tone. Never 'success' except for PROPOSED with PASS verdict. */
  readonly tone: OutcomeTone
  /** Whether a proposed ontology code may be shown. Only true for PROPOSED. */
  readonly showCode: boolean
  /** Whether "A person should check this." should be shown. False only for PROPOSED. */
  readonly showPersonCheck: boolean
}

/** The canonical NEEDS_REVIEW state used for any unknown outcome string. */
export const NEEDS_REVIEW_ENTRY: OutcomeEntry = {
  headline: 'Result could not be interpreted',
  tone: 'neutral',
  showCode: false,
  showPersonCheck: true,
}

const OUTCOME_MAP: Readonly<Record<string, OutcomeEntry>> = {
  PROPOSED: {
    headline: 'Proposed',
    tone: 'neutral', // tone is overridden to pass in the panel when verdict === 'PASS'
    showCode: true,
    showPersonCheck: false,
  },
  ABSTAIN: {
    headline: 'Set aside — not classified',
    tone: 'caution',
    showCode: false,
    showPersonCheck: true,
  },
  // Legacy alias → ABSTAIN
  ABSTAINED: {
    headline: 'Set aside — not classified',
    tone: 'caution',
    showCode: false,
    showPersonCheck: true,
  },
  REVIEW_REQUIRED: {
    headline: 'Review required',
    tone: 'caution',
    showCode: false,
    showPersonCheck: true,
  },
  BLOCKED: {
    headline: 'Blocked by governance',
    tone: 'caution',
    showCode: false,
    showPersonCheck: true,
  },
  // Legacy aliases → BLOCKED
  REFUSED: {
    headline: 'Blocked by governance',
    tone: 'caution',
    showCode: false,
    showPersonCheck: true,
  },
  BLOCK: {
    headline: 'Blocked by governance',
    tone: 'caution',
    showCode: false,
    showPersonCheck: true,
  },
  NOT_CONFIGURED: {
    headline: 'Not configured',
    tone: 'neutral',
    showCode: false,
    showPersonCheck: true,
  },
}

/**
 * Returns the OutcomeEntry for the given wire outcome string.
 * Unknown strings return NEEDS_REVIEW_ENTRY — never throws, never null.
 */
export function lookupOutcome(outcome: string): OutcomeEntry {
  return OUTCOME_MAP[outcome] ?? NEEDS_REVIEW_ENTRY
}

