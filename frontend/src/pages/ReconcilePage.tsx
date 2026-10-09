import { useEffect, useState, type FormEvent } from 'react'

import type { S } from '@zoikotax/sdk'

import { instant, periodLabel } from '../dashboard/format'
import { ztax } from '../platform/client'
import { Amount, Card, Empty, Field, Id, Outcome, PageHeader, Status, Submit, useAction } from '../ui/kit'
import { lines } from './evidence/time'
import './evidence/pages.css'
import type { PageProps } from './types'

type Run = S<'ReconciliationRun'>
type Item = S<'ReconciliationItem'>
type Stage = S<'ReconStage'>
type RootCause = S<'RootCause'>
type ResolutionAction = S<'ResolutionRequest'>['action']

const STAGE_LABEL: Record<Stage, string> = {
  R1_CALCULATED_TO_DOCUMENT: 'R1 · calculated → document',
  R2_DOCUMENT_TO_COLLECTION: 'R2 · document → collection',
  R3_DOCUMENT_TO_SUBLEDGER: 'R3 · document → subledger',
  R4_SUBLEDGER_TO_RETURN: 'R4 · subledger → return',
  R5_RETURN_TO_REMITTANCE: 'R5 · return → remittance',
  R6_SUBLEDGER_TO_GL: 'R6 · subledger → GL',
  R7_END_TO_END: 'R7 · end to end',
}

const ACTIONS: readonly ResolutionAction[] = ['ADJUST', 'RECLASSIFY', 'AMEND', 'WAIT', 'WAIVE_WITH_APPROVAL', 'EXTERNAL_CORRECTION']

const ROOT_CAUSES: readonly RootCause[] = [
  'CLASSIFICATION', 'JURISDICTION', 'TAX_RULE', 'ROUNDING', 'FX', 'DOCUMENT', 'COLLECTION', 'POSTING', 'RETURN',
  'REMITTANCE', 'GL', 'TIMING', 'DATA', 'OTHER',
]

const MATCHED: ReadonlySet<Item['status']> = new Set(['MATCHED', 'TOLERANCE_MATCH'])
const SETTLED: ReadonlySet<Item['status']> = new Set(['RESOLVED', 'EXPLAINED'])

/** An exception a person may still resolve: not matched, not already resolved. */
const isOpenException = (item: Item) =>
  !MATCHED.has(item.status) && !SETTLED.has(item.status) && item.resolution === undefined

function counts(items: readonly Item[]) {
  let matched = 0
  let pending = 0
  let resolved = 0
  let breaks = 0
  for (const item of items) {
    if (MATCHED.has(item.status)) matched += 1
    else if (item.status === 'PENDING') pending += 1
    else if (SETTLED.has(item.status) || item.resolution !== undefined) resolved += 1
    else breaks += 1
  }
  return { matched, pending, resolved, breaks }
}

const PERIOD = /^\d{4}-(0[1-9]|1[0-2])$/

export const ReconcilePage = ({ session, period }: PageProps) => {
  const operator = session.user.roles.includes('OPERATOR')
  const [runs, setRuns] = useState<Run[]>([])
  const [selected, setSelected] = useState<string | null>(null)
  const [target, setTarget] = useState(period)
  const [openId, setOpenId] = useState('')
  const runAction = useAction<Run>()
  const openAction = useAction<Run>()

  useEffect(() => { setTarget(period) }, [period])

  const remember = (run: Run) => {
    setRuns((prev) => [run, ...prev.filter((r) => r.id !== run.id)])
    setSelected(run.id)
  }

  const onRun = (e: FormEvent) => {
    e.preventDefault()
    void runAction.run(() => ztax.runReconciliation({ period: target })).then((run) => {
      if (run !== null) remember(run)
    })
  }

  const onOpen = (e: FormEvent) => {
    e.preventDefault()
    const id = openId.trim()
    void openAction.run(() => ztax.getReconciliation(id)).then((run) => {
      if (run !== null) {
        remember(run)
        setOpenId('')
      }
    })
  }

  const replaceItem = (runId: string, item: Item) => {
    setRuns((prev) =>
      prev.map((r) => (r.id === runId ? { ...r, items: r.items.map((i) => (i.id === item.id ? item : i)) } : r)),
    )
  }

  const current = runs.find((r) => r.id === selected)

  return (
    <div className="ztax-page">
      <PageHeader
        title="Reconcile"
        subtitle={<span>Stage-by-stage comparison of a legal period · header period {periodLabel(period)}</span>}
      />

      <Card title="Run a reconciliation">
        <p className="muted zp-intro">
          A run compares the period stage by stage and records every item, matched or not, with its exact variance. No
          tolerance applies. Each run is a record of its own; a later run of the same period does not replace an earlier
          one.
        </p>
        {operator ? null : (
          <p className="ztax-error-note" role="note">
            Running a reconciliation and resolving its exceptions needs the OPERATOR role, which you do not hold. You can
            still open a run by its id.
          </p>
        )}
        <form className="ztax-form" onSubmit={onRun} aria-label="Run a reconciliation">
          <Field label="Legal period (YYYY-MM)">
            <input
              type="month"
              value={target}
              required
              pattern="\d{4}-\d{2}"
              onChange={(e) => { setTarget(e.target.value) }}
            />
          </Field>
          <div className="ztax-form-actions">
            <Submit busy={runAction.busy}>Run for {PERIOD.test(target) ? periodLabel(target) : 'period'}</Submit>
            {target === period ? null : (
              <button type="button" className="ztax-btn-quiet" onClick={() => { setTarget(period) }}>
                Use header period
              </button>
            )}
          </div>
        </form>
        <Outcome error={runAction.error} done={null} />

        <form className="ztax-form" onSubmit={onOpen} aria-label="Open a run by id">
          <Field label="Open a run by id" help="There is no list of past runs; a run id comes from its result or the audit trail.">
            <input
              className="mono"
              value={openId}
              required
              placeholder="01920ab0-1a2b-7c3d-8e4f-5a6b7c8d9e0f"
              onChange={(e) => { setOpenId(e.target.value) }}
            />
          </Field>
          <div className="ztax-form-actions">
            <Submit busy={openAction.busy}>Open run</Submit>
          </div>
        </form>
        <Outcome error={openAction.error} done={null} />
      </Card>

      {runs.length === 0 ? null : (
        <Card title="Runs in this session" aside={<span className="ztax-timestamp">{runs.length} kept until you leave</span>}>
          <div className="zp-scroll">
            <table className="table">
              <thead>
                <tr>
                  <th scope="col">Run</th>
                  <th scope="col">Period</th>
                  <th scope="col">Ran at</th>
                  <th scope="col">First break (R7)</th>
                  <th scope="col">Items</th>
                  <th scope="col"><span className="zp-sr-only">Open</span></th>
                </tr>
              </thead>
              <tbody>
                {runs.map((r) => (
                  <tr key={r.id} className={r.id === selected ? 'row--current' : undefined}>
                    <td><Id value={r.id} /></td>
                    <td>{periodLabel(r.period)}</td>
                    <td>{instant(r.ranAt)}</td>
                    <td>{r.firstBreak === undefined ? <span className="tag tag--ok">none</span> : STAGE_LABEL[r.firstBreak]}</td>
                    <td>{r.items.length}</td>
                    <td>
                      <button
                        type="button"
                        className="ztax-btn-quiet"
                        aria-pressed={r.id === selected}
                        onClick={() => { setSelected(r.id) }}
                      >
                        {r.id === selected ? 'Showing' : 'Show'}
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </Card>
      )}

      {current === undefined ? (
        runs.length === 0 ? <Empty>No run yet in this session. Run one above, or open one by its id.</Empty> : null
      ) : (
        <RunView run={current} operator={operator} onResolved={(item) => { replaceItem(current.id, item) }} />
      )}
    </div>
  )
}

const RunView = ({ run, operator, onResolved }: { run: Run; operator: boolean; onResolved: (item: Item) => void }) => {
  const c = counts(run.items)
  const [resolving, setResolving] = useState<string | null>(null)

  return (
    <>
      <Card title={`Run for ${periodLabel(run.period)}`} aside={<Id value={run.id} />}>
        <dl className="ztax-kv">
          <dt>Run id</dt>
          <dd className="mono">{run.id}</dd>
          <dt>Period</dt>
          <dd>{run.period}</dd>
          <dt>Legal entity</dt>
          <dd className="mono">{run.legalEntityId}</dd>
          <dt>Ran at</dt>
          <dd>{instant(run.ranAt)}</dd>
          {run.ranBy === undefined ? null : (
            <>
              <dt>Ran by</dt>
              <dd className="mono">{run.ranBy}</dd>
            </>
          )}
          <dt>First break (R7)</dt>
          <dd>
            {run.firstBreak === undefined ? (
              <Status value="MATCHED" />
            ) : (
              <>
                <span className="ztax-status ztax-status--bad">{STAGE_LABEL[run.firstBreak]}</span>{' '}
                <span className="muted">the first stage, in order, with an exception</span>
              </>
            )}
          </dd>
          <dt>Items</dt>
          <dd>
            {c.matched} matched · {c.breaks} {c.breaks === 1 ? 'break' : 'breaks'} · {c.pending} pending · {c.resolved}{' '}
            resolved · {run.items.length} in all
          </dd>
        </dl>

        {run.unavailable.length === 0 ? null : (
          <div className="zp-warn-note" role="note">
            <strong>Not compared in this run:</strong> these stages compare records this cell does not hold yet. They are
            unreconciled, not clean.
            <ul className="zp-list">
              {run.unavailable.map((s) => (
                <li key={s}>
                  {STAGE_LABEL[s]} <span className="mono muted">({s})</span>
                </li>
              ))}
            </ul>
          </div>
        )}
      </Card>

      <Card title="Items" aside={<span className="ztax-timestamp">variance = observed − expected, as the server computed it</span>}>
        {run.items.length === 0 ? (
          <Empty>The run recorded no items: nothing in the compared stages fell in this period.</Empty>
        ) : (
          <div className="zp-scroll">
            <table className="table">
              <thead>
                <tr>
                  <th scope="col">Stage</th>
                  <th scope="col">Match key</th>
                  <th scope="col">Expected</th>
                  <th scope="col">Observed</th>
                  <th scope="col">Variance</th>
                  <th scope="col">Status</th>
                  <th scope="col">Root cause</th>
                  <th scope="col">Detail</th>
                  <th scope="col"><span className="zp-sr-only">Resolve</span></th>
                </tr>
              </thead>
              <tbody>
                {run.items.map((item) => (
                  <ItemRows
                    key={item.id}
                    runId={run.id}
                    item={item}
                    operator={operator}
                    open={resolving === item.id}
                    onToggle={() => { setResolving((cur) => (cur === item.id ? null : item.id)) }}
                    onResolved={(updated) => {
                      setResolving(null)
                      onResolved(updated)
                    }}
                  />
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>
    </>
  )
}

const ItemRows = ({ runId, item, operator, open, onToggle, onResolved }: {
  runId: string
  item: Item
  operator: boolean
  open: boolean
  onToggle: () => void
  onResolved: (item: Item) => void
}) => {
  const exception = isOpenException(item)
  const r = item.resolution
  return (
    <>
      <tr>
        <td style={{ whiteSpace: 'nowrap' }}>{STAGE_LABEL[item.stage]}</td>
        <td className="mono" style={{ wordBreak: 'break-all' }}>{item.matchKey}</td>
        <td><Amount amount={item.expected?.amount} currency={item.expected?.currency} /></td>
        <td><Amount amount={item.observed?.amount} currency={item.observed?.currency} /></td>
        <td><Amount amount={item.variance?.amount} currency={item.variance?.currency} /></td>
        <td><Status value={item.status} /></td>
        <td>{item.rootCause ?? r?.rootCause ?? '—'}</td>
        <td>{item.detail ?? '—'}</td>
        <td>
          {exception && operator ? (
            <button type="button" className="ztax-btn-quiet" aria-expanded={open} onClick={onToggle}>
              {open ? 'Close' : 'Resolve'}
            </button>
          ) : null}
        </td>
      </tr>
      {r === undefined ? null : (
        <tr className="zp-subrow">
          <td colSpan={9}>
            <dl className="ztax-kv">
              <dt>Resolution</dt>
              <dd>
                <Status value="RESOLVED" /> {r.action.replaceAll('_', ' ')} · root cause {r.rootCause} · by{' '}
                {r.actor === 'HUMAN' ? 'a person' : 'AI (A3)'}
                {r.resolver === undefined ? null : <span className="mono muted"> {r.resolver}</span>} ·{' '}
                {instant(r.resolvedAt)}
              </dd>
              <dt>Reason</dt>
              <dd>{r.reason}</dd>
              <dt>Evidence</dt>
              <dd>
                {r.evidence.map((ev) => (
                  <span key={ev} className="tag mono">{ev}</span>
                ))}
              </dd>
            </dl>
          </td>
        </tr>
      )}
      {open ? (
        <tr className="zp-subrow">
          <td colSpan={9}>
            <ResolveForm runId={runId} item={item} onResolved={onResolved} />
          </td>
        </tr>
      ) : null}
    </>
  )
}

const ResolveForm = ({ runId, item, onResolved }: { runId: string; item: Item; onResolved: (item: Item) => void }) => {
  const [reason, setReason] = useState('')
  const [action, setAction] = useState<ResolutionAction>('WAIT')
  const [rootCause, setRootCause] = useState<RootCause>(item.rootCause ?? 'TIMING')
  const [evidence, setEvidence] = useState('')
  const [local, setLocal] = useState<string | null>(null)
  const resolve = useAction<Item>()

  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    const refs = lines(evidence)
    if (refs.length === 0) {
      setLocal('Name at least one piece of evidence the resolution relies on.')
      return
    }
    setLocal(null)
    void resolve
      .run(() => ztax.resolveReconciliationItem(runId, item.id, { reason: reason.trim(), action, rootCause, evidence: refs }))
      .then((updated) => {
        if (updated !== null) onResolved(updated)
      })
  }

  return (
    <form className="ztax-form" onSubmit={onSubmit} aria-label={`Resolve ${item.matchKey}`}>
      <Field label="Action">
        <select value={action} onChange={(e) => { setAction(e.target.value as ResolutionAction) }}>
          {ACTIONS.map((a) => <option key={a} value={a}>{a.replaceAll('_', ' ')}</option>)}
        </select>
      </Field>
      <Field label="Root cause">
        <select value={rootCause} onChange={(e) => { setRootCause(e.target.value as RootCause) }}>
          {ROOT_CAUSES.map((rc) => <option key={rc} value={rc}>{rc.replaceAll('_', ' ')}</option>)}
        </select>
      </Field>
      <Field label="Reason">
        <textarea value={reason} required maxLength={1000} rows={2} onChange={(e) => { setReason(e.target.value) }} />
      </Field>
      <Field label="Evidence (one reference per line)" help="A document id, a ticket, a statement line. At least one.">
        <textarea
          className="mono"
          value={evidence}
          required
          rows={2}
          placeholder="bss:invoice-run-2026-10-01"
          onChange={(e) => { setEvidence(e.target.value) }}
        />
      </Field>
      <div className="ztax-form-actions">
        <Submit busy={resolve.busy}>Record resolution</Submit>
      </div>
      {local === null ? null : <p className="ztax-error-note" role="alert">{local}</p>}
      <Outcome error={resolve.error} done={null} />
    </form>
  )
}
