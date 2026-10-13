import { useState, type FormEvent } from 'react'

import type { S } from '@zoikotax/sdk'

import { instant } from '../../dashboard/format'
import { ztax } from '../../platform/client'
import { Card, Empty, Field, Id, Loaded, Outcome, Status, Submit, useAction, useLoad } from '../../ui/kit'
import { fromLocalInput, lines, toLocalInput } from './time'

type Hold = S<'LegalHold'>
type Scope = S<'LegalHoldScope'>

/** The scope form's fields, as typed. */
interface ScopeDraft {
  businessKeys: string
  decisionIds: string
  eventFrom: string
  eventTo: string
  legalEntityId: string
}

const EMPTY_DRAFT: ScopeDraft = { businessKeys: '', decisionIds: '', eventFrom: '', eventTo: '', legalEntityId: '' }

function draftOf(scope: Scope): ScopeDraft {
  return {
    businessKeys: (scope.businessKeys ?? []).join('\n'),
    decisionIds: (scope.decisionIds ?? []).join('\n'),
    eventFrom: scope.eventFrom === undefined ? '' : toLocalInput(scope.eventFrom),
    eventTo: scope.eventTo === undefined ? '' : toLocalInput(scope.eventTo),
    legalEntityId: scope.legalEntityId ?? '',
  }
}

/** The scope the API takes, or why the draft is not one. */
function scopeOf(d: ScopeDraft): { scope: Scope } | { problem: string } {
  const scope: Scope = {}
  const keys = lines(d.businessKeys)
  const ids = lines(d.decisionIds)
  if (keys.length > 0) scope.businessKeys = keys
  if (ids.length > 0) scope.decisionIds = ids
  if (d.eventFrom !== '' || d.eventTo !== '') {
    const from = fromLocalInput(d.eventFrom)
    const to = fromLocalInput(d.eventTo)
    if (from === null || to === null) {
      return { problem: 'A time window needs both its start and its end.' }
    }
    scope.eventFrom = from
    scope.eventTo = to
  }
  if (d.legalEntityId.trim() !== '') scope.legalEntityId = d.legalEntityId.trim()
  if (keys.length === 0 && ids.length === 0 && scope.eventFrom === undefined) {
    return { problem: 'Name business keys or decisions, or give a time window. A hold never covers a tenant at large.' }
  }
  return { scope }
}

/** "2 business keys · 1 decision · window … · entity …" */
function scopeSummary(scope: Scope): string {
  const parts: string[] = []
  const keys = scope.businessKeys?.length ?? 0
  const ids = scope.decisionIds?.length ?? 0
  if (keys > 0) parts.push(`${keys} business ${keys === 1 ? 'key' : 'keys'}`)
  if (ids > 0) parts.push(`${ids} ${ids === 1 ? 'decision' : 'decisions'}`)
  if (scope.eventFrom !== undefined && scope.eventTo !== undefined) {
    parts.push(`events ${scope.eventFrom.slice(0, 10)} → ${scope.eventTo.slice(0, 10)}`)
  }
  if (scope.legalEntityId !== undefined) parts.push(`entity ${scope.legalEntityId.slice(0, 8)}…`)
  return parts.length === 0 ? '—' : parts.join(' · ')
}

const ScopeFields = ({ draft, onChange }: { draft: ScopeDraft; onChange: (d: ScopeDraft) => void }) => (
  <>
    <Field label="Business keys (one per line)">
      <textarea
        className="mono"
        rows={3}
        value={draft.businessKeys}
        placeholder="INV-0001/1"
        onChange={(e) => { onChange({ ...draft, businessKeys: e.target.value }) }}
      />
    </Field>
    <Field label="Decision ids (one per line)">
      <textarea
        className="mono"
        rows={3}
        value={draft.decisionIds}
        onChange={(e) => { onChange({ ...draft, decisionIds: e.target.value }) }}
      />
    </Field>
    <Field label="Events from (UTC)" help="Window of event times [from, to), at most ten years.">
      <input
        type="datetime-local"
        value={draft.eventFrom}
        onChange={(e) => { onChange({ ...draft, eventFrom: e.target.value }) }}
      />
    </Field>
    <Field label="Events to (UTC, exclusive)">
      <input
        type="datetime-local"
        value={draft.eventTo}
        onChange={(e) => { onChange({ ...draft, eventTo: e.target.value }) }}
      />
    </Field>
    <Field label="Legal entity id (optional)" help="Narrows the names or the window to one entity.">
      <input
        className="mono"
        value={draft.legalEntityId}
        onChange={(e) => { onChange({ ...draft, legalEntityId: e.target.value }) }}
      />
    </Field>
  </>
)

const ScopeView = ({ scope }: { scope: Scope }) => (
  <dl className="ztax-kv">
    <dt>Business keys</dt>
    <dd>{(scope.businessKeys ?? []).length === 0 ? '—' : scope.businessKeys?.map((k) => <span key={k} className="tag mono">{k}</span>)}</dd>
    <dt>Decisions</dt>
    <dd>{(scope.decisionIds ?? []).length === 0 ? '—' : scope.decisionIds?.map((k) => <span key={k} className="tag mono">{k}</span>)}</dd>
    <dt>Event window</dt>
    <dd>
      {scope.eventFrom === undefined || scope.eventTo === undefined
        ? '—'
        : `[${instant(scope.eventFrom)} → ${instant(scope.eventTo)})`}
    </dd>
    <dt>Legal entity</dt>
    <dd className="mono">{scope.legalEntityId ?? 'any'}</dd>
  </dl>
)

export const HoldsTab = ({ admin }: { admin: boolean }) => {
  const { load, reload } = useLoad((signal) => ztax.listLegalHolds(signal), [])
  const [selected, setSelected] = useState<string | null>(null)

  return (
    <>
      <Card title="Legal holds" aside={<button type="button" className="ztax-btn-quiet" onClick={reload}>Refresh</button>}>
        <p className="muted zp-intro">
          A hold serves a named claim, investigation or regulator request and blocks disposal of what it scopes, past any
          retention period. Reading this list is recorded in the audit trail as privileged evidence access.
        </p>
        <Loaded load={load}>
          {(data) =>
            data.holds.length === 0 ? (
              <Empty>No legal hold has been placed.</Empty>
            ) : (
              <div className="zp-scroll">
                <table className="table">
                  <thead>
                    <tr>
                      <th scope="col">Matter</th>
                      <th scope="col">Status</th>
                      <th scope="col">Scope</th>
                      <th scope="col">History</th>
                      <th scope="col">Hold</th>
                      <th scope="col"><span className="zp-sr-only">Open</span></th>
                    </tr>
                  </thead>
                  <tbody>
                    {data.holds.map((h) => (
                      <tr key={h.id} className={h.id === selected ? 'row--current' : undefined}>
                        <td>{h.matter}</td>
                        <td><Status value={h.status} /></td>
                        <td>{scopeSummary(h.scope)}</td>
                        <td>{h.history.length} {h.history.length === 1 ? 'entry' : 'entries'}</td>
                        <td><Id value={h.id} /></td>
                        <td>
                          <button
                            type="button"
                            className="ztax-btn-quiet"
                            aria-pressed={h.id === selected}
                            onClick={() => { setSelected(h.id === selected ? null : h.id) }}
                          >
                            {h.id === selected ? 'Close' : 'Open'}
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

      {load.status === 'ready' && selected !== null
        ? (() => {
            const hold = load.data.holds.find((h) => h.id === selected)
            return hold === undefined ? null : <HoldDetail key={hold.id} hold={hold} admin={admin} onChanged={reload} />
          })()
        : null}

      {admin ? (
        <PlaceHoldForm
          onPlaced={(hold) => {
            reload()
            setSelected(hold.id)
          }}
        />
      ) : (
        <Card title="Place a legal hold">
          <p className="muted zp-intro">Placing, changing and releasing holds needs the ADMIN role.</p>
        </Card>
      )}
    </>
  )
}

const HoldDetail = ({ hold, admin, onChanged }: { hold: Hold; admin: boolean; onChanged: () => void }) => {
  const active = hold.status === 'ACTIVE'
  return (
    <Card title={`Hold · ${hold.matter}`} aside={<Status value={hold.status} />}>
      <h3 className="ztax-card-title">Current scope</h3>
      <ScopeView scope={hold.scope} />

      <h3 className="ztax-card-title">History</h3>
      <ol className="zp-list">
        {hold.history.map((ev) => (
          <li key={ev.seq} style={{ marginBottom: 6 }}>
            <Status value={ev.kind === 'PLACED' ? 'ACTIVE' : ev.kind === 'RELEASED' ? 'RELEASED' : 'OPEN'} />{' '}
            <strong>{ev.kind.replaceAll('_', ' ').toLowerCase()}</strong> · {instant(ev.recordedAt)} · by{' '}
            <span className="mono">{ev.recordedBy.slice(0, 8)}…</span>
            <div>“{ev.reason}”</div>
            <div className="muted">Scope then: {scopeSummary(ev.scope)}</div>
          </li>
        ))}
      </ol>

      {!active ? (
        <p className="muted zp-intro">Released holds are final and cannot be changed. A matter that revives places a new hold.</p>
      ) : admin ? (
        <>
          <ChangeScopeForm hold={hold} onChanged={onChanged} />
          <ReleaseForm hold={hold} onReleased={onChanged} />
        </>
      ) : null}
    </Card>
  )
}

const PlaceHoldForm = ({ onPlaced }: { onPlaced: (hold: Hold) => void }) => {
  const [matter, setMatter] = useState('')
  const [reason, setReason] = useState('')
  const [draft, setDraft] = useState<ScopeDraft>(EMPTY_DRAFT)
  const [local, setLocal] = useState<string | null>(null)
  const place = useAction<Hold>()

  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    const built = scopeOf(draft)
    if ('problem' in built) {
      setLocal(built.problem)
      return
    }
    setLocal(null)
    void place.run(() => ztax.placeLegalHold({ matter: matter.trim(), reason: reason.trim(), scope: built.scope })).then((h) => {
      if (h !== null) {
        setMatter('')
        setReason('')
        setDraft(EMPTY_DRAFT)
        onPlaced(h)
      }
    })
  }

  return (
    <Card title="Place a legal hold">
      <p className="muted zp-intro">
        The scope must be specific: named decisions or business keys, or a bounded window of event times of at most ten
        years, optionally narrowed to one legal entity. Every criterion given must hold.
      </p>
      <form className="ztax-form" onSubmit={onSubmit} aria-label="Place a legal hold">
        <Field label="Matter" help="The claim, investigation or regulator request.">
          <input value={matter} required maxLength={255} placeholder="Claim 2026/114" onChange={(e) => { setMatter(e.target.value) }} />
        </Field>
        <Field label="Reason">
          <input value={reason} required maxLength={1000} placeholder="litigation notice received" onChange={(e) => { setReason(e.target.value) }} />
        </Field>
        <ScopeFields draft={draft} onChange={setDraft} />
        <div className="ztax-form-actions">
          <Submit busy={place.busy}>Place hold</Submit>
        </div>
      </form>
      {local === null ? null : <p className="ztax-error-note" role="alert">{local}</p>}
      <Outcome
        error={place.error}
        done={place.done === null ? null : <p className="ztax-ok-note" role="status">Hold placed for “{place.done.matter}”.</p>}
      />
    </Card>
  )
}

const ChangeScopeForm = ({ hold, onChanged }: { hold: Hold; onChanged: () => void }) => {
  const [draft, setDraft] = useState<ScopeDraft>(() => draftOf(hold.scope))
  const [reason, setReason] = useState('')
  const [local, setLocal] = useState<string | null>(null)
  const change = useAction<Hold>()

  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    const built = scopeOf(draft)
    if ('problem' in built) {
      setLocal(built.problem)
      return
    }
    setLocal(null)
    void change.run(() => ztax.changeLegalHoldScope(hold.id, { reason: reason.trim(), scope: built.scope })).then((h) => {
      if (h !== null) {
        setReason('')
        onChanged()
      }
    })
  }

  return (
    <details>
      <summary className="ztax-card-title" style={{ cursor: 'pointer' }}>Change scope</summary>
      <p className="muted zp-intro">The new scope replaces the old as a new history entry; the old one stays readable there.</p>
      <form className="ztax-form" onSubmit={onSubmit} aria-label="Change the hold's scope">
        <ScopeFields draft={draft} onChange={setDraft} />
        <Field label="Why the scope changes">
          <input value={reason} required maxLength={1000} onChange={(e) => { setReason(e.target.value) }} />
        </Field>
        <div className="ztax-form-actions">
          <Submit busy={change.busy}>Change scope</Submit>
        </div>
      </form>
      {local === null ? null : <p className="ztax-error-note" role="alert">{local}</p>}
      <Outcome
        error={change.error}
        done={change.done === null ? null : <p className="ztax-ok-note" role="status">Scope changed.</p>}
      />
    </details>
  )
}

const ReleaseForm = ({ hold, onReleased }: { hold: Hold; onReleased: () => void }) => {
  const [reason, setReason] = useState('')
  const release = useAction<Hold>()

  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    if (!window.confirm(`Release the hold for “${hold.matter}”? This is final and cannot be undone; a revived matter needs a new hold.`)) {
      return
    }
    void release.run(() => ztax.releaseLegalHold(hold.id, { reason: reason.trim() })).then((h) => {
      if (h !== null) onReleased()
    })
  }

  return (
    <details>
      <summary className="ztax-card-title" style={{ cursor: 'pointer' }}>Release</summary>
      <p className="muted zp-intro">
        Final. What the hold kept is governed by its retention policy again from the next verdict on, unless another
        active hold scopes it.
      </p>
      <form className="ztax-form" onSubmit={onSubmit} aria-label="Release the hold">
        <Field label="Why it is released">
          <input value={reason} required maxLength={1000} placeholder="claim settled" onChange={(e) => { setReason(e.target.value) }} />
        </Field>
        <div className="ztax-form-actions">
          <Submit busy={release.busy}>Release hold</Submit>
        </div>
      </form>
      <Outcome error={release.error} done={null} />
    </details>
  )
}
