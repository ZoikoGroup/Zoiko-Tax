import { useState, type FormEvent } from 'react'

import { periodLabel } from '../dashboard/format'
import { ztax } from '../platform/client'
import { Card, Empty, ErrorNote, errorText, Field, Id, Loaded, PageHeader, Status, Submit, useAction, useLoad } from '../ui/kit'
import { CURRENCY, isDecimal, TabBar } from './determine/shared'
import { useRoutedTab } from './routedTab'
import type { PageProps } from './types'
import { WorkpaperDetail } from './workpapers/WorkpaperDetail'
import {
  DUTIES,
  isDuty,
  isStatus,
  reasonOf,
  refusalHint,
  STATUSES,
  type Duty,
  type Workpaper,
  type WorkpaperStatus,
} from './workpapers/model'
import { hasRole, Tiles, words } from './workspace'

// Regulatory Workpapers (OBL-07 Workpaper Assembly & Tax/Regulatory Base
// Ledger; LED-19 tie-out; OPS-15 exception triage). A workpaper is built by
// the cell from the provider contribution ledger and the subledger; this page
// shows the schedules, the reconciliation and the exceptions, and offers only
// the acts the workpaper's state admits. The cell enforces every rule.

type Tab = 'register' | 'open'

const TABS: readonly { value: Tab; label: string }[] = [
  { value: 'register', label: 'Workpapers' },
  { value: 'open', label: 'Open a workpaper' },
]

export const WorkpapersPage = ({ session, period, route, onTabChange }: PageProps) => {
  const [tab, setTab] = useRoutedTab(TABS.map((t) => t.value), 'register', route, onTabChange)
  const [selected, setSelected] = useState<string | null>(null)
  const roles = session.user.roles
  const open = (id: string) => {
    setSelected(id)
    setTab('register')
  }

  return (
    <div className="ztax-page">
      <PageHeader
        title="Regulatory Workpapers"
        subtitle="Deterministic workpapers built from the provider contribution ledger and the subledger: schedules, reconciliation, evidence-linked adjustments and a two-person approval."
      />
      <TabBar label="Regulatory workpapers" tabs={TABS} current={tab} onSelect={setTab} />
      {tab === 'register'
        ? <Register period={period} roles={roles} selected={selected} setSelected={setSelected} />
        : <OpenForm period={period} canOpen={hasRole(roles, 'OPERATOR', 'ANALYST')} onOpen={open} />}
    </div>
  )
}

// ---------------------------------------------------------------------------
// register
// ---------------------------------------------------------------------------

const Register = ({ period, roles, selected, setSelected }: {
  period: string
  roles: readonly string[]
  selected: string | null
  setSelected: (id: string | null) => void
}) => {
  const [month, setMonth] = useState(period)
  const [duty, setDuty] = useState<Duty | ''>('')
  const [status, setStatus] = useState<WorkpaperStatus | ''>('')
  const { load, reload } = useLoad(
    (signal) => ztax.listWorkpapers({
      ...(month === '' ? {} : { period: month }),
      ...(duty === '' ? {} : { duty }),
      ...(status === '' ? {} : { status }),
      limit: 500,
    }, signal),
    [month, duty, status],
  )

  return (
    <>
      <Loaded load={load}>
        {(data) => (
          <Tiles tiles={[
            { label: 'Workpapers', count: data.workpapers.length },
            { label: 'Data incomplete', count: data.workpapers.filter((w) => w.status === 'DATA_INCOMPLETE').length, tone: 'bad' },
            { label: 'Ready for review', count: data.workpapers.filter((w) => w.status === 'READY_FOR_REVIEW').length, tone: 'warn' },
            { label: 'Approved', count: data.workpapers.filter((w) => w.status === 'APPROVED').length, tone: 'ok' },
          ]} />
        )}
      </Loaded>
      <Card
        title="Workpapers"
        aside={
          <div className="ztax-filter-bar">
            <Field label="Period">
              <input type="month" value={month} onChange={(e) => { setMonth(e.target.value) }} />
            </Field>
            <Field label="Duty">
              <select value={duty} onChange={(e) => { setDuty(isDuty(e.target.value) ? e.target.value : '') }}>
                <option value="">All</option>
                {DUTIES.map((d) => <option key={d.value} value={d.value}>{d.label}</option>)}
              </select>
            </Field>
            <Field label="Status">
              <select value={status} onChange={(e) => { setStatus(isStatus(e.target.value) ? e.target.value : '') }}>
                <option value="">All</option>
                {STATUSES.map((s) => <option key={s} value={s}>{words(s)}</option>)}
              </select>
            </Field>
            <button type="button" className="ztax-btn-quiet" onClick={reload}>Refresh</button>
          </div>
        }
      >
        <Loaded load={load}>
          {(data) => data.workpapers.length === 0 ? (
            <Empty>No workpaper matches. An OPERATOR or ANALYST opens one from the Open tab.</Empty>
          ) : (
            <div style={{ overflowX: 'auto' }}>
              <table className="ztax-action-table">
                <thead>
                  <tr>
                    <th>Period</th><th>Duty</th><th>Authority</th><th>Legal filer</th><th>Status</th><th>Builds</th>
                    <th>Content release</th><th>Action</th>
                  </tr>
                </thead>
                <tbody>
                  {data.workpapers.map((w: Workpaper) => (
                    <tr key={w.id} className={w.id === selected ? 'ztax-row-selected' : undefined}>
                      <td>{periodLabel(w.period)}</td>
                      <td>{words(w.duty)}</td>
                      <td>{w.authority}</td>
                      <td><Id value={w.legalEntityId} /></td>
                      <td><Status value={w.status} /></td>
                      <td>{w.builds.length}{w.frozenBuildVersion === undefined ? null : <span className="muted"> · frozen v{w.frozenBuildVersion}</span>}</td>
                      <td className="mono">{w.contentRelease}</td>
                      <td>
                        <button type="button" className="ztax-table-action-btn" aria-pressed={w.id === selected}
                          onClick={() => { setSelected(w.id) }}>
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
        <WorkpaperDetail key={selected} id={selected} roles={roles} onChanged={reload} onClose={() => { setSelected(null) }} />
      )}
    </>
  )
}

// ---------------------------------------------------------------------------
// open
// ---------------------------------------------------------------------------

const OpenForm = ({ period, canOpen, onOpen }: { period: string; canOpen: boolean; onOpen: (id: string) => void }) => {
  const [form, setForm] = useState({
    duty: 'US_USF' as Duty, period, authority: 'USAC', reportingCurrency: 'USD', materialityThreshold: '0.00',
    contentRelease: '', legalEntityId: '',
  })
  const [invalid, setInvalid] = useState<string | null>(null)
  const action = useAction<Workpaper>()
  const set = (k: keyof typeof form) => (v: string) => { setForm((f) => ({ ...f, [k]: v })) }

  if (!canOpen) {
    return <Card><p className="ztax-note">Opening a workpaper needs the OPERATOR or ANALYST role.</p></Card>
  }

  const submit = (e: FormEvent) => {
    e.preventDefault()
    setInvalid(null)
    if (!CURRENCY.test(form.reportingCurrency) || !isDecimal(form.materialityThreshold) || form.materialityThreshold.startsWith('-')) {
      setInvalid('The reporting currency is an ISO code such as USD, and the materiality threshold a decimal string of zero or more.')
      return
    }
    // A retry is safe without a key: the filer, duty and period are the
    // workpaper's identity, so the same header returns the one already open.
    void action.run(() => ztax.createWorkpaper({
      duty: form.duty, period: form.period, authority: form.authority.trim(), reportingCurrency: form.reportingCurrency,
      materialityThreshold: form.materialityThreshold, contentRelease: form.contentRelease.trim(),
      ...(form.legalEntityId.trim() === '' ? {} : { legalEntityId: form.legalEntityId.trim() }),
    })).then((w) => {
      if (w !== null) onOpen(w.id)
    })
  }

  const hint = refusalHint(reasonOf(action.error))
  return (
    <Card title="Open a workpaper">
      <form className="ztax-form" onSubmit={submit}>
        <Field label="Duty">
          <select value={form.duty} onChange={(e) => { if (isDuty(e.target.value)) set('duty')(e.target.value) }}>
            {DUTIES.map((d) => <option key={d.value} value={d.value}>{d.label}</option>)}
          </select>
        </Field>
        <Field label="Period">
          <input type="month" required value={form.period} onChange={(e) => { set('period')(e.target.value) }} />
        </Field>
        <Field label="Authority">
          <input required maxLength={200} value={form.authority} onChange={(e) => { set('authority')(e.target.value) }} />
        </Field>
        <Field label="Reporting currency">
          <input required value={form.reportingCurrency} onChange={(e) => { set('reportingCurrency')(e.target.value.toUpperCase()) }} />
        </Field>
        <Field label="Materiality threshold" help="An adjustment at or above it needs a second person. Zero makes every adjustment material.">
          <input required value={form.materialityThreshold} onChange={(e) => { set('materialityThreshold')(e.target.value) }} />
        </Field>
        <Field label="Content release" help="The rule or form release the workpaper applies, e.g. usf-499q@2026.3.">
          <input required maxLength={200} value={form.contentRelease} onChange={(e) => { set('contentRelease')(e.target.value) }} />
        </Field>
        <Field label="Legal filer" help="Optional. The tenant's default legal entity when empty.">
          <input value={form.legalEntityId} onChange={(e) => { set('legalEntityId')(e.target.value) }} />
        </Field>
        <div className="ztax-form-actions">
          <Submit busy={action.busy}>Open</Submit>
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
