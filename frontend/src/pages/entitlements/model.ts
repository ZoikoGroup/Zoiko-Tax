/**
 * Pieces of the Entitlements page (Engineering Doc 18 W06, W07, W15):
 * vocabularies, labels, which actions a person is offered, and the request
 * bodies the forms send.
 *
 * The cell decides everything — the effective state, the coverage, the
 * four-eye rules. This only keeps the screen from offering an act the cell
 * would refuse, and says why instead of showing a dead button.
 */
import { ZoikoTaxError, type S } from '@zoikotax/sdk'

export type Entitlement = S<'Entitlement'>
export type EntitlementList = S<'EntitlementList'>
export type Mode = S<'EntitlementMode'>
export type Operation = S<'EntitlementOperation'>
export type Action = S<'EntitlementAction'>
export type Effective = S<'EntitlementEffective'>
export type Reason = S<'EntitlementReason'>
export type EntitlementRequest = S<'EntitlementRequest'>
export type ActionRequest = S<'EntitlementActionRequest'>

export const MODES: readonly { value: Mode; label: string; operations: readonly Operation[] }[] = [
  { value: 'NATIVE', label: 'Native', operations: ['QUOTE', 'COMMIT', 'FILE', 'REMIT', 'E_INVOICE'] },
  { value: 'FEDERATED', label: 'Federated (third-party provider)', operations: ['QUOTE', 'COMMIT'] },
  { value: 'SHADOW_ASSURANCE', label: 'Shadow assurance', operations: ['SHADOW_COMPARE'] },
  { value: 'OEM_EMBEDDED', label: 'OEM / embedded', operations: ['QUOTE', 'COMMIT'] },
  { value: 'MANAGED_COMPLIANCE', label: 'Managed compliance', operations: ['FILE', 'REMIT'] },
]

export const OPERATION_LABEL: Record<Operation, string> = {
  QUOTE: 'Quote / calculate',
  COMMIT: 'Commit',
  FILE: 'File returns',
  REMIT: 'Remit',
  E_INVOICE: 'E-invoicing',
  SHADOW_COMPARE: 'Shadow comparison',
}

/** The operations a mode carries. A mode never implies another's (Doc 18 §02). */
export function operationsFor(mode: Mode): readonly Operation[] {
  return MODES.find((m) => m.value === mode)?.operations ?? []
}

export const EFFECTIVES: readonly Effective[] = ['ACTIVE', 'RESTRICTED', 'PENDING', 'SUSPENDED', 'EXPIRED', 'ENDED']

export const EFFECTIVE_TONE: Record<Effective, 'ok' | 'warn' | 'bad' | 'neutral'> = {
  ACTIVE: 'ok', RESTRICTED: 'bad', PENDING: 'warn', SUSPENDED: 'bad', EXPIRED: 'neutral', ENDED: 'neutral',
}

/** What a reason code means, in a sentence a customer can act on. */
export const REASON_LABEL: Record<Reason, string> = {
  AWAITING_SUBMISSION: 'Draft — not yet submitted for review',
  AWAITING_SECOND_APPROVAL: 'Awaiting a second administrator’s approval',
  AWAITING_EFFECTIVE_DATE: 'Approved — starts on a later date',
  ENTITLEMENT_SUSPENDED: 'Suspended',
  RESUME_AWAITING_APPROVAL: 'Resume requested — awaiting a second administrator',
  ENTITLEMENT_EXPIRED: 'Past its end date',
  ENTITLEMENT_ENDED: 'Rejected, withdrawn or revoked',
  CONTENT_NOT_ACTIVE: 'No content in force for this service there',
  OPERATION_NOT_SUPPORTED: 'This operation is not supported there yet',
  TECHNICALLY_UNAVAILABLE: 'The cell cannot run this operation yet',
  GEOGRAPHY_NOT_PROVEN: 'Coverage proven for the country, not this subdivision',
  PROVIDER_EVIDENCE_ABSENT: 'Provider coverage not evidenced',
}

export const ACTION_LABEL: Record<Action, string> = {
  SUBMIT: 'Submit for review',
  APPROVE: 'Approve (second administrator)',
  REJECT: 'Reject',
  WITHDRAW: 'Withdraw',
  SUSPEND: 'Suspend now',
  REQUEST_RESUME: 'Request resume',
  RESUME: 'Approve resume (second administrator)',
  REVOKE: 'Revoke',
}

export interface Offer {
  readonly action: Action
  /** Why this person cannot take it, or null when they can. */
  readonly blocked: string | null
}

function lastBy(e: Entitlement, kind: string): string | undefined {
  for (let i = e.history.length - 1; i >= 0; i--) {
    const h = e.history[i]
    if (h?.kind === kind) return h.recordedBy
  }
  return undefined
}

/**
 * The actions the grant's lifecycle allows, each with why this person may
 * not take it: only an ADMIN acts, and an approval or a resume is a second
 * administrator's.
 */
export function offersFor(e: Entitlement, roles: readonly string[], me: string): Offer[] {
  return e.actions.map((action) => {
    if (!roles.includes('ADMIN')) return { action, blocked: 'Only an ADMIN acts on entitlements.' }
    if (action === 'APPROVE' && (e.createdBy === me || e.submittedBy === me)) {
      return { action, blocked: 'You recorded or submitted this grant; a different administrator approves it.' }
    }
    if (action === 'RESUME' && (lastBy(e, 'RESUME_REQUESTED') === me || lastBy(e, 'SUSPENDED') === me)) {
      return { action, blocked: 'You requested the resume or suspended the grant; a different administrator approves it.' }
    }
    return { action, blocked: null }
  })
}

/** The body an action sends, or why it cannot be sent. Every act but SUBMIT gives its reason. */
export function actionBody(e: Pick<Entitlement, 'version'>, action: Action, reason: string): { body: ActionRequest } | { invalid: string } {
  const r = reason.trim()
  if (action !== 'SUBMIT' && r === '') return { invalid: 'Give the reason for this act; it is kept in the grant’s history.' }
  if (r.length > 2000) return { invalid: 'A reason is at most 2000 characters.' }
  return { body: { action, expectedVersion: e.version, ...(r === '' ? {} : { reason: r }) } }
}

export interface GrantForm {
  legalEntityId: string
  jurisdiction: string
  service: string
  mode: Mode
  operation: Operation
  effectiveFrom: string
  effectiveTo: string
  contractRef: string
  contractRevision: string
  submit: boolean
}

const JURISDICTION = /^[A-Z]{2}(-[A-Z0-9]{1,3})?$/
const DATE = /^[0-9]{4}-[0-9]{2}-[0-9]{2}$/

/** The request a grant form sends, or why it cannot be sent. */
export function grantRequest(f: GrantForm, today: string): { body: EntitlementRequest } | { invalid: string } {
  const j = f.jurisdiction.trim().toUpperCase()
  if (!JURISDICTION.test(j)) return { invalid: 'A jurisdiction is a country, or a country and one subdivision: GB, US-NY.' }
  if (!operationsFor(f.mode).includes(f.operation)) return { invalid: `A ${f.mode} grant does not carry ${f.operation}.` }
  if (!DATE.test(f.effectiveFrom)) return { invalid: 'Give the date the grant starts.' }
  if (f.effectiveFrom < today) return { invalid: 'A grant is never backdated: it starts today or later.' }
  if (f.effectiveTo !== '' && (!DATE.test(f.effectiveTo) || f.effectiveTo < f.effectiveFrom)) {
    return { invalid: 'A grant ends on or after the date it starts.' }
  }
  if (f.contractRef.trim() === '') return { invalid: 'Name the signed contract the right comes from.' }
  return {
    body: {
      jurisdiction: j, service: f.service as EntitlementRequest['service'], mode: f.mode, operation: f.operation,
      effectiveFrom: f.effectiveFrom, contractRef: f.contractRef.trim(), submit: f.submit,
      ...(f.effectiveTo === '' ? {} : { effectiveTo: f.effectiveTo }),
      ...(f.contractRevision.trim() === '' ? {} : { contractRevision: f.contractRevision.trim() }),
      ...(f.legalEntityId.trim() === '' ? {} : { legalEntityId: f.legalEntityId.trim() }),
    },
  }
}

export const reasonOf = (error: unknown): string | null => (error instanceof ZoikoTaxError ? error.reasonCode : null)

/** A plain-language line for the refusals a person can do something about. */
export function refusalHint(reason: string | null): string | null {
  switch (reason) {
    case 'MAKER_CHECKER':
      return 'A different administrator must take this step.'
    case 'VERSION_CONFLICT':
      return 'The grant changed since you read it. Refresh and act on the current version.'
    case 'ENTITLEMENT_OVERLAP':
      return 'Another approved grant already covers this exact scope for part of these dates.'
    case 'IDEMPOTENCY_KEY_REUSE':
      return 'This form was already sent with different values. Start a new grant.'
  }
  return null
}

/** Counts by effective state, for the tiles. */
export function countEffective(list: readonly Entitlement[]): Record<Effective, number> {
  const out: Record<Effective, number> = { ACTIVE: 0, RESTRICTED: 0, PENDING: 0, SUSPENDED: 0, EXPIRED: 0, ENDED: 0 }
  for (const e of list) out[e.effective] += 1
  return out
}
