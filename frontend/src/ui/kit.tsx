/**
 * Building blocks every workspace page shares, so a page is about its own
 * surface and not about loading states.
 *
 * Calls go through the generated SDK (ADR-0019 C4), which returns a Result
 * rather than throwing. Amounts stay strings and are shown with
 * dashboard/format.ts (C1). Nothing here stores anything in the browser (C8).
 */
import { useCallback, useEffect, useState, type ReactNode } from 'react'

import { ZoikoTaxError, type MoneyValue, type Result } from '@zoikotax/sdk'

import { moneyText } from '../dashboard/format'

// ---------------------------------------------------------------------------
// errors
// ---------------------------------------------------------------------------

/** What a failed call says to a person: the reason code and the cell's words. */
export function errorText(error: unknown): string {
  if (error instanceof ZoikoTaxError) {
    return `${error.reasonCode}: ${error.message}`
  }
  return error instanceof Error ? error.message : 'the request failed'
}

/** True when the cell refused for want of a role, which is not a fault. */
export function isForbidden(error: unknown): boolean {
  return error instanceof ZoikoTaxError && error.reasonCode === 'FORBIDDEN'
}

/** True when the cell has no content bundle loaded. */
export function isNoContent(error: unknown): boolean {
  return error instanceof ZoikoTaxError && error.reasonCode === 'NO_CONTENT_BUNDLE'
}

export const ErrorNote = ({ error }: { error: unknown }) => {
  if (isForbidden(error)) {
    return (
      <p className="ztax-error-note" role="alert">
        Your roles do not include this. It needs a fiscal role — OPERATOR to change, ANALYST or AUDITOR to read.
      </p>
    )
  }
  if (isNoContent(error)) {
    return (
      <p className="ztax-error-note" role="alert">
        This cell has no content bundle loaded, so it cannot serve this. Set the content variables in .env (see
        .env.example) and restart.
        <span className="mono muted"> {errorText(error)}</span>
      </p>
    )
  }
  return (
    <p className="ztax-error-note" role="alert">
      <span className="mono">{errorText(error)}</span>
    </p>
  )
}

// ---------------------------------------------------------------------------
// loading
// ---------------------------------------------------------------------------

export type Load<T> =
  | { readonly status: 'loading' }
  | { readonly status: 'ready'; readonly data: T }
  | { readonly status: 'error'; readonly error: unknown }

/**
 * Reads on mount and whenever `deps` change; `reload` reads again. The call
 * receives an AbortSignal, so a page left before its read returns does not
 * set state afterwards.
 */
export function useLoad<T>(
  call: (signal: AbortSignal) => Promise<Result<T>>,
  deps: readonly unknown[],
): { load: Load<T>; reload: () => void } {
  const [load, setLoad] = useState<Load<T>>({ status: 'loading' })
  const [attempt, setAttempt] = useState(0)

  useEffect(() => {
    const controller = new AbortController()
    setLoad({ status: 'loading' })
    void call(controller.signal).then((result) => {
      if (controller.signal.aborted) {
        return
      }
      setLoad(result.ok ? { status: 'ready', data: result.data } : { status: 'error', error: result.error })
    })
    return () => { controller.abort() }
    // `call` is a fresh closure every render; `deps` are what it closes over,
    // so they and `attempt` are what a re-read depends on.
  }, [...deps, attempt])

  const reload = useCallback(() => { setAttempt((n) => n + 1) }, [])
  return { load, reload }
}

/** Renders a Load: a quiet line while reading, the error, or the content. */
export function Loaded<T>({ load, children }: { load: Load<T>; children: (data: T) => ReactNode }) {
  if (load.status === 'loading') {
    return <p className="muted" aria-live="polite">Reading…</p>
  }
  if (load.status === 'error') {
    return <ErrorNote error={load.error} />
  }
  return <>{children(load.data)}</>
}

/**
 * A write. `run` sends it and remembers the outcome; `busy` is true while it
 * is in flight, so a button cannot send it twice.
 */
export function useAction<T>(): {
  busy: boolean
  error: unknown
  done: T | null
  run: (call: () => Promise<Result<T>>) => Promise<T | null>
  reset: () => void
} {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [done, setDone] = useState<T | null>(null)
  const run = useCallback(async (call: () => Promise<Result<T>>) => {
    setBusy(true)
    setError(null)
    try {
      const result = await call()
      if (result.ok) {
        setDone(result.data)
        return result.data
      }
      setError(result.error)
      return null
    } finally {
      setBusy(false)
    }
  }, [])
  const reset = useCallback(() => {
    setError(null)
    setDone(null)
  }, [])
  return { busy, error, done, run, reset }
}

/**
 * A fresh Idempotency-Key. Minted per attempt by the caller and reused on a
 * retry of the same request (ADR-0013); a page that retries a failed write
 * keeps the key it minted rather than calling this again.
 */
export function newKey(): string {
  // crypto.randomUUID exists only in a secure context (HTTPS or localhost);
  // getRandomValues exists everywhere, so the app also works when opened by
  // a LAN address over plain HTTP. A version 4 UUID from 16 random bytes.
  const b = crypto.getRandomValues(new Uint8Array(16))
  b[6] = ((b[6] ?? 0) & 0x0f) | 0x40
  b[8] = ((b[8] ?? 0) & 0x3f) | 0x80
  const hex = Array.from(b, (x) => x.toString(16).padStart(2, '0')).join('')
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`
}

// ---------------------------------------------------------------------------
// layout
// ---------------------------------------------------------------------------

export const PageHeader = ({ title, subtitle, actions }: { title: string; subtitle?: ReactNode; actions?: ReactNode }) => (
  <div className="ztax-title-bar">
    <div>
      <h1 className="ztax-title-text">{title}</h1>
      {subtitle === undefined ? null : <div className="ztax-status-line">{subtitle}</div>}
    </div>
    {actions === undefined ? null : <div className="ztax-title-actions">{actions}</div>}
  </div>
)

export const Card = ({ title, aside, children }: { title?: string; aside?: ReactNode; children: ReactNode }) => (
  <section className="ztax-card ztax-page-card">
    {title === undefined && aside === undefined ? null : (
      <div className="ztax-table-header">
        {title === undefined ? <span /> : <h2 className="ztax-card-title">{title}</h2>}
        {aside}
      </div>
    )}
    {children}
  </section>
)

/** Two or more cards side by side, wrapping on a narrow screen. */
export const Columns = ({ children }: { children: ReactNode }) => <div className="ztax-page-columns">{children}</div>

export const Empty = ({ children }: { children: ReactNode }) => <p className="ztax-empty-note">{children}</p>

// ---------------------------------------------------------------------------
// values
// ---------------------------------------------------------------------------

/** An amount with its currency, or a dash. Never a number. */
export const Money = ({ value }: { value: MoneyValue | undefined }) => (
  <span className="mono">{value === undefined ? '—' : moneyText(value)}</span>
)

/** A decimal string with the currency beside it. */
export const Amount = ({ amount, currency }: { amount: string | undefined; currency: string | undefined }) =>
  amount === undefined || currency === undefined ? <span className="mono">—</span> : <Money value={{ amount, currency }} />

const TONE: Record<string, 'ok' | 'warn' | 'bad' | 'neutral'> = {
  ACTIVE: 'ok', MATCHED: 'ok', TOLERANCE_MATCH: 'ok', RESOLVED: 'ok', EXPLAINED: 'ok', COMPLETED: 'ok', VALID: 'ok',
  MATCH: 'ok', COMMITTED: 'ok', READY: 'ok', FILED: 'ok', ACCEPTED: 'ok', PAID: 'ok', SUCCEEDED: 'ok', DELIVERED: 'ok',
  ADVISORY: 'neutral', OPEN: 'neutral', PENDING: 'neutral', REQUESTED: 'neutral', QUEUED: 'neutral', RUNNING: 'neutral',
  SOFT_CLOSE: 'neutral', HARD_CLOSE: 'neutral', SEALED: 'neutral', RELEASED: 'neutral', REVOKED: 'warn',
  OVERDUE: 'bad', REJECTED: 'bad', FAILED: 'bad', UNCERTAIN: 'bad', DIVERGED: 'bad', CONFLICTED: 'bad', MISSING: 'bad',
  UNMATCHED: 'bad', DUPLICATE: 'bad', PARTIAL: 'warn', AMENDMENT_REQUIRED: 'warn', DATA_REQUIRED: 'warn',
  REVIEW_REQUIRED: 'warn', AMBIGUOUS: 'warn', VOIDED: 'warn', PARTIALLY_CREDITED: 'warn', FULLY_CREDITED: 'warn',
  BUNDLE_UNAVAILABLE: 'warn', SUSPENDED: 'warn', CLOSED: 'neutral', REOPENED: 'warn', AMENDMENT_ACTIVE: 'warn',
}

/** A status as a coloured tag. */
export const Status = ({ value }: { value: string | undefined }) => (
  <span className={`ztax-status ztax-status--${value === undefined ? 'neutral' : (TONE[value] ?? 'neutral')}`}>
    {value === undefined ? '—' : value.replaceAll('_', ' ')}
  </span>
)

/** A long identifier, shortened for a table, whole on hover and for copy. */
export const Id = ({ value }: { value: string | undefined }) =>
  value === undefined ? <span className="mono">—</span> : (
    <span className="mono" title={value}>{value.length > 13 ? `${value.slice(0, 8)}…${value.slice(-4)}` : value}</span>
  )

/** The raw document, for whoever needs every field. */
export const Raw = ({ value, label = 'Full record' }: { value: unknown; label?: string }) => (
  <details className="ztax-raw">
    <summary>{label}</summary>
    <pre>{JSON.stringify(value, null, 2)}</pre>
  </details>
)

// ---------------------------------------------------------------------------
// forms
// ---------------------------------------------------------------------------

export const Field = ({ label, children, help }: { label: string; children: ReactNode; help?: string }) => (
  <label className="field ztax-field">
    <span>{label}</span>
    {children}
    {help === undefined ? null : <span className="field__help">{help}</span>}
  </label>
)

export const Submit = ({ busy, children }: { busy: boolean; children: ReactNode }) => (
  <button type="submit" className="ztax-btn-copper" disabled={busy}>
    {busy ? 'Sending…' : children}
  </button>
)

/** The outcome of a write: its error, or a short confirmation. */
export const Outcome = ({ error, done }: { error: unknown; done: ReactNode }) => (
  <>
    {error === null || error === undefined ? null : <ErrorNote error={error} />}
    {done}
  </>
)
