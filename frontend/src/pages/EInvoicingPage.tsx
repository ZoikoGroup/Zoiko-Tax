import { useState, type FormEvent } from 'react'

import type { S } from '@zoikotax/sdk'

import { instant } from '../dashboard/format'
import { ztax } from '../platform/client'
import { Card, Columns, Empty, ErrorNote, Field, Id, Loaded, PageHeader, Raw, Status, Submit, useAction, useLoad } from '../ui/kit'
import { TabBar, todayUtc } from './determine/shared'
import {
  APPLICABILITY_MEANING,
  APPLICABILITY_STATUSES,
  BUYER_KINDS,
  dispatchBlocked,
  dispatches,
  DOCUMENT_TYPES,
  DUTIES,
  gateLabel,
  lastAttempt,
  MODELS,
  NOT_CERTIFICATION,
  PROFILES,
  profilesFor,
  reasonOf,
  receiptDigest,
  recordOffers,
  refusalText,
  responseKinds,
  responseNeeds,
  ruleCounts,
  sha256Of,
  SUBMISSION_MEANING,
  SUBMISSION_STATUSES,
  type Action,
  type Applicability,
  type ApplicabilityStatus,
  type BuyerKind,
  type DocumentType,
  type Duty,
  type Gate,
  type Model,
  type Offer,
  type Profile,
  type ResponseKind,
  type Submission,
  type SubmissionStatus,
  type Terms,
  type Validation,
} from './einvoicing/model'
import { documentOf, type EvidenceDocument } from './registrations/model'
import { useRoutedTab } from './routedTab'
import type { PageProps } from './types'
import { both, Tiles, words } from './workspace'

// E-Invoicing / CTC (Engineering Doc 21, G-030): country applicability per
// legal entity, country, document type and buyer kind; deterministic format
// validation of committed fiscal documents; and authority submissions. The
// layer sits on the fiscal documents: Invoice Processing, Rejections &
// Corrections and the Fiscal Archive stay on the Documents screen, and a
// rejected submission is corrected there. The cell enforces every rule; this
// page offers only the acts a record admits, with the reason when not.

type Tab = 'applicability' | 'validation' | 'submissions'

const TABS: readonly { value: Tab; label: string }[] = [
  { value: 'applicability', label: 'Country applicability' },
  { value: 'validation', label: 'Format validation' },
  { value: 'submissions', label: 'Authority submissions' },
]

export const EInvoicingPage = ({ session, route, onTabChange, onNavigate }: PageProps) => {
  const [tab, setTab] = useRoutedTab(TABS.map((t) => t.value), 'applicability', route, onTabChange)
  const roles = session.user.roles
  const me = session.user.id
  return (
    <div className="ztax-page">
      <PageHeader
        title="E-Invoicing / CTC"
        subtitle="Which documents owe an electronic duty where, whether they meet the format profile, and what the authority answered. No country is active because a format exists."
      />
      <p className="ztax-note" role="note">{NOT_CERTIFICATION}</p>
      <TabBar label="E-invoicing and CTC" tabs={TABS} current={tab} onSelect={setTab} />
      {tab === 'applicability' ? <ApplicabilityTab roles={roles} me={me} />
        : tab === 'validation' ? <ValidationTab />
        : <SubmissionsTab roles={roles} me={me} onCorrect={(documentId) => { onNavigate('documents', { tab: 'corrections', draft: documentId }) }} />}
    </div>
  )
}

const Refusal = ({ error }: { error: unknown }) => {
  if (error === null || error === undefined) {
    return null
  }
  const text = refusalText(reasonOf(error))
  return text === null ? <div style={{ gridColumn: '1 / -1' }}><ErrorNote error={error} /></div> : (
    <p className="ztax-error-note" role="alert" style={{ gridColumn: '1 / -1' }}>{text}</p>
  )
}

const Blocked = ({ offer }: { offer: Offer }) =>
  offer.blocked === null ? null : <p className="muted" style={{ margin: '4px 0' }}>{offer.label}: {offer.blocked}</p>

// ---------------------------------------------------------------------------
// country applicability (W01, W20)
// ---------------------------------------------------------------------------

const ApplicabilityTab = ({ roles, me }: { roles: readonly string[]; me: string }) => {
  const [country, setCountry] = useState('')
  const [status, setStatus] = useState<ApplicabilityStatus | ''>('')
  const [selected, setSelected] = useState<string | null>(null)
  const { load, reload } = useLoad((signal) => ztax.listCTCApplicability({
    ...(country.length === 2 ? { country } : {}), ...(status === '' ? {} : { status }),
  }, signal), [country, status])
  return (
    <>
      <Resolve />
      <Card title="Applicability records" aside={
        <span style={{ display: 'flex', gap: 8 }}>
          <input aria-label="Country filter" placeholder="Country" maxLength={2} value={country} style={{ width: 80 }}
            onChange={(e) => { setCountry(e.target.value.toUpperCase()) }} />
          <select aria-label="Status filter" value={status} onChange={(e) => { setStatus(APPLICABILITY_STATUSES.find((s) => s === e.target.value) ?? '') }}>
            <option value="">Any status</option>
            {APPLICABILITY_STATUSES.map((s) => <option key={s} value={s}>{words(s)}</option>)}
          </select>
        </span>
      }>
        <Loaded load={load}>
          {(data) => data.records.length === 0 ? (
            <Empty>No applicability record. Every cell without an active record resolves to REVIEW_BLOCKED.</Empty>
          ) : (
            <div style={{ overflowX: 'auto' }}>
              <table className="ztax-action-table">
                <thead>
                  <tr><th>Country</th><th>Document</th><th>Buyer</th><th>Duty</th><th>Model / profile</th><th>Effective</th><th>Status</th><th>Gates missing</th><th>Action</th></tr>
                </thead>
                <tbody>
                  {data.records.map((a) => (
                    <tr key={a.id} className={a.id === selected ? 'ztax-row-selected' : undefined}>
                      <td>{a.country}</td>
                      <td>{words(a.documentType)}</td>
                      <td>{a.buyerKind}</td>
                      <td>{a.terms.duty}</td>
                      <td>{words(a.terms.model)} · {words(a.terms.profile)}</td>
                      <td style={{ whiteSpace: 'nowrap' }}>{a.terms.effectiveFrom} → {a.terms.effectiveTo ?? 'open'}</td>
                      <td><Status value={a.status} /></td>
                      <td>{a.status === 'ACTIVE' || a.status === 'RETIRED' ? '—' : a.missingGates.length}</td>
                      <td>
                        <button type="button" className="ztax-table-action-btn" aria-pressed={a.id === selected}
                          onClick={() => { setSelected(a.id) }}>Open</button>
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
        <RecordDetail key={selected} id={selected} roles={roles} me={me} onChanged={reload} onClose={() => { setSelected(null) }} />
      )}
      <NewRecord roles={roles} onCreated={(id) => { reload(); setSelected(id) }} />
    </>
  )
}

const Resolve = () => {
  const [country, setCountry] = useState('')
  const [documentType, setDocumentType] = useState<DocumentType>('INVOICE')
  const [buyerKind, setBuyerKind] = useState<BuyerKind>('B2B')
  const [date, setDate] = useState(todayUtc())
  const action = useAction<S<'CTCResolution'>>()
  const submit = (e: FormEvent) => {
    e.preventDefault()
    void action.run(() => ztax.resolveCTCApplicability({ country, documentType, buyerKind, date }))
  }
  const r = action.done
  return (
    <Card title="Resolve a cell">
      <form className="ztax-form" onSubmit={submit}>
        <Field label="Country"><input required pattern="[A-Z]{2}" maxLength={2} value={country} onChange={(e) => { setCountry(e.target.value.toUpperCase()) }} /></Field>
        <Field label="Document type">
          <select value={documentType} onChange={(e) => { setDocumentType(DOCUMENT_TYPES.find((t) => t === e.target.value) ?? 'INVOICE') }}>
            {DOCUMENT_TYPES.map((t) => <option key={t} value={t}>{words(t)}</option>)}
          </select>
        </Field>
        <Field label="Buyer kind">
          <select value={buyerKind} onChange={(e) => { setBuyerKind(BUYER_KINDS.find((t) => t === e.target.value) ?? 'B2B') }}>
            {BUYER_KINDS.map((t) => <option key={t} value={t}>{t}</option>)}
          </select>
        </Field>
        <Field label="Issue date"><input type="date" required value={date} onChange={(e) => { setDate(e.target.value) }} /></Field>
        <div className="ztax-form-actions"><Submit busy={action.busy}>Resolve</Submit></div>
        <Refusal error={action.error} />
      </form>
      {r === null ? null : (
        <dl className="ztax-kv" aria-live="polite">
          <dt>Duty</dt><dd><Status value={r.duty} /> {r.dispatchPermitted ? 'Documents of this cell may be validated and submitted.' : 'Nothing is submitted for this cell.'}</dd>
          {r.reasonCode === undefined ? null : <><dt>Reason</dt><dd className="mono">{r.reasonCode}</dd></>}
          <dt>Why</dt><dd>{r.detail}</dd>
          {r.terms === undefined ? null : (
            <><dt>Source</dt><dd>{r.terms.legalSource} <span className="muted">(as of {r.terms.sourceAsOf}; {r.terms.authority})</span></dd></>
          )}
        </dl>
      )}
    </Card>
  )
}

const TermsView = ({ t }: { t: Terms }) => (
  <dl className="ztax-kv">
    <dt>Duty</dt><dd>{t.duty}</dd>
    <dt>Model</dt><dd>{words(t.model)}</dd>
    <dt>Profile</dt><dd>{words(t.profile)}{t.formatVersion === undefined ? null : ` ${t.formatVersion}`}</dd>
    <dt>Authority</dt><dd>{t.authority}</dd>
    <dt>Legal source</dt><dd>{t.legalSource} <span className="muted">(reviewed {t.sourceAsOf})</span></dd>
    <dt>Effective</dt><dd>{t.effectiveFrom} → {t.effectiveTo ?? 'open'}</dd>
    {t.conditions === undefined ? null : <><dt>Conditions</dt><dd>{t.conditions}</dd></>}
  </dl>
)

const RecordDetail = ({ id, roles, me, onChanged, onClose }: {
  id: string
  roles: readonly string[]
  me: string
  onChanged: () => void
  onClose: () => void
}) => {
  const { load, reload } = useLoad((signal) => ztax.getCTCApplicability(id, signal), [id])
  const changed = () => {
    reload()
    onChanged()
  }
  return (
    <Loaded load={load}>
      {(a) => {
        const offers = recordOffers(a, roles, me)
        const offer = (act: Offer['act']) => offers.find((o) => o.act === act)
        return (
          <Columns>
            <Card title={`${a.country} · ${words(a.documentType)} · ${a.buyerKind}`} aside={<button type="button" className="ztax-btn-quiet" onClick={onClose}>Close</button>}>
              <dl className="ztax-kv">
                <dt>Record</dt><dd><Id value={a.id} /></dd>
                <dt>Legal entity</dt><dd><Id value={a.legalEntityId} /></dd>
                <dt>Status</dt><dd><Status value={a.status} /> <span className="muted">{APPLICABILITY_MEANING[a.status]}</span></dd>
                <dt>Version</dt><dd>{a.currentVersion} <span className="mono muted">{a.versions[a.versions.length - 1]?.digest}</span></dd>
              </dl>
              <TermsView t={a.terms} />
              <h3 className="ztax-card-title">Release gates on version {a.currentVersion}</h3>
              <ul className="ztax-plain-list">
                {a.requiredGates.map((g) => {
                  const s = a.signoffs.find((x) => x.gate === g)
                  return (
                    <li key={g}>
                      {s === undefined ? 'Not signed — ' : 'Signed — '}{gateLabel(g)}
                      {s === undefined ? null : <span className="muted"> · {s.signoffRef} · {instant(s.signedAt)} · <Id value={s.signedBy} /></span>}
                    </li>
                  )
                })}
              </ul>
              <h3 className="ztax-card-title">Evidence</h3>
              {a.evidence.length === 0 ? <Empty>No evidence attached. A record is activated only with evidence.</Empty> : (
                <ul className="ztax-plain-list">
                  {a.evidence.map((d) => <li key={d.sha256}>{d.filename} <span className="mono muted">SHA-256 {d.sha256}</span></li>)}
                </ul>
              )}
              <h3 className="ztax-card-title">History</h3>
              <ol className="ztax-plain-list">
                {a.history.map((e) => (
                  <li key={e.seq}>
                    {instant(e.recordedAt)} — {words(e.kind)}{e.gate === undefined ? '' : ` ${gateLabel(e.gate)}`}
                    {e.version === undefined ? '' : ` (v${e.version})`} → {words(e.status)}
                    {e.reason === undefined ? null : <span className="muted"> · {e.reason}</span>}
                  </li>
                ))}
              </ol>
              <Raw value={a} />
            </Card>
            <Card title="Act on this record">
              {offers.map((o) => <Blocked key={o.act} offer={o} />)}
              {offer('REVISE')?.blocked === null ? <ReviseTerms a={a} onDone={changed} /> : null}
              {offer('SUBMIT')?.blocked === null ? <Move a={a} action="SUBMIT" label="Submit for review" onDone={changed} /> : null}
              {offer('SIGN')?.blocked === null ? <SignGate a={a} onDone={changed} /> : null}
              {offer('ACTIVATE')?.blocked === null ? <Move a={a} action="ACTIVATE" label={`Activate version ${a.currentVersion}`} onDone={changed} /> : null}
              {offer('RETURN')?.blocked === null ? <Move a={a} action="RETURN" label="Return to draft" reason onDone={changed} /> : null}
              {offer('SUSPEND')?.blocked === null ? <Move a={a} action="SUSPEND" label="Suspend" reason onDone={changed} /> : null}
              {offer('RESUME')?.blocked === null ? <Move a={a} action="RESUME" label="Resume" reason onDone={changed} /> : null}
              {offer('EVIDENCE')?.blocked === null ? <AttachEvidence a={a} onDone={changed} /> : null}
              {offer('RETIRE')?.blocked === null ? <Move a={a} action="RETIRE" label="Retire" reason onDone={changed} /> : null}
            </Card>
          </Columns>
        )
      }}
    </Loaded>
  )
}

const Move = ({ a, action: act, label, reason: needsReason = false, onDone }: {
  a: Applicability
  action: Action
  label: string
  reason?: boolean
  onDone: () => void
}) => {
  const [reason, setReason] = useState('')
  const action = useAction<Applicability>()
  const submit = (e: FormEvent) => {
    e.preventDefault()
    void action.run(() => ztax.transitionCTCApplicability(a.id, {
      action: act, ...(act === 'ACTIVATE' ? { version: a.currentVersion } : {}), ...(needsReason ? { reason } : {}),
    })).then((r) => { if (r !== null) { setReason(''); onDone() } })
  }
  return (
    <form className="ztax-form" onSubmit={submit} style={{ marginTop: 12 }}>
      {needsReason ? (
        <Field label={`${label}: reason`} help="Recorded in the history; nothing is edited.">
          <textarea value={reason} required maxLength={2000} onChange={(e) => { setReason(e.target.value) }} />
        </Field>
      ) : null}
      <div className="ztax-form-actions"><Submit busy={action.busy}>{label}</Submit></div>
      <Refusal error={action.error} />
    </form>
  )
}

const SignGate = ({ a, onDone }: { a: Applicability; onDone: () => void }) => {
  const [gate, setGate] = useState<Gate | ''>(a.missingGates[0] ?? '')
  const [signoffRef, setSignoffRef] = useState('')
  const [note, setNote] = useState('')
  const action = useAction<Applicability>()
  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (gate === '') {
      return
    }
    void action.run(() => ztax.signCTCApplicabilityGate(a.id, {
      gate, version: a.currentVersion, signoffRef: signoffRef.trim(), ...(note.trim() === '' ? {} : { note: note.trim() }),
    })).then((r) => { if (r !== null) { setSignoffRef(''); setNote(''); onDone() } })
  }
  return (
    <form className="ztax-form" onSubmit={submit} style={{ marginTop: 12 }}>
      <Field label="Gate" help={`Signed on version ${a.currentVersion} exactly; a revision voids it.`}>
        <select value={gate} required onChange={(e) => { setGate(a.missingGates.find((g) => g === e.target.value) ?? '') }}>
          {a.missingGates.map((g) => <option key={g} value={g}>{gateLabel(g)}</option>)}
        </select>
      </Field>
      <Field label="Sign-off reference" help="The approval's own reference: a ticket, minute or ADR id.">
        <input required maxLength={200} value={signoffRef} onChange={(e) => { setSignoffRef(e.target.value) }} />
      </Field>
      <Field label="Note (optional)"><textarea maxLength={2000} value={note} onChange={(e) => { setNote(e.target.value) }} /></Field>
      <div className="ztax-form-actions"><Submit busy={action.busy}>Sign gate</Submit></div>
      <Refusal error={action.error} />
    </form>
  )
}

const DocumentPicker = ({ value, onChange }: { value: EvidenceDocument | null; onChange: (d: EvidenceDocument | null) => void }) => {
  const [problem, setProblem] = useState<string | null>(null)
  return (
    <Field label="Evidence document" help="PDF or image, at most 25 MiB. Hashed with SHA-256 here in the browser; the file is not uploaded.">
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
      {value === null ? null : <span className="mono muted" style={{ overflowWrap: 'anywhere' }}>SHA-256 {value.sha256}</span>}
    </Field>
  )
}

const AttachEvidence = ({ a, onDone }: { a: Applicability; onDone: () => void }) => {
  const [doc, setDoc] = useState<EvidenceDocument | null>(null)
  const action = useAction<Applicability>()
  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (doc === null) {
      return
    }
    void action.run(() => ztax.addCTCApplicabilityEvidence(a.id, doc)).then((r) => { if (r !== null) { onDone() } })
  }
  return (
    <form className="ztax-form" onSubmit={submit} style={{ marginTop: 12 }}>
      <DocumentPicker value={doc} onChange={setDoc} />
      <div className="ztax-form-actions"><Submit busy={action.busy}>Attach evidence</Submit></div>
      <Refusal error={action.error} />
    </form>
  )
}

/** The terms a record carries, as the form holds them. */
interface TermsDraft {
  duty: Duty
  model: Model
  profile: Profile
  formatVersion: string
  authority: string
  legalSource: string
  sourceAsOf: string
  effectiveFrom: string
  effectiveTo: string
  conditions: string
}

const blankTerms = (): TermsDraft => ({
  duty: 'REQUIRED', model: 'CLEARANCE', profile: 'PEPPOL_BIS_3', formatVersion: '', authority: '', legalSource: '',
  sourceAsOf: todayUtc(), effectiveFrom: '', effectiveTo: '', conditions: '',
})

const draftOf = (t: Terms): TermsDraft => ({
  duty: t.duty, model: t.model, profile: t.profile, formatVersion: t.formatVersion ?? '', authority: t.authority,
  legalSource: t.legalSource, sourceAsOf: t.sourceAsOf, effectiveFrom: t.effectiveFrom, effectiveTo: t.effectiveTo ?? '',
  conditions: t.conditions ?? '',
})

/** The terms the cell receives: the model and profile the country and duty admit, empty optional fields left out. */
const termsOf = (d: TermsDraft, country: string): Terms => {
  const profiles = profilesFor(country, d.duty)
  return {
    duty: d.duty,
    model: d.duty === 'PROHIBITED' ? 'NONE' : d.model,
    profile: profiles.includes(d.profile) ? d.profile : (profiles[0] ?? 'NONE'),
    authority: d.authority.trim(), legalSource: d.legalSource.trim(), sourceAsOf: d.sourceAsOf, effectiveFrom: d.effectiveFrom,
    ...(d.formatVersion.trim() === '' ? {} : { formatVersion: d.formatVersion.trim() }),
    ...(d.effectiveTo === '' ? {} : { effectiveTo: d.effectiveTo }),
    ...(d.conditions.trim() === '' ? {} : { conditions: d.conditions.trim() }),
  }
}

const TermsFields = ({ country, value: d, onChange }: { country: string; value: TermsDraft; onChange: (d: TermsDraft) => void }) => {
  const set = (patch: Partial<TermsDraft>) => { onChange({ ...d, ...patch }) }
  const t = termsOf(d, country)
  const profiles = profilesFor(country, d.duty)
  return (
    <>
      <Field label="Duty">
        <select value={d.duty} onChange={(e) => { set({ duty: DUTIES.find((x) => x === e.target.value) ?? 'REQUIRED' }) }}>
          {DUTIES.map((x) => <option key={x} value={x}>{x}</option>)}
        </select>
      </Field>
      <Field label="Control model">
        <select value={t.model} disabled={d.duty === 'PROHIBITED'} onChange={(e) => { set({ model: MODELS.find((x) => x === e.target.value) ?? 'CLEARANCE' }) }}>
          {MODELS.filter((m) => (d.duty === 'PROHIBITED') === (m === 'NONE')).map((x) => <option key={x} value={x}>{words(x)}</option>)}
        </select>
      </Field>
      <Field label="Format profile" help="A national profile is offered only for its own country.">
        <select value={t.profile} onChange={(e) => { set({ profile: profiles.find((p) => p === e.target.value) ?? 'NONE' }) }}>
          {profiles.map((p) => <option key={p} value={p}>{PROFILES.find((x) => x.value === p)?.label ?? p}</option>)}
        </select>
      </Field>
      <Field label="Format version"><input maxLength={200} required={d.duty !== 'PROHIBITED'} value={d.formatVersion} onChange={(e) => { set({ formatVersion: e.target.value }) }} /></Field>
      <Field label="Authority / rail"><input required maxLength={200} value={d.authority} onChange={(e) => { set({ authority: e.target.value }) }} /></Field>
      <Field label="Legal source"><textarea required maxLength={2000} value={d.legalSource} onChange={(e) => { set({ legalSource: e.target.value }) }} /></Field>
      <Field label="Source reviewed on"><input type="date" required value={d.sourceAsOf} onChange={(e) => { set({ sourceAsOf: e.target.value }) }} /></Field>
      <Field label="Effective from"><input type="date" required value={d.effectiveFrom} onChange={(e) => { set({ effectiveFrom: e.target.value }) }} /></Field>
      <Field label="Effective to (optional)"><input type="date" value={d.effectiveTo} onChange={(e) => { set({ effectiveTo: e.target.value }) }} /></Field>
      <Field label="Eligibility conditions (optional)" help="Issuer size, thresholds, waivers the duty depends on.">
        <textarea maxLength={2000} value={d.conditions} onChange={(e) => { set({ conditions: e.target.value }) }} />
      </Field>
    </>
  )
}

const NewRecord = ({ roles, onCreated }: { roles: readonly string[]; onCreated: (id: string) => void }) => {
  const [country, setCountry] = useState('')
  const [documentType, setDocumentType] = useState<DocumentType>('INVOICE')
  const [buyerKind, setBuyerKind] = useState<BuyerKind>('B2B')
  const [terms, setTerms] = useState<TermsDraft>(blankTerms)
  const action = useAction<Applicability>()
  if (!roles.includes('OPERATOR') && !roles.includes('ANALYST')) {
    return null
  }
  const submit = (e: FormEvent) => {
    e.preventDefault()
    void action.run(() => ztax.createCTCApplicability({ country, documentType, buyerKind, terms: termsOf(terms, country) }))
      .then((r) => { if (r !== null) { onCreated(r.id) } })
  }
  return (
    <Card title="Record an applicability record">
      <p className="muted">One cell of the default legal entity, in DRAFT. It decides nothing until its required G0–G4 gates are signed on its current version by someone else, evidence is attached, and another person activates it.</p>
      <form className="ztax-form" onSubmit={submit}>
        <Field label="Country"><input required pattern="[A-Z]{2}" maxLength={2} value={country} onChange={(e) => { setCountry(e.target.value.toUpperCase()) }} /></Field>
        <Field label="Document type">
          <select value={documentType} onChange={(e) => { setDocumentType(DOCUMENT_TYPES.find((t) => t === e.target.value) ?? 'INVOICE') }}>
            {DOCUMENT_TYPES.map((t) => <option key={t} value={t}>{words(t)}</option>)}
          </select>
        </Field>
        <Field label="Buyer kind">
          <select value={buyerKind} onChange={(e) => { setBuyerKind(BUYER_KINDS.find((t) => t === e.target.value) ?? 'B2B') }}>
            {BUYER_KINDS.map((t) => <option key={t} value={t}>{t}</option>)}
          </select>
        </Field>
        <TermsFields country={country} value={terms} onChange={setTerms} />
        <div className="ztax-form-actions"><Submit busy={action.busy}>Record</Submit></div>
        <Refusal error={action.error} />
      </form>
    </Card>
  )
}

/** The next version of a draft or in-review record's terms. A revision voids its gate sign-offs and returns it to DRAFT. */
const ReviseTerms = ({ a, onDone }: { a: Applicability; onDone: () => void }) => {
  const [open, setOpen] = useState(false)
  const [terms, setTerms] = useState<TermsDraft>(() => draftOf(a.terms))
  const action = useAction<Applicability>()
  if (!open) {
    return (
      <div className="ztax-form-actions" style={{ marginTop: 12 }}>
        <button type="button" className="ztax-btn-quiet" onClick={() => { setTerms(draftOf(a.terms)); setOpen(true) }}>Revise terms…</button>
      </div>
    )
  }
  const submit = (e: FormEvent) => {
    e.preventDefault()
    void action.run(() => ztax.reviseCTCApplicability(a.id, termsOf(terms, a.country))).then((r) => {
      if (r !== null) {
        setOpen(false)
        onDone()
      }
    })
  }
  return (
    <form className="ztax-form" onSubmit={submit} style={{ marginTop: 12 }} aria-label="Revise terms">
      <p className="muted">Records version {a.currentVersion + 1}. Gate sign-offs on version {a.currentVersion} do not carry over, and the record returns to DRAFT. Terms identical to the current version record nothing.</p>
      <TermsFields country={a.country} value={terms} onChange={setTerms} />
      <div className="ztax-form-actions">
        <Submit busy={action.busy}>Record version {a.currentVersion + 1}</Submit>
        <button type="button" className="ztax-btn-quiet" onClick={() => { setOpen(false) }}>Cancel</button>
      </div>
      <Refusal error={action.error} />
    </form>
  )
}

// ---------------------------------------------------------------------------
// format validation (W06)
// ---------------------------------------------------------------------------

/** A document and an active dispatching record, chosen from what exists. */
const Pair = ({ documentId, setDocumentId, recordId, setRecordId }: {
  documentId: string
  setDocumentId: (v: string) => void
  recordId: string
  setRecordId: (v: string) => void
}) => {
  const { load } = useLoad((signal) => both(ztax.listDocuments({ limit: 100 }, signal), ztax.listCTCApplicability({ status: 'ACTIVE' }, signal)), [])
  return (
    <Loaded load={load}>
      {([docs, recs]) => (
        <>
          <Field label="Fiscal document" help="A committed document from Invoice Processing.">
            <select required value={documentId} onChange={(e) => { setDocumentId(e.target.value) }}>
              <option value="">Choose…</option>
              {docs.documents.map((d) => (
                <option key={d.id} value={d.id}>{d.number ?? d.id.slice(0, 8)} · {words(d.type)} · {d.grossTotal} {d.currency} · {words(d.status)}</option>
              ))}
            </select>
          </Field>
          <Field label="Applicability record" help="Only ACTIVE records that dispatch documents.">
            <select required value={recordId} onChange={(e) => { setRecordId(e.target.value) }}>
              <option value="">Choose…</option>
              {recs.records.filter(dispatches).map((a) => (
                <option key={a.id} value={a.id}>{a.country} · {words(a.documentType)} · {a.buyerKind} · {words(a.terms.profile)} v{a.currentVersion}</option>
              ))}
            </select>
          </Field>
        </>
      )}
    </Loaded>
  )
}

const ValidationTab = () => {
  const [documentId, setDocumentId] = useState('')
  const [recordId, setRecordId] = useState('')
  const [shown, setShown] = useState<Validation | null>(null)
  const action = useAction<Validation>()
  const { load, reload } = useLoad((signal) => ztax.listCTCValidations({}, signal), [])
  const submit = (e: FormEvent) => {
    e.preventDefault()
    void action.run(() => ztax.validateCTCDocument({ documentId, applicabilityId: recordId })).then((v) => {
      if (v !== null) {
        setShown(v)
        reload()
      }
    })
  }
  return (
    <>
      <Card title="Validate a document">
        <form className="ztax-form" onSubmit={submit}>
          <Pair documentId={documentId} setDocumentId={setDocumentId} recordId={recordId} setRecordId={setRecordId} />
          <div className="ztax-form-actions"><Submit busy={action.busy}>Validate</Submit></div>
          <Refusal error={action.error} />
        </form>
        <p className="muted">Deterministic: the same document state, record version and ruleset give the same report, returned rather than re-run.</p>
      </Card>
      {shown === null ? null : <Report v={shown} />}
      <Card title="Recent reports">
        <Loaded load={load}>
          {(data) => data.validations.length === 0 ? <Empty>No validation recorded yet.</Empty> : (
            <table className="ztax-action-table">
              <thead><tr><th>Validated</th><th>Document</th><th>Profile</th><th>Ruleset</th><th>Outcome</th><th>Fatal</th><th>Action</th></tr></thead>
              <tbody>
                {data.validations.map((v) => (
                  <tr key={v.id}>
                    <td>{instant(v.validatedAt)}</td>
                    <td><Id value={v.documentId} /></td>
                    <td>{words(v.profile)} {v.formatVersion}</td>
                    <td className="mono">{v.ruleset}</td>
                    <td><Status value={v.outcome} /></td>
                    <td>{v.fatalFailures}</td>
                    <td><button type="button" className="ztax-table-action-btn" onClick={() => { setShown(v) }}>Open</button></td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </Loaded>
      </Card>
    </>
  )
}

const Report = ({ v }: { v: Validation }) => {
  const c = ruleCounts(v)
  return (
    <Card title={`Report — ${v.outcome}`}>
      <Tiles tiles={[
        { label: 'Rules passed', count: c.pass, tone: 'ok' },
        { label: 'Fatal failures', count: c.fatal, tone: c.fatal > 0 ? 'bad' : 'ok' },
        { label: 'Warnings', count: c.fail - c.fatal, tone: c.fail - c.fatal > 0 ? 'warn' : 'ok' },
        { label: 'Not evaluated', count: c.notEvaluated, tone: 'neutral', note: 'Never a pass' },
      ]} />
      <dl className="ztax-kv">
        <dt>Report</dt><dd><Id value={v.id} /> <span className="mono muted">{v.digest}</span></dd>
        <dt>Document</dt><dd><Id value={v.documentId} /> <span className="mono muted">input {v.inputDigest}</span></dd>
        <dt>Record</dt><dd><Id value={v.applicabilityId} /> v{v.applicabilityVersion} · {words(v.profile)} {v.formatVersion}</dd>
        <dt>Ruleset</dt><dd className="mono">{v.ruleset}</dd>
        <dt>Official schema</dt><dd>{v.schemaValidated ? 'Validated' : 'Not run — no licensed bundle installed; syntax validation is the accredited adapter’s.'}</dd>
      </dl>
      <div style={{ overflowX: 'auto' }}>
        <table className="ztax-action-table">
          <thead><tr><th>Stage</th><th>Rule</th><th>Business term</th><th>Severity</th><th>Outcome</th><th>Reason</th><th>Detail</th></tr></thead>
          <tbody>
            {v.results.map((r) => (
              <tr key={r.ruleId}>
                <td>{words(r.stage)}</td>
                <td className="mono">{r.ruleId}</td>
                <td>{r.businessTerm ?? '—'}</td>
                <td>{r.severity}</td>
                <td><Status value={r.outcome === 'PASS' ? 'PASSED' : r.outcome === 'FAIL' ? 'FAILED' : 'NOT_EVALUATED'} /></td>
                <td className="mono">{r.reasonCode ?? '—'}</td>
                <td>{r.detail ?? '—'}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </Card>
  )
}

// ---------------------------------------------------------------------------
// authority submissions (W14, W15)
// ---------------------------------------------------------------------------

const SubmissionsTab = ({ roles, me, onCorrect }: { roles: readonly string[]; me: string; onCorrect: (documentId: string) => void }) => {
  const [status, setStatus] = useState<SubmissionStatus | ''>('')
  const [selected, setSelected] = useState<string | null>(null)
  const { load, reload } = useLoad((signal) => ztax.listCTCSubmissions(status === '' ? {} : { status }, signal), [status])
  return (
    <>
      {roles.includes('OPERATOR') ? <Prepare onPrepared={(id) => { reload(); setSelected(id) }} /> : null}
      <Card title="Submissions" aside={
        <select aria-label="Status filter" value={status} onChange={(e) => { setStatus(SUBMISSION_STATUSES.find((s) => s === e.target.value) ?? '') }}>
          <option value="">Any status</option>
          {SUBMISSION_STATUSES.map((s) => <option key={s} value={s}>{words(s)}</option>)}
        </select>
      }>
        <Loaded load={load}>
          {(data) => data.submissions.length === 0 ? <Empty>No submission.</Empty> : (
            <div style={{ overflowX: 'auto' }}>
              <table className="ztax-action-table">
                <thead><tr><th>Prepared</th><th>Document</th><th>Country</th><th>Profile</th><th>Gross</th><th>Status</th><th>Correction</th><th>Action</th></tr></thead>
                <tbody>
                  {data.submissions.map((s) => (
                    <tr key={s.id} className={s.id === selected ? 'ztax-row-selected' : undefined}>
                      <td>{instant(s.preparedAt)}</td>
                      <td>{s.documentNumber ?? <Id value={s.documentId} />} · {words(s.documentType)}</td>
                      <td>{s.country}</td>
                      <td>{words(s.profile)} {s.formatVersion}</td>
                      <td className="mono">{s.gross} {s.currency}</td>
                      <td><Status value={s.status} /></td>
                      <td>{s.correction === undefined ? '—' : s.correction.open ? 'Open' : 'Corrected'}</td>
                      <td><button type="button" className="ztax-table-action-btn" aria-pressed={s.id === selected} onClick={() => { setSelected(s.id) }}>Open</button></td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </Loaded>
      </Card>
      {selected === null ? null : (
        <SubmissionDetail key={selected} id={selected} roles={roles} me={me} onChanged={reload} onCorrect={onCorrect}
          onClose={() => { setSelected(null) }} />
      )}
    </>
  )
}

const Prepare = ({ onPrepared }: { onPrepared: (id: string) => void }) => {
  const [documentId, setDocumentId] = useState('')
  const [recordId, setRecordId] = useState('')
  const action = useAction<Submission>()
  const submit = (e: FormEvent) => {
    e.preventDefault()
    void action.run(() => ztax.prepareCTCSubmission({ documentId, applicabilityId: recordId })).then((s) => { if (s !== null) { onPrepared(s.id) } })
  }
  return (
    <Card title="Prepare a submission">
      <form className="ztax-form" onSubmit={submit}>
        <Pair documentId={documentId} setDocumentId={setDocumentId} recordId={recordId} setRecordId={setRecordId} />
        <div className="ztax-form-actions"><Submit busy={action.busy}>Prepare and seal</Submit></div>
        <Refusal error={action.error} />
      </form>
      <p className="muted">Needs a PASSED validation of the document under the record&apos;s current version. Preparing a document already live under the record returns that submission.</p>
    </Card>
  )
}

const SubmissionDetail = ({ id, roles, me, onChanged, onCorrect, onClose }: {
  id: string
  roles: readonly string[]
  me: string
  onChanged: () => void
  onCorrect: (documentId: string) => void
  onClose: () => void
}) => {
  const { load, reload } = useLoad((signal) => ztax.getCTCSubmission(id, signal), [id])
  const changed = () => {
    reload()
    onChanged()
  }
  return (
    <Loaded load={load}>
      {(s) => {
        const blocked = dispatchBlocked(s, roles, me)
        const kinds = responseKinds(s)
        return (
          <Columns>
            <Card title={`Submission — ${s.documentNumber ?? s.documentId}`} aside={<button type="button" className="ztax-btn-quiet" onClick={onClose}>Close</button>}>
              <dl className="ztax-kv">
                <dt>Status</dt><dd><Status value={s.status} /> <span className="muted">{SUBMISSION_MEANING[s.status]}</span></dd>
                <dt>External reference</dt><dd className="mono">{s.externalRef}</dd>
                <dt>Payload digest</dt><dd className="mono" style={{ overflowWrap: 'anywhere' }}>{s.payloadDigest}</dd>
                <dt>Document</dt><dd><Id value={s.documentId} /> · {words(s.documentType)} · <span className="mono">{s.gross} {s.currency}</span></dd>
                <dt>Record</dt><dd><Id value={s.applicabilityId} /> v{s.applicabilityVersion} · {s.country} · {s.buyerKind} · {words(s.model)} · {words(s.profile)} {s.formatVersion}</dd>
                <dt>Validation</dt><dd><Id value={s.validationId} /></dd>
                <dt>Prepared</dt><dd>{instant(s.preparedAt)} · <Id value={s.preparedBy} /></dd>
              </dl>
              <h3 className="ztax-card-title">Authority timeline</h3>
              <ol className="ztax-plain-list">
                {s.history.map((e) => (
                  <li key={e.seq}>
                    {instant(e.recordedAt)} — {words(e.kind)} → {words(e.status)}
                    {e.transportRef === undefined ? null : <span className="muted"> · transport {e.transportRef}</span>}
                    {e.authorityRef === undefined ? null : <span className="muted"> · authority {e.authorityRef}</span>}
                    {e.authorityCode === undefined ? null : <span className="muted"> · code {e.authorityCode}</span>}
                    {e.evidenceSha256 === undefined ? null : <span className="mono muted"> · receipt {e.evidenceSha256.slice(0, 12)}…</span>}
                    {e.detail === undefined ? null : <span className="muted"> · {e.detail}</span>}
                  </li>
                ))}
              </ol>
              {s.correction === undefined ? null : (
                <div className="ztax-note" role="note">
                  {s.correction.open
                    ? <>Rejected{s.correction.authorityCode === undefined ? '' : ` (${s.correction.authorityCode})`}. The document is corrected by a new document naming it, never edited. Document id: <span className="mono">{s.documentId}</span>{' '}
                      <button type="button" className="ztax-btn-quiet" onClick={() => { onCorrect(s.documentId) }}>Open Rejections &amp; Corrections</button></>
                    : <>Corrected by {s.correction.correctedBy.map((d) => <Id key={d} value={d} />)}.</>}
                </div>
              )}
              <Raw value={s} />
            </Card>
            <Card title="Act on this submission">
              {blocked === null ? <Dispatch s={s} onDone={changed} /> : <p className="muted">Dispatch: {blocked}</p>}
              {kinds.length === 0 ? null : <Respond s={s} kinds={kinds} onDone={changed} />}
              {s.status === 'PREPARED' && roles.includes('OPERATOR') ? <Withdraw s={s} onDone={changed} /> : null}
            </Card>
          </Columns>
        )
      }}
    </Loaded>
  )
}

const Dispatch = ({ s, onDone }: { s: Submission; onDone: () => void }) => {
  const action = useAction<Submission>()
  const submit = (e: FormEvent) => {
    e.preventDefault()
    void action.run(() => ztax.dispatchCTCSubmission(s.id, { payloadDigest: s.payloadDigest })).then((r) => { if (r !== null) { onDone() } })
  }
  return (
    <form className="ztax-form" onSubmit={submit}>
      <p className="muted" style={{ gridColumn: '1 / -1' }}>
        Authorises one attempt under {s.externalRef} for the payload sealed as <span className="mono">{s.payloadDigest.slice(0, 16)}…</span>. Transmit it through the approved channel and record what comes back.
      </p>
      <div className="ztax-form-actions"><Submit busy={action.busy}>Authorise dispatch</Submit></div>
      <Refusal error={action.error} />
    </form>
  )
}

const Respond = ({ s, kinds, onDone }: { s: Submission; kinds: readonly ResponseKind[]; onDone: () => void }) => {
  const [kind, setKind] = useState<ResponseKind>(kinds[0] ?? 'TRANSPORT_RECEIVED')
  const [transportRef, setTransportRef] = useState('')
  const [authorityRef, setAuthorityRef] = useState('')
  const [authorityCode, setAuthorityCode] = useState('')
  const [evidence, setEvidence] = useState('')
  const [detail, setDetail] = useState('')
  const action = useAction<Submission>()
  const needs = responseNeeds(kind)
  const attempt = lastAttempt(s)
  const digest = receiptDigest(evidence)
  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (attempt === undefined || (needs.evidence && digest === null)) {
      return
    }
    void action.run(() => ztax.recordCTCResponse(s.id, {
      kind, attemptId: attempt,
      ...(needs.transportRef ? { transportRef: transportRef.trim() } : {}),
      ...(needs.authorityRef ? { authorityRef: authorityRef.trim() } : {}),
      ...(needs.authorityCode ? { authorityCode: authorityCode.trim() } : {}),
      ...(digest === null ? {} : { evidenceSha256: digest }),
      ...(detail.trim() === '' ? {} : { detail: detail.trim() }),
    })).then((r) => { if (r !== null) { onDone() } })
  }
  return (
    <form className="ztax-form" onSubmit={submit} style={{ marginTop: 12 }}>
      <p className="ztax-note" role="note" style={{ gridColumn: '1 / -1' }}>
        Record only what the rail or the authority itself returned. A transport receipt is not acceptance.
      </p>
      <Field label="Answer">
        <select value={kind} onChange={(e) => { setKind(kinds.find((k) => k === e.target.value) ?? kind) }}>
          {kinds.map((k) => <option key={k} value={k}>{words(k)}</option>)}
        </select>
      </Field>
      {needs.transportRef ? <Field label={kind === 'STATUS_NOT_FOUND' ? 'Enquiry reference' : 'Transport receipt reference'}><input required maxLength={200} value={transportRef} onChange={(e) => { setTransportRef(e.target.value) }} /></Field> : null}
      {needs.authorityRef ? <Field label="Authority document reference"><input required maxLength={200} value={authorityRef} onChange={(e) => { setAuthorityRef(e.target.value) }} /></Field> : null}
      {needs.authorityCode ? <Field label="Authority rejection code"><input required maxLength={200} value={authorityCode} onChange={(e) => { setAuthorityCode(e.target.value) }} /></Field> : null}
      {needs.evidence ? (
        <Field label="Raw receipt SHA-256" help="Choose the saved receipt to hash it here, or paste its SHA-256. The receipt itself is not uploaded.">
          <input type="file" onChange={(e) => {
            const f = e.target.files?.[0]
            if (f !== undefined) {
              void sha256Of(f).then(setEvidence)
            }
          }} />
          <input className="mono" required value={evidence} onChange={(e) => { setEvidence(e.target.value) }} />
          {evidence !== '' && digest === null ? <span className="ztax-error-note" role="alert">Not a SHA-256.</span> : null}
        </Field>
      ) : null}
      <Field label={needs.detail ? 'Detail' : 'Detail (optional)'}><textarea required={needs.detail} maxLength={2000} value={detail} onChange={(e) => { setDetail(e.target.value) }} /></Field>
      <div className="ztax-form-actions"><Submit busy={action.busy}>Record answer</Submit></div>
      <Refusal error={action.error} />
    </form>
  )
}

const Withdraw = ({ s, onDone }: { s: Submission; onDone: () => void }) => {
  const [reason, setReason] = useState('')
  const action = useAction<Submission>()
  const submit = (e: FormEvent) => {
    e.preventDefault()
    void action.run(() => ztax.withdrawCTCSubmission(s.id, { reason })).then((r) => { if (r !== null) { onDone() } })
  }
  return (
    <form className="ztax-form" onSubmit={submit} style={{ marginTop: 12 }}>
      <Field label="Withdraw: reason"><textarea required maxLength={2000} value={reason} onChange={(e) => { setReason(e.target.value) }} /></Field>
      <div className="ztax-form-actions"><Submit busy={action.busy}>Withdraw</Submit></div>
      <Refusal error={action.error} />
    </form>
  )
}
