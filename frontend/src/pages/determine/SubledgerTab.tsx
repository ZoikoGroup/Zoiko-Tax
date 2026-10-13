import { useState, type FormEvent } from 'react'

import type { S } from '@zoikotax/sdk'

import { ztax } from '../../platform/client'
import { instant, periodLabel } from '../../dashboard/format'
import { Amount, Card, Columns, Field, Id, Loaded, Outcome, Raw, Status, Submit, useAction, useLoad } from '../../ui/kit'
import { Invalid, KV } from './shared'

type Target = S<'PeriodTransitionRequest'>['to']

const TARGETS: readonly { value: Target; label: string }[] = [
  { value: 'SOFT_CLOSE', label: 'SOFT_CLOSE — only adjustment and rounding journals post' },
  { value: 'OPEN', label: 'OPEN — back from a soft close' },
  { value: 'HARD_CLOSE', label: 'HARD_CLOSE — writes a close manifest; nothing posts by the normal path' },
  { value: 'AMENDMENT_ACTIVE', label: 'AMENDMENT_ACTIVE — an amendment window on a hard-closed period' },
  { value: 'SEALED', label: 'SEALED — final; the period can never move again' },
]

export const SubledgerTab = ({ period }: { period: string }) => (
  <>
    <BalancesCard />
    <PeriodPanel key={period} period={period} />
  </>
)

const BalancesCard = () => {
  const { load, reload } = useLoad((signal) => ztax.getSubledgerBalances(signal), [])
  return (
    <Card
      title="Control balances"
      aside={<button type="button" className="ztax-btn-quiet" onClick={reload}>Refresh</button>}
    >
      <p className="muted ztax-note">
        The Tax Control Subledger of the tenant's default legal entity — not the general ledger. Debits and credits are
        summed from immutable journal lines and reported apart, never netted.
      </p>
      <Loaded load={load}>
        {(b) =>
          b.balances.length === 0 ? (
            <p className="ztax-empty-note">Nothing has posted yet.</p>
          ) : (
            <table className="table">
              <caption className="table__caption">Legal entity <span className="mono">{b.legalEntityId}</span></caption>
              <thead>
                <tr>
                  <th scope="col">Account</th>
                  <th scope="col">Currency</th>
                  <th scope="col">Debits</th>
                  <th scope="col">Credits</th>
                </tr>
              </thead>
              <tbody>
                {b.balances.map((row) => (
                  <tr key={`${row.account}-${row.currency}`}>
                    <td className="mono">{row.account}</td>
                    <td>{row.currency}</td>
                    <td><Amount amount={row.debits} currency={row.currency} /></td>
                    <td><Amount amount={row.credits} currency={row.currency} /></td>
                  </tr>
                ))}
              </tbody>
            </table>
          )
        }
      </Loaded>
    </Card>
  )
}

const PeriodPanel = ({ period }: { period: string }) => {
  const { load, reload } = useLoad((signal) => ztax.getSubledgerPeriod(period, signal), [period])
  return (
    <>
      <Card
        title={`Legal period ${periodLabel(period)}`}
        aside={<button type="button" className="ztax-btn-quiet" onClick={reload}>Refresh</button>}
      >
        <Loaded load={load}>
          {(p) => (
            <>
              <KV
                rows={[
                  ['Period', <span className="mono">{p.period}</span>],
                  ['State', <Status value={p.state} />],
                  [
                    'Intact',
                    p.intact === undefined
                      ? 'not yet hard-closed'
                      : p.intact
                        ? 'yes — journals and balances are exactly what the latest manifest sealed'
                        : 'NO — what is posted today differs from the latest manifest',
                  ],
                  ['Manifest digest', <span className="mono">{p.manifest?.digest ?? '—'}</span>],
                  ['Legal entity', <span className="mono">{p.legalEntityId}</span>],
                ]}
              />
              {p.history.length === 0 ? (
                <p className="ztax-empty-note">No history: a period that has never moved is OPEN.</p>
              ) : (
                <table className="table">
                  <caption className="table__caption">History, oldest first</caption>
                  <thead>
                    <tr>
                      <th scope="col">#</th>
                      <th scope="col">State</th>
                      <th scope="col">Reason</th>
                      <th scope="col">Recorded</th>
                      <th scope="col">By</th>
                      <th scope="col">Approved by</th>
                      <th scope="col">Reopen request</th>
                      <th scope="col">Manifest</th>
                    </tr>
                  </thead>
                  <tbody>
                    {p.history.map((ev) => (
                      <tr key={ev.seq}>
                        <td>{ev.seq}</td>
                        <td><Status value={ev.state} /></td>
                        <td>{ev.reason ?? '—'}</td>
                        <td>{instant(ev.recordedAt)}</td>
                        <td><Id value={ev.recordedBy} /></td>
                        <td><Id value={ev.approvedBy} /></td>
                        <td><Id value={ev.requestId} /></td>
                        <td><Id value={ev.manifestDigest} /></td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
              <Raw value={p} label="Full period" />
            </>
          )}
        </Loaded>
      </Card>
      <Columns>
        <TransitionForm period={period} onDone={reload} />
        <ReopenForms period={period} onDone={reload} />
      </Columns>
    </>
  )
}

const TransitionForm = ({ period, onDone }: { period: string; onDone: () => void }) => {
  const [to, setTo] = useState<Target>('SOFT_CLOSE')
  const [reason, setReason] = useState('')
  const [confirmed, setConfirmed] = useState(false)
  const [problem, setProblem] = useState<string | null>(null)
  const move = useAction<S<'SubledgerPeriod'>>()
  const serious = to === 'HARD_CLOSE' || to === 'SEALED'

  const submit = (e: FormEvent) => {
    e.preventDefault()
    const p = serious && !confirmed ? `Confirm that you mean to move ${period} to ${to}.` : null
    setProblem(p)
    if (p !== null) {
      return
    }
    const body: S<'PeriodTransitionRequest'> = { to, ...(reason.trim() === '' ? {} : { reason: reason.trim() }) }
    void move.run(() => ztax.transitionSubledgerPeriod(period, body)).then((r) => {
      if (r !== null) {
        setConfirmed(false)
        onDone()
      }
    })
  }

  return (
    <Card title="Move the period">
      <p className="muted ztax-note">
        OPEN ⇄ SOFT_CLOSE → HARD_CLOSE → AMENDMENT_ACTIVE → HARD_CLOSE, and HARD_CLOSE → SEALED, which is final.
        REOPENED is not a transition: it takes a reopen request and a second person's approval.
      </p>
      <form className="ztax-form" onSubmit={submit} aria-label={`Move period ${period}`}>
        <Field label="To">
          <select value={to} onChange={(e) => { setTo(e.target.value as Target); setConfirmed(false) }}>
            {TARGETS.map((t) => <option key={t.value} value={t.value}>{t.label}</option>)}
          </select>
        </Field>
        <Field label="Reason (optional)">
          <input value={reason} onChange={(e) => { setReason(e.target.value) }} placeholder="month end" />
        </Field>
        {serious ? (
          <label className="ztax-field ztax-check" htmlFor="period-confirm">
            <input id="period-confirm" type="checkbox" checked={confirmed} onChange={(e) => { setConfirmed(e.target.checked) }} />
            <span>I mean to move {period} to {to}{to === 'SEALED' ? ', which cannot be undone' : ''}.</span>
          </label>
        ) : null}
        <div className="ztax-form-actions">
          <Submit busy={move.busy}>Move to {to}</Submit>
        </div>
      </form>
      <Invalid message={problem} />
      <Outcome
        error={move.error}
        done={move.done === null ? null : <p className="ztax-ok-note">The period is now <Status value={move.done.state} />.</p>}
      />
    </Card>
  )
}

const ReopenForms = ({ period, onDone }: { period: string; onDone: () => void }) => {
  const [reason, setReason] = useState('')
  const [requestId, setRequestId] = useState('')
  const request = useAction<S<'PeriodReopenRequest'>>()
  const approve = useAction<S<'SubledgerPeriod'>>()

  const submitRequest = (e: FormEvent) => {
    e.preventDefault()
    if (reason.trim() === '') {
      return
    }
    void request.run(() => ztax.requestPeriodReopen(period, { reason: reason.trim() }))
  }
  const submitApproval = (e: FormEvent) => {
    e.preventDefault()
    const id = requestId.trim()
    if (id === '') {
      return
    }
    void approve.run(() => ztax.approvePeriodReopen(period, id)).then((r) => { if (r !== null) onDone() })
  }

  return (
    <Card title="Reopen a hard-closed period">
      <p className="muted ztax-note">
        Reopening takes two people. An OPERATOR records a request with its reason; an ADMIN who is <strong>not</strong>{' '}
        the requester approves it. Every earlier close manifest stays, and the next hard close writes a new one. A sealed
        period cannot be reopened.
      </p>
      <form className="ztax-form" onSubmit={submitRequest} aria-label="Request a reopening">
        <Field label="Reason">
          <input value={reason} onChange={(e) => { setReason(e.target.value) }} required />
        </Field>
        <div className="ztax-form-actions">
          <Submit busy={request.busy}>Request reopening</Submit>
        </div>
      </form>
      <Outcome
        error={request.error}
        done={request.done === null ? null : (
          <p className="ztax-ok-note">
            Request <span className="mono">{request.done.id}</span> recorded {instant(request.done.requestedAt)}. Someone
            else must approve it.{' '}
            <button type="button" className="ztax-btn-quiet" onClick={() => { if (request.done !== null) setRequestId(request.done.id) }}>
              Use this id below
            </button>
          </p>
        )}
      />
      <form className="ztax-form" onSubmit={submitApproval} aria-label="Approve a reopening">
        <Field label="Reopen request id" help="Approve someone else's request. Your own is refused.">
          <input className="mono" value={requestId} onChange={(e) => { setRequestId(e.target.value) }} required autoComplete="off" />
        </Field>
        <div className="ztax-form-actions">
          <Submit busy={approve.busy}>Approve reopening</Submit>
        </div>
      </form>
      <Outcome
        error={approve.error}
        done={approve.done === null ? null : <p className="ztax-ok-note">The period is now <Status value={approve.done.state} />.</p>}
      />
    </Card>
  )
}
