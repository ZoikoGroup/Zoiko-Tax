import { useState, type FormEvent } from 'react'

import type { S } from '@zoikotax/sdk'

import { instant, percent } from '../dashboard/format'
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

// Shadow assurance (ZTAX-SHD-001): another engine's results compared with
// ZoikoTax's committed decisions. Non-impact: nothing here changes a
// decision. Agreement with a comparator is never presented as verified
// correctness unless the comparator is an independent oracle.

type Comparator = S<'Comparator'>
type ComparatorType = S<'ComparatorType'>
type Mode = S<'ShadowMode'>
type Run = S<'ShadowRun'>
type Discrepancy = S<'Discrepancy'>
type DState = S<'DiscrepancyState'>
type Severity = S<'ShadowSeverity'>
type Cause = S<'ShadowRootCause'>
type Observation = S<'ShadowObservation'>

const TYPES: readonly ComparatorType[] = [
  'COMMERCIAL_TAX_ENGINE', 'CUSTOMER_LEGACY_ENGINE', 'HISTORICAL_BILLED_RESULT', 'AUTHORITY_CALCULATOR',
  'QUALIFIED_REFERENCE_IMPLEMENTATION', 'GOLDEN_VECTOR', 'FILING_RESULT', 'MANUAL_TAX_REVIEW', 'PRIOR_ZOIKO_RELEASE',
]
const MODES: readonly Mode[] = ['DUAL_DETERMINATION', 'HISTORICAL_REPLAY', 'LIVE_SHADOW', 'RELEASE_DIFFERENTIAL']
const STATES: readonly DState[] = [
  'TRIAGED', 'DATA_REQUIRED', 'ANALYSIS', 'SME_REVIEW', 'COMPARATOR_QUERY', 'ZOIKO_DEFECT_CONFIRMED',
  'COMPARATOR_ISSUE_CONFIRMED', 'EXPECTED_DIFFERENCE_APPROVED', 'LEGAL_AMBIGUITY', 'FIX_PENDING', 'RETEST_REQUIRED',
  'RESOLVED', 'ACCEPTED_RISK', 'CLOSED',
]
const SEVERITIES: readonly Severity[] = ['SHD-SEV-1', 'SHD-SEV-2', 'SHD-SEV-3', 'SHD-SEV-4']
const CAUSES: readonly Cause[] = [
  'UNKNOWN', 'SOURCE_DATA', 'CATALOG_MAPPING', 'ONTOLOGY', 'JURISDICTION_SITUS', 'TAXABILITY', 'REGULATORY_REVENUE',
  'EXEMPTION', 'RESPONSIBILITY', 'RATE_RULE', 'EFFECTIVE_DATE', 'ROUNDING_PRECISION', 'BUNDLE_ALLOCATION',
  'CUSTOMER_CONFIGURATION', 'COMPARATOR_LIMITATION', 'ZOIKO_DEFECT', 'CONTENT_COVERAGE_GAP', 'LEGAL_AMBIGUITY',
  'TIMING_STATE', 'MIGRATION_TRANSFORM',
]

const pick = <T extends string>(values: readonly T[], v: string, fallback: T): T =>
  (values as readonly string[]).includes(v) ? (v as T) : fallback
const words = (s: string) => s.replaceAll('_', ' ')
const DECIMAL = /^-?(0|[1-9][0-9]*)(\.[0-9]+)?$/

const Claim = ({ value }: { value: string }) => (
  <span className={`ztax-status ztax-status--${value === 'VERIFIED_CORRECT' ? 'ok' : 'neutral'}`}>{words(value)}</span>
)

const pct = (part: number, whole: number) => {
  const p = percent(part, whole)
  return p === null ? '—' : `${p}%`
}

export const ShadowPage = ({ session }: PageProps) => {
  const comparators = useLoad((signal) => ztax.listComparators(signal), [])
  const runs = useLoad((signal) => ztax.listShadowRuns({ limit: 100 }, signal), [])
  const [selected, setSelected] = useState<string | null>(null)
  const roles = session.user.roles
  const canRun = roles.includes('ADMIN') || roles.includes('OPERATOR') || roles.includes('ANALYST')

  return (
    <div className="ztax-page">
      <PageHeader
        title="Shadow Assurance"
        subtitle="Another engine's results compared with ZoikoTax's committed decisions, component by component."
        actions={<button type="button" className="ztax-btn-quiet" onClick={() => { comparators.reload(); runs.reload() }}>Refresh</button>}
      />
      <p className="ztax-note">
        Non-impact: a run reads decisions and changes none. Matching a commercial or legacy engine is reported as
        “matches comparator”, never as verified correctness.
      </p>

      <Card title="Runs">
        <Loaded load={runs.load}>
          {(data) => data.runs.length === 0 ? <Empty>No runs yet. Import a comparator’s results below.</Empty> : (
            <div style={{ overflowX: 'auto' }}>
              <table className="ztax-action-table">
                <thead>
                  <tr><th>When</th><th>Comparator</th><th>Mode</th><th>Cases</th><th>Matched</th><th>Material</th><th>Blockers</th><th>Action</th></tr>
                </thead>
                <tbody>
                  {data.runs.map((r) => (
                    <tr key={r.id} className={r.id === selected ? 'ztax-row-selected' : undefined}>
                      <td style={{ paddingRight: 8, whiteSpace: 'nowrap' }}>{instant(r.createdAt)}</td>
                      <td style={{ paddingRight: 8 }}>{r.comparator.name} <Claim value={r.comparator.claim} /></td>
                      <td style={{ paddingRight: 8 }}>{words(r.mode)}</td>
                      <td style={{ paddingRight: 8 }}>{r.metrics.cases}</td>
                      <td style={{ paddingRight: 8 }}>{pct(r.metrics.exact + r.metrics.equivalent, r.metrics.cases)}</td>
                      <td style={{ paddingRight: 8 }}>{r.metrics.material === 0 ? '0' : <span className="ztax-status ztax-status--bad">{r.metrics.material}</span>}</td>
                      <td style={{ paddingRight: 8 }}>{r.cutoverBlockers.length}</td>
                      <td>
                        <button type="button" className="ztax-table-action-btn" aria-pressed={r.id === selected}
                          onClick={() => { setSelected(r.id) }}>Open</button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </Loaded>
      </Card>

      {selected === null ? null : (
        <RunDetail key={selected} id={selected} canMove={canRun} onClose={() => { setSelected(null) }} onChanged={runs.reload} />
      )}

      {canRun ? (
        <Loaded load={comparators.load}>
          {(c) => <NewRun comparators={c.comparators} onRun={(r) => { setSelected(r.id); runs.reload() }} />}
        </Loaded>
      ) : null}

      <Card title="Comparators">
        <Loaded load={comparators.load}>
          {(c) => (
            <>
              {c.comparators.length === 0 ? <Empty>No comparator profiles yet.</Empty> : (
                <table className="ztax-action-table">
                  <thead><tr><th>Name</th><th>Type</th><th>Provider</th><th>Version</th><th>A match may claim</th></tr></thead>
                  <tbody>
                    {c.comparators.map((x) => (
                      <tr key={x.id}>
                        <td style={{ paddingRight: 8 }}>{x.name}</td>
                        <td style={{ paddingRight: 8 }}>{words(x.type)}</td>
                        <td style={{ paddingRight: 8 }}>{x.provider}</td>
                        <td style={{ paddingRight: 8 }} className="mono">{x.version}</td>
                        <td><Claim value={x.claim} /></td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
              {canRun ? <NewComparator onCreated={comparators.reload} /> : null}
            </>
          )}
        </Loaded>
      </Card>
    </div>
  )
}

const NewComparator = ({ onCreated }: { onCreated: () => void }) => {
  const [name, setName] = useState('')
  const [type, setType] = useState<ComparatorType>('COMMERCIAL_TAX_ENGINE')
  const [provider, setProvider] = useState('')
  const [version, setVersion] = useState('')
  const [limitations, setLimitations] = useState('')
  const action = useAction<Comparator>()
  const submit = (e: FormEvent) => {
    e.preventDefault()
    void action.run(() => ztax.createComparator({
      name: name.trim(), type, provider: provider.trim(),
      ...(version.trim() === '' ? {} : { version: version.trim() }),
      ...(limitations.trim() === '' ? {} : { limitations: limitations.trim() }),
    })).then((c) => {
      if (c !== null) {
        setName('')
        setProvider('')
        setVersion('')
        setLimitations('')
        onCreated()
      }
    })
  }
  return (
    <>
      <h3 className="ztax-card-title" style={{ marginTop: 16 }}>Add a comparator</h3>
      <form className="ztax-form" onSubmit={submit}>
        <Field label="Name"><input value={name} required onChange={(e) => { setName(e.target.value) }} /></Field>
        <Field label="Type" help="Decides what a match may claim.">
          <select value={type} onChange={(e) => { setType(pick(TYPES, e.target.value, 'COMMERCIAL_TAX_ENGINE')) }}>
            {TYPES.map((t) => <option key={t} value={t}>{words(t)}</option>)}
          </select>
        </Field>
        <Field label="Provider"><input value={provider} required onChange={(e) => { setProvider(e.target.value) }} /></Field>
        <Field label="Version" help="Leave empty when the engine does not expose it; it is recorded as UNKNOWN.">
          <input value={version} onChange={(e) => { setVersion(e.target.value) }} />
        </Field>
        <Field label="Known limitations"><input value={limitations} onChange={(e) => { setLimitations(e.target.value) }} /></Field>
        <div className="ztax-form-actions"><Submit busy={action.busy}>Record comparator</Submit></div>
        <div style={{ gridColumn: '1 / -1' }}>{action.error === null ? null : <ErrorNote error={action.error} />}</div>
      </form>
    </>
  )
}

// parseObservations reads "caseRef,total,CODE:amount;CODE:amount" lines.
function parseObservations(text: string): Observation[] | string {
  const out: Observation[] = []
  const lines = text.split('\n').map((l) => l.trim()).filter((l) => l !== '' && !l.startsWith('#'))
  for (const [i, line] of lines.entries()) {
    const [caseRef = '', total = '', comps = ''] = line.split(',').map((x) => x.trim())
    if (caseRef === '' || !DECIMAL.test(total)) {
      return `Line ${i + 1}: expected "businessKey,total,CODE:amount;CODE:amount".`
    }
    const components: Observation['components'] = []
    for (const part of comps.split(';').map((x) => x.trim()).filter((x) => x !== '')) {
      const [code = '', amount = ''] = part.split(':').map((x) => x.trim())
      if (code === '' || !DECIMAL.test(amount)) {
        return `Line ${i + 1}: component "${part}" is not CODE:amount.`
      }
      components.push({ code, amount })
    }
    out.push({ caseRef, total, components })
  }
  return out.length === 0 ? 'Paste at least one comparator result.' : out
}

const NewRun = ({ comparators, onRun }: { comparators: readonly Comparator[]; onRun: (r: Run) => void }) => {
  const [comparatorId, setComparatorId] = useState('')
  const [mode, setMode] = useState<Mode>('DUAL_DETERMINATION')
  const [from, setFrom] = useState('2026-09-01')
  const [to, setTo] = useState('2026-10-01')
  const [currency, setCurrency] = useState('EUR')
  const [mapping, setMapping] = useState('VAT=TAX_VAT\nECO=TAX_ECO_LEVY')
  const [tolerance, setTolerance] = useState('0.00')
  const [materiality, setMateriality] = useState('0.01')
  const [results, setResults] = useState('')
  const [problem, setProblem] = useState<string | null>(null)
  const action = useAction<Run>()
  if (comparators.length === 0) {
    return <Card title="Run a comparison"><Empty>Record a comparator profile first.</Empty></Card>
  }
  const submit = (e: FormEvent) => {
    e.preventDefault()
    setProblem(null)
    const observations = parseObservations(results)
    if (typeof observations === 'string') {
      setProblem(observations)
      return
    }
    const componentMap = mapping.split('\n').map((l) => l.trim()).filter((l) => l !== '').map((l) => {
      const [externalCode = '', slot = ''] = l.split('=').map((x) => x.trim())
      return { externalCode, slot }
    })
    if (componentMap.some((m) => m.externalCode === '' || m.slot === '')) {
      setProblem('Each mapping line is CODE=ZOIKO_SLOT.')
      return
    }
    void action.run(() => ztax.executeShadowRun({
      comparatorId: comparatorId || (comparators[0]?.id ?? ''), mode, currency: currency.trim().toUpperCase(),
      windowFrom: `${from}T00:00:00.000000Z`, windowTo: `${to}T00:00:00.000000Z`, componentMap,
      tolerance: tolerance.trim(), materiality: materiality.trim(), observations,
    })).then((r) => { if (r !== null) onRun(r) })
  }
  return (
    <Card title="Run a comparison">
      <form className="ztax-form" onSubmit={submit}>
        <Field label="Comparator">
          <select value={comparatorId || (comparators[0]?.id ?? '')} onChange={(e) => { setComparatorId(e.target.value) }}>
            {comparators.map((c) => <option key={c.id} value={c.id}>{c.name} · {words(c.type)}</option>)}
          </select>
        </Field>
        <Field label="Mode">
          <select value={mode} onChange={(e) => { setMode(pick(MODES, e.target.value, 'DUAL_DETERMINATION')) }}>
            {MODES.map((m) => <option key={m} value={m}>{words(m)}</option>)}
          </select>
        </Field>
        <Field label="Window from (UTC)" help="Decisions in the window with no comparator result are reported unpaired.">
          <input type="date" value={from} required onChange={(e) => { setFrom(e.target.value) }} />
        </Field>
        <Field label="Window to (UTC)"><input type="date" value={to} required onChange={(e) => { setTo(e.target.value) }} /></Field>
        <Field label="Currency"><input value={currency} maxLength={3} required onChange={(e) => { setCurrency(e.target.value) }} /></Field>
        <Field label="Tolerance" help="Classifies a difference as equivalent; never changes an amount.">
          <input value={tolerance} required onChange={(e) => { setTolerance(e.target.value) }} />
        </Field>
        <Field label="Materiality" help="A difference at or above this opens a discrepancy.">
          <input value={materiality} required onChange={(e) => { setMateriality(e.target.value) }} />
        </Field>
        <Field label="Component mapping" help="One per line: COMPARATOR_CODE=ZOIKO_SLOT. Unmapped codes stay unmapped.">
          <textarea rows={3} value={mapping} onChange={(e) => { setMapping(e.target.value) }} />
        </Field>
        <Field label="Comparator results" help='One per line: businessKey,total,CODE:amount;CODE:amount — e.g. INV-0001/1,63.12,VAT:63.00;ECO:0.12'>
          <textarea rows={5} value={results} required onChange={(e) => { setResults(e.target.value) }} />
        </Field>
        <div className="ztax-form-actions"><Submit busy={action.busy}>Run comparison</Submit></div>
        <div style={{ gridColumn: '1 / -1' }}>
          {problem === null ? null : <p className="ztax-error-note" role="alert">{problem}</p>}
          {action.error === null ? null : <ErrorNote error={action.error} />}
        </div>
      </form>
    </Card>
  )
}

const RunDetail = ({ id, canMove, onClose, onChanged }: { id: string; canMove: boolean; onClose: () => void; onChanged: () => void }) => {
  const { load, reload } = useLoad((signal) => ztax.getShadowRun(id, signal), [id])
  return (
    <Loaded load={load}>
      {(r) => {
        const m = r.metrics
        return (
          <>
            <Columns>
              <Card title={`Run · ${r.comparator.name}`} aside={<button type="button" className="ztax-btn-quiet" onClick={onClose}>Close</button>}>
                <dl className="ztax-kv">
                  <dt>Run</dt><dd><Id value={r.id} /></dd>
                  <dt>A match claims</dt><dd><Claim value={r.comparator.claim} /> <span className="muted">comparator version {r.comparator.version}</span></dd>
                  <dt>Mode</dt><dd>{words(r.mode)}</dd>
                  <dt>Window</dt><dd>{instant(r.windowFrom)} – {instant(r.windowTo)}</dd>
                  <dt>Policy</dt><dd>tolerance {r.tolerance} · materiality {r.materiality} {r.currency}</dd>
                  <dt>Mapping</dt><dd className="mono">{r.componentMap.map((x) => `${x.externalCode}→${x.slot}`).join(', ')}</dd>
                  <dt>Input hash</dt><dd className="mono">{r.inputHash}</dd>
                  <dt>Cases</dt><dd>{m.cases} · paired {m.paired} ({pct(m.paired, m.cases)})</dd>
                  <dt>By count</dt>
                  <dd>exact {m.exact} · equivalent {m.equivalent} · differing {m.differing} · category {m.category} · unpaired {m.population} · uncomparable {m.uncomparable}</dd>
                  <dt>By value</dt><dd>compared <Amount amount={m.comparedValue} currency={r.currency} /> · absolute variance <Amount amount={m.absoluteVariance} currency={r.currency} /></dd>
                </dl>
                <Raw value={r} />
              </Card>
              <Card title="Cutover evidence">
                {r.cutoverBlockers.length === 0 ? <p className="ztax-note">No blocker on this run’s evidence. Cutover is still a governed decision made elsewhere.</p> : (
                  <ul>{r.cutoverBlockers.map((b) => <li key={b}>{b}</li>)}</ul>
                )}
              </Card>
            </Columns>
            <Card title="Cases">
              <div style={{ overflowX: 'auto' }}>
                <table className="ztax-action-table">
                  <thead><tr><th>#</th><th>Business key</th><th>Pairing</th><th>State</th><th>Comparator</th><th>ZoikoTax</th><th>Variance</th><th>Components</th></tr></thead>
                  <tbody>
                    {(r.cases ?? []).map((c) => (
                      <tr key={c.seq}>
                        <td className="mono" style={{ paddingRight: 8 }}>{c.seq}</td>
                        <td className="mono" style={{ paddingRight: 8 }}>{c.caseRef}</td>
                        <td style={{ paddingRight: 8 }}>{words(c.pairing)}</td>
                        <td style={{ paddingRight: 8 }}>
                          <span className={`ztax-status ztax-status--${c.state === 'EXACT_MATCH' || c.state === 'EQUIVALENT_MATCH' ? 'ok' : c.material ? 'bad' : 'warn'}`}>{words(c.state)}</span>
                        </td>
                        <td style={{ paddingRight: 8 }}><Amount amount={c.comparatorTotal} currency={r.currency} /></td>
                        <td style={{ paddingRight: 8 }}><Amount amount={c.zoikoTotal} currency={r.currency} /></td>
                        <td style={{ paddingRight: 8 }}><Amount amount={c.variance} currency={r.currency} /></td>
                        <td className="mono">
                          {c.components.map((x) => `${x.externalCode ?? '—'}→${x.slot}: ${x.comparator ?? '—'} / ${x.zoiko ?? '—'} ${words(x.state)}`).join(' · ')}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </Card>
            <Card title="Discrepancies">
              {r.discrepancies.length === 0 ? <Empty>No material discrepancy.</Empty> : r.discrepancies.map((d) => (
                <DiscrepancyRow key={d.id} d={d} caseRef={(r.cases ?? []).find((c) => c.seq === d.caseSeq)?.caseRef ?? ''} canMove={canMove}
                  onMoved={() => { reload(); onChanged() }} />
              ))}
            </Card>
          </>
        )
      }}
    </Loaded>
  )
}

const DiscrepancyRow = ({ d, caseRef, canMove, onMoved }: { d: Discrepancy; caseRef: string; canMove: boolean; onMoved: () => void }) => {
  const [to, setTo] = useState<DState>(d.state === 'NEW' ? 'TRIAGED' : 'ANALYSIS')
  const [severity, setSeverity] = useState<Severity>(d.severity)
  const [cause, setCause] = useState<Cause>(d.rootCause)
  const [note, setNote] = useState('')
  const action = useAction<Discrepancy>()
  const submit = (e: FormEvent) => {
    e.preventDefault()
    void action.run(() => ztax.moveDiscrepancy(d.id, { to, severity, rootCause: cause, note: note.trim() })).then((x) => {
      if (x !== null) {
        setNote('')
        onMoved()
      }
    })
  }
  return (
    <div style={{ borderTop: '1px solid #e5e7eb', padding: '12px 0' }}>
      <p>
        <span className="mono">{caseRef}</span> · <Status value={d.state} />{' '}
        <span className={`ztax-status ztax-status--${d.severity === 'SHD-SEV-1' ? 'bad' : 'warn'}`}>{d.severity}</span>{' '}
        <span className="muted">root cause {words(d.rootCause)} · last: {d.history[d.history.length - 1]?.note}</span>
      </p>
      {canMove && d.state !== 'CLOSED' ? (
        <form className="ztax-form" onSubmit={submit}>
          <Field label="Move to">
            <select value={to} onChange={(e) => { setTo(pick(STATES, e.target.value, 'TRIAGED')) }}>
              {STATES.map((s) => <option key={s} value={s}>{words(s)}</option>)}
            </select>
          </Field>
          <Field label="Severity">
            <select value={severity} onChange={(e) => { setSeverity(pick(SEVERITIES, e.target.value, 'SHD-SEV-2')) }}>
              {SEVERITIES.map((s) => <option key={s} value={s}>{s}</option>)}
            </select>
          </Field>
          <Field label="Root cause">
            <select value={cause} onChange={(e) => { setCause(pick(CAUSES, e.target.value, 'UNKNOWN')) }}>
              {CAUSES.map((c) => <option key={c} value={c}>{words(c)}</option>)}
            </select>
          </Field>
          <Field label="Why (required)"><input value={note} required maxLength={2000} onChange={(e) => { setNote(e.target.value) }} /></Field>
          <div className="ztax-form-actions"><Submit busy={action.busy}>Record</Submit></div>
          <div style={{ gridColumn: '1 / -1' }}>{action.error === null ? null : <ErrorNote error={action.error} />}</div>
        </form>
      ) : null}
    </div>
  )
}
