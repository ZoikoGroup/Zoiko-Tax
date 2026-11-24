import { useState, type FormEvent } from 'react'

import { ztax } from '../platform/client'
import { Card, Empty, ErrorNote, errorText, Field, Id, Loaded, newKey, PageHeader, Status, Submit, useAction, useLoad } from '../ui/kit'
import { TabBar, todayUtc } from './determine/shared'
import { useRoutedTab } from './routedTab'
import {
  basisDigest,
  meterLabel,
  METERS,
  planOffers,
  planRequest,
  reasonOf,
  STATE_LABEL,
  STATE_TONE,
  usedPercent,
  type Meter,
  type Plan,
  type PlanForm,
  type UsageBasis,
  type UsageLine,
} from './subscriptions/model'
import type { PageProps } from './types'
import { words } from './workspace'

// Subscriptions & Usage (Engineering Doc 18 W04, W08, W09; G-049). A plan
// records a term, a contract and an included monthly quota per metered
// capability — no price — and is in force once a second administrator
// approves it. Usage is counted by the cell from its own records, exact
// integers with a digest over the rows counted, so every number here can be
// checked row by row.

type Tab = 'usage' | 'plans' | 'propose'

const TABS: readonly { value: Tab; label: string }[] = [
  { value: 'usage', label: 'Usage' },
  { value: 'plans', label: 'Plans' },
  { value: 'propose', label: 'Propose a plan' },
]

export const SubscriptionsPage = ({ session, period, route, onTabChange }: PageProps) => {
  const [tab, setTab] = useRoutedTab(TABS.map((t) => t.value), 'usage', route, onTabChange)
  const roles = session.user.roles

  return (
    <div className="ztax-page">
      <PageHeader
        title="Subscriptions & Usage"
        subtitle="The plan in force and its included quotas, and usage counted exactly from this cell’s own records — every count reproducible from the rows it counted."
      />
      <TabBar label="Subscriptions and usage" tabs={TABS} current={tab} onSelect={setTab} />
      {tab === 'usage' ? <Usage period={period} /> : null}
      {tab === 'plans' ? <Plans roles={roles} me={session.user.id} /> : null}
      {tab === 'propose' ? <ProposeForm canPropose={roles.includes('ADMIN')} onProposed={() => { setTab('plans') }} /> : null}
    </div>
  )
}

const currentMonth = () => new Date().toISOString().slice(0, 7)

const Usage = ({ period }: { period: string }) => {
  const [month, setMonth] = useState(period > currentMonth() ? currentMonth() : period)
  const [basisOf, setBasisOf] = useState<Meter | null>(null)
  const { load, reload } = useLoad((signal) => ztax.getUsage({ period: month }, signal), [month])

  return (
    <>
      <Card
        title="Metered usage"
        aside={
          <div className="ztax-filter-bar">
            <Field label="Month (UTC)">
              <input type="month" max={currentMonth()} value={month} onChange={(e) => { setMonth(e.target.value); setBasisOf(null) }} />
            </Field>
            <button type="button" className="ztax-btn-quiet" onClick={reload}>Recount</button>
          </div>
        }
      >
        <Loaded load={load}>
          {(r) => (
            <>
              <p className="ztax-note">
                {r.from} → {r.to} · {r.complete ? 'closed month: recounting gives the same numbers and digests' : 'open month: counts grow until it ends'} ·
                {' '}plan {r.plan === undefined ? 'none in force' : `${r.plan.planCode} (${r.plan.planName})`} · meters {r.definitionsVersion}
              </p>
              <div style={{ overflowX: 'auto' }}>
                <table className="ztax-action-table">
                  <thead>
                    <tr><th>Meter</th><th>Count</th><th>Included</th><th>Remaining</th><th>Overage</th><th>Standing</th><th>Basis</th></tr>
                  </thead>
                  <tbody>
                    {r.lines.map((l) => <Line key={l.meter} line={l} open={basisOf === l.meter} onOpen={() => { setBasisOf(basisOf === l.meter ? null : l.meter) }} />)}
                  </tbody>
                </table>
              </div>
              <p className="ztax-note">API request traffic is not metered: this cell records no request log, and Doc 18 counts raw traffic only where a contract says so.</p>
            </>
          )}
        </Loaded>
      </Card>
      {basisOf === null ? null : <Basis key={`${month}/${basisOf}`} period={month} meter={basisOf} />}
    </>
  )
}

const Line = ({ line: l, open, onOpen }: { line: UsageLine; open: boolean; onOpen: () => void }) => {
  const pct = usedPercent(l)
  return (
    <tr>
      <td>{meterLabel(l.meter)}<br /><span className="muted">{l.unit}s · {l.source}</span></td>
      <td className="mono">{l.count}</td>
      <td className="mono">{l.included ?? '—'}{l.overage === undefined ? null : <span className="muted"> · {words(l.overage)}</span>}</td>
      <td className="mono">{l.included === undefined ? '—' : l.remaining}</td>
      <td className="mono">{l.included === undefined ? '—' : l.overageCount}</td>
      <td>
        <span className={`ztax-status ztax-status--${STATE_TONE[l.state]}`}>{STATE_LABEL[l.state]}</span>
        {pct === null ? null : (
          <div aria-label={`${pct}% of the included quantity used`} style={{ background: '#e5e7eb', height: 6, borderRadius: 3, marginTop: 4 }}>
            <div style={{ width: `${pct}%`, height: 6, borderRadius: 3, background: l.state === 'OVER_QUOTA' ? '#b91c1c' : l.state === 'AT_QUOTA' ? '#92400e' : '#065f46' }} />
          </div>
        )}
      </td>
      <td>
        <span className="mono" title={l.basisDigest}>{l.basisDigest.slice(0, 12)}…</span>{' '}
        <button type="button" className="ztax-table-action-btn" aria-pressed={open} onClick={onOpen}>{open ? 'Hide' : 'Rows'}</button>
        <details><summary className="muted">Rule</summary>{l.rule}</details>
      </td>
    </tr>
  )
}

const Basis = ({ period, meter }: { period: string; meter: Meter }) => {
  const { load } = useLoad((signal) => ztax.getUsageBasis({ meter, period, limit: 500 }, signal), [period, meter])
  const [check, setCheck] = useState<string | null>(null)
  const verify = (b: UsageBasis) => {
    void basisDigest(b.rows.map((r) => r.id)).then((d) => {
      setCheck(d === b.basisDigest ? `Verified in this browser: the ${b.rows.length} rows digest to ${d}.` : `MISMATCH: the rows digest to ${d}, not ${b.basisDigest}.`)
    })
  }
  return (
    <Card title={`Basis — ${meterLabel(meter)}, ${period}`}>
      <Loaded load={load}>
        {(b) => (
          <>
            <p className="ztax-note">
              {b.count} rows counted from {b.source}. The digest is SHA-256 over the row ids sorted bytewise and joined by newlines.
              {b.truncated ? ` Showing the first ${b.rows.length}; list the rest from the source by the rule to check the digest.` : ''}
            </p>
            {b.truncated ? null : <button type="button" className="ztax-btn-quiet" onClick={() => { verify(b) }}>Verify digest</button>}
            {check === null ? null : <p className={check.startsWith('MISMATCH') ? 'ztax-error-note' : 'ztax-note'} role="status">{check}</p>}
            {b.rows.length === 0 ? <Empty>Nothing was counted in this month.</Empty> : (
              <div style={{ overflowX: 'auto', maxHeight: 360 }}>
                <table className="ztax-action-table">
                  <thead><tr><th>#</th><th>Row id</th><th>Instant</th></tr></thead>
                  <tbody>{b.rows.map((r, i) => <tr key={r.id}><td>{i + 1}</td><td className="mono">{r.id}</td><td className="mono">{r.at}</td></tr>)}</tbody>
                </table>
              </div>
            )}
          </>
        )}
      </Loaded>
    </Card>
  )
}

const Plans = ({ roles, me }: { roles: readonly string[]; me: string }) => {
  const { load, reload } = useLoad((signal) => ztax.listSubscriptionPlans(signal), [])
  const [reason, setReason] = useState('')
  const action = useAction<Plan>()
  const act = (p: Plan, a: 'APPROVE' | 'REJECT' | 'WITHDRAW') => {
    void action.run(() => ztax.actOnSubscriptionPlan(p.id, {
      action: a, expectedVersion: p.version, ...(reason.trim() === '' ? {} : { reason: reason.trim() }),
    })).then((done) => { if (done !== null) { setReason(''); reload() } })
  }
  const reasonCode = reasonOf(action.error)

  return (
    <>
      <Card title="Plan revisions" aside={<button type="button" className="ztax-btn-quiet" onClick={reload}>Refresh</button>}>
        <Loaded load={load}>
          {(data) => data.plans.length === 0 ? <Empty>No plan is recorded. An ADMIN proposes one; a second administrator approves it.</Empty> : (
            <>
              <Field label="Reason" help="Rejecting or withdrawing gives its reason.">
                <input maxLength={2000} value={reason} onChange={(e) => { setReason(e.target.value) }} />
              </Field>
              <div style={{ overflowX: 'auto' }}>
                <table className="ztax-action-table">
                  <thead><tr><th>Plan</th><th>Term</th><th>Contract</th><th>Included per month</th><th>Status</th><th>History</th><th>Action</th></tr></thead>
                  <tbody>
                    {data.plans.map((p) => (
                      <tr key={p.id}>
                        <td><strong>{p.planCode}</strong> {p.planName}{p.inForce ? <span className="ztax-status ztax-status--ok" style={{ marginLeft: 4 }}>IN FORCE</span> : null}</td>
                        <td className="mono">{p.termStart} → {p.termEnd ?? 'open'}</td>
                        <td>{p.contractRef}</td>
                        <td>{p.quotas.map((q) => <div key={q.meter}>{meterLabel(q.meter)}: <span className="mono">{q.included}</span> <span className="muted">{words(q.overage)}</span></div>)}</td>
                        <td><Status value={p.status} /></td>
                        <td>{p.history.map((h) => <div key={h.seq} className="muted">{words(h.kind)} · <Id value={h.recordedBy} />{h.reason === undefined ? '' : ` — ${h.reason}`}</div>)}</td>
                        <td>
                          {planOffers(p, roles, me).map((o) => (
                            <button key={o.action} type="button" className="ztax-table-action-btn" disabled={o.blocked !== null || action.busy}
                              title={o.blocked ?? undefined} onClick={() => { act(p, o.action) }}>
                              {words(o.action)}
                            </button>
                          ))}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </>
          )}
        </Loaded>
        {action.error === null ? null : reasonCode === 'MAKER_CHECKER'
          ? <p className="ztax-error-note" role="alert">A different administrator must approve this plan. <span className="mono muted">{errorText(action.error)}</span></p>
          : <ErrorNote error={action.error} />}
      </Card>
      <Card title="Meter definitions">
        <Loaded load={load}>
          {(data) => (
            <>
              <p className="ztax-note">Version {data.definitionsVersion}. Each meter counts one kind of row this cell writes; nothing is entered by hand.</p>
              <ul>{data.meters.map((m) => <li key={m.meter}><strong>{meterLabel(m.meter)}</strong> <span className="mono muted">{m.source}</span> — {m.rule}</li>)}</ul>
            </>
          )}
        </Loaded>
      </Card>
    </>
  )
}

const ProposeForm = ({ canPropose, onProposed }: { canPropose: boolean; onProposed: () => void }) => {
  const [form, setForm] = useState<PlanForm>({
    planCode: '', planName: '', contractRef: '', termStart: todayUtc(), termEnd: '',
    quotas: METERS.map((m) => ({ meter: m.value, included: '0', overage: 'ALLOW_OVERAGE' as const, on: m.value === 'COMMITTED_DECISIONS' })),
  })
  const [key, setKey] = useState(newKey)
  const [invalid, setInvalid] = useState<string | null>(null)
  const action = useAction<Plan>()

  if (!canPropose) {
    return <Card><p className="ztax-note">Proposing a plan needs the ADMIN role. Every role can read plans and usage.</p></Card>
  }
  const set = (k: 'planCode' | 'planName' | 'contractRef' | 'termStart' | 'termEnd') => (v: string) => { setForm((f) => ({ ...f, [k]: v })) }
  const setQuota = (i: number, patch: Partial<PlanForm['quotas'][number]>) => {
    setForm((f) => ({ ...f, quotas: f.quotas.map((q, j) => (j === i ? { ...q, ...patch } : q)) }))
  }
  const submit = (e: FormEvent) => {
    e.preventDefault()
    setInvalid(null)
    const built = planRequest(form, todayUtc())
    if ('invalid' in built) {
      setInvalid(built.invalid)
      return
    }
    void action.run(() => ztax.proposeSubscriptionPlan(key, built.body)).then((p) => {
      if (p !== null) {
        setKey(newKey())
        onProposed()
      }
    })
  }

  return (
    <Card title="Propose a plan revision">
      <form className="ztax-form" onSubmit={submit}>
        <Field label="Plan code"><input required value={form.planCode} onChange={(e) => { set('planCode')(e.target.value.toUpperCase()) }} /></Field>
        <Field label="Plan name"><input required maxLength={128} value={form.planName} onChange={(e) => { set('planName')(e.target.value) }} /></Field>
        <Field label="Contract reference"><input required maxLength={128} value={form.contractRef} onChange={(e) => { set('contractRef')(e.target.value) }} /></Field>
        <Field label="Term starts" help="Today or later: a plan never reaches back over usage already metered.">
          <input type="date" required value={form.termStart} onChange={(e) => { set('termStart')(e.target.value) }} />
        </Field>
        <Field label="Term ends" help="Optional."><input type="date" value={form.termEnd} onChange={(e) => { set('termEnd')(e.target.value) }} /></Field>
        <fieldset style={{ gridColumn: '1 / -1' }}>
          <legend>Included per monthly period</legend>
          {form.quotas.map((q, i) => (
            <div key={q.meter} className="ztax-filter-bar">
              <label className="ztax-check"><input type="checkbox" checked={q.on} onChange={(e) => { setQuota(i, { on: e.target.checked }) }} /> {meterLabel(q.meter)}</label>
              <input aria-label={`${meterLabel(q.meter)} included`} inputMode="numeric" disabled={!q.on} value={q.included} onChange={(e) => { setQuota(i, { included: e.target.value }) }} />
              <select aria-label={`${meterLabel(q.meter)} overage`} disabled={!q.on} value={q.overage}
                onChange={(e) => { setQuota(i, { overage: e.target.value === 'HARD_LIMIT' ? 'HARD_LIMIT' : 'ALLOW_OVERAGE' }) }}>
                <option value="ALLOW_OVERAGE">Overage allowed</option>
                <option value="HARD_LIMIT">Hard limit (reported, not enforced here)</option>
              </select>
            </div>
          ))}
        </fieldset>
        <div className="ztax-form-actions"><Submit busy={action.busy}>Propose</Submit></div>
        {invalid === null ? null : <p className="ztax-error-note" role="alert" style={{ gridColumn: '1 / -1' }}>{invalid}</p>}
        {action.error === null ? null : <div style={{ gridColumn: '1 / -1' }}><ErrorNote error={action.error} /></div>}
      </form>
    </Card>
  )
}
