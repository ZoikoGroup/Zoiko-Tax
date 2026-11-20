import { useState, type ReactNode } from 'react'

import type { S } from '@zoikotax/sdk'

import { instant, periodLabel } from '../../dashboard/format'
import { ztax } from '../../platform/client'
import { Amount, Card, Columns, Empty, ErrorNote, errorText, Field, Id, Loaded, Raw, Status, Submit, useAction, useLoad } from '../../ui/kit'
import { mediaTypeOf, sha256Hex } from '../notices/model'
import { hasRole, words } from '../workspace'
import {
  ACTION_LABEL,
  ACTION_ROLES,
  actionBody,
  BLOCKER_LABEL,
  EMPTY_FORM,
  isRoundingMode,
  reasonOf,
  refusalHint,
  ROUNDING_MODES,
  type Action,
  type ActionForm,
  type Evidence,
  type Workpaper,
} from './model'

// One workpaper: the header, the latest build's schedules with lineage to
// every source event, the reconciliation, the exceptions and their
// resolutions, and only the acts its state admits. The cell applies every
// guard; a refusal is shown with its reason code and what it means.

export const WorkpaperDetail = ({ id, roles, onChanged, onClose }: {
  id: string
  roles: readonly string[]
  onChanged: () => void
  onClose: () => void
}) => {
  const { load, reload } = useLoad((signal) => ztax.getWorkpaper(id, signal), [id])
  const changed = () => {
    reload()
    onChanged()
  }
  return (
    <Loaded load={load}>
      {(w) => (
        <>
          <Header w={w} roles={roles} onChanged={changed} onClose={onClose} />
          <Exceptions w={w} />
          <Schedules w={w} />
          <Reconciliation w={w} />
          <Columns>
            <Acts w={w} roles={roles} onDone={changed} onStale={reload} />
            <Inputs w={w} />
          </Columns>
          <History w={w} />
        </>
      )}
    </Loaded>
  )
}

const Refusal = ({ error }: { error: unknown }) => {
  if (error === null) return null
  const hint = refusalHint(reasonOf(error))
  return hint === null ? <ErrorNote error={error} /> : (
    <p className="ztax-error-note" role="alert">{hint} <span className="mono muted">{errorText(error)}</span></p>
  )
}

// ---------------------------------------------------------------------------
// header, build and source check
// ---------------------------------------------------------------------------

const Header = ({ w, roles, onChanged, onClose }: { w: Workpaper; roles: readonly string[]; onChanged: () => void; onClose: () => void }) => {
  const build = useAction<Workpaper>()
  const check = useAction<S<'WorkpaperSourceCheck'>>()
  const [unchanged, setUnchanged] = useState(false)
  const latest = w.latestBuild
  const canBuild = w.allowedActions.includes('BUILD') && hasRole(roles, ...ACTION_ROLES.BUILD)
  const runBuild = () => {
    const before = w.builds.length
    setUnchanged(false)
    void build.run(() => ztax.buildWorkpaper(w.id, { expectedSeq: w.eventCount })).then((next) => {
      if (next !== null) {
        setUnchanged(next.builds.length === before)
        onChanged()
      }
    })
  }
  return (
    <Columns>
      <Card title={`${words(w.duty)} · ${periodLabel(w.period)}`}
        aside={<button type="button" className="ztax-btn-quiet" onClick={onClose}>Close</button>}>
        <dl className="ztax-kv">
          <dt>Status</dt><dd><Status value={w.status} /></dd>
          <dt>Workpaper</dt><dd><Id value={w.id} /></dd>
          <dt>Legal filer</dt><dd><Id value={w.legalEntityId} /></dd>
          <dt>Authority</dt><dd>{w.authority}</dd>
          <dt>Reporting currency</dt><dd className="mono">{w.reportingCurrency}</dd>
          <dt>Materiality threshold</dt><dd><Amount amount={w.materialityThreshold} currency={w.reportingCurrency} /></dd>
          <dt>Content release</dt>
          <dd className="mono">{w.contentRelease}{w.contentRelease === w.initialContentRelease ? null : <span className="muted"> (opened under {w.initialContentRelease})</span>}</dd>
          <dt>Formula</dt><dd className="mono">{w.formulaVersion}</dd>
          <dt>Obligation</dt><dd><Id value={w.obligationId} /></dd>
          <dt>Frozen build</dt><dd>{w.frozenBuildVersion === undefined ? '—' : `v${w.frozenBuildVersion} — source snapshot locked`}</dd>
          <dt>Opened</dt><dd>{instant(w.createdAt)} by <Id value={w.createdBy} /></dd>
        </dl>
        {w.blockers.length === 0 ? null : (
          <div className="ztax-note" role="note" style={{ marginTop: 12 }}>
            <strong>Not ready for review:</strong>
            <ul>{w.blockers.map((b) => <li key={b}>{BLOCKER_LABEL[b]}</li>)}</ul>
          </div>
        )}
      </Card>
      <Card title="Build and source snapshot">
        {latest === undefined ? <Empty>Not built yet.</Empty> : (
          <dl className="ztax-kv">
            <dt>Latest build</dt><dd>v{latest.version} · {instant(latest.builtAt)} by <Id value={latest.builtBy} /></dd>
            <dt>Inputs hash</dt><dd><Id value={latest.inputsHash} /></dd>
            <dt>Output hash</dt><dd><Id value={latest.outputHash} /></dd>
            <dt>Basis digest</dt><dd><Id value={latest.basisDigest} /></dd>
            <dt>Formula</dt><dd className="mono">{latest.formulaVersion}</dd>
          </dl>
        )}
        <div className="ztax-form-actions" style={{ marginTop: 12, gap: 8, display: 'flex', flexWrap: 'wrap' }}>
          {canBuild ? (
            <button type="button" className="ztax-btn-copper" disabled={build.busy} onClick={runBuild}>
              {ACTION_LABEL.BUILD}
            </button>
          ) : null}
          <button type="button" className="ztax-btn-quiet" disabled={check.busy}
            onClick={() => { void check.run(() => ztax.checkWorkpaperSources(w.id)) }}>
            Compare with sources now
          </button>
        </div>
        <p className="ztax-note" style={{ marginTop: 8 }}>
          A build reads the contribution ledger and the filer&rsquo;s subledger for the period. The same inputs record nothing;
          different inputs are a new version. A frozen workpaper is not rebuilt.
        </p>
        {unchanged ? <p className="ztax-note" role="status">Nothing changed since the latest build; no new version was recorded.</p> : null}
        <Refusal error={build.error} />
        {check.error === null ? null : <ErrorNote error={check.error} />}
        {check.done === null ? null : (
          <p className="ztax-note" role="status">
            {check.done.latestBuildVersion === undefined ? 'Not built yet.'
              : check.done.matches ? `The sources still build exactly what v${check.done.latestBuildVersion} read.`
                : `The sources moved since v${check.done.latestBuildVersion}: a rebuild would record a new version${w.frozenBuildVersion === undefined ? '' : '; the frozen snapshot stands'}.`}
            {' '}<span className="mono muted">now {check.done.currentInputsHash}</span>
          </p>
        )}
      </Card>
    </Columns>
  )
}

// ---------------------------------------------------------------------------
// exceptions
// ---------------------------------------------------------------------------

const Exceptions = ({ w }: { w: Workpaper }) => (
  <Card title="Exceptions">
    {w.latestBuild === undefined ? <Empty>Build the workpaper to see its exceptions.</Empty>
      : w.exceptions.length === 0 ? <Empty>None. Every schedule ties and every source is present.</Empty> : (
        <div style={{ overflowX: 'auto' }}>
          <table className="ztax-action-table">
            <thead><tr><th>Exception</th><th>Detail</th><th>Difference</th><th>State</th><th>Resolution</th></tr></thead>
            <tbody>
              {w.exceptions.map((x) => (
                <tr key={x.key}>
                  <td className="mono">{x.key}</td>
                  <td>{x.detail}{x.resolvable ? null : <div className="muted">Repaired at the source or by documenting a rate, then rebuilt.</div>}</td>
                  <td style={{ whiteSpace: 'nowrap' }}>{x.difference === undefined ? '—' : <Amount amount={x.difference} currency={x.currency} />}</td>
                  <td><Status value={x.state === 'OPEN' ? 'DATA_REQUIRED' : x.state} /></td>
                  <td>
                    {x.resolution === undefined ? '—' : (
                      <>
                        {x.resolution.explanation}
                        <div className="muted mono">{x.resolution.evidence.filename} · {x.resolution.evidence.sha256}</div>
                        <div className="muted">
                          by <Id value={x.resolution.proposedBy} />
                          {x.resolution.decidedBy === undefined ? ' · awaiting a second person' : <> · {words(x.resolution.state)} by <Id value={x.resolution.decidedBy} /></>}
                        </div>
                      </>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
  </Card>
)

// ---------------------------------------------------------------------------
// schedules
// ---------------------------------------------------------------------------

const Lineage = ({ sources }: { sources: readonly S<'WorkpaperSourceRef'>[] }) => (
  <details>
    <summary className="muted">{sources.length} source event{sources.length === 1 ? '' : 's'}</summary>
    <ul className="mono" style={{ margin: 0, paddingLeft: 16 }}>
      {sources.map((s) => (
        <li key={`${s.contributionId}`}>
          {s.sourceEventId} v{s.sourceVersion} · {s.sourceRef} · {s.amount} · rule {s.ruleVersion}
          {s.reverses === undefined ? null : <span className="muted"> · reverses {s.reverses}</span>}
          <span className="muted"> · contribution <Id value={s.contributionId} /></span>
        </li>
      ))}
    </ul>
  </details>
)

const Schedules = ({ w }: { w: Workpaper }) => {
  const o = w.latestBuild?.output
  if (o === undefined) return null
  const rep = o.reportingCurrency
  return (
    <Card title={`Schedules · build v${w.latestBuild?.version ?? ''}`}>
      <h3 className="ztax-card-title">Revenue categories</h3>
      {o.revenue.length === 0 ? <Empty>No accepted source event.</Empty> : (
        <table className="ztax-action-table">
          <thead><tr><th>Category</th><th>Events</th><th>Amount</th><th>In {rep}</th><th>Lineage</th></tr></thead>
          <tbody>
            {o.revenue.map((l) => (
              <tr key={`${l.category}/${l.currency}`}>
                <td className="mono">{l.category}</td><td>{l.count}</td>
                <td style={{ whiteSpace: 'nowrap' }}><Amount amount={l.amount} currency={l.currency} /></td>
                <td style={{ whiteSpace: 'nowrap' }}>{l.currency === rep ? '—' : <Amount amount={l.converted} currency={rep} />}</td>
                <td><Lineage sources={l.sources} /></td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      <h3 className="ztax-card-title" style={{ marginTop: 16 }}>Excluded bases</h3>
      {o.excluded.length === 0 ? <Empty>Nothing excluded.</Empty> : (
        <table className="ztax-action-table">
          <thead><tr><th>Reason</th><th>Events</th><th>Amount</th><th>In {rep}</th><th>Lineage</th></tr></thead>
          <tbody>
            {o.excluded.map((l) => (
              <tr key={`${l.reason}/${l.currency}`}>
                <td>{l.reason}</td><td>{l.count}</td>
                <td style={{ whiteSpace: 'nowrap' }}><Amount amount={l.amount} currency={l.currency} /></td>
                <td style={{ whiteSpace: 'nowrap' }}>{l.currency === rep ? '—' : <Amount amount={l.converted} currency={rep} />}</td>
                <td><Lineage sources={l.sources} /></td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      <h3 className="ztax-card-title" style={{ marginTop: 16 }}>Volume and transaction counts</h3>
      <dl className="ztax-kv">
        <dt>Accepted events</dt><dd>{o.volumes.accepted}</dd>
        <dt>Quarantined (not summed)</dt><dd>{o.volumes.quarantined}</dd>
        <dt>Reversals</dt><dd>{o.volumes.reversals}</dd>
        <dt>Late (amendments)</dt><dd>{o.volumes.amendments}</dd>
        {o.volumes.quantities.map((q) => (
          <span key={q.unit} style={{ display: 'contents' }}><dt>Quantity ({q.unit})</dt><dd className="mono">{q.total}</dd></span>
        ))}
      </dl>
      <h3 className="ztax-card-title" style={{ marginTop: 16 }}>Permitted allowances and audited adjustments</h3>
      {o.allowances.length + o.adjustments.length === 0 ? <Empty>None in this build.</Empty> : (
        <table className="ztax-action-table">
          <thead><tr><th>#</th><th>Kind</th><th>Category</th><th>Amount</th><th>Reason</th><th>Evidence</th></tr></thead>
          <tbody>
            {[...o.allowances.map((a) => ({ a, kind: 'Allowance (deducted)' })), ...o.adjustments.map((a) => ({ a, kind: 'Adjustment' }))].map(({ a, kind }) => (
              <tr key={a.number}>
                <td>{a.number}</td><td>{kind}{a.material ? <span className="muted"> · material</span> : null}</td><td>{a.category}</td>
                <td style={{ whiteSpace: 'nowrap' }}><Amount amount={a.amount} currency={rep} /></td><td>{a.reason}</td>
                <td><Id value={a.evidenceSha256} /></td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      <h3 className="ztax-card-title" style={{ marginTop: 16 }}>Report totals</h3>
      {o.totals === undefined ? (
        <p className="ztax-note">No totals: a source currency has no documented FX rate and conversion basis. Unconverted amounts are never added to converted ones.</p>
      ) : (
        <dl className="ztax-kv">
          <dt>Gross eligible</dt><dd><Amount amount={o.totals.grossEligible} currency={o.totals.currency} /></dd>
          <dt>Excluded</dt><dd><Amount amount={o.totals.excluded} currency={o.totals.currency} /></dd>
          <dt>Allowances</dt><dd><Amount amount={o.totals.allowances} currency={o.totals.currency} /></dd>
          <dt>Adjustments</dt><dd><Amount amount={o.totals.adjustments} currency={o.totals.currency} /></dd>
          <dt>Reportable base</dt><dd><strong><Amount amount={o.totals.reportableBase} currency={o.totals.currency} /></strong></dd>
        </dl>
      )}
    </Card>
  )
}

// ---------------------------------------------------------------------------
// reconciliation
// ---------------------------------------------------------------------------

const Reconciliation = ({ w }: { w: Workpaper }) => {
  const o = w.latestBuild?.output
  if (o === undefined) return null
  return (
    <Card title="Reconciliation">
      <table className="ztax-action-table">
        <thead><tr><th>Control</th><th>Expected</th><th>Actual</th><th>Difference</th><th>Status</th></tr></thead>
        <tbody>
          {o.reconciliation.map((r) => (
            <tr key={`${r.code}/${r.currency}`}>
              <td>{words(r.code)}</td>
              <td style={{ whiteSpace: 'nowrap' }}><Amount amount={r.expected} currency={r.currency} /></td>
              <td style={{ whiteSpace: 'nowrap' }}><Amount amount={r.actual} currency={r.currency} /></td>
              <td style={{ whiteSpace: 'nowrap' }}><Amount amount={r.difference} currency={r.currency} /></td>
              <td><Status value={r.status === 'BRIDGED' ? 'EXPLAINED' : r.status === 'DIFFERENCE' ? 'UNMATCHED' : r.status} /></td>
            </tr>
          ))}
        </tbody>
      </table>
      <h3 className="ztax-card-title" style={{ marginTop: 16 }}>Contribution ledger control totals (BSS)</h3>
      {o.controlTotals.length === 0 ? <Empty>No source event for this filer, duty and period.</Empty> : (
        <table className="ztax-action-table">
          <thead><tr><th>Currency</th><th>Accepted</th><th>Quarantined</th><th>Eligible</th><th>Excluded</th><th>Period digest</th><th>Incomplete</th></tr></thead>
          <tbody>
            {o.controlTotals.map((c) => (
              <tr key={c.currency}>
                <td className="mono">{c.currency}</td><td>{c.accepted}</td><td>{c.quarantined}</td>
                <td style={{ whiteSpace: 'nowrap' }}><Amount amount={c.eligible} currency={c.currency} /></td>
                <td style={{ whiteSpace: 'nowrap' }}><Amount amount={c.excluded} currency={c.currency} /></td>
                <td><Id value={c.periodDigest} /></td>
                <td>{c.incomplete.length === 0 ? '—' : c.incomplete.map(words).join(', ')}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {o.volumes.bySource.length === 0 ? null : (
        <table className="ztax-action-table" style={{ marginTop: 12 }}>
          <thead><tr><th>Source type</th><th>Events</th><th>Eligible</th></tr></thead>
          <tbody>
            {o.volumes.bySource.map((s) => (
              <tr key={`${s.sourceType}/${s.currency}`}>
                <td>{words(s.sourceType)}</td><td>{s.count}</td>
                <td style={{ whiteSpace: 'nowrap' }}><Amount amount={s.eligible} currency={s.currency} /></td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      <h3 className="ztax-card-title" style={{ marginTop: 16 }}>Tax Control Subledger and GL bridge</h3>
      <p className="ztax-note">
        The filer&rsquo;s subledger balances for the period, shown beside the schedules. Retail tax collected is not a provider
        contribution and is never netted into it; only suspense and the GL bridge are required to clear.
      </p>
      {o.subledger.length === 0 ? <Empty>No subledger posting for the filer in this period.</Empty> : (
        <table className="ztax-action-table">
          <thead><tr><th>Account</th><th>Debits</th><th>Credits</th></tr></thead>
          <tbody>
            {o.subledger.map((b) => (
              <tr key={`${b.account}/${b.currency}`}>
                <td className="mono">{b.account}</td>
                <td style={{ whiteSpace: 'nowrap' }}><Amount amount={b.debits} currency={b.currency} /></td>
                <td style={{ whiteSpace: 'nowrap' }}><Amount amount={b.credits} currency={b.currency} /></td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {o.conversions.length === 0 ? null : (
        <>
          <h3 className="ztax-card-title" style={{ marginTop: 16 }}>Conversions applied</h3>
          <ul>
            {o.conversions.map((c) => (
              <li key={c.from} className="mono">
                1 {c.from} = {c.rate} {c.to} · {c.rateSource} {c.rateDate} · {words(c.roundingMode)} at {c.scale} · {c.basis}
              </li>
            ))}
          </ul>
        </>
      )}
    </Card>
  )
}

// ---------------------------------------------------------------------------
// acts
// ---------------------------------------------------------------------------

const Acts = ({ w, roles, onDone, onStale }: { w: Workpaper; roles: readonly string[]; onDone: () => void; onStale: () => void }) => {
  const acts = w.allowedActions.filter((a): a is Exclude<Action, 'BUILD'> => a !== 'BUILD')
  const [chosen, setChosen] = useState<Action | ''>('')
  const [form, setForm] = useState<ActionForm>(EMPTY_FORM)
  const [evidence, setEvidence] = useState<Evidence | null>(null)
  const [invalid, setInvalid] = useState<string | null>(null)
  const action = useAction<Workpaper>()
  const set = <K extends keyof ActionForm>(k: K) => (v: ActionForm[K]) => { setForm((f) => ({ ...f, [k]: v })) }

  if (acts.length === 0) {
    return <Card title="Act"><Empty>Nothing to do in this state{w.status === 'APPROVED' ? ' except a rebuild, which would revoke the approval if the inputs changed' : ''}.</Empty></Card>
  }
  const current = chosen !== '' && acts.includes(chosen as Exclude<Action, 'BUILD'>) ? chosen : ''

  const submit = () => {
    if (current === '') return
    setInvalid(null)
    const built = actionBody(w, current, form, evidence)
    if ('invalid' in built) {
      setInvalid(built.invalid)
      return
    }
    void action.run(() => ztax.actOnWorkpaper(w.id, built.body)).then((next) => {
      if (next !== null) {
        setForm(EMPTY_FORM)
        setEvidence(null)
        setChosen('')
        onDone()
      } else if (reasonOf(action.error) === 'WORKPAPER_STALE') {
        onStale()
      }
    })
  }

  const text = (k: 'note' | 'contentRelease' | 'fxCurrency' | 'fxRate' | 'fxSource' | 'fxScale' | 'adjCategory' | 'adjAmount', label: string, help?: string) => (
    <Field label={label} {...(help === undefined ? {} : { help })}>
      <input value={form[k]} onChange={(e) => { set(k)(e.target.value) }} />
    </Field>
  )
  const area = (k: 'note' | 'fxBasis' | 'adjReason', label: string) => (
    <Field label={label}>
      <textarea rows={2} maxLength={2000} value={form[k]} onChange={(e) => { set(k)(e.target.value) }} />
    </Field>
  )
  const decision = (
    <Field label="Decision">
      <select value={form.decision} onChange={(e) => { set('decision')(e.target.value === 'REJECTED' ? 'REJECTED' : 'APPROVED') }}>
        <option value="APPROVED">Approve</option>
        <option value="REJECTED">Reject</option>
      </select>
    </Field>
  )
  const file = (
    <Field label="Evidence" help="Hashed in this browser. Only the SHA-256, name and type are sent; nothing is uploaded.">
      <input type="file" onChange={(e) => {
        const f = e.target.files?.[0]
        if (f === undefined) {
          setEvidence(null)
          return
        }
        void sha256Hex(f).then((sha256) => { setEvidence({ sha256, filename: f.name.slice(0, 255), mediaType: mediaTypeOf(f) }) })
      }} />
    </Field>
  )

  let fields: ReactNode = null
  switch (current) {
    case 'FREEZE':
      fields = <>{area('note', 'Note (optional)')}<p className="ztax-note">Locks build v{w.latestBuild?.version} and its source snapshot for review.</p></>
      break
    case 'RETURN_TO_DRAFT':
      fields = area('note', 'Why it goes back (required)')
      break
    case 'APPROVE':
      fields = <>{area('note', 'Review note')}<p className="ztax-note">Approves frozen build v{w.frozenBuildVersion}. Not by the person who froze or built it.</p></>
      break
    case 'CHANGE_BASIS':
      fields = <>{text('contentRelease', 'New content release')}{area('note', 'Why (required)')}
        <p className="ztax-note">An approval is revoked; a frozen workpaper returns to draft. The next freeze needs a rebuild.</p></>
      break
    case 'DOCUMENT_FX_RATE':
      fields = (
        <>
          {text('fxCurrency', 'Source currency', `Converted into ${w.reportingCurrency}.`)}
          {text('fxRate', 'Rate', `1 unit of the source currency in ${w.reportingCurrency}, e.g. 0.7300.`)}
          {text('fxSource', 'Rate source', 'e.g. Bank of Canada daily rate.')}
          <Field label="Rate date"><input type="date" value={form.fxDate} onChange={(e) => { set('fxDate')(e.target.value) }} /></Field>
          <Field label="Rounding">
            <select value={form.fxMode} onChange={(e) => { if (isRoundingMode(e.target.value)) set('fxMode')(e.target.value) }}>
              {ROUNDING_MODES.map((m) => <option key={m} value={m}>{words(m)}</option>)}
            </select>
          </Field>
          {text('fxScale', 'Rounding scale', 'Decimal places, 0 to 12.')}
          {area('fxBasis', 'Conversion basis')}
        </>
      )
      break
    case 'PROPOSE_ADJUSTMENT':
      fields = (
        <>
          <Field label="Kind">
            <select value={form.adjKind} onChange={(e) => { set('adjKind')(e.target.value === 'PERMITTED_ALLOWANCE' ? 'PERMITTED_ALLOWANCE' : 'AUDITED_ADJUSTMENT') }}>
              <option value="AUDITED_ADJUSTMENT">Audited adjustment (signed, added)</option>
              <option value="PERMITTED_ALLOWANCE">Permitted allowance (positive, deducted)</option>
            </select>
          </Field>
          {text('adjCategory', 'Category')}
          {text('adjAmount', `Amount (${w.reportingCurrency})`, `At or above ${w.materialityThreshold} it needs a second person.`)}
          {area('adjReason', 'Reason')}
          {file}
        </>
      )
      break
    case 'DECIDE_ADJUSTMENT':
      fields = (
        <>
          <Field label="Adjustment">
            <select value={form.adjustmentNumber} onChange={(e) => { set('adjustmentNumber')(e.target.value) }}>
              <option value="">Choose…</option>
              {w.adjustments.filter((a) => a.state === 'PROPOSED').map((a) => (
                <option key={a.number} value={String(a.number)}>#{a.number} {a.category} · {a.amount} {a.currency}</option>
              ))}
            </select>
          </Field>
          {decision}
          {area('note', 'Note (required to reject)')}
        </>
      )
      break
    case 'PROPOSE_RESOLUTION':
      fields = (
        <>
          <Field label="Exception">
            <select value={form.exceptionKey} onChange={(e) => { set('exceptionKey')(e.target.value) }}>
              <option value="">Choose…</option>
              {w.exceptions.filter((x) => x.resolvable && x.state === 'OPEN').map((x) => <option key={x.key} value={x.key}>{x.key}</option>)}
            </select>
          </Field>
          {area('note', 'Explanation')}
          {file}
        </>
      )
      break
    case 'DECIDE_RESOLUTION':
      fields = (
        <>
          <Field label="Resolution">
            <select value={form.exceptionKey} onChange={(e) => { set('exceptionKey')(e.target.value) }}>
              <option value="">Choose…</option>
              {w.exceptions.filter((x) => x.state === 'PENDING').map((x) => <option key={x.key} value={x.key}>{x.key}</option>)}
            </select>
          </Field>
          {decision}
          {area('note', 'Note (required to reject)')}
        </>
      )
      break
  }

  return (
    <Card title="Act">
      <Field label="Action">
        <select value={current} onChange={(e) => {
          setChosen(acts.find((a) => a === e.target.value) ?? '')
          setInvalid(null)
          action.reset()
        }}>
          <option value="">Choose…</option>
          {acts.map((a) => (
            <option key={a} value={a} disabled={!hasRole(roles, ...ACTION_ROLES[a])}>
              {ACTION_LABEL[a]}{hasRole(roles, ...ACTION_ROLES[a]) ? '' : ` (needs ${ACTION_ROLES[a].join(' or ')})`}
            </option>
          ))}
        </select>
      </Field>
      {current === '' ? null : (
        <form className="ztax-form" style={{ marginTop: 12 }} onSubmit={(e) => { e.preventDefault(); submit() }}>
          {fields}
          {evidence === null || (current !== 'PROPOSE_ADJUSTMENT' && current !== 'PROPOSE_RESOLUTION') ? null : (
            <p className="mono muted" style={{ gridColumn: '1 / -1', overflowWrap: 'anywhere' }}>SHA-256 {evidence.sha256}</p>
          )}
          <div className="ztax-form-actions"><Submit busy={action.busy}>{ACTION_LABEL[current]}</Submit></div>
          {invalid === null ? null : <p className="ztax-error-note" role="alert" style={{ gridColumn: '1 / -1' }}>{invalid}</p>}
          <div style={{ gridColumn: '1 / -1' }}><Refusal error={action.error} /></div>
        </form>
      )}
    </Card>
  )
}

// ---------------------------------------------------------------------------
// inputs and history
// ---------------------------------------------------------------------------

const Inputs = ({ w }: { w: Workpaper }) => (
  <Card title="Rates and adjustments">
    <h3 className="ztax-card-title">Documented FX rates</h3>
    {w.fxRates.length === 0 ? <Empty>None documented.</Empty> : (
      <ul>
        {w.fxRates.map((r) => (
          <li key={r.currency}>
            <span className="mono">1 {r.currency} = {r.rate} {w.reportingCurrency}</span> · {r.rateSource} {r.rateDate} · {words(r.roundingMode)} at {r.scale}
            <div className="muted">{r.conversionBasis} · by <Id value={r.recordedBy} /></div>
          </li>
        ))}
      </ul>
    )}
    <h3 className="ztax-card-title" style={{ marginTop: 16 }}>Adjustments</h3>
    {w.adjustments.length === 0 ? <Empty>None proposed.</Empty> : (
      <table className="ztax-action-table">
        <thead><tr><th>#</th><th>Kind</th><th>Amount</th><th>State</th><th>By</th></tr></thead>
        <tbody>
          {w.adjustments.map((a) => (
            <tr key={a.number}>
              <td>{a.number}</td>
              <td>{words(a.kind)}<div className="muted">{a.category}{a.material ? ' · material' : ''}</div></td>
              <td style={{ whiteSpace: 'nowrap' }}><Amount amount={a.amount} currency={a.currency} /></td>
              <td><Status value={a.state === 'PROPOSED' ? 'APPROVAL_REQUIRED' : a.state} /></td>
              <td><Id value={a.proposedBy} />{a.decidedBy === undefined ? null : <> → <Id value={a.decidedBy} /></>}</td>
            </tr>
          ))}
        </tbody>
      </table>
    )}
  </Card>
)

const History = ({ w }: { w: Workpaper }) => (
  <Card title="History">
    <table className="ztax-action-table">
      <thead><tr><th>#</th><th>Event</th><th>Status after</th><th>Detail</th><th>When</th><th>By</th></tr></thead>
      <tbody>
        {[...w.history].reverse().map((e) => (
          <tr key={e.seq}>
            <td>{e.seq}</td><td>{words(e.kind)}</td><td><Status value={e.status} /></td>
            <td>
              {[e.buildVersion === undefined ? null : `build v${e.buildVersion}`, e.contentRelease, e.decision, e.exceptionKey,
                e.adjustmentNumber === undefined ? null : `adjustment #${e.adjustmentNumber}`, e.adjustmentAmount,
                e.fx === undefined ? null : `${e.fx.currency} @ ${e.fx.rate}`, e.note].filter((x) => x !== null && x !== undefined).join(' · ') || '—'}
              {e.evidence === undefined ? null : <div className="muted mono">{e.evidence.filename} · {e.evidence.sha256}</div>}
            </td>
            <td>{instant(e.recordedAt)}</td><td><Id value={e.recordedBy} /></td>
          </tr>
        ))}
      </tbody>
    </table>
    <Raw value={w} />
  </Card>
)
