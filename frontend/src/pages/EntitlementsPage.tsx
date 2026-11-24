import { useState, type FormEvent } from 'react'

import { ztax } from '../platform/client'
import { Card, Empty, ErrorNote, errorText, Field, Id, Loaded, newKey, PageHeader, Status, Submit, useAction, useLoad } from '../ui/kit'
import { TabBar, todayUtc } from './determine/shared'
import {
  ACTION_LABEL,
  actionBody,
  countEffective,
  EFFECTIVE_TONE,
  EFFECTIVES,
  grantRequest,
  MODES,
  OPERATION_LABEL,
  offersFor,
  operationsFor,
  REASON_LABEL,
  reasonOf,
  refusalHint,
  type Action,
  type Effective,
  type Entitlement,
  type EntitlementList,
  type GrantForm,
} from './entitlements/model'
import { SERVICES, serviceLabel } from './locations/coverage'
import { useRoutedTab } from './routedTab'
import type { PageProps } from './types'
import { Tiles, words } from './workspace'

// Entitlements (Engineering Doc 18 W06, W07, W15; G-004). A grant is a
// contract right; the cell reads it beside the coverage it can prove, so a
// customer sees the exact entitlement, mode, dates and every reason it is
// pending or blocked. An approved grant over unproven coverage is
// RESTRICTED, never live. Only an ADMIN records and acts, and approval is a
// second administrator's; the cell enforces every rule.

type Tab = 'register' | 'record'

const TABS: readonly { value: Tab; label: string }[] = [
  { value: 'register', label: 'Entitlements & coverage' },
  { value: 'record', label: 'Record a grant' },
]

export const EntitlementsPage = ({ session, route, onTabChange }: PageProps) => {
  const [tab, setTab] = useRoutedTab(TABS.map((t) => t.value), 'register', route, onTabChange)
  const [selected, setSelected] = useState<string | null>(null)
  const roles = session.user.roles
  const isAdmin = roles.includes('ADMIN')

  return (
    <div className="ztax-page">
      <PageHeader
        title="Entitlements"
        subtitle="Contract-to-capability grants per legal entity, jurisdiction, service, mode and operation — each read against the coverage this cell can prove. An entitlement alone never makes a market live."
      />
      <TabBar label="Entitlements" tabs={TABS} current={tab} onSelect={setTab} />
      {tab === 'register'
        ? <Register roles={roles} me={session.user.id} selected={selected} setSelected={setSelected} />
        : <RecordForm canRecord={isAdmin} onRecorded={(id) => { setSelected(id); setTab('register') }} />}
    </div>
  )
}

const Register = ({ roles, me, selected, setSelected }: {
  roles: readonly string[]
  me: string
  selected: string | null
  setSelected: (id: string | null) => void
}) => {
  const [effective, setEffective] = useState<Effective | ''>('')
  const [mismatch, setMismatch] = useState(false)
  const { load, reload } = useLoad(
    (signal) => ztax.listEntitlements({
      ...(effective === '' ? {} : { effective }),
      ...(mismatch ? { mismatch: 'true' as const } : {}),
      limit: 500,
    }, signal),
    [effective, mismatch],
  )

  return (
    <>
      <Loaded load={load}>
        {(data) => {
          const n = countEffective(data.entitlements)
          return (
            <Tiles tiles={[
              { label: 'Active (coverage verified)', count: n.ACTIVE, tone: 'ok' },
              { label: 'Restricted (coverage unproven)', count: n.RESTRICTED, tone: 'bad' },
              { label: 'Pending', count: n.PENDING, tone: 'warn' },
              { label: 'Suspended', count: n.SUSPENDED, tone: 'bad' },
              { label: 'Coverage mismatches', count: data.entitlements.filter((e) => e.mismatch).length, tone: 'bad',
                note: 'Approved grants the cell cannot back' },
            ]} />
          )
        }}
      </Loaded>
      <Card
        title="Grants"
        aside={
          <div className="ztax-filter-bar">
            <Field label="Effective state">
              <select value={effective} onChange={(e) => { setEffective((EFFECTIVES as readonly string[]).includes(e.target.value) ? e.target.value as Effective : '') }}>
                <option value="">All</option>
                {EFFECTIVES.map((s) => <option key={s} value={s}>{words(s)}</option>)}
              </select>
            </Field>
            <label className="ztax-check">
              <input type="checkbox" checked={mismatch} onChange={(e) => { setMismatch(e.target.checked) }} /> Mismatches only
            </label>
            <button type="button" className="ztax-btn-quiet" onClick={reload}>Refresh</button>
          </div>
        }
      >
        <Loaded load={load}>
          {(data) => data.entitlements.length === 0 ? (
            <Empty>No grant matches. An ADMIN records one from the Record tab; a second administrator approves it.</Empty>
          ) : (
            <div style={{ overflowX: 'auto' }}>
              <table className="ztax-action-table">
                <thead>
                  <tr>
                    <th>Jurisdiction</th><th>Service</th><th>Mode</th><th>Operation</th><th>Dates</th><th>Contract</th>
                    <th>Status</th><th>Effective</th><th>Coverage</th><th>Why pending or blocked</th><th>Action</th>
                  </tr>
                </thead>
                <tbody>
                  {data.entitlements.map((e) => (
                    <tr key={e.id} className={e.id === selected ? 'ztax-row-selected' : undefined}>
                      <td className="mono">{e.jurisdiction}</td>
                      <td>{serviceLabel(e.service)}</td>
                      <td>{words(e.mode)}</td>
                      <td>{OPERATION_LABEL[e.operation]}</td>
                      <td className="mono">{e.effectiveFrom} → {e.effectiveTo ?? 'open'}</td>
                      <td>{e.contractRef}{e.contractRevision === undefined ? null : <span className="muted"> · {e.contractRevision}</span>}</td>
                      <td><Status value={e.status} /></td>
                      <td><EffectiveTag value={e.effective} />{e.mismatch ? <span className="ztax-status ztax-status--bad" style={{ marginLeft: 4 }}>MISMATCH</span> : null}</td>
                      <td>{words(e.coverage.state)} <span className="muted">· {words(e.coverage.marketStatus)}</span></td>
                      <td>{e.findings.length === 0 ? <span className="muted">—</span> : (
                        <ul className="ztax-plain-list">{e.findings.map((f) => <li key={f.reason}>{REASON_LABEL[f.reason]}</li>)}</ul>
                      )}</td>
                      <td>
                        <button type="button" className="ztax-table-action-btn" aria-pressed={e.id === selected} onClick={() => { setSelected(e.id) }}>
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
        <Detail key={selected} id={selected} roles={roles} me={me} onChanged={reload} onClose={() => { setSelected(null) }} />
      )}
      <Card title="Verified market coverage">
        <Loaded load={load}>{(data) => <Markets data={data} />}</Loaded>
      </Card>
    </>
  )
}

const EffectiveTag = ({ value }: { value: Effective }) => (
  <span className={`ztax-status ztax-status--${EFFECTIVE_TONE[value]}`}>{value}</span>
)

const Markets = ({ data }: { data: EntitlementList }) => (
  <>
    <p className="ztax-note">
      The coverage each grant is read against: content in force, the cell’s authority
      ({data.authoritative ? 'authoritative' : 'advisory only'}) and the filing gate
      {data.catalogue === undefined ? '' : `, with research catalogue ${data.catalogue}`}. A research profile is never a live market.
    </p>
    {data.markets.length === 0 ? <Empty>This cell knows of no market.</Empty> : (
      <div style={{ overflowX: 'auto' }}>
        <table className="ztax-action-table">
          <thead><tr><th>Country</th><th>Status</th><th>What keeps it from the next state</th><th>Services with content in force</th><th>Grants</th></tr></thead>
          <tbody>
            {data.markets.map((m) => {
              const grants = data.entitlements.filter((e) => e.country === m.country)
              return (
                <tr key={m.country}>
                  <td><span className="mono">{m.country}</span>{m.name === undefined ? null : <span className="muted"> {m.name}</span>}</td>
                  <td><Status value={m.status} /></td>
                  <td>{m.reason === undefined ? '—' : words(m.reason)}</td>
                  <td>{m.services.length === 0 ? '—' : m.services.map(serviceLabel).join(', ')}</td>
                  <td>{grants.length === 0 ? <span className="muted">none</span> : `${grants.length} (${grants.filter((g) => g.effective === 'ACTIVE').length} active)`}</td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>
    )}
  </>
)

const Detail = ({ id, roles, me, onChanged, onClose }: {
  id: string
  roles: readonly string[]
  me: string
  onChanged: () => void
  onClose: () => void
}) => {
  const { load, reload } = useLoad((signal) => ztax.getEntitlement(id, signal), [id])
  const [reason, setReason] = useState('')
  const [invalid, setInvalid] = useState<string | null>(null)
  const action = useAction<Entitlement>()

  const act = (e: Entitlement, a: Action) => {
    setInvalid(null)
    const built = actionBody(e, a, reason)
    if ('invalid' in built) {
      setInvalid(built.invalid)
      return
    }
    void action.run(() => ztax.actOnEntitlement(e.id, built.body)).then((done) => {
      if (done !== null) {
        setReason('')
        reload()
        onChanged()
      }
    })
  }
  const hint = refusalHint(reasonOf(action.error))

  return (
    <Card title="Grant" aside={<button type="button" className="ztax-btn-quiet" onClick={onClose}>Close</button>}>
      <Loaded load={load}>
        {(e) => (
          <>
            <dl className="ztax-kv">
              <dt>Grant</dt><dd><Id value={e.id} /></dd>
              <dt>Legal entity</dt><dd><Id value={e.legalEntityId} /></dd>
              <dt>Scope</dt><dd>{e.jurisdiction} · {serviceLabel(e.service)} · {words(e.mode)} · {OPERATION_LABEL[e.operation]}</dd>
              <dt>Contract</dt><dd>{e.contractRef}{e.contractRevision === undefined ? '' : ` · ${e.contractRevision}`}</dd>
              <dt>Dates</dt><dd>{e.effectiveFrom} → {e.effectiveTo ?? 'open-ended'}{e.activeFrom === undefined ? '' : ` · runs from ${e.activeFrom}`}</dd>
              <dt>State</dt><dd><Status value={e.status} /> <EffectiveTag value={e.effective} /> decision {e.decision}</dd>
              <dt>Coverage</dt>
              <dd>
                {words(e.coverage.state)} · market {words(e.coverage.marketStatus)}
                {e.coverage.marketReason === undefined ? '' : ` (${words(e.coverage.marketReason)})`}
                {e.coverage.servicesInForce.length === 0 ? '' : ` · content in force for ${e.coverage.servicesInForce.map(serviceLabel).join(', ')}`}
                {e.coverage.filing === undefined ? '' : ` · filing gate ${e.coverage.filing.ready ? 'ready' : 'not ready'}${e.coverage.filing.authority === undefined ? '' : ` (${e.coverage.filing.authority}${e.coverage.filing.reason === undefined ? '' : `: ${e.coverage.filing.reason}`})`}`}
              </dd>
            </dl>
            {e.findings.length === 0 ? <p className="ztax-note">Allowed: an active grant over coverage the cell proves.</p> : (
              <>
                <h3 className="ztax-card-subtitle">Why it is pending or blocked</h3>
                <ul>
                  {e.findings.map((f) => (
                    <li key={f.reason}><strong>{REASON_LABEL[f.reason]}</strong> <span className="mono muted">{f.reason}</span><br />{f.detail}</li>
                  ))}
                </ul>
              </>
            )}
            <h3 className="ztax-card-subtitle">History</h3>
            <ol>
              {e.history.map((h) => (
                <li key={h.seq}>
                  <span className="mono">{h.recordedAt}</span> {words(h.kind)} → {words(h.lifecycle)} by <Id value={h.recordedBy} />
                  {h.reason === undefined ? null : <span className="muted"> — {h.reason}</span>}
                </li>
              ))}
            </ol>
            {e.actions.length === 0 ? <p className="ztax-note">Nothing more happens to this grant; it stays readable as recorded.</p> : (
              <div className="ztax-form">
                <Field label="Reason" help="Every act but submission gives its reason; it is kept in the grant’s history.">
                  <textarea rows={2} maxLength={2000} value={reason} onChange={(ev) => { setReason(ev.target.value) }} />
                </Field>
                <div className="ztax-form-actions">
                  {offersFor(e, roles, me).map((o) => (
                    <button key={o.action} type="button" className="ztax-btn-quiet" disabled={o.blocked !== null || action.busy}
                      title={o.blocked ?? undefined} onClick={() => { act(e, o.action) }}>
                      {ACTION_LABEL[o.action]}
                    </button>
                  ))}
                </div>
                {offersFor(e, roles, me).filter((o) => o.blocked !== null).slice(0, 1).map((o) => (
                  <p key="why" className="ztax-note">{o.blocked}</p>
                ))}
                {invalid === null ? null : <p className="ztax-error-note" role="alert">{invalid}</p>}
                {action.error === null ? null : hint === null ? <ErrorNote error={action.error} /> : (
                  <p className="ztax-error-note" role="alert">{hint} <span className="mono muted">{errorText(action.error)}</span></p>
                )}
              </div>
            )}
          </>
        )}
      </Loaded>
    </Card>
  )
}

const RecordForm = ({ canRecord, onRecorded }: { canRecord: boolean; onRecorded: (id: string) => void }) => {
  const [form, setForm] = useState<GrantForm>({
    legalEntityId: '', jurisdiction: '', service: 'MOBILE_VOICE', mode: 'NATIVE', operation: 'QUOTE',
    effectiveFrom: todayUtc(), effectiveTo: '', contractRef: '', contractRevision: '', submit: true,
  })
  const [key, setKey] = useState(newKey)
  const [invalid, setInvalid] = useState<string | null>(null)
  const action = useAction<Entitlement>()
  const set = <K extends keyof GrantForm>(k: K) => (v: GrantForm[K]) => { setForm((f) => ({ ...f, [k]: v })) }

  if (!canRecord) {
    return <Card><p className="ztax-note">Recording a grant needs the ADMIN role. Every role can read the register.</p></Card>
  }

  const submit = (e: FormEvent) => {
    e.preventDefault()
    setInvalid(null)
    const built = grantRequest(form, todayUtc())
    if ('invalid' in built) {
      setInvalid(built.invalid)
      return
    }
    // The key is kept across a retry of the same form, so a retried request
    // returns the grant already recorded rather than a second one.
    void action.run(() => ztax.createEntitlement(key, built.body)).then((g) => {
      if (g !== null) {
        setKey(newKey())
        onRecorded(g.id)
      }
    })
  }
  const hint = refusalHint(reasonOf(action.error))
  const ops = operationsFor(form.mode)

  return (
    <Card title="Record a grant">
      <form className="ztax-form" onSubmit={submit}>
        <Field label="Jurisdiction" help="A country, or a country and one subdivision: GB, US-NY.">
          <input required value={form.jurisdiction} onChange={(e) => { set('jurisdiction')(e.target.value.toUpperCase()) }} />
        </Field>
        <Field label="Service family">
          <select value={form.service} onChange={(e) => { set('service')(e.target.value) }}>
            {SERVICES.map((s) => <option key={s.value} value={s.value}>{s.label}</option>)}
          </select>
        </Field>
        <Field label="Mode" help="Each mode is a separate right; none implies another.">
          <select value={form.mode} onChange={(e) => {
            const m = MODES.find((x) => x.value === e.target.value)
            if (m !== undefined) setForm((f) => ({ ...f, mode: m.value, operation: m.operations.includes(f.operation) ? f.operation : (m.operations[0] ?? f.operation) }))
          }}>
            {MODES.map((m) => <option key={m.value} value={m.value}>{m.label}</option>)}
          </select>
        </Field>
        <Field label="Operation">
          <select value={form.operation} onChange={(e) => { const o = ops.find((x) => x === e.target.value); if (o !== undefined) set('operation')(o) }}>
            {ops.map((o) => <option key={o} value={o}>{OPERATION_LABEL[o]}</option>)}
          </select>
        </Field>
        <Field label="Effective from" help="Today or later: a grant is never backdated.">
          <input type="date" required value={form.effectiveFrom} onChange={(e) => { set('effectiveFrom')(e.target.value) }} />
        </Field>
        <Field label="Effective to" help="Optional; the grant is in force on this date.">
          <input type="date" value={form.effectiveTo} onChange={(e) => { set('effectiveTo')(e.target.value) }} />
        </Field>
        <Field label="Contract reference">
          <input required maxLength={128} value={form.contractRef} onChange={(e) => { set('contractRef')(e.target.value) }} />
        </Field>
        <Field label="Contract revision" help="Optional: the schedule or amendment.">
          <input maxLength={128} value={form.contractRevision} onChange={(e) => { set('contractRevision')(e.target.value) }} />
        </Field>
        <Field label="Legal entity" help="Optional. The tenant's default legal entity when empty.">
          <input value={form.legalEntityId} onChange={(e) => { set('legalEntityId')(e.target.value) }} />
        </Field>
        <label className="ztax-check">
          <input type="checkbox" checked={form.submit} onChange={(e) => { set('submit')(e.target.checked) }} /> Submit for review now
        </label>
        <div className="ztax-form-actions">
          <Submit busy={action.busy}>Record</Submit>
        </div>
        {invalid === null ? null : <p className="ztax-error-note" role="alert" style={{ gridColumn: '1 / -1' }}>{invalid}</p>}
        {action.error === null ? null : (
          <div style={{ gridColumn: '1 / -1' }}>
            {hint === null ? <ErrorNote error={action.error} /> : (
              <p className="ztax-error-note" role="alert">{hint} <span className="mono muted">{errorText(action.error)}</span></p>
            )}
          </div>
        )}
      </form>
    </Card>
  )
}
