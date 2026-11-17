/**
 * The rules that put an item on a work list, one function per domain, so the
 * Action Center and each workspace's own exception tab agree on what an
 * exception or a pending approval is. Statuses come from the contract
 * (schema.ts); nothing here looks at an amount.
 */
import type { S } from '@zoikotax/sdk'

import { civilDate, instant } from '../dashboard/format'
import { words, type WorkRow } from './workspace'

type Obligation = S<'Obligation'>
type Package = S<'CompliancePackage'>
type Remittance = S<'Remittance'>
type Beneficiary = S<'Beneficiary'>

const period = (start: string, end: string) => `${civilDate(start)} – ${civilDate(end)}`

// ---------------------------------------------------------------------------
// exceptions
// ---------------------------------------------------------------------------

/** Obligation statuses that need a person, and why. */
export const OBLIGATION_EXCEPTIONS: Readonly<Partial<Record<S<'EffectiveObligationStatus'>, string>>> = {
  OVERDUE: 'Past its due date in the authority’s calendar and not filed',
  REJECTED: 'The authority rejected the return',
  UNCERTAIN: 'Unknown whether the authority received the return',
  AMENDMENT_REQUIRED: 'A correction landed after filing; an amended return is needed',
  DATA_REQUIRED: 'Data is missing before it can be filed',
  SUSPENDED: 'Suspended by hand',
}

export function obligationExceptions(list: readonly Obligation[]): WorkRow[] {
  return list.flatMap((o) => {
    if (o.supersededBy !== undefined) {
      return []
    }
    const why = OBLIGATION_EXCEPTIONS[o.effectiveStatus]
    return why === undefined ? [] : [{
      key: `obl-${o.id}`,
      source: 'Obligation',
      item: <>{o.type} <span className="mono muted">{o.jurisdiction} · {o.authority}</span></>,
      period: period(o.periodStart, o.periodEnd),
      status: o.effectiveStatus,
      why,
      due: civilDate(o.dueDate),
      sortKey: o.dueDate,
      bad: o.effectiveStatus === 'OVERDUE' || o.effectiveStatus === 'REJECTED' || o.effectiveStatus === 'UNCERTAIN',
      open: { view: 'obligations', tab: 'register' },
    }]
  })
}

const PACKAGE_EXCEPTIONS: Readonly<Partial<Record<S<'ComplianceStatus'>, string>>> = {
  REJECTED: 'The authority rejected the filing',
  UNCERTAIN_SUBMISSION: 'Unknown whether the authority received it; resolve before anything is resent',
}

export function packageExceptions(list: readonly Package[]): WorkRow[] {
  return list.flatMap((p) => {
    const finished = p.status === 'ACCEPTED' || p.status === 'CANCELLED'
    const why = PACKAGE_EXCEPTIONS[p.status]
      ?? (finished ? undefined
        : p.overdue.statutory ? 'The authority’s deadline has passed unfiled'
        : p.overdue.internal ? 'One of our own targets has passed' : undefined)
    return why === undefined ? [] : [{
      key: `pkg-${p.id}`,
      source: 'Filing',
      item: <>{p.obligationType} <span className="mono muted">{p.filingCountry} · {p.filingAuthority}</span></>,
      period: period(p.periodStart, p.periodEnd),
      status: p.status,
      why,
      due: instant(p.statutoryDue),
      sortKey: p.statutoryDue,
      bad: p.status === 'REJECTED' || p.status === 'UNCERTAIN_SUBMISSION' || p.overdue.statutory,
      open: { view: 'compliance', tab: 'queue' },
    }]
  })
}

const REMITTANCE_EXCEPTIONS: Readonly<Partial<Record<S<'RemittanceStatus'>, string>>> = {
  REJECTED: 'The provider or authority rejected the payment',
  RETURNED: 'The money was sent back after settlement',
  UNCERTAIN_PAYMENT: 'Unknown whether the payment went out; resolve before anything is resent',
}

export function remittanceExceptions(list: readonly Remittance[]): WorkRow[] {
  return list.flatMap((r) => {
    const done = r.status === 'SETTLED' || r.status === 'CANCELLED'
    const why = REMITTANCE_EXCEPTIONS[r.status] ?? (!done && r.overdue ? 'The payment due date has passed unpaid' : undefined)
    return why === undefined ? [] : [{
      key: `rem-${r.id}`,
      source: 'Payment',
      item: <>{r.beneficiary.legalName} <span className="mono muted">{r.authority} · {r.paymentReference}</span></>,
      period: period(r.periodStart, r.periodEnd),
      status: r.status,
      why,
      due: instant(r.paymentDue),
      sortKey: r.paymentDue,
      bad: true,
      open: { view: 'remittance', tab: 'status' },
    }]
  })
}

// ---------------------------------------------------------------------------
// approvals
// ---------------------------------------------------------------------------

/** A package in review: waiting for a reviewer, or reviewed and waiting for approval. */
export function packageApprovals(list: readonly Package[]): WorkRow[] {
  return list.filter((p) => p.status === 'IN_REVIEW').map((p) => {
    const last = [...p.history].reverse().find((s) => s.kind === 'REVIEWED' || s.kind === 'PREPARED')
    const prepared = [...p.history].reverse().find((s) => s.kind === 'PREPARED')
    const reviewed = last?.kind === 'REVIEWED' && last.review === 'ACCEPT'
    const v = prepared?.version === undefined ? '' : ` v${prepared.version}`
    return {
      key: `pkg-${p.id}`,
      source: 'Filing',
      item: <>{p.obligationType} <span className="mono muted">{p.filingCountry} · {p.filingAuthority}</span></>,
      period: period(p.periodStart, p.periodEnd),
      status: reviewed ? 'APPROVAL_REQUIRED' : 'REVIEW_REQUIRED',
      why: reviewed ? `Reviewed${v}; waiting for approval by someone other than the preparer` : `Prepared${v}; waiting for an independent review`,
      due: instant(p.reviewDue ?? p.statutoryDue),
      sortKey: p.reviewDue ?? p.statutoryDue,
      bad: p.overdue.statutory,
      open: { view: 'compliance', tab: 'queue' },
    }
  })
}

export function remittanceApprovals(list: readonly Remittance[]): WorkRow[] {
  return list.filter((r) => r.status === 'APPROVAL_REQUIRED').map((r) => ({
    key: `rem-${r.id}`,
    source: 'Payment',
    item: <>{r.beneficiary.legalName} <span className="mono muted">{r.authority} · {r.paymentReference}</span></>,
    period: period(r.periodStart, r.periodEnd),
    status: r.status,
    why: r.ready
      ? 'Instruction made; waiting for approval of its exact hash by someone other than its maker'
      : `Waiting for approval; blocked on ${r.readiness.filter((c) => !c.ready).map((c) => words(c.dimension)).join(', ')}`,
    due: instant(r.paymentDue),
    sortKey: r.paymentDue,
    bad: r.overdue,
    open: { view: 'remittance', tab: 'queue' },
  }))
}

export function beneficiaryApprovals(list: readonly Beneficiary[]): WorkRow[] {
  return list.filter((b) => b.state === 'UNVERIFIED' || b.state === 'CHANGE_PENDING').map((b) => ({
    key: `ben-${b.id}`,
    source: 'Payee',
    item: <>{b.legalName} <span className="mono muted">{b.country} · {b.authority} · {words(b.rail)}</span></>,
    period: '—',
    status: b.state,
    why: b.state === 'CHANGE_PENDING'
      ? 'Changed payee details waiting for verification; the old version stays payable until then'
      : 'New payee details waiting for verification by someone other than their author',
    due: instant(b.createdAt),
    sortKey: b.createdAt,
    open: { view: 'remittance', tab: 'payments' },
  }))
}
