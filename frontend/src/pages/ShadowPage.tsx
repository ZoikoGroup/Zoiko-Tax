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
import { TabBar } from './determine/shared'
import { useRoutedTab } from './routedTab'
import type { PageProps } from './types'
import './obligations.css'

type Tab = 'runs' | 'discrepancies' | 'historical' | 'assurance' | 'reports'

const TABS: readonly { value: Tab; label: string }[] = [
  { value: 'runs', label: 'Comparison runs' },
  { value: 'discrepancies', label: 'Discrepancies' },
  { value: 'historical', label: 'Historical' },
  { value: 'assurance', label: 'Assurance cases' },
  { value: 'reports', label: 'Reports' },
]

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

export const ShadowPage = ({ session, route, onTabChange }: PageProps) => {
  const [tab, setTab] = useRoutedTab(TABS.map((t) => t.value), 'runs', route, onTabChange)
  const comparators = useLoad((signal) => ztax.listComparators(signal), [])
  const runs = useLoad((signal) => ztax.listShadowRuns({ limit: 100 }, signal), [])
  const [selected, setSelected] = useState<string | null>(null)
  const openRun = (id: string) => {
    setSelected(id)
    setTab('runs')
  }
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

      <TabBar label="Shadow" tabs={TABS} current={tab} onSelect={setTab} />
      {tab === 'runs' ? (<>
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
      </>) : null}

      {tab === 'runs' ? null : (
        <Loaded load={runs.load}>
          {(data) => {
            switch (tab) {
              case 'discrepancies':
                return <DiscrepanciesTab runs={data.runs} canMove={canRun} onOpenRun={openRun} onChanged={runs.reload} />
              case 'historical':
                return <HistoricalTab runs={data.runs} onOpenRun={openRun} />
              case 'assurance':
                return <AssuranceTab runs={data.runs} onOpenRun={openRun} />
              case 'reports':
                return <ReportsTab runs={data.runs} />
            }
          }}
        </Loaded>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// Discrepancies across runs
// ---------------------------------------------------------------------------

/** The cell's own rule (domain/shadow State.Open): only RESOLVED and CLOSED are done. */
const isOpen = (s: DState) => s !== 'RESOLVED' && s !== 'CLOSED'

const DiscrepanciesTab = ({ runs, canMove, onOpenRun, onChanged }: {
  runs: readonly Run[]; canMove: boolean; onOpenRun: (id: string) => void; onChanged: () => void
}) => {
  const [all, setAll] = useState(false)
  const rows = runs.flatMap((r) => r.discrepancies.map((d) => ({ run: r, d })))
    .filter(({ d }) => all || isOpen(d.state))
  return (
    <Card
      title={all ? 'All discrepancies' : 'Open discrepancies'}
      aside={
        <label className="ztax-field" style={{ flexDirection: 'row', alignItems: 'center', gap: 6 }}>
          <input type="checkbox" checked={all} onChange={(e) => { setAll(e.target.checked) }} />
          <span>Include resolved and closed</span>
        </label>
      }
    >
      <p className="ztax-note">
        Material differences opened by every run, with each transition recorded against them. A discrepancy is open
        until it is resolved or closed.
      </p>
      {rows.length === 0 ? <Empty>{all ? 'No discrepancy has been opened.' : 'No open discrepancy.'}</Empty> : rows.map(({ run, d }) => (
        <div key={d.id} style={{ borderTop: '1px solid #e5e7eb', padding: '12px 0' }}>
          <p>
            <strong>{run.comparator.name}</strong> · run of {instant(run.createdAt)} · case #{d.caseSeq}{' '}
            <button type="button" className="ztax-table-action-btn" onClick={() => { onOpenRun(run.id) }}>Open run</button>
          </p>
          <details className="ztax-raw">
            <summary>Transitions ({d.history.length})</summary>
            <table className="ztax-action-table">
              <thead><tr><th>#</th><th>State</th><th>Severity</th><th>Root cause</th><th>Note</th><th>By</th><th>When</th></tr></thead>
              <tbody>
                {d.history.map((h) => (
                  <tr key={h.seq}>
                    <td className="mono" style={{ paddingRight: 8 }}>{h.seq}</td>
                    <td style={{ paddingRight: 8 }}><Status value={h.state} /></td>
                    <td style={{ paddingRight: 8 }}>{h.severity ?? '—'}</td>
                    <td style={{ paddingRight: 8 }}>{h.rootCause === undefined ? '—' : words(h.rootCause)}</td>
                    <td style={{ paddingRight: 8 }}>{h.note}</td>
                    <td style={{ paddingRight: 8 }}><Id value={h.recordedBy} /></td>
                    <td>{instant(h.recordedAt)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </details>
          <DiscrepancyRow d={d} caseRef={`case #${d.caseSeq}`} canMove={canMove} onMoved={onChanged} />
        </div>
      ))}
    </Card>
  )
}

// ---------------------------------------------------------------------------
// Historical comparisons
// ---------------------------------------------------------------------------

const HistoricalTab = ({ runs, onOpenRun }: { runs: readonly Run[]; onOpenRun: (id: string) => void }) => {
  const byComparator = new Map<string, Run[]>()
  for (const r of runs) {
    byComparator.set(r.comparator.id, [...(byComparator.get(r.comparator.id) ?? []), r])
  }
  if (byComparator.size === 0) {
    return <Card title="Historical comparisons"><Empty>No runs yet.</Empty></Card>
  }
  return (
    <>
      {[...byComparator.values()].map((list) => {
        const first = list[0]
        if (first === undefined) {
          return null
        }
        // Oldest first, so the history reads forward. Timestamps are canonical
        // RFC 3339 UTC, so their text order is their time order.
        const ordered = [...list].sort((a, b) => a.createdAt.localeCompare(b.createdAt))
        return (
          <Card key={first.comparator.id} title={first.comparator.name} aside={<Claim value={first.comparator.claim} />}>
            <p className="muted">{words(first.comparator.type)} · {first.comparator.provider} · version {first.comparator.version}</p>
            <div style={{ overflowX: 'auto' }}>
              <table className="ztax-action-table">
                <thead>
                  <tr><th>When</th><th>Mode</th><th>Window</th><th>Cases</th><th>Matched</th><th>Differing</th><th>Material</th><th>Abs. variance</th><th>Open disc.</th><th>Action</th></tr>
                </thead>
                <tbody>
                  {ordered.map((r) => (
                    <tr key={r.id}>
                      <td style={{ paddingRight: 8, whiteSpace: 'nowrap' }}>{instant(r.createdAt)}</td>
                      <td style={{ paddingRight: 8 }}>{words(r.mode)}</td>
                      <td style={{ paddingRight: 8, whiteSpace: 'nowrap' }}>{r.windowFrom.slice(0, 10)} – {r.windowTo.slice(0, 10)}</td>
                      <td style={{ paddingRight: 8 }}>{r.metrics.cases}</td>
                      <td style={{ paddingRight: 8 }}>{pct(r.metrics.exact + r.metrics.equivalent, r.metrics.cases)}</td>
                      <td style={{ paddingRight: 8 }}>{r.metrics.differing}</td>
                      <td style={{ paddingRight: 8 }}>{r.metrics.material === 0 ? '0' : <span className="ztax-status ztax-status--bad">{r.metrics.material}</span>}</td>
                      <td style={{ paddingRight: 8 }}><Amount amount={r.metrics.absoluteVariance} currency={r.currency} /></td>
                      <td style={{ paddingRight: 8 }}>{r.discrepancies.filter((d) => isOpen(d.state)).length}</td>
                      <td><button type="button" className="ztax-table-action-btn" onClick={() => { onOpenRun(r.id) }}>Open</button></td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </Card>
        )
      })}
    </>
  )
}

// ---------------------------------------------------------------------------
// Assurance cases
// ---------------------------------------------------------------------------

/**
 * One case per comparator: the evidence its most recent run gives for or
 * against cutover. The blockers are the cell's; this page lists them and
 * decides nothing.
 */
const AssuranceTab = ({ runs, onOpenRun }: { runs: readonly Run[]; onOpenRun: (id: string) => void }) => {
  const latest = new Map<string, Run>()
  for (const r of runs) {
    const seen = latest.get(r.comparator.id)
    if (seen === undefined || r.createdAt.localeCompare(seen.createdAt) > 0) {
      latest.set(r.comparator.id, r)
    }
  }
  if (latest.size === 0) {
    return <Card title="Assurance cases"><Empty>No runs yet, so there is no assurance evidence.</Empty></Card>
  }
  return (
    <>
      <p className="ztax-note">
        Each comparator&apos;s latest run, as evidence for a cutover decision. A case with no blocker is evidence, not
        an approval: cutover remains a governed decision made in Migration.
      </p>
      {[...latest.values()].map((r) => {
        const open = r.discrepancies.filter((d) => isOpen(d.state))
        const sev1 = open.filter((d) => d.severity === 'SHD-SEV-1').length
        return (
          <Card
            key={r.comparator.id}
            title={r.comparator.name}
            aside={r.cutoverBlockers.length === 0 ? <Status value="READY" /> : <span className="ztax-status ztax-status--bad">{r.cutoverBlockers.length} BLOCKER{r.cutoverBlockers.length === 1 ? '' : 'S'}</span>}
          >
            <dl className="ztax-kv">
              <dt>A match claims</dt><dd><Claim value={r.comparator.claim} /></dd>
              <dt>Latest run</dt>
              <dd>{instant(r.createdAt)} · {words(r.mode)} <button type="button" className="ztax-table-action-btn" onClick={() => { onOpenRun(r.id) }}>Open</button></dd>
              <dt>Cases</dt><dd>{r.metrics.cases} · matched {pct(r.metrics.exact + r.metrics.equivalent, r.metrics.cases)} · material {r.metrics.material}</dd>
              <dt>Open discrepancies</dt><dd>{open.length}{sev1 === 0 ? '' : ` (${sev1} SHD-SEV-1)`}</dd>
              <dt>Input hash</dt><dd className="mono" style={{ wordBreak: 'break-all' }}>{r.inputHash}</dd>
            </dl>
            {r.cutoverBlockers.length === 0 ? (
              <p className="ztax-note">No blocker on this run&apos;s evidence.</p>
            ) : (
              <ul>{r.cutoverBlockers.map((b) => <li key={b}>{b}</li>)}</ul>
            )}
          </Card>
        )
      })}
    </>
  )
}

// ---------------------------------------------------------------------------
// Reports
// ---------------------------------------------------------------------------

const csvCell = (s: string) => (/[",\n]/.test(s) ? `"${s.replaceAll('"', '""')}"` : s)

function downloadCsv(name: string, header: readonly string[], rows: readonly (readonly string[])[]) {
  const text = [header, ...rows].map((row) => row.map(csvCell).join(',')).join('\n') + '\n'
  const url = URL.createObjectURL(new Blob([text], { type: 'text/csv;charset=utf-8' }))
  const a = document.createElement('a')
  a.href = url
  a.download = `${name}-${new Date().toISOString().slice(0, 10)}.csv`
  a.click()
  URL.revokeObjectURL(url)
}

const RUN_HEADER = [
  'run_id', 'created_at', 'comparator', 'comparator_type', 'claim', 'mode', 'window_from', 'window_to', 'currency',
  'tolerance', 'materiality', 'cases', 'paired', 'exact', 'equivalent', 'differing', 'category', 'unpaired',
  'uncomparable', 'material', 'compared_value', 'absolute_variance', 'cutover_blockers', 'input_hash',
] as const

const DISC_HEADER = [
  'discrepancy_id', 'run_id', 'comparator', 'case_seq', 'state', 'severity', 'root_cause', 'transitions',
  'last_note', 'last_recorded_at',
] as const

const ReportsTab = ({ runs }: { runs: readonly Run[] }) => {
  // Amounts go out as the canonical strings the cell sent; counts as integers.
  const runRows = runs.map((r) => [
    r.id, r.createdAt, r.comparator.name, r.comparator.type, r.comparator.claim, r.mode, r.windowFrom, r.windowTo,
    r.currency, r.tolerance, r.materiality, String(r.metrics.cases), String(r.metrics.paired), String(r.metrics.exact),
    String(r.metrics.equivalent), String(r.metrics.differing), String(r.metrics.category), String(r.metrics.population),
    String(r.metrics.uncomparable), String(r.metrics.material), r.metrics.comparedValue, r.metrics.absoluteVariance,
    r.cutoverBlockers.join(' | '), r.inputHash,
  ])
  const discRows = runs.flatMap((r) => r.discrepancies.map((d) => {
    const last = d.history[d.history.length - 1]
    return [
      d.id, r.id, r.comparator.name, String(d.caseSeq), d.state, d.severity, d.rootCause, String(d.history.length),
      last?.note ?? '', last?.recordedAt ?? '',
    ]
  }))
  return (
    <Columns>
      <Card title="Comparison runs">
        <p className="ztax-note">
          One row per run: comparator, mode, window, policy, every count and the two value figures exactly as the
          cell reported them, with the run&apos;s cutover blockers.
        </p>
        <p className="muted">{runRows.length} {runRows.length === 1 ? 'run' : 'runs'}</p>
        <button type="button" className="ztax-btn-copper" disabled={runRows.length === 0}
          onClick={() => { downloadCsv('shadow-runs', RUN_HEADER, runRows) }}>Download CSV</button>
      </Card>
      <Card title="Discrepancies">
        <p className="ztax-note">
          One row per discrepancy across every run: state, severity, root cause, how many transitions it has had and
          the latest note.
        </p>
        <p className="muted">{discRows.length} {discRows.length === 1 ? 'discrepancy' : 'discrepancies'}</p>
        <button type="button" className="ztax-btn-copper" disabled={discRows.length === 0}
          onClick={() => { downloadCsv('shadow-discrepancies', DISC_HEADER, discRows) }}>Download CSV</button>
      </Card>
    </Columns>
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
