import { useState, type ReactNode } from 'react'

import type { S } from '@zoikotax/sdk'

import { instant } from '../dashboard/format'
import { ztax } from '../platform/client'
import { Amount, Card, Empty, Field, Id, Loaded, Status, useLoad, type Load } from '../ui/kit'

// The latest records of each kind that was once reachable only by pasting its
// id: decisions, refunds, batch jobs, fiscal documents and reconciliation
// runs. Each row opens the record in the view beside it. Figures are the
// cell's own strings; nothing here computes one (ADR-0019 C1).

interface Column<T> {
  readonly head: string
  readonly cell: (row: T) => ReactNode
}

function RecentTable<T>({ rows, columns, rowId, onOpen, selected, empty }: {
  rows: readonly T[]
  columns: readonly Column<T>[]
  rowId: (row: T) => string
  onOpen: (id: string) => void
  selected?: string | undefined
  empty: string
}) {
  if (rows.length === 0) {
    return <Empty>{empty}</Empty>
  }
  return (
    <div style={{ overflowX: 'auto' }}>
      <table className="ztax-action-table">
        <thead>
          <tr>
            {columns.map((c) => <th key={c.head}>{c.head}</th>)}
            <th aria-label="Open" />
          </tr>
        </thead>
        <tbody>
          {rows.map((row) => {
            const rid = rowId(row)
            return (
              <tr key={rid} aria-current={rid === selected ? 'true' : undefined}>
                {columns.map((c) => <td key={c.head} style={{ paddingRight: 8 }}>{c.cell(row)}</td>)}
                <td>
                  <button type="button" className="ztax-btn-quiet" disabled={rid === selected} onClick={() => { onOpen(rid) }}>
                    {rid === selected ? 'Showing' : 'Open'}
                  </button>
                </td>
              </tr>
            )
          })}
        </tbody>
      </table>
    </div>
  )
}

function RecentCard<T>({ title, load, reload, children, filters }: {
  title: string
  load: Load<T>
  reload: () => void
  children: (data: T) => ReactNode
  filters?: ReactNode
}) {
  return (
    <Card
      title={title}
      aside={
        <div className="ztax-filter-bar">
          {filters}
          <button type="button" className="ztax-btn-quiet" onClick={reload}>Refresh</button>
        </div>
      }
    >
      <Loaded load={load}>{children}</Loaded>
    </Card>
  )
}

const when = (t: string | undefined) => (t === undefined ? '—' : instant(t))

// --- decisions ---------------------------------------------------------------

const OUTCOMES: readonly S<'Outcome'>[] = ['ADVISORY', 'AUTHORITATIVE', 'AMBIGUOUS', 'CONFLICTED', 'UNSUPPORTED', 'REVIEW_REQUIRED']
const isOutcome = (v: string): v is S<'Outcome'> => (OUTCOMES as readonly string[]).includes(v)

export const RecentDecisions = ({ selected, onOpen }: { selected?: string | undefined; onOpen: (id: string) => void }) => {
  const [outcome, setOutcome] = useState<S<'Outcome'> | ''>('')
  const [currentOnly, setCurrentOnly] = useState(false)
  const { load, reload } = useLoad(
    (signal) => ztax.listDecisions(outcome === '' ? { limit: 100 } : { outcome, limit: 100 }, signal),
    [outcome],
  )
  return (
    <RecentCard
      title="Recent decisions"
      load={load}
      reload={reload}
      filters={
        <>
          <Field label="Outcome">
            <select value={outcome} onChange={(e) => { setOutcome(isOutcome(e.target.value) ? e.target.value : '') }}>
              <option value="">Any</option>
              {OUTCOMES.map((o) => <option key={o} value={o}>{o.replaceAll('_', ' ')}</option>)}
            </select>
          </Field>
          <label>
            <input type="checkbox" checked={currentOnly} onChange={(e) => { setCurrentOnly(e.target.checked) }} /> Current only
          </label>
        </>
      }
    >
      {(d) => (
        <RecentTable
          rows={currentOnly ? d.decisions.filter((x) => x.supersededBy === undefined) : d.decisions}
          rowId={(x) => x.id}
          selected={selected}
          onOpen={onOpen}
          empty="No decision recorded yet. Commit one in Quote & commit."
          columns={[
            { head: 'Business key', cell: (x) => <span className="mono">{x.businessKey}</span> },
            { head: 'Outcome', cell: (x) => <Status value={x.outcome} /> },
            { head: 'Event', cell: (x) => when(x.eventTime) },
            { head: 'Recorded', cell: (x) => when(x.recordedAt) },
            {
              head: 'Version', cell: (x) => (x.supersededBy !== undefined ? <Status value="SUPERSEDED" />
                : x.supersedes !== undefined ? 'Correction' : 'Original'),
            },
            { head: 'Decision', cell: (x) => <Id value={x.id} /> },
          ]}
        />
      )}
    </RecentCard>
  )
}

// --- refunds -----------------------------------------------------------------

export const RecentRefunds = ({ selected, onOpen }: { selected?: string | undefined; onOpen: (id: string) => void }) => {
  const { load, reload } = useLoad((signal) => ztax.listRefunds({ limit: 100 }, signal), [])
  return (
    <RecentCard title="Recent refunds" load={load} reload={reload}>
      {(d) => (
        <RecentTable
          rows={d.refunds}
          rowId={(x) => x.id}
          selected={selected}
          onOpen={onOpen}
          empty="No refund requested yet. Refunds are requested from a decision, in the Decision tab."
          columns={[
            { head: 'Requested', cell: (x) => when(x.requestedAt) },
            { head: 'Amount', cell: (x) => <Amount amount={x.amount.amount} currency={x.amount.currency} /> },
            { head: 'Status', cell: (x) => <Status value={x.status} /> },
            { head: 'Reference', cell: (x) => <span className="mono">{x.paymentReference}</span> },
            { head: 'Decision', cell: (x) => <Id value={x.decisionId} /> },
          ]}
        />
      )}
    </RecentCard>
  )
}

// --- batch jobs --------------------------------------------------------------

export const RecentJobs = ({ selected, onOpen }: { selected?: string | undefined; onOpen: (id: string) => void }) => {
  const { load, reload } = useLoad((signal) => ztax.listJobs({ limit: 50 }, signal), [])
  return (
    <RecentCard title="Recent jobs" load={load} reload={reload}>
      {(d) => (
        <RecentTable
          rows={d.jobs}
          rowId={(x) => x.id}
          selected={selected}
          onOpen={onOpen}
          empty="No batch submitted yet."
          columns={[
            { head: 'Requested', cell: (x) => when(x.requestedAt) },
            { head: 'Status', cell: (x) => <Status value={x.status} /> },
            { head: 'Items', cell: (x) => <span className="mono">{x.itemCount}</span> },
            { head: 'Succeeded', cell: (x) => <span className="mono">{x.succeeded}</span> },
            { head: 'Failed', cell: (x) => <span className="mono">{x.failed}</span> },
            { head: 'Pending', cell: (x) => <span className="mono">{x.pending}</span> },
            { head: 'Finished', cell: (x) => when(x.finishedAt) },
          ]}
        />
      )}
    </RecentCard>
  )
}

// --- fiscal documents --------------------------------------------------------

/** Document types that correct or cancel another. */
export const CORRECTION_TYPES: ReadonlySet<string> = new Set([
  'CREDIT_NOTE', 'DEBIT_NOTE', 'PARTIAL_CREDIT', 'VOID', 'REFUND', 'REBILL', 'ADJUSTMENT', 'AMENDMENT', 'RESTATEMENT',
])

export const RecentDocuments = ({ title = 'Fiscal archive', selected, onOpen, only, empty }: {
  title?: string
  selected?: string | undefined
  onOpen: (id: string) => void
  /** Keep only the documents this accepts. */
  only?: (d: S<'FiscalDocument'>) => boolean
  empty?: string
}) => {
  const { load, reload } = useLoad((signal) => ztax.listDocuments({ limit: 100 }, signal), [])
  return (
    <RecentCard title={title} load={load} reload={reload}>
      {(d) => (
        <RecentTable
          rows={only === undefined ? d.documents : d.documents.filter(only)}
          rowId={(x) => x.id}
          selected={selected}
          onOpen={onOpen}
          empty={empty ?? 'No document committed yet. Issue one from a committed decision.'}
          columns={[
            { head: 'Type', cell: (x) => x.type.replaceAll('_', ' ') },
            { head: 'Number', cell: (x) => <span className="mono">{x.number ?? '—'}</span> },
            { head: 'Issued', cell: (x) => <span className="mono">{x.issueDate}</span> },
            { head: 'Tax', cell: (x) => <Amount amount={x.taxTotal} currency={x.currency} /> },
            { head: 'Gross', cell: (x) => <Amount amount={x.grossTotal} currency={x.currency} /> },
            { head: 'Status', cell: (x) => <Status value={x.status} /> },
            { head: 'Corrects', cell: (x) => (x.predecessors.length === 0 ? '—' : <Id value={x.predecessors[0]} />) },
          ]}
        />
      )}
    </RecentCard>
  )
}

// --- reconciliation runs -----------------------------------------------------

export const RecentRuns = ({ title = 'Runs', selected, onOpen, exceptionsOnly = false }: {
  title?: string
  selected?: string | undefined
  onOpen: (id: string) => void
  /** Keep only runs with a break still unresolved. */
  exceptionsOnly?: boolean
}) => {
  const { load, reload } = useLoad((signal) => ztax.listReconciliations({ limit: 100 }, signal), [])
  return (
    <RecentCard title={title} load={load} reload={reload}>
      {(d) => (
        <RecentTable
          rows={exceptionsOnly ? d.runs.filter((r) => r.exceptionCount > r.resolvedCount) : d.runs}
          rowId={(x) => x.id}
          selected={selected}
          onOpen={onOpen}
          empty={exceptionsOnly ? 'No run has an unresolved variance.' : 'No reconciliation run yet. Run one above.'}
          columns={[
            { head: 'Period', cell: (x) => <span className="mono">{x.period}</span> },
            { head: 'Ran', cell: (x) => when(x.ranAt) },
            { head: 'First break', cell: (x) => (x.firstBreak === undefined ? <Status value="MATCHED" /> : <Status value={x.firstBreak} />) },
            { head: 'Items', cell: (x) => <span className="mono">{x.itemCount}</span> },
            { head: 'Exceptions', cell: (x) => <span className="mono">{x.exceptionCount}</span> },
            { head: 'Resolved', cell: (x) => <span className="mono">{x.resolvedCount}</span> },
          ]}
        />
      )}
    </RecentCard>
  )
}
