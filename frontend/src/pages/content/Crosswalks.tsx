import { useState, type FormEvent } from 'react'

import { ztax } from '../../platform/client'
import { Card, Empty, ErrorNote, Field, Id, Loaded, Status, Submit, newKey, useAction, useLoad } from '../../ui/kit'
import { sha256Hex } from '../registrations/model'
import {
  CROSSWALK_MEANING, CROSSWALK_STATUSES, MATCHES, NOT_FISCAL_AUTHORITY, RESOLUTION_TEXT, RIGHTS_BASES,
  crosswalkOffers, parseEntries, reasonOf, refusalText,
  type Crosswalk, type CrosswalkLookup, type CrosswalkRightsBasis, type CrosswalkStatus, type CrosswalkVersion, type Offer,
} from './model'

/**
 * Vendor Crosswalks (Doc 02 WF-06/WF-07; Doc 12 FED-04): the library of
 * versioned vendor-code mappings, a version's entries and grant, four-eyed
 * review, and an exact, advisory lookup. Served by /v1/content/crosswalks.
 */

const Refusal = ({ error }: { error: unknown }) => {
  if (error === null || error === undefined) return null
  const text = refusalText(reasonOf(error))
  return text === null ? <ErrorNote error={error} /> : <p className="ztax-error-note" role="alert">{text}</p>
}

const ENTRY_HELP = 'One code per line: code | vendor meaning | EXACT, PARTIAL or NO_EQUIVALENT | ontology:… target | predicate (required for PARTIAL).'

export const CrosswalksTab = ({ roles, me }: { roles: readonly string[]; me: string }) => {
  const [status, setStatus] = useState<CrosswalkStatus | ''>('')
  const [selected, setSelected] = useState<string | null>(null)
  const { load, reload } = useLoad(
    (signal) => ztax.listCrosswalks({ ...(status === '' ? {} : { status }), limit: 200 }, signal), [status])
  const author = roles.includes('OPERATOR') || roles.includes('ANALYST')
  return (
    <>
      <Card title="Vendor crosswalks" aside={
        <label className="ztax-field" style={{ flexDirection: 'row', alignItems: 'center', gap: 6 }}>
          <span>Status</span>
          <select value={status} onChange={(e) => { setStatus(CROSSWALK_STATUSES.find((s) => s === e.target.value) ?? '') }}>
            <option value="">Any</option>
            {CROSSWALK_STATUSES.map((s) => <option key={s} value={s}>{s.replaceAll('_', ' ')}</option>)}
          </select>
        </label>
      }>
        <p className="ztax-note" role="note">{NOT_FISCAL_AUTHORITY}</p>
        <Loaded load={load}>
          {(list) => list.crosswalks.length === 0 ? <Empty>No crosswalk yet.</Empty> : (
            <div className="zp-scroll">
              <table className="table">
                <thead>
                  <tr>
                    <th scope="col">Vendor</th><th scope="col">Namespace</th><th scope="col">Release</th>
                    <th scope="col">Title</th><th scope="col">In use</th><th scope="col">Latest</th><th scope="col" />
                  </tr>
                </thead>
                <tbody>
                  {list.crosswalks.map((c) => {
                    const latest = c.versions.at(-1)
                    return (
                      <tr key={c.id}>
                        <td>{c.vendor}</td><td className="mono">{c.namespace}</td><td>{c.vendorRelease}</td><td>{c.title}</td>
                        <td>{c.currentVersion === undefined ? '—' : `v${String(c.currentVersion)}`}</td>
                        <td>{latest === undefined ? '—' : <>v{latest.version} <Status value={latest.status} /></>}</td>
                        <td><button type="button" className="ztax-btn-quiet" onClick={() => { setSelected(c.id) }}>Open</button></td>
                      </tr>
                    )
                  })}
                </tbody>
              </table>
            </div>
          )}
        </Loaded>
      </Card>
      {selected === null ? null : <CrosswalkDetail id={selected} roles={roles} me={me} onChanged={reload} />}
      {author ? <NewCrosswalk onDone={(c) => { setSelected(c.id); reload() }} /> : null}
    </>
  )
}

const CrosswalkDetail = ({ id, roles, me, onChanged }: { id: string; roles: readonly string[]; me: string; onChanged: () => void }) => {
  const { load, reload } = useLoad((signal) => ztax.getCrosswalk(id, signal), [id])
  const changed = () => { reload(); onChanged() }
  return (
    <Loaded load={load}>
      {(c) => (
        <Card title={`${c.vendor} ${c.namespace} · release ${c.vendorRelease}`} aside={<Id value={c.id} />}>
          <p className="muted">{c.title}. Advisory only — never fiscal authority.</p>
          {[...c.versions].reverse().map((v) => (
            <VersionView key={v.version} crosswalk={c} version={v} roles={roles} me={me} onChanged={changed} />
          ))}
          <Lookup crosswalk={c} />
          {(roles.includes('OPERATOR') || roles.includes('ANALYST')) ? <NextVersion crosswalk={c} onDone={changed} /> : null}
        </Card>
      )}
    </Loaded>
  )
}

const VersionView = ({ crosswalk, version: v, roles, me, onChanged }: {
  crosswalk: Crosswalk; version: CrosswalkVersion; roles: readonly string[]; me: string; onChanged: () => void
}) => {
  const [act, setAct] = useState<Offer | null>(null)
  const offers = crosswalkOffers(v, roles, me)
  return (
    <section style={{ borderTop: '1px solid var(--line, #ddd)', paddingTop: 12, marginTop: 12 }}>
      <h3 className="ztax-card-title">Version {v.version} <Status value={v.status} /></h3>
      <p className="muted">{CROSSWALK_MEANING[v.status]}</p>
      <dl className="ztax-kv">
        <dt>Grant</dt>
        <dd>{RIGHTS_BASES.find((b) => b.value === v.rights.basis)?.label} — {v.rights.reference}
          {v.rights.expiresOn === undefined ? '' : `, until ${v.rights.expiresOn}`}</dd>
        <dt>Effective</dt><dd>{v.effectiveFrom} → {v.effectiveTo ?? 'open'} (end-exclusive)</dd>
        <dt>Content digest</dt><dd><Id value={v.contentDigest} /></dd>
        <dt>Revision</dt><dd>{v.revision}</dd>
      </dl>
      <div className="zp-scroll">
        <table className="table">
          <thead><tr><th scope="col">External code</th><th scope="col">Vendor meaning</th><th scope="col">Match</th><th scope="col">Canonical service</th><th scope="col">Predicate</th></tr></thead>
          <tbody>
            {v.entries.map((e) => (
              <tr key={`${e.externalCode}|${e.predicate ?? ''}`}>
                <td className="mono">{e.externalCode}</td><td>{e.description}</td><td>{e.match.replaceAll('_', ' ')}</td>
                <td className="mono">{e.serviceTarget ?? '—'}</td><td>{e.predicate ?? '—'}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <details>
        <summary>History ({v.history.length})</summary>
        <ol>
          {v.history.map((e) => <li key={e.seq}>{e.kind.replaceAll('_', ' ')} · {e.recordedAt} · <Id value={e.recordedBy} />{e.reason === undefined ? '' : ` — ${e.reason}`}</li>)}
        </ol>
      </details>
      {offers.length === 0 ? null : (
        <div className="ztax-form-actions" style={{ flexWrap: 'wrap', gap: 8 }}>
          {offers.map((o) => (
            <button key={o.act + o.label} type="button" className="ztax-btn-quiet" disabled={o.blocked !== null}
              title={o.blocked ?? undefined} onClick={() => { setAct(o) }}>{o.label}</button>
          ))}
        </div>
      )}
      {offers.filter((o) => o.blocked !== null).map((o) => <p key={o.label} className="muted">{o.label}: {o.blocked}</p>)}
      {act === null ? null : (
        <VersionMove crosswalk={crosswalk} version={v} offer={act} onDone={() => { setAct(null); onChanged() }} />
      )}
    </section>
  )
}

const VersionMove = ({ crosswalk, version, offer, onDone }: {
  crosswalk: Crosswalk; version: CrosswalkVersion; offer: Offer; onDone: () => void
}) => {
  const [reason, setReason] = useState('')
  const action = useAction<Crosswalk>()
  const submit = (e: FormEvent) => {
    e.preventDefault()
    const to = CROSSWALK_STATUSES.find((s) => s === offer.act)
    if (to === undefined) return
    void action.run(() => ztax.transitionCrosswalkVersion(crosswalk.id, version.version, {
      to, expectedRevision: version.revision, ...(reason.trim() === '' ? {} : { reason: reason.trim() }),
    })).then((done) => { if (done !== null) onDone() })
  }
  return (
    <form className="ztax-form" onSubmit={submit} style={{ marginTop: 12 }}>
      <Field label={offer.reason ? 'Reason' : 'Note (optional)'} help={`Applies to revision ${String(version.revision)} exactly.`}>
        <textarea value={reason} required={offer.reason} maxLength={2000} onChange={(e) => { setReason(e.target.value) }} />
      </Field>
      <div className="ztax-form-actions"><Submit busy={action.busy}>{offer.label}</Submit></div>
      <Refusal error={action.error} />
    </form>
  )
}

const Lookup = ({ crosswalk }: { crosswalk: Crosswalk }) => {
  const [code, setCode] = useState('')
  const [on, setOn] = useState('')
  const action = useAction<CrosswalkLookup>()
  const submit = (e: FormEvent) => {
    e.preventDefault()
    void action.run(() => ztax.lookupCrosswalk(crosswalk.id, { code, ...(on === '' ? {} : { on }) }))
  }
  const got = action.done
  return (
    <form className="ztax-form" onSubmit={submit} style={{ marginTop: 16 }}>
      <h3 className="ztax-card-title" style={{ gridColumn: '1 / -1' }}>Look up a code (exact, advisory)</h3>
      <Field label="External code"><input value={code} required maxLength={128} onChange={(e) => { setCode(e.target.value) }} /></Field>
      <Field label="Transaction date" help="Defaults to today."><input type="date" value={on} onChange={(e) => { setOn(e.target.value) }} /></Field>
      <div className="ztax-form-actions"><Submit busy={action.busy}>Look up</Submit></div>
      <Refusal error={action.error} />
      {got === null ? null : (
        <div style={{ gridColumn: '1 / -1' }} aria-live="polite">
          <p><Status value={got.resolution} /> {RESOLUTION_TEXT[got.resolution]}{got.version === undefined ? '' : ` · version ${String(got.version)}`}</p>
          <p className="muted">{got.detail}</p>
          {got.candidates.length === 0 ? null : (
            <ul>{got.candidates.map((c) => <li key={c.predicate ?? c.externalCode}><span className="mono">{c.serviceTarget ?? 'no equivalent'}</span>{c.predicate === undefined ? '' : ` — when ${c.predicate}`}</li>)}</ul>
          )}
        </div>
      )}
    </form>
  )
}

interface VersionDraft {
  basis: CrosswalkRightsBasis
  reference: string
  expiresOn: string
  evidence: string
  effectiveFrom: string
  effectiveTo: string
  entries: string
  submit: boolean
}

const emptyDraft = (): VersionDraft => ({
  basis: 'CUSTOMER_SUPPLIED', reference: '', expiresOn: '', evidence: '', effectiveFrom: '', effectiveTo: '', entries: '', submit: true,
})

const versionBody = (d: VersionDraft) => {
  const parsed = parseEntries(d.entries)
  return {
    error: parsed.error,
    body: {
      rights: {
        basis: d.basis, reference: d.reference.trim(),
        ...(d.expiresOn === '' ? {} : { expiresOn: d.expiresOn }), ...(d.evidence === '' ? {} : { evidenceSha256: d.evidence }),
      },
      effectiveFrom: d.effectiveFrom, ...(d.effectiveTo === '' ? {} : { effectiveTo: d.effectiveTo }),
      entries: parsed.entries, submit: d.submit,
    },
  }
}

const VersionFields = ({ draft, onChange }: { draft: VersionDraft; onChange: (d: VersionDraft) => void }) => {
  const set = (p: Partial<VersionDraft>) => { onChange({ ...draft, ...p }) }
  return (
    <>
      <Field label="Rights basis" help="On what footing Zoiko holds this vendor's code data.">
        <select value={draft.basis} onChange={(e) => { set({ basis: RIGHTS_BASES.find((b) => b.value === e.target.value)?.value ?? 'CUSTOMER_SUPPLIED' }) }}>
          {RIGHTS_BASES.map((b) => <option key={b.value} value={b.value}>{b.label}</option>)}
        </select>
      </Field>
      <Field label="Grant reference" help="Contract, written authorization, or documentation URL and edition.">
        <input value={draft.reference} required maxLength={200} onChange={(e) => { set({ reference: e.target.value }) }} />
      </Field>
      <Field label="Grant ends (optional)"><input type="date" value={draft.expiresOn} onChange={(e) => { set({ expiresOn: e.target.value }) }} /></Field>
      <Field label="Grant document (optional)" help={draft.evidence === '' ? 'Hashed here; never uploaded.' : `SHA-256 ${draft.evidence.slice(0, 12)}…`}>
        <input type="file" onChange={(e) => {
          const f = e.target.files?.[0]
          if (f === undefined) { set({ evidence: '' }); return }
          void sha256Hex(f).then((h) => { set({ evidence: h }) })
        }} />
      </Field>
      <Field label="Effective from"><input type="date" value={draft.effectiveFrom} required onChange={(e) => { set({ effectiveFrom: e.target.value }) }} /></Field>
      <Field label="Effective to (optional, exclusive)"><input type="date" value={draft.effectiveTo} onChange={(e) => { set({ effectiveTo: e.target.value }) }} /></Field>
      <Field label="Entries" help={ENTRY_HELP}>
        <textarea rows={6} value={draft.entries} required className="mono"
          placeholder={'010101 | Local exchange service | EXACT | ontology:telecom/voice/fixed/local\n990101 | Vendor adjustment | NO_EQUIVALENT'}
          onChange={(e) => { set({ entries: e.target.value }) }} />
      </Field>
      <label className="ztax-field" style={{ flexDirection: 'row', gap: 6 }}>
        <input type="checkbox" checked={draft.submit} onChange={(e) => { set({ submit: e.target.checked }) }} />
        <span>Submit for review now</span>
      </label>
      <p className="muted" style={{ gridColumn: '1 / -1' }}>Match qualities: {MATCHES.map((m) => m.label).join('; ')}.</p>
    </>
  )
}

const NewCrosswalk = ({ onDone }: { onDone: (c: Crosswalk) => void }) => {
  const [key, setKey] = useState(newKey)
  const [vendor, setVendor] = useState('')
  const [namespace, setNamespace] = useState('')
  const [release, setRelease] = useState('')
  const [title, setTitle] = useState('')
  const [draft, setDraft] = useState(emptyDraft)
  const [local, setLocal] = useState<string | null>(null)
  const action = useAction<Crosswalk>()
  const submit = (e: FormEvent) => {
    e.preventDefault()
    const { error, body } = versionBody(draft)
    setLocal(error)
    if (error !== null) return
    void action.run(() => ztax.createCrosswalk(key, {
      vendor: vendor.trim().toUpperCase(), namespace: namespace.trim().toUpperCase(), vendorRelease: release.trim(), title: title.trim(), version: body,
    })).then((done) => {
      if (done !== null) {
        setKey(newKey())
        setDraft(emptyDraft())
        onDone(done)
      }
    })
  }
  return (
    <Card title="New crosswalk">
      <form className="ztax-form" onSubmit={submit}>
        <Field label="Vendor" help="e.g. SURETAX, CERETAX, AVALARA, BSS_CUSTOM"><input value={vendor} required maxLength={64} onChange={(e) => { setVendor(e.target.value) }} /></Field>
        <Field label="Code namespace" help="e.g. TRANS_TYPE, PS_CODE"><input value={namespace} required maxLength={64} onChange={(e) => { setNamespace(e.target.value) }} /></Field>
        <Field label="Vendor release"><input value={release} required maxLength={64} onChange={(e) => { setRelease(e.target.value) }} /></Field>
        <Field label="Title"><input value={title} required maxLength={200} onChange={(e) => { setTitle(e.target.value) }} /></Field>
        <VersionFields draft={draft} onChange={setDraft} />
        <div className="ztax-form-actions"><Submit busy={action.busy}>Write version 1</Submit></div>
        {local === null ? null : <p className="ztax-error-note" role="alert">{local}</p>}
        <Refusal error={action.error} />
      </form>
    </Card>
  )
}

const NextVersion = ({ crosswalk, onDone }: { crosswalk: Crosswalk; onDone: () => void }) => {
  const [open, setOpen] = useState(false)
  const [key, setKey] = useState(newKey)
  const [draft, setDraft] = useState(emptyDraft)
  const [local, setLocal] = useState<string | null>(null)
  const action = useAction<Crosswalk>()
  const pending = crosswalk.versions.find((v) => v.status === 'DRAFT' || v.status === 'IN_REVIEW')
  if (pending !== undefined) {
    return <p className="muted" style={{ marginTop: 12 }}>Version {pending.version} is {pending.status.replaceAll('_', ' ')}; finish or withdraw it before writing another.</p>
  }
  if (!open) {
    return <button type="button" className="ztax-btn-quiet" style={{ marginTop: 12 }} onClick={() => { setOpen(true) }}>Write the next version</button>
  }
  const submit = (e: FormEvent) => {
    e.preventDefault()
    const { error, body } = versionBody(draft)
    setLocal(error)
    if (error !== null) return
    void action.run(() => ztax.addCrosswalkVersion(crosswalk.id, key, body)).then((done) => {
      if (done !== null) {
        setKey(newKey())
        setOpen(false)
        onDone()
      }
    })
  }
  return (
    <form className="ztax-form" onSubmit={submit} style={{ marginTop: 12 }}>
      <h3 className="ztax-card-title" style={{ gridColumn: '1 / -1' }}>Next version — written whole; earlier versions never change</h3>
      <VersionFields draft={draft} onChange={setDraft} />
      <div className="ztax-form-actions"><Submit busy={action.busy}>Write version</Submit></div>
      {local === null ? null : <p className="ztax-error-note" role="alert">{local}</p>}
      <Refusal error={action.error} />
    </form>
  )
}
