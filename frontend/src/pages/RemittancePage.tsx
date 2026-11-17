import { useState, type FormEvent, type ReactNode } from 'react'

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
  type Load,
  PageHeader,
  Raw,
  Status,
  Submit,
  useAction,
  useLoad,
} from '../ui/kit'
import { TabBar, todayUtc } from './determine/shared'
import { useRoutedTab } from './routedTab'
import { downloadCsv, downloadJson, Tiles } from './workspace'
import type { PageProps } from './types'
import './obligations.css'

// Remittance (ZTAX-REM-001): paying what an accepted return owes. ZoikoTax
// prepares, approves and records instructions to the customer's own
// provider and never holds the funds. The cell enforces every rule; the page
// offers only the step a payment's status allows.

type Remittance = S<'Remittance'>
type RemStatus = S<'RemittanceStatus'>
type Beneficiary = S<'Beneficiary'>
type Mode = S<'RemittanceMode'>
type Rail = S<'PaymentRail'>
type Reported = S<'ProviderReportedStatus'>
type Evidence = S<'PaymentEvidence'>
type Resolution = S<'PaymentResolution'>

const STATUSES: readonly RemStatus[] = [
  'APPROVAL_REQUIRED', 'APPROVED', 'SUBMITTED', 'ACCEPTED_BY_PROVIDER', 'PROCESSING', 'UNCERTAIN_PAYMENT',
  'SETTLED', 'REJECTED', 'RETURNED', 'CANCELLED',
]
const MODES: readonly Mode[] = [
  'CUSTOMER_EXECUTES', 'BANK_API_INITIATION', 'TREASURY_PLATFORM', 'DIRECT_DEBIT_AUTHORITY', 'LOCAL_PARTNER',
  'MANAGED_ORCHESTRATION', 'OFFLINE_MANUAL',
]
const RAILS: readonly Rail[] = ['SEPA', 'WIRE', 'ACH', 'RTGS', 'LOCAL_TRANSFER', 'PORTAL', 'DIRECT_DEBIT', 'OTHER']
const REPORTED: readonly Reported[] = ['ACCEPTED_BY_PROVIDER', 'PROCESSING', 'SETTLED', 'REJECTED', 'UNCERTAIN_PAYMENT']
const EVIDENCE: readonly Evidence[] = [
  'BANK_STATEMENT', 'AUTHORITY_RECEIPT', 'PROVIDER_SETTLED', 'BENEFICIARY_CREDIT', 'MANUAL_EVIDENCE',
  'PROVIDER_ACCEPTANCE', 'PROCESSING_REFERENCE', 'ACCOUNT_DEBIT', 'NONE',
]
const RESOLUTIONS: readonly Resolution[] = [
  'CONFIRMED_SETTLED', 'CONFIRMED_NOT_SENT', 'CONFIRMED_REJECTED', 'RETURNED', 'DUPLICATE_SUSPECTED', 'MANUAL_INVESTIGATION',
]
// Evidence that proves completion; acceptance, a processing reference or a
// debit do not (ZTAX-REM-REQ-0043).
const SETTLES: readonly string[] = ['BANK_STATEMENT', 'AUTHORITY_RECEIPT', 'PROVIDER_SETTLED', 'BENEFICIARY_CREDIT', 'MANUAL_EVIDENCE']
const SENT: readonly string[] = ['SUBMITTED', 'ACCEPTED_BY_PROVIDER', 'PROCESSING', 'UNCERTAIN_PAYMENT', 'SETTLED', 'RETURNED']
const LIVE_DONE: readonly string[] = ['REJECTED', 'RETURNED', 'CANCELLED']

const pick = <T extends string>(values: readonly T[], v: string, fallback: T): T =>
  (values as readonly string[]).includes(v) ? (v as T) : fallback
const isStatus = (v: string): v is RemStatus => (STATUSES as readonly string[]).includes(v)
const words = (s: string) => s.replaceAll('_', ' ')

type Tab = 'payments' | 'queue' | 'status' | 'settlement' | 'evidence'

const TABS: readonly { value: Tab; label: string }[] = [
  { value: 'payments', label: 'Payment authorisations' },
  { value: 'queue', label: 'Remittance queue' },
  { value: 'status', label: 'Payment status' },
  { value: 'settlement', label: 'Settlement reconciliation' },
  { value: 'evidence', label: 'Evidence' },
]

const SUBTITLE: Record<Tab, string> = {
  payments: 'Payments of accepted returns, overdue first. Each instruction is approved by someone other than its maker, as an exact hash.',
  queue: 'Instructions waiting for approval or for release to your provider, earliest due first.',
  status: 'Instructions released to your provider, and every status the provider reported.',
  settlement: 'Settled and returned payments against the provider’s or authority’s reference that evidences them.',
  evidence: 'One payment’s full record: the approved instruction hash, references and every recorded step, downloadable as JSON.',
}

export const RemittancePage = ({ session, route, onTabChange }: PageProps) => {
  const [tab, setTab] = useRoutedTab(TABS.map((t) => t.value), 'payments', route, onTabChange)
  const [selected, setSelected] = useState<string | null>(null)
  const [evidenceId, setEvidenceId] = useState('')
  const openPayment = (id: string) => {
    setSelected(id)
    setTab('payments')
  }
  const openEvidence = (id: string) => {
    setEvidenceId(id)
    setTab('evidence')
  }
  return (
    <div className="ztax-page">
      <PageHeader title="Remittance" subtitle={SUBTITLE[tab]} />
      <p className="ztax-note">
        Funds posture: <span className="mono">NO_CUSTODY</span>. ZoikoTax prepares and records instructions to your own bank or
        provider. It never holds, pools or moves your money.
      </p>
      <TabBar label="Remittance" tabs={TABS} current={tab} onSelect={setTab} />
      {tab === 'payments' ? <Payments session={session} selected={selected} setSelected={setSelected} />
        : tab === 'queue' ? <QueueTab onOpen={openPayment} />
        : tab === 'status' ? <StatusTab onOpen={openPayment} onEvidence={openEvidence} />
        : tab === 'settlement' ? <SettlementTab onEvidence={openEvidence} />
        : <EvidenceTab id={evidenceId} onPick={setEvidenceId} onOpen={openPayment} />}
    </div>
  )
}

// ---------------------------------------------------------------------------
// remittance queue
// ---------------------------------------------------------------------------

const byDue = (a: Remittance, b: Remittance) => (a.paymentDue < b.paymentDue ? -1 : a.paymentDue > b.paymentDue ? 1 : 0)

const Payee = ({ r }: { r: Remittance }) => (
  <>{r.beneficiary.legalName} <span className="mono muted">{r.authority} · {r.paymentReference}</span></>
)

const Due = ({ r }: { r: Remittance }) => (
  <>{instant(r.paymentDue)} {r.overdue && !['SETTLED', 'CANCELLED'].includes(r.status) ? <span className="ztax-status ztax-status--bad">overdue</span> : null}</>
)

const QueueTab = ({ onOpen }: { onOpen: (id: string) => void }) => {
  const { load, reload } = useLoad((signal) => ztax.listRemittances({ limit: 500 }, signal), [])
  return (
    <Loaded load={load}>
      {(data) => {
        const queue = data.remittances.filter((r) => r.status === 'APPROVAL_REQUIRED' || r.status === 'APPROVED').sort(byDue)
        return (
          <>
            <Tiles tiles={[
              { label: 'Awaiting approval', count: queue.filter((r) => r.status === 'APPROVAL_REQUIRED').length, tone: 'warn' },
              { label: 'Approved, awaiting release', count: queue.filter((r) => r.status === 'APPROVED').length },
              { label: 'Blocked on readiness', count: queue.filter((r) => !r.ready).length, tone: 'warn' },
              { label: 'Overdue', count: queue.filter((r) => r.overdue).length, tone: 'bad' },
            ]} />
            <Card title={`${queue.length} in the queue`} aside={<button type="button" className="ztax-btn-quiet" onClick={reload}>Refresh</button>}>
              {queue.length === 0 ? <Empty>Nothing waits for approval or release.</Empty> : (
                <div style={{ overflowX: 'auto' }}>
                  <table className="ztax-action-table">
                    <thead>
                      <tr><th>Payee</th><th>Period</th><th>Amount</th><th>Executes</th><th>Due</th><th>Status</th><th>Next step</th><th>Action</th></tr>
                    </thead>
                    <tbody>
                      {queue.map((r) => {
                        const blocked = r.readiness.filter((c) => !c.ready)
                        return (
                          <tr key={r.id}>
                            <td style={{ paddingRight: 8 }}><Payee r={r} /></td>
                            <td style={{ paddingRight: 8, whiteSpace: 'nowrap' }}>{civilDate(r.periodStart)} – {civilDate(r.periodEnd)}</td>
                            <td style={{ paddingRight: 8, whiteSpace: 'nowrap' }}><Amount amount={r.amount} currency={r.currency} /></td>
                            <td style={{ paddingRight: 8, whiteSpace: 'nowrap' }}>{civilDate(r.executionDate)} · {words(r.mode)}</td>
                            <td style={{ paddingRight: 8, whiteSpace: 'nowrap' }}><Due r={r} /></td>
                            <td style={{ paddingRight: 8 }}><Status value={r.status} /></td>
                            <td style={{ paddingRight: 8, color: blocked.length > 0 ? '#b91c1c' : '#6b7280' }}>
                              {blocked.length > 0 ? `Blocked: ${blocked.map((c) => words(c.dimension)).join(', ')}`
                                : r.status === 'APPROVAL_REQUIRED' ? 'Approve the exact instruction hash (not its maker)'
                                : 'Release to your provider, then record the submission'}
                            </td>
                            <td><button type="button" className="ztax-table-action-btn" onClick={() => { onOpen(r.id) }}>Open</button></td>
                          </tr>
                        )
                      })}
                    </tbody>
                  </table>
                </div>
              )}
            </Card>
          </>
        )
      }}
    </Loaded>
  )
}

// ---------------------------------------------------------------------------
// payment status
// ---------------------------------------------------------------------------

const IN_FLIGHT: readonly RemStatus[] = ['SUBMITTED', 'ACCEPTED_BY_PROVIDER', 'PROCESSING']
type StatusGroup = 'ALL' | 'IN_FLIGHT' | 'UNCERTAIN_PAYMENT' | 'SETTLED' | 'FAILED'
const GROUPS: readonly StatusGroup[] = ['ALL', 'IN_FLIGHT', 'UNCERTAIN_PAYMENT', 'SETTLED', 'FAILED']
const GROUP_LABEL: Record<StatusGroup, string> = {
  ALL: 'All released', IN_FLIGHT: 'In flight', UNCERTAIN_PAYMENT: 'Uncertain', SETTLED: 'Settled', FAILED: 'Rejected or returned',
}
const groupOf = (s: RemStatus): Exclude<StatusGroup, 'ALL'> | null =>
  IN_FLIGHT.includes(s) ? 'IN_FLIGHT' : s === 'UNCERTAIN_PAYMENT' ? 'UNCERTAIN_PAYMENT' : s === 'SETTLED' ? 'SETTLED'
    : s === 'REJECTED' || s === 'RETURNED' ? 'FAILED' : null

const stepText = (s: S<'RemittanceStep'>) =>
  [s.evidence === undefined ? null : words(s.evidence), s.resolution === undefined ? null : words(s.resolution),
    s.providerReference === undefined ? null : `ref ${s.providerReference}`, s.note ?? null].filter((x) => x !== null).join(' · ')

const StatusTab = ({ onOpen, onEvidence }: { onOpen: (id: string) => void; onEvidence: (id: string) => void }) => {
  const [group, setGroup] = useState<StatusGroup>('ALL')
  const { load, reload } = useLoad((signal) => ztax.listRemittances({ limit: 500 }, signal), [])
  return (
    <Loaded load={load}>
      {(data) => {
        // A REJECTED instruction that never left (rejected before release) has no submission step; it still belongs here.
        const released = data.remittances.filter((r) => groupOf(r.status) !== null).sort(byDue)
        const shown = released.filter((r) => group === 'ALL' || groupOf(r.status) === group)
        return (
          <>
            <Tiles tiles={GROUPS.filter((g) => g !== 'ALL').map((g) => ({
              label: GROUP_LABEL[g], count: released.filter((r) => groupOf(r.status) === g).length,
              tone: g === 'SETTLED' ? 'ok' as const : g === 'IN_FLIGHT' ? 'neutral' as const : 'bad' as const,
            }))} />
            <Card
              title={`${shown.length} of ${released.length} released payments`}
              aside={
                <div className="ztax-filter-bar">
                  <Field label="Show">
                    <select value={group} onChange={(e) => { setGroup(pick(GROUPS, e.target.value, 'ALL')) }}>
                      {GROUPS.map((g) => <option key={g} value={g}>{GROUP_LABEL[g]}</option>)}
                    </select>
                  </Field>
                  <button type="button" className="ztax-btn-quiet" onClick={reload}>Refresh</button>
                </div>
              }
            >
              {shown.length === 0 ? <Empty>{released.length === 0 ? 'No payment has been released to a provider yet.' : 'No payment matches this filter.'}</Empty> : (
                <div style={{ overflowX: 'auto' }}>
                  <table className="ztax-action-table">
                    <thead>
                      <tr><th>Payee</th><th>Amount</th><th>Submitted</th><th>Status</th><th>Provider history</th><th>Action</th></tr>
                    </thead>
                    <tbody>
                      {shown.map((r) => {
                        const submitted = r.history.find((s) => s.kind === 'SUBMITTED')
                        const reports = r.history.filter((s) => s.kind === 'PROVIDER_STATUS' || s.kind === 'UNCERTAIN_RESOLVED' || s.kind === 'RETURNED')
                        return (
                          <tr key={r.id} style={{ verticalAlign: 'top' }}>
                            <td style={{ paddingRight: 8 }}><Payee r={r} /></td>
                            <td style={{ paddingRight: 8, whiteSpace: 'nowrap' }}><Amount amount={r.amount} currency={r.currency} /></td>
                            <td style={{ paddingRight: 8, whiteSpace: 'nowrap' }}>{submitted === undefined ? '—' : instant(submitted.recordedAt)}</td>
                            <td style={{ paddingRight: 8 }}><Status value={r.status} /></td>
                            <td style={{ paddingRight: 8 }}>
                              {reports.length === 0 ? <span className="muted">no provider status recorded yet</span> : (
                                <ol style={{ margin: 0, paddingLeft: 16 }}>
                                  {reports.map((s) => (
                                    <li key={s.seq}>
                                      <Status value={s.status} /> <span className="muted">{instant(s.recordedAt)}</span>
                                      {stepText(s) === '' ? null : <div className="mono" style={{ fontSize: 11 }}>{stepText(s)}</div>}
                                    </li>
                                  ))}
                                </ol>
                              )}
                            </td>
                            <td style={{ whiteSpace: 'nowrap' }}>
                              <button type="button" className="ztax-table-action-btn" onClick={() => { onOpen(r.id) }}>Open</button>{' '}
                              <button type="button" className="ztax-table-action-btn" onClick={() => { onEvidence(r.id) }}>Evidence</button>
                            </td>
                          </tr>
                        )
                      })}
                    </tbody>
                  </table>
                </div>
              )}
            </Card>
          </>
        )
      }}
    </Loaded>
  )
}

// ---------------------------------------------------------------------------
// settlement reconciliation
// ---------------------------------------------------------------------------

const SettlementTab = ({ onEvidence }: { onEvidence: (id: string) => void }) => {
  const { load, reload } = useLoad((signal) => ztax.listRemittances({ limit: 500 }, signal), [])
  return (
    <Loaded load={load}>
      {(data) => {
        const rows = data.remittances.filter((r) => r.status === 'SETTLED' || r.status === 'RETURNED').sort(byDue).map((r) => {
          const settled = [...r.history].reverse().find((s) => s.status === 'SETTLED')
          const returned = [...r.history].reverse().find((s) => s.kind === 'RETURNED')
          return { r, settled, returned, hasRef: settled?.providerReference !== undefined }
        })
        const csv = () => {
          downloadCsv('settlement-reconciliation', ['Payee', 'Authority', 'Payment reference', 'Period start', 'Period end', 'Amount',
            'Currency', 'Status', 'Settled at', 'Settlement evidence', 'Provider reference', 'Returned at', 'Return reference',
            'Instruction hash'], rows.map(({ r, settled, returned }) => [r.beneficiary.legalName, r.authority, r.paymentReference,
            r.periodStart, r.periodEnd, r.amount, r.currency, r.status, settled?.recordedAt ?? '', settled?.evidence ?? '',
            settled?.providerReference ?? '', returned?.recordedAt ?? '', returned?.providerReference ?? '', r.instructionHash]))
        }
        return (
          <>
            <Tiles tiles={[
              { label: 'Settled', count: rows.filter((x) => x.r.status === 'SETTLED').length, tone: 'ok' },
              { label: 'Settled with a reference', count: rows.filter((x) => x.r.status === 'SETTLED' && x.hasRef).length, tone: 'ok' },
              { label: 'Settled without a reference', count: rows.filter((x) => x.r.status === 'SETTLED' && !x.hasRef).length, tone: 'warn',
                note: 'Chase the provider or authority for one' },
              { label: 'Returned after settlement', count: rows.filter((x) => x.r.status === 'RETURNED').length, tone: 'bad' },
            ]} />
            <Card
              title={`${rows.length} settled or returned`}
              aside={
                <div className="ztax-filter-bar">
                  <button type="button" className="ztax-btn-quiet" onClick={reload}>Refresh</button>
                  <button type="button" className="ztax-btn-quiet" disabled={rows.length === 0} onClick={csv}>Download CSV</button>
                </div>
              }
            >
              <p className="ztax-note">
                A payment is settled only on completion evidence — a bank statement, an authority receipt, provider settlement or a beneficiary
                credit. This view matches each settlement to the reference that evidences it; amounts are shown as recorded, never recomputed.
              </p>
              {rows.length === 0 ? <Empty>No payment has settled yet.</Empty> : (
                <div style={{ overflowX: 'auto' }}>
                  <table className="ztax-action-table">
                    <thead>
                      <tr><th>Payee</th><th>Period</th><th>Amount</th><th>Settled</th><th>Evidence</th><th>Reference</th><th>Match</th><th>Return</th><th>Action</th></tr>
                    </thead>
                    <tbody>
                      {rows.map(({ r, settled, returned, hasRef }) => (
                        <tr key={r.id}>
                          <td style={{ paddingRight: 8 }}><Payee r={r} /></td>
                          <td style={{ paddingRight: 8, whiteSpace: 'nowrap' }}>{civilDate(r.periodStart)} – {civilDate(r.periodEnd)}</td>
                          <td style={{ paddingRight: 8, whiteSpace: 'nowrap' }}><Amount amount={r.amount} currency={r.currency} /></td>
                          <td style={{ paddingRight: 8, whiteSpace: 'nowrap' }}>{settled === undefined ? '—' : instant(settled.recordedAt)}</td>
                          <td style={{ paddingRight: 8 }}>{settled?.evidence === undefined ? '—' : words(settled.evidence)}</td>
                          <td className="mono" style={{ paddingRight: 8 }}>{settled?.providerReference ?? '—'}</td>
                          <td style={{ paddingRight: 8 }}>
                            {r.status === 'RETURNED' ? <Status value="RETURNED" /> : hasRef ? <Status value="MATCHED" /> : <Status value="MISSING" />}
                          </td>
                          <td style={{ paddingRight: 8 }}>
                            {returned === undefined ? <span className="muted">—</span> : (
                              <>{instant(returned.recordedAt)}{returned.providerReference === undefined ? null : <span className="mono muted"> · ref {returned.providerReference}</span>}</>
                            )}
                          </td>
                          <td><button type="button" className="ztax-table-action-btn" onClick={() => { onEvidence(r.id) }}>Evidence</button></td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </Card>
          </>
        )
      }}
    </Loaded>
  )
}

// ---------------------------------------------------------------------------
// evidence
// ---------------------------------------------------------------------------

const EvidenceTab = ({ id, onPick, onOpen }: { id: string; onPick: (id: string) => void; onOpen: (id: string) => void }) => {
  const list = useLoad((signal) => ztax.listRemittances({ limit: 500 }, signal), [])
  return (
    <Loaded load={list.load}>
      {(data) => {
        const sorted = [...data.remittances].sort(byDue)
        const chosen = id || (sorted[0]?.id ?? '')
        return (
          <>
            <Card
              title="Payment"
              aside={
                <div className="ztax-filter-bar">
                  <Field label="Payment">
                    <select value={chosen} onChange={(e) => { onPick(e.target.value) }}>
                      {sorted.map((r) => (
                        <option key={r.id} value={r.id}>
                          {r.beneficiary.legalName} · {civilDate(r.periodStart)} – {civilDate(r.periodEnd)} · {words(r.status)}
                        </option>
                      ))}
                    </select>
                  </Field>
                </div>
              }
            >
              {sorted.length === 0 ? <Empty>No payment instruction exists yet.</Empty> : null}
            </Card>
            {chosen === '' ? null : <EvidenceRecord key={chosen} id={chosen} onOpen={onOpen} />}
          </>
        )
      }}
    </Loaded>
  )
}

const EvidenceRecord = ({ id, onOpen }: { id: string; onOpen: (id: string) => void }) => {
  const { load } = useLoad((signal) => ztax.getRemittance(id, signal), [id])
  return (
    <Loaded load={load}>
      {(r) => (
        <>
          <Columns>
            <Card
              title="Instruction"
              aside={
                <div className="ztax-filter-bar">
                  <button type="button" className="ztax-btn-quiet" onClick={() => { onOpen(r.id) }}>Open payment</button>
                  <button type="button" className="ztax-btn-quiet"
                    onClick={() => { downloadJson(`remittance-evidence-${r.id}`, r) }}>Download JSON</button>
                </div>
              }
            >
              <dl className="ztax-kv">
                <dt>Payment</dt><dd className="mono">{r.id}</dd>
                <dt>Status</dt><dd><Status value={r.status} /></dd>
                <dt>Amount</dt><dd><Amount amount={r.amount} currency={r.currency} /></dd>
                <dt>Obligation</dt><dd className="mono">{r.obligationId} <Status value={r.obligationStatus} /></dd>
                <dt>Legal entity</dt><dd className="mono">{r.legalEntityId}</dd>
                <dt>Payee</dt><dd>{r.beneficiary.legalName} <span className="mono muted">{r.beneficiary.id}</span> <Status value={r.beneficiary.state} /></dd>
                <dt>Payee source</dt><dd>{r.beneficiary.source}</dd>
                <dt>Funding</dt><dd className="mono">{r.fundingRef}</dd>
                <dt>Payment reference</dt><dd className="mono">{r.paymentReference}</dd>
                <dt>Executes</dt><dd>{civilDate(r.executionDate)} · {words(r.mode)}</dd>
                <dt>Funds posture</dt><dd className="mono">{r.fundsPosture}</dd>
                <dt>Instruction hash</dt><dd className="mono">{r.instructionHash}</dd>
                <dt>Replaces</dt><dd className="mono">{r.supersedes ?? '— first instruction'}</dd>
                <dt>Made</dt><dd>{instant(r.createdAt)} by <Id value={r.createdBy} /></dd>
              </dl>
            </Card>
            <Card title="Payee verification">
              <table className="ztax-action-table">
                <thead><tr><th>#</th><th>Event</th><th>Evidence</th><th>By</th><th>When</th></tr></thead>
                <tbody>
                  {r.beneficiary.history.map((e) => (
                    <tr key={e.seq}>
                      <td className="mono" style={{ paddingRight: 8 }}>{e.seq}</td>
                      <td style={{ paddingRight: 8 }}><Status value={e.kind} /></td>
                      <td style={{ paddingRight: 8 }}>{e.evidence ?? '—'}</td>
                      <td style={{ paddingRight: 8 }}><Id value={e.recordedBy} /></td>
                      <td style={{ whiteSpace: 'nowrap' }}>{instant(e.recordedAt)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </Card>
          </Columns>
          <Card title={`Recorded steps · ${r.history.length}`}>
            <div style={{ overflowX: 'auto' }}>
              <table className="ztax-action-table">
                <thead>
                  <tr><th>#</th><th>Step</th><th>Then</th><th>Instruction hash</th><th>Submission</th><th>Evidence · reference</th><th>Raw</th><th>By</th><th>When</th></tr>
                </thead>
                <tbody>
                  {r.history.map((s) => (
                    <tr key={s.seq} style={{ verticalAlign: 'top' }}>
                      <td className="mono" style={{ paddingRight: 8 }}>{s.seq}</td>
                      <td style={{ paddingRight: 8 }}>{words(s.kind)}</td>
                      <td style={{ paddingRight: 8 }}><Status value={s.status} /></td>
                      <td style={{ paddingRight: 8 }}><Id value={s.instructionHash} /></td>
                      <td style={{ paddingRight: 8 }}><Id value={s.submissionId} /></td>
                      <td style={{ paddingRight: 8 }}>{stepText(s) || '—'}</td>
                      <td className="mono" style={{ paddingRight: 8, maxWidth: 260, overflowWrap: 'anywhere' }}>
                        {s.raw === undefined ? '—' : s.raw.length > 160 ? <span title={s.raw}>{s.raw.slice(0, 160)}…</span> : s.raw}
                      </td>
                      <td style={{ paddingRight: 8 }}><Id value={s.recordedBy} /></td>
                      <td style={{ whiteSpace: 'nowrap' }}>{instant(s.recordedAt)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </Card>
        </>
      )}
    </Loaded>
  )
}

// ---------------------------------------------------------------------------
// payment authorisations
// ---------------------------------------------------------------------------

const Payments = ({ session, selected, setSelected }: {
  session: PageProps['session']
  selected: string | null
  setSelected: (id: string | null) => void
}) => {
  const [status, setStatus] = useState<RemStatus | 'ALL'>('ALL')
  const { load, reload } = useLoad(
    (signal) => ztax.listRemittances(status === 'ALL' ? { limit: 500 } : { status, limit: 500 }, signal),
    [status],
  )
  const payees = useLoad((signal) => ztax.listBeneficiaries(signal), [])
  const roles = session.user.roles
  const canOperate = roles.includes('OPERATOR')
  const canApprove = canOperate || roles.includes('ANALYST')
  const canKeepPayees = canOperate || roles.includes('ADMIN')
  const canCheckPayees = canKeepPayees || roles.includes('ANALYST')
  const refresh = () => {
    reload()
    payees.reload()
  }

  return (
    <>
      <Card
        title="Payments"
        aside={
          <div className="ztax-filter-bar">
            <Field label="Status">
              <select value={status} onChange={(e) => { setStatus(isStatus(e.target.value) ? e.target.value : 'ALL') }}>
                <option value="ALL">All</option>
                {STATUSES.map((s) => <option key={s} value={s}>{words(s)}</option>)}
              </select>
            </Field>
            <button type="button" className="ztax-btn-quiet" onClick={refresh}>Refresh</button>
          </div>
        }
      >
        <Loaded load={load}>
          {(data) =>
            data.remittances.length === 0 ? (
              <Empty>
                {status === 'ALL' ? 'No payments yet. Make one below for a return the authority accepted.' : `No payment is ${words(status)}.`}
              </Empty>
            ) : (
              <div style={{ overflowX: 'auto' }}>
                <table className="ztax-action-table">
                  <thead>
                    <tr>
                      <th>Period</th>
                      <th>Amount</th>
                      <th>Payee</th>
                      <th>Due</th>
                      <th>Status</th>
                      <th>Ready</th>
                      <th>Action</th>
                    </tr>
                  </thead>
                  <tbody>
                    {data.remittances.map((r) => (
                      <tr key={r.id} className={r.id === selected ? 'ztax-row-selected' : undefined}>
                        <td style={{ paddingRight: 8, whiteSpace: 'nowrap' }}>{civilDate(r.periodStart)} – {civilDate(r.periodEnd)}</td>
                        <td style={{ paddingRight: 8, whiteSpace: 'nowrap' }}><Amount amount={r.amount} currency={r.currency} /></td>
                        <td style={{ paddingRight: 8 }}>{r.beneficiary.legalName} <span className="mono muted">{r.beneficiary.authority}</span></td>
                        <td style={{ paddingRight: 8, whiteSpace: 'nowrap' }}>
                          {instant(r.paymentDue)} {r.overdue ? <span className="ztax-status ztax-status--bad">overdue</span> : null}
                        </td>
                        <td style={{ paddingRight: 8 }}><Status value={r.status} /></td>
                        <td style={{ paddingRight: 8 }}>
                          {LIVE_DONE.includes(r.status) || r.status === 'SETTLED' ? <span className="muted">—</span>
                            : r.ready ? <Status value="READY" /> : <span className="muted">blocked</span>}
                        </td>
                        <td>
                          <button type="button" className="ztax-table-action-btn" aria-pressed={r.id === selected}
                            onClick={() => { setSelected(r.id) }}>Open</button>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )
          }
        </Loaded>
      </Card>

      {selected === null ? null : (
        <PaymentDetail key={selected} id={selected} canOperate={canOperate} canApprove={canApprove}
          onChanged={reload} onClose={() => { setSelected(null) }} />
      )}

      {canOperate ? (
        <MakePayment
          payees={payees.load.status === 'ready' ? payees.load.data.beneficiaries : []}
          live={load.status === 'ready' ? load.data.remittances.filter((r) => !LIVE_DONE.includes(r.status)).map((r) => `${r.periodStart}|${r.authority}`) : []}
          onCreated={(r) => { setSelected(r.id); reload() }}
        />
      ) : null}

      <Payees load={payees.load} canKeep={canKeepPayees} canCheck={canCheckPayees} onChanged={payees.reload} />
    </>
  )
}

// ---------------------------------------------------------------------------
// make a payment
// ---------------------------------------------------------------------------

const MakePayment = ({ payees, live, onCreated }: {
  payees: readonly Beneficiary[]
  live: readonly string[]
  onCreated: (r: Remittance) => void
}) => {
  const obligations = useLoad((signal) => ztax.listObligations({ limit: 500 }, signal), [])
  const [obligationId, setObligationId] = useState('')
  const [beneficiaryId, setBeneficiaryId] = useState('')
  const [fundingRef, setFundingRef] = useState('vault://funding/')
  const [reference, setReference] = useState('')
  const [date, setDate] = useState(todayUtc())
  const [mode, setMode] = useState<Mode>('CUSTOMER_EXECUTES')
  const action = useAction<Remittance>()
  const usable = payees.filter((b) => b.state === 'VERIFIED' || b.state === 'UNVERIFIED' || b.state === 'CHANGE_PENDING')

  return (
    <Card title="Make a payment">
      <Loaded load={obligations.load}>
        {(o) => {
          const payable = o.obligations.filter((x) =>
            (x.status === 'ACCEPTED' || x.status === 'PAYMENT_DUE') && !live.includes(`${x.periodStart}|${x.authority}`))
          if (payable.length === 0) {
            return <Empty>No accepted return is waiting for payment. A return is payable once the authority accepts it under Compliance.</Empty>
          }
          if (usable.length === 0) {
            return <Empty>No payee is on record. Add the authority’s payee details below first.</Empty>
          }
          const submit = (e: FormEvent) => {
            e.preventDefault()
            void action.run(() => ztax.createRemittance({
              obligationId: obligationId || (payable[0]?.id ?? ''), beneficiaryId: beneficiaryId || (usable[0]?.id ?? ''),
              fundingRef: fundingRef.trim(), paymentReference: reference.trim(), executionDate: date, mode,
            })).then((r) => { if (r !== null) onCreated(r) })
          }
          return (
            <form className="ztax-form" onSubmit={submit}>
              <Field label="Return" help="The liability is the obligation’s assessment; it is not typed here.">
                <select value={obligationId || (payable[0]?.id ?? '')} onChange={(e) => { setObligationId(e.target.value) }}>
                  {payable.map((x) => (
                    <option key={x.id} value={x.id}>
                      {x.type} · {civilDate(x.periodStart)} – {civilDate(x.periodEnd)} · {x.assessedAmount ?? '—'} {x.currency ?? ''}
                    </option>
                  ))}
                </select>
              </Field>
              <Field label="Payee version" help="Only a verified version can be approved.">
                <select value={beneficiaryId || (usable[0]?.id ?? '')} onChange={(e) => { setBeneficiaryId(e.target.value) }}>
                  {usable.map((b) => <option key={b.id} value={b.id}>{b.legalName} · {b.authority} · {words(b.state)}</option>)}
                </select>
              </Field>
              <Field label="Funding account reference" help="Your own account at your provider, as a vault or token reference — never the account number.">
                <input value={fundingRef} required onChange={(e) => { setFundingRef(e.target.value) }} />
              </Field>
              <Field label="Authority payment reference">
                <input value={reference} required maxLength={140} onChange={(e) => { setReference(e.target.value) }} />
              </Field>
              <Field label="Execution date">
                <input type="date" value={date} required onChange={(e) => { setDate(e.target.value) }} />
              </Field>
              <Field label="Who executes">
                <select value={mode} onChange={(e) => { setMode(pick(MODES, e.target.value, 'CUSTOMER_EXECUTES')) }}>
                  {MODES.map((m) => <option key={m} value={m}>{words(m)}</option>)}
                </select>
              </Field>
              <div className="ztax-form-actions"><Submit busy={action.busy}>Make instruction</Submit></div>
              <div style={{ gridColumn: '1 / -1' }}>{action.error === null ? null : <ErrorNote error={action.error} />}</div>
            </form>
          )
        }}
      </Loaded>
    </Card>
  )
}

// ---------------------------------------------------------------------------
// one payment
// ---------------------------------------------------------------------------

const PaymentDetail = ({ id, canOperate, canApprove, onChanged, onClose }: {
  id: string
  canOperate: boolean
  canApprove: boolean
  onChanged: () => void
  onClose: () => void
}) => {
  const { load, reload } = useLoad((signal) => ztax.getRemittance(id, signal), [id])
  return (
    <Loaded load={load}>
      {(r) => (
        <>
          <Columns>
            <Card title={`Payment · ${civilDate(r.periodStart)} – ${civilDate(r.periodEnd)}`}
              aside={<button type="button" className="ztax-btn-quiet" onClick={onClose}>Close</button>}>
              <dl className="ztax-kv">
                <dt>Payment</dt><dd><Id value={r.id} /></dd>
                <dt>Status</dt><dd><Status value={r.status} /></dd>
                <dt>Amount</dt><dd><Amount amount={r.amount} currency={r.currency} /></dd>
                <dt>Obligation</dt><dd><Id value={r.obligationId} /> <Status value={r.obligationStatus} /></dd>
                <dt>Payee</dt><dd>{r.beneficiary.legalName} <Status value={r.beneficiary.state} /></dd>
                <dt>Funding</dt><dd className="mono">{r.fundingRef}</dd>
                <dt>Reference</dt><dd className="mono">{r.paymentReference}</dd>
                <dt>Executes</dt><dd>{civilDate(r.executionDate)} · {words(r.mode)}</dd>
                <dt>Due</dt><dd>{instant(r.paymentDue)} {r.overdue ? <span className="ztax-status ztax-status--bad">overdue</span> : null}</dd>
                <dt>Instruction hash</dt><dd className="mono">{r.instructionHash}</dd>
                <dt>Replaces</dt><dd>{r.supersedes === undefined ? <span className="muted">— first instruction</span> : <Id value={r.supersedes} />}</dd>
              </dl>
              <Raw value={r} />
            </Card>
            <Card title="Readiness">
              <table className="ztax-action-table">
                <tbody>
                  {r.readiness.map((c) => (
                    <tr key={c.dimension}>
                      <td style={{ paddingRight: 8 }}>{c.ready ? <Status value="READY" /> : <Status value="DATA_REQUIRED" />}</td>
                      <td style={{ paddingRight: 8 }} className="mono">{words(c.dimension)}</td>
                      <td>{c.detail}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </Card>
          </Columns>
          <Columns>
            <Card title="Next step">
              <NextStep r={r} canOperate={canOperate} canApprove={canApprove} onDone={() => { reload(); onChanged() }} />
            </Card>
            <Card title="History">
              <table className="ztax-action-table">
                <thead><tr><th>#</th><th>Step</th><th>Then</th><th>Detail</th><th>By</th><th>When</th></tr></thead>
                <tbody>
                  {[...r.history].reverse().map((s) => (
                    <tr key={s.seq}>
                      <td className="mono" style={{ paddingRight: 8 }}>{s.seq}</td>
                      <td style={{ paddingRight: 8 }}>{words(s.kind)}</td>
                      <td style={{ paddingRight: 8 }}><Status value={s.status} /></td>
                      <td style={{ paddingRight: 8 }}>
                        {[s.evidence === undefined ? null : words(s.evidence), s.resolution === undefined ? null : words(s.resolution),
                          s.providerReference === undefined ? null : `ref ${s.providerReference}`, s.note ?? null, s.raw ?? null]
                          .filter((x) => x !== null).join(' · ')}
                      </td>
                      <td style={{ paddingRight: 8 }}><Id value={s.recordedBy} /></td>
                      <td style={{ whiteSpace: 'nowrap' }}>{instant(s.recordedAt)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </Card>
          </Columns>
        </>
      )}
    </Loaded>
  )
}

const StepForm = ({ label, busy, onSubmit, children, help }: {
  label: string
  busy: boolean
  onSubmit: () => void
  children?: ReactNode
  help?: string
}) => (
  <form className="ztax-form" style={{ marginBottom: 16 }} onSubmit={(e) => { e.preventDefault(); onSubmit() }}>
    {children}
    <div className="ztax-form-actions"><Submit busy={busy}>{label}</Submit></div>
    {help === undefined ? null : <div style={{ gridColumn: '1 / -1' }}><p className="ztax-note">{help}</p></div>}
  </form>
)

const NextStep = ({ r, canOperate, canApprove, onDone }: {
  r: Remittance
  canOperate: boolean
  canApprove: boolean
  onDone: () => void
}) => {
  const action = useAction<Remittance>()
  const [reported, setReported] = useState<Reported>('SETTLED')
  const [evidence, setEvidence] = useState<Evidence>('BANK_STATEMENT')
  const [resolution, setResolution] = useState<Resolution>('CONFIRMED_SETTLED')
  const [ref, setRef] = useState('')
  const [raw, setRaw] = useState('')
  const [reason, setReason] = useState('')
  const run = (call: () => ReturnType<typeof ztax.getRemittance>) => {
    void action.run(call).then((next) => {
      if (next !== null) {
        setRef('')
        setRaw('')
        setReason('')
        onDone()
      }
    })
  }
  const optRef = () => (ref.trim() === '' ? {} : { providerReference: ref.trim() })
  const refField = (required: boolean) => (
    <Field label={required ? 'Provider / authority reference (required)' : 'Provider / authority reference'}>
      <input value={ref} maxLength={200} onChange={(e) => { setRef(e.target.value) }} />
    </Field>
  )
  const rawField = (label: string) => (
    <Field label={label}>
      <textarea value={raw} rows={3} maxLength={20000} required onChange={(e) => { setRaw(e.target.value) }} />
    </Field>
  )
  const forms: ReactNode[] = []

  if (r.status === 'APPROVAL_REQUIRED') {
    forms.push(canApprove ? (
      <StepForm key="approve" label="Approve this exact instruction" busy={action.busy}
        onSubmit={() => { run(() => ztax.approveRemittance(r.id, { instructionHash: r.instructionHash })) }}
        help="Binds the instruction hash. The maker and the payee version’s author cannot approve." />
    ) : <p key="a" className="ztax-note">Approving needs the OPERATOR or ANALYST role.</p>)
  }
  if (canOperate && r.status === 'APPROVED') {
    forms.push(
      <StepForm key="submit" label="Record submission" busy={action.busy}
        onSubmit={() => { run(() => ztax.submitRemittance(r.id, {})) }}
        help="Record that the approved instruction was released to your provider. Submitted is not settled." />,
    )
  }
  if (canOperate && ['SUBMITTED', 'ACCEPTED_BY_PROVIDER', 'PROCESSING'].includes(r.status)) {
    forms.push(
      <StepForm key="status" label="Record provider status" busy={action.busy}
        onSubmit={() => { run(() => ztax.recordRemittanceStatus(r.id, { status: reported, evidence, raw, ...optRef() })) }}
        help={reported === 'SETTLED' && !SETTLES.includes(evidence)
          ? `${words(evidence)} is not settlement evidence; the cell will refuse it.`
          : 'If you cannot tell whether the payment went out, record UNCERTAIN PAYMENT. It is never resent blindly.'}>
        <Field label="Provider says">
          <select value={reported} onChange={(e) => { setReported(pick(REPORTED, e.target.value, 'SETTLED')) }}>
            {REPORTED.map((s) => <option key={s} value={s}>{words(s)}</option>)}
          </select>
        </Field>
        <Field label="Evidence">
          <select value={evidence} onChange={(e) => { setEvidence(pick(EVIDENCE, e.target.value, 'BANK_STATEMENT')) }}>
            {EVIDENCE.map((s) => <option key={s} value={s}>{words(s)}</option>)}
          </select>
        </Field>
        {refField(reported === 'SETTLED')}
        {rawField('Provider response, as received (required)')}
      </StepForm>,
    )
  }
  if (canOperate && r.status === 'UNCERTAIN_PAYMENT') {
    forms.push(
      <StepForm key="resolve" label="Record the resolution" busy={action.busy}
        onSubmit={() => { run(() => ztax.resolveRemittance(r.id, { resolution, evidence, raw, ...optRef() })) }}
        help="Confirmed-not-sent sends the payment back for a new approval; a second payment is never made on the old one.">
        <Field label="Resolution">
          <select value={resolution} onChange={(e) => { setResolution(pick(RESOLUTIONS, e.target.value, 'CONFIRMED_SETTLED')) }}>
            {RESOLUTIONS.map((s) => <option key={s} value={s}>{words(s)}</option>)}
          </select>
        </Field>
        <Field label="Evidence">
          <select value={evidence} onChange={(e) => { setEvidence(pick(EVIDENCE, e.target.value, 'BANK_STATEMENT')) }}>
            {EVIDENCE.map((s) => <option key={s} value={s}>{words(s)}</option>)}
          </select>
        </Field>
        {refField(resolution === 'CONFIRMED_SETTLED')}
        {rawField('How it was established (required)')}
      </StepForm>,
    )
  }
  if (canOperate && r.status === 'SETTLED') {
    forms.push(
      <StepForm key="return" label="Record a return" busy={action.busy}
        onSubmit={() => { run(() => ztax.returnRemittance(r.id, { raw, ...optRef() })) }}
        help="Only if the bank or authority sent the money back. The settlement stays on record.">
        {refField(false)}
        {rawField('Return notice, as received (required)')}
      </StepForm>,
    )
  }
  if (canOperate && !SENT.includes(r.status) && !LIVE_DONE.includes(r.status)) {
    forms.push(
      <StepForm key="cancel" label="Cancel instruction" busy={action.busy}
        onSubmit={() => { run(() => ztax.cancelRemittance(r.id, { reason: reason.trim() })) }}
        help="Only before it is sent. A replacement can then be made, linked to this one.">
        <Field label="Reason (required)">
          <input value={reason} maxLength={2000} required onChange={(e) => { setReason(e.target.value) }} />
        </Field>
      </StepForm>,
    )
  }
  return (
    <>
      {forms.length === 0 ? (
        <Empty>{r.status === 'SETTLED' ? 'Settled on evidence.' : LIVE_DONE.includes(r.status) ? `This instruction is ${words(r.status)}; make a new one if payment is still owed.` : 'Nothing your roles can do on this payment now.'}</Empty>
      ) : forms}
      {action.error === null ? null : <ErrorNote error={action.error} />}
    </>
  )
}

// ---------------------------------------------------------------------------
// payees
// ---------------------------------------------------------------------------

const Payees = ({ load, canKeep, canCheck, onChanged }: {
  load: Load<S<'BeneficiaryList'>>
  canKeep: boolean
  canCheck: boolean
  onChanged: () => void
}) => {
  const action = useAction<Beneficiary>()
  const [note, setNote] = useState<Record<string, string>>({})
  const [form, setForm] = useState({ country: 'DE', authority: 'DE-ELSTER', legalName: '', rail: 'SEPA' as Rail,
    accountRef: 'vault://payees/', referenceRule: '', currencies: 'EUR', source: '', supersedes: '' })
  const set = (k: keyof typeof form) => (v: string) => { setForm({ ...form, [k]: v }) }
  const done = (b: Beneficiary | null) => {
    if (b !== null) {
      setNote({})
      onChanged()
    }
  }
  const create = (e: FormEvent) => {
    e.preventDefault()
    void action.run(() => ztax.createBeneficiary({
      country: form.country.trim(), authority: form.authority.trim(), legalName: form.legalName.trim(), rail: form.rail,
      accountRef: form.accountRef.trim(), currencies: form.currencies.split(',').map((c) => c.trim()).filter((c) => c !== ''),
      source: form.source.trim(),
      ...(form.referenceRule.trim() === '' ? {} : { referenceRule: form.referenceRule.trim() }),
      ...(form.supersedes === '' ? {} : { supersedes: form.supersedes }),
    })).then(done)
  }

  return (
    <Card title="Payees">
      <Loaded load={load}>
        {(data) => (
          <>
            {data.beneficiaries.length === 0 ? <Empty>No payee details on record.</Empty> : (
              <div style={{ overflowX: 'auto' }}>
                <table className="ztax-action-table">
                  <thead><tr><th>Payee</th><th>Rail</th><th>Account</th><th>State</th><th>Entered</th><th>Action</th></tr></thead>
                  <tbody>
                    {data.beneficiaries.map((b) => (
                      <tr key={b.id}>
                        <td style={{ paddingRight: 8 }}>{b.legalName} <span className="mono muted">{b.country} · {b.authority}</span></td>
                        <td style={{ paddingRight: 8 }}>{words(b.rail)} · {b.currencies.join(', ')}</td>
                        <td style={{ paddingRight: 8 }} className="mono">{b.accountRef}</td>
                        <td style={{ paddingRight: 8 }}><Status value={b.state} /></td>
                        <td style={{ paddingRight: 8 }}><Id value={b.createdBy} /></td>
                        <td>
                          {canCheck && (b.state === 'UNVERIFIED' || b.state === 'CHANGE_PENDING' || b.state === 'VERIFIED') ? (
                            <span style={{ display: 'flex', gap: 6, alignItems: 'center' }}>
                              <input placeholder={b.state === 'VERIFIED' ? 'Reason' : 'How verified'} value={note[b.id] ?? ''}
                                onChange={(e) => { setNote({ ...note, [b.id]: e.target.value }) }} />
                              {b.state === 'VERIFIED' ? (
                                <button type="button" className="ztax-table-action-btn" disabled={action.busy || (note[b.id] ?? '').trim() === ''}
                                  onClick={() => { void action.run(() => ztax.suspendBeneficiary(b.id, { reason: (note[b.id] ?? '').trim() })).then(done) }}>
                                  Suspend
                                </button>
                              ) : (
                                <button type="button" className="ztax-table-action-btn" disabled={action.busy || (note[b.id] ?? '').trim() === ''}
                                  onClick={() => { void action.run(() => ztax.verifyBeneficiary(b.id, { evidence: (note[b.id] ?? '').trim() })).then(done) }}>
                                  Verify
                                </button>
                              )}
                            </span>
                          ) : <span className="muted">—</span>}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
            {canKeep ? (
              <>
                <h3 className="ztax-card-title" style={{ marginTop: 16 }}>Add payee details</h3>
                <form className="ztax-form" onSubmit={create}>
                  <Field label="Country"><input value={form.country} maxLength={2} required onChange={(e) => { set('country')(e.target.value) }} /></Field>
                  <Field label="Authority"><input value={form.authority} required onChange={(e) => { set('authority')(e.target.value) }} /></Field>
                  <Field label="Legal name"><input value={form.legalName} required onChange={(e) => { set('legalName')(e.target.value) }} /></Field>
                  <Field label="Rail">
                    <select value={form.rail} onChange={(e) => { set('rail')(pick(RAILS, e.target.value, 'SEPA')) }}>
                      {RAILS.map((x) => <option key={x} value={x}>{words(x)}</option>)}
                    </select>
                  </Field>
                  <Field label="Account reference" help="A vault or token reference. Never type an IBAN or account number here.">
                    <input value={form.accountRef} required onChange={(e) => { set('accountRef')(e.target.value) }} />
                  </Field>
                  <Field label="Reference format (optional)" help="A pattern, e.g. [0-9]{3}/[0-9]{3}/[0-9]{5}.">
                    <input value={form.referenceRule} onChange={(e) => { set('referenceRule')(e.target.value) }} />
                  </Field>
                  <Field label="Currencies"><input value={form.currencies} required onChange={(e) => { set('currencies')(e.target.value) }} /></Field>
                  <Field label="Official source" help="Where these details were taken from.">
                    <input value={form.source} required onChange={(e) => { set('source')(e.target.value) }} />
                  </Field>
                  <Field label="Replaces (optional)" help="A new version of an existing payee; the old one stays payable until this one is verified.">
                    <select value={form.supersedes} onChange={(e) => { set('supersedes')(e.target.value) }}>
                      <option value="">— a new payee</option>
                      {data.beneficiaries.filter((b) => b.state !== 'SUPERSEDED').map((b) => (
                        <option key={b.id} value={b.id}>{b.legalName} · {words(b.state)}</option>
                      ))}
                    </select>
                  </Field>
                  <div className="ztax-form-actions"><Submit busy={action.busy}>Record payee version</Submit></div>
                  <div style={{ gridColumn: '1 / -1' }}>
                    <p className="ztax-note">Someone other than you must verify these details before any payment to them is approved.</p>
                  </div>
                </form>
              </>
            ) : null}
            {action.error === null ? null : <ErrorNote error={action.error} />}
          </>
        )}
      </Loaded>
    </Card>
  )
}
