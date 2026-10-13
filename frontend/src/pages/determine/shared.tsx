/**
 * Pieces the Determine and E-Invoicing pages share: a tab bar, the emitted
 * values of a quote or decision, timestamps as the contract writes them, and
 * an Idempotency-Key that survives a retry of the same request.
 *
 * Amounts stay the strings a person typed or the server sent (ADR-0019 C1);
 * the only checks here are on their shape.
 */
import { useState, type ReactNode } from 'react'

import { ZoikoTaxError, type S } from '@zoikotax/sdk'

import { Money, newKey } from '../../ui/kit'

// ---------------------------------------------------------------------------
// tabs
// ---------------------------------------------------------------------------

export function TabBar<T extends string>({ label, tabs, current, onSelect }: {
  label: string
  tabs: readonly { readonly value: T; readonly label: string }[]
  current: T
  onSelect: (value: T) => void
}) {
  return (
    <nav className="tabs" aria-label={label}>
      {tabs.map((t) => (
        <button
          key={t.value}
          type="button"
          className={t.value === current ? 'tab tab--selected' : 'tab'}
          aria-current={t.value === current ? 'page' : undefined}
          onClick={() => { onSelect(t.value) }}
        >
          {t.label}
        </button>
      ))}
    </nav>
  )
}

// ---------------------------------------------------------------------------
// values
// ---------------------------------------------------------------------------

/** A canonical decimal: optional minus, no leading zeros, optional fraction. */
export const DECIMAL = /^-?(0|[1-9][0-9]*)(\.[0-9]+)?$/

export const CURRENCY = /^[A-Z]{3}$/

export function isDecimal(value: string): boolean {
  return DECIMAL.test(value)
}

/** One emitted value, shown by its type; a MONEY value through Money. */
export const ResultValueText = ({ value }: { value: S<'ResultValue'> }) => {
  switch (value.type) {
    case 'MONEY':
      return value.amount === undefined || value.currency === undefined
        ? <span className="mono">—</span>
        : <Money value={{ amount: value.amount, currency: value.currency }} />
    case 'RATE':
      return <span className="mono">{value.value ?? '—'} {value.basis ?? ''}</span>
    case 'QUANTITY':
      return <span className="mono">{value.value ?? '—'} {value.unit ?? ''}</span>
    case 'BOOL':
      return <span className="mono">{value.flag === undefined ? '—' : value.flag ? 'true' : 'false'}</span>
    case 'STRING':
      return <span>{value.text ?? '—'}</span>
    case 'REASON_CODE':
      return <span className="mono">{value.reasonCode ?? '—'}</span>
  }
}

/** The values a quote or decision emitted, one row per result slot. */
export const EmittedTable = ({ emitted, caption }: { emitted: S<'Emitted'>; caption: string }) => {
  const slots = Object.entries(emitted)
  if (slots.length === 0) {
    return <p className="ztax-empty-note">Nothing was emitted.</p>
  }
  return (
    <table className="table">
      <caption className="table__caption">{caption}</caption>
      <thead>
        <tr>
          <th scope="col">Component</th>
          <th scope="col">Type</th>
          <th scope="col">Value</th>
        </tr>
      </thead>
      <tbody>
        {slots.map(([slot, value]) => (
          <tr key={slot}>
            <td className="mono">{slot}</td>
            <td>{value.type}</td>
            <td><ResultValueText value={value} /></td>
          </tr>
        ))}
      </tbody>
    </table>
  )
}

// ---------------------------------------------------------------------------
// time
// ---------------------------------------------------------------------------

/** The current UTC minute as a datetime-local value, "2026-10-09T10:32". */
export function nowUtcLocal(): string {
  return new Date().toISOString().slice(0, 16)
}

/** Today's UTC date, "2026-10-09". */
export function todayUtc(): string {
  return new Date().toISOString().slice(0, 10)
}

/**
 * A datetime-local value, read as UTC, as the contract's Timestamp: exactly
 * six fractional digits and Z. "2026-09-24T18:00" → "2026-09-24T18:00:00.000000Z".
 * Null when the value is not a date and time.
 */
export function toTimestamp(local: string): string | null {
  const m = /^(\d{4}-\d{2}-\d{2})T(\d{2}:\d{2})(?::(\d{2})(?:\.(\d{1,6}))?)?$/.exec(local.trim())
  if (m === null) {
    return null
  }
  const seconds = m[3] ?? '00'
  const fraction = (m[4] ?? '').padEnd(6, '0')
  return `${m[1] ?? ''}T${m[2] ?? ''}:${seconds}.${fraction}Z`
}

/** A Timestamp back to a datetime-local value, for pre-filling a field. */
export function toLocalInput(timestamp: string): string {
  return timestamp.slice(0, 16)
}

// ---------------------------------------------------------------------------
// idempotency
// ---------------------------------------------------------------------------

/**
 * The Idempotency-Key for a write (ADR-0013). A key is minted when a person
 * sends a request; sending the identical request again after it failed reuses
 * that key, so a retry after a transient failure cannot record twice. A
 * different request, or any request after a success, gets a new key.
 */
export function useIdempotencyKey(): {
  keyFor: (body: unknown) => string
  succeeded: () => void
  current: string | null
} {
  const [state, setState] = useState<{ key: string; body: string; settled: boolean } | null>(null)
  const keyFor = (body: unknown): string => {
    const text = JSON.stringify(body)
    if (state !== null && !state.settled && state.body === text) {
      return state.key
    }
    const key = newKey()
    setState({ key, body: text, settled: false })
    return key
  }
  const succeeded = () => {
    setState((s) => (s === null ? s : { ...s, settled: true }))
  }
  return { keyFor, succeeded, current: state?.key ?? null }
}

/** The key a write is using, so a person can quote it. */
export const KeyNote = ({ value }: { value: string | null }) =>
  value === null ? null : (
    <p className="muted" style={{ fontSize: 11, margin: 0 }}>
      Idempotency-Key <span className="mono">{value}</span> — sending the same request again reuses it.
    </p>
  )

// ---------------------------------------------------------------------------
// errors
// ---------------------------------------------------------------------------

export function isNotFound(error: unknown): boolean {
  return error instanceof ZoikoTaxError && error.status === 404
}

/** A form-level problem found before sending anything. */
export const Invalid = ({ message }: { message: string | null }) =>
  message === null ? null : <p className="ztax-error-note" role="alert">{message}</p>

/** A labelled group of key/values. */
export const KV = ({ rows }: { rows: readonly (readonly [string, ReactNode])[] }) => (
  <dl className="ztax-kv">
    {rows.map(([k, v]) => (
      <div key={k} style={{ display: 'contents' }}>
        <dt>{k}</dt>
        <dd>{v}</dd>
      </div>
    ))}
  </dl>
)
