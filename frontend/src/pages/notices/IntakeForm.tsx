import { useState, type FormEvent } from 'react'

import type { S } from '@zoikotax/sdk'

import { ztax } from '../../platform/client'
import { Card, ErrorNote, Field, Submit, useAction } from '../../ui/kit'
import { CURRENCY, isDecimal, nowUtcLocal, toTimestamp } from '../determine/shared'
import {
  LINK_KINDS,
  mediaTypeOf,
  opt,
  pick,
  refusalHint,
  sha256Hex,
  SOURCES,
  TYPES,
  words,
  type DeadlineSource,
  type LinkKind,
  type Notice,
  type NoticeType,
} from './model'

// Intake (W17): a notice is registered with the deadline the authority set,
// where that deadline came from and whether anyone verified it — never a
// support due date. Attachments are hashed here, in the browser; nothing is
// uploaded.

interface Attached {
  readonly sha256: string
  readonly filename: string
  readonly mediaType: string
}

export const IntakeForm = ({ onCreated }: { onCreated: (n: Notice) => void }) => {
  const [type, setType] = useState<NoticeType>('NOTICE')
  const [authority, setAuthority] = useState('')
  const [jurisdiction, setJurisdiction] = useState('')
  const [legalEntityId, setLegalEntityId] = useState('')
  const [ambiguous, setAmbiguous] = useState(false)
  const [reference, setReference] = useState('')
  const [received, setReceived] = useState(nowUtcLocal())
  const [periodStart, setPeriodStart] = useState('')
  const [periodEnd, setPeriodEnd] = useState('')
  const [amount, setAmount] = useState('')
  const [currency, setCurrency] = useState('EUR')
  const [dueDate, setDueDate] = useState('')
  const [timezone, setTimezone] = useState('Europe/Berlin')
  const [source, setSource] = useState<DeadlineSource>('STATED_ON_NOTICE')
  const [verified, setVerified] = useState(false)
  const [deadlineEvidence, setDeadlineEvidence] = useState('')
  const [sla, setSla] = useState('')
  const [linkedCase, setLinkedCase] = useState('')
  const [linkKind, setLinkKind] = useState<LinkKind>('RETURN')
  const [linkRef, setLinkRef] = useState('')
  const [links, setLinks] = useState<S<'NoticeLink'>[]>([])
  const [note, setNote] = useState('')
  const [files, setFiles] = useState<Attached[]>([])
  const [hashing, setHashing] = useState(false)
  const [invalid, setInvalid] = useState<string | null>(null)
  const action = useAction<Notice>()
  const attach = useAction<Notice>()

  const known = dueDate !== ''
  const effectiveSource: DeadlineSource = known ? source : 'UNKNOWN'
  const canVerify = known && effectiveSource !== 'ESTIMATED' && effectiveSource !== 'UNKNOWN'

  const onFiles = async (list: FileList | null) => {
    if (list === null) {
      return
    }
    setHashing(true)
    try {
      const next: Attached[] = []
      for (const f of Array.from(list)) {
        next.push({ sha256: await sha256Hex(f), filename: f.name.slice(0, 255), mediaType: mediaTypeOf(f) })
      }
      setFiles((prev) => [...prev, ...next.filter((n) => !prev.some((p) => p.sha256 === n.sha256))])
    } finally {
      setHashing(false)
    }
  }

  const submit = (e: FormEvent) => {
    e.preventDefault()
    setInvalid(null)
    const receivedAt = toTimestamp(received)
    if (receivedAt === null) {
      setInvalid('Received at is a date and time.')
      return
    }
    const slaDue = sla === '' ? null : toTimestamp(sla)
    if (sla !== '' && slaDue === null) {
      setInvalid('The internal SLA is a date and time.')
      return
    }
    if (amount.trim() !== '' && (!isDecimal(amount.trim()) || !CURRENCY.test(currency))) {
      setInvalid('The stated amount is a decimal such as 1250.00, with a three-letter currency. It is recorded as written.')
      return
    }
    if (canVerify && verified && deadlineEvidence.trim() === '') {
      setInvalid('A verified deadline says how it was verified.')
      return
    }
    const body: S<'CreateNoticeRequest'> = {
      type, authority: authority.trim(), jurisdiction: jurisdiction.trim().toUpperCase(), authorityReference: reference.trim(),
      receivedAt,
      deadline: { ...(known ? { dueDate } : {}), timezone, source: effectiveSource, verified: canVerify && verified },
      ...opt('legalEntityId', legalEntityId),
      ...(ambiguous ? { entityAmbiguous: true } : {}),
      ...(periodStart !== '' && periodEnd !== '' ? { periodStart, periodEnd } : {}),
      ...(amount.trim() === '' ? {} : { amount: { amount: amount.trim(), currency } }),
      ...opt('linkedCaseId', linkedCase),
      ...(links.length === 0 ? {} : { links }),
      ...opt('deadlineEvidence', deadlineEvidence),
      ...(slaDue === null ? {} : { slaDue }),
      ...opt('note', note),
    }
    void action.run(() => ztax.createNotice(body)).then(async (created) => {
      if (created === null) {
        return
      }
      let latest = created
      // Each document is recorded as evidence by digest after intake.
      for (const f of files) {
        const after = await attach.run(() => ztax.recordNoticeAttachment(created.id, f))
        if (after !== null) {
          latest = after
        }
      }
      setFiles([])
      onCreated(latest)
    })
  }

  const hint = refusalHint(action.error)
  return (
    <Card title="Register a notice">
      <p className="ztax-note">
        Register what the authority sent with its own deadline: the date in the authority’s calendar, its time zone, where it came
        from and whether it was verified. An unknown or unverified deadline is flagged for urgent legal review, and a notice with
        no clear recipient goes straight to legal review. Links are references only — nothing here changes a return, a ledger or a rule.
      </p>
      <form className="ztax-form" onSubmit={submit}>
        <Field label="Type">
          <select value={type} onChange={(e) => { setType(pick(TYPES, e.target.value, 'NOTICE')) }}>
            {TYPES.map((t) => <option key={t} value={t}>{words(t)}</option>)}
          </select>
        </Field>
        <Field label="Authority" help="As the legal matrix names it, e.g. DE-BZST.">
          <input value={authority} required maxLength={128} onChange={(e) => { setAuthority(e.target.value) }} />
        </Field>
        <Field label="Jurisdiction" help="ISO country or subdivision, e.g. DE or US-CA.">
          <input value={jurisdiction} required pattern="[A-Za-z]{2}(-[A-Za-z0-9]{1,3})?" onChange={(e) => { setJurisdiction(e.target.value) }} />
        </Field>
        <Field label="Authority reference" help="The authority’s own reference for the matter.">
          <input value={reference} required maxLength={200} onChange={(e) => { setReference(e.target.value) }} />
        </Field>
        <Field label="Received at (UTC)">
          <input type="datetime-local" value={received} required onChange={(e) => { setReceived(e.target.value) }} />
        </Field>
        <Field label="Recipient legal entity" help="Leave empty when the notice names none we can resolve; it then goes to legal review.">
          <input value={legalEntityId} placeholder="legal entity id" onChange={(e) => { setLegalEntityId(e.target.value) }} />
        </Field>
        <Field label="Recipient ambiguous">
          <input type="checkbox" checked={ambiguous} onChange={(e) => { setAmbiguous(e.target.checked) }} />
        </Field>
        <Field label="Period start">
          <input type="date" value={periodStart} onChange={(e) => { setPeriodStart(e.target.value) }} />
        </Field>
        <Field label="Period end">
          <input type="date" value={periodEnd} onChange={(e) => { setPeriodEnd(e.target.value) }} />
        </Field>
        <Field label="Stated amount" help="As printed on the notice. Recorded as written, never computed.">
          <input value={amount} inputMode="decimal" placeholder="1250.00" onChange={(e) => { setAmount(e.target.value) }} />
        </Field>
        <Field label="Currency">
          <input value={currency} maxLength={3} onChange={(e) => { setCurrency(e.target.value.toUpperCase()) }} />
        </Field>

        <Field label="Statutory due date" help="Leave empty if the deadline is not known yet; that is flagged, not guessed.">
          <input type="date" value={dueDate} onChange={(e) => { setDueDate(e.target.value) }} />
        </Field>
        <Field label="Deadline time zone" help="The authority’s; the deadline ends at the end of that day there.">
          <input value={timezone} list="notice-timezones" required onChange={(e) => { setTimezone(e.target.value) }} />
        </Field>
        <Field label="Deadline source">
          <select value={effectiveSource} disabled={!known} onChange={(e) => { setSource(pick(SOURCES, e.target.value, 'STATED_ON_NOTICE')) }}>
            {SOURCES.filter((s) => (known ? s !== 'UNKNOWN' : s === 'UNKNOWN')).map((s) => <option key={s} value={s}>{words(s)}</option>)}
          </select>
        </Field>
        <Field label="Deadline verified" help={canVerify ? 'Only if checked against the authority.' : 'An unknown or estimated deadline is never verified.'}>
          <input type="checkbox" checked={canVerify && verified} disabled={!canVerify} onChange={(e) => { setVerified(e.target.checked) }} />
        </Field>
        <Field label="How the deadline was established">
          <textarea value={deadlineEvidence} rows={2} maxLength={20000} onChange={(e) => { setDeadlineEvidence(e.target.value) }} />
        </Field>
        <Field label="Internal SLA (UTC)" help="Our own target. A separate clock: it never moves the statutory deadline.">
          <input type="datetime-local" value={sla} onChange={(e) => { setSla(e.target.value) }} />
        </Field>
        <Field label="Linked case" help="A related support or legal case id, as a reference.">
          <input value={linkedCase} maxLength={200} onChange={(e) => { setLinkedCase(e.target.value) }} />
        </Field>
        <Field label="Note">
          <textarea value={note} rows={2} maxLength={2000} onChange={(e) => { setNote(e.target.value) }} />
        </Field>

        <div style={{ gridColumn: '1 / -1' }}>
          <div className="ztax-filter-bar">
            <Field label="Link kind">
              <select value={linkKind} onChange={(e) => { setLinkKind(pick(LINK_KINDS, e.target.value, 'RETURN')) }}>
                {LINK_KINDS.map((k) => <option key={k} value={k}>{words(k)}</option>)}
              </select>
            </Field>
            <Field label="Referenced record id">
              <input value={linkRef} maxLength={200} onChange={(e) => { setLinkRef(e.target.value) }} />
            </Field>
            <button type="button" className="ztax-btn-quiet" disabled={linkRef.trim() === '' || links.length >= 50}
              onClick={() => { setLinks((l) => [...l, { kind: linkKind, reference: linkRef.trim() }]); setLinkRef('') }}>
              Add link
            </button>
          </div>
          {links.length === 0 ? null : (
            <ul>
              {links.map((l, i) => (
                <li key={`${l.kind}-${l.reference}-${String(i)}`}>
                  {words(l.kind)} <span className="mono">{l.reference}</span>{' '}
                  <button type="button" className="ztax-btn-quiet" onClick={() => { setLinks((all) => all.filter((_, j) => j !== i)) }}>Remove</button>
                </li>
              ))}
            </ul>
          )}
        </div>

        <div style={{ gridColumn: '1 / -1' }}>
          <Field label="Documents" help="Hashed with SHA-256 here in the browser. Only the digest, name and type are recorded; the file is not uploaded.">
            <input type="file" multiple onChange={(e) => { void onFiles(e.target.files) }} />
          </Field>
          {hashing ? <p className="muted" aria-live="polite">Hashing…</p> : null}
          {files.length === 0 ? null : (
            <ul>
              {files.map((f) => (
                <li key={f.sha256}>
                  {f.filename} <span className="mono muted">{f.mediaType} · {f.sha256}</span>{' '}
                  <button type="button" className="ztax-btn-quiet" onClick={() => { setFiles((all) => all.filter((x) => x.sha256 !== f.sha256)) }}>Remove</button>
                </li>
              ))}
            </ul>
          )}
        </div>

        <div className="ztax-form-actions"><Submit busy={action.busy || attach.busy || hashing}>Register notice</Submit></div>
        <div style={{ gridColumn: '1 / -1' }}>
          {invalid === null ? null : <p className="ztax-error-note" role="alert">{invalid}</p>}
          {action.error === null ? null : <ErrorNote error={action.error} />}
          {hint === null ? null : <p className="ztax-note">{hint}</p>}
          {attach.error === null ? null : <ErrorNote error={attach.error} />}
        </div>
      </form>
    </Card>
  )
}
