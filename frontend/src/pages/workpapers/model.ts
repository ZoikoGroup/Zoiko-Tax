/**
 * Pieces of the Regulatory Workpapers page: vocabularies, labels, the reason
 * codes a write is refused with, and the request bodies the action forms
 * send.
 *
 * Every amount is the string the cell computed, or the string the person
 * typed. Nothing here adds, compares or rounds one (ADR-0019 C1).
 */
import { ZoikoTaxError, type S } from '@zoikotax/sdk'

export type Workpaper = S<'Workpaper'>
export type WorkpaperStatus = S<'WorkpaperStatus'>
export type Action = S<'WorkpaperAction'>
export type Duty = S<'ContributionRegime'>
export type ActionRequest = S<'WorkpaperActionRequest'>
export type Evidence = S<'WorkpaperEvidence'>
export type RoundingMode = S<'WorkpaperRoundingMode'>
export type AdjustmentKind = S<'WorkpaperAdjustmentKind'>
export type Decision = S<'WorkpaperDecision'>

export const DUTIES: readonly { value: Duty; label: string }[] = [
  { value: 'US_USF', label: 'US USF (USAC 499-A / 499-Q)' },
  { value: 'CA_CRTC_NCF', label: 'Canada CRTC NCF' },
  { value: 'OTHER', label: 'Other fund' },
]

export const STATUSES: readonly WorkpaperStatus[] = ['DRAFT', 'DATA_INCOMPLETE', 'READY_FOR_REVIEW', 'APPROVED', 'REVOKED']

export const ROUNDING_MODES: readonly RoundingMode[] = ['HALF_EVEN', 'HALF_UP', 'HALF_DOWN', 'DOWN', 'UP', 'CEILING', 'FLOOR']

export const ACTION_LABEL: Record<Action, string> = {
  BUILD: 'Build deterministic',
  FREEZE: 'Freeze draft for review',
  RETURN_TO_DRAFT: 'Return to draft',
  APPROVE: 'Approve frozen build (four-eye)',
  CHANGE_BASIS: 'Change content release',
  DOCUMENT_FX_RATE: 'Document FX rate',
  PROPOSE_ADJUSTMENT: 'Propose allowance or adjustment',
  DECIDE_ADJUSTMENT: 'Decide material adjustment',
  PROPOSE_RESOLUTION: 'Resolve exception',
  DECIDE_RESOLUTION: 'Confirm or reject resolution',
}

export const BLOCKER_LABEL: Record<S<'WorkpaperBlocker'>, string> = {
  NO_BUILD: 'Not built yet.',
  BASIS_CHANGED_SINCE_BUILD: 'The content release, a documented rate or an approved adjustment changed since the last build: rebuild.',
  FORMULA_CHANGED: 'The last build ran under a formula this service no longer runs: rebuild.',
  EXCEPTION_UNRESOLVED: 'An exception of the latest build is unresolved.',
  ADJUSTMENT_PENDING: 'A material adjustment awaits a second person.',
  RESOLUTION_PENDING: 'An exception resolution awaits a second person.',
}

/** The roles each action needs, as the cell checks them. */
export const ACTION_ROLES: Record<Action, readonly string[]> = {
  BUILD: ['OPERATOR', 'ANALYST'],
  FREEZE: ['OPERATOR', 'ANALYST'],
  RETURN_TO_DRAFT: ['OPERATOR', 'ANALYST'],
  APPROVE: ['ANALYST'],
  CHANGE_BASIS: ['OPERATOR', 'ANALYST'],
  DOCUMENT_FX_RATE: ['OPERATOR', 'ANALYST'],
  PROPOSE_ADJUSTMENT: ['OPERATOR', 'ANALYST'],
  DECIDE_ADJUSTMENT: ['ANALYST'],
  PROPOSE_RESOLUTION: ['OPERATOR', 'ANALYST'],
  DECIDE_RESOLUTION: ['ANALYST'],
}

export const isDuty = (v: string): v is Duty => DUTIES.some((d) => d.value === v)
export const isStatus = (v: string): v is WorkpaperStatus => (STATUSES as readonly string[]).includes(v)
export const isRoundingMode = (v: string): v is RoundingMode => (ROUNDING_MODES as readonly string[]).includes(v)

export const reasonOf = (error: unknown): string | null => (error instanceof ZoikoTaxError ? error.reasonCode : null)

/** What a refusal means for the next step, for the codes this page expects. */
export function refusalHint(reason: string | null): string | null {
  switch (reason) {
    case 'SEGREGATION_OF_DUTIES':
      return 'Two people: the person who proposed, froze or built this cannot also decide or approve it. Another person must.'
    case 'WORKPAPER_NOT_READY':
      return 'The workpaper cannot reach READY_FOR_REVIEW yet. See what blocks it beside the status.'
    case 'WORKPAPER_SNAPSHOT_LOCKED':
      return 'The workpaper is frozen for review and its source snapshot is locked. Return it to draft first.'
    case 'WORKPAPER_STALE':
      return 'Someone changed the workpaper since it was read. It has been reloaded; decide again.'
    case 'WORKPAPER_EXISTS':
      return 'A workpaper is already open for this filer, duty and period with a different header.'
    case 'WORKPAPER_FORMULA_CHANGED':
      return 'The frozen build was assembled under an older formula. Return it to draft and rebuild.'
    default:
      return null
  }
}

/** The fields an action form collects, as typed. */
export interface ActionForm {
  note: string
  contentRelease: string
  fxCurrency: string
  fxRate: string
  fxSource: string
  fxDate: string
  fxMode: RoundingMode
  fxScale: string
  fxBasis: string
  adjKind: AdjustmentKind
  adjCategory: string
  adjAmount: string
  adjReason: string
  adjustmentNumber: string
  decision: Decision
  exceptionKey: string
}

export const EMPTY_FORM: ActionForm = {
  note: '', contentRelease: '', fxCurrency: '', fxRate: '', fxSource: '', fxDate: '', fxMode: 'HALF_EVEN', fxScale: '2',
  fxBasis: '', adjKind: 'AUDITED_ADJUSTMENT', adjCategory: '', adjAmount: '', adjReason: '', adjustmentNumber: '',
  decision: 'APPROVED', exceptionKey: '',
}

const DECIMAL = /^-?(0|[1-9][0-9]*)(\.[0-9]+)?$/
const RATE = /^(0|[1-9][0-9]*)(\.[0-9]+)?$/

/**
 * The request an action form sends, or why it cannot be sent. Every act
 * carries the event count the person read, so a workpaper that moved since
 * is refused rather than acted on blind. Only fields with a value are sent.
 */
export function actionBody(
  w: Pick<Workpaper, 'eventCount' | 'frozenBuildVersion' | 'exceptions'>,
  action: Action,
  f: ActionForm,
  evidence: Evidence | null,
): { body: ActionRequest } | { invalid: string } {
  const note = f.note.trim()
  const base: ActionRequest = { action, expectedSeq: w.eventCount, ...(note === '' ? {} : { note }) }
  switch (action) {
    case 'FREEZE':
      return { body: base }
    case 'RETURN_TO_DRAFT':
      return note === '' ? { invalid: 'Say why it goes back to draft.' } : { body: base }
    case 'APPROVE':
      return w.frozenBuildVersion === undefined ? { invalid: 'No build is frozen.' } : { body: { ...base, buildVersion: w.frozenBuildVersion } }
    case 'CHANGE_BASIS':
      if (f.contentRelease.trim() === '' || note === '') return { invalid: 'Name the new content release and say why it changes.' }
      return { body: { ...base, contentRelease: f.contentRelease.trim() } }
    case 'DOCUMENT_FX_RATE': {
      const scale = Number.parseInt(f.fxScale, 10)
      if (!/^[A-Za-z]{3}$/.test(f.fxCurrency.trim()) || !RATE.test(f.fxRate.trim()) || f.fxDate === ''
        || !(scale >= 0 && scale <= 12) || f.fxSource.trim() === '' || f.fxBasis.trim() === '') {
        return { invalid: 'A rate needs its source currency, a positive decimal rate, its source and date, a rounding scale of 0 to 12 and the conversion basis.' }
      }
      return {
        body: {
          ...base,
          fx: {
            currency: f.fxCurrency.trim().toUpperCase(), rate: f.fxRate.trim(), rateSource: f.fxSource.trim(), rateDate: f.fxDate,
            roundingMode: f.fxMode, scale, conversionBasis: f.fxBasis.trim(),
          },
        },
      }
    }
    case 'PROPOSE_ADJUSTMENT':
      if (!DECIMAL.test(f.adjAmount.trim())) return { invalid: 'The amount is a decimal string such as -1200.00, in the reporting currency.' }
      if (f.adjCategory.trim() === '' || f.adjReason.trim() === '') return { invalid: 'An adjustment names its category and its reason.' }
      if (evidence === null) return { invalid: 'Attach the evidence. Only its SHA-256, name and type are sent.' }
      return {
        body: {
          ...base,
          adjustment: { kind: f.adjKind, category: f.adjCategory.trim(), amount: f.adjAmount.trim(), reason: f.adjReason.trim() },
          evidence,
        },
      }
    case 'DECIDE_ADJUSTMENT': {
      const n = Number.parseInt(f.adjustmentNumber, 10)
      if (!(n >= 1)) return { invalid: 'Choose the adjustment to decide.' }
      if (f.decision === 'REJECTED' && note === '') return { invalid: 'A rejection says why.' }
      return { body: { ...base, adjustmentNumber: n, decision: f.decision } }
    }
    case 'PROPOSE_RESOLUTION': {
      const x = w.exceptions.find((e) => e.key === f.exceptionKey)
      if (x === undefined) return { invalid: 'Choose the exception to resolve.' }
      if (note === '') return { invalid: 'Explain the exception.' }
      if (evidence === null) return { invalid: 'Attach the evidence. Only its SHA-256, name and type are sent.' }
      return { body: { ...base, exceptionKey: x.key, fingerprint: x.fingerprint, evidence } }
    }
    case 'DECIDE_RESOLUTION':
      if (f.exceptionKey === '') return { invalid: 'Choose the resolution to decide.' }
      if (f.decision === 'REJECTED' && note === '') return { invalid: 'A rejection says why.' }
      return { body: { ...base, exceptionKey: f.exceptionKey, decision: f.decision } }
    case 'BUILD':
      return { invalid: 'A build is its own request.' }
  }
}
