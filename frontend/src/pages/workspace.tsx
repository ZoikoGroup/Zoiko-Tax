/**
 * Pieces the work-list screens share: reading several lists at once, a row
 * model that can come from any domain, a table of such rows, count tiles, and
 * downloads.
 *
 * Every figure stays the cell's canonical string (ADR-0019 C1). What the
 * browser does here is count rows and compare identifiers and dates as
 * strings; it never adds, subtracts or compares an amount numerically.
 */
import type { ReactNode } from 'react'

import type { Result } from '@zoikotax/sdk'

import type { View } from '../components/layout/Sidebar'
import { ApiError, UnreachableError } from '../platform/api'
import { Empty, ErrorNote, Status, type Load } from '../ui/kit'

export const words = (s: string) => s.replaceAll('_', ' ')

/** True when the session holds any of these roles. */
export const hasRole = (roles: readonly string[], ...wanted: readonly string[]) => wanted.some((r) => roles.includes(r))

// ---------------------------------------------------------------------------
// reading several lists
// ---------------------------------------------------------------------------

/** Two reads as one: the first refusal wins, otherwise both results. */
export function both<A, B>(a: Promise<Result<A>>, b: Promise<Result<B>>): Promise<Result<[A, B]>> {
  return Promise.all([a, b]).then(([ra, rb]) => {
    if (!ra.ok) {
      return ra
    }
    if (!rb.ok) {
      return rb
    }
    return { ok: true, data: [ra.data, rb.data] }
  })
}

/** Three reads as one: the first refusal wins, otherwise all three results. */
export function all3<A, B, C>(
  a: Promise<Result<A>>,
  b: Promise<Result<B>>,
  c: Promise<Result<C>>,
): Promise<Result<[A, B, C]>> {
  return Promise.all([both(a, b), c]).then(([rab, rc]) => {
    if (!rab.ok) {
      return rab
    }
    if (!rc.ok) {
      return rc
    }
    return { ok: true, data: [rab.data[0], rab.data[1], rc.data] }
  })
}

// ---------------------------------------------------------------------------
// rows from any domain
// ---------------------------------------------------------------------------

/** One item someone has to look at, wherever it lives. */
export interface WorkRow {
  readonly key: string
  /** The domain it comes from, e.g. "Obligation". */
  readonly source: string
  readonly item: ReactNode
  readonly period: string
  readonly status: string
  /** Why it is on this list, in words. */
  readonly why: string
  /** The date shown, already formatted; '' when there is none. */
  readonly due: string
  /** ISO date or timestamp the list is ordered by (string order). */
  readonly sortKey: string
  readonly bad?: boolean
  readonly open: { readonly view: View; readonly tab?: string }
}

/** A list from one source, already turned into rows when it has been read. */
export interface Listed {
  readonly label: string
  readonly load: Load<readonly WorkRow[]>
}

/** One source's read, as rows. */
export function listed<T>(label: string, load: Load<T>, rows: (data: T) => readonly WorkRow[]): Listed {
  return { label, load: load.status === 'ready' ? { status: 'ready', data: rows(load.data) } : load }
}

/**
 * Rows from several sources in one table, ordered by date. A source still
 * reading or refused says so above the table, so one refusal (a role the
 * user lacks for that domain) does not hide what the others found.
 */
export const WorkTable = ({ sources, empty, onOpen }: {
  sources: readonly Listed[]
  empty: string
  onOpen: (row: WorkRow) => void
}) => {
  const rows: WorkRow[] = []
  const notes: ReactNode[] = []
  let reading = 0
  for (const s of sources) {
    if (s.load.status === 'ready') {
      rows.push(...s.load.data)
    } else if (s.load.status === 'loading') {
      reading += 1
    } else {
      notes.push(
        <div key={s.label}>
          <p className="ztax-note"><strong>{s.label}</strong> could not be read:</p>
          <ErrorNote error={s.load.error} />
        </div>,
      )
    }
  }
  rows.sort((a, b) => (a.sortKey < b.sortKey ? -1 : a.sortKey > b.sortKey ? 1 : 0))
  return (
    <>
      {reading > 0 ? <p className="muted" aria-live="polite">Reading {reading} of {sources.length} lists…</p> : null}
      {notes}
      {rows.length === 0 ? (reading > 0 ? null : <Empty>{empty}</Empty>) : (
        <div style={{ overflowX: 'auto' }}>
          <table className="ztax-action-table">
            <thead>
              <tr>
                <th>Area</th>
                <th>Item</th>
                <th>Period</th>
                <th>Status</th>
                <th>Why it is here</th>
                <th>Date</th>
                <th>Action</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => (
                <tr key={r.key}>
                  <td style={{ paddingRight: 8, whiteSpace: 'nowrap' }}>{r.source}</td>
                  <td style={{ fontWeight: 500, color: '#111827', paddingRight: 8 }}>{r.item}</td>
                  <td style={{ paddingRight: 8, whiteSpace: 'nowrap' }}>{r.period}</td>
                  <td style={{ paddingRight: 8 }}><Status value={r.status} /></td>
                  <td style={{ paddingRight: 8, color: r.bad === true ? '#b91c1c' : '#6b7280' }}>{r.why}</td>
                  <td style={{ paddingRight: 8, whiteSpace: 'nowrap' }}>{r.due === '' ? '—' : r.due}</td>
                  <td style={{ whiteSpace: 'nowrap' }}>
                    <button type="button" className="ztax-table-action-btn" onClick={() => { onOpen(r) }}>Open</button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </>
  )
}

// ---------------------------------------------------------------------------
// counts
// ---------------------------------------------------------------------------

export interface Tile {
  readonly label: string
  readonly count: number
  readonly note?: string
  readonly tone?: 'ok' | 'warn' | 'bad' | 'neutral'
}

const TONE_COLOUR: Record<NonNullable<Tile['tone']>, string> = {
  ok: '#065f46', warn: '#92400e', bad: '#b91c1c', neutral: '#111827',
}

/** Counts of rows, as a row of tiles. Never a sum of amounts. */
export const Tiles = ({ tiles }: { tiles: readonly Tile[] }) => (
  <div className="ztax-kpi-grid">
    {tiles.map((t) => (
      <section key={t.label} className="ztax-card" aria-label={t.label}>
        <div className="ztax-kpi-header"><span>{t.label}</span></div>
        <div className="ztax-kpi-value" style={{ color: t.count > 0 ? TONE_COLOUR[t.tone ?? 'neutral'] : '#111827' }}>
          {t.count}
        </div>
        {t.note === undefined ? null : <div className="ztax-kpi-footer"><span className="muted">{t.note}</span></div>}
      </section>
    ))}
  </div>
)

/** How many rows carry each value, in first-seen order. */
export function countBy<T>(rows: readonly T[], key: (row: T) => string): [string, number][] {
  const counts = new Map<string, number>()
  for (const r of rows) {
    const k = key(r)
    counts.set(k, (counts.get(k) ?? 0) + 1)
  }
  return [...counts.entries()]
}

// ---------------------------------------------------------------------------
// downloads
// ---------------------------------------------------------------------------

const csvCell = (s: string) => (/[",\n\r]/.test(s) ? `"${s.replaceAll('"', '""')}"` : s)

const today = () => new Date().toISOString().slice(0, 10)

function save(blob: Blob, name: string) {
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = name
  a.click()
  URL.revokeObjectURL(url)
}

const slug = (title: string) => title.toLowerCase().replaceAll(/[^a-z0-9]+/g, '-').replaceAll(/^-|-$/g, '')

/** A table as CSV, every cell exactly the string shown. */
export function downloadCsv(title: string, header: readonly string[], rows: readonly (readonly string[])[]) {
  const text = [header, ...rows].map((row) => row.map(csvCell).join(',')).join('\n') + '\n'
  save(new Blob([text], { type: 'text/csv;charset=utf-8' }), `${slug(title)}-${today()}.csv`)
}

/** A record as the cell sent it, as JSON. */
export function downloadJson(title: string, value: unknown) {
  save(new Blob([JSON.stringify(value, null, 2) + '\n'], { type: 'application/json' }), `${slug(title)}-${today()}.json`)
}

// ---------------------------------------------------------------------------
// the administration surface's errors
// ---------------------------------------------------------------------------

/** True when an administration call was refused for want of a role. */
export const isAdminForbidden = (error: unknown) =>
  error instanceof ApiError && (error.reasonCode === 'FORBIDDEN' || error.status === 403)

/** An administration call's failure, in words, branching on its reason code. */
export const AdminErrorNote = ({ error, forbidden }: { error: unknown; forbidden: string }) => {
  if (isAdminForbidden(error)) {
    return <p className="ztax-error-note" role="alert">{forbidden}</p>
  }
  if (error instanceof ApiError) {
    return <p className="ztax-error-note" role="alert"><span className="mono">{error.reasonCode}: {error.message}</span></p>
  }
  if (error instanceof UnreachableError) {
    return <p className="ztax-error-note" role="alert">The cell did not respond: <span className="mono">{error.message}</span></p>
  }
  return <p className="ztax-error-note" role="alert">The request failed.</p>
}
