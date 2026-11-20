import { useState, type FormEvent } from 'react'

import { ztax } from '../platform/client'
import { Card, Empty, ErrorNote, errorText, Field, Id, Loaded, newKey, PageHeader, Status, Submit, useAction, useLoad } from '../ui/kit'
import { TabBar } from './determine/shared'
import {
  ACTIONS, ADVISORY, changeBody, changeOffers, EMPTY_CHANGE, HOLD_REASONS, isAction, isHold, isKind, isPriority, isRecStatus,
  isSeverity, isStatus, KINDS, PRIORITIES, REC_STATUSES, reasonOf, recommendationOffers, refusalText, SEVERITIES, sha256Hex,
  STATUS_MEANING, STATUSES, targetKey, targetsFor, transitionBody,
  type Assessment, type Change, type ChangeForm, type ChangeKind, type ChangeStatus, type HoldReason, type Priority,
  type RecAction, type Recommendation, type RecStatus, type Severity,
} from './regchange/model'
import { useRoutedTab } from './routedTab'
import type { PageProps } from './types'
import { hasRole, Tiles, words } from './workspace'

// Regulatory Change Feed, Tax Impact Analysis and Review Recommendations
// (Engineering Doc 03 WF-05, WF-09; Sidebar WF-02). The cell records changes
// with their three clocks, assesses them deterministically against the active
// content and the tenant's own records, and runs the specialist recommend /
// independent review. Nothing here publishes content or changes a tax result.

type Tab = 'feed' | 'impact' | 'recommendations'

const TABS: readonly { value: Tab; label: string }[] = [
  { value: 'feed', label: 'Regulatory Change Feed' },
  { value: 'impact', label: 'Tax Impact Analysis' },
  { value: 'recommendations', label: 'Review Recommendations' },
]

const Refusal = ({ error }: { error: unknown }) => {
  if (error === null) return null
  const text = refusalText(reasonOf(error))
  return text === null ? <ErrorNote error={error} />
    : <p className="ztax-error-note" role="alert">{text} <span className="mono muted">{errorText(error)}</span></p>
}

export const RegulatoryIntelligencePage = ({ session, route, onTabChange }: PageProps) => {
  const [tab, setTab] = useRoutedTab(TABS.map((t) => t.value), 'feed', route, onTabChange)
  const [assessment, setAssessment] = useState<string | null>(null)
  const [recommendation, setRecommendation] = useState<string | null>(null)
  const roles = session.user.roles
  const me = session.user.id

  return (
    <div className="ztax-page">
      <PageHeader
        title="Regulatory Intelligence"
        subtitle="Reported regulatory changes with their published, effective and discovered dates; deterministic impact against the active content and your registrations and obligations; and recommendations a specialist recommends and a different person independently reviews."
      />
      <p className="ztax-note">{ADVISORY}</p>
      <TabBar label="Regulatory intelligence" tabs={TABS} current={tab} onSelect={setTab} />
      {tab === 'feed' ? (
        <Feed roles={roles} onAssessed={(id) => { setAssessment(id); setTab('impact') }} />
      ) : tab === 'impact' ? (
        <Impact roles={roles} selected={assessment} setSelected={setAssessment}
          onDrafted={(id) => { setRecommendation(id); setTab('recommendations') }} />
      ) : (
        <Recommendations roles={roles} me={me} selected={recommendation} setSelected={setRecommendation} />
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// feed
// ---------------------------------------------------------------------------

const Feed = ({ roles, onAssessed }: { roles: readonly string[]; onAssessed: (assessmentId: string) => void }) => {
  const [status, setStatus] = useState<ChangeStatus | ''>('')
  const [kind, setKind] = useState<ChangeKind | ''>('')
  const [priority, setPriority] = useState<Priority | ''>('')
  const [selected, setSelected] = useState<string | null>(null)
  const [recording, setRecording] = useState(false)
  const { load, reload } = useLoad(
    (signal) => ztax.listRegulatoryChanges({
      ...(status === '' ? {} : { status }), ...(kind === '' ? {} : { kind }), ...(priority === '' ? {} : { priority }), limit: 500,
    }, signal),
    [status, kind, priority],
  )
  const canRecord = hasRole(roles, 'OPERATOR', 'ANALYST')

  return (
    <>
      <Loaded load={load}>
        {(data) => (
          <Tiles tiles={[
            { label: 'Changes', count: data.changes.length },
            { label: 'New, untriaged', count: data.changes.filter((c) => c.status === 'NEW').length, tone: 'warn' },
            { label: 'Urgent', count: data.changes.filter((c) => c.priority === 'URGENT' && !['CLOSED', 'NO_IMPACT'].includes(c.status)).length, tone: 'bad' },
            { label: 'On hold', count: data.changes.filter((c) => c.status === 'ON_HOLD').length, tone: 'warn' },
          ]} />
        )}
      </Loaded>
      <Card
        title="Regulatory changes"
        aside={
          <div className="ztax-filter-bar">
            <Field label="Status">
              <select value={status} onChange={(e) => { setStatus(isStatus(e.target.value) ? e.target.value : '') }}>
                <option value="">All</option>
                {STATUSES.map((s) => <option key={s} value={s}>{words(s)}</option>)}
              </select>
            </Field>
            <Field label="Kind">
              <select value={kind} onChange={(e) => { setKind(isKind(e.target.value) ? e.target.value : '') }}>
                <option value="">All</option>
                {KINDS.map((k) => <option key={k.value} value={k.value}>{k.label}</option>)}
              </select>
            </Field>
            <Field label="Priority">
              <select value={priority} onChange={(e) => { setPriority(isPriority(e.target.value) ? e.target.value : '') }}>
                <option value="">All</option>
                {PRIORITIES.map((p) => <option key={p} value={p}>{words(p)}</option>)}
              </select>
            </Field>
            <button type="button" className="ztax-btn-quiet" onClick={reload}>Refresh</button>
            {canRecord ? (
              <button type="button" className="ztax-btn-quiet" aria-expanded={recording} onClick={() => { setRecording((r) => !r) }}>
                {recording ? 'Close the form' : 'Record a change'}
              </button>
            ) : null}
          </div>
        }
      >
        <Loaded load={load}>
          {(data) => data.changes.length === 0 ? (
            <Empty>No change matches. An OPERATOR or ANALYST records one from a source edition.</Empty>
          ) : (
            <div style={{ overflowX: 'auto' }}>
              <table className="ztax-action-table">
                <thead>
                  <tr><th>Jurisdiction</th><th>Kind</th><th>Title</th><th>Published</th><th>Effective</th><th>Detected by</th><th>Priority</th><th>Status</th><th>Action</th></tr>
                </thead>
                <tbody>
                  {data.changes.map((c: Change) => (
                    <tr key={c.id} className={c.id === selected ? 'ztax-row-selected' : undefined}>
                      <td>{c.jurisdiction}</td>
                      <td>{words(c.kind)}{c.retroactive ? <span className="muted"> · retroactive</span> : null}</td>
                      <td>{c.title}</td>
                      <td>{c.publishedOn}</td>
                      <td>{c.effectiveFrom}</td>
                      <td>{words(c.detectedBy)}</td>
                      <td>{c.priority === undefined ? '—' : words(c.priority)}</td>
                      <td><Status value={c.status} /></td>
                      <td>
                        <button type="button" className="ztax-table-action-btn" aria-pressed={c.id === selected} onClick={() => { setSelected(c.id) }}>
                          Open
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </Loaded>
      </Card>
      {recording ? <RecordForm onRecorded={(id) => { setRecording(false); setSelected(id); reload() }} /> : null}
      {selected === null ? null : (
        <ChangeDetail key={selected} id={selected} roles={roles} onChanged={reload} onAssessed={onAssessed}
          onClose={() => { setSelected(null) }} />
      )}
    </>
  )
}

const RecordForm = ({ onRecorded }: { onRecorded: (id: string) => void }) => {
  const [form, setForm] = useState<ChangeForm>(EMPTY_CHANGE)
  const [file, setFile] = useState<string | null>(null)
  const [invalid, setInvalid] = useState<string | null>(null)
  // One key per filled form: a retry of the same form is the same request.
  const [key, setKey] = useState(newKey)
  const action = useAction<Change>()
  const set = (k: keyof ChangeForm) => (v: string) => { setForm((f) => ({ ...f, [k]: v })) }

  const pick = async (f: File | undefined) => {
    if (f === undefined) return
    const sha256 = await sha256Hex(f)
    setFile(`${f.name} · ${sha256.slice(0, 12)}…`)
    setForm((x) => ({ ...x, sha256 }))
  }

  const submit = (e: FormEvent) => {
    e.preventDefault()
    setInvalid(null)
    const body = changeBody(form)
    if ('invalid' in body) {
      setInvalid(body.invalid)
      return
    }
    void action.run(() => ztax.recordRegulatoryChange(key, body)).then((c) => {
      if (c !== null) {
        setKey(newKey())
        onRecorded(c.id)
      }
    })
  }

  return (
    <Card title="Record a regulatory change">
      <form className="ztax-form" onSubmit={submit}>
        <Field label="Jurisdiction" help="A country, or a country and one subdivision: US, US-NY, GB.">
          <input required value={form.jurisdiction} onChange={(e) => { set('jurisdiction')(e.target.value.toUpperCase()) }} />
        </Field>
        <Field label="Kind" help="One legal effect per change. A source with two effects is recorded twice, one per kind.">
          <select value={form.kind} onChange={(e) => { if (isKind(e.target.value)) set('kind')(e.target.value) }}>
            {KINDS.map((k) => <option key={k.value} value={k.value}>{k.label}</option>)}
          </select>
        </Field>
        <Field label="Title">
          <input required maxLength={200} value={form.title} onChange={(e) => { set('title')(e.target.value) }} />
        </Field>
        <Field label="Summary">
          <textarea maxLength={4000} value={form.summary} onChange={(e) => { set('summary')(e.target.value) }} />
        </Field>
        <Field label="Publishing authority">
          <input required maxLength={200} value={form.authority} onChange={(e) => { set('authority')(e.target.value) }} />
        </Field>
        <Field label="Citation">
          <input required maxLength={200} value={form.citation} onChange={(e) => { set('citation')(e.target.value) }} />
        </Field>
        <Field label="Edition / version" help="The source's edition or version label as published.">
          <input required maxLength={200} value={form.edition} onChange={(e) => { set('edition')(e.target.value) }} />
        </Field>
        <Field label="Official URL" help="Optional, https only.">
          <input value={form.url} onChange={(e) => { set('url')(e.target.value) }} />
        </Field>
        <Field label="Captured source document" help="Hashed with SHA-256 in your browser. The file is not uploaded.">
          <input type="file" required onChange={(e) => { void pick(e.target.files?.[0]) }} />
        </Field>
        {file === null ? null : <p className="ztax-note mono">{file}</p>}
        <Field label="Published on" help="The authority's publication date.">
          <input type="date" required value={form.publishedOn} onChange={(e) => { set('publishedOn')(e.target.value) }} />
        </Field>
        <Field label="Effective from" help="The legal effective date. Never assumed to be the publication date.">
          <input type="date" required value={form.effectiveFrom} onChange={(e) => { set('effectiveFrom')(e.target.value) }} />
        </Field>
        <Field label="Detected by">
          <select value={form.detectedBy} onChange={(e) => { set('detectedBy')(e.target.value === 'MONITOR' ? 'MONITOR' : 'HUMAN') }}>
            <option value="HUMAN">Human legal intake</option>
            <option value="MONITOR">Source watcher alert (recorded by you)</option>
          </select>
        </Field>
        {form.detectedBy === 'MONITOR' ? (
          <Field label="Watcher alert reference">
            <input required maxLength={200} value={form.monitorRef} onChange={(e) => { set('monitorRef')(e.target.value) }} />
          </Field>
        ) : null}
        <Field label="Service families" help="Optional, comma separated: MOBILE_VOICE, MOBILE_DATA. Empty means the whole jurisdiction.">
          <input value={form.services} onChange={(e) => { set('services')(e.target.value) }} />
        </Field>
        <Field label="Tax types" help="Optional, comma separated as the content packs name them: SALES_TAX, TELECOM_EXCISE.">
          <input value={form.taxTypes} onChange={(e) => { set('taxTypes')(e.target.value) }} />
        </Field>
        <div className="ztax-form-actions">
          <Submit busy={action.busy}>Record</Submit>
        </div>
        {invalid === null ? null : <p className="ztax-error-note" role="alert" style={{ gridColumn: '1 / -1' }}>{invalid}</p>}
        <div style={{ gridColumn: '1 / -1' }}><Refusal error={action.error} /></div>
      </form>
    </Card>
  )
}

const ChangeDetail = ({ id, roles, onChanged, onAssessed, onClose }: {
  id: string
  roles: readonly string[]
  onChanged: () => void
  onAssessed: (assessmentId: string) => void
  onClose: () => void
}) => {
  const { load, reload } = useLoad((signal) => ztax.getRegulatoryChange(id, signal), [id])
  const [act, setAct] = useState<string | null>(null)
  const [priority, setPriority] = useState<Priority>('MATERIAL')
  const [hold, setHold] = useState<HoldReason>('MISSING_FACTS')
  const [reason, setReason] = useState('')
  const [invalid, setInvalid] = useState<string | null>(null)
  const move = useAction<Change>()
  const assess = useAction<Assessment>()

  const done = () => { setAct(null); setReason(''); reload(); onChanged() }

  return (
    <Card title="Regulatory change" aside={<button type="button" className="ztax-btn-quiet" onClick={onClose}>Close</button>}>
      <Loaded load={load}>
        {(c) => {
          const offers = changeOffers(c, roles)
          const submit = (e: FormEvent) => {
            e.preventDefault()
            setInvalid(null)
            if (act === null) return
            const body = transitionBody(c, act, { priority, hold, reason })
            if ('invalid' in body) {
              setInvalid(body.invalid)
              return
            }
            void move.run(() => ztax.transitionRegulatoryChange(c.id, body)).then((v) => { if (v !== null) done() })
          }
          return (
            <>
              <p><strong>{c.title}</strong> <Status value={c.status} /></p>
              <p className="ztax-note">{STATUS_MEANING[c.status]}</p>
              <dl className="ztax-dl">
                <dt>Jurisdiction / kind</dt><dd>{c.jurisdiction} · {words(c.kind)}{c.retroactive ? ' · retroactive' : ''}</dd>
                <dt>Source</dt><dd>{c.source.authority} — {c.source.citation} ({c.source.edition}){c.source.url === undefined ? null : <> · <a href={c.source.url} target="_blank" rel="noreferrer noopener">official source</a></>}</dd>
                <dt>Document SHA-256</dt><dd className="mono">{c.source.documentSha256}</dd>
                <dt>Published · effective · discovered</dt><dd>{c.publishedOn} · {c.effectiveFrom} · {c.discoveredAt}</dd>
                <dt>Detected by</dt><dd>{words(c.detectedBy)}{c.monitorRef === undefined ? '' : ` (${c.monitorRef})`}</dd>
                <dt>Scope</dt><dd>{[...c.serviceFamilies, ...c.taxTypes].join(', ') || 'Whole jurisdiction'}</dd>
                {c.summary === undefined ? null : <><dt>Summary</dt><dd>{c.summary}</dd></>}
                {c.holdReason === undefined ? null : <><dt>Held for</dt><dd>{words(c.holdReason)}</dd></>}
                {c.disposition === undefined ? null : <><dt>Disposition</dt><dd>{words(c.disposition)}</dd></>}
                {c.currentAssessmentId === undefined ? null : (
                  <><dt>Current assessment</dt><dd>
                    <button type="button" className="ztax-btn-quiet" onClick={() => { if (c.currentAssessmentId !== undefined) onAssessed(c.currentAssessmentId) }}>
                      {words(c.currentSeverity ?? '')} · open
                    </button>{' '}{c.acceptedRecommendationCount} of {c.recommendationCount} recommendation(s) accepted on it
                  </dd></>
                )}
                {c.relatedChangeIds.length === 0 ? null : <><dt>Same source edition</dt><dd>{c.relatedChangeIds.map((r) => <Id key={r} value={r} />)}</dd></>}
                <dt>Source key</dt><dd className="mono">{c.sourceKey}</dd>
              </dl>
              {offers.length === 0 ? null : (
                <div className="ztax-filter-bar">
                  {offers.map((o) => (
                    <button key={o.act} type="button" className="ztax-btn-quiet" disabled={o.blocked !== null || assess.busy}
                      title={o.blocked ?? undefined} aria-pressed={act === o.act}
                      onClick={() => {
                        if (o.act === 'ASSESS') {
                          void assess.run(() => ztax.assessRegulatoryChange(c.id)).then((a) => {
                            if (a !== null) { reload(); onChanged(); onAssessed(a.id) }
                          })
                        } else {
                          setAct(o.act)
                        }
                      }}>
                      {o.label}
                    </button>
                  ))}
                </div>
              )}
              {offers.filter((o) => o.blocked !== null).map((o) => <p key={o.act} className="ztax-note">{o.label}: {o.blocked}</p>)}
              <Refusal error={assess.error} />
              {act === null ? null : (
                <form className="ztax-form" onSubmit={submit}>
                  {act === 'TRIAGED' ? (
                    <Field label="Priority" help="Urgent: route within four monitored hours. Material: triage within one business day.">
                      <select value={priority} onChange={(e) => { if (isPriority(e.target.value)) setPriority(e.target.value) }}>
                        {PRIORITIES.map((p) => <option key={p} value={p}>{words(p)}</option>)}
                      </select>
                    </Field>
                  ) : null}
                  {act === 'ON_HOLD' ? (
                    <Field label="Hold reason">
                      <select value={hold} onChange={(e) => { if (isHold(e.target.value)) setHold(e.target.value) }}>
                        {HOLD_REASONS.map((h) => <option key={h} value={h}>{words(h)}</option>)}
                      </select>
                    </Field>
                  ) : null}
                  <Field label="Rationale">
                    <textarea required maxLength={2000} value={reason} onChange={(e) => { setReason(e.target.value) }} />
                  </Field>
                  <div className="ztax-form-actions">
                    <Submit busy={move.busy}>{offers.find((o) => o.act === act)?.label ?? 'Record'}</Submit>
                    <button type="button" className="ztax-btn-quiet" onClick={() => { setAct(null) }}>Cancel</button>
                  </div>
                  {invalid === null ? null : <p className="ztax-error-note" role="alert">{invalid}</p>}
                  <div style={{ gridColumn: '1 / -1' }}><Refusal error={move.error} /></div>
                </form>
              )}
              <History rows={c.history.map((e) => ({
                key: e.seq, what: words(e.kind), status: e.status,
                detail: [e.priority, e.holdReason, e.disposition].filter((x) => x !== undefined).map((x) => words(x)).join(' · '),
                reason: e.reason, at: e.recordedAt, by: e.recordedBy,
              }))} />
            </>
          )
        }}
      </Loaded>
    </Card>
  )
}

const History = ({ rows }: {
  rows: readonly { key: number; what: string; status: string; detail: string; reason: string | undefined; at: string; by: string }[]
}) => (
  <div style={{ overflowX: 'auto' }}>
    <table className="ztax-action-table">
      <thead><tr><th>#</th><th>Event</th><th>Status</th><th>Detail</th><th>Reason</th><th>When</th><th>Who</th></tr></thead>
      <tbody>
        {rows.map((r) => (
          <tr key={r.key}>
            <td>{r.key}</td><td>{r.what}</td><td><Status value={r.status} /></td><td>{r.detail}</td><td>{r.reason ?? ''}</td>
            <td className="mono">{r.at}</td><td><Id value={r.by} /></td>
          </tr>
        ))}
      </tbody>
    </table>
  </div>
)

// ---------------------------------------------------------------------------
// impact
// ---------------------------------------------------------------------------

const Impact = ({ roles, selected, setSelected, onDrafted }: {
  roles: readonly string[]
  selected: string | null
  setSelected: (id: string | null) => void
  onDrafted: (id: string) => void
}) => {
  const [severity, setSeverity] = useState<Severity | ''>('')
  const { load, reload } = useLoad(
    (signal) => ztax.listImpactAssessments({ ...(severity === '' ? {} : { minSeverity: severity }), limit: 500 }, signal),
    [severity],
  )
  return (
    <>
      <Card
        title="Tax impact assessments"
        aside={
          <div className="ztax-filter-bar">
            <Field label="At least">
              <select value={severity} onChange={(e) => { setSeverity(isSeverity(e.target.value) ? e.target.value : '') }}>
                <option value="">Any severity</option>
                {SEVERITIES.map((s) => <option key={s} value={s}>{words(s)}</option>)}
              </select>
            </Field>
            <button type="button" className="ztax-btn-quiet" onClick={reload}>Refresh</button>
          </div>
        }
      >
        <Loaded load={load}>
          {(data) => data.assessments.length === 0 ? (
            <Empty>No assessment yet. An ANALYST runs one from a triaged change in the feed.</Empty>
          ) : (
            <div style={{ overflowX: 'auto' }}>
              <table className="ztax-action-table">
                <thead><tr><th>Change</th><th>Severity</th><th>Items</th><th>Content as of</th><th>Bundle</th><th>Current</th><th>Assessed</th><th>Action</th></tr></thead>
                <tbody>
                  {data.assessments.map((a) => (
                    <tr key={a.id} className={a.id === selected ? 'ztax-row-selected' : undefined}>
                      <td><Id value={a.changeId} /></td>
                      <td><Status value={a.severity} />{a.retroactive ? <span className="muted"> · retroactive</span> : null}</td>
                      <td>{a.items.length}</td>
                      <td>{a.asOf}</td>
                      <td className="mono">{a.bundleId}</td>
                      <td>{a.current ? 'Current' : 'Superseded'}</td>
                      <td className="mono">{a.createdAt}</td>
                      <td>
                        <button type="button" className="ztax-table-action-btn" aria-pressed={a.id === selected} onClick={() => { setSelected(a.id) }}>
                          Open
                        </button>
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
        <AssessmentDetail key={selected} id={selected} roles={roles} onDrafted={onDrafted} onClose={() => { setSelected(null) }} />
      )}
    </>
  )
}

const AssessmentDetail = ({ id, roles, onDrafted, onClose }: {
  id: string
  roles: readonly string[]
  onDrafted: (id: string) => void
  onClose: () => void
}) => {
  const { load } = useLoad((signal) => ztax.getImpactAssessment(id, signal), [id])
  return (
    <Card title="Impact assessment" aside={<button type="button" className="ztax-btn-quiet" onClick={onClose}>Close</button>}>
      <Loaded load={load}>
        {(a) => (
          <>
            <p>
              <Status value={a.severity} /> {a.current ? 'This is the change’s current assessment.' : 'Superseded by a later assessment of the same change.'}
            </p>
            <dl className="ztax-dl">
              <dt>Change</dt><dd><Id value={a.changeId} /></dd>
              <dt>Content</dt><dd className="mono">{a.bundleId} · {a.bundleDigest} · as of {a.asOf}</dd>
              <dt>Read</dt><dd>{a.componentsRead} content components · {a.registrationsRead} registrations · {a.obligationsRead} obligations</dd>
              <dt>Inputs digest</dt><dd className="mono">{a.inputsDigest}</dd>
              <dt>Assessment digest</dt><dd className="mono">{a.assessmentDigest}</dd>
            </dl>
            <p className="ztax-note">The same inputs give the same digests: assessing again on unchanged inputs records nothing.</p>
            <div style={{ overflowX: 'auto' }}>
              <table className="ztax-action-table">
                <thead><tr><th>Item</th><th>Reference</th><th>Jurisdiction</th><th>Severity</th><th>Basis</th></tr></thead>
                <tbody>
                  {a.items.map((it) => (
                    <tr key={targetKey(it)}>
                      <td>{words(it.kind)}{it.retrospective ? <span className="muted"> · retrospective</span> : null}</td>
                      <td>{it.label}<br /><span className="mono muted">{it.ref}</span></td>
                      <td className="mono">{it.jurisdiction}</td>
                      <td><Status value={it.severity} /></td>
                      <td>{it.basis}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            {a.current && hasRole(roles, 'ANALYST') ? <DraftForm assessment={a} onDrafted={onDrafted} /> : (
              <p className="ztax-note">{a.current ? 'Drafting a recommendation needs the ANALYST role.' : 'Recommendations are drafted on the current assessment only.'}</p>
            )}
          </>
        )}
      </Loaded>
    </Card>
  )
}

const DraftForm = ({ assessment, onDrafted }: { assessment: Assessment; onDrafted: (id: string) => void }) => {
  const [actionKind, setActionKind] = useState<RecAction>('UPDATE_RULE')
  const [rationale, setRationale] = useState('')
  const [chosen, setChosen] = useState<readonly string[]>([])
  const [aiAssisted, setAiAssisted] = useState(false)
  const [ai, setAi] = useState({ useCase: '', modelProfile: '', providerProfile: '', promptProfile: '', aiTrainVersion: '', invocationId: '', draft: '' })
  const [key, setKey] = useState(newKey)
  const draft = useAction<Recommendation>()
  const eligible = targetsFor(actionKind, assessment.items)
  const needs = ACTIONS.find((x) => x.value === actionKind)?.needs ?? ''

  const submit = (e: FormEvent) => {
    e.preventDefault()
    const targets = eligible.filter((it) => chosen.includes(targetKey(it))).map((it) => ({ kind: it.kind, ref: it.ref }))
    const invocationId = ai.invocationId.trim()
    void draft.run(() => ztax.draftReviewRecommendation(key, {
      assessmentId: assessment.id, origin: aiAssisted ? 'AI_ASSISTED' : 'HUMAN', action: actionKind, rationale: rationale.trim(), targets,
      ...(aiAssisted ? {
        ai: {
          useCase: ai.useCase.trim(), modelProfile: ai.modelProfile.trim(), providerProfile: ai.providerProfile.trim(),
          promptProfile: ai.promptProfile.trim(), aiTrainVersion: ai.aiTrainVersion.trim(), authorityOutcome: 'A1' as const,
          draft: ai.draft.trim(), ...(invocationId === '' ? {} : { invocationId }),
        },
      } : {}),
    })).then((r) => {
      if (r !== null) {
        setKey(newKey())
        onDrafted(r.id)
      }
    })
  }
  const setAiField = (k: keyof typeof ai) => (v: string) => { setAi((x) => ({ ...x, [k]: v })) }

  return (
    <form className="ztax-form" onSubmit={submit}>
      <h3 style={{ gridColumn: '1 / -1' }}>Draft a recommendation</h3>
      <Field label="Action" help={`Names ${needs}.`}>
        <select value={actionKind} onChange={(e) => { if (isAction(e.target.value)) { setActionKind(e.target.value); setChosen([]) } }}>
          {ACTIONS.map((x) => <option key={x.value} value={x.value}>{x.label}</option>)}
        </select>
      </Field>
      <fieldset style={{ gridColumn: '1 / -1' }}>
        <legend>Items it is about</legend>
        {eligible.length === 0 ? <p className="ztax-note">The assessment found no item this action can name.</p> : eligible.map((it) => {
          const k = targetKey(it)
          return (
            <label key={k} style={{ display: 'block' }}>
              <input type="checkbox" checked={chosen.includes(k)}
                onChange={(e) => { setChosen((c) => (e.target.checked ? [...c, k] : c.filter((x) => x !== k))) }} />{' '}
              {words(it.kind)} — {it.label}
            </label>
          )
        })}
      </fieldset>
      <Field label="Rationale" help="Your own words. If AI drafted it, its original text is kept separately as evidence.">
        <textarea required maxLength={2000} value={rationale} onChange={(e) => { setRationale(e.target.value) }} />
      </Field>
      <label style={{ gridColumn: '1 / -1' }}>
        <input type="checkbox" checked={aiAssisted} onChange={(e) => { setAiAssisted(e.target.checked) }} /> AI-assisted draft (authority A1, propose only)
      </label>
      {aiAssisted ? (
        <>
          <Field label="AI use case"><input required maxLength={200} value={ai.useCase} onChange={(e) => { setAiField('useCase')(e.target.value) }} /></Field>
          <Field label="Model profile"><input required maxLength={200} value={ai.modelProfile} onChange={(e) => { setAiField('modelProfile')(e.target.value) }} /></Field>
          <Field label="Provider profile"><input required maxLength={200} value={ai.providerProfile} onChange={(e) => { setAiField('providerProfile')(e.target.value) }} /></Field>
          <Field label="Prompt profile"><input required maxLength={200} value={ai.promptProfile} onChange={(e) => { setAiField('promptProfile')(e.target.value) }} /></Field>
          <Field label="AI release train"><input required maxLength={200} value={ai.aiTrainVersion} onChange={(e) => { setAiField('aiTrainVersion')(e.target.value) }} /></Field>
          <Field label="Invocation id" help="Optional, the Gateway evidence record id."><input maxLength={200} value={ai.invocationId} onChange={(e) => { setAiField('invocationId')(e.target.value) }} /></Field>
          <Field label="The model's original text"><textarea required maxLength={4000} value={ai.draft} onChange={(e) => { setAiField('draft')(e.target.value) }} /></Field>
        </>
      ) : null}
      <div className="ztax-form-actions"><Submit busy={draft.busy}>Draft</Submit></div>
      <div style={{ gridColumn: '1 / -1' }}><Refusal error={draft.error} /></div>
    </form>
  )
}

// ---------------------------------------------------------------------------
// recommendations
// ---------------------------------------------------------------------------

const Recommendations = ({ roles, me, selected, setSelected }: {
  roles: readonly string[]
  me: string
  selected: string | null
  setSelected: (id: string | null) => void
}) => {
  const [status, setStatus] = useState<RecStatus | ''>('')
  const { load, reload } = useLoad(
    (signal) => ztax.listReviewRecommendations({ ...(status === '' ? {} : { status }), limit: 500 }, signal),
    [status],
  )
  return (
    <>
      <Loaded load={load}>
        {(data) => (
          <Tiles tiles={[
            { label: 'Recommendations', count: data.recommendations.length },
            { label: 'Awaiting independent review', count: data.recommendations.filter((r) => r.status === 'RECOMMENDED').length, tone: 'warn' },
            { label: 'Stale', count: data.recommendations.filter((r) => r.stale).length, tone: 'bad' },
            { label: 'Accepted', count: data.recommendations.filter((r) => r.status === 'ACCEPTED').length, tone: 'ok' },
          ]} />
        )}
      </Loaded>
      <Card
        title="Review recommendations"
        aside={
          <div className="ztax-filter-bar">
            <Field label="Status">
              <select value={status} onChange={(e) => { setStatus(isRecStatus(e.target.value) ? e.target.value : '') }}>
                <option value="">All</option>
                {REC_STATUSES.map((s) => <option key={s} value={s}>{words(s)}</option>)}
              </select>
            </Field>
            <button type="button" className="ztax-btn-quiet" onClick={reload}>Refresh</button>
          </div>
        }
      >
        <Loaded load={load}>
          {(data) => data.recommendations.length === 0 ? (
            <Empty>No recommendation matches. An ANALYST drafts one from a current impact assessment.</Empty>
          ) : (
            <div style={{ overflowX: 'auto' }}>
              <table className="ztax-action-table">
                <thead><tr><th>Action</th><th>Change</th><th>Origin</th><th>Status</th><th>Hand-off</th><th>Drafted</th><th>Open</th></tr></thead>
                <tbody>
                  {data.recommendations.map((r) => (
                    <tr key={r.id} className={r.id === selected ? 'ztax-row-selected' : undefined}>
                      <td>{words(r.action)}</td>
                      <td><Id value={r.changeId} /></td>
                      <td>{r.origin === 'AI_ASSISTED' ? 'AI-assisted' : 'Human'}</td>
                      <td><Status value={r.status} />{r.stale ? <span className="muted"> · stale</span> : null}</td>
                      <td className="mono">{r.handoffRef ?? '—'}</td>
                      <td className="mono">{r.createdAt}</td>
                      <td>
                        <button type="button" className="ztax-table-action-btn" aria-pressed={r.id === selected} onClick={() => { setSelected(r.id) }}>Open</button>
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
        <RecommendationDetail key={selected} id={selected} roles={roles} me={me} onChanged={reload} onClose={() => { setSelected(null) }} />
      )}
    </>
  )
}

const RecommendationDetail = ({ id, roles, me, onChanged, onClose }: {
  id: string
  roles: readonly string[]
  me: string
  onChanged: () => void
  onClose: () => void
}) => {
  const { load, reload } = useLoad((signal) => ztax.getReviewRecommendation(id, signal), [id])
  const [act, setAct] = useState<string | null>(null)
  const [reason, setReason] = useState('')
  const [revised, setRevised] = useState('')
  const step = useAction<Recommendation>()

  return (
    <Card title="Review recommendation" aside={<button type="button" className="ztax-btn-quiet" onClick={onClose}>Close</button>}>
      <Loaded load={load}>
        {(r) => {
          const offers = recommendationOffers(r, roles, me)
          const submit = (e: FormEvent) => {
            e.preventDefault()
            const v = r.version
            const call = act === 'RECOMMEND' ? () => ztax.recommendReviewRecommendation(r.id, { reason: reason.trim(), expectedVersion: v })
              : act === 'WITHDRAW' ? () => ztax.withdrawReviewRecommendation(r.id, { reason: reason.trim(), expectedVersion: v })
              : act === 'REVISE' ? () => ztax.reviseReviewRecommendation(r.id, { action: r.action, rationale: revised.trim(), targets: r.targets, expectedVersion: v })
              : act === 'ACCEPT' || act === 'REJECT' || act === 'REQUEST_CHANGES'
                ? () => ztax.reviewReviewRecommendation(r.id, { decision: act, reason: reason.trim(), expectedVersion: v })
                : null
            if (call === null) return
            void step.run(call).then((x) => { if (x !== null) { setAct(null); setReason(''); reload(); onChanged() } })
          }
          return (
            <>
              <p><strong>{words(r.action)}</strong> <Status value={r.status} />{r.stale ? <span className="muted"> · the change was reassessed since</span> : null}</p>
              <p>{r.rationale}</p>
              <dl className="ztax-dl">
                <dt>Recommendation id</dt><dd className="mono">{r.id}</dd>
                <dt>About</dt><dd>{r.targets.length === 0 ? 'No specific item' : r.targets.map((t) => `${words(t.kind)} ${t.ref}`).join('; ')}</dd>
                <dt>Bound to assessment</dt><dd><Id value={r.assessmentId} /> <span className="mono muted">{r.assessmentDigest}</span></dd>
                <dt>Origin</dt><dd>{r.origin === 'AI_ASSISTED' ? 'AI-assisted (advisory, A1 propose)' : 'Human'}</dd>
                {r.ai === undefined ? null : (
                  <>
                    <dt>AI provenance</dt>
                    <dd>{r.ai.useCase} · model {r.ai.modelProfile} · provider {r.ai.providerProfile} · prompt {r.ai.promptProfile} · train {r.ai.aiTrainVersion}{r.ai.invocationId === undefined ? '' : ` · invocation ${r.ai.invocationId}`}</dd>
                    <dt>Model's original text</dt><dd>{r.ai.draft}</dd>
                  </>
                )}
                {r.recommendedBy === undefined ? null : <><dt>Recommended by</dt><dd><Id value={r.recommendedBy} /></dd></>}
                {r.reviewedBy === undefined ? null : <><dt>Independently reviewed by</dt><dd><Id value={r.reviewedBy} /></dd></>}
                {r.handoffRef === undefined ? null : <><dt>Hand-off reference</dt><dd className="mono">{r.handoffRef}</dd></>}
              </dl>
              {r.status === 'ACCEPTED' ? <p className="ztax-note">{ADVISORY}</p> : null}
              {offers.length === 0 ? null : (
                <div className="ztax-filter-bar">
                  {offers.map((o) => (
                    <button key={o.act} type="button" className="ztax-btn-quiet" disabled={o.blocked !== null} title={o.blocked ?? undefined}
                      aria-pressed={act === o.act} onClick={() => { setAct(o.act); setRevised(r.rationale) }}>
                      {o.label}
                    </button>
                  ))}
                </div>
              )}
              {offers.filter((o) => o.blocked !== null).map((o) => <p key={o.act} className="ztax-note">{o.label}: {o.blocked}</p>)}
              {act === null ? null : (
                <form className="ztax-form" onSubmit={submit}>
                  {act === 'REVISE' ? (
                    <Field label="Revised rationale" help="The earlier text stays in the history. As a reviser you cannot review it.">
                      <textarea required maxLength={2000} value={revised} onChange={(e) => { setRevised(e.target.value) }} />
                    </Field>
                  ) : (
                    <Field label="Reason">
                      <textarea required maxLength={2000} value={reason} onChange={(e) => { setReason(e.target.value) }} />
                    </Field>
                  )}
                  <div className="ztax-form-actions">
                    <Submit busy={step.busy}>{offers.find((o) => o.act === act)?.label ?? 'Record'}</Submit>
                    <button type="button" className="ztax-btn-quiet" onClick={() => { setAct(null) }}>Cancel</button>
                  </div>
                  <div style={{ gridColumn: '1 / -1' }}><Refusal error={step.error} /></div>
                </form>
              )}
              <History rows={r.history.map((e) => ({
                key: e.seq, what: words(e.kind), status: e.status,
                detail: [e.action === undefined ? '' : words(e.action), e.rationale ?? '', e.handoffRef ?? ''].filter((x) => x !== '').join(' · '),
                reason: e.reason, at: e.recordedAt, by: e.recordedBy,
              }))} />
            </>
          )
        }}
      </Loaded>
    </Card>
  )
}
