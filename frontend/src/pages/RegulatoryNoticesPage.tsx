import { useState } from 'react'

import type { NoticeListParams } from '@zoikotax/sdk'

import { instant } from '../dashboard/format'
import { ztax } from '../platform/client'
import { Card, Empty, Field, Id, Loaded, Money, PageHeader, Status, useLoad } from '../ui/kit'
import { CURRENCY, isDecimal, TabBar, toTimestamp } from './determine/shared'
import { IntakeForm } from './notices/IntakeForm'
import {
  AUTHENTICITY,
  deadlineText,
  pick,
  STATUSES,
  TIMEZONES,
  TYPES,
  words,
  type Authenticity,
  type NoticeStatus,
  type NoticeType,
} from './notices/model'
import { Flags, NoticeDetail } from './notices/NoticeDetail'
import { useRoutedTab } from './routedTab'
import type { PageProps } from './types'
import { Tiles } from './workspace'
import './obligations.css'

// Regulatory Notices (Engineering Doc 18 W17, G-052; NAV-013): notices,
// assessments, audits, appeals and information requests registered with the
// authority's own deadline, verified or flagged, and worked to an approved,
// receipted answer. The cell enforces every rule; this page shows what needs
// attention and offers only the acts a notice's status admits.

type Tab = 'register' | 'intake'

const TABS: readonly { value: Tab; label: string }[] = [
  { value: 'register', label: 'Notice register' },
  { value: 'intake', label: 'Register a notice' },
]

interface Filters {
  type: NoticeType | ''
  status: NoticeStatus | ''
  authority: string
  legalEntityId: string
  reference: string
  receivedFrom: string
  receivedTo: string
  period: string
  amountMin: string
  amountMax: string
  currency: string
  attachment: string
  authenticity: Authenticity | ''
  urgent: boolean
  includeClosed: boolean
}

const NO_FILTERS: Filters = {
  type: '', status: '', authority: '', legalEntityId: '', reference: '', receivedFrom: '', receivedTo: '', period: '',
  amountMin: '', amountMax: '', currency: 'EUR', attachment: '', authenticity: '', urgent: false, includeClosed: false,
}

/** The register query: only the filters set, amounts as the strings typed. */
function paramsOf(f: Filters): { params: NoticeListParams; invalid: string | null } {
  const params: NoticeListParams = { limit: 500 }
  const text: readonly (readonly ['authority' | 'legalEntityId' | 'authorityReference' | 'period' | 'attachmentSha256', string])[] = [
    ['authority', f.authority.trim()], ['legalEntityId', f.legalEntityId.trim()], ['authorityReference', f.reference.trim()],
    ['period', f.period], ['attachmentSha256', f.attachment.trim().toLowerCase()],
  ]
  for (const [k, v] of text) {
    if (v !== '') params[k] = v
  }
  if (f.type !== '') params.type = f.type
  if (f.status !== '') params.status = f.status
  if (f.authenticity !== '') params.authenticity = f.authenticity
  if (f.urgent) params.urgent = true
  if (f.includeClosed) params.includeClosed = true
  for (const [key, local] of [['receivedFrom', f.receivedFrom], ['receivedTo', f.receivedTo]] as const) {
    if (local !== '') {
      const ts = toTimestamp(local)
      if (ts === null) return { params, invalid: 'Received from and to are dates and times.' }
      params[key] = ts
    }
  }
  for (const [key, v] of [['amountMin', f.amountMin.trim()], ['amountMax', f.amountMax.trim()]] as const) {
    if (v !== '') {
      if (!isDecimal(v) || !CURRENCY.test(f.currency)) {
        return { params, invalid: 'Amount bounds are decimals such as 1000.00 with a three-letter currency; the cell compares them.' }
      }
      params[key] = v
      params.currency = f.currency
    }
  }
  return { params, invalid: null }
}

export const RegulatoryNoticesPage = ({ session, route, onTabChange }: PageProps) => {
  const [tab, setTab] = useRoutedTab(TABS.map((t) => t.value), 'register', route, onTabChange)
  const [selected, setSelected] = useState<string | null>(null)
  const [draft, setDraft] = useState<Filters>(NO_FILTERS)
  const [applied, setApplied] = useState<Filters>(NO_FILTERS)
  const roles = session.user.roles
  const canAct = roles.includes('OPERATOR') || roles.includes('ANALYST')
  const { params, invalid } = paramsOf(applied)
  const { load, reload } = useLoad((signal) => ztax.listNotices(params, signal), [JSON.stringify(params)])
  const field = <K extends keyof Filters>(k: K) => (v: Filters[K]) => { setDraft((d) => ({ ...d, [k]: v })) }

  return (
    <div className="ztax-page">
      <PageHeader title="Regulatory Notices"
        subtitle="Notices, assessments, audits, appeals and information requests, each with the authority’s verified deadline — not a support due date." />
      <TabBar label="Regulatory notices" tabs={TABS} current={tab} onSelect={setTab} />
      <datalist id="notice-timezones">
        {TIMEZONES.map((z) => <option key={z} value={z} />)}
      </datalist>
      {tab === 'intake' ? (
        canAct ? (
          <IntakeForm onCreated={(n) => { setSelected(n.id); setTab('register'); reload() }} />
        ) : (
          <Card><Empty>Registering a notice is for OPERATOR or ANALYST. Your roles can read the register.</Empty></Card>
        )
      ) : (
        <>
          <Card title="Filters">
            <form className="ztax-filter-bar" onSubmit={(e) => { e.preventDefault(); setApplied(draft) }}>
              <Field label="Type">
                <select value={draft.type} onChange={(e) => { field('type')(e.target.value === '' ? '' : pick(TYPES, e.target.value, 'NOTICE')) }}>
                  <option value="">Any</option>
                  {TYPES.map((t) => <option key={t} value={t}>{words(t)}</option>)}
                </select>
              </Field>
              <Field label="Status">
                <select value={draft.status} onChange={(e) => { field('status')(e.target.value === '' ? '' : pick(STATUSES, e.target.value, 'RECEIVED')) }}>
                  <option value="">Open</option>
                  {STATUSES.map((s) => <option key={s} value={s}>{words(s)}</option>)}
                </select>
              </Field>
              <Field label="Authority">
                <input value={draft.authority} onChange={(e) => { field('authority')(e.target.value) }} />
              </Field>
              <Field label="Legal entity">
                <input value={draft.legalEntityId} onChange={(e) => { field('legalEntityId')(e.target.value) }} />
              </Field>
              <Field label="Authority reference">
                <input value={draft.reference} onChange={(e) => { field('reference')(e.target.value) }} />
              </Field>
              <Field label="Received from (UTC)">
                <input type="datetime-local" value={draft.receivedFrom} onChange={(e) => { field('receivedFrom')(e.target.value) }} />
              </Field>
              <Field label="Received to (UTC)">
                <input type="datetime-local" value={draft.receivedTo} onChange={(e) => { field('receivedTo')(e.target.value) }} />
              </Field>
              <Field label="Period contains">
                <input type="date" value={draft.period} onChange={(e) => { field('period')(e.target.value) }} />
              </Field>
              <Field label="Amount at least">
                <input value={draft.amountMin} inputMode="decimal" onChange={(e) => { field('amountMin')(e.target.value) }} />
              </Field>
              <Field label="Amount at most">
                <input value={draft.amountMax} inputMode="decimal" onChange={(e) => { field('amountMax')(e.target.value) }} />
              </Field>
              <Field label="Currency">
                <input value={draft.currency} maxLength={3} onChange={(e) => { field('currency')(e.target.value.toUpperCase()) }} />
              </Field>
              <Field label="Attachment SHA-256">
                <input value={draft.attachment} onChange={(e) => { field('attachment')(e.target.value) }} />
              </Field>
              <Field label="Authenticity">
                <select value={draft.authenticity} onChange={(e) => { field('authenticity')(e.target.value === '' ? '' : pick(AUTHENTICITY, e.target.value, 'UNKNOWN')) }}>
                  <option value="">Any</option>
                  {AUTHENTICITY.map((a) => <option key={a} value={a}>{words(a)}</option>)}
                </select>
              </Field>
              <Field label="Urgent only">
                <input type="checkbox" checked={draft.urgent} onChange={(e) => { field('urgent')(e.target.checked) }} />
              </Field>
              <Field label="Include closed">
                <input type="checkbox" checked={draft.includeClosed} onChange={(e) => { field('includeClosed')(e.target.checked) }} />
              </Field>
              <button type="submit" className="ztax-btn-copper">Apply</button>
              <button type="button" className="ztax-btn-quiet" onClick={() => { setDraft(NO_FILTERS); setApplied(NO_FILTERS) }}>Clear</button>
              <button type="button" className="ztax-btn-quiet" onClick={reload}>Refresh</button>
            </form>
            {invalid === null ? null : <p className="ztax-error-note" role="alert">{invalid}</p>}
          </Card>

          <Loaded load={load}>
            {(data) => {
              const rows = data.notices
              const has = (f: string) => rows.filter((n) => (n.flags as readonly string[]).includes(f)).length
              return (
                <>
                  <Tiles tiles={[
                    { label: 'Urgent legal review', count: rows.filter((n) => n.urgentLegalReview).length, tone: 'bad' },
                    { label: 'Deadline unknown or unverified', count: has('DEADLINE_UNKNOWN') + has('DEADLINE_UNVERIFIED'), tone: 'bad' },
                    { label: 'Statutory deadline passed', count: has('STATUTORY_OVERDUE'), tone: 'bad' },
                    { label: 'Deadline within 7 days', count: has('DEADLINE_SOON'), tone: 'warn' },
                    { label: 'Internal SLA breached', count: has('SLA_BREACHED'), tone: 'warn', note: 'Our clock, not the authority’s' },
                  ]} />
                  <Card title={`${String(rows.length)} notices`}>
                    {rows.length === 0 ? <Empty>No notice matches. Register one under “Register a notice”.</Empty> : (
                      <div style={{ overflowX: 'auto' }}>
                        <table className="ztax-action-table">
                          <thead>
                            <tr>
                              <th>Authority · reference</th><th>Type</th><th>Received</th><th>Statutory deadline</th>
                              <th>Amount</th><th>Status</th><th>Attention</th><th>Action</th>
                            </tr>
                          </thead>
                          <tbody>
                            {rows.map((n) => (
                              <tr key={n.id} className={selected === n.id ? 'ztax-row-selected' : undefined} style={{ verticalAlign: 'top' }}>
                                <td style={{ paddingRight: 8 }}>
                                  {n.authority} <span className="mono">{n.authorityReference}</span>
                                  <div className="muted" style={{ fontSize: 11 }}>
                                    {n.jurisdiction} · {n.entityResolved ? <Id value={n.resolvedLegalEntityId} /> : 'recipient unresolved'}
                                  </div>
                                </td>
                                <td style={{ paddingRight: 8 }}>{words(n.type)}</td>
                                <td style={{ paddingRight: 8, whiteSpace: 'nowrap' }}>{instant(n.receivedAt)}</td>
                                <td style={{ paddingRight: 8 }}>
                                  {deadlineText(n.statutoryDeadline.current)}{' '}
                                  <Status value={n.statutoryDeadline.current.verified ? 'VERIFIED' : 'UNVERIFIED'} />
                                  {n.statutoryDeadline.current.kind === 'ORIGINAL' ? null : (
                                    <div className="muted" style={{ fontSize: 11 }}>
                                      {words(n.statutoryDeadline.current.kind)}; received with {deadlineText(n.statutoryDeadline.original)}
                                    </div>
                                  )}
                                </td>
                                <td style={{ paddingRight: 8, whiteSpace: 'nowrap' }}><Money value={n.amount} /></td>
                                <td style={{ paddingRight: 8 }}><Status value={n.status} /></td>
                                <td style={{ paddingRight: 8 }}><Flags flags={n.flags} /></td>
                                <td><button type="button" className="ztax-table-action-btn" onClick={() => { setSelected(n.id) }}>Open</button></td>
                              </tr>
                            ))}
                          </tbody>
                        </table>
                      </div>
                    )}
                  </Card>
                </>
              )
            }}
          </Loaded>

          {selected === null ? null : (
            <NoticeDetail key={selected} id={selected} canAct={canAct} onChanged={reload} onClose={() => { setSelected(null) }} />
          )}
        </>
      )}
    </div>
  )
}
