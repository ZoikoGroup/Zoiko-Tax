import { useState, type FormEvent } from 'react'

import type { S } from '@zoikotax/sdk'

import { civilDate, instant } from '../dashboard/format'
import { ztax } from '../platform/client'
import {
  Amount,
  Card,
  Columns,
  Empty,
  ErrorNote,
  Field,
  Id,
  Loaded,
  PageHeader,
  Raw,
  Status,
  Submit,
  useAction,
  useLoad,
} from '../ui/kit'
import type { PageProps } from './types'
import './obligations.css'

// Migration (ZTAX-MIG-001): history brought in AS_KNOWN from immutable
// snapshots through versioned mappings, reconciled by count, value and
// crosswalk, with governed exceptions and a cutover state machine.

type Program = S<'MigrationProgram'>
type Detail = S<'MigrationProgramDetail'>
type MType = S<'MigrationType'>
type Method = S<'ExtractionMethod'>
type Run = S<'MigrationRun'>
type Exception = S<'MigrationException'>
type Cutover = S<'CutoverState'>
type Decision = S<'MigrationDecision'>
type SourceRecord = S<'MigrationSourceRecord'>

const TYPES: readonly MType[] = [
  'HISTORICAL_DECISION_MIGRATION', 'INCUMBENT_ENGINE_REPLACEMENT', 'GREENFIELD_ONBOARD', 'CONFIGURATION_MIGRATION',
  'FULL_LEDGER_MIGRATION', 'BSS_ERP_TRANSFORMATION', 'COUNTRY_PACK_EXPANSION', 'MERGER_ACQUISITION',
  'DATA_CENTER_REGION_MOVE', 'EXIT_EXPORT',
]
const METHODS: readonly Method[] = ['FILE', 'API', 'DB_EXPORT', 'OBJECT_EXPORT', 'PROVIDER_EXPORT', 'CONTROLLED_MANUAL']
// The moves §26 allows from each state, so the page offers only those.
const NEXT: Record<string, readonly Cutover[]> = {
  PLANNED: ['PRECHECK', 'ABORTED'], PRECHECK: ['FREEZE_ACTIVE', 'ABORTED'], FREEZE_ACTIVE: ['FINAL_DELTA', 'ABORTED'],
  FINAL_DELTA: ['RECONCILING', 'ABORTED'], RECONCILING: ['READY_TO_SWITCH', 'FINAL_DELTA', 'ABORTED'],
  READY_TO_SWITCH: ['ROUTING_SWITCH', 'ABORTED'], ROUTING_SWITCH: ['VALIDATING_LIVE', 'ROLLBACK_ACTIVE'],
  VALIDATING_LIVE: ['CUTOVER_COMPLETE', 'ROLLBACK_ACTIVE'], CUTOVER_COMPLETE: ['ROLLBACK_ACTIVE'],
  ROLLBACK_ACTIVE: ['ROLLED_BACK', 'FAILED_MANUAL_RECOVERY'],
}

const SCALES: readonly number[] = [0, 1, 2, 3, 4, 5, 6]

const pick = <T extends string>(values: readonly T[], v: string, fallback: T): T =>
  (values as readonly string[]).includes(v) ? (v as T) : fallback
const words = (s: string) => s.replaceAll('_', ' ')

export const MigrationPage = ({ session }: PageProps) => {
  const programs = useLoad((signal) => ztax.listMigrationPrograms(signal), [])
  const [selected, setSelected] = useState<string | null>(null)
  const roles = session.user.roles
  const canOperate = roles.includes('ADMIN') || roles.includes('OPERATOR')
  const canDecide = canOperate || roles.includes('ANALYST')

  return (
    <div className="ztax-page">
      <PageHeader
        title="Migration"
        subtitle="History brought in as it was recorded: immutable snapshots, versioned mappings, reconciled runs and a governed cutover."
        actions={<button type="button" className="ztax-btn-quiet" onClick={programs.reload}>Refresh</button>}
      />
      <Card title="Programs">
        <Loaded load={programs.load}>
          {(data) => data.programs.length === 0 ? <Empty>No migration programs yet.</Empty> : (
            <table className="ztax-action-table">
              <thead><tr><th>Name</th><th>Type</th><th>Period</th><th>Created</th><th>Action</th></tr></thead>
              <tbody>
                {data.programs.map((p) => (
                  <tr key={p.id} className={p.id === selected ? 'ztax-row-selected' : undefined}>
                    <td style={{ paddingRight: 8 }}>{p.name}</td>
                    <td style={{ paddingRight: 8 }}>{words(p.type)}</td>
                    <td style={{ paddingRight: 8, whiteSpace: 'nowrap' }}>{civilDate(p.periodFrom)} – {civilDate(p.periodTo)}</td>
                    <td style={{ paddingRight: 8 }}>{instant(p.createdAt)}</td>
                    <td><button type="button" className="ztax-table-action-btn" aria-pressed={p.id === selected} onClick={() => { setSelected(p.id) }}>Open</button></td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </Loaded>
        {canOperate ? <NewProgram onCreated={(p) => { setSelected(p.id); programs.reload() }} /> : null}
      </Card>
      {selected === null ? null : <ProgramDetail key={selected} id={selected} canOperate={canOperate} canDecide={canDecide} />}
    </div>
  )
}

const NewProgram = ({ onCreated }: { onCreated: (p: Program) => void }) => {
  const [name, setName] = useState('')
  const [type, setType] = useState<MType>('HISTORICAL_DECISION_MIGRATION')
  const [from, setFrom] = useState('2025-01-01')
  const [to, setTo] = useState('2026-01-01')
  const [scale, setScale] = useState(2)
  const action = useAction<Program>()
  const submit = (e: FormEvent) => {
    e.preventDefault()
    void action.run(() => ztax.createMigrationProgram({ name: name.trim(), type, periodFrom: from, periodTo: to, scale }))
      .then((p) => { if (p !== null) { setName(''); onCreated(p) } })
  }
  return (
    <>
      <h3 className="ztax-card-title" style={{ marginTop: 16 }}>New program</h3>
      <form className="ztax-form" onSubmit={submit}>
        <Field label="Name"><input value={name} required onChange={(e) => { setName(e.target.value) }} /></Field>
        <Field label="Type">
          <select value={type} onChange={(e) => { setType(pick(TYPES, e.target.value, 'HISTORICAL_DECISION_MIGRATION')) }}>
            {TYPES.map((t) => <option key={t} value={t}>{words(t)}</option>)}
          </select>
        </Field>
        <Field label="Period from"><input type="date" value={from} required onChange={(e) => { setFrom(e.target.value) }} /></Field>
        <Field label="Period to (exclusive)"><input type="date" value={to} required onChange={(e) => { setTo(e.target.value) }} /></Field>
        <Field label="Amount decimal places" help="Declared, not assumed: a finer source amount is an exception, never rounded.">
          <select value={scale} onChange={(e) => { setScale(SCALES[e.target.selectedIndex] ?? 2) }}>
            {SCALES.map((n) => <option key={n} value={n}>{n}</option>)}
          </select>
        </Field>
        <div className="ztax-form-actions"><Submit busy={action.busy}>Create program</Submit></div>
        <div style={{ gridColumn: '1 / -1' }}>{action.error === null ? null : <ErrorNote error={action.error} />}</div>
      </form>
    </>
  )
}

const ProgramDetail = ({ id, canOperate, canDecide }: { id: string; canOperate: boolean; canDecide: boolean }) => {
  const { load, reload } = useLoad((signal) => ztax.getMigrationProgram(id, signal), [id])
  const [runId, setRunId] = useState<string | null>(null)
  return (
    <Loaded load={load}>
      {(d) => (
        <>
          <Columns>
            <Card title={`Program · ${d.program.name}`}>
              <dl className="ztax-kv">
                <dt>Program</dt><dd><Id value={d.program.id} /></dd>
                <dt>Type</dt><dd>{words(d.program.type)}</dd>
                <dt>Legal entity</dt><dd><Id value={d.program.legalEntityId} /></dd>
                <dt>Period</dt><dd>{civilDate(d.program.periodFrom)} – {civilDate(d.program.periodTo)}</dd>
                <dt>Scale</dt><dd>{d.program.scale} decimal places</dd>
                <dt>Conversion</dt><dd>AS KNOWN — amounts carried as the source recorded them, never recalculated.</dd>
              </dl>
              <Raw value={d} />
            </Card>
            <CutoverCard d={d} canOperate={canOperate} onMoved={reload} />
          </Columns>
          <Columns>
            <SnapshotsCard d={d} canOperate={canOperate} onDone={reload} />
            <MappingsCard d={d} canOperate={canOperate} onDone={reload} />
          </Columns>
          <RunsCard d={d} canOperate={canOperate} canDecide={canDecide} onDone={reload} onOpen={setRunId} />
          {runId === null ? null : <RunRecords key={runId} id={runId} />}
        </>
      )}
    </Loaded>
  )
}

const CutoverCard = ({ d, canOperate, onMoved }: { d: Detail; canOperate: boolean; onMoved: () => void }) => {
  const options = NEXT[d.cutoverState] ?? []
  const [to, setTo] = useState<Cutover>(options[0] ?? 'PRECHECK')
  const [note, setNote] = useState('')
  const action = useAction<Detail>()
  const submit = (e: FormEvent) => {
    e.preventDefault()
    void action.run(() => ztax.moveMigrationCutover(d.program.id, { to: options.includes(to) ? to : (options[0] ?? to), note: note.trim() }))
      .then((x) => { if (x !== null) { setNote(''); onMoved() } })
  }
  const r = d.readiness
  return (
    <Card title="Cutover">
      <dl className="ztax-kv">
        <dt>State</dt><dd><Status value={d.cutoverState} /></dd>
        <dt>Latest run</dt><dd>{r.hasRun ? (r.reconciled ? <Status value="READY" /> : <Status value="UNMATCHED" />) : <span className="muted">none</span>}</dd>
        <dt>Exceptions</dt><dd>open high {r.openHigh} · open other {r.openOther} · accepted residuals {r.residuals}</dd>
      </dl>
      {r.blockers.length === 0 ? <p className="ztax-note">No switch blocker. The switch is still made by someone other than the program’s creator.</p> : (
        <ul>{r.blockers.map((b) => <li key={b}>{b}</li>)}</ul>
      )}
      {d.cutover.length === 0 ? null : (
        <table className="ztax-action-table">
          <tbody>
            {[...d.cutover].reverse().map((e) => (
              <tr key={e.seq}>
                <td style={{ paddingRight: 8 }}><Status value={e.state} /></td>
                <td style={{ paddingRight: 8 }}>{e.note}</td>
                <td style={{ paddingRight: 8 }}><Id value={e.recordedBy} /></td>
                <td>{instant(e.recordedAt)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {canOperate && options.length > 0 ? (
        <form className="ztax-form" onSubmit={submit}>
          <Field label="Move to">
            <select value={options.includes(to) ? to : options[0]} onChange={(e) => { setTo(pick(options, e.target.value, options[0] ?? 'PRECHECK')) }}>
              {options.map((o) => <option key={o} value={o}>{words(o)}</option>)}
            </select>
          </Field>
          <Field label="Why (required)"><input value={note} required onChange={(e) => { setNote(e.target.value) }} /></Field>
          <div className="ztax-form-actions"><Submit busy={action.busy}>Record move</Submit></div>
          <div style={{ gridColumn: '1 / -1' }}>{action.error === null ? null : <ErrorNote error={action.error} />}</div>
        </form>
      ) : null}
    </Card>
  )
}

// "sourceId,documentDate,currency,total,CODE:amount;CODE:amount" per line.
function parseRecords(text: string): SourceRecord[] | string {
  const out: SourceRecord[] = []
  const lines = text.split('\n').map((l) => l.trim()).filter((l) => l !== '' && !l.startsWith('#'))
  for (const [i, line] of lines.entries()) {
    const [sourceId = '', documentDate = '', currency = '', total = '', comps = ''] = line.split(',').map((x) => x.trim())
    if (sourceId === '' && documentDate === '') {
      return `Line ${i + 1}: expected "sourceId,date,currency,total,CODE:amount;…".`
    }
    const components = comps.split(';').map((x) => x.trim()).filter((x) => x !== '').map((p) => {
      const [code = '', amount = ''] = p.split(':').map((x) => x.trim())
      return { code, amount }
    })
    // Values travel exactly as typed: a bad one becomes an exception in the
    // run, which is the point of converting rather than validating here.
    out.push({ sourceId, documentDate, currency, total, components })
  }
  return out.length === 0 ? 'Paste at least one source record.' : out
}

const SnapshotsCard = ({ d, canOperate, onDone }: { d: Detail; canOperate: boolean; onDone: () => void }) => {
  const [system, setSystem] = useState('')
  const [method, setMethod] = useState<Method>('FILE')
  const [totals, setTotals] = useState('')
  const [records, setRecords] = useState('')
  const [problem, setProblem] = useState<string | null>(null)
  const action = useAction<S<'MigrationSnapshot'>>()
  const submit = (e: FormEvent) => {
    e.preventDefault()
    setProblem(null)
    const parsed = parseRecords(records)
    if (typeof parsed === 'string') {
      setProblem(parsed)
      return
    }
    const controlTotals = totals.split(/[\n,]/).map((x) => x.trim()).filter((x) => x !== '').map((x) => {
      const [currency = '', total = ''] = x.split('=').map((y) => y.trim())
      return { currency, total }
    })
    void action.run(() => ztax.registerMigrationSnapshot(d.program.id, { sourceSystem: system.trim(), method, controlTotals, records: parsed }))
      .then((s) => { if (s !== null) { setRecords(''); onDone() } })
  }
  return (
    <Card title="Source snapshots">
      {d.snapshots.length === 0 ? <Empty>No snapshot registered.</Empty> : (
        <table className="ztax-action-table">
          <thead><tr><th>Source</th><th>Method</th><th>Records</th><th>Declared totals</th><th>Hash</th></tr></thead>
          <tbody>
            {d.snapshots.map((s) => (
              <tr key={s.id}>
                <td style={{ paddingRight: 8 }}>{s.sourceSystem}</td>
                <td style={{ paddingRight: 8 }}>{words(s.method)}</td>
                <td style={{ paddingRight: 8 }}>{s.recordCount}</td>
                <td style={{ paddingRight: 8 }}>{s.controlTotals.map((t) => `${t.total} ${t.currency}`).join(', ') || '—'}</td>
                <td><Id value={s.hash} /></td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {canOperate ? (
        <form className="ztax-form" onSubmit={submit} style={{ marginTop: 12 }}>
          <Field label="Source system"><input value={system} required onChange={(e) => { setSystem(e.target.value) }} /></Field>
          <Field label="Method">
            <select value={method} onChange={(e) => { setMethod(pick(METHODS, e.target.value, 'FILE')) }}>
              {METHODS.map((m) => <option key={m} value={m}>{words(m)}</option>)}
            </select>
          </Field>
          <Field label="Declared control totals" help="What the source says it holds, e.g. EUR=42.00. Reconciled, not trusted.">
            <input value={totals} onChange={(e) => { setTotals(e.target.value) }} />
          </Field>
          <Field label="Records" help="One per line: sourceId,YYYY-MM-DD,currency,total,CODE:amount;CODE:amount">
            <textarea rows={4} value={records} required onChange={(e) => { setRecords(e.target.value) }} />
          </Field>
          <div className="ztax-form-actions"><Submit busy={action.busy}>Register snapshot</Submit></div>
          <div style={{ gridColumn: '1 / -1' }}>
            {problem === null ? null : <p className="ztax-error-note" role="alert">{problem}</p>}
            {action.error === null ? null : <ErrorNote error={action.error} />}
          </div>
        </form>
      ) : null}
    </Card>
  )
}

const MappingsCard = ({ d, canOperate, onDone }: { d: Detail; canOperate: boolean; onDone: () => void }) => {
  const [codes, setCodes] = useState('')
  const action = useAction<S<'MigrationMapping'>>()
  const submit = (e: FormEvent) => {
    e.preventDefault()
    const parsed = codes.split('\n').map((l) => l.trim()).filter((l) => l !== '').map((l) => {
      const [legacyCode = '', slot = ''] = l.split('=').map((x) => x.trim())
      return { legacyCode, slot }
    })
    void action.run(() => ztax.createMigrationMapping(d.program.id, { codes: parsed })).then((m) => { if (m !== null) { setCodes(''); onDone() } })
  }
  return (
    <Card title="Mapping versions">
      {d.mappings.length === 0 ? <Empty>No mapping yet.</Empty> : (
        <table className="ztax-action-table">
          <thead><tr><th>Version</th><th>Codes</th><th>Recorded</th></tr></thead>
          <tbody>
            {d.mappings.map((m) => (
              <tr key={m.id}>
                <td style={{ paddingRight: 8 }}>v{m.version}</td>
                <td style={{ paddingRight: 8 }} className="mono">{m.codes.map((c) => `${c.legacyCode}→${c.slot}`).join(', ')}</td>
                <td>{instant(m.createdAt)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {canOperate ? (
        <form className="ztax-form" onSubmit={submit} style={{ marginTop: 12 }}>
          <Field label="Codes" help="One per line: LEGACY_CODE=ZOIKO_SLOT. A code left out stays unmapped and becomes an exception.">
            <textarea rows={3} value={codes} required onChange={(e) => { setCodes(e.target.value) }} />
          </Field>
          <div className="ztax-form-actions"><Submit busy={action.busy}>Record next version</Submit></div>
          <div style={{ gridColumn: '1 / -1' }}>{action.error === null ? null : <ErrorNote error={action.error} />}</div>
        </form>
      ) : null}
    </Card>
  )
}

const RunsCard = ({ d, canOperate, canDecide, onDone, onOpen }: {
  d: Detail; canOperate: boolean; canDecide: boolean; onDone: () => void; onOpen: (id: string) => void
}) => {
  const [snapshotId, setSnapshotId] = useState('')
  const [mappingId, setMappingId] = useState('')
  const action = useAction<Run>()
  const submit = (e: FormEvent) => {
    e.preventDefault()
    void action.run(() => ztax.executeMigrationRun(d.program.id, {
      snapshotId: snapshotId || (d.snapshots[0]?.id ?? ''), mappingId: mappingId || (d.mappings[0]?.id ?? ''),
    })).then((r) => { if (r !== null) { onDone(); onOpen(r.id) } })
  }
  return (
    <Card title="Runs">
      {d.runs.length === 0 ? <Empty>No run yet.</Empty> : d.runs.map((r) => (
        <div key={r.id} style={{ borderTop: '1px solid #e5e7eb', padding: '12px 0' }}>
          <p>
            {instant(r.createdAt)} · {r.reconciliation.passed ? <Status value="MATCHED" /> : <Status value="UNMATCHED" />}{' '}
            source {r.reconciliation.sourceCount} · converted {r.reconciliation.convertedCount} · excepted {r.reconciliation.exceptedCount}
            {' '}· crosswalk {r.reconciliation.crosswalkComplete ? 'complete' : 'incomplete'}{' '}
            <button type="button" className="ztax-table-action-btn" onClick={() => { onOpen(r.id) }}>Records</button>
          </p>
          <p className="muted">
            {r.reconciliation.totals.map((t) => `${t.currency}: declared ${t.declared ?? '—'} · source ${t.source} · converted ${t.converted}${t.matches ? '' : ' (does not match)'}`).join(' | ')}
          </p>
          {r.exceptions.length === 0 ? null : (
            <table className="ztax-action-table">
              <thead><tr><th>#</th><th>Kind</th><th>Severity</th><th>Source</th><th>Detail</th><th>Disposition</th></tr></thead>
              <tbody>{r.exceptions.map((x) => <ExceptionRow key={x.id} x={x} canDecide={canDecide} onDone={onDone} />)}</tbody>
            </table>
          )}
        </div>
      ))}
      {canOperate && d.snapshots.length > 0 && d.mappings.length > 0 ? (
        <form className="ztax-form" onSubmit={submit} style={{ marginTop: 12 }}>
          <Field label="Snapshot">
            <select value={snapshotId || (d.snapshots[0]?.id ?? '')} onChange={(e) => { setSnapshotId(e.target.value) }}>
              {d.snapshots.map((s) => <option key={s.id} value={s.id}>{s.sourceSystem} · {s.recordCount} records · {instant(s.createdAt)}</option>)}
            </select>
          </Field>
          <Field label="Mapping">
            <select value={mappingId || (d.mappings[0]?.id ?? '')} onChange={(e) => { setMappingId(e.target.value) }}>
              {d.mappings.map((m) => <option key={m.id} value={m.id}>v{m.version}</option>)}
            </select>
          </Field>
          <div className="ztax-form-actions"><Submit busy={action.busy}>Run conversion</Submit></div>
          <div style={{ gridColumn: '1 / -1' }}>{action.error === null ? null : <ErrorNote error={action.error} />}</div>
        </form>
      ) : null}
    </Card>
  )
}

const ExceptionRow = ({ x, canDecide, onDone }: { x: Exception; canDecide: boolean; onDone: () => void }) => {
  const [note, setNote] = useState('')
  const action = useAction<Exception>()
  const decide = (d: Decision) => {
    void action.run(() => ztax.decideMigrationException(x.id, { disposition: d, note: note.trim() })).then((r) => { if (r !== null) onDone() })
  }
  return (
    <tr>
      <td className="mono" style={{ paddingRight: 8 }}>{x.seq}</td>
      <td style={{ paddingRight: 8 }}>{words(x.kind)}</td>
      <td style={{ paddingRight: 8 }}><span className={`ztax-status ztax-status--${x.severity === 'HIGH' ? 'bad' : 'warn'}`}>{x.severity}</span></td>
      <td className="mono" style={{ paddingRight: 8 }}>{x.sourceId ?? '—'}</td>
      <td style={{ paddingRight: 8 }}>{x.detail}</td>
      <td>
        {x.disposition !== 'OPEN' ? <><Status value={x.disposition} /> <span className="muted">{x.note}</span></> : canDecide ? (
          <span style={{ display: 'flex', gap: 6, alignItems: 'center' }}>
            <input placeholder="Reason" value={note} onChange={(e) => { setNote(e.target.value) }} />
            <button type="button" className="ztax-table-action-btn" disabled={action.busy || note.trim() === ''} onClick={() => { decide('RESOLVED') }}>Resolved</button>
            <button type="button" className="ztax-table-action-btn" disabled={action.busy || note.trim() === ''} onClick={() => { decide('ACCEPTED_RESIDUAL') }}>Accept residual</button>
          </span>
        ) : <Status value="OPEN" />}
        {action.error === null ? null : <ErrorNote error={action.error} />}
      </td>
    </tr>
  )
}

const RunRecords = ({ id }: { id: string }) => {
  const { load } = useLoad((signal) => ztax.getMigrationRun(id, signal), [id])
  return (
    <Card title="Converted records">
      <Loaded load={load}>
        {(r) => (r.records ?? []).length === 0 ? <Empty>This run converted nothing.</Empty> : (
          <table className="ztax-action-table">
            <thead><tr><th>#</th><th>Source id</th><th>Canonical id</th><th>Date</th><th>Total</th><th>Components</th></tr></thead>
            <tbody>
              {(r.records ?? []).map((c) => (
                <tr key={c.seq}>
                  <td className="mono" style={{ paddingRight: 8 }}>{c.seq}</td>
                  <td className="mono" style={{ paddingRight: 8 }}>{c.sourceId}</td>
                  <td style={{ paddingRight: 8 }}><Id value={c.canonicalId} /></td>
                  <td style={{ paddingRight: 8 }}>{civilDate(c.documentDate)}</td>
                  <td style={{ paddingRight: 8 }}><Amount amount={c.total} currency={c.currency} /></td>
                  <td className="mono">{c.components.map((x) => `${x.slot} ${x.amount}`).join(' · ')}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Loaded>
    </Card>
  )
}
