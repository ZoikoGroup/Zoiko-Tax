import { useState, type ReactNode } from 'react'

import type { Result, S } from '@zoikotax/sdk'

import { instant } from '../../dashboard/format'
import { ztax } from '../../platform/client'
import { Card, Columns, Empty, ErrorNote, Field, Id, Loaded, Money, Raw, Status, Submit, useAction, useLoad } from '../../ui/kit'
import { CURRENCY, isDecimal, toTimestamp } from '../determine/shared'
import {
  deadlineText,
  ESCALATIONS,
  FLAG,
  FLAG_ORDER,
  mediaTypeOf,
  opt,
  OUTCOMES,
  pick,
  refusalHint,
  SETTLEMENTS,
  sha256Hex,
  SOURCES,
  words,
  type Action,
  type Authenticity,
  type DeadlineSource,
  type EscalationReason,
  type Flag,
  type Notice,
  type Outcome,
  type Settlement,
} from './model'

// One notice: the statutory deadline ledger beside the separate SLA clock,
// the recorded documents, the history, and only the acts its status admits.
// The cell still applies every guard; a refusal is shown with its reason code
// and what it means for the next step.

export const Flags = ({ flags }: { flags: readonly Flag[] }) => (
  <>
    {FLAG_ORDER.filter((f) => flags.includes(f)).map((f) => (
      <span key={f} className={`ztax-status ztax-status--${FLAG[f].tone}`} style={{ marginRight: 4 }}>{FLAG[f].label}</span>
    ))}
  </>
)

export const NoticeDetail = ({ id, canAct, onChanged, onClose }: {
  id: string
  canAct: boolean
  onChanged: () => void
  onClose: () => void
}) => {
  const { load, reload } = useLoad((signal) => ztax.getNotice(id, signal), [id])
  return (
    <Loaded load={load}>
      {(n) => (
        <>
          <Columns>
            <Card title={`${words(n.type)} · ${n.authority} · ${n.authorityReference}`}
              aside={<button type="button" className="ztax-btn-quiet" onClick={onClose}>Close</button>}>
              <dl className="ztax-kv">
                <dt>Notice</dt><dd><Id value={n.id} /></dd>
                <dt>Status</dt><dd><Status value={n.status} /></dd>
                <dt>Attention</dt><dd>{n.flags.length === 0 ? <span className="muted">nothing flagged</span> : <Flags flags={n.flags} />}</dd>
                <dt>Jurisdiction</dt><dd className="mono">{n.jurisdiction}</dd>
                <dt>Recipient</dt>
                <dd>
                  {n.entityResolved ? <Id value={n.resolvedLegalEntityId} /> : <span className="ztax-status ztax-status--bad">unresolved</span>}
                  {n.entityAmbiguous ? <span className="muted"> · marked ambiguous at intake</span> : null}
                </dd>
                <dt>Authenticity</dt><dd><Status value={n.authenticity} /></dd>
                <dt>Received</dt><dd>{instant(n.receivedAt)}</dd>
                <dt>Period</dt><dd>{n.periodStart === undefined ? '—' : `${n.periodStart} – ${n.periodEnd ?? ''}`}</dd>
                <dt>Stated amount</dt><dd><Money value={n.amount} /></dd>
                <dt>Linked case</dt><dd className="mono">{n.linkedCaseId ?? '—'}</dd>
                <dt>References</dt>
                <dd>
                  {n.links.length === 0 ? '—' : n.links.map((l) => (
                    <div key={`${l.kind}-${l.reference}`}>{words(l.kind)} <span className="mono">{l.reference}</span></div>
                  ))}
                  <span className="muted" style={{ fontSize: 11 }}>Read-only references; a notice never changes these records.</span>
                </dd>
              </dl>
              <Raw value={n} />
            </Card>
            <Card title="Clocks">
              <h3 className="ztax-card-title" style={{ fontSize: 14 }}>Statutory deadline</h3>
              <dl className="ztax-kv">
                <dt>In force</dt>
                <dd>
                  {deadlineText(n.statutoryDeadline.current)} · {words(n.statutoryDeadline.current.source)}{' '}
                  <Status value={n.statutoryDeadline.current.verified ? 'VERIFIED' : 'UNVERIFIED'} />
                </dd>
                <dt>As received</dt>
                <dd>{deadlineText(n.statutoryDeadline.original)} · {words(n.statutoryDeadline.original.source)}</dd>
              </dl>
              <table className="ztax-action-table">
                <thead><tr><th>#</th><th>Entry</th><th>Due</th><th>Source</th><th>Evidence</th><th>By</th><th>When</th></tr></thead>
                <tbody>
                  {n.deadlines.map((d) => (
                    <tr key={d.seq}>
                      <td className="mono" style={{ paddingRight: 8 }}>{d.seq}</td>
                      <td style={{ paddingRight: 8 }}>{words(d.kind)}</td>
                      <td style={{ paddingRight: 8, whiteSpace: 'nowrap' }}>{d.dueDate ?? 'unknown'} <span className="muted">{d.timezone}</span></td>
                      <td style={{ paddingRight: 8 }}>{words(d.source)} {d.verified ? <Status value="VERIFIED" /> : null}</td>
                      <td style={{ paddingRight: 8 }}>
                        {[d.evidence, d.authorityReference === undefined ? undefined : `ref ${d.authorityReference}`,
                          d.attachmentSha256 === undefined ? undefined : `doc ${d.attachmentSha256.slice(0, 12)}…`]
                          .filter((x) => x !== undefined).join(' · ')}
                      </td>
                      <td style={{ paddingRight: 8 }}><Id value={d.recordedBy} /></td>
                      <td style={{ whiteSpace: 'nowrap' }}>{instant(d.recordedAt)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
              <p className="ztax-note">The original stays in this ledger; the date changes only through an extension that carries evidence.</p>
              <h3 className="ztax-card-title" style={{ fontSize: 14 }}>Internal SLA</h3>
              <dl className="ztax-kv">
                <dt>Owner</dt><dd>{n.sla.ownerId === undefined ? <span className="muted">unassigned</span> : <Id value={n.sla.ownerId} />}</dd>
                <dt>Target</dt>
                <dd>
                  {n.sla.dueAt === undefined ? '—' : instant(n.sla.dueAt)}{' '}
                  {n.sla.breached ? <span className="ztax-status ztax-status--warn">breached</span> : null}
                </dd>
              </dl>
              <p className="ztax-note">Our own target. It never moves the statutory deadline.</p>
            </Card>
          </Columns>
          <Columns>
            <Card title="Act on this notice">
              {canAct ? (
                <Actions n={n} onDone={() => { reload(); onChanged() }} />
              ) : (
                <Empty>Your roles read notices. Registering and working them is for OPERATOR or ANALYST.</Empty>
              )}
            </Card>
            <Card title="Documents">
              {n.attachments.length === 0 ? <Empty>No document recorded.</Empty> : (
                <table className="ztax-action-table">
                  <thead><tr><th>File</th><th>Type</th><th>SHA-256</th><th>When</th></tr></thead>
                  <tbody>
                    {n.attachments.map((a) => (
                      <tr key={a.sha256}>
                        <td style={{ paddingRight: 8 }}>{a.filename}</td>
                        <td className="mono" style={{ paddingRight: 8 }}>{a.mediaType}</td>
                        <td className="mono" style={{ paddingRight: 8, overflowWrap: 'anywhere' }}>{a.sha256}</td>
                        <td style={{ whiteSpace: 'nowrap' }}>{a.recordedAt === undefined ? '—' : instant(a.recordedAt)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
            </Card>
          </Columns>
          <Card title="History">
            <table className="ztax-action-table">
              <thead><tr><th>#</th><th>Event</th><th>Then</th><th>Detail</th><th>By</th><th>When</th></tr></thead>
              <tbody>
                {[...n.history].reverse().map((e) => (
                  <tr key={e.seq}>
                    <td className="mono" style={{ paddingRight: 8 }}>{e.seq}</td>
                    <td style={{ paddingRight: 8 }}>{words(e.kind)}</td>
                    <td style={{ paddingRight: 8 }}><Status value={e.status} /></td>
                    <td style={{ paddingRight: 8 }}>{eventText(e)}</td>
                    <td style={{ paddingRight: 8 }}><Id value={e.recordedBy} /></td>
                    <td style={{ whiteSpace: 'nowrap' }}>{instant(e.recordedAt)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </Card>
        </>
      )}
    </Loaded>
  )
}

function eventText(e: S<'NoticeEvent'>): string {
  const parts: (string | undefined)[] = [
    e.authenticity === undefined || e.kind === 'RECEIVED' ? undefined : words(e.authenticity),
    e.escalationReason === undefined ? undefined : words(e.escalationReason),
    e.deadline === undefined ? undefined : `${words(e.deadline.kind)} ${deadlineText(e.deadline)}`,
    e.responseVersion === undefined ? undefined : `version ${String(e.responseVersion)}`,
    e.outcome === undefined ? undefined : words(e.outcome),
    e.outcomeAmount === undefined ? undefined : `${e.outcomeAmount.amount} ${e.outcomeAmount.currency}`,
    e.settled === true ? `settled (${words(e.settlement ?? '')})` : e.settlement === undefined ? undefined : words(e.settlement),
    e.hearingDate === undefined ? undefined : `hearing ${e.hearingDate}`,
    e.attachment === undefined ? undefined : `${e.attachment.filename}`,
    e.legalEntityId === undefined ? undefined : `entity ${e.legalEntityId}`,
    e.authorityReference === undefined ? undefined : `ref ${e.authorityReference}`,
    e.note, e.evidence,
  ]
  return parts.filter((p) => p !== undefined && p !== '').join(' · ')
}

// ---------------------------------------------------------------------------
// actions
// ---------------------------------------------------------------------------

const StepForm = ({ label, busy, onSubmit, children, help }: {
  label: string
  busy: boolean
  onSubmit: () => void
  children?: ReactNode
  help?: string | undefined
}) => (
  <form className="ztax-form" style={{ marginBottom: 16 }} onSubmit={(e) => { e.preventDefault(); onSubmit() }}>
    {children}
    <div className="ztax-form-actions"><Submit busy={busy}>{label}</Submit></div>
    {help === undefined ? null : <div style={{ gridColumn: '1 / -1' }}><p className="ztax-note">{help}</p></div>}
  </form>
)

const ACTION_LABEL: Record<Action, string> = {
  START_AUTHENTICITY_REVIEW: 'Start authenticity review',
  MOVE_TO_LEGAL_REVIEW: 'Move to legal review',
  RECORD_AUTHENTICITY: 'Record authenticity',
  ESCALATE: 'Escalate to legal',
  RESOLVE_ENTITY: 'Resolve recipient',
  ASSIGN_OWNER: 'Assign deadline owner',
  RECORD_ATTACHMENT: 'Secure upload (record digest)',
  RECORD_DEADLINE: 'Confirm or extend deadline',
  PREPARE_RESPONSE: 'Create response package',
  APPROVE_RESPONSE: 'Approve response (four-eye)',
  RECORD_SUBMISSION: 'Record authority receipt',
  RECORD_OUTCOME: 'Record outcome',
  LODGE_APPEAL: 'Lodge appeal',
  CLOSE: 'Close',
}

const Actions = ({ n, onDone }: { n: Notice; onDone: () => void }) => {
  const [chosen, setChosen] = useState<Action | ''>('')
  const action = useAction<Notice>()
  const [text, setText] = useState('')
  const [evidence, setEvidence] = useState('')
  const [ref, setRef] = useState('')
  const [date, setDate] = useState('')
  const [timezone, setTimezone] = useState(n.statutoryDeadline.current.timezone)
  const [source, setSource] = useState<DeadlineSource>('AUTHORITY_CONFIRMATION')
  const [verified, setVerified] = useState(true)
  const [kind, setKind] = useState<'CONFIRMATION' | 'EXTENSION'>('EXTENSION')
  const [authenticity, setAuthenticity] = useState<Authenticity>('VERIFIED')
  const [reason, setReason] = useState<EscalationReason>('LEGAL_JUDGEMENT')
  const [outcome, setOutcome] = useState<Outcome>('ACCEPTED')
  const [settlement, setSettlement] = useState<Settlement | ''>('')
  const [settled, setSettled] = useState(false)
  const [amount, setAmount] = useState('')
  const [currency, setCurrency] = useState(n.amount?.currency ?? 'EUR')
  const [hearing, setHearing] = useState('')
  const [sla, setSla] = useState('')
  const [attachmentSha, setAttachmentSha] = useState('')
  const [file, setFile] = useState<{ sha256: string; filename: string; mediaType: string } | null>(null)
  const [invalid, setInvalid] = useState<string | null>(null)

  const run = (call: () => Promise<Result<Notice>>) => {
    setInvalid(null)
    void action.run(call).then((next) => {
      if (next !== null) {
        setText('')
        setEvidence('')
        setRef('')
        setDate('')
        setFile(null)
        setChosen('')
        onDone()
      }
    })
  }

  const allowed = n.allowedActions
  const response = [...n.history].reverse().find((e) => e.kind === 'RESPONSE_PREPARED')
  const textField = (label: string, required: boolean, max = 2000) => (
    <Field label={label}>
      <textarea value={text} rows={2} maxLength={max} required={required} onChange={(e) => { setText(e.target.value) }} />
    </Field>
  )
  const evidenceField = (label: string, required: boolean) => (
    <Field label={label}>
      <textarea value={evidence} rows={3} maxLength={20000} required={required} onChange={(e) => { setEvidence(e.target.value) }} />
    </Field>
  )
  const refField = (label: string, required: boolean) => (
    <Field label={label}>
      <input value={ref} maxLength={200} required={required} onChange={(e) => { setRef(e.target.value) }} />
    </Field>
  )
  const deadlineFields = (withSource: boolean) => (
    <>
      <Field label="Due date (authority’s calendar)">
        <input type="date" value={date} onChange={(e) => { setDate(e.target.value) }} />
      </Field>
      <Field label="Time zone">
        <input value={timezone} list="notice-timezones" required onChange={(e) => { setTimezone(e.target.value) }} />
      </Field>
      {withSource ? (
        <Field label="Source">
          <select value={source} onChange={(e) => { setSource(pick(SOURCES, e.target.value, 'AUTHORITY_CONFIRMATION')) }}>
            {SOURCES.filter((s) => s !== 'UNKNOWN').map((s) => <option key={s} value={s}>{words(s)}</option>)}
          </select>
        </Field>
      ) : null}
      <Field label="Verified with the authority">
        <input type="checkbox" checked={verified} onChange={(e) => { setVerified(e.target.checked) }} />
      </Field>
    </>
  )

  let form: ReactNode = null
  switch (chosen) {
    case 'START_AUTHENTICITY_REVIEW':
      form = (
        <StepForm label={ACTION_LABEL[chosen]} busy={action.busy}
          onSubmit={() => { run(() => ztax.transitionNotice(n.id, { to: 'AUTHENTICITY_REVIEW', ...opt('note', text) })) }}>
          {textField('Note', false)}
        </StepForm>
      )
      break
    case 'MOVE_TO_LEGAL_REVIEW':
      form = (
        <StepForm label={ACTION_LABEL[chosen]} busy={action.busy}
          help={n.status === 'RESPONSE_APPROVED' ? 'Withdraws the approval for more work. The deadline is not touched.'
            : n.status === 'AUTHENTICITY_REVIEW' ? 'Needs verified authenticity; otherwise escalate to legal validation.' : undefined}
          onSubmit={() => { run(() => ztax.transitionNotice(n.id, { to: 'LEGAL_REVIEW', ...opt('note', text) })) }}>
          {textField(n.status === 'RESPONSE_APPROVED' ? 'Why (required)' : 'Note', n.status === 'RESPONSE_APPROVED')}
        </StepForm>
      )
      break
    case 'RECORD_AUTHENTICITY':
      form = (
        <StepForm label={ACTION_LABEL[chosen]} busy={action.busy}
          help="Anything but VERIFIED escalates to legal validation and voids an approved answer."
          onSubmit={() => { run(() => ztax.recordNoticeAuthenticity(n.id, { authenticity, evidence: evidence.trim() })) }}>
          <Field label="Authenticity">
            <select value={authenticity} onChange={(e) => { setAuthenticity(pick<Authenticity>(['VERIFIED', 'UNVERIFIABLE', 'NOT_AUTHENTIC'], e.target.value, 'VERIFIED')) }}>
              {(['VERIFIED', 'UNVERIFIABLE', 'NOT_AUTHENTIC'] as const).map((a) => <option key={a} value={a}>{words(a)}</option>)}
            </select>
          </Field>
          {evidenceField('How it was established', true)}
        </StepForm>
      )
      break
    case 'ESCALATE':
      form = (
        <StepForm label={ACTION_LABEL[chosen]} busy={action.busy}
          help="Counsel’s reference or memo as evidence makes it a documented escalation, which a disposition without an authority outcome needs."
          onSubmit={() => { run(() => ztax.escalateNotice(n.id, { reason, note: text.trim(), ...opt('evidence', evidence) })) }}>
          <Field label="Reason">
            <select value={reason} onChange={(e) => { setReason(pick(ESCALATIONS, e.target.value, 'LEGAL_JUDGEMENT')) }}>
              {ESCALATIONS.map((r) => <option key={r} value={r}>{words(r)}</option>)}
            </select>
          </Field>
          {textField('What legal is asked to decide', true)}
          {evidenceField('Documentation (counsel reference, memo)', false)}
        </StepForm>
      )
      break
    case 'RESOLVE_ENTITY':
      form = (
        <StepForm label={ACTION_LABEL[chosen]} busy={action.busy}
          onSubmit={() => { run(() => ztax.resolveNoticeEntity(n.id, { legalEntityId: ref.trim(), evidence: evidence.trim() })) }}>
          {refField('Legal entity id', true)}
          {evidenceField('How it was established', true)}
        </StepForm>
      )
      break
    case 'ASSIGN_OWNER':
      form = (
        <StepForm label={ACTION_LABEL[chosen]} busy={action.busy}
          help="The SLA is our own target and never moves the statutory deadline."
          onSubmit={() => {
            const slaDue = sla === '' ? null : toTimestamp(sla)
            if (sla !== '' && slaDue === null) {
              setInvalid('The SLA is a date and time.')
              return
            }
            run(() => ztax.assignNoticeOwner(n.id, { ownerId: ref.trim(), ...(slaDue === null ? {} : { slaDue }), ...opt('note', text) }))
          }}>
          {refField('Owner user id', true)}
          <Field label="Internal SLA (UTC)">
            <input type="datetime-local" value={sla} onChange={(e) => { setSla(e.target.value) }} />
          </Field>
          {textField('Note', false)}
        </StepForm>
      )
      break
    case 'RECORD_ATTACHMENT':
      form = (
        <StepForm label={ACTION_LABEL[chosen]} busy={action.busy}
          help="The file is hashed here; only its SHA-256, name and type are recorded. Nothing is uploaded."
          onSubmit={() => {
            if (file === null) {
              setInvalid('Choose a file to hash.')
              return
            }
            run(() => ztax.recordNoticeAttachment(n.id, { ...file, ...opt('note', text) }))
          }}>
          <Field label="Document">
            <input type="file" onChange={(e) => {
              const f = e.target.files?.[0]
              if (f === undefined) {
                setFile(null)
                return
              }
              void sha256Hex(f).then((sha256) => { setFile({ sha256, filename: f.name.slice(0, 255), mediaType: mediaTypeOf(f) }) })
            }} />
          </Field>
          {file === null ? null : <p className="mono muted" style={{ gridColumn: '1 / -1', overflowWrap: 'anywhere' }}>SHA-256 {file.sha256}</p>}
          {textField('Note', false)}
        </StepForm>
      )
      break
    case 'RECORD_DEADLINE':
      form = (
        <StepForm label={ACTION_LABEL[chosen]} busy={action.busy}
          help={kind === 'CONFIRMATION' ? 'A confirmation verifies the date in force, or establishes an unknown one. It never moves a known date.'
            : 'An extension needs a verified deadline to extend, the authority’s grant verified, and its reference or a recorded document.'}
          onSubmit={() => {
            if (date === '') {
              setInvalid('Name the due date.')
              return
            }
            run(() => ztax.recordNoticeDeadline(n.id, {
              kind, dueDate: date, timezone: timezone.trim(), source, verified, evidence: evidence.trim(),
              ...opt('authorityReference', ref), ...opt('attachmentSha256', attachmentSha),
            }))
          }}>
          <Field label="Entry">
            <select value={kind} onChange={(e) => { setKind(e.target.value === 'CONFIRMATION' ? 'CONFIRMATION' : 'EXTENSION') }}>
              <option value="EXTENSION">Extension</option>
              <option value="CONFIRMATION">Confirmation</option>
            </select>
          </Field>
          {deadlineFields(true)}
          {evidenceField('Evidence', true)}
          {refField('Authority reference for the grant', false)}
          <Field label="Or a recorded document">
            <select value={attachmentSha} onChange={(e) => { setAttachmentSha(e.target.value) }}>
              <option value="">—</option>
              {n.attachments.map((a) => <option key={a.sha256} value={a.sha256}>{a.filename}</option>)}
            </select>
          </Field>
        </StepForm>
      )
      break
    case 'PREPARE_RESPONSE':
      form = (
        <StepForm label={ACTION_LABEL[chosen]} busy={action.busy}
          help="A new version supersedes any approval; someone else approves it."
          onSubmit={() => { run(() => ztax.prepareNoticeResponse(n.id, { summary: text.trim(), ...(file === null ? {} : { sha256: file.sha256 }) })) }}>
          {textField('What the response answers', true)}
          <Field label="Response document (hashed here, optional)">
            <input type="file" onChange={(e) => {
              const f = e.target.files?.[0]
              if (f !== undefined) {
                void sha256Hex(f).then((sha256) => { setFile({ sha256, filename: f.name.slice(0, 255), mediaType: mediaTypeOf(f) }) })
              }
            }} />
          </Field>
        </StepForm>
      )
      break
    case 'APPROVE_RESPONSE':
      form = response?.responseVersion === undefined ? <Empty>No response package to approve.</Empty> : (
        <StepForm label={`Approve version ${String(response.responseVersion)}`} busy={action.busy}
          help="Four-eye: the person who prepared the package cannot approve it. Authenticity must be verified and the recipient resolved."
          onSubmit={() => { run(() => ztax.approveNoticeResponse(n.id, { version: response.responseVersion ?? 1, ...opt('note', text) })) }}>
          <p style={{ gridColumn: '1 / -1' }}>
            Version {String(response.responseVersion)} by <Id value={response.recordedBy} />: {response.note}
          </p>
          {textField('Note', false)}
        </StepForm>
      )
      break
    case 'RECORD_SUBMISSION':
      form = (
        <StepForm label={ACTION_LABEL[chosen]} busy={action.busy}
          onSubmit={() => { run(() => ztax.recordNoticeSubmission(n.id, { authorityReceiptReference: ref.trim(), ...opt('evidence', evidence), ...opt('note', text) })) }}>
          {refField('Authority submission receipt (required)', true)}
          {evidenceField('Evidence', false)}
          {textField('Note', false)}
        </StepForm>
      )
      break
    case 'RECORD_OUTCOME':
      form = (
        <StepForm label={ACTION_LABEL[chosen]} busy={action.busy}
          help="Settled needs the authority’s confirmation or a settled payment; a pending payment instruction does not settle a notice."
          onSubmit={() => {
            if (amount.trim() !== '' && (!isDecimal(amount.trim()) || !CURRENCY.test(currency))) {
              setInvalid('The decided amount is a decimal such as 980.00, with a three-letter currency, recorded as written.')
              return
            }
            run(() => ztax.recordNoticeOutcome(n.id, {
              outcome, authorityReceiptReference: ref.trim(), evidence: evidence.trim(), ...opt('note', text),
              ...(amount.trim() === '' ? {} : { amount: { amount: amount.trim(), currency } }),
              ...(settled ? { settled: true } : {}), ...(settlement === '' ? {} : { settlement }),
            }))
          }}>
          <Field label="Outcome">
            <select value={outcome} onChange={(e) => { setOutcome(pick(OUTCOMES, e.target.value, 'ACCEPTED')) }}>
              {OUTCOMES.map((o) => <option key={o} value={o}>{words(o)}</option>)}
            </select>
          </Field>
          {refField('Authority receipt or decision reference (required)', true)}
          {evidenceField('The decision as received', true)}
          <Field label="Decided amount" help="As stated by the authority.">
            <input value={amount} inputMode="decimal" onChange={(e) => { setAmount(e.target.value) }} />
          </Field>
          <Field label="Currency">
            <input value={currency} maxLength={3} onChange={(e) => { setCurrency(e.target.value.toUpperCase()) }} />
          </Field>
          <Field label="Settled">
            <input type="checkbox" checked={settled} onChange={(e) => { setSettled(e.target.checked) }} />
          </Field>
          <Field label="Settlement evidence">
            <select value={settlement} onChange={(e) => { setSettlement(e.target.value === '' ? '' : pick(SETTLEMENTS, e.target.value, 'AUTHORITY_CONFIRMED')) }}>
              <option value="">—</option>
              {SETTLEMENTS.map((s) => <option key={s} value={s}>{words(s)}</option>)}
            </select>
          </Field>
          {textField('Note', false)}
        </StepForm>
      )
      break
    case 'LODGE_APPEAL':
      form = (
        <StepForm label={ACTION_LABEL[chosen]} busy={action.busy}
          help="An appeal with a hearing to come keeps the matter open and needs its next deadline. The original deadline stays in the ledger."
          onSubmit={() => {
            run(() => ztax.lodgeNoticeAppeal(n.id, {
              nextDeadline: date === '' ? { timezone: timezone.trim(), source: 'UNKNOWN', verified: false }
                : { dueDate: date, timezone: timezone.trim(), source, verified },
              evidence: evidence.trim(), ...(hearing === '' ? {} : { hearingDate: hearing }),
              ...opt('authorityReference', ref), ...opt('note', text),
            }))
          }}>
          {deadlineFields(true)}
          <Field label="Hearing date">
            <input type="date" value={hearing} onChange={(e) => { setHearing(e.target.value) }} />
          </Field>
          {evidenceField('Evidence of the appeal', true)}
          {refField('Authority appeal reference', false)}
          {textField('Note', false)}
        </StepForm>
      )
      break
    case 'CLOSE':
      form = (
        <StepForm label={ACTION_LABEL[chosen]} busy={action.busy}
          help="Closes only on the authority’s outcome with its receipt, or on a documented legal escalation. Nothing reopens a closed notice."
          onSubmit={() => { run(() => ztax.transitionNotice(n.id, { to: 'CLOSED', note: text.trim() })) }}>
          {textField('Disposition (required)', true)}
        </StepForm>
      )
      break
    case '':
      break
  }

  const hint = refusalHint(action.error)
  if (allowed.length === 0) {
    return <Empty>This notice is closed. Nothing reopens it, and its clock is not reset; a new matter is a new notice.</Empty>
  }
  return (
    <>
      <div className="ztax-filter-bar" style={{ marginBottom: 12 }}>
        {allowed.map((a) => (
          <button key={a} type="button" className={chosen === a ? 'ztax-btn-copper' : 'ztax-btn-quiet'}
            onClick={() => { setChosen(a); setInvalid(null); action.reset() }}>
            {ACTION_LABEL[a]}
          </button>
        ))}
      </div>
      {form}
      {invalid === null ? null : <p className="ztax-error-note" role="alert">{invalid}</p>}
      {action.error === null ? null : <ErrorNote error={action.error} />}
      {hint === null ? null : <p className="ztax-note">{hint}</p>}
    </>
  )
}
