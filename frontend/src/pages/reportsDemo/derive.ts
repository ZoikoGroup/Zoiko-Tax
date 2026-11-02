/**
 * Pure helpers for the Reports (demo) page: display formatting, SVG geometry,
 * the seven-step view of an explanation, and the CSV export. No React, no I/O.
 *
 * Nothing here computes a figure. Every number shown is the string the demo
 * service sent. Bar proportions are supplied by the server as per-mille
 * integers (ADR-0019 C1: the server provides derived figures; the browser only
 * positions them). The only remaining BigInt arithmetic in Charts.tsx is on
 * layout constants and parsed per-mille integers, never on a fiscal value.
 */
import { displayAmount, fiscalAmount, money } from '../../fiscal/money'
import type { ExplainReply, ExplainTarget, Fixture } from './model'

// ---------------------------------------------------------------------------
// display
// ---------------------------------------------------------------------------

/** "EUR 182,500.00", or the plain "EUR 182500.00" if the string is not a canonical decimal. */
export function formatMoney(amount: string, currency: string): string {
  try {
    const m = money(amount, currency)
    return `${m.currency} ${displayAmount(m.amount)}`
  } catch {
    return `${currency} ${amount}`
  }
}

/** An amount with grouping and no currency, for a column that has its own currency column. */
export function formatAmount(amount: string): string {
  try {
    return displayAmount(fiscalAmount(amount))
  } catch {
    return amount
  }
}

/** A figure's value with its unit: money, a percentage, or a bare count. */
export function formatFigure(value: string, unit: string): string {
  if (unit === '%') {
    return `${value}%`
  }
  if (unit === 'count') {
    return value
  }
  return formatMoney(value, unit)
}

// ---------------------------------------------------------------------------
// SVG geometry, in BigInt per-mille (bar proportions supplied by the server)
// ---------------------------------------------------------------------------

/** Tenths of a percent as an SVG length: 734n -> "73.4%". */
export function pct(tenths: bigint): string {
  return `${tenths / 10n}.${tenths % 10n}%`
}

// ---------------------------------------------------------------------------
// tabs and ids
// ---------------------------------------------------------------------------

export type TabId = 'obligations' | 'filing' | 'payments' | 'shadow'

export const TABS: readonly { readonly id: TabId; readonly label: string }[] = [
  { id: 'obligations', label: 'Obligations Register' },
  { id: 'filing', label: 'Filing Status' },
  { id: 'payments', label: 'Payments / Remittances' },
  { id: 'shadow', label: 'Shadow Assurance' },
]

/** Which tab a row id lives on, or null for a KPI / chart id (FIG-, TRD-). */
export function tabOfId(id: string): TabId | null {
  if (id.startsWith('OBL-')) return 'obligations'
  if (id.startsWith('FIL-')) return 'filing'
  if (id.startsWith('PAY-')) return 'payments'
  if (id.startsWith('SHD-')) return 'shadow'
  return null
}

/** Shown on PAID in the Obligations Register only. */
export const PAID_TOOLTIP = 'Payment/remittance evidence confirmed'
/** Shown on SETTLED on the Payments / Remittances tab only. */
export const SETTLED_TOOLTIP = 'Payment confirmed received by tax authority'
export const FORECAST_TOOLTIP =
  'A derived month-on-month metric, computed from the last two monthly points. Not a forecast of future values.'

// ---------------------------------------------------------------------------
// CSV of the visible register
// ---------------------------------------------------------------------------

export interface CsvTable {
  readonly headers: readonly string[]
  readonly rows: readonly (readonly string[])[]
}

/** The visible register as headers and rows -- the same fixture fields the tab shows. */
export function csvTable(fixture: Fixture, tab: TabId): CsvTable {
  switch (tab) {
    case 'obligations':
      return {
        headers: [
          'Id', 'Type', 'Jurisdiction', 'Authority', 'Period', 'Status', 'Due Date',
          'Assessed Amount', 'Currency', `${fixture.currency} Equivalent`,
        ],
        rows: fixture.obligations.map((o) => [
          o.id, o.type, o.jurisdiction, o.authority, o.period, o.status, o.dueDate,
          o.amount, o.currency, o.currency === fixture.currency ? '' : o.amountUsd,
        ]),
      }
    case 'filing':
      return {
        headers: ['Id', 'Bucket', 'Count', 'Percent', 'Obligations'],
        rows: fixture.buckets.map((b) => [b.id, b.label, b.count, b.percent, b.sourceIds.join(' ')]),
      }
    case 'payments':
      return {
        headers: ['Payment id', 'Obligation', 'Amount', 'Currency', 'Date', 'Status'],
        rows: fixture.payments.map((p) => [p.id, p.obligationId, p.amount, p.currency, p.date, p.status]),
      }
    case 'shadow':
      return {
        headers: ['Id', 'Obligation', 'Comparator value', 'Zoiko value', 'Absolute variance', 'Result', 'Case state'],
        rows: fixture.shadow.map((s) => [
          s.id, s.obligationId, s.comparatorValue, s.zoikoValue, s.absoluteVariance, s.result, s.caseState,
        ]),
      }
  }
}

/** One CSV cell: quoted when needed, and neutralised if a spreadsheet would read it as a formula. */
function csvCell(value: string): string {
  const safe = /^[=+\-@]/.test(value) ? `'${value}` : value
  return /[",\r\n]/.test(safe) ? `"${safe.replaceAll('"', '""')}"` : safe
}

export function toCsv(table: CsvTable): string {
  return [table.headers, ...table.rows].map((row) => row.map(csvCell).join(',')).join('\r\n') + '\r\n'
}

// ---------------------------------------------------------------------------
// the seven-step view of one explanation
// ---------------------------------------------------------------------------

export type StepState = 'pending' | 'passed' | 'stopped'

export interface StepSpec {
  readonly n: number
  readonly title: string
  readonly state: StepState
  readonly note: string
}

// The reason strings below are the demo service's own, from
// intelligence/src governance.Refusal, server.py (REASON_*) and
// report_ai_http.py's guard. The reply carries a reason, not a "stopped at"
// step, so the stopping step is read from them. A reason this page does not
// recognise from a REVIEW_REQUIRED reply is treated as having stopped before
// the Gateway (the service's own pre-call checks: unknown kind, no ids found).
const GOVERNANCE_REASONS: readonly string[] = [
  'AI_UNKNOWN_USE_CASE', 'AI_USE_CASE_SUSPENDED', 'AI_KILL_SWITCH_ENGAGED', 'AI_AUTHORITY_REFUSED',
  'AI_RISK_TIER_EXCEEDED', 'AI_RESIDENCY_REFUSED', 'AI_MALFORMED_CONTEXT', 'AI_DATA_CLASS_REFUSED',
]
const MODEL_CALL_REASONS: readonly string[] = ['AI_GATEWAY_UNAVAILABLE', 'AI_GATEWAY_NOT_CONFIGURED']
const GUARD_REASONS: readonly string[] = [
  'the explanation cites no row id',
  'cited id(s) not in the supplied rows',
  'explanation states a status that is not the cited row',
  'explanation states a number not present in the supplied rows',
]

function startsWithAny(text: string, prefixes: readonly string[]): boolean {
  return prefixes.some((p) => text.startsWith(p))
}

const STEP_INPUT = 1
const STEP_GOVERNANCE = 2
const STEP_MODEL_CALL = 3
const STEP_OUTPUT_CHECKS = 4
const STEP_DISPOSITION = 5

/** The step a non-EXPLAINED reply stopped at; null for EXPLAINED. */
export function stoppedAt(reply: ExplainReply): number | null {
  if (reply.outcome === 'EXPLAINED') {
    return null
  }
  if (reply.outcome === 'REVIEW_REQUIRED') {
    if (startsWithAny(reply.reason, GOVERNANCE_REASONS)) return STEP_GOVERNANCE
    if (startsWithAny(reply.reason, MODEL_CALL_REASONS)) return STEP_MODEL_CALL
    return STEP_INPUT
  }
  // ABSTAINED: either the deterministic guard rejected a reply (Output checks),
  // or the model itself abstained and there was nothing to check (Disposition).
  return startsWithAny(reply.reason, GUARD_REASONS) ? STEP_OUTPUT_CHECKS : STEP_DISPOSITION
}

const UNAFFECTED = 'The figures on this page are deterministic and unaffected.'

export function explainSteps(target: Pick<ExplainTarget, 'kind' | 'ids'>, reply: ExplainReply): readonly StepSpec[] {
  const stop = stoppedAt(reply)
  const at = (n: number): StepState => {
    if (stop === null || n < stop) return 'passed'
    return n === stop ? 'stopped' : 'pending'
  }
  const modelAbstained = stop === STEP_DISPOSITION
  const hasProvenance = reply.provenance !== null
  const stoppedNote = `${reply.reason} ${UNAFFECTED}`

  return [
    {
      n: 1, title: 'Input', state: at(1),
      note: at(1) === 'stopped'
        ? stoppedNote
        : `Asked to explain ${target.ids.join(', ')} (${target.kind}). Only those rows were sent to the model.`,
    },
    {
      n: 2, title: 'Governance gate', state: at(2),
      note: at(2) === 'stopped'
        ? stoppedNote
        : at(2) === 'pending'
          ? 'Not reached.'
          : 'Cleared at authority A1, risk tier T1, data class P0, region local — fixed by the demo service, not returned by the reply.',
    },
    {
      n: 3, title: 'Model call', state: at(3),
      note: at(3) === 'stopped'
        ? stoppedNote
        : at(3) === 'pending'
          ? 'Not reached.'
          : 'The Model Gateway returned a reply.',
    },
    {
      n: 4, title: 'Output checks', state: modelAbstained ? 'pending' : at(4),
      note: modelAbstained
        ? 'Not run: the model abstained, so there was no explanation to check.'
        : at(4) === 'stopped'
          ? `The deterministic guard rejected the reply: ${reply.reason} ${UNAFFECTED}`
          : at(4) === 'pending'
            ? 'Not reached.'
            : 'The deterministic guard (not the model) checked the explanation against the cited rows: cited ids exist, and its numbers and status words match them. It cannot prove the sentence is true, so a person should still check it.',
    },
    {
      n: 5, title: 'Disposition', state: at(5),
      note: stop === null
        ? 'EXPLAINED — an advisory explanation whose figures were checked against the cited rows; a person should still check it. It never changes a figure on this page.'
        : at(5) === 'stopped'
          ? `ABSTAINED — ${reply.reason} ${UNAFFECTED}`
          : `Not reached — the outcome is ${reply.outcome}, decided at the step above.`,
    },
    {
      n: 6, title: 'Human review', state: 'pending',
      note: stop === null
        ? 'A person reviews the explanation against the register. Demo only: nothing here is recorded.'
        : 'A person should check this.',
    },
    {
      n: 7, title: 'Evidence', state: hasProvenance ? 'passed' : 'pending',
      note: hasProvenance
        ? 'Provenance returned with the reply. The Model Gateway keeps the full evidence record (AIGOV-001 section 14) on its side; it is not shown on this page.'
        : 'Not reached: the call stopped before a reply was produced.',
    },
  ]
}
