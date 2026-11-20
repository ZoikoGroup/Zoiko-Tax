import { useState, type FormEvent } from 'react'

import { instant } from '../dashboard/format'
import { ztax } from '../platform/client'
import {
  Card,
  Columns,
  Empty,
  ErrorNote,
  Field,
  Id,
  Loaded,
  newKey,
  PageHeader,
  Raw,
  Status,
  Submit,
  useAction,
  useLoad,
} from '../ui/kit'
import { TabBar, toTimestamp } from './determine/shared'
import {
  CERTIFICATE_MEANING,
  CERTIFICATE_STATUSES,
  CERTIFICATE_TYPES,
  certificateOffers,
  CRITERIA,
  documentOf,
  endOf,
  FLAG_TEXT,
  NOT_TAX_TREATMENT,
  OUTCOMES,
  reasonOf,
  refusalText,
  REGISTRATION_MEANING,
  REGISTRATION_STATUSES,
  registrationOffers,
  SCHEMES,
  type Certificate,
  type CertificateStatus,
  type CertificateType,
  type CheckOutcome,
  type CheckResult,
  type CheckSource,
  type EvidenceDocument,
  type EvidenceFlag,
  type Offer,
  type Registration,
  type RegistrationScheme,
  type RegistrationStatus,
  type ScreeningResult,
} from './registrations/model'
import { useRoutedTab } from './routedTab'
import type { PageProps } from './types'
import { all3, both, hasRole, Tiles, words } from './workspace'

// Registrations & Exemptions (Engineering Doc 17, G-048; OBL-02; OPS-13,
// OPS-14): the registrations a legal entity holds with authorities and the
// exemption certificates its buyers give it, as evidence. Documents are
// hashed here in the browser and only their digests are recorded. The cell
// enforces every rule — roles, four eyes, exact versions, dates; this page
// shows what needs attention and offers only the acts a record admits, with
// the reason when an act is not this person's to take.

type Tab = 'overview' | 'registrations' | 'certificates' | 'renewals' | 'record' | 'receive'

const TABS: readonly { value: Tab; label: string }[] = [
  { value: 'overview', label: 'Readiness' },
  { value: 'registrations', label: 'Registrations' },
  { value: 'certificates', label: 'Exemption certificates' },
  { value: 'renewals', label: 'Expiry & renewals' },
  { value: 'record', label: 'Record a registration' },
  { value: 'receive', label: 'Receive a certificate' },
]

const ATTENTION: readonly EvidenceFlag[] = [
  'PAST_EXPIRY', 'AUTHORITY_NEGATIVE', 'PENDING_VALIDATION', 'AWAITING_ACTIVATION', 'AWAITING_SECOND_REVIEW',
  'CORRECTION_REQUESTED', 'EXPIRING_SOON',
]

export const RegistrationsPage = ({ session, route, onTabChange }: PageProps) => {
  const [tab, setTab] = useRoutedTab(TABS.map((t) => t.value), 'overview', route, onTabChange)
  const [registration, setRegistration] = useState<string | null>(null)
  const [certificate, setCertificate] = useState<string | null>(null)
  const roles = session.user.roles
  const me = session.user.id
  const openRegistration = (id: string) => {
    setRegistration(id)
    setTab('registrations')
  }
  const openCertificate = (id: string) => {
    setCertificate(id)
    setTab('certificates')
  }

  return (
    <div className="ztax-page">
      <PageHeader
        title="Registrations & Exemptions"
        subtitle="Registrations with authorities and buyers' exemption certificates, held as evidence: checked, approved by two people, and watched for expiry."
      />
      <p className="ztax-note" role="note">{NOT_TAX_TREATMENT}</p>
      <TabBar label="Registrations and exemptions" tabs={TABS} current={tab} onSelect={setTab} />
      {tab === 'overview' ? <Overview onRegistration={openRegistration} onCertificate={openCertificate} />
        : tab === 'registrations' ? <Registrations roles={roles} me={me} selected={registration} setSelected={setRegistration} />
        : tab === 'certificates' ? <Certificates roles={roles} me={me} selected={certificate} setSelected={setCertificate} />
        : tab === 'renewals' ? <Renewals onRegistration={openRegistration} onCertificate={openCertificate} />
        : tab === 'record' ? <RecordRegistration roles={roles} onOpen={openRegistration} />
        : <ReceiveCertificate roles={roles} onOpen={openCertificate} />}
    </div>
  )
}

const Flags = ({ flags }: { flags: readonly EvidenceFlag[] }) =>
  flags.length === 0 ? <span className="muted">—</span> : (
    <ul className="ztax-plain-list" style={{ margin: 0, paddingLeft: 16 }}>
      {flags.map((f) => <li key={f}>{FLAG_TEXT[f]}</li>)}
    </ul>
  )

const Refusal = ({ error }: { error: unknown }) => {
  if (error === null || error === undefined) {
    return null
  }
  const text = refusalText(reasonOf(error))
  return text === null ? <div style={{ gridColumn: '1 / -1' }}><ErrorNote error={error} /></div> : (
    <p className="ztax-error-note" role="alert" style={{ gridColumn: '1 / -1' }}>{text}</p>
  )
}

/** A file picker that hashes the file here and reports the digest. */
const DocumentPicker = ({ label, value, onChange }: {
  label: string
  value: EvidenceDocument | null
  onChange: (d: EvidenceDocument | null) => void
}) => {
  const [problem, setProblem] = useState<string | null>(null)
  return (
    <Field label={label} help="PDF or image, at most 25 MiB. Hashed with SHA-256 here in the browser; only the digest, name, type and size are recorded. The file is not uploaded.">
      <input type="file" accept="application/pdf,image/png,image/jpeg,image/tiff,image/webp" onChange={(e) => {
        const f = e.target.files?.[0]
        if (f === undefined) {
          onChange(null)
          return
        }
        void documentOf(f).then((d) => {
          if (typeof d === 'string') {
            setProblem(d)
            onChange(null)
          } else {
            setProblem(null)
            onChange(d)
          }
        })
      }} />
      {problem === null ? null : <span className="ztax-error-note" role="alert">{problem}</span>}
      {value === null ? null : <span className="mono muted" style={{ overflowWrap: 'anywhere' }}>SHA-256 {value.sha256} · {value.bytes} bytes</span>}
    </Field>
  )
}

// ---------------------------------------------------------------------------
// readiness
// ---------------------------------------------------------------------------

const Overview = ({ onRegistration, onCertificate }: { onRegistration: (id: string) => void; onCertificate: (id: string) => void }) => {
  const { load, reload } = useLoad(
    (signal) => both(ztax.listRegistrations({ limit: 500 }, signal), ztax.listExemptionCertificates({ limit: 500 }, signal)),
    [],
  )
  return (
    <Loaded load={load}>
      {([regs, certs]) => {
        const rows = [
          ...regs.registrations.filter((r) => r.flags.some((f) => ATTENTION.includes(f))).map((r) => ({
            key: r.id, kind: 'Registration', name: `${words(r.scheme)} · ${r.jurisdiction}`, status: r.status, flags: r.flags,
            open: () => { onRegistration(r.id) },
          })),
          ...certs.certificates.filter((c) => c.flags.some((f) => ATTENTION.includes(f))).map((c) => ({
            key: c.id, kind: 'Certificate', name: `${c.buyerName} · ${c.jurisdiction}`, status: c.status, flags: c.flags,
            open: () => { onCertificate(c.id) },
          })),
        ].sort((a, b) => Math.min(...a.flags.map((f) => ATTENTION.indexOf(f))) - Math.min(...b.flags.map((f) => ATTENTION.indexOf(f))))
        const checks = regs.registrations.map((r) => r.lastCheck?.checkedAt).filter((t): t is string => t !== undefined).sort()
        return (
          <>
            <Tiles tiles={[
              { label: 'Registrations in force', count: regs.registrations.filter((r) => r.inForce).length, tone: 'ok' },
              { label: 'Registrations pending', count: regs.registrations.filter((r) => ['DRAFT', 'SUBMITTED', 'VERIFIED'].includes(r.status)).length, tone: 'neutral', note: 'Pending is not registered' },
              { label: 'Certificates approved in force', count: certs.certificates.filter((c) => c.inForce).length, tone: 'ok' },
              { label: 'Certificates in review', count: certs.certificates.filter((c) => ['SUBMITTED', 'SCREENING', 'REVIEW_REQUIRED'].includes(c.status)).length, tone: 'neutral' },
              { label: 'Needing action', count: rows.length, tone: rows.length > 0 ? 'warn' : 'ok' },
            ]} />
            <Card title="Action queue" aside={
              <span className="muted">
                Last authority check: {checks.length === 0 ? 'none recorded' : instant(checks[checks.length - 1] ?? '')}
                {' '}<button type="button" className="ztax-btn-quiet" onClick={reload}>Refresh</button>
              </span>
            }>
              {rows.length === 0 ? (
                <Empty>Nothing needs attention. A jurisdiction with no registration here is unregistered or unknown — never exempt.</Empty>
              ) : (
                <table className="ztax-action-table">
                  <thead><tr><th>Record</th><th>Subject</th><th>Status</th><th>Why</th><th>Action</th></tr></thead>
                  <tbody>
                    {rows.map((r) => (
                      <tr key={r.key}>
                        <td>{r.kind}</td><td>{r.name}</td><td><Status value={r.status} /></td><td><Flags flags={r.flags} /></td>
                        <td><button type="button" className="ztax-table-action-btn" onClick={r.open}>Open</button></td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
            </Card>
          </>
        )
      }}
    </Loaded>
  )
}

// ---------------------------------------------------------------------------
// registrations
// ---------------------------------------------------------------------------

const isRegStatus = (v: string): v is RegistrationStatus => (REGISTRATION_STATUSES as readonly string[]).includes(v)
const isScheme = (v: string): v is RegistrationScheme => SCHEMES.some((s) => s.value === v)

const Registrations = ({ roles, me, selected, setSelected }: {
  roles: readonly string[]
  me: string
  selected: string | null
  setSelected: (id: string | null) => void
}) => {
  const [status, setStatus] = useState<RegistrationStatus | ''>('')
  const [scheme, setScheme] = useState<RegistrationScheme | ''>('')
  const [jurisdiction, setJurisdiction] = useState('')
  const j = jurisdiction.trim().toUpperCase()
  const { load, reload } = useLoad(
    (signal) => ztax.listRegistrations({
      ...(status === '' ? {} : { status }),
      ...(scheme === '' ? {} : { scheme }),
      ...(/^[A-Z]{2}(-[A-Z0-9]{1,3})?$/.test(j) ? { jurisdiction: j } : {}),
      limit: 500,
    }, signal),
    [status, scheme, j],
  )
  return (
    <>
      <Card title="Registration & permit inventory" aside={
        <div className="ztax-filter-bar">
          <Field label="Status">
            <select value={status} onChange={(e) => { setStatus(isRegStatus(e.target.value) ? e.target.value : '') }}>
              <option value="">All</option>
              {REGISTRATION_STATUSES.map((s) => <option key={s} value={s}>{words(s)}</option>)}
            </select>
          </Field>
          <Field label="Scheme">
            <select value={scheme} onChange={(e) => { setScheme(isScheme(e.target.value) ? e.target.value : '') }}>
              <option value="">All</option>
              {SCHEMES.map((s) => <option key={s.value} value={s.value}>{s.label}</option>)}
            </select>
          </Field>
          <Field label="Jurisdiction">
            <input value={jurisdiction} placeholder="US, US-NY, GB" maxLength={6} onChange={(e) => { setJurisdiction(e.target.value) }} />
          </Field>
          <button type="button" className="ztax-btn-quiet" onClick={reload}>Refresh</button>
        </div>
      }>
        <Loaded load={load}>
          {(data) => data.registrations.length === 0 ? (
            <Empty>No registration matches. No registration is NOT REGISTERED, never "tax exempt".</Empty>
          ) : (
            <div style={{ overflowX: 'auto' }}>
              <table className="ztax-action-table">
                <thead>
                  <tr><th>Scheme</th><th>Number</th><th>Jurisdiction</th><th>Authority</th><th>Effective</th><th>Status</th><th>Attention</th><th>Action</th></tr>
                </thead>
                <tbody>
                  {data.registrations.map((r) => (
                    <tr key={r.id} className={r.id === selected ? 'ztax-row-selected' : undefined}>
                      <td>{words(r.scheme)}</td>
                      <td className="mono">{r.registrationNumber}</td>
                      <td>{r.jurisdiction}</td>
                      <td>{r.authority}</td>
                      <td style={{ whiteSpace: 'nowrap' }}>{r.effectiveFrom} → {r.effectiveTo ?? 'open'}</td>
                      <td><Status value={r.status} />{r.inForce ? <span className="muted"> · in force</span> : null}</td>
                      <td><Flags flags={r.flags} /></td>
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
        <RegistrationDetail key={selected} id={selected} roles={roles} me={me} onChanged={reload}
          onClose={() => { setSelected(null) }} />
      )}
    </>
  )
}

const RegistrationDetail = ({ id, roles, me, onChanged, onClose }: {
  id: string
  roles: readonly string[]
  me: string
  onChanged: () => void
  onClose: () => void
}) => {
  const { load, reload } = useLoad((signal) => ztax.getRegistration(id, signal), [id])
  const changed = () => {
    reload()
    onChanged()
  }
  return (
    <Loaded load={load}>
      {(r) => (
        <Columns>
          <Card title={`${words(r.scheme)} · ${r.jurisdiction}`} aside={<button type="button" className="ztax-btn-quiet" onClick={onClose}>Close</button>}>
            <dl className="ztax-kv">
              <dt>Registration</dt><dd><Id value={r.id} /></dd>
              <dt>Status</dt><dd><Status value={r.status} /> <span className="muted">{REGISTRATION_MEANING[r.status]}</span></dd>
              <dt>In force</dt><dd>{r.inForce ? 'Yes — active and within its dates' : 'No'}</dd>
              <dt>Number</dt><dd className="mono">{r.registrationNumber}{r.numberMasked ? <span className="muted"> (masked for your role)</span> : null}</dd>
              <dt>Authority</dt><dd>{r.authority}</dd>
              <dt>Legal entity</dt><dd><Id value={r.legalEntityId} /></dd>
              <dt>Effective</dt><dd>{r.effectiveFrom} → {r.effectiveTo ?? 'open-ended'} <span className="muted">(UTC dates)</span></dd>
              <dt>Supersedes</dt><dd><Id value={r.supersedes} /></dd>
              <dt>Version</dt><dd>{r.version}</dd>
              <dt>Attention</dt><dd><Flags flags={r.flags} /></dd>
              <dt>Last check</dt>
              <dd>
                {r.lastCheck === undefined ? <span className="muted">No authority check recorded</span> : (
                  <span>
                    <Status value={r.lastCheck.outcome} /> {r.lastCheck.source} · {instant(r.lastCheck.checkedAt)}
                    {r.lastCheck.reference === undefined ? null : <span className="mono"> · {r.lastCheck.reference}</span>}
                    <span className="muted"> · a person's lookup; no authority adapter is connected</span>
                  </span>
                )}
              </dd>
            </dl>
            <h3 style={{ marginTop: 16 }}>Supporting documents</h3>
            {r.attachments.length === 0 ? <Empty>No documents recorded.</Empty> : (
              <ul className="ztax-plain-list">
                {r.attachments.map((a) => (
                  <li key={a.sha256}>{a.filename} <span className="mono muted" style={{ overflowWrap: 'anywhere' }}>{a.mediaType} · {a.bytes} bytes · {a.sha256}</span></li>
                ))}
              </ul>
            )}
            <Raw value={r} />
          </Card>
          <Card title="History and actions">
            <table className="ztax-action-table">
              <thead><tr><th>#</th><th>Event</th><th>Status</th><th>Detail</th><th>When</th><th>By</th></tr></thead>
              <tbody>
                {r.history.map((e) => (
                  <tr key={e.seq}>
                    <td>{e.seq}</td><td>{words(e.kind)}</td><td><Status value={e.status} /></td>
                    <td>
                      {e.reason ?? null}
                      {e.check === undefined ? null : <span> {e.check.source}: {words(e.check.outcome)}{e.check.reference === undefined ? '' : ` (${e.check.reference})`}</span>}
                      {e.attachment === undefined ? null : <span> {e.attachment.filename}</span>}
                    </td>
                    <td>{instant(e.recordedAt)}</td><td><Id value={e.recordedBy} /></td>
                  </tr>
                ))}
              </tbody>
            </table>
            <RegistrationActions registration={r} offers={registrationOffers(r, roles, me)} onChanged={changed} />
          </Card>
        </Columns>
      )}
    </Loaded>
  )
}

const RegistrationActions = ({ registration, offers, onChanged }: { registration: Registration; offers: Offer[]; onChanged: () => void }) => {
  const [act, setAct] = useState<string | null>(null)
  const current = offers.find((o) => o.act === act && o.blocked === null)
  return (
    <div style={{ marginTop: 12 }}>
      <div className="ztax-filter-bar">
        {offers.map((o) => o.blocked === null ? (
          <button key={o.act} type="button" className="ztax-btn-quiet" aria-pressed={o.act === act} onClick={() => { setAct(o.act) }}>{o.label}</button>
        ) : (
          <span key={o.act} className="muted" title={o.blocked}>{o.label}: {o.blocked}</span>
        ))}
      </div>
      {current === undefined ? null
        : current.act === 'CHECK' ? <CheckForm registration={registration} onDone={() => { setAct(null); onChanged() }} />
        : current.act === 'ATTACH' ? <AttachForm registration={registration} onDone={() => { setAct(null); onChanged() }} />
        : <RegistrationMove registration={registration} offer={current} onDone={() => { setAct(null); onChanged() }} />}
    </div>
  )
}

const RegistrationMove = ({ registration, offer, onDone }: { registration: Registration; offer: Offer; onDone: () => void }) => {
  const [reason, setReason] = useState('')
  const action = useAction<Registration>()
  const to = offer.act as RegistrationStatus
  const optional = to === 'SUBMITTED' || to === 'EXPIRED'
  const submit = (e: FormEvent) => {
    e.preventDefault()
    void action.run(() => ztax.transitionRegistration(registration.id, {
      to, expectedVersion: registration.version, ...(reason.trim() === '' ? {} : { reason: reason.trim() }),
    })).then((done) => { if (done !== null) onDone() })
  }
  return (
    <form className="ztax-form" onSubmit={submit} style={{ marginTop: 12 }}>
      <Field label={optional ? 'Note (optional)' : 'Reason'}
        help={to === 'ACTIVE' ? `Applies to version ${registration.version} exactly; anything recorded since is a version conflict.` : 'Recorded in the history; nothing is edited.'}>
        <textarea value={reason} required={!optional} maxLength={2000} onChange={(e) => { setReason(e.target.value) }} />
      </Field>
      <div className="ztax-form-actions"><Submit busy={action.busy}>{offer.label}</Submit></div>
      <Refusal error={action.error} />
    </form>
  )
}

const CheckForm = ({ registration, onDone }: { registration: Registration; onDone: () => void }) => {
  const sources = SCHEMES.find((s) => s.value === registration.scheme)?.sources ?? []
  const [source, setSource] = useState<CheckSource | ''>(sources[0] ?? '')
  const [outcome, setOutcome] = useState<CheckOutcome>('UNAVAILABLE')
  const [reference, setReference] = useState('')
  const [checkedAt, setCheckedAt] = useState(new Date().toISOString().slice(0, 16))
  const [note, setNote] = useState('')
  const [receipt, setReceipt] = useState<EvidenceDocument | null>(null)
  const action = useAction<Registration>()
  const submit = (e: FormEvent) => {
    e.preventDefault()
    const at = toTimestamp(checkedAt)
    if (source === '' || at === null) {
      return
    }
    void action.run(() => ztax.recordRegistrationCheck(registration.id, {
      source, method: 'MANUAL_LOOKUP', outcome, checkedAt: at,
      ...(reference.trim() === '' ? {} : { reference: reference.trim() }),
      ...(note.trim() === '' ? {} : { note: note.trim() }),
      ...(receipt === null ? {} : { receiptSha256: receipt.sha256 }),
    })).then((done) => { if (done !== null) onDone() })
  }
  return (
    <form className="ztax-form" onSubmit={submit} style={{ marginTop: 12 }}>
      <p className="ztax-note" role="note" style={{ gridColumn: '1 / -1' }}>
        No authority adapter is connected. Record only what the authority itself returned to your lookup; inconclusive or unavailable is never valid.
      </p>
      <Field label="Authority source">
        <select value={source} required onChange={(e) => { setSource(sources.find((s) => s === e.target.value) ?? '') }}>
          {sources.map((s) => <option key={s} value={s}>{words(s)}</option>)}
        </select>
      </Field>
      <Field label="Outcome">
        <select value={outcome} onChange={(e) => { setOutcome(OUTCOMES.find((o) => o.value === e.target.value)?.value ?? 'UNAVAILABLE') }}>
          {OUTCOMES.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
        </select>
      </Field>
      <Field label="Authority reference" help="Required for verified or negative: the authority's own reference or receipt.">
        <input value={reference} maxLength={200} required={outcome === 'VERIFIED' || outcome === 'NEGATIVE'} onChange={(e) => { setReference(e.target.value) }} />
      </Field>
      <Field label="Checked at (UTC)">
        <input type="datetime-local" value={checkedAt} required onChange={(e) => { setCheckedAt(e.target.value) }} />
      </Field>
      <DocumentPicker label="Saved receipt (optional)" value={receipt} onChange={setReceipt} />
      <Field label="Note (optional)">
        <textarea value={note} maxLength={2000} onChange={(e) => { setNote(e.target.value) }} />
      </Field>
      <div className="ztax-form-actions"><Submit busy={action.busy}>Record check</Submit></div>
      <Refusal error={action.error} />
    </form>
  )
}

const AttachForm = ({ registration, onDone }: { registration: Registration; onDone: () => void }) => {
  const [doc, setDoc] = useState<EvidenceDocument | null>(null)
  const action = useAction<Registration>()
  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (doc === null) {
      return
    }
    void action.run(() => ztax.recordRegistrationAttachment(registration.id, doc)).then((done) => { if (done !== null) onDone() })
  }
  return (
    <form className="ztax-form" onSubmit={submit} style={{ marginTop: 12 }}>
      <DocumentPicker label="Supporting document" value={doc} onChange={setDoc} />
      <div className="ztax-form-actions"><Submit busy={action.busy}>Record document</Submit></div>
      <Refusal error={action.error} />
    </form>
  )
}

const RecordRegistration = ({ roles, onOpen }: { roles: readonly string[]; onOpen: (id: string) => void }) => {
  const [key, setKey] = useState(newKey)
  const [scheme, setScheme] = useState<RegistrationScheme>('US_USAC_499_FILER_ID')
  const [jurisdiction, setJurisdiction] = useState('US')
  const [authority, setAuthority] = useState('USAC')
  const [number, setNumber] = useState('')
  const [from, setFrom] = useState('')
  const [to, setTo] = useState('')
  const [docs, setDocs] = useState<EvidenceDocument[]>([])
  const [pending, setPending] = useState<EvidenceDocument | null>(null)
  const action = useAction<Registration>()
  if (!hasRole(roles, 'OPERATOR', 'ANALYST')) {
    return <Card><Empty>Recording a registration needs the OPERATOR or ANALYST role.</Empty></Card>
  }
  const send = (submit: boolean) => {
    void action.run(() => ztax.createRegistration(key, {
      scheme, jurisdiction: jurisdiction.trim().toUpperCase(), authority: authority.trim(), registrationNumber: number.trim(),
      effectiveFrom: from, ...(to === '' ? {} : { effectiveTo: to }), attachments: docs, submit,
    })).then((done) => {
      if (done !== null) {
        setKey(newKey())
        onOpen(done.id)
      }
    })
  }
  const hint = SCHEMES.find((s) => s.value === scheme)?.hint ?? ''
  return (
    <Card title="Record a registration">
      <p className="ztax-note" role="note">Recording is not registering. A submitted registration is SUBMITTED until an authority check verifies it and a second person activates it.</p>
      <form className="ztax-form" onSubmit={(e) => { e.preventDefault(); send(true) }}>
        <Field label="Scheme" help="An FRN, a 499 Filer ID and a 498 ID are separate registrations. Enter each under its own scheme.">
          <select value={scheme} onChange={(e) => { setScheme(SCHEMES.find((s) => s.value === e.target.value)?.value ?? scheme) }}>
            {SCHEMES.map((s) => <option key={s.value} value={s.value}>{s.label}</option>)}
          </select>
        </Field>
        <Field label="Registration number" help={`Format: ${hint}`}>
          <input value={number} required maxLength={64} onChange={(e) => { setNumber(e.target.value) }} />
        </Field>
        <Field label="Jurisdiction" help="A country, or a country and subdivision: US, US-NY, GB, DE.">
          <input value={jurisdiction} required maxLength={6} onChange={(e) => { setJurisdiction(e.target.value) }} />
        </Field>
        <Field label="Authority">
          <input value={authority} required maxLength={128} onChange={(e) => { setAuthority(e.target.value) }} />
        </Field>
        <Field label="Effective from">
          <input type="date" value={from} required onChange={(e) => { setFrom(e.target.value) }} />
        </Field>
        <Field label="Effective to (optional)">
          <input type="date" value={to} onChange={(e) => { setTo(e.target.value) }} />
        </Field>
        <DocumentPicker label="Supporting document" value={pending} onChange={(d) => {
          setPending(d)
          if (d !== null) setDocs((all) => (all.some((x) => x.sha256 === d.sha256) ? all : [...all, d]))
        }} />
        {docs.length === 0 ? null : (
          <ul className="ztax-plain-list" style={{ gridColumn: '1 / -1' }}>
            {docs.map((d) => (
              <li key={d.sha256}>{d.filename} <span className="mono muted">{d.sha256.slice(0, 16)}…</span>{' '}
                <button type="button" className="ztax-btn-quiet" onClick={() => { setDocs((all) => all.filter((x) => x.sha256 !== d.sha256)) }}>Remove</button>
              </li>
            ))}
          </ul>
        )}
        <div className="ztax-form-actions">
          <Submit busy={action.busy}>Submit for review</Submit>
          <button type="button" className="ztax-btn-quiet" disabled={action.busy} onClick={() => { send(false) }}>Save as draft</button>
        </div>
        <Refusal error={action.error} />
      </form>
    </Card>
  )
}

// ---------------------------------------------------------------------------
// exemption certificates
// ---------------------------------------------------------------------------

const isCertStatus = (v: string): v is CertificateStatus => (CERTIFICATE_STATUSES as readonly string[]).includes(v)

const Certificates = ({ roles, me, selected, setSelected }: {
  roles: readonly string[]
  me: string
  selected: string | null
  setSelected: (id: string | null) => void
}) => {
  const [status, setStatus] = useState<CertificateStatus | ''>('')
  const [buyer, setBuyer] = useState('')
  const { load, reload } = useLoad(
    (signal) => ztax.listExemptionCertificates({
      ...(status === '' ? {} : { status }),
      ...(buyer.trim() === '' ? {} : { buyerRef: buyer.trim() }),
      limit: 500,
    }, signal),
    [status, buyer],
  )
  return (
    <>
      <Card title="Customer & certificate register" aside={
        <div className="ztax-filter-bar">
          <Field label="Status">
            <select value={status} onChange={(e) => { setStatus(isCertStatus(e.target.value) ? e.target.value : '') }}>
              <option value="">All</option>
              {CERTIFICATE_STATUSES.map((s) => <option key={s} value={s}>{words(s)}</option>)}
            </select>
          </Field>
          <Field label="Buyer reference">
            <input value={buyer} maxLength={200} onChange={(e) => { setBuyer(e.target.value) }} />
          </Field>
          <button type="button" className="ztax-btn-quiet" onClick={reload}>Refresh</button>
        </div>
      }>
        <Loaded load={load}>
          {(data) => data.certificates.length === 0 ? (
            <Empty>No certificate matches. With no approved certificate, nothing is presumed exempt.</Empty>
          ) : (
            <div style={{ overflowX: 'auto' }}>
              <table className="ztax-action-table">
                <thead>
                  <tr><th>Buyer</th><th>Type</th><th>Jurisdiction</th><th>Issued → expires</th><th>Version</th><th>Status</th><th>Attention</th><th>Action</th></tr>
                </thead>
                <tbody>
                  {data.certificates.map((c) => (
                    <tr key={c.id} className={c.id === selected ? 'ztax-row-selected' : undefined}>
                      <td>{c.buyerName} <span className="mono muted">{c.buyerRef}</span></td>
                      <td>{words(c.certificateType)}</td>
                      <td>{c.jurisdiction}</td>
                      <td style={{ whiteSpace: 'nowrap' }}>{c.issuedOn} → {c.expiresOn ?? 'none stated'}</td>
                      <td>v{c.evidenceVersion}</td>
                      <td><Status value={c.status} />{c.inForce ? <span className="muted"> · in force</span> : null}</td>
                      <td><Flags flags={c.flags} /></td>
                      <td>
                        <button type="button" className="ztax-table-action-btn" aria-pressed={c.id === selected}
                          onClick={() => { setSelected(c.id) }}>Open</button>
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
        <CertificateDetail key={selected} id={selected} roles={roles} me={me} onChanged={reload}
          onClose={() => { setSelected(null) }} />
      )}
    </>
  )
}

const CertificateDetail = ({ id, roles, me, onChanged, onClose }: {
  id: string
  roles: readonly string[]
  me: string
  onChanged: () => void
  onClose: () => void
}) => {
  const { load, reload } = useLoad((signal) => ztax.getExemptionCertificate(id, signal), [id])
  const changed = () => {
    reload()
    onChanged()
  }
  return (
    <Loaded load={load}>
      {(c) => (
        <Columns>
          <Card title={`${c.buyerName} · ${words(c.certificateType)} · ${c.jurisdiction}`} aside={<button type="button" className="ztax-btn-quiet" onClick={onClose}>Close</button>}>
            <dl className="ztax-kv">
              <dt>Certificate</dt><dd><Id value={c.id} /></dd>
              <dt>Status</dt><dd><Status value={c.status} /> <span className="muted">{CERTIFICATE_MEANING[c.status]}</span></dd>
              <dt>In force</dt><dd>{c.inForce ? 'Yes — approved evidence within its period' : 'No'}</dd>
              <dt>Buyer</dt><dd>{c.buyerName} <span className="mono muted">{c.buyerRef}</span></dd>
              <dt>Seller entity</dt><dd><Id value={c.legalEntityId} /></dd>
              <dt>Claimed exemption</dt><dd>{c.exemptionReason}</dd>
              <dt>Certificate number</dt><dd className="mono">{c.certificateNumber ?? '—'}</dd>
              <dt>Product scope (as written)</dt><dd>{c.productScope.length === 0 ? '—' : c.productScope.join(', ')}</dd>
              <dt>Issued → expires</dt><dd>{c.issuedOn} → {c.expiresOn ?? 'none stated'}</dd>
              <dt>Approved period</dt>
              <dd>{c.approval === undefined ? '—' : (
                <span>{c.approval.effectiveFrom} → {c.approval.effectiveTo ?? 'open'} · recommended by <Id value={c.approval.recommendedBy} />, confirmed by <Id value={c.approval.confirmedBy} /></span>
              )}</dd>
              <dt>Supersedes</dt><dd><Id value={c.supersedes} /></dd>
              <dt>Evidence version</dt><dd>v{c.evidenceVersion} · <span className="mono" style={{ overflowWrap: 'anywhere' }}>{c.document.filename} · {c.document.sha256}</span></dd>
              <dt>Attention</dt><dd><Flags flags={c.flags} /></dd>
            </dl>
            {c.screening === undefined ? null : (
              <>
                <h3 style={{ marginTop: 16 }}>Screening of v{c.evidenceVersion}</h3>
                <table className="ztax-action-table">
                  <tbody>
                    {CRITERIA.map((k) => <tr key={k.key}><td>{k.label}</td><td><Status value={c.screening?.[k.key]} /></td></tr>)}
                    <tr><td><strong>Result</strong></td><td><Status value={c.screening.result} /></td></tr>
                  </tbody>
                </table>
              </>
            )}
            {c.pendingRecommendation === undefined ? null : (
              <p className="ztax-note" role="note" style={{ marginTop: 12 }}>
                Recommended {words(c.pendingRecommendation.recommendation)} on v{c.pendingRecommendation.evidenceVersion}
                {c.pendingRecommendation.effectiveFrom === undefined ? '' : ` for ${c.pendingRecommendation.effectiveFrom} → ${c.pendingRecommendation.effectiveTo ?? 'open'}`}
                {' '}by <Id value={c.pendingRecommendation.recordedBy} />: {c.pendingRecommendation.rationale}. Awaiting a different checker.
              </p>
            )}
            <Raw value={c} />
          </Card>
          <Card title="Documents, history and actions">
            <table className="ztax-action-table">
              <thead><tr><th>#</th><th>Event</th><th>Status</th><th>v</th><th>Detail</th><th>When</th><th>By</th></tr></thead>
              <tbody>
                {c.history.map((e) => (
                  <tr key={e.seq}>
                    <td>{e.seq}</td><td>{words(e.kind)}</td><td><Status value={e.status} /></td><td>{e.evidenceVersion}</td>
                    <td>
                      {e.document === undefined ? null : <span>{e.document.filename} <span className="mono muted">{e.document.sha256.slice(0, 12)}…</span> </span>}
                      {e.screening === undefined ? null : <span>{words(e.screening.result)} </span>}
                      {e.recommendation === undefined ? null : <span>{words(e.recommendation)} </span>}
                      {e.decision === undefined ? null : <span>{words(e.decision)} </span>}
                      {e.note ?? null}
                    </td>
                    <td>{instant(e.recordedAt)}</td><td><Id value={e.recordedBy} /></td>
                  </tr>
                ))}
              </tbody>
            </table>
            <CertificateActions certificate={c} offers={certificateOffers(c, roles, me)} onChanged={changed} />
          </Card>
        </Columns>
      )}
    </Loaded>
  )
}

const CertificateActions = ({ certificate, offers, onChanged }: { certificate: Certificate; offers: Offer[]; onChanged: () => void }) => {
  const [act, setAct] = useState<string | null>(null)
  const current = offers.find((o) => o.act === act && o.blocked === null)
  const done = () => {
    setAct(null)
    onChanged()
  }
  return (
    <div style={{ marginTop: 12 }}>
      <div className="ztax-filter-bar">
        {offers.map((o) => o.blocked === null ? (
          <button key={o.act} type="button" className="ztax-btn-quiet" aria-pressed={o.act === act} onClick={() => { setAct(o.act) }}>{o.label}</button>
        ) : (
          <span key={o.act} className="muted" title={o.blocked}>{o.label}: {o.blocked}</span>
        ))}
      </div>
      {current === undefined ? null
        : current.act === 'VERSION' ? <VersionForm certificate={certificate} onDone={done} />
        : current.act === 'SCREEN' ? <ScreenForm certificate={certificate} onDone={done} />
        : current.act === 'RECOMMEND' ? <RecommendForm certificate={certificate} onDone={done} />
        : current.act === 'CONFIRM' || current.act === 'RETURN' ? <DecideForm certificate={certificate} decision={current.act} label={current.label} onDone={done} />
        : <CertificateMove certificate={certificate} offer={current} onDone={done} />}
    </div>
  )
}

const VersionForm = ({ certificate, onDone }: { certificate: Certificate; onDone: () => void }) => {
  const [doc, setDoc] = useState<EvidenceDocument | null>(null)
  const [note, setNote] = useState('')
  const action = useAction<Certificate>()
  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (doc === null) {
      return
    }
    void action.run(() => ztax.addExemptionCertificateVersion(certificate.id, {
      document: doc, ...(note.trim() === '' ? {} : { note: note.trim() }),
    })).then((d) => { if (d !== null) onDone() })
  }
  return (
    <form className="ztax-form" onSubmit={submit} style={{ marginTop: 12 }}>
      <DocumentPicker label="Corrected document" value={doc} onChange={setDoc} />
      <Field label="Note (optional)" help="The certificate returns to SUBMITTED as the next evidence version; earlier screening and recommendations no longer count.">
        <textarea value={note} maxLength={2000} onChange={(e) => { setNote(e.target.value) }} />
      </Field>
      <div className="ztax-form-actions"><Submit busy={action.busy}>Record version {certificate.evidenceVersion + 1}</Submit></div>
      <Refusal error={action.error} />
    </form>
  )
}

const RESULTS: readonly { value: ScreeningResult; label: string }[] = [
  { value: 'DOCUMENT_COMPLETE', label: 'Document complete' },
  { value: 'INCOMPLETE', label: 'Incomplete — request a corrected document' },
  { value: 'INCONSISTENT', label: 'Inconsistent — request a corrected document' },
  { value: 'NEEDS_LEGAL_DECISION', label: 'Needs a specialist legal decision' },
]

const ScreenForm = ({ certificate, onDone }: { certificate: Certificate; onDone: () => void }) => {
  const [checks, setChecks] = useState<Record<string, CheckResult>>(() => Object.fromEntries(CRITERIA.map((k) => [k.key, 'PASS'])))
  const [result, setResult] = useState<ScreeningResult>('DOCUMENT_COMPLETE')
  const [note, setNote] = useState('')
  const action = useAction<Certificate>()
  const value = (k: string): CheckResult => checks[k] ?? 'PASS'
  const submit = (e: FormEvent) => {
    e.preventDefault()
    void action.run(() => ztax.screenExemptionCertificate(certificate.id, {
      screening: {
        sellerMatches: value('sellerMatches'), buyerMatches: value('buyerMatches'), jurisdictionMatches: value('jurisdictionMatches'),
        formAccepted: value('formAccepted'), signaturePresent: value('signaturePresent'), datesValid: value('datesValid'),
        numberPresent: value('numberPresent'), result,
      },
      ...(note.trim() === '' ? {} : { note: note.trim() }),
    })).then((d) => { if (d !== null) onDone() })
  }
  return (
    <form className="ztax-form" onSubmit={submit} style={{ marginTop: 12 }}>
      {CRITERIA.map((k) => (
        <Field key={k.key} label={k.label}>
          <select value={value(k.key)} onChange={(e) => {
            const v = e.target.value
            setChecks((all) => ({ ...all, [k.key]: v === 'FAIL' ? 'FAIL' : v === 'NOT_APPLICABLE' ? 'NOT_APPLICABLE' : 'PASS' }))
          }}>
            <option value="PASS">Pass</option><option value="FAIL">Fail</option><option value="NOT_APPLICABLE">Not applicable</option>
          </select>
        </Field>
      ))}
      <Field label="Result" help="Complete is not approved. A different form may be required per jurisdiction; nothing here is a global default.">
        <select value={result} onChange={(e) => { setResult(RESULTS.find((r) => r.value === e.target.value)?.value ?? 'DOCUMENT_COMPLETE') }}>
          {RESULTS.map((r) => <option key={r.value} value={r.value}>{r.label}</option>)}
        </select>
      </Field>
      <Field label="Finding" help="Required unless the document is complete: what is missing, inconsistent or needs a legal decision.">
        <textarea value={note} maxLength={2000} required={result !== 'DOCUMENT_COMPLETE'} onChange={(e) => { setNote(e.target.value) }} />
      </Field>
      <div className="ztax-form-actions"><Submit busy={action.busy}>Record screening of v{certificate.evidenceVersion}</Submit></div>
      <Refusal error={action.error} />
    </form>
  )
}

const RecommendForm = ({ certificate, onDone }: { certificate: Certificate; onDone: () => void }) => {
  const [approve, setApprove] = useState(true)
  const [from, setFrom] = useState(certificate.issuedOn)
  const [to, setTo] = useState(certificate.expiresOn ?? '')
  const [rationale, setRationale] = useState('')
  const action = useAction<Certificate>()
  const submit = (e: FormEvent) => {
    e.preventDefault()
    void action.run(() => ztax.recommendExemptionCertificate(certificate.id, {
      recommendation: approve ? 'APPROVE' : 'REJECT', rationale: rationale.trim(),
      ...(approve ? { effectiveFrom: from } : {}), ...(approve && to !== '' ? { effectiveTo: to } : {}),
    })).then((d) => { if (d !== null) onDone() })
  }
  return (
    <form className="ztax-form" onSubmit={submit} style={{ marginTop: 12 }}>
      <Field label="Recommendation">
        <select value={approve ? 'APPROVE' : 'REJECT'} onChange={(e) => { setApprove(e.target.value === 'APPROVE') }}>
          <option value="APPROVE">Approve as evidence</option><option value="REJECT">Reject</option>
        </select>
      </Field>
      {approve ? (
        <>
          <Field label="Evidence from"><input type="date" value={from} required onChange={(e) => { setFrom(e.target.value) }} /></Field>
          <Field label="Evidence to" help="On or before the certificate's own expiry.">
            <input type="date" value={to} required={certificate.expiresOn !== undefined} onChange={(e) => { setTo(e.target.value) }} />
          </Field>
        </>
      ) : null}
      <Field label="Rationale" help={`A different person confirms or returns this on v${certificate.evidenceVersion}.`}>
        <textarea value={rationale} required maxLength={2000} onChange={(e) => { setRationale(e.target.value) }} />
      </Field>
      <div className="ztax-form-actions"><Submit busy={action.busy}>Recommend</Submit></div>
      <Refusal error={action.error} />
    </form>
  )
}

const DecideForm = ({ certificate, decision, label, onDone }: {
  certificate: Certificate
  decision: 'CONFIRM' | 'RETURN'
  label: string
  onDone: () => void
}) => {
  const [note, setNote] = useState('')
  const action = useAction<Certificate>()
  const submit = (e: FormEvent) => {
    e.preventDefault()
    void action.run(() => ztax.decideExemptionCertificate(certificate.id, {
      decision, expectedEvidenceVersion: certificate.evidenceVersion, ...(note.trim() === '' ? {} : { note: note.trim() }),
    })).then((d) => { if (d !== null) onDone() })
  }
  return (
    <form className="ztax-form" onSubmit={submit} style={{ marginTop: 12 }}>
      <Field label={decision === 'RETURN' ? 'Reason for returning' : 'Note (optional)'}
        help={`Applies to evidence version ${certificate.evidenceVersion} exactly. Approval is evidence for this buyer, seller, jurisdiction and period; it changes no tax result.`}>
        <textarea value={note} maxLength={2000} required={decision === 'RETURN'} onChange={(e) => { setNote(e.target.value) }} />
      </Field>
      <div className="ztax-form-actions"><Submit busy={action.busy}>{label}</Submit></div>
      <Refusal error={action.error} />
    </form>
  )
}

const CertificateMove = ({ certificate, offer, onDone }: { certificate: Certificate; offer: Offer; onDone: () => void }) => {
  const [reason, setReason] = useState('')
  const action = useAction<Certificate>()
  const to = offer.act as CertificateStatus
  const submit = (e: FormEvent) => {
    e.preventDefault()
    void action.run(() => ztax.transitionExemptionCertificate(certificate.id, {
      to, ...(reason.trim() === '' ? {} : { reason: reason.trim() }),
    })).then((d) => { if (d !== null) onDone() })
  }
  return (
    <form className="ztax-form" onSubmit={submit} style={{ marginTop: 12 }}>
      <Field label={to === 'EXPIRED' ? 'Note (optional)' : 'Reason'} help="Blocks future use only. Nothing already recorded or committed changes.">
        <textarea value={reason} maxLength={2000} required={to !== 'EXPIRED'} onChange={(e) => { setReason(e.target.value) }} />
      </Field>
      <div className="ztax-form-actions"><Submit busy={action.busy}>{offer.label}</Submit></div>
      <Refusal error={action.error} />
    </form>
  )
}

const ReceiveCertificate = ({ roles, onOpen }: { roles: readonly string[]; onOpen: (id: string) => void }) => {
  const [key, setKey] = useState(newKey)
  const [buyerRef, setBuyerRef] = useState('')
  const [buyerName, setBuyerName] = useState('')
  const [type, setType] = useState<CertificateType>('RESALE')
  const [reason, setReason] = useState('')
  const [jurisdiction, setJurisdiction] = useState('')
  const [number, setNumber] = useState('')
  const [scope, setScope] = useState('')
  const [issued, setIssued] = useState('')
  const [expires, setExpires] = useState('')
  const [supersedes, setSupersedes] = useState('')
  const [doc, setDoc] = useState<EvidenceDocument | null>(null)
  const action = useAction<Certificate>()
  if (!hasRole(roles, 'OPERATOR', 'ANALYST')) {
    return <Card><Empty>Receiving a certificate needs the OPERATOR or ANALYST role.</Empty></Card>
  }
  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (doc === null) {
      return
    }
    const products = scope.split(',').map((s) => s.trim()).filter((s) => s !== '')
    void action.run(() => ztax.receiveExemptionCertificate(key, {
      buyerRef: buyerRef.trim(), buyerName: buyerName.trim(), certificateType: type, exemptionReason: reason.trim(),
      jurisdiction: jurisdiction.trim().toUpperCase(), issuedOn: issued, document: doc,
      ...(number.trim() === '' ? {} : { certificateNumber: number.trim() }),
      ...(products.length === 0 ? {} : { productScope: products }),
      ...(expires === '' ? {} : { expiresOn: expires }),
      ...(supersedes.trim() === '' ? {} : { supersedes: supersedes.trim() }),
    })).then((d) => {
      if (d !== null) {
        setKey(newKey())
        onOpen(d.id)
      }
    })
  }
  return (
    <Card title="Receive an exemption certificate">
      <p className="ztax-note" role="note">A received certificate is SUBMITTED evidence. It is screened, recommended and confirmed by a different person before it is approved, and even then it is not a tax exemption.</p>
      <form className="ztax-form" onSubmit={submit}>
        <Field label="Buyer reference" help="The buyer's id in your customer system. A certificate is evidence only for this buyer.">
          <input value={buyerRef} required maxLength={200} onChange={(e) => { setBuyerRef(e.target.value) }} />
        </Field>
        <Field label="Buyer name (as on the certificate)">
          <input value={buyerName} required maxLength={200} onChange={(e) => { setBuyerName(e.target.value) }} />
        </Field>
        <Field label="Certificate type" help="A resale or wholesale certificate is not an unconditional exemption and relieves no provider duty.">
          <select value={type} onChange={(e) => { setType(CERTIFICATE_TYPES.find((t) => t.value === e.target.value)?.value ?? 'RESALE') }}>
            {CERTIFICATE_TYPES.map((t) => <option key={t.value} value={t.value}>{t.label}</option>)}
          </select>
        </Field>
        <Field label="Claimed exemption">
          <input value={reason} required maxLength={500} onChange={(e) => { setReason(e.target.value) }} />
        </Field>
        <Field label="Jurisdiction" help="The one jurisdiction the certificate is for: US-NY, GB.">
          <input value={jurisdiction} required maxLength={6} onChange={(e) => { setJurisdiction(e.target.value) }} />
        </Field>
        <Field label="Certificate number (optional)">
          <input value={number} maxLength={200} onChange={(e) => { setNumber(e.target.value) }} />
        </Field>
        <Field label="Product scope (optional)" help="As written on the certificate, comma-separated. Never interpreted as a rule.">
          <input value={scope} onChange={(e) => { setScope(e.target.value) }} />
        </Field>
        <Field label="Issued / signed on">
          <input type="date" value={issued} required onChange={(e) => { setIssued(e.target.value) }} />
        </Field>
        <Field label="Expires on (optional)">
          <input type="date" value={expires} onChange={(e) => { setExpires(e.target.value) }} />
        </Field>
        <Field label="Renews certificate (optional)" help="The id of the certificate this one renews; it is superseded when this one is approved.">
          <input value={supersedes} onChange={(e) => { setSupersedes(e.target.value) }} />
        </Field>
        <DocumentPicker label="Certificate document" value={doc} onChange={setDoc} />
        <div className="ztax-form-actions"><Submit busy={action.busy}>Receive certificate</Submit></div>
        <Refusal error={action.error} />
      </form>
    </Card>
  )
}

// ---------------------------------------------------------------------------
// expiry and renewals
// ---------------------------------------------------------------------------

const Renewals = ({ onRegistration, onCertificate }: { onRegistration: (id: string) => void; onCertificate: (id: string) => void }) => {
  const { load, reload } = useLoad(
    (signal) => all3(
      ztax.listRegistrations({ flag: 'EXPIRING_SOON', limit: 500 }, signal),
      ztax.listRegistrations({ flag: 'PAST_EXPIRY', limit: 500 }, signal),
      both(
        ztax.listExemptionCertificates({ flag: 'EXPIRING_SOON', limit: 500 }, signal),
        ztax.listExemptionCertificates({ flag: 'PAST_EXPIRY', limit: 500 }, signal),
      ),
    ),
    [],
  )
  return (
    <Card title="Expiry, revocation & renewal queue" aside={<button type="button" className="ztax-btn-quiet" onClick={reload}>Refresh</button>}>
      <p className="ztax-note" role="note">
        Dates are UTC calendar dates. Expiry is recorded by a person; a renewal upload never reactivates anything until it is itself approved, and history stays as recorded.
      </p>
      <Loaded load={load}>
        {([soon, past, [certSoon, certPast]]) => {
          const rows = [
            ...[...soon.registrations, ...past.registrations].map((r) => ({
              key: r.id, kind: 'Registration', subject: `${words(r.scheme)} · ${r.jurisdiction} · ${r.registrationNumber}`,
              end: endOf(r), status: r.status, flags: r.flags, open: () => { onRegistration(r.id) },
            })),
            ...[...certSoon.certificates, ...certPast.certificates].map((c) => ({
              key: c.id, kind: 'Certificate', subject: `${c.buyerName} · ${c.jurisdiction}`,
              end: endOf(c), status: c.status, flags: c.flags, open: () => { onCertificate(c.id) },
            })),
          ].sort((a, b) => a.end.localeCompare(b.end))
          return rows.length === 0 ? <Empty>Nothing expires within 60 days, and nothing is past its end.</Empty> : (
            <table className="ztax-action-table">
              <thead><tr><th>Ends</th><th>Record</th><th>Subject</th><th>Status</th><th>Attention</th><th>Action</th></tr></thead>
              <tbody>
                {rows.map((r) => (
                  <tr key={r.key}>
                    <td style={{ whiteSpace: 'nowrap' }}>{r.end}</td><td>{r.kind}</td><td>{r.subject}</td>
                    <td><Status value={r.status} /></td><td><Flags flags={r.flags} /></td>
                    <td><button type="button" className="ztax-table-action-btn" onClick={r.open}>Open</button></td>
                  </tr>
                ))}
              </tbody>
            </table>
          )
        }}
      </Loaded>
    </Card>
  )
}
