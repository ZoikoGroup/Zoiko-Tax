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
import { todayUtc } from './determine/shared'
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

export const RemittancePage = ({ session }: PageProps) => {
  const [status, setStatus] = useState<RemStatus | 'ALL'>('ALL')
  const [selected, setSelected] = useState<string | null>(null)
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
    <div className="ztax-page">
      <PageHeader
        title="Remittance"
        subtitle="Payments of accepted returns, overdue first. Each instruction is approved by someone other than its maker, as an exact hash."
        actions={<button type="button" className="ztax-btn-quiet" onClick={refresh}>Refresh</button>}
      />
      <p className="ztax-note">
        Funds posture: <span className="mono">NO_CUSTODY</span>. ZoikoTax prepares and records instructions to your own bank or
        provider. It never holds, pools or moves your money.
      </p>

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
    </div>
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
