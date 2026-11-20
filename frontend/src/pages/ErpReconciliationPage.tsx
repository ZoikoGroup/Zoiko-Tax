import { useEffect, useState, type FormEvent } from 'react'

import { ZoikoTaxError } from '@zoikotax/sdk'

import { instant, periodLabel } from '../dashboard/format'
import { ztax } from '../platform/client'
import {
  Amount,
  Card,
  Columns,
  Empty,
  ErrorNote,
  errorText,
  Field,
  Id,
  Loaded,
  PageHeader,
  Raw,
  Submit,
  useAction,
  useLoad,
} from '../ui/kit'
import { CURRENCY, TabBar } from './determine/shared'
import {
  attemptsOf,
  type Bridge,
  CONTROL_ACCOUNTS,
  type ControlAccount,
  dispatchable,
  type Export,
  EXPORT_STATUSES,
  type ExportStatus,
  type GLReconciliation,
  HOLDS,
  isExportStatus,
  isResponseKind,
  isVendor,
  parseBalances,
  type ResponseKind,
  RESPONSES,
  type Snapshot,
  toneOf,
  type Vendor,
  VENDORS,
} from './erp/model'
import { useRoutedTab } from './routedTab'
import type { PageProps } from './types'
import { hasRole, Tiles, words } from './workspace'

// The ERP journal bridge and the subledger-to-GL reconciliation (LED-23,
// LED-18; R6). Every amount is the string the cell computed; nothing here
// adds or compares one. No ERP is connected to this cell: what the ERP
// answered, and the GL balances it reported, are recorded here by a person
// with the ERP's own references.

type Tab = 'exports' | 'balances' | 'reconcile' | 'bridges'

const TABS: readonly { value: Tab; label: string }[] = [
  { value: 'exports', label: 'Export batches' },
  { value: 'balances', label: 'ERP GL balances' },
  { value: 'reconcile', label: 'GL reconciliation' },
  { value: 'bridges', label: 'Bridges & mapping' },
]

const reasonOf = (error: unknown): string | null => (error instanceof ZoikoTaxError ? error.reasonCode : null)

/** A status in its tone, without touching the shared tone table. */
const Tag = ({ value }: { value: string | undefined }) => (
  <span className={`ztax-status ztax-status--${value === undefined ? 'neutral' : toneOf(value)}`}>
    {value === undefined ? '—' : words(value)}
  </span>
)

export const ErpReconciliationPage = ({ session, period, route, onTabChange, onNavigate }: PageProps) => {
  const [tab, setTab] = useRoutedTab(TABS.map((t) => t.value), 'exports', route, onTabChange)
  const roles = session.user.roles
  const { load, reload } = useLoad((signal) => ztax.listERPBridges(signal), [])
  const [bridgeId, setBridgeId] = useState<string | null>(null)

  return (
    <div className="ztax-page">
      <PageHeader
        title="ERP Reconciliation"
        subtitle="Sealed export batches of posted subledger journals, their delivery to the customer's ERP, and the subledger-to-GL reconciliation (R6)."
      />
      <p className="ztax-note" role="note">
        ZoikoTax is not the general ledger, and no ERP is connected to this cell. A dispatch leaves through the outbox for a
        connector; what the ERP answered and the GL balances it reported are recorded here with the ERP’s own references.
        Transmitted and acknowledged are not posted — only an ERP posting counts towards the GL reconciliation.
      </p>
      <Loaded load={load}>
        {(data) => {
          const bridges = data.bridges
          const current = bridges.find((b) => b.id === bridgeId) ?? bridges[0]
          return (
            <>
              {bridges.length === 0 ? null : (
                <div className="ztax-filter-bar" style={{ marginBottom: 12 }}>
                  <Field label="Bridge">
                    <select value={current?.id ?? ''} onChange={(e) => { setBridgeId(e.target.value) }}>
                      {bridges.map((b) => (
                        <option key={b.id} value={b.id}>{b.name} · {b.vendor} · {b.companyCode}</option>
                      ))}
                    </select>
                  </Field>
                  <button type="button" className="ztax-btn-quiet" onClick={reload}>Refresh</button>
                </div>
              )}
              <TabBar label="ERP reconciliation" tabs={TABS} current={tab} onSelect={setTab} />
              {current === undefined && tab !== 'bridges' ? (
                <Card>
                  <Empty>No ERP bridge is recorded yet. An OPERATOR records one, with its account mapping, under Bridges &amp; mapping.</Empty>
                  <button type="button" className="ztax-btn-quiet" onClick={() => { setTab('bridges') }}>Open Bridges &amp; mapping</button>
                </Card>
              ) : tab === 'bridges' ? (
                <Bridges bridges={bridges} current={current} roles={roles}
                  onChanged={(b) => { setBridgeId(b.id); reload() }} />
              ) : current === undefined ? null : tab === 'exports' ? (
                <Exports key={current.id} bridge={current} period={period} roles={roles} />
              ) : tab === 'balances' ? (
                <Balances key={current.id} bridge={current} period={period} roles={roles} onReconciled={() => { setTab('reconcile') }} />
              ) : (
                <Reconciliations key={current.id} bridge={current} period={period}
                  onVariances={() => { onNavigate('reconcile', { tab: 'variances' }) }} />
              )}
            </>
          )
        }}
      </Loaded>
    </div>
  )
}

// ---------------------------------------------------------------------------
// bridges and mapping
// ---------------------------------------------------------------------------

const Bridges = ({ bridges, current, roles, onChanged }: {
  bridges: readonly Bridge[]
  current: Bridge | undefined
  roles: readonly string[]
  onChanged: (b: Bridge) => void
}) => {
  const operator = hasRole(roles, 'OPERATOR')
  return (
    <>
      <Card title="Bridges">
        {bridges.length === 0 ? <Empty>No bridge yet.</Empty> : (
          <div style={{ overflowX: 'auto' }}>
            <table className="ztax-action-table">
              <thead><tr><th>Name</th><th>Vendor</th><th>Company code</th><th>Legal entity</th><th>Mapping</th><th>Created</th></tr></thead>
              <tbody>
                {bridges.map((b) => (
                  <tr key={b.id} className={b.id === current?.id ? 'ztax-row-selected' : undefined}>
                    <td>{b.name}</td>
                    <td>{b.vendor}</td>
                    <td className="mono">{b.companyCode}</td>
                    <td><Id value={b.legalEntityId} /></td>
                    <td>{b.currentMappingVersion === undefined ? <Tag value="UNMAPPED" /> : `v${b.currentMappingVersion}`}</td>
                    <td>{instant(b.createdAt)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>
      <Columns>
        {operator ? <CreateBridge onCreated={onChanged} /> : (
          <Card><p className="ztax-note">Recording a bridge or a mapping needs the OPERATOR role.</p></Card>
        )}
        {current === undefined ? null : <Mapping key={current.id} bridge={current} canRecord={operator} onRecorded={onChanged} />}
      </Columns>
    </>
  )
}

const CreateBridge = ({ onCreated }: { onCreated: (b: Bridge) => void }) => {
  const [name, setName] = useState('')
  const [vendor, setVendor] = useState<Vendor>('SAP')
  const [companyCode, setCompanyCode] = useState('')
  const action = useAction<Bridge>()
  const submit = (e: FormEvent) => {
    e.preventDefault()
    void action.run(() => ztax.createERPBridge({ name: name.trim(), vendor, companyCode: companyCode.trim() })).then((b) => {
      if (b !== null) {
        setName('')
        setCompanyCode('')
        onCreated(b)
      }
    })
  }
  return (
    <Card title="Record a bridge">
      <form className="ztax-form" onSubmit={submit}>
        <Field label="Name" help="A label for the connection, e.g. Acme Telecom — SAP S/4HANA.">
          <input value={name} required maxLength={200} onChange={(e) => { setName(e.target.value) }} />
        </Field>
        <Field label="ERP vendor" help="Names the connector that will consume a dispatch. Each target needs its own contract verification.">
          <select value={vendor} onChange={(e) => { if (isVendor(e.target.value)) setVendor(e.target.value) }}>
            {VENDORS.map((v) => <option key={v.value} value={v.value}>{v.label}</option>)}
          </select>
        </Field>
        <Field label="ERP company code" help="The legal entity's company code in the ERP.">
          <input value={companyCode} required pattern="[A-Za-z0-9._\-]{1,40}" onChange={(e) => { setCompanyCode(e.target.value) }} />
        </Field>
        <div className="ztax-form-actions"><Submit busy={action.busy}>Record bridge</Submit></div>
        <div style={{ gridColumn: '1 / -1' }}><ErrorNote error={action.error} /></div>
      </form>
    </Card>
  )
}

const Mapping = ({ bridge, canRecord, onRecorded }: { bridge: Bridge; canRecord: boolean; onRecorded: (b: Bridge) => void }) => {
  const latest = bridge.mappings[bridge.mappings.length - 1]
  const initial = (a: ControlAccount) => latest?.accounts.find((m) => m.account === a)?.glAccount ?? ''
  const [chart, setChart] = useState(latest?.chartVersion ?? '')
  const [targets, setTargets] = useState<Record<string, string>>(
    () => Object.fromEntries(CONTROL_ACCOUNTS.map((a) => [a, initial(a)])),
  )
  const [unchanged, setUnchanged] = useState(false)
  const action = useAction<Bridge>()
  const submit = (e: FormEvent) => {
    e.preventDefault()
    setUnchanged(false)
    const accounts = CONTROL_ACCOUNTS.flatMap((account) => {
      const glAccount = (targets[account] ?? '').trim()
      return glAccount === '' ? [] : [{ account, glAccount }]
    })
    const before = bridge.mappings.length
    void action.run(() => ztax.recordERPMapping(bridge.id, { chartVersion: chart.trim(), accounts })).then((b) => {
      if (b !== null) {
        setUnchanged(b.mappings.length === before)
        onRecorded(b)
      }
    })
  }
  return (
    <Card title={`Account mapping · ${bridge.name}`}>
      <p className="ztax-note">
        Each TCSL control account onto one of the customer’s GL accounts. A version is never edited; a change is the next
        version, and a batch prepared under an earlier one is no longer dispatched. An unmapped account holds any export
        that needs it — nothing goes to a suspense account by default.
      </p>
      {latest === undefined ? <p className="muted">No mapping yet.</p> : (
        <p className="muted">Current: v{latest.version} · chart {latest.chartVersion} · <Id value={latest.digest} /> · {instant(latest.recordedAt)}</p>
      )}
      {!canRecord ? null : (
        <form className="ztax-form" onSubmit={submit}>
          <Field label="Chart-of-accounts version">
            <input value={chart} required maxLength={200} onChange={(e) => { setChart(e.target.value) }} />
          </Field>
          {CONTROL_ACCOUNTS.map((a) => (
            <Field key={a} label={words(a)}>
              <input className="mono" value={targets[a] ?? ''} placeholder="unmapped" pattern="[A-Za-z0-9._:/\-]{1,60}"
                onChange={(e) => { setTargets((t) => ({ ...t, [a]: e.target.value })) }} />
            </Field>
          ))}
          <div className="ztax-form-actions"><Submit busy={action.busy}>Record mapping version</Submit></div>
          {unchanged ? <p className="ztax-note" style={{ gridColumn: '1 / -1' }}>That is the current mapping already; no version was recorded.</p> : null}
          <div style={{ gridColumn: '1 / -1' }}><ErrorNote error={action.error} /></div>
        </form>
      )}
    </Card>
  )
}

// ---------------------------------------------------------------------------
// export batches
// ---------------------------------------------------------------------------

const today = () => new Date().toISOString().slice(0, 10)

const Exports = ({ bridge, period, roles }: { bridge: Bridge; period: string; roles: readonly string[] }) => {
  const [month, setMonth] = useState(period)
  const [status, setStatus] = useState<ExportStatus | ''>('')
  const [selected, setSelected] = useState<string | null>(null)
  const { load, reload } = useLoad(
    (signal) => ztax.listERPExports(bridge.id, { ...(month === '' ? {} : { period: month }), ...(status === '' ? {} : { status }) }, signal),
    [bridge.id, month, status],
  )
  useEffect(() => { setMonth(period) }, [period])

  return (
    <>
      {hasRole(roles, 'OPERATOR') ? (
        <Prepare bridge={bridge} period={period} onPrepared={(b) => { setSelected(b.id); reload() }} />
      ) : null}
      <Card
        title="Export batches"
        aside={
          <div className="ztax-filter-bar">
            <Field label="Period">
              <input type="month" value={month} onChange={(e) => { setMonth(e.target.value) }} />
            </Field>
            <Field label="Transfer state">
              <select value={status} onChange={(e) => { setStatus(isExportStatus(e.target.value) ? e.target.value : '') }}>
                <option value="">All</option>
                {EXPORT_STATUSES.map((s) => <option key={s} value={s}>{words(s)}</option>)}
              </select>
            </Field>
            <button type="button" className="ztax-btn-quiet" onClick={reload}>Refresh</button>
          </div>
        }
      >
        <Loaded load={load}>
          {(data) => data.exports.length === 0 ? (
            <Empty>No batch matches. An OPERATOR prepares one from the period’s posted journals.</Empty>
          ) : (
            <>
              <Tiles tiles={[
                { label: 'Batches', count: data.exports.length },
                { label: 'ERP posted', count: data.exports.filter((b) => b.status === 'ERP_POSTED').length, tone: 'ok' },
                { label: 'In flight', count: data.exports.filter((b) => ['PREPARED', 'TRANSMITTED', 'ACKNOWLEDGED'].includes(b.status)).length, tone: 'warn' },
                { label: 'Open corrections', count: data.exports.filter((b) => b.correction?.open === true).length, tone: 'bad' },
              ]} />
              <div style={{ overflowX: 'auto' }}>
                <table className="ztax-action-table">
                  <thead>
                    <tr>
                      <th>Batch</th><th>Period</th><th>Posting date</th><th>Mapping</th><th>Journals</th><th>Debits</th>
                      <th>Credits</th><th>Attempts</th><th>Transfer state</th><th>Correction</th><th>Action</th>
                    </tr>
                  </thead>
                  <tbody>
                    {data.exports.map((b) => (
                      <tr key={b.id} className={b.id === selected ? 'ztax-row-selected' : undefined}>
                        <td><Id value={b.id} /></td>
                        <td>{b.period}</td>
                        <td>{b.postingDate}</td>
                        <td>v{b.mappingVersion}</td>
                        <td>{b.journalIds.length}</td>
                        <td style={{ whiteSpace: 'nowrap' }}><Amount amount={b.totalDebits} currency={b.currency} /></td>
                        <td style={{ whiteSpace: 'nowrap' }}><Amount amount={b.totalCredits} currency={b.currency} /></td>
                        <td>{attemptsOf(b).length}</td>
                        <td><Tag value={b.status} /></td>
                        <td>{b.correction === undefined ? '—' : <Tag value={b.correction.open ? 'OPEN' : 'CLOSED'} />}</td>
                        <td>
                          <button type="button" className="ztax-table-action-btn" aria-pressed={b.id === selected}
                            onClick={() => { setSelected(b.id) }}>
                            Open
                          </button>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </>
          )}
        </Loaded>
      </Card>
      {selected === null ? null : (
        <BatchDetail key={selected} bridge={bridge} batchId={selected} roles={roles} onChanged={reload}
          onClose={() => { setSelected(null) }} />
      )}
    </>
  )
}

const Prepare = ({ bridge, period, onPrepared }: { bridge: Bridge; period: string; onPrepared: (b: Export) => void }) => {
  const [month, setMonth] = useState(period)
  const [currency, setCurrency] = useState('USD')
  const [postingDate, setPostingDate] = useState(today())
  const [invalid, setInvalid] = useState<string | null>(null)
  const action = useAction<Export>()
  const submit = (e: FormEvent) => {
    e.preventDefault()
    const cur = currency.trim().toUpperCase()
    if (!CURRENCY.test(cur)) {
      setInvalid('The currency is an ISO code such as USD.')
      return
    }
    setInvalid(null)
    void action.run(() => ztax.prepareERPExport(bridge.id, { period: month, currency: cur, postingDate })).then((b) => {
      if (b !== null) onPrepared(b)
    })
  }
  const reason = reasonOf(action.error)
  return (
    <Card title="Prepare an export batch">
      <p className="ztax-note">
        Seals every posted journal of the period and currency not already in a live batch: the exact journal set, each line
        mapped by mapping v{bridge.currentMappingVersion ?? '—'}, under one digest. A checker other than you authorises the
        dispatch.
      </p>
      <form className="ztax-form" onSubmit={submit}>
        <Field label="Legal period">
          <input type="month" required value={month} onChange={(e) => { setMonth(e.target.value) }} />
        </Field>
        <Field label="Currency">
          <input value={currency} required maxLength={3} onChange={(e) => { setCurrency(e.target.value) }} />
        </Field>
        <Field label="ERP posting date">
          <input type="date" required value={postingDate} onChange={(e) => { setPostingDate(e.target.value) }} />
        </Field>
        <div className="ztax-form-actions"><Submit busy={action.busy}>Prepare and seal</Submit></div>
        {invalid === null ? null : <p className="ztax-error-note" role="alert" style={{ gridColumn: '1 / -1' }}>{invalid}</p>}
        {action.error === null ? null : (
          <div style={{ gridColumn: '1 / -1' }}>
            {reason === 'ERP_MAPPING_INCOMPLETE' ? (
              <p className="ztax-error-note" role="alert">
                The export is held: a journal posts to an account the mapping does not map. Record a mapping version that maps it.{' '}
                <span className="mono muted">{errorText(action.error)}</span>
              </p>
            ) : reason === 'ERP_NOTHING_TO_EXPORT' ? (
              <p className="ztax-note" role="alert">Every posted journal of this period and currency is already in a live batch.</p>
            ) : <ErrorNote error={action.error} />}
          </div>
        )}
      </form>
    </Card>
  )
}

const BatchDetail = ({ bridge, batchId, roles, onChanged, onClose }: {
  bridge: Bridge
  batchId: string
  roles: readonly string[]
  onChanged: () => void
  onClose: () => void
}) => {
  const { load, reload } = useLoad((signal) => ztax.getERPExport(bridge.id, batchId, signal), [bridge.id, batchId])
  const changed = () => {
    reload()
    onChanged()
  }
  return (
    <Loaded load={load}>
      {(b) => (
        <>
          <Columns>
            <Card title={`Batch ${b.period} · ${b.currency}`} aside={<button type="button" className="ztax-btn-quiet" onClick={onClose}>Close</button>}>
              <dl className="ztax-kv">
                <dt>Transfer state</dt><dd><Tag value={b.status} /></dd>
                <dt>Batch</dt><dd><Id value={b.id} /></dd>
                <dt>External reference</dt><dd className="mono">{b.externalRef}</dd>
                <dt>Batch digest</dt><dd className="mono" style={{ wordBreak: 'break-all' }}>{b.batchDigest}</dd>
                <dt>Company code</dt><dd className="mono">{b.companyCode}</dd>
                <dt>Mapping</dt><dd>v{b.mappingVersion} · chart {b.chartVersion} · <Id value={b.mappingDigest} /></dd>
                <dt>Posting date</dt><dd>{b.postingDate}</dd>
                <dt>Journals</dt><dd>{b.journalIds.length} · {b.lineCount} lines</dd>
                <dt>Totals</dt><dd><Amount amount={b.totalDebits} currency={b.currency} /> debit · <Amount amount={b.totalCredits} currency={b.currency} /> credit</dd>
                <dt>Prepared</dt><dd>{instant(b.preparedAt)} by <Id value={b.preparedBy} /></dd>
              </dl>
              {b.correction === undefined ? null : (
                <div className={b.correction.open ? 'zp-warn-note' : 'ztax-note'} role="note" style={{ marginTop: 12 }}>
                  <strong>Correction task {b.correction.open ? 'open' : 'closed'}.</strong>{' '}
                  {b.correction.reason ?? ''}{' '}
                  {b.correction.open
                    ? `${b.correction.outstandingJournalIds.length} journal(s) are not in a posted batch yet; the period's GL reconciliation is held. Prepare a new batch for them once the cause is fixed.`
                    : 'Every rejected journal is now in a batch the ERP posted.'}
                </div>
              )}
              <Raw value={b} />
            </Card>
            <Card title="Act on the batch">
              <Actions bridge={bridge} batch={b} roles={roles} onChanged={changed} />
            </Card>
          </Columns>
          <Card title="Delivery evidence">
            <div style={{ overflowX: 'auto' }}>
              <table className="ztax-action-table">
                <thead>
                  <tr><th>#</th><th>Event</th><th>State after</th><th>Attempt</th><th>Remote correlation</th><th>ERP reference</th><th>Response digest</th><th>Detail</th><th>When</th><th>By</th></tr>
                </thead>
                <tbody>
                  {b.history.map((e) => (
                    <tr key={e.seq}>
                      <td>{e.seq}</td><td>{words(e.kind)}</td><td><Tag value={e.status} /></td><td><Id value={e.attemptId} /></td>
                      <td className="mono">{e.remoteCorrelation ?? '—'}</td><td className="mono">{e.erpReference ?? '—'}</td>
                      <td><Id value={e.responseDigest} /></td><td>{e.detail ?? '—'}</td><td>{instant(e.recordedAt)}</td><td><Id value={e.recordedBy} /></td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </Card>
          <Card title="Exported lines" aside={<span className="ztax-timestamp">each traced to its TCSL journal line</span>}>
            {(b.lines ?? []).length === 0 ? <Empty>No lines.</Empty> : (
              <div style={{ overflowX: 'auto' }}>
                <table className="ztax-action-table">
                  <thead><tr><th>Journal</th><th>Line</th><th>TCSL account</th><th>GL account</th><th>Side</th><th>Amount</th><th>Authority</th><th>Jurisdiction</th></tr></thead>
                  <tbody>
                    {(b.lines ?? []).map((l) => (
                      <tr key={`${l.journalId}/${l.journalLine}`}>
                        <td><Id value={l.journalId} /></td><td>{l.journalLine}</td><td>{words(l.account)}</td>
                        <td className="mono">{l.glAccount}</td><td>{l.side}</td>
                        <td style={{ whiteSpace: 'nowrap' }}><Amount amount={l.amount} currency={b.currency} /></td>
                        <td>{l.authority ?? '—'}</td><td>{l.jurisdiction ?? '—'}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </Card>
        </>
      )}
    </Loaded>
  )
}

const Actions = ({ bridge, batch, roles, onChanged }: { bridge: Bridge; batch: Export; roles: readonly string[]; onChanged: () => void }) => {
  const checker = hasRole(roles, 'OPERATOR', 'ANALYST')
  const operator = hasRole(roles, 'OPERATOR')
  const answerable = ['TRANSMITTED', 'ACKNOWLEDGED', 'OUTCOME_UNKNOWN'].includes(batch.status)
  if (!checker) {
    return <p className="ztax-note">Dispatching a batch and recording the ERP’s answers needs the OPERATOR or ANALYST role.</p>
  }
  return (
    <>
      {dispatchable(batch) ? <Dispatch bridge={bridge} batch={batch} onChanged={onChanged} /> : null}
      {batch.status === 'OUTCOME_UNKNOWN' && !dispatchable(batch) ? (
        <p className="zp-warn-note" role="note">
          The ERP’s outcome is unknown. Query the ERP by <span className="mono">{batch.externalRef}</span> and record what it
          says before anything is sent again — a distinct resend could post the journal twice.
        </p>
      ) : null}
      {answerable ? <Respond bridge={bridge} batch={batch} onChanged={onChanged} /> : null}
      {batch.status === 'PREPARED' && operator ? <Withdraw bridge={bridge} batch={batch} onChanged={onChanged} /> : null}
      {batch.status === 'ERP_POSTED' ? <p className="ztax-note">Posted by the ERP. Nothing further to do on this batch.</p> : null}
      {batch.status === 'REJECTED' || batch.status === 'WITHDRAWN'
        ? <p className="ztax-note">A {words(batch.status).toLowerCase()} batch is never sent. Its journals can be prepared into a new batch.</p>
        : null}
    </>
  )
}

const Dispatch = ({ bridge, batch, onChanged }: { bridge: Bridge; batch: Export; onChanged: () => void }) => {
  const [confirmed, setConfirmed] = useState(false)
  const action = useAction<Export>()
  const submit = (e: FormEvent) => {
    e.preventDefault()
    void action.run(() => ztax.dispatchERPExport(bridge.id, batch.id, { expectedDigest: batch.batchDigest })).then((b) => {
      if (b !== null) onChanged()
    })
  }
  const reason = reasonOf(action.error)
  return (
    <form className="ztax-form" onSubmit={submit}>
      <Field label="Authorise dispatch" help="You authorise exactly this sealed batch; the cell refuses a dispatch whose digest has changed or whose mapping is no longer current.">
        <label style={{ display: 'flex', gap: 8, alignItems: 'baseline' }}>
          <input type="checkbox" checked={confirmed} onChange={(e) => { setConfirmed(e.target.checked) }} />
          <span>I reviewed batch <span className="mono">{batch.batchDigest.slice(0, 16)}…</span> and its {batch.lineCount} lines.</span>
        </label>
      </Field>
      <div className="ztax-form-actions">
        <button type="submit" className="ztax-btn-copper" disabled={action.busy || !confirmed}>
          {action.busy ? 'Sending…' : batch.status === 'PREPARED' ? 'Dispatch through the outbox' : 'Send again under the same reference'}
        </button>
      </div>
      {action.error === null ? null : (
        <div style={{ gridColumn: '1 / -1' }}>
          {reason === 'MAKER_CHECKER' ? (
            <p className="ztax-error-note" role="alert">You prepared this batch, so someone else must authorise its dispatch.</p>
          ) : reason === 'ERP_MAPPING_CHANGED' ? (
            <p className="ztax-error-note" role="alert">The mapping changed after this batch was sealed. Withdraw it and prepare a new one.</p>
          ) : <ErrorNote error={action.error} />}
        </div>
      )}
    </form>
  )
}

const Respond = ({ bridge, batch, onChanged }: { bridge: Bridge; batch: Export; onChanged: () => void }) => {
  const attempts = attemptsOf(batch)
  const [kind, setKind] = useState<ResponseKind>(batch.status === 'OUTCOME_UNKNOWN' ? 'STATUS_NOT_FOUND' : 'ACKNOWLEDGED')
  const [attemptId, setAttemptId] = useState(attempts[attempts.length - 1] ?? '')
  const [form, setForm] = useState({ remoteCorrelation: '', erpReference: '', responseDigest: '', detail: '' })
  const action = useAction<Export>()
  const set = (k: keyof typeof form) => (v: string) => { setForm((f) => ({ ...f, [k]: v })) }
  const help = RESPONSES.find((r) => r.value === kind)?.help
  const submit = (e: FormEvent) => {
    e.preventDefault()
    const v = (k: keyof typeof form) => form[k].trim()
    void action.run(() => ztax.recordERPResponse(bridge.id, batch.id, {
      kind,
      attemptId,
      ...(v('remoteCorrelation') === '' ? {} : { remoteCorrelation: v('remoteCorrelation') }),
      ...(v('erpReference') === '' ? {} : { erpReference: v('erpReference') }),
      ...(v('responseDigest') === '' ? {} : { responseDigest: v('responseDigest') }),
      ...(v('detail') === '' ? {} : { detail: v('detail') }),
    })).then((b) => {
      if (b !== null) {
        setForm({ remoteCorrelation: '', erpReference: '', responseDigest: '', detail: '' })
        onChanged()
      }
    })
  }
  return (
    <form className="ztax-form" onSubmit={submit} style={{ marginTop: 12 }}>
      <Field label="ERP answer" {...(help === undefined ? {} : { help })}>
        <select value={kind} onChange={(e) => { if (isResponseKind(e.target.value)) setKind(e.target.value) }}>
          {RESPONSES.map((r) => <option key={r.value} value={r.value}>{r.label}</option>)}
        </select>
      </Field>
      <Field label="About attempt">
        <select value={attemptId} onChange={(e) => { setAttemptId(e.target.value) }}>
          {attempts.map((a, i) => <option key={a} value={a}>#{i + 1} · {a}</option>)}
        </select>
      </Field>
      <Field label="Remote correlation" help="The ERP's message, receipt or query reference.">
        <input className="mono" value={form.remoteCorrelation} maxLength={200}
          required={kind === 'ACKNOWLEDGED' || kind === 'STATUS_NOT_FOUND'} onChange={(e) => { set('remoteCorrelation')(e.target.value) }} />
      </Field>
      <Field label="ERP posted journal reference">
        <input className="mono" value={form.erpReference} maxLength={200} required={kind === 'POSTED'}
          onChange={(e) => { set('erpReference')(e.target.value) }} />
      </Field>
      <Field label="Response digest" help="Checksum of the raw ERP response kept as evidence. Optional.">
        <input className="mono" value={form.responseDigest} maxLength={200} onChange={(e) => { set('responseDigest')(e.target.value) }} />
      </Field>
      <Field label="Detail">
        <textarea value={form.detail} maxLength={2000} required={kind === 'REJECTED' || kind === 'OUTCOME_UNKNOWN'}
          onChange={(e) => { set('detail')(e.target.value) }} />
      </Field>
      <div className="ztax-form-actions"><Submit busy={action.busy}>Record the ERP’s answer</Submit></div>
      <div style={{ gridColumn: '1 / -1' }}><ErrorNote error={action.error} /></div>
    </form>
  )
}

const Withdraw = ({ bridge, batch, onChanged }: { bridge: Bridge; batch: Export; onChanged: () => void }) => {
  const [reason, setReason] = useState('')
  const action = useAction<Export>()
  const submit = (e: FormEvent) => {
    e.preventDefault()
    void action.run(() => ztax.withdrawERPExport(bridge.id, batch.id, { reason: reason.trim() })).then((b) => {
      if (b !== null) onChanged()
    })
  }
  return (
    <form className="ztax-form" onSubmit={submit} style={{ marginTop: 12 }}>
      <Field label="Withdraw reason" help="Releases a batch never sent, so its journals can be prepared again. The batch stays on record.">
        <textarea value={reason} required maxLength={2000} onChange={(e) => { setReason(e.target.value) }} />
      </Field>
      <div className="ztax-form-actions"><Submit busy={action.busy}>Withdraw</Submit></div>
      <div style={{ gridColumn: '1 / -1' }}><ErrorNote error={action.error} /></div>
    </form>
  )
}

// ---------------------------------------------------------------------------
// ERP GL balances
// ---------------------------------------------------------------------------

const Balances = ({ bridge, period, roles, onReconciled }: {
  bridge: Bridge
  period: string
  roles: readonly string[]
  onReconciled: () => void
}) => {
  const [month, setMonth] = useState(period)
  const { load, reload } = useLoad(
    (signal) => ztax.listGLSnapshots(bridge.id, month === '' ? {} : { period: month }, signal),
    [bridge.id, month],
  )
  const operator = hasRole(roles, 'OPERATOR')
  const run = useAction<GLReconciliation>()
  return (
    <>
      {operator ? <RecordSnapshot bridge={bridge} period={period} onRecorded={reload} /> : (
        <Card><p className="ztax-note">Recording the ERP’s balances and reconciling need the OPERATOR role.</p></Card>
      )}
      <Card
        title="Balances the ERP reported"
        aside={
          <div className="ztax-filter-bar">
            <Field label="Period">
              <input type="month" value={month} onChange={(e) => { setMonth(e.target.value) }} />
            </Field>
            <button type="button" className="ztax-btn-quiet" onClick={reload}>Refresh</button>
          </div>
        }
      >
        <ErrorNote error={run.error} />
        <Loaded load={load}>
          {(data) => data.snapshots.length === 0 ? <Empty>No GL balances recorded for this bridge and period.</Empty> : (
            <div style={{ overflowX: 'auto' }}>
              <table className="ztax-action-table">
                <thead><tr><th>Period</th><th>Currency</th><th>As of</th><th>ERP report</th><th>Accounts</th><th>Digest</th><th>Recorded</th><th>Action</th></tr></thead>
                <tbody>
                  {data.snapshots.map((s: Snapshot) => (
                    <tr key={s.id}>
                      <td>{s.period}</td><td>{s.currency}</td><td>{instant(s.asOf)}</td>
                      <td className="mono">{s.sourceRef}{s.sourceDigest === undefined ? null : <> · <Id value={s.sourceDigest} /></>}</td>
                      <td>{s.balances.length}</td><td><Id value={s.digest} /></td><td>{instant(s.recordedAt)}</td>
                      <td>
                        {operator ? (
                          <button type="button" className="ztax-table-action-btn" disabled={run.busy}
                            onClick={() => {
                              void run.run(() => ztax.runGLReconciliation(bridge.id, { snapshotId: s.id })).then((r) => {
                                if (r !== null) onReconciled()
                              })
                            }}>
                            Reconcile
                          </button>
                        ) : null}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </Loaded>
      </Card>
    </>
  )
}

const RecordSnapshot = ({ bridge, period, onRecorded }: { bridge: Bridge; period: string; onRecorded: () => void }) => {
  const [form, setForm] = useState({ period, currency: 'USD', asOf: new Date().toISOString(), sourceRef: '', sourceDigest: '', balances: '' })
  const [invalid, setInvalid] = useState<string | null>(null)
  const action = useAction<Snapshot>()
  const set = (k: keyof typeof form) => (v: string) => { setForm((f) => ({ ...f, [k]: v })) }
  const submit = (e: FormEvent) => {
    e.preventDefault()
    const parsed = parseBalances(form.balances)
    const currency = form.currency.trim().toUpperCase()
    if (!parsed.ok) {
      setInvalid(parsed.error)
      return
    }
    if (!CURRENCY.test(currency)) {
      setInvalid('The currency is an ISO code such as USD.')
      return
    }
    setInvalid(null)
    void action.run(() => ztax.recordGLSnapshot(bridge.id, {
      period: form.period, currency, asOf: form.asOf.trim(), sourceRef: form.sourceRef.trim(),
      ...(form.sourceDigest.trim() === '' ? {} : { sourceDigest: form.sourceDigest.trim() }), balances: parsed.lines,
    })).then((s) => {
      if (s !== null) {
        set('balances')('')
        onRecorded()
      }
    })
  }
  return (
    <Card title="Record the GL balances the ERP reported">
      <p className="ztax-note">
        The period’s activity on each mapped GL account, debits and credits apart, exactly as the ERP report shows them.
        ZoikoTax reads no ERP itself: these are the ERP’s figures, recorded with the report they came from.
      </p>
      <form className="ztax-form" onSubmit={submit}>
        <Field label="Legal period">
          <input type="month" required value={form.period} onChange={(e) => { set('period')(e.target.value) }} />
        </Field>
        <Field label="Currency">
          <input value={form.currency} required maxLength={3} onChange={(e) => { set('currency')(e.target.value) }} />
        </Field>
        <Field label="As of" help="When the ERP's figures were taken, RFC 3339.">
          <input className="mono" value={form.asOf} required onChange={(e) => { set('asOf')(e.target.value) }} />
        </Field>
        <Field label="ERP report reference" help="E.g. the report name, company code and period.">
          <input value={form.sourceRef} required maxLength={200} onChange={(e) => { set('sourceRef')(e.target.value) }} />
        </Field>
        <Field label="Report checksum" help="Optional, when the report is kept as a file.">
          <input className="mono" value={form.sourceDigest} maxLength={200} onChange={(e) => { set('sourceDigest')(e.target.value) }} />
        </Field>
        <Field label="Balances" help="One GL account per line: account, debits, credits.">
          <textarea className="mono" rows={5} required value={form.balances} placeholder={'2310-00, 0.00, 17.50\n1990-00, 17.50, 0.00'}
            onChange={(e) => { set('balances')(e.target.value) }} />
        </Field>
        <div className="ztax-form-actions"><Submit busy={action.busy}>Record balances</Submit></div>
        {invalid === null ? null : <p className="ztax-error-note" role="alert" style={{ gridColumn: '1 / -1' }}>{invalid}</p>}
        <div style={{ gridColumn: '1 / -1' }}><ErrorNote error={action.error} /></div>
      </form>
    </Card>
  )
}

// ---------------------------------------------------------------------------
// GL reconciliation
// ---------------------------------------------------------------------------

const Reconciliations = ({ bridge, period, onVariances }: { bridge: Bridge; period: string; onVariances: () => void }) => {
  const [month, setMonth] = useState(period)
  const [selected, setSelected] = useState<string | null>(null)
  const { load, reload } = useLoad(
    (signal) => ztax.listGLReconciliations(bridge.id, month === '' ? {} : { period: month }, signal),
    [bridge.id, month],
  )
  return (
    <Card
      title="Subledger to GL (R6)"
      aside={
        <div className="ztax-filter-bar">
          <Field label="Period">
            <input type="month" value={month} onChange={(e) => { setMonth(e.target.value) }} />
          </Field>
          <button type="button" className="ztax-btn-quiet" onClick={reload}>Refresh</button>
          <button type="button" className="ztax-btn-quiet" onClick={onVariances}>Billing-to-tax variances</button>
        </div>
      }
    >
      <p className="ztax-note">
        Each GL account’s net debit in the subledger against what the ERP reported, exactly. There is no tolerance. A
        reconciliation is HELD while any journal of the period is not in a batch the ERP posted. A variance is cleared by
        fixing its source and reconciling a new snapshot — not by a note.
      </p>
      <Loaded load={load}>
        {(data) => {
          if (data.reconciliations.length === 0) {
            return <Empty>No reconciliation yet. Record the ERP’s balances, then reconcile them from ERP GL balances.</Empty>
          }
          const current = data.reconciliations.find((r) => r.id === selected) ?? data.reconciliations[0]
          return (
            <>
              <div style={{ overflowX: 'auto' }}>
                <table className="ztax-action-table">
                  <thead><tr><th>Ran</th><th>Period</th><th>Currency</th><th>Mapping</th><th>Outcome</th><th>Variances</th><th>Posted journals</th><th>Action</th></tr></thead>
                  <tbody>
                    {data.reconciliations.map((r) => (
                      <tr key={r.id} className={r.id === current?.id ? 'ztax-row-selected' : undefined}>
                        <td>{instant(r.ranAt)}</td><td>{periodLabel(r.period)}</td><td>{r.currency}</td><td>v{r.mappingVersion}</td>
                        <td><Tag value={r.outcome} /></td><td>{r.exceptionCount}</td><td>{r.postedJournalCount} / {r.journalCount}</td>
                        <td>
                          <button type="button" className="ztax-table-action-btn" aria-pressed={r.id === current?.id}
                            onClick={() => { setSelected(r.id) }}>
                            Open
                          </button>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
              {current === undefined ? null : <ReconciliationView rec={current} />}
            </>
          )
        }}
      </Loaded>
    </Card>
  )
}

const ReconciliationView = ({ rec }: { rec: GLReconciliation }) => (
  <div style={{ marginTop: 16 }}>
    <Tiles tiles={[
      { label: 'GL accounts compared', count: rec.items.length },
      { label: 'Tied', count: rec.items.filter((i) => i.status === 'MATCHED').length, tone: 'ok' },
      { label: 'Variances', count: rec.exceptionCount, tone: 'bad' },
      { label: 'Journals not yet posted', count: rec.journalCount - rec.postedJournalCount, tone: 'warn' },
    ]} />
    <dl className="ztax-kv">
      <dt>Outcome</dt><dd><Tag value={rec.outcome} /></dd>
      <dt>Snapshot</dt><dd><Id value={rec.snapshotId} /> · <Id value={rec.snapshotDigest} /></dd>
      <dt>Subledger population</dt><dd>{rec.journalCount} journals · <Id value={rec.populationDigest} /></dd>
      <dt>Legal entity</dt><dd><Id value={rec.legalEntityId} /></dd>
    </dl>
    {rec.holds.length === 0 ? null : (
      <div className="zp-warn-note" role="note">
        <strong>Held — not reconciled.</strong> Journals of {periodLabel(rec.period)} that the ERP has not posted:
        <ul className="zp-list">
          {rec.holds.map((h) => <li key={h.reason}>{h.journals} {HOLDS[h.reason]} <span className="mono muted">({h.reason})</span></li>)}
        </ul>
      </div>
    )}
    <div style={{ overflowX: 'auto' }}>
      <table className="ztax-action-table">
        <thead>
          <tr><th>GL account</th><th>TCSL accounts</th><th>Subledger (expected)</th><th>ERP (observed)</th><th>Variance</th><th>Status</th><th>Detail</th></tr>
        </thead>
        <tbody>
          {rec.items.map((i) => (
            <tr key={i.matchKey}>
              <td className="mono">{i.glAccount ?? '—'}</td>
              <td>{i.tcslAccounts.map(words).join(', ')}</td>
              <td style={{ whiteSpace: 'nowrap' }}><Amount amount={i.expected?.amount} currency={i.expected?.currency} /></td>
              <td style={{ whiteSpace: 'nowrap' }}><Amount amount={i.observed?.amount} currency={i.observed?.currency} /></td>
              <td style={{ whiteSpace: 'nowrap' }}><Amount amount={i.variance?.amount} currency={i.variance?.currency} /></td>
              <td><Tag value={i.status} /></td>
              <td>{i.detail ?? '—'}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
    <Raw value={rec} />
  </div>
)
