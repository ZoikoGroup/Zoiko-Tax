import { useState } from 'react'

import type { DashboardAction } from '@zoikotax/sdk'

import type { View } from '../components/layout/Sidebar'
import { civilDate, instant, moneyText, periodLabel } from '../dashboard/format'
import { ztax } from '../platform/client'
import { Card, Empty, Field, Id, Loaded, PageHeader, useLoad } from '../ui/kit'
import type { PageProps } from './types'
import './obligations.css'

type Priority = DashboardAction['priority']
type Kind = DashboardAction['kind']

const PRIORITIES: readonly Priority[] = ['P0', 'P1', 'P2']

const KINDS: readonly Kind[] = [
  'OBLIGATION_OVERDUE', 'OBLIGATION_ATTENTION', 'OBLIGATION_DUE', 'RECONCILIATION_BREAK',
  'REFUND_UNCERTAIN', 'REFUND_FAILED', 'DECISION_REVIEW', 'WEBHOOK_DEAD',
]

/** The workspace where each kind of action is worked. */
const WORKSPACE: Record<Kind, View> = {
  OBLIGATION_OVERDUE: 'obligations',
  OBLIGATION_ATTENTION: 'obligations',
  OBLIGATION_DUE: 'obligations',
  RECONCILIATION_BREAK: 'reconcile',
  REFUND_UNCERTAIN: 'determine',
  REFUND_FAILED: 'determine',
  DECISION_REVIEW: 'determine',
  WEBHOOK_DEAD: 'admin',
}

const PRIORITY_MEANING: Record<Priority, string> = { P0: 'now', P1: 'this week', P2: 'this month' }

const priorityClass = (p: Priority) =>
  p === 'P0' ? 'ztax-badge-p0-red' : p === 'P1' ? 'ztax-badge-p1-yellow' : 'ztax-badge-p2-teal'

const isPriority = (v: string): v is Priority => (PRIORITIES as readonly string[]).includes(v)
const isKind = (v: string): v is Kind => (KINDS as readonly string[]).includes(v)

export const ActionCenterPage = ({ period, onNavigate }: PageProps) => {
  const [priority, setPriority] = useState<Priority | 'ALL'>('ALL')
  const [kind, setKind] = useState<Kind | 'ALL'>('ALL')
  const { load, reload } = useLoad((signal) => ztax.getDashboard({ period }, signal), [period])

  const subtitle = load.status === 'ready' ? (
    <span>
      {periodLabel(load.data.period)} ·{' '}
      {PRIORITIES.map((p) => `${load.data.actions.filter((a) => a.priority === p).length} ${p}`).join(' · ')} · read{' '}
      {instant(load.data.asOf)}
    </span>
  ) : undefined

  return (
    <div className="ztax-page">
      <PageHeader
        title="Action Center"
        subtitle={subtitle}
        actions={
          <button type="button" className="ztax-btn-quiet" onClick={reload}>
            Refresh
          </button>
        }
      />
      <Loaded load={load}>
        {(d) => {
          const shown = d.actions.filter(
            (a) => (priority === 'ALL' || a.priority === priority) && (kind === 'ALL' || a.kind === kind),
          )
          return (
            <Card
              title={`${shown.length} of ${d.actions.length} ${d.actions.length === 1 ? 'action' : 'actions'}`}
              aside={
                <div className="ztax-filter-bar">
                  <Field label="Priority">
                    <select
                      value={priority}
                      onChange={(e) => {
                        const v = e.target.value
                        setPriority(isPriority(v) ? v : 'ALL')
                      }}
                    >
                      <option value="ALL">All</option>
                      {PRIORITIES.map((p) => (
                        <option key={p} value={p}>{p} — {PRIORITY_MEANING[p]}</option>
                      ))}
                    </select>
                  </Field>
                  <Field label="Kind">
                    <select
                      value={kind}
                      onChange={(e) => {
                        const v = e.target.value
                        setKind(isKind(v) ? v : 'ALL')
                      }}
                    >
                      <option value="ALL">All</option>
                      {KINDS.map((k) => (
                        <option key={k} value={k}>{k.replaceAll('_', ' ')}</option>
                      ))}
                    </select>
                  </Field>
                </div>
              }
            >
              {d.actions.length === 0 ? (
                <Empty>Nothing in the record for {periodLabel(d.period)} needs a person right now.</Empty>
              ) : shown.length === 0 ? (
                <Empty>No action matches these filters.</Empty>
              ) : (
                <div style={{ overflowX: 'auto' }}>
                  <table className="ztax-action-table">
                    <thead>
                      <tr>
                        <th>Priority</th>
                        <th>Issue</th>
                        <th>Context</th>
                        <th>Subject</th>
                        <th>Exposure</th>
                        <th>Due</th>
                        <th>Action</th>
                      </tr>
                    </thead>
                    <tbody>
                      {shown.map((item, i) => (
                        <tr key={item.subjectId ?? `${item.kind}-${i}`}>
                          <td style={{ paddingRight: 8 }}>
                            <span className={`ztax-badge-priority ${priorityClass(item.priority)}`}>{item.priority}</span>
                          </td>
                          <td style={{ fontWeight: 500, color: '#111827', paddingRight: 8 }}>
                            {item.title}
                            <div className="muted" style={{ fontSize: 10 }}>{item.kind.replaceAll('_', ' ')}</div>
                          </td>
                          <td style={{ color: '#6b7280', paddingRight: 8 }}>{item.context}</td>
                          <td style={{ paddingRight: 8 }}><Id value={item.subjectId} /></td>
                          <td style={{ fontWeight: 700, color: '#111827', paddingRight: 8, whiteSpace: 'nowrap' }}>
                            {item.exposure === undefined ? '—' : moneyText(item.exposure)}
                          </td>
                          <td
                            style={{
                              paddingRight: 8,
                              whiteSpace: 'nowrap',
                              color: item.priority === 'P0' ? '#dc2626' : '#6b7280',
                              fontWeight: item.priority === 'P0' ? 600 : 400,
                            }}
                          >
                            {item.due === undefined ? '—' : civilDate(item.due)}
                          </td>
                          <td style={{ whiteSpace: 'nowrap' }}>
                            <button
                              type="button"
                              className="ztax-table-action-btn"
                              onClick={() => { onNavigate(WORKSPACE[item.kind]) }}
                            >
                              Open
                            </button>
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </Card>
          )
        }}
      </Loaded>
    </div>
  )
}
