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
  PageHeader,
  Raw,
  Status,
  Submit,
  useAction,
  useLoad,
} from '../ui/kit'
import { toTimestamp } from './determine/shared'
import type { PageProps } from './types'
import './obligations.css'

// Compliance packages (ZTAX-COMP-001): one obligation's filing, prepared,
// independently reviewed, approved as an exact version, submitted, and
// answered by the authority. The cell enforces every rule here; the page only
// offers the step the package's status allows.

type Package = S<'CompliancePackage'>
type PackageStatus = S<'ComplianceStatus'>
type Mode = S<'ComplianceMode'>
type Channel = S<'ComplianceChannel'>
type Decision = S<'ComplianceReviewDecision'>
type Ack = S<'ComplianceAcknowledgement'>
type Resolution = S<'ComplianceResolution'>

const STATUSES: readonly PackageStatus[] = [
  'READY', 'IN_REVIEW', 'APPROVED', 'TRANSMITTING', 'UNCERTAIN_SUBMISSION', 'FILED', 'ACCEPTED', 'REJECTED', 'CANCELLED',
]
const MODES: readonly Mode[] = ['SOFTWARE_ONLY', 'ASSISTED', 'MANAGED_PREP', 'MANAGED_FILE', 'FULL_MANAGED_COMPLIANCE', 'PARTNER_LED']
const CHANNELS: readonly Channel[] = [
  'MANUAL_PORTAL', 'DIRECT_API', 'EFILE_NETWORK', 'FILE_TRANSFER', 'WEB_PORTAL_RPA', 'LOCAL_PARTNER', 'PAPER',
]
const DECISIONS: readonly Decision[] = ['ACCEPT', 'RETURN_FOR_CHANGE', 'REJECT', 'ESCALATE']
const ACKS: readonly Ack[] = [
  'RECEIVED', 'ACCEPTED', 'ACCEPTED_WITH_WARNINGS', 'PENDING_AUTHORITY', 'REJECTED_VALIDATION',
  'REJECTED_AUTHORIZATION', 'REJECTED_DUPLICATE', 'UNKNOWN',
]
const RESOLUTIONS: readonly Resolution[] = ['CONFIRMED_FILED', 'CONFIRMED_NOT_FILED', 'DUPLICATE_DETECTED', 'MANUAL_INVESTIGATION']

// Acknowledgements and resolutions that claim the authority has the filing,
// which only its own reference evidences.
const NEEDS_REFERENCE: readonly string[] = ['RECEIVED', 'ACCEPTED', 'ACCEPTED_WITH_WARNINGS', 'PENDING_AUTHORITY', 'CONFIRMED_FILED', 'DUPLICATE_DETECTED']

const PRIORITY_LABEL: Record<string, string> = {
  P0: 'P0 · deadline imminent', P1: 'P1 · material blocker', P2: 'P2 · review', P3: 'P3 · normal', P4: 'P4 · follow-up', P5: 'P5 · done',
}

const pick = <T extends string>(values: readonly T[], v: string, fallback: T): T =>
  (values as readonly string[]).includes(v) ? (v as T) : fallback

const isStatus = (v: string): v is PackageStatus => (STATUSES as readonly string[]).includes(v)

const words = (s: string) => s.replaceAll('_', ' ')

const Priority = ({ value }: { value: string }) => (
  <span className={`ztax-status ztax-status--${value === 'P0' ? 'bad' : value === 'P1' ? 'warn' : 'neutral'}`}>
    {PRIORITY_LABEL[value] ?? value}
  </span>
)

const Overdue = ({ p }: { p: Package }) => (
  <>
    {p.overdue.statutory ? <span className="ztax-status ztax-status--bad">statutory deadline missed</span> : null}
    {p.overdue.internal && !p.overdue.statutory ? <span className="ztax-status ztax-status--warn">internal target missed</span> : null}
    {!p.overdue.statutory && !p.overdue.internal ? <span className="muted">on time</span> : null}
  </>
)

export const CompliancePage = ({ session }: PageProps) => {
  const [status, setStatus] = useState<PackageStatus | 'ALL'>('ALL')
  const [selected, setSelected] = useState<string | null>(null)
  const { load, reload } = useLoad(
    (signal) => ztax.listCompliancePackages(status === 'ALL' ? { limit: 500 } : { status, limit: 500 }, signal),
    [status],
  )
  const roles = session.user.roles
  const canOperate = roles.includes('OPERATOR')
  const canReview = canOperate || roles.includes('ANALYST')

  return (
    <div className="ztax-page">
      <PageHeader
        title="Compliance"
        subtitle="Filings in queue order: priority, then the authority's deadline. Each is prepared, reviewed by someone else, approved as an exact version, then submitted."
        actions={
          <button type="button" className="ztax-btn-quiet" onClick={reload}>
            Refresh
          </button>
        }
      />

      <Card
        title="Queue"
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
            data.packages.length === 0 ? (
              <Empty>
                {status === 'ALL'
                  ? 'No compliance packages yet. Start one below for an obligation that is due.'
                  : `No package is ${words(status)}.`}
              </Empty>
            ) : (
              <div style={{ overflowX: 'auto' }}>
                <table className="ztax-action-table">
                  <thead>
                    <tr>
                      <th>Priority</th>
                      <th>Return</th>
                      <th>Period</th>
                      <th>Authority deadline</th>
                      <th>Deadlines</th>
                      <th>Status</th>
                      <th>Ready</th>
                      <th>Action</th>
                    </tr>
                  </thead>
                  <tbody>
                    {data.packages.map((p) => (
                      <tr key={p.id} className={p.id === selected ? 'ztax-row-selected' : undefined}>
                        <td style={{ paddingRight: 8 }}><Priority value={p.priority} /></td>
                        <td style={{ fontWeight: 500, color: '#111827', paddingRight: 8 }}>
                          {p.obligationType} <span className="mono muted">{p.filingCountry} · {p.filingAuthority}</span>
                        </td>
                        <td style={{ paddingRight: 8, whiteSpace: 'nowrap' }}>{civilDate(p.periodStart)} – {civilDate(p.periodEnd)}</td>
                        <td style={{ paddingRight: 8, whiteSpace: 'nowrap' }}>{instant(p.statutoryDue)}</td>
                        <td style={{ paddingRight: 8 }}><Overdue p={p} /></td>
                        <td style={{ paddingRight: 8 }}><Status value={p.status} /></td>
                        <td style={{ paddingRight: 8 }}>
                          {p.status === 'ACCEPTED' || p.status === 'CANCELLED' ? <span className="muted">—</span>
                            : p.ready ? <Status value="READY" /> : <span className="muted">blocked</span>}
                        </td>
                        <td>
                          <button type="button" className="ztax-table-action-btn" aria-pressed={p.id === selected}
                            onClick={() => { setSelected(p.id) }}>
                            Open
                          </button>
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
        <PackageDetail key={selected} id={selected} canOperate={canOperate} canReview={canReview}
          onChanged={reload} onClose={() => { setSelected(null) }} />
      )}

      {canOperate ? (
        <StartPackage
          taken={load.status === 'ready' ? load.data.packages.map((p) => `${p.obligationType}|${p.periodStart}`) : []}
          onCreated={(p) => { setSelected(p.id); reload() }}
        />
      ) : (
        <Card title="Start a package">
          <p className="ztax-note">Starting a compliance package needs the OPERATOR role.</p>
        </Card>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// start a package
// ---------------------------------------------------------------------------

const StartPackage = ({ taken, onCreated }: { taken: readonly string[]; onCreated: (p: Package) => void }) => {
  const obligations = useLoad((signal) => ztax.listObligations({ limit: 500 }, signal), [])
  const matrix = useLoad((signal) => ztax.getLegalMatrix(signal), [])
  const [obligationId, setObligationId] = useState('')
  const [mode, setMode] = useState<Mode>('SOFTWARE_ONLY')
  const [channel, setChannel] = useState<Channel>('MANUAL_PORTAL')
  const [target, setTarget] = useState('')
  const [authority, setAuthority] = useState('')
  const [problem, setProblem] = useState<string | null>(null)
  const action = useAction<Package>()

  const submit = (fallbackObligation: string, fallbackAuthority: string) => (e: FormEvent) => {
    e.preventDefault()
    setProblem(null)
    const [country = '', name = ''] = (authority || fallbackAuthority).split('|')
    const submitTarget = target.trim() === '' ? undefined : toTimestamp(target)
    if (submitTarget === null) {
      setProblem('The submission target is a date and time.')
      return
    }
    void action.run(() => ztax.createCompliancePackage({
      obligationId: obligationId || fallbackObligation, mode, channel, filingCountry: country, filingAuthority: name,
      ...(submitTarget === undefined ? {} : { submitTarget }),
    })).then((p) => {
      if (p !== null) {
        onCreated(p)
      }
    })
  }

  return (
    <Card title="Start a package">
      <Loaded load={obligations.load}>
        {(o) => (
          <Loaded load={matrix.load}>
            {(m) => {
              // An obligation has one package; the ones that have it are worked from the queue.
              const open = o.obligations.filter((x) => x.status !== 'CLOSED' && !taken.includes(`${x.type}|${x.periodStart}`))
              const fileTargets = [...new Set(m.rules.filter((r) => r.service === 'FILE').map((r) => `${r.country}|${r.authority}`))]
              if (open.length === 0) {
                return <Empty>Every open obligation already has a package. A new one appears the first time a commit falls in a new period.</Empty>
              }
              if (fileTargets.length === 0) {
                return <Empty>The legal matrix this cell loaded has no FILE rule, so no authority can be filed with.</Empty>
              }
              return (
                <form className="ztax-form" onSubmit={submit(open[0]?.id ?? '', fileTargets[0] ?? '')}>
                  <Field label="Obligation" help="Asking again for the same obligation returns the package already made.">
                    <select value={obligationId || (open[0]?.id ?? '')} onChange={(e) => { setObligationId(e.target.value) }}>
                      {open.map((x) => (
                        <option key={x.id} value={x.id}>
                          {x.type} · {civilDate(x.periodStart)} – {civilDate(x.periodEnd)} · {words(x.effectiveStatus)}
                        </option>
                      ))}
                    </select>
                  </Field>
                  <Field label="File with" help="An authority the legal matrix has a FILE rule for. The gate is checked at submission.">
                    <select value={authority || (fileTargets[0] ?? '')} onChange={(e) => { setAuthority(e.target.value) }}>
                      {fileTargets.map((t) => <option key={t} value={t}>{t.replace('|', ' · ')}</option>)}
                    </select>
                  </Field>
                  <Field label="Operating mode" help="Who does the work. Software-only: the customer submits and represents.">
                    <select value={mode} onChange={(e) => { setMode(pick(MODES, e.target.value, 'SOFTWARE_ONLY')) }}>
                      {MODES.map((x) => <option key={x} value={x}>{words(x)}</option>)}
                    </select>
                  </Field>
                  <Field label="Channel">
                    <select value={channel} onChange={(e) => { setChannel(pick(CHANNELS, e.target.value, 'MANUAL_PORTAL')) }}>
                      {CHANNELS.map((x) => <option key={x} value={x}>{words(x)}</option>)}
                    </select>
                  </Field>
                  <Field label="Our submission target (UTC, optional)" help="Never later than the authority's deadline, which is not changed by it.">
                    <input type="datetime-local" value={target} onChange={(e) => { setTarget(e.target.value) }} />
                  </Field>
                  <div className="ztax-form-actions">
                    <Submit busy={action.busy}>Start package</Submit>
                  </div>
                  <div style={{ gridColumn: '1 / -1' }}>
                    {problem === null ? null : <p className="ztax-error-note" role="alert">{problem}</p>}
                    {action.error === null ? null : <ErrorNote error={action.error} />}
                  </div>
                </form>
              )
            }}
          </Loaded>
        )}
      </Loaded>
    </Card>
  )
}

// ---------------------------------------------------------------------------
// one package
// ---------------------------------------------------------------------------

const PackageDetail = ({ id, canOperate, canReview, onChanged, onClose }: {
  id: string
  canOperate: boolean
  canReview: boolean
  onChanged: () => void
  onClose: () => void
}) => {
  const { load, reload } = useLoad((signal) => ztax.getCompliancePackage(id, signal), [id])
  const changed = () => {
    reload()
    onChanged()
  }

  return (
    <Loaded load={load}>
      {(p) => {
        const current = [...p.history].reverse().find((s) => s.kind === 'PREPARED')
        return (
          <>
            <Columns>
              <Card
                title={`${p.obligationType} · ${civilDate(p.periodStart)} – ${civilDate(p.periodEnd)}`}
                aside={<button type="button" className="ztax-btn-quiet" onClick={onClose}>Close</button>}
              >
                <dl className="ztax-kv">
                  <dt>Package</dt>
                  <dd><Id value={p.id} /></dd>
                  <dt>Status</dt>
                  <dd><Status value={p.status} /> <Priority value={p.priority} /></dd>
                  <dt>Obligation</dt>
                  <dd><Id value={p.obligationId} /> <Status value={p.obligationStatus} /></dd>
                  <dt>File with</dt>
                  <dd className="mono">{p.filingCountry} · {p.filingAuthority}</dd>
                  <dt>Mode · channel</dt>
                  <dd>{words(p.mode)} · {words(p.channel)}</dd>
                  <dt>Authority deadline</dt>
                  <dd>{instant(p.statutoryDue)} <span className="muted">· authority calendar {p.timezone}</span></dd>
                  <dt>Our target</dt>
                  <dd>{p.submitTarget === undefined ? <span className="muted">none set</span> : instant(p.submitTarget)}</dd>
                  <dt>Deadlines</dt>
                  <dd><Overdue p={p} /></dd>
                  <dt>Current version</dt>
                  <dd>
                    {current === undefined ? <span className="muted">not prepared</span> : (
                      <>v{current.version} · <Amount amount={current.controlTotal} currency={current.currency} /> · <Id value={current.payloadHash} /></>
                    )}
                  </dd>
                </dl>
                <Raw value={p} />
              </Card>

              <Card title="Readiness">
                <table className="ztax-action-table">
                  <tbody>
                    {p.readiness.map((r) => (
                      <tr key={r.dimension}>
                        <td style={{ paddingRight: 8, whiteSpace: 'nowrap' }}>
                          {r.ready ? <Status value="READY" /> : <Status value="DATA_REQUIRED" />}
                        </td>
                        <td style={{ paddingRight: 8 }} className="mono">{words(r.dimension)}</td>
                        <td>{r.detail}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
                <p className="ztax-note" style={{ marginTop: 12 }}>
                  {p.ready ? 'Every dimension passes.' : 'Not ready: any dimension that fails or is unknown blocks the package.'}
                </p>
              </Card>
            </Columns>

            <Columns>
              <Card title="Next step">
                <NextStep p={p} canOperate={canOperate} canReview={canReview} onDone={changed} />
              </Card>
              <Card title="History">
                <History p={p} />
              </Card>
            </Columns>
          </>
        )
      }}
    </Loaded>
  )
}

const History = ({ p }: { p: Package }) => (
  <div style={{ overflowX: 'auto' }}>
    <table className="ztax-action-table">
      <thead>
        <tr>
          <th>#</th>
          <th>Step</th>
          <th>Then</th>
          <th>Detail</th>
          <th>By</th>
          <th>When</th>
        </tr>
      </thead>
      <tbody>
        {[...p.history].reverse().map((s) => (
          <tr key={s.seq}>
            <td className="mono" style={{ paddingRight: 8 }}>{s.seq}</td>
            <td style={{ paddingRight: 8 }}>{words(s.kind)}</td>
            <td style={{ paddingRight: 8 }}><Status value={s.status} /></td>
            <td style={{ paddingRight: 8 }}>
              {[
                s.version === undefined ? null : `v${s.version}`,
                s.review === undefined ? null : words(s.review),
                s.acknowledgement === undefined ? null : words(s.acknowledgement),
                s.resolution === undefined ? null : words(s.resolution),
                s.authorityReference === undefined ? null : `ref ${s.authorityReference}`,
                s.note ?? null,
                s.evidence ?? null,
              ].filter((x) => x !== null).join(' · ')}
            </td>
            <td style={{ paddingRight: 8 }}><Id value={s.recordedBy} /></td>
            <td style={{ whiteSpace: 'nowrap' }}>{instant(s.recordedAt)}</td>
          </tr>
        ))}
      </tbody>
    </table>
  </div>
)

/** One write, rendered as a small form with its outcome. */
const StepForm = ({ label, busy, error, onSubmit, children, help }: {
  label: string
  busy: boolean
  error: unknown
  onSubmit: () => void
  children?: ReactNode
  help?: string
}) => (
  <form className="ztax-form" style={{ marginBottom: 16 }} onSubmit={(e) => { e.preventDefault(); onSubmit() }}>
    {children}
    <div className="ztax-form-actions">
      <Submit busy={busy}>{label}</Submit>
    </div>
    <div style={{ gridColumn: '1 / -1' }}>
      {help === undefined ? null : <p className="ztax-note">{help}</p>}
      {error === null ? null : <ErrorNote error={error} />}
    </div>
  </form>
)

const NextStep = ({ p, canOperate, canReview, onDone }: {
  p: Package
  canOperate: boolean
  canReview: boolean
  onDone: () => void
}) => {
  const action = useAction<Package>()
  const [note, setNote] = useState('')
  const [decision, setDecision] = useState<Decision>('ACCEPT')
  const [ack, setAck] = useState<Ack>('RECEIVED')
  const [resolution, setResolution] = useState<Resolution>('CONFIRMED_FILED')
  const [reference, setReference] = useState('')
  const [evidence, setEvidence] = useState('')
  const [reason, setReason] = useState('')

  const run = (call: () => ReturnType<typeof ztax.getCompliancePackage>) => {
    void action.run(call).then((next) => {
      if (next !== null) {
        setNote('')
        setReference('')
        setEvidence('')
        setReason('')
        onDone()
      }
    })
  }
  const opt = (s: string) => (s.trim() === '' ? {} : { note: s.trim() })
  const ref = () => (reference.trim() === '' ? {} : { authorityReference: reference.trim() })

  const current = [...p.history].reverse().find((s) => s.kind === 'PREPARED')
  const lastReview = [...p.history].reverse().find((s) => s.kind === 'REVIEWED' || s.kind === 'PREPARED')
  const reviewed = lastReview?.kind === 'REVIEWED' && lastReview.review === 'ACCEPT'
  const notSubmitted = ['READY', 'IN_REVIEW', 'APPROVED'].includes(p.status)

  const noteField = (label = 'Note (optional)') => (
    <Field label={label}>
      <input value={note} maxLength={2000} onChange={(e) => { setNote(e.target.value) }} />
    </Field>
  )
  const forms: ReactNode[] = []

  if (canOperate && (p.status === 'READY' || p.status === 'REJECTED' || p.status === 'IN_REVIEW' || p.status === 'APPROVED')) {
    forms.push(
      <StepForm key="prepare" label={current === undefined ? 'Prepare version 1' : `Prepare version ${(current.version ?? 0) + 1}`}
        busy={action.busy} error={null} onSubmit={() => { run(() => ztax.prepareCompliancePackage(p.id, opt(note))) }}
        help={p.status === 'IN_REVIEW' || p.status === 'APPROVED'
          ? 'A new version replaces the one under review and cancels any approval of it.'
          : 'Snapshots the obligation as it stands: its assessment becomes the control total.'}>
        {noteField()}
      </StepForm>,
    )
  }
  if (p.status === 'IN_REVIEW' && current !== undefined) {
    if (!canReview) {
      forms.push(<p key="r" className="ztax-note">Reviewing needs the OPERATOR or ANALYST role.</p>)
    } else if (!reviewed) {
      forms.push(
        <StepForm key="review" label={`Record review of v${current.version}`} busy={action.busy} error={null}
          onSubmit={() => { run(() => ztax.reviewCompliancePackage(p.id, { version: current.version ?? 1, decision, ...opt(note) })) }}
          help="The person who prepared this version cannot review it.">
          <Field label="Decision">
            <select value={decision} onChange={(e) => { setDecision(pick(DECISIONS, e.target.value, 'ACCEPT')) }}>
              {DECISIONS.map((d) => <option key={d} value={d}>{words(d)}</option>)}
            </select>
          </Field>
          {noteField(decision === 'ACCEPT' ? 'Note (optional)' : 'Reason (required)')}
        </StepForm>,
      )
    } else {
      forms.push(
        <StepForm key="approve" label={`Approve v${current.version} for submission`} busy={action.busy} error={null}
          onSubmit={() => { run(() => ztax.approveCompliancePackage(p.id, { version: current.version ?? 1, payloadHash: current.payloadHash ?? '', ...opt(note) })) }}
          help="Approval pins this exact version and payload hash. The preparer cannot approve it.">
          {noteField()}
        </StepForm>,
      )
    }
  }
  if (canOperate && p.status === 'APPROVED') {
    forms.push(
      <StepForm key="submit" label="Start submission" busy={action.busy} error={null}
        onSubmit={() => { run(() => ztax.submitCompliancePackage(p.id, opt(note))) }}
        help={p.gate.allowed
          ? 'The package becomes TRANSMITTING. It is FILED only when you record the authority’s answer.'
          : `The legal gate blocks this now: ${p.gate.detail ?? p.gate.reason ?? ''}`}>
        {noteField()}
      </StepForm>,
    )
  }
  if (canOperate && (p.status === 'TRANSMITTING' || p.status === 'FILED')) {
    forms.push(
      <StepForm key="ack" label="Record the authority’s answer" busy={action.busy} error={null}
        onSubmit={() => { run(() => ztax.acknowledgeCompliancePackage(p.id, { acknowledgement: ack, evidence, ...ref(), ...opt(note) })) }}
        help="If you cannot tell whether the authority received it, record UNKNOWN. It is never resubmitted blindly.">
        <Field label="Answer">
          <select value={ack} onChange={(e) => { setAck(pick(ACKS, e.target.value, 'RECEIVED')) }}>
            {ACKS.map((a) => <option key={a} value={a}>{words(a)}</option>)}
          </select>
        </Field>
        <Field label={NEEDS_REFERENCE.includes(ack) ? 'Authority reference (required)' : 'Authority reference'}>
          <input value={reference} maxLength={200} onChange={(e) => { setReference(e.target.value) }} />
        </Field>
        <Field label="Authority response, as received (required)">
          <textarea value={evidence} rows={3} maxLength={20000} required onChange={(e) => { setEvidence(e.target.value) }} />
        </Field>
      </StepForm>,
    )
  }
  if (canOperate && p.status === 'UNCERTAIN_SUBMISSION') {
    forms.push(
      <StepForm key="resolve" label="Record the resolution" busy={action.busy} error={null}
        onSubmit={() => { run(() => ztax.resolveCompliancePackage(p.id, { resolution, evidence, ...ref() })) }}
        help="Establish what the authority holds before deciding. Confirmed-not-filed reopens submission, linked to this attempt.">
        <Field label="Resolution">
          <select value={resolution} onChange={(e) => { setResolution(pick(RESOLUTIONS, e.target.value, 'CONFIRMED_FILED')) }}>
            {RESOLUTIONS.map((r) => <option key={r} value={r}>{words(r)}</option>)}
          </select>
        </Field>
        <Field label={NEEDS_REFERENCE.includes(resolution) ? 'Authority reference (required)' : 'Authority reference'}>
          <input value={reference} maxLength={200} onChange={(e) => { setReference(e.target.value) }} />
        </Field>
        <Field label="How it was established (required)">
          <textarea value={evidence} rows={3} maxLength={20000} required onChange={(e) => { setEvidence(e.target.value) }} />
        </Field>
      </StepForm>,
    )
  }
  if (canOperate && notSubmitted) {
    forms.push(
      <StepForm key="cancel" label="Cancel package" busy={action.busy} error={null}
        onSubmit={() => { run(() => ztax.cancelCompliancePackage(p.id, { reason: reason.trim() })) }}
        help="Only before submission. The package and its history are kept.">
        <Field label="Reason (required)">
          <input value={reason} maxLength={2000} required onChange={(e) => { setReason(e.target.value) }} />
        </Field>
      </StepForm>,
    )
  }

  return (
    <>
      {forms.length === 0 ? (
        <Empty>
          {p.status === 'ACCEPTED' ? 'Accepted by the authority. Nothing left to do here; payment is handled under Remittance.'
            : p.status === 'CANCELLED' ? 'Cancelled. The history is kept.'
            : 'Nothing your roles can do on this package now.'}
        </Empty>
      ) : forms}
      {action.error === null ? null : <ErrorNote error={action.error} />}
    </>
  )
}
