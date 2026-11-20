import { Fragment, useEffect, useRef, useState, type FormEvent } from 'react'

import { ZoikoTaxError, type S } from '@zoikotax/sdk'

import { civilDate, instant, periodLabel } from '../dashboard/format'
import { ztax } from '../platform/client'
import {
  Amount,
  Card,
  Columns,
  Empty,
  ErrorNote,
  errorText,
  Field,
  Id,
  Loaded,
  PageHeader,
  Raw,
  Status,
  Submit,
  useAction,
  useLoad,
} from '../ui/kit'
import { TabBar } from './determine/shared'
import { ObligationExplainPanel } from './obligations/ObligationExplainPanel'
import { useRoutedTab } from './routedTab'
import type { PageProps } from './types'
import { Tiles, words } from './workspace'
import { OBLIGATION_EXCEPTIONS } from './worklists'
import './obligations.css'

type Obligation = S<'Obligation'>
type Effective = S<'EffectiveObligationStatus'>
type Target = S<'ObligationTransitionRequest'>['to']

const STATUSES: readonly Effective[] = [
  'OPEN', 'DATA_REQUIRED', 'READY', 'OVERDUE', 'FILED', 'ACCEPTED', 'REJECTED', 'UNCERTAIN',
  'PAYMENT_DUE', 'PAID', 'AMENDMENT_REQUIRED', 'SUSPENDED', 'CLOSED',
]

// The submission and payment boundary sets every other status, never a person.
const TARGETS: readonly Target[] = ['OPEN', 'DATA_REQUIRED', 'READY', 'SUSPENDED', 'CLOSED']

const isStatus = (value: string): value is Effective => (STATUSES as readonly string[]).includes(value)
const isTarget = (value: string): value is Target => (TARGETS as readonly string[]).includes(value)

const isConflict = (error: unknown) => error instanceof ZoikoTaxError && error.reasonCode === 'OPTIMISTIC_CONFLICT'

type Tab = 'register' | 'calendar' | 'exceptions'

const TABS: readonly { value: Tab; label: string }[] = [
  { value: 'register', label: 'Register' },
  { value: 'calendar', label: 'Compliance calendar' },
  { value: 'exceptions', label: 'Exceptions' },
]

export const ObligationsPage = ({ session, period, route, onTabChange }: PageProps) => {
  const [tab, setTab] = useRoutedTab(TABS.map((t) => t.value), 'register', route, onTabChange)
  const [selected, setSelected] = useState<string | null>(null)
  const openInRegister = (id: string) => {
    setSelected(id)
    setTab('register')
  }

  return (
    <div className="ztax-page">
      <PageHeader
        title="Obligations"
        subtitle="Periodic duties owed to each authority, earliest due first. Status is as of today in the authority's calendar."
      />
      <TabBar label="Obligations" tabs={TABS} current={tab} onSelect={setTab} />
      {tab === 'register' ? <Register session={session} selected={selected} setSelected={setSelected} />
        : tab === 'calendar' ? <Calendar period={period} onOpen={openInRegister} />
        : <Exceptions onOpen={openInRegister} />}
    </div>
  )
}

// ---------------------------------------------------------------------------
// calendar
// ---------------------------------------------------------------------------

const pad2 = (n: number) => (n < 10 ? `0${n}` : `${n}`)

/** "2026-09" moved by `delta` months, without parsing digits by hand. */
function shiftMonth(month: string, delta: number): string {
  const d = new Date(`${month}-01T00:00:00Z`)
  if (Number.isNaN(d.getTime())) {
    return month
  }
  d.setUTCMonth(d.getUTCMonth() + delta)
  return `${d.getUTCFullYear()}-${pad2(d.getUTCMonth() + 1)}`
}

const Calendar = ({ period, onOpen }: { period: string; onOpen: (id: string) => void }) => {
  const [month, setMonth] = useState(period)
  const { load, reload } = useLoad((signal) => ztax.listObligations({ limit: 500 }, signal), [])

  return (
    <Loaded load={load}>
      {(data) => {
        const current = data.obligations.filter((o) => o.supersededBy === undefined)
        const months = [...new Set(current.map((o) => o.dueDate.slice(0, 7)))].sort()
        const inMonth = current.filter((o) => o.dueDate.startsWith(`${month}-`))
        const days = [...new Set(inMonth.map((o) => o.dueDate))].sort()
        const overdue = inMonth.filter((o) => o.effectiveStatus === 'OVERDUE').length
        const settled = inMonth.filter((o) => ['FILED', 'ACCEPTED', 'PAID', 'CLOSED'].includes(o.effectiveStatus)).length
        return (
          <>
            <Card
              title={`Due in ${periodLabel(month)}`}
              aside={
                <div className="ztax-filter-bar">
                  <button type="button" className="ztax-btn-quiet" onClick={() => { setMonth(shiftMonth(month, -1)) }}>‹ Previous</button>
                  <Field label="Month">
                    <select value={month} onChange={(e) => { setMonth(e.target.value) }}>
                      {[...new Set([...months, month])].sort().map((m) => (
                        <option key={m} value={m}>
                          {periodLabel(m)} ({current.filter((o) => o.dueDate.startsWith(`${m}-`)).length})
                        </option>
                      ))}
                    </select>
                  </Field>
                  <button type="button" className="ztax-btn-quiet" onClick={() => { setMonth(shiftMonth(month, 1)) }}>Next ›</button>
                  <button type="button" className="ztax-btn-quiet" onClick={reload}>Refresh</button>
                </div>
              }
            >
              <p className="ztax-note">
                {inMonth.length} due · {overdue} overdue · {settled} filed, accepted, paid or closed · {inMonth.length - overdue - settled} still open.
                Due dates are civil dates in each authority’s own calendar.
              </p>
              {days.length === 0 ? <Empty>Nothing falls due in {periodLabel(month)}.</Empty> : (
                <div style={{ display: 'flex', flexDirection: 'column', gap: 16, marginTop: 12 }}>
                  {days.map((day) => (
                    <section key={day} aria-label={civilDate(day)}>
                      <h3 className="ztax-card-title" style={{ marginBottom: 6 }}>{civilDate(day)}</h3>
                      <table className="ztax-action-table">
                        <tbody>
                          {inMonth.filter((o) => o.dueDate === day).map((o) => (
                            <tr key={o.id}>
                              <td style={{ fontWeight: 500, color: '#111827', paddingRight: 8 }}>{o.type}</td>
                              <td className="mono" style={{ paddingRight: 8 }}>{o.jurisdiction} · {o.authority}</td>
                              <td style={{ paddingRight: 8, whiteSpace: 'nowrap' }}>{civilDate(o.periodStart)} – {civilDate(o.periodEnd)}</td>
                              <td style={{ paddingRight: 8 }}><Status value={o.effectiveStatus} /></td>
                              <td style={{ paddingRight: 8, whiteSpace: 'nowrap' }}><Amount amount={o.assessedAmount} currency={o.currency} /></td>
                              <td className="muted" style={{ paddingRight: 8 }}>{o.timezone}</td>
                              <td>
                                <button type="button" className="ztax-table-action-btn" onClick={() => { onOpen(o.id) }}>Open</button>
                              </td>
                            </tr>
                          ))}
                        </tbody>
                      </table>
                    </section>
                  ))}
                </div>
              )}
            </Card>
          </>
        )
      }}
    </Loaded>
  )
}

// ---------------------------------------------------------------------------
// exceptions
// ---------------------------------------------------------------------------

const Exceptions = ({ onOpen }: { onOpen: (id: string) => void }) => {
  const { load, reload } = useLoad((signal) => ztax.listObligations({ limit: 500 }, signal), [])
  return (
    <Loaded load={load}>
      {(data) => {
        const rows = data.obligations
          .filter((o) => o.supersededBy === undefined && OBLIGATION_EXCEPTIONS[o.effectiveStatus] !== undefined)
          .sort((a, b) => (a.dueDate < b.dueDate ? -1 : a.dueDate > b.dueDate ? 1 : 0))
        const tiles = (Object.keys(OBLIGATION_EXCEPTIONS) as Effective[]).map((s) => ({
          label: words(s),
          count: rows.filter((o) => o.effectiveStatus === s).length,
          tone: s === 'OVERDUE' || s === 'REJECTED' || s === 'UNCERTAIN' ? 'bad' as const : 'warn' as const,
        }))
        return (
          <>
            <Tiles tiles={tiles} />
            <Card
              title={`${rows.length} ${rows.length === 1 ? 'obligation needs' : 'obligations need'} attention`}
              aside={<button type="button" className="ztax-btn-quiet" onClick={reload}>Refresh</button>}
            >
              {rows.length === 0 ? <Empty>No obligation is overdue, rejected, uncertain, or waiting on data or an amendment.</Empty> : (
                <div style={{ overflowX: 'auto' }}>
                  <table className="ztax-action-table">
                    <thead>
                      <tr><th>Type</th><th>Authority</th><th>Period</th><th>Due</th><th>Status</th><th>Why</th><th>Assessed</th><th>Action</th></tr>
                    </thead>
                    <tbody>
                      {rows.map((o) => (
                        <tr key={o.id}>
                          <td style={{ fontWeight: 500, color: '#111827', paddingRight: 8 }}>{o.type}</td>
                          <td className="mono" style={{ paddingRight: 8 }}>{o.jurisdiction} · {o.authority}</td>
                          <td style={{ paddingRight: 8, whiteSpace: 'nowrap' }}>{civilDate(o.periodStart)} – {civilDate(o.periodEnd)}</td>
                          <td style={{ paddingRight: 8, whiteSpace: 'nowrap' }}>{civilDate(o.dueDate)}</td>
                          <td style={{ paddingRight: 8 }}><Status value={o.effectiveStatus} /></td>
                          <td style={{ paddingRight: 8, color: '#6b7280' }}>{OBLIGATION_EXCEPTIONS[o.effectiveStatus]}</td>
                          <td style={{ paddingRight: 8, whiteSpace: 'nowrap' }}><Amount amount={o.assessedAmount} currency={o.currency} /></td>
                          <td>
                            <button type="button" className="ztax-table-action-btn" onClick={() => { onOpen(o.id) }}>Open</button>
                          </td>
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
  )
}

// ---------------------------------------------------------------------------
// register
// ---------------------------------------------------------------------------

const Register = ({ session, selected, setSelected }: {
  session: PageProps['session']
  selected: string | null
  setSelected: (id: string | null) => void
}) => {
  const [status, setStatus] = useState<Effective | 'ALL'>('ALL')
  const { load, reload } = useLoad(
    (signal) => ztax.listObligations(status === 'ALL' ? { limit: 500 } : { status, limit: 500 }, signal),
    [status],
  )
  const canMove = session.user.roles.includes('OPERATOR')
  // Asking the AI to explain a row is open to the roles that may ask it to propose.
  const canExplain = session.user.roles.some((r) => r === 'OPERATOR' || r === 'ANALYST')
  // The one row whose explanation is open. Closing it gives focus back to that row's button.
  const [explaining, setExplaining] = useState<string | null>(null)
  const openers = useRef(new Map<string, HTMLButtonElement>())
  const returnFocus = useRef<string | null>(null)
  const closeExplain = () => {
    returnFocus.current = explaining
    setExplaining(null)
  }
  useEffect(() => {
    if (explaining === null && returnFocus.current !== null) {
      openers.current.get(returnFocus.current)?.focus()
      returnFocus.current = null
    }
  }, [explaining])

  return (
    <>
      <Card
        title="Obligations"
        aside={
          <div className="ztax-filter-bar">
            <Field label="Status">
              <select
                value={status}
                onChange={(e) => {
                  const v = e.target.value
                  setStatus(isStatus(v) ? v : 'ALL')
                }}
              >
                <option value="ALL">All</option>
                {STATUSES.map((s) => (
                  <option key={s} value={s}>{s.replaceAll('_', ' ')}</option>
                ))}
              </select>
            </Field>
            <button type="button" className="ztax-btn-quiet" onClick={reload}>
              Refresh
            </button>
          </div>
        }
      >
        <Loaded load={load}>
          {(data) =>
            data.obligations.length === 0 ? (
              <Empty>
                {status === 'ALL'
                  ? 'No obligations yet. One is created the first time a commit falls in its period.'
                  : `No obligation is ${status.replaceAll('_', ' ')}.`}
              </Empty>
            ) : (
              <div style={{ overflowX: 'auto' }}>
                <table className="ztax-action-table">
                  <thead>
                    <tr>
                      <th>Type</th>
                      <th>Jurisdiction</th>
                      <th>Authority</th>
                      <th>Period</th>
                      <th>Due</th>
                      <th>Status</th>
                      <th>Assessed</th>
                      <th>Action</th>
                    </tr>
                  </thead>
                  <tbody>
                    {data.obligations.map((o) => (
                      <Fragment key={o.id}>
                      <tr className={o.id === selected ? 'ztax-row-selected' : undefined}>
                        <td style={{ fontWeight: 500, color: '#111827', paddingRight: 8 }}>{o.type}</td>
                        <td className="mono" style={{ paddingRight: 8 }}>{o.jurisdiction}</td>
                        <td className="mono" style={{ paddingRight: 8 }}>{o.authority}</td>
                        <td style={{ paddingRight: 8, whiteSpace: 'nowrap' }}>
                          {civilDate(o.periodStart)} – {civilDate(o.periodEnd)}
                        </td>
                        <td style={{ paddingRight: 8, whiteSpace: 'nowrap' }}>{civilDate(o.dueDate)}</td>
                        <td style={{ paddingRight: 8 }}>
                          <Status value={o.effectiveStatus} />
                        </td>
                        <td style={{ paddingRight: 8, whiteSpace: 'nowrap' }}>
                          <Amount amount={o.assessedAmount} currency={o.currency} />
                        </td>
                        <td style={{ whiteSpace: 'nowrap' }}>
                          <button
                            type="button"
                            className="ztax-table-action-btn"
                            aria-pressed={o.id === selected}
                            onClick={() => { setSelected(o.id) }}
                          >
                            Open
                          </button>
                          {canExplain ? (
                            <>
                              {' '}
                              <button
                                type="button"
                                className="ztax-table-action-btn"
                                aria-expanded={explaining === o.id}
                                aria-label={`Explain ${o.type} due ${civilDate(o.dueDate)}`}
                                ref={(el) => {
                                  if (el === null) openers.current.delete(o.id)
                                  else openers.current.set(o.id, el)
                                }}
                                onClick={() => { setExplaining(o.id) }}
                              >
                                Explain
                              </button>
                            </>
                          ) : null}
                        </td>
                      </tr>
                      {explaining === o.id ? (
                        <tr>
                          <td colSpan={8}>
                            <ObligationExplainPanel obligationId={o.id} onClose={closeExplain} />
                          </td>
                        </tr>
                      ) : null}
                      </Fragment>
                    ))}
                  </tbody>
                </table>
              </div>
            )
          }
        </Loaded>
        {canExplain ? null : (
          <p className="ztax-note">Asking the AI to explain an obligation needs the Operator or Analyst role.</p>
        )}
      </Card>

      {selected === null ? null : (
        <ObligationDetail
          key={selected}
          id={selected}
          canMove={canMove}
          onSelect={setSelected}
          onStale={reload}
          onMoved={(next) => {
            setSelected(next.id)
            reload()
          }}
          onClose={() => { setSelected(null) }}
        />
      )}
    </>
  )
}

const ObligationDetail = ({ id, canMove, onSelect, onStale, onMoved, onClose }: {
  id: string
  canMove: boolean
  onSelect: (id: string) => void
  onStale: () => void
  onMoved: (next: Obligation) => void
  onClose: () => void
}) => {
  const { load, reload } = useLoad((signal) => ztax.getObligation(id, signal), [id])

  return (
    <Loaded load={load}>
      {(o) => (
        <Columns>
          <Card
            title={`${o.type} · ${civilDate(o.periodStart)} – ${civilDate(o.periodEnd)}`}
            aside={
              <button type="button" className="ztax-btn-quiet" onClick={onClose}>
                Close
              </button>
            }
          >
            <dl className="ztax-kv">
              <dt>Obligation</dt>
              <dd><Id value={o.id} /></dd>
              <dt>Status</dt>
              <dd>
                <Status value={o.effectiveStatus} />
                {o.effectiveStatus === o.status ? null : <span className="muted"> stored as {o.status.replaceAll('_', ' ')}</span>}
              </dd>
              <dt>Duty</dt>
              <dd>{o.duty.replaceAll('_', ' ')}</dd>
              <dt>Jurisdiction</dt>
              <dd className="mono">{o.jurisdiction}</dd>
              <dt>Authority</dt>
              <dd className="mono">{o.authority}</dd>
              <dt>Legal entity</dt>
              <dd><Id value={o.legalEntityId} /></dd>
              <dt>Due</dt>
              <dd>{civilDate(o.dueDate)} <span className="muted">({o.timezone})</span></dd>
              <dt>Assessed</dt>
              <dd><Amount amount={o.assessedAmount} currency={o.currency} /></dd>
              <dt>Definition</dt>
              <dd className="mono">{o.definition.id} @ {o.definition.version}</dd>
              <dt>Content bundle</dt>
              <dd className="mono">{o.content.bundleId}</dd>
              <dt>Bundle digest</dt>
              <dd><Id value={o.content.bundleDigest} /></dd>
            </dl>
            <Raw value={o} />
          </Card>

          <Card title="History">
            <dl className="ztax-kv">
              <dt>Recorded</dt>
              <dd>{instant(o.recordedAt)}</dd>
              <dt>Recorded by</dt>
              <dd>{o.recordedBy === undefined ? <span className="muted">a commit</span> : <Id value={o.recordedBy} />}</dd>
              <dt>Supersedes</dt>
              <dd>
                {o.supersedes === undefined ? <span className="muted">— first row</span> : (
                  <button type="button" className="ztax-table-action-btn" onClick={() => { onSelect(o.supersedes ?? id) }}>
                    <Id value={o.supersedes} />
                  </button>
                )}
              </dd>
              <dt>Superseded by</dt>
              <dd>
                {o.supersededBy === undefined ? <span className="muted">— current row</span> : (
                  <button type="button" className="ztax-table-action-btn" onClick={() => { onSelect(o.supersededBy ?? id) }}>
                    <Id value={o.supersededBy} />
                  </button>
                )}
              </dd>
            </dl>

            <h3 className="ztax-card-title" style={{ marginTop: 16 }}>Move to a status</h3>
            {o.supersededBy !== undefined ? (
              <p className="ztax-note">This row is history. Open the row that superseded it to move the obligation.</p>
            ) : canMove ? (
              <TransitionForm
                obligation={o}
                onMoved={onMoved}
                onStale={() => {
                  reload()
                  onStale()
                }}
              />
            ) : (
              <p className="ztax-note">Moving an obligation needs the OPERATOR role.</p>
            )}
          </Card>
        </Columns>
      )}
    </Loaded>
  )
}

const TransitionForm = ({ obligation, onMoved, onStale }: {
  obligation: Obligation
  onMoved: (next: Obligation) => void
  onStale: () => void
}) => {
  const [to, setTo] = useState<Target>(TARGETS.find((t) => t !== obligation.status) ?? 'OPEN')
  const action = useAction<Obligation>()

  const submit = (e: FormEvent) => {
    e.preventDefault()
    // A retry is safe without an idempotency key: a second attempt names a
    // superseded row and is refused.
    void action.run(() => ztax.transitionObligation(obligation.id, { to })).then((next) => {
      if (next !== null) {
        onMoved(next)
      }
    })
  }

  return (
    <form className="ztax-form" onSubmit={submit}>
      <Field label="New status" help="FILED, ACCEPTED, PAID and the rest are set by submission and payment, never by hand.">
        <select
          value={to}
          onChange={(e) => {
            const v = e.target.value
            if (isTarget(v)) {
              setTo(v)
            }
          }}
        >
          {TARGETS.map((t) => (
            <option key={t} value={t} disabled={t === obligation.status}>
              {t.replaceAll('_', ' ')}
            </option>
          ))}
        </select>
      </Field>
      <div className="ztax-form-actions">
        <Submit busy={action.busy}>Move to {to.replaceAll('_', ' ')}</Submit>
      </div>
      {action.error === null ? null : isConflict(action.error) ? (
        <div style={{ gridColumn: '1 / -1' }}>
          <p className="ztax-error-note" role="alert">
            Someone changed this obligation since you read it — another user, or a commit that changed its figures. Nothing
            was moved. Read it again and decide on the current figures.
            <span className="mono muted"> {errorText(action.error)}</span>
          </p>
          <button type="button" className="ztax-btn-quiet" onClick={onStale} style={{ marginTop: 8 }}>
            Read again
          </button>
        </div>
      ) : (
        <div style={{ gridColumn: '1 / -1' }}>
          <ErrorNote error={action.error} />
        </div>
      )}
    </form>
  )
}
