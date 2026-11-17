/**
 * Filing-to-remittance pairing: each compliance package beside the payment
 * instruction(s) made for the same obligation.
 *
 * Pairing is by identifiers and strings only. The "same figure" check is
 * string equality of the filed control total and the instructed amount, both
 * canonical decimals from the cell, with their currencies; nothing is added,
 * subtracted or compared numerically (ADR-0019 C1).
 */
import type { S } from '@zoikotax/sdk'

type Package = S<'CompliancePackage'>
type Remittance = S<'Remittance'>

export type PairState =
  | 'FILED_AND_PAID'
  | 'IN_PROGRESS'
  | 'ACCEPTED_UNPAID'
  | 'NOT_YET_PAYABLE'
  | 'FILING_EXCEPTION'
  | 'PAYMENT_EXCEPTION'
  | 'PAYMENT_WITHOUT_FILING'
  | 'CANCELLED'

export const PAIR_STATES: readonly PairState[] = [
  'FILED_AND_PAID', 'IN_PROGRESS', 'ACCEPTED_UNPAID', 'NOT_YET_PAYABLE', 'FILING_EXCEPTION', 'PAYMENT_EXCEPTION',
  'PAYMENT_WITHOUT_FILING', 'CANCELLED',
]

export const PAIR_LABEL: Record<PairState, string> = {
  FILED_AND_PAID: 'Filed and paid',
  IN_PROGRESS: 'Filing and payment in progress',
  ACCEPTED_UNPAID: 'Accepted, no payment instruction',
  NOT_YET_PAYABLE: 'Not yet accepted, no payment',
  FILING_EXCEPTION: 'Filing rejected or uncertain',
  PAYMENT_EXCEPTION: 'Payment rejected, returned or uncertain',
  PAYMENT_WITHOUT_FILING: 'Payment with no filing on record',
  CANCELLED: 'Filing cancelled',
}

export const PAIR_TONE: Record<PairState, 'ok' | 'warn' | 'bad' | 'neutral'> = {
  FILED_AND_PAID: 'ok', IN_PROGRESS: 'neutral', ACCEPTED_UNPAID: 'warn', NOT_YET_PAYABLE: 'neutral',
  FILING_EXCEPTION: 'bad', PAYMENT_EXCEPTION: 'bad', PAYMENT_WITHOUT_FILING: 'bad', CANCELLED: 'neutral',
}

export type Figure = 'same' | 'differs' | 'n/a'

export interface Pair {
  readonly key: string
  readonly pkg: Package | undefined
  /** The instruction that stands: the live one, else the latest made. */
  readonly rem: Remittance | undefined
  /** Other instructions for the same obligation: cancelled, rejected or replaced. */
  readonly others: readonly Remittance[]
  /** How the two were matched. */
  readonly basis: 'obligation' | 'entity-period-authority' | 'none'
  readonly state: PairState
  /** The filed version's control total, as the cell recorded it. */
  readonly filed: { readonly amount: string; readonly currency: string; readonly version: number | undefined } | undefined
  readonly figure: Figure
  /** Period start, for ordering. */
  readonly sortKey: string
}

const DEAD: readonly string[] = ['CANCELLED', 'REJECTED']

const fallbackKey = (entity: string, start: string, end: string, authority: string) => `${entity}|${start}|${end}|${authority}`

function standing(list: readonly Remittance[]): [Remittance | undefined, Remittance[]] {
  const ordered = [...list].sort((a, b) => (a.createdAt < b.createdAt ? 1 : a.createdAt > b.createdAt ? -1 : 0))
  const live = ordered.find((r) => !DEAD.includes(r.status)) ?? ordered[0]
  return [live, ordered.filter((r) => r !== live)]
}

function stateOf(pkg: Package | undefined, rem: Remittance | undefined): PairState {
  if (pkg === undefined) {
    return 'PAYMENT_WITHOUT_FILING'
  }
  if (pkg.status === 'CANCELLED') {
    return 'CANCELLED'
  }
  if (pkg.status === 'REJECTED' || pkg.status === 'UNCERTAIN_SUBMISSION') {
    return 'FILING_EXCEPTION'
  }
  if (rem === undefined || rem.status === 'CANCELLED') {
    return pkg.status === 'ACCEPTED' ? 'ACCEPTED_UNPAID' : 'NOT_YET_PAYABLE'
  }
  if (['REJECTED', 'RETURNED', 'UNCERTAIN_PAYMENT'].includes(rem.status)) {
    return 'PAYMENT_EXCEPTION'
  }
  return pkg.status === 'ACCEPTED' && rem.status === 'SETTLED' ? 'FILED_AND_PAID' : 'IN_PROGRESS'
}

export function pairFilings(packages: readonly Package[], remittances: readonly Remittance[]): Pair[] {
  const byObligation = new Map<string, Remittance[]>()
  for (const r of remittances) {
    byObligation.set(r.obligationId, [...(byObligation.get(r.obligationId) ?? []), r])
  }
  const used = new Set<string>()
  const pairs: Pair[] = []

  for (const p of packages) {
    let basis: Pair['basis'] = 'none'
    let mine = (byObligation.get(p.obligationId) ?? []).filter((r) => !used.has(r.id))
    if (mine.length > 0) {
      basis = 'obligation'
    } else {
      // The obligation may have been superseded by a later row since one side was made.
      const k = fallbackKey(p.legalEntityId, p.periodStart, p.periodEnd, p.filingAuthority)
      mine = remittances.filter((r) => !used.has(r.id) && fallbackKey(r.legalEntityId, r.periodStart, r.periodEnd, r.authority) === k)
      basis = mine.length > 0 ? 'entity-period-authority' : 'none'
    }
    for (const r of mine) {
      used.add(r.id)
    }
    const [rem, others] = standing(mine)
    const prep = [...p.history].reverse().find((s) => s.kind === 'PREPARED')
    const filed = prep?.controlTotal === undefined || prep.currency === undefined
      ? undefined : { amount: prep.controlTotal, currency: prep.currency, version: prep.version }
    const figure: Figure = filed === undefined || rem === undefined ? 'n/a'
      : filed.amount === rem.amount && filed.currency === rem.currency ? 'same' : 'differs'
    pairs.push({ key: p.id, pkg: p, rem, others, basis, state: stateOf(p, rem), filed, figure, sortKey: p.periodStart })
  }

  // Payments no filing accounts for.
  const orphans = new Map<string, Remittance[]>()
  for (const r of remittances) {
    if (!used.has(r.id)) {
      orphans.set(r.obligationId, [...(orphans.get(r.obligationId) ?? []), r])
    }
  }
  for (const [obligationId, list] of orphans) {
    const [rem, others] = standing(list)
    pairs.push({
      key: `rem-${obligationId}`, pkg: undefined, rem, others, basis: 'none', state: 'PAYMENT_WITHOUT_FILING',
      filed: undefined, figure: 'n/a', sortKey: rem?.periodStart ?? '',
    })
  }

  return pairs.sort((a, b) => (a.sortKey < b.sortKey ? 1 : a.sortKey > b.sortKey ? -1 : 0))
}

export const PAIR_HEADER = [
  'State', 'Return', 'Filing authority', 'Period start', 'Period end', 'Filing status', 'Filed version', 'Filed total', 'Filed currency',
  'Payment status', 'Payment amount', 'Payment currency', 'Same figure', 'Payment reference', 'Matched on', 'Other instructions',
] as const

export const pairRow = (x: Pair): string[] => [
  PAIR_LABEL[x.state], x.pkg?.obligationType ?? '', x.pkg === undefined ? x.rem?.authority ?? '' : `${x.pkg.filingCountry} ${x.pkg.filingAuthority}`,
  x.pkg?.periodStart ?? x.rem?.periodStart ?? '', x.pkg?.periodEnd ?? x.rem?.periodEnd ?? '', x.pkg?.status ?? '',
  x.filed?.version === undefined ? '' : String(x.filed.version), x.filed?.amount ?? '', x.filed?.currency ?? '',
  x.rem?.status ?? '', x.rem?.amount ?? '', x.rem?.currency ?? '', x.figure, x.rem?.paymentReference ?? '',
  x.basis, String(x.others.length),
]
