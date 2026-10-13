import { useState, type FormEvent } from 'react'

import { ZoikoTaxError, type S } from '@zoikotax/sdk'

import { civilDate, instant } from '../dashboard/format'
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
import type { PageProps } from './types'
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

export const ObligationsPage = ({ session }: PageProps) => {
  const [status, setStatus] = useState<Effective | 'ALL'>('ALL')
  const [selected, setSelected] = useState<string | null>(null)
  const { load, reload } = useLoad(
    (signal) => ztax.listObligations(status === 'ALL' ? { limit: 500 } : { status, limit: 500 }, signal),
    [status],
  )
  const canMove = session.user.roles.includes('OPERATOR')

  return (
    <div className="ztax-page">
      <PageHeader
        title="Obligations"
        subtitle="Periodic duties owed to each authority, earliest due first. Status is as of today in the authority's calendar."
        actions={
          <button type="button" className="ztax-btn-quiet" onClick={reload}>
            Refresh
          </button>
        }
      />

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
                      <tr key={o.id} className={o.id === selected ? 'ztax-row-selected' : undefined}>
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
    </div>
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
