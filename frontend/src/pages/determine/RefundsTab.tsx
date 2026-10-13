import { useState, type FormEvent } from 'react'

import type { S } from '@zoikotax/sdk'

import { ztax } from '../../platform/client'
import { instant } from '../../dashboard/format'
import { Amount, Card, Field, Id, Loaded, Outcome, Raw, Status, Submit, useAction, useLoad } from '../../ui/kit'
import { KV } from './shared'

const OUTCOMES: readonly { value: S<'RefundOutcome'>; label: string }[] = [
  { value: 'ACCEPTED', label: 'ACCEPTED — the provider took the request (refund becomes PENDING)' },
  { value: 'SUCCEEDED', label: 'SUCCEEDED — the money was returned (COMPLETED; posts the REFUND journal)' },
  { value: 'DECLINED', label: 'DECLINED — the provider refused (FAILED; its tax may be refunded again)' },
  { value: 'TIMED_OUT', label: 'TIMED_OUT — no answer in time (UNCERTAIN)' },
  { value: 'UNKNOWN', label: 'UNKNOWN — the answer did not say (UNCERTAIN)' },
]

export const RefundsTab = ({ id, onOpen, onOpenDecision }: {
  id: string
  onOpen: (id: string) => void
  onOpenDecision: (id: string) => void
}) => {
  const [draft, setDraft] = useState(id)
  const submit = (e: FormEvent) => {
    e.preventDefault()
    onOpen(draft.trim())
  }
  return (
    <>
      <Card title="Look up a refund">
        <form className="ztax-form" onSubmit={submit} aria-label="Look up a refund">
          <Field label="Refund id">
            <input className="mono" value={draft} onChange={(e) => { setDraft(e.target.value) }} autoComplete="off" />
          </Field>
          <div className="ztax-form-actions">
            <button type="submit" className="ztax-btn-copper" disabled={draft.trim() === ''}>Look up</button>
          </div>
        </form>
        <p className="muted ztax-note">
          Refunds are requested from a decision, in the Decision tab. There is no list of refunds; open one by its id.
        </p>
      </Card>
      {id === '' ? null : <RefundView key={id} id={id} onOpenDecision={onOpenDecision} />}
    </>
  )
}

const RefundView = ({ id, onOpenDecision }: { id: string; onOpenDecision: (id: string) => void }) => {
  const { load, reload } = useLoad((signal) => ztax.getRefund(id, signal), [id])
  return (
    <Loaded load={load}>
      {(r) => (
        <>
          <Card
            title="Refund"
            aside={<button type="button" className="ztax-btn-quiet" onClick={reload}>Refresh</button>}
          >
            <KV
              rows={[
                ['Refund', <span className="mono">{r.id}</span>],
                ['Status', <Status value={r.status} />],
                ['Amount', <Amount amount={r.amount.amount} currency={r.amount.currency} />],
                [
                  'Decision',
                  <button type="button" className="ztax-btn-quiet mono" onClick={() => { onOpenDecision(r.decisionId) }}>
                    {r.decisionId}
                  </button>,
                ],
                ['Payment reference', <span className="mono">{r.paymentReference}</span>],
                ['Reason', r.reason ?? '—'],
                ['Requested', instant(r.requestedAt)],
                ['Requested by', <Id value={r.requestedBy} />],
              ]}
            />
            {r.status === 'UNCERTAIN' ? (
              <p className="ztax-error-note" role="status">
                UNCERTAIN: the provider's answer did not say whether the money moved. It is neither paid nor failed; record
                the definite report when the provider gives one.
              </p>
            ) : null}
            <table className="table">
              <caption className="table__caption">History, oldest first</caption>
              <thead>
                <tr>
                  <th scope="col">#</th>
                  <th scope="col">Status</th>
                  <th scope="col">Provider outcome</th>
                  <th scope="col">External reference</th>
                  <th scope="col">Recorded</th>
                  <th scope="col">By</th>
                </tr>
              </thead>
              <tbody>
                {r.history.map((ev) => (
                  <tr key={ev.seq}>
                    <td>{ev.seq}</td>
                    <td><Status value={ev.status} /></td>
                    <td>{ev.outcome ?? '—'}</td>
                    <td className="mono">{ev.externalReference ?? '—'}</td>
                    <td>{instant(ev.recordedAt)}</td>
                    <td><Id value={ev.recordedBy} /></td>
                  </tr>
                ))}
              </tbody>
            </table>
            <Raw value={r} label="Full refund" />
          </Card>
          <ReportForm refund={r} onReported={reload} />
        </>
      )}
    </Loaded>
  )
}

const ReportForm = ({ refund, onReported }: { refund: S<'Refund'>; onReported: () => void }) => {
  const [outcome, setOutcome] = useState<S<'RefundOutcome'>>('SUCCEEDED')
  const [externalReference, setExternalReference] = useState('')
  const report = useAction<S<'Refund'>>()
  const final = refund.status === 'COMPLETED' || refund.status === 'FAILED'

  const submit = (e: FormEvent) => {
    e.preventDefault()
    const body: S<'RefundReportRequest'> = {
      outcome,
      ...(externalReference.trim() === '' ? {} : { externalReference: externalReference.trim() }),
    }
    // Safe to retry without a key: the same report again changes nothing.
    void report.run(() => ztax.reportRefund(refund.id, body)).then((r) => { if (r !== null) onReported() })
  }

  return (
    <Card title="Record what the payment provider reported">
      {final ? (
        <p className="ztax-empty-note">This refund is {refund.status}; nothing leaves COMPLETED or FAILED.</p>
      ) : null}
      <form className="ztax-form" onSubmit={submit} aria-label="Report a provider outcome">
        <Field label="Provider outcome">
          <select value={outcome} onChange={(e) => { setOutcome(e.target.value as S<'RefundOutcome'>) }}>
            {OUTCOMES.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
          </select>
        </Field>
        <Field label="External reference (optional)" help="The provider's refund reference, e.g. psp:re_9Kd2.">
          <input value={externalReference} onChange={(e) => { setExternalReference(e.target.value) }} autoComplete="off" />
        </Field>
        <div className="ztax-form-actions">
          <Submit busy={report.busy}>Record report</Submit>
        </div>
      </form>
      <Outcome
        error={report.error}
        done={report.done === null ? null : (
          <p className="ztax-ok-note">Recorded. The refund is now <Status value={report.done.status} />.</p>
        )}
      />
    </Card>
  )
}
