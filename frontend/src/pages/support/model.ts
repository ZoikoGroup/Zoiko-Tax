import { ZoikoTaxError, type S } from '@zoikotax/sdk'

// Support cases (Engineering Doc 18, W16): the vocabularies, and what each
// action asks of the person taking it. The cell enforces every rule; this
// only decides which fields a form shows.

export type SupportCase = S<'SupportCase'>
export type CaseType = S<'SupportCaseType'>
export type Severity = S<'SupportSeverity'>
export type CaseStatus = S<'SupportCaseStatus'>
export type CaseAction = S<'SupportCaseAction'>
export type ClockState = S<'SupportClockState'>
export type EscalationTarget = S<'SupportEscalationTarget'>

export const TYPES: readonly CaseType[] = [
  'COVERAGE_ENTITLEMENT', 'INVOICE_EXPLANATION', 'TAX_SPECIALIST', 'FILING', 'REMITTANCE', 'SECURITY', 'LEGAL',
]
export const SEVERITIES: readonly Severity[] = ['S1', 'S2', 'S3', 'S4']
export const STATUSES: readonly CaseStatus[] = [
  'NEW', 'VERIFIED', 'TRIAGE_INCOMPLETE', 'TRIAGED', 'IN_REVIEW', 'RESPONDED', 'RESOLVED', 'CLOSED',
]
export const ESCALATIONS: readonly EscalationTarget[] = ['TAX_SPECIALIST', 'LEGAL', 'SECURITY', 'FINANCE_OPERATIONS', 'ENGINEERING']
export const SLA_STATES: readonly ClockState[] = ['ON_TRACK', 'AT_RISK', 'BREACHED', 'MET']
export const STATUTORY_STATES: readonly ClockState[] = ['NONE', 'ON_TRACK', 'AT_RISK', 'PASSED']

/** The support SLA's target by severity, as the cell applies it. */
export const SLA_TARGET: Record<Severity, string> = { S1: '4 hours', S2: '24 hours', S3: '72 hours', S4: '7 days' }

export const words = (s: string) => s.replaceAll('_', ' ')

export const pick = <T extends string>(values: readonly T[], v: string): T | '' =>
  (values as readonly string[]).includes(v) ? (v as T) : ''

/** The fields an action's form shows, and which of them it needs. */
export interface ActionForm {
  readonly label: string
  readonly help: string
  readonly note: 'required' | 'optional'
  readonly noteLabel?: string
  readonly triage?: true
  readonly assignee?: true
  readonly escalate?: true
  readonly evidence?: true
}

export const ACTION_FORMS: Record<Exclude<CaseAction, 'OPENED'>, ActionForm> = {
  VERIFY: { label: 'Verify requester', help: 'Confirm who is asking and that they may ask for this customer.', note: 'optional' },
  TRIAGE: {
    label: 'Triage', triage: true, note: 'optional',
    help: 'Classify the case. It stays TRIAGE INCOMPLETE, and the missing facts are requested, until it has a contract, a period and a seller (SUP-02).',
  },
  ASSIGN: { label: 'Assign', assignee: true, note: 'optional', help: 'Name the person who owns the case.' },
  REQUEST_EVIDENCE: { label: 'Request evidence', note: 'required', noteLabel: 'What is needed', help: 'Ask the customer for what the case needs.' },
  ADD_EVIDENCE: { label: 'Add evidence', evidence: true, note: 'optional', help: 'Record references to evidence — hashes or document ids — never the evidence itself.' },
  START_REVIEW: { label: 'Start review', note: 'optional', help: 'Begin working the case.' },
  ESCALATE: { label: 'Escalate', escalate: true, note: 'required', noteLabel: 'Why', help: 'Route the case to people with authority. Support gains none.' },
  RESPOND: { label: 'Respond', note: 'required', noteLabel: 'Response to the customer', help: 'Record what the customer was told. A case closes only after one (SUP-14).' },
  RESOLVE: { label: 'Resolve', note: 'optional', help: 'The customer’s request is answered.' },
  SIGN_OFF: {
    label: 'Sign off', note: 'required', noteLabel: 'What was checked',
    help: 'Independent sign-off. The person who opened the case cannot give it (T18-18).',
  },
  CLOSE: { label: 'Close', note: 'optional', help: 'End the case.' },
  REOPEN: { label: 'Reopen', note: 'required', noteLabel: 'Why', help: 'Back to review, linked to the closure it reverses. Neither clock restarts (SUP-07).' },
  NOTE: { label: 'Add note', note: 'required', noteLabel: 'Note', help: 'An internal note on the case history.' },
  REQUEST_LEGAL_HOLD: {
    label: 'Request legal hold', note: 'required', noteLabel: 'What to hold, and why',
    help: 'Recorded for an administrator to act on under Evidence. Support places no hold itself.',
  },
}

/** Why a resolved case cannot close yet, or null when it can. */
export function closeBlocker(c: SupportCase): string | null {
  if (c.status !== 'RESOLVED') {
    return null
  }
  if (!c.responded) {
    return 'No customer response is recorded since the case was last reopened (SUP-14).'
  }
  if (c.signOffRequired && !c.signedOff) {
    return 'A LEGAL or TAX SPECIALIST case needs a sign-off by someone other than its opener before it closes (T18-18).'
  }
  return null
}

/** What a refusal means for the person who met it. */
export function refusalHint(error: unknown): string | null {
  if (!(error instanceof ZoikoTaxError)) {
    return null
  }
  switch (error.reasonCode) {
    case 'SIGN_OFF_REQUIRED':
      return 'Ask someone other than the case opener to sign it off, then close it.'
    case 'SEGREGATION_OF_DUTIES':
      return 'You opened this case, so another person must sign it off.'
    case 'STATE_TRANSITION_INVALID':
      return 'The case’s status does not allow this now. It may have moved since you opened it; refresh and look again.'
    case 'FORBIDDEN':
      return 'Your roles do not allow acting on support cases. ADMIN, OPERATOR or ANALYST may; AUDITOR reads.'
    default:
      return null
  }
}

/** A datetime-local value, read as UTC, in the contract's timestamp form. */
export function utcTimestamp(local: string): string | undefined {
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$/.test(local)) {
    return undefined
  }
  return `${local}:00.000000Z`
}

export const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/

/** Evidence references from a box, one per line or comma-separated. */
export const evidenceList = (text: string): string[] =>
  [...new Set(text.split(/[\n,]/).map((s) => s.trim()).filter((s) => s !== ''))]

export const canAct = (roles: readonly string[]) => roles.some((r) => r === 'ADMIN' || r === 'OPERATOR' || r === 'ANALYST')

/** The object without its undefined fields, as an exact optional type wants. */
export const compact = <T,>(o: Record<string, unknown>): T =>
  Object.fromEntries(Object.entries(o).filter(([, v]) => v !== undefined)) as T
