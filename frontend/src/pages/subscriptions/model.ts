/**
 * Pieces of the Subscriptions & Usage page (Engineering Doc 18 W04, W08,
 * W09): the plan form, who may act on a plan revision, how a usage line
 * reads, and the check that a basis digest is what the listed rows give.
 *
 * Every count is the exact integer the cell counted from its own records;
 * nothing here estimates or rounds one. A percentage drawn for a bar is a
 * whole number for display only.
 */
import { ZoikoTaxError, type S } from '@zoikotax/sdk'

export type Plan = S<'SubscriptionPlan'>
export type PlanList = S<'SubscriptionPlanList'>
export type PlanRequest = S<'SubscriptionPlanRequest'>
export type PlanAction = S<'SubscriptionPlanAction'>
export type Meter = S<'UsageMeter'>
export type Overage = S<'UsageOveragePolicy'>
export type UsageReport = S<'UsageReport'>
export type UsageLine = S<'UsageLine'>
export type UsageState = S<'UsageState'>
export type UsageBasis = S<'UsageBasis'>

export const METERS: readonly { value: Meter; label: string }[] = [
  { value: 'COMMITTED_DECISIONS', label: 'Committed tax decisions' },
  { value: 'FISCAL_DOCUMENTS', label: 'Fiscal documents issued' },
  { value: 'FILING_SUBMISSIONS', label: 'Filing submissions' },
  { value: 'SHADOW_RUNS', label: 'Shadow comparison runs' },
]

export const meterLabel = (m: string) => METERS.find((x) => x.value === m)?.label ?? m.replaceAll('_', ' ')

export const STATE_LABEL: Record<UsageState, string> = {
  NO_PLAN: 'No plan in force',
  NOT_CONTRACTED: 'Not in the plan',
  WITHIN_QUOTA: 'Within quota',
  AT_QUOTA: 'At quota',
  OVER_QUOTA: 'Over quota',
}

export const STATE_TONE: Record<UsageState, 'ok' | 'warn' | 'bad' | 'neutral'> = {
  NO_PLAN: 'neutral', NOT_CONTRACTED: 'neutral', WITHIN_QUOTA: 'ok', AT_QUOTA: 'warn', OVER_QUOTA: 'bad',
}

/** Whole percent of the included quantity used, capped at 100, for a bar; null without a quota. */
export function usedPercent(l: Pick<UsageLine, 'count' | 'included'>): number | null {
  if (l.included === undefined) return null
  if (l.included === 0) return l.count === 0 ? 0 : 100
  return Math.min(100, Math.floor((l.count * 100) / l.included))
}

export interface PlanOffer {
  readonly action: PlanAction
  readonly blocked: string | null
}

/** What a person is offered on a plan revision: only a PROPOSED one moves, and the proposer does not approve it. */
export function planOffers(p: Pick<Plan, 'status' | 'createdBy'>, roles: readonly string[], me: string): PlanOffer[] {
  if (p.status !== 'PROPOSED') return []
  const acts: PlanAction[] = ['APPROVE', 'REJECT', 'WITHDRAW']
  return acts.map((action) => {
    if (!roles.includes('ADMIN')) return { action, blocked: 'Only an ADMIN acts on plans.' }
    if (action === 'APPROVE' && p.createdBy === me) {
      return { action, blocked: 'You proposed this plan; a different administrator approves it.' }
    }
    return { action, blocked: null }
  })
}

export interface QuotaRow {
  meter: Meter
  included: string
  overage: Overage
  on: boolean
}

export interface PlanForm {
  planCode: string
  planName: string
  contractRef: string
  termStart: string
  termEnd: string
  quotas: QuotaRow[]
}

const WHOLE = /^(0|[1-9][0-9]{0,12})$/

/** A whole-number count typed as digits, at most 13 of them — exact as a JS integer. */
function wholeOf(digits: string): number {
  let n = 0
  for (const c of digits) n = n * 10 + (c.charCodeAt(0) - 48)
  return n
}
const DATE = /^[0-9]{4}-[0-9]{2}-[0-9]{2}$/

/** The request a plan form sends, or why it cannot be sent. Included quantities are whole numbers. */
export function planRequest(f: PlanForm, today: string): { body: PlanRequest } | { invalid: string } {
  const code = f.planCode.trim().toUpperCase()
  if (!/^[A-Z0-9][A-Z0-9_.-]{0,63}$/.test(code)) return { invalid: 'A plan code is capital letters, digits, dots, underscores or hyphens.' }
  if (f.planName.trim() === '') return { invalid: 'Give the plan a name.' }
  if (f.contractRef.trim() === '') return { invalid: 'Name the signed contract the plan comes from.' }
  if (!DATE.test(f.termStart) || f.termStart < today) return { invalid: 'A plan’s term starts today or later.' }
  if (f.termEnd !== '' && (!DATE.test(f.termEnd) || f.termEnd < f.termStart)) return { invalid: 'A term ends on or after it starts.' }
  const quotas: PlanRequest['quotas'] = []
  for (const q of f.quotas) {
    if (!q.on) continue
    const n = q.included.trim()
    if (!WHOLE.test(n) || (n.length === 13 && n > '1000000000000')) {
      return { invalid: `${meterLabel(q.meter)}: the included quantity is a whole number from 0 to 1,000,000,000,000.` }
    }
    quotas.push({ meter: q.meter, included: wholeOf(n), overage: q.overage })
  }
  if (quotas.length === 0) return { invalid: 'Contract at least one metered capability.' }
  return {
    body: {
      planCode: code, planName: f.planName.trim(), contractRef: f.contractRef.trim(), termStart: f.termStart, quotas,
      ...(f.termEnd === '' ? {} : { termEnd: f.termEnd }),
    },
  }
}

/**
 * The basis digest a list of row ids gives: SHA-256, lowercase hex, over
 * the ids sorted bytewise and joined by newlines — the rule the cell
 * digests by. Comparing it with `basisDigest` checks the count row by row.
 */
export async function basisDigest(ids: readonly string[]): Promise<string> {
  const sorted = [...ids].sort((a, b) => (a < b ? -1 : a > b ? 1 : 0))
  const bytes = new TextEncoder().encode(sorted.join('\n'))
  const sum = await crypto.subtle.digest('SHA-256', bytes)
  return Array.from(new Uint8Array(sum), (b) => b.toString(16).padStart(2, '0')).join('')
}

export const reasonOf = (error: unknown): string | null => (error instanceof ZoikoTaxError ? error.reasonCode : null)
