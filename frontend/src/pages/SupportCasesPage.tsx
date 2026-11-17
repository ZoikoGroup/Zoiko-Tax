import { useState } from 'react'

import { instant } from '../dashboard/format'
import { ztax } from '../platform/client'
import { Card, Empty, Field, Id, Loaded, PageHeader, Status, useLoad } from '../ui/kit'
import { TabBar } from './determine/shared'
import { useRoutedTab } from './routedTab'
import { CaseDetail } from './support/CaseDetail'
import { ClockChip } from './support/Clocks'
import {
  canAct, compact, pick, SEVERITIES, SLA_STATES, STATUSES, STATUTORY_STATES, TYPES, words,
  type CaseStatus, type CaseType, type ClockState, type Severity,
} from './support/model'
import { OpenCaseForm } from './support/OpenCaseForm'
import { Tiles } from './workspace'
import type { PageProps } from './types'
import './obligations.css'

// Support cases (Engineering Doc 18, W16): the triage queue. Requests are
// routed and prioritised without any tax-rule or financial authority; the
// support SLA and a statutory deadline are two clocks, shown apart.

type Tab = 'queue' | 'open'

const TABS: readonly { value: Tab; label: string }[] = [
  { value: 'queue', label: 'Triage queue' },
  { value: 'open', label: 'Open a case' },
]

type Owner = 'ALL' | 'MINE' | 'UNASSIGNED'

export const SupportCasesPage = ({ session, route, onTabChange }: PageProps) => {
  const [tab, setTab] = useRoutedTab(TABS.map((t) => t.value), 'queue', route, onTabChange)
  const [selected, setSelected] = useState<string | null>(null)
  const [opened, setOpened] = useState(0)
  const acting = canAct(session.user.roles)
  return (
    <div className="ztax-page">
      <PageHeader
        title="Support Cases"
        subtitle="Route and prioritise customer requests. Support classifies, requests evidence, assigns, escalates and responds — it never refunds, files, pays or edits a rule."
      />
      <TabBar label="Support cases" tabs={acting ? TABS : TABS.slice(0, 1)} current={tab} onSelect={setTab} />
      {tab === 'open' && acting ? (
        <OpenCaseForm onOpened={(c) => { setSelected(c.id); setOpened((n) => n + 1); setTab('queue') }} />
      ) : (
        <Queue key={opened} userId={session.user.id} acting={acting} selected={selected} setSelected={setSelected} />
      )}
    </div>
  )
}

const Queue = ({ userId, acting, selected, setSelected }: {
  userId: string
  acting: boolean
  selected: string | null
  setSelected: (id: string | null) => void
}) => {
  const [type, setType] = useState<CaseType | ''>('')
  const [severity, setSeverity] = useState<Severity | ''>('')
  const [status, setStatus] = useState<CaseStatus | ''>('')
  const [owner, setOwner] = useState<Owner>('ALL')
  const [sla, setSla] = useState<ClockState | ''>('')
  const [statutory, setStatutory] = useState<ClockState | ''>('')
  const [product, setProduct] = useState('')
  const [period, setPeriod] = useState('')
  const validPeriod = /^\d{4}-(0[1-9]|1[0-2])$/.test(period)
  const { load, reload } = useQueue(type, severity, status, owner, sla, statutory, product, validPeriod ? period : '', userId)

  return (
    <>
      <Card
        title="Queue"
        aside={<button type="button" className="ztax-btn-quiet" onClick={reload}>Refresh</button>}
      >
        <div className="ztax-filter-bar" style={{ flexWrap: 'wrap' }}>
          <Field label="Type">
            <select value={type} onChange={(e) => { setType(pick(TYPES, e.target.value)) }}>
              <option value="">All</option>
              {TYPES.map((t) => <option key={t} value={t}>{words(t)}</option>)}
            </select>
          </Field>
          <Field label="Severity">
            <select value={severity} onChange={(e) => { setSeverity(pick(SEVERITIES, e.target.value)) }}>
              <option value="">All</option>
              {SEVERITIES.map((s) => <option key={s} value={s}>{s}</option>)}
            </select>
          </Field>
          <Field label="Status">
            <select value={status} onChange={(e) => { setStatus(pick(STATUSES, e.target.value)) }}>
              <option value="">All</option>
              {STATUSES.map((s) => <option key={s} value={s}>{words(s)}</option>)}
            </select>
          </Field>
          <Field label="Owner">
            <select value={owner} onChange={(e) => { setOwner(pick(['ALL', 'MINE', 'UNASSIGNED'] as const, e.target.value) || 'ALL') }}>
              <option value="ALL">Anyone</option>
              <option value="MINE">Mine</option>
              <option value="UNASSIGNED">Unassigned</option>
            </select>
          </Field>
          <Field label="Support SLA">
            <select value={sla} onChange={(e) => { setSla(pick(SLA_STATES, e.target.value)) }}>
              <option value="">Any</option>
              {SLA_STATES.map((s) => <option key={s} value={s}>{words(s)}</option>)}
            </select>
          </Field>
          <Field label="Statutory clock">
            <select value={statutory} onChange={(e) => { setStatutory(pick(STATUTORY_STATES, e.target.value)) }}>
              <option value="">Any</option>
              {STATUTORY_STATES.map((s) => <option key={s} value={s}>{s === 'NONE' ? 'No statutory deadline' : words(s)}</option>)}
            </select>
          </Field>
          <Field label="Product">
            <input value={product} onChange={(e) => { setProduct(e.target.value) }} />
          </Field>
          <Field label="Fiscal period" help="YYYY-MM">
            <input value={period} placeholder="2026-09" onChange={(e) => { setPeriod(e.target.value) }} />
          </Field>
        </div>
        <Loaded load={load}>
          {(data) => {
            const open = data.cases.filter((c) => c.status !== 'CLOSED')
            return (
              <>
                <Tiles tiles={[
                  { label: 'Open', count: open.length },
                  { label: 'SLA breached', count: open.filter((c) => c.clocks.sla === 'BREACHED').length, tone: 'bad' },
                  { label: 'SLA at risk', count: open.filter((c) => c.clocks.sla === 'AT_RISK').length, tone: 'warn' },
                  { label: 'Statutory deadline first', count: open.filter((c) => c.clocks.statutoryPrecedes).length, tone: 'warn',
                    note: 'The law’s deadline falls before the SLA' },
                  { label: 'Facts requested', count: open.filter((c) => c.status === 'TRIAGE_INCOMPLETE').length, tone: 'warn' },
                ]} />
                {data.cases.length === 0 ? <Empty>No case matches. {acting ? 'Open one from the tab above.' : ''}</Empty> : (
                  <div style={{ overflowX: 'auto' }}>
                    <table className="ztax-action-table">
                      <thead>
                        <tr><th>Subject</th><th>Type</th><th>Sev.</th><th>Status</th><th>Support SLA</th><th>Statutory</th><th>Owner</th><th>Opened</th><th>Action</th></tr>
                      </thead>
                      <tbody>
                        {data.cases.map((c) => (
                          <tr key={c.id} className={c.id === selected ? 'ztax-row-selected' : undefined}>
                            <td style={{ paddingRight: 8 }}>{c.subject}{c.product === undefined ? null : <span className="muted"> · {c.product}</span>}</td>
                            <td style={{ paddingRight: 8 }}>{words(c.type)}</td>
                            <td style={{ paddingRight: 8 }}>{c.severity}</td>
                            <td style={{ paddingRight: 8 }}><Status value={c.status} /></td>
                            <td style={{ paddingRight: 8, whiteSpace: 'nowrap' }}><ClockChip state={c.clocks.sla} /> {instant(c.clocks.slaDueAt)}</td>
                            <td style={{ paddingRight: 8, whiteSpace: 'nowrap' }}>
                              {c.clocks.statutoryDueAt === undefined ? <span className="muted">—</span> : (
                                <>
                                  <ClockChip state={c.clocks.statutory} /> {instant(c.clocks.statutoryDueAt)}
                                  {c.clocks.statutoryPrecedes ? <span className="ztax-status ztax-status--bad" style={{ marginLeft: 4 }}>first</span> : null}
                                </>
                              )}
                            </td>
                            <td style={{ paddingRight: 8 }}>
                              {c.assignee === undefined ? <span className="muted">unassigned</span> : c.assignee === userId ? 'me' : <Id value={c.assignee} />}
                            </td>
                            <td style={{ paddingRight: 8, whiteSpace: 'nowrap' }}>{instant(c.openedAt)}</td>
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
              </>
            )
          }}
        </Loaded>
      </Card>
      {selected === null ? null : (
        <CaseDetail key={selected} id={selected} userId={userId} canAct={acting} onChanged={reload} onClose={() => { setSelected(null) }} />
      )}
    </>
  )
}

/** The queue, read with the filters the server applies. */
function useQueue(type: string, severity: string, status: string, owner: Owner, sla: string, statutory: string,
  product: string, period: string, userId: string) {
  return useLoad(
    (signal) => ztax.listSupportCases(compact<Parameters<typeof ztax.listSupportCases>[0]>({
      type: type || undefined, severity: severity || undefined, status: status || undefined,
      slaClock: sla || undefined, statutoryClock: statutory || undefined, product: product.trim() || undefined,
      fiscalPeriod: period || undefined,
      assignee: owner === 'MINE' ? userId : undefined, unassigned: owner === 'UNASSIGNED' ? true : undefined, limit: 500,
    }), signal),
    [type, severity, status, owner, sla, statutory, product, period, userId],
  )
}
