/**
 * One line of the active worked pack, as a person types it, and the request
 * body it becomes.
 *
 * The names — line.netAmount, line.quantity and the two flags — are the
 * pack's, not the contract's. Accumulators are never sent: the cell reads the
 * ones its content binds, and a request that supplies one is refused.
 */
import type { S } from '@zoikotax/sdk'

import { Field } from '../../ui/kit'
import { CURRENCY, isDecimal, nowUtcLocal, toTimestamp } from './shared'

export interface LineDraft {
  readonly businessKey: string
  readonly eventTime: string
  readonly netAmount: string
  readonly currency: string
  readonly quantity: string
  readonly unit: string
  readonly reducedRateApplies: boolean
  readonly exemptCertificateHeld: boolean
}

export function emptyLine(businessKey = ''): LineDraft {
  return {
    businessKey,
    eventTime: nowUtcLocal(),
    netAmount: '',
    currency: 'EUR',
    quantity: '1',
    unit: 'EA',
    reducedRateApplies: false,
    exemptCertificateHeld: false,
  }
}

/** What is wrong with a line, or null. `needKey` for a commit, not a quote. */
export function lineProblem(line: LineDraft, needKey: boolean): string | null {
  if (needKey && line.businessKey.trim() === '') {
    return 'A business key is required to commit.'
  }
  if (toTimestamp(line.eventTime) === null) {
    return 'The event time must be a date and time.'
  }
  if (!isDecimal(line.netAmount.trim())) {
    return 'The net amount must be a decimal such as 100.00 — digits, an optional fraction, no thousands separators.'
  }
  if (!CURRENCY.test(line.currency.trim())) {
    return 'The currency must be a three-letter ISO 4217 code such as EUR.'
  }
  if (!isDecimal(line.quantity.trim())) {
    return 'The quantity must be a decimal such as 1 or 2.5.'
  }
  if (line.unit.trim() === '') {
    return 'The unit is required, e.g. EA.'
  }
  return null
}

function input(line: LineDraft): S<'DeterminationInput'> {
  return {
    money: { 'line.netAmount': { amount: line.netAmount.trim(), currency: line.currency.trim() } },
    quantities: { 'line.quantity': { value: line.quantity.trim(), unit: line.unit.trim() } },
    flags: {
      'line.reducedRateApplies': line.reducedRateApplies,
      'line.exemptCertificateHeld': line.exemptCertificateHeld,
    },
  }
}

/** The quote body. Call only once lineProblem(line, false) is null. */
export function quoteBody(line: LineDraft): S<'QuoteRequest'> {
  return { eventTime: toTimestamp(line.eventTime) ?? '', input: input(line) }
}

/** The commit (or adjust) body. Call only once lineProblem(line, true) is null. */
export function commitBody(line: LineDraft, supersedes?: string): S<'CommitRequest'> {
  const base: S<'CommitRequest'> = {
    businessKey: line.businessKey.trim(),
    eventTime: toTimestamp(line.eventTime) ?? '',
    input: input(line),
  }
  const s = supersedes?.trim() ?? ''
  return s === '' ? base : { ...base, supersedes: s }
}

/** The fields of one line. `idPrefix` keeps ids unique when several are shown. */
export const LineFields = ({ line, onChange, idPrefix }: {
  line: LineDraft
  onChange: (line: LineDraft) => void
  idPrefix: string
}) => {
  const set = <K extends keyof LineDraft>(k: K, v: LineDraft[K]) => { onChange({ ...line, [k]: v }) }
  return (
    <>
      <Field label="Business key" help="Your stable reference for the line, e.g. INV-0001/1.">
        <input value={line.businessKey} onChange={(e) => { set('businessKey', e.target.value) }} autoComplete="off" />
      </Field>
      <Field label="Event time (UTC)">
        <input
          type="datetime-local"
          step="1"
          value={line.eventTime}
          onChange={(e) => { set('eventTime', e.target.value) }}
        />
      </Field>
      <Field label="Net amount" help="line.netAmount — a decimal, sent exactly as typed.">
        <input
          inputMode="decimal"
          value={line.netAmount}
          onChange={(e) => { set('netAmount', e.target.value) }}
          placeholder="100.00"
          autoComplete="off"
        />
      </Field>
      <Field label="Currency">
        <input
          value={line.currency}
          onChange={(e) => { set('currency', e.target.value.toUpperCase()) }}
          maxLength={3}
          autoComplete="off"
        />
      </Field>
      <Field label="Quantity" help="line.quantity">
        <input inputMode="decimal" value={line.quantity} onChange={(e) => { set('quantity', e.target.value) }} />
      </Field>
      <Field label="Unit">
        <input value={line.unit} onChange={(e) => { set('unit', e.target.value) }} autoComplete="off" />
      </Field>
      <label className="ztax-field ztax-check" htmlFor={`${idPrefix}-reduced`}>
        <input
          id={`${idPrefix}-reduced`}
          type="checkbox"
          checked={line.reducedRateApplies}
          onChange={(e) => { set('reducedRateApplies', e.target.checked) }}
        />
        <span>Reduced rate applies <span className="mono muted">line.reducedRateApplies</span></span>
      </label>
      <label className="ztax-field ztax-check" htmlFor={`${idPrefix}-exempt`}>
        <input
          id={`${idPrefix}-exempt`}
          type="checkbox"
          checked={line.exemptCertificateHeld}
          onChange={(e) => { set('exemptCertificateHeld', e.target.checked) }}
        />
        <span>Exemption certificate held <span className="mono muted">line.exemptCertificateHeld</span></span>
      </label>
    </>
  )
}
