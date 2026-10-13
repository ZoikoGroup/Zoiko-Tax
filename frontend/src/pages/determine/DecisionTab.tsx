import { useState, type FormEvent } from 'react'

import type { S } from '@zoikotax/sdk'

import { ztax } from '../../platform/client'
import { instant } from '../../dashboard/format'
import {
  Amount, Card, Columns, Field, Id, Loaded, Outcome, Raw, Status, Submit, useAction, useLoad,
} from '../../ui/kit'
import { DecisionFacts } from './QuoteCommitTab'
import { CURRENCY, EmittedTable, Invalid, isDecimal, isNotFound, KeyNote, KV, useIdempotencyKey } from './shared'

/** Look up a decision by id. `id` is controlled so another tab can open one. */
export const DecisionTab = ({ id, onOpen, onOpenRefund }: {
  id: string
  onOpen: (id: string) => void
  onOpenRefund: (id: string) => void
}) => {
  const [draft, setDraft] = useState(id)
  const submit = (e: FormEvent) => {
    e.preventDefault()
    onOpen(draft.trim())
  }
  return (
    <>
      <Card title="Look up a decision">
        <form className="ztax-form" onSubmit={submit} aria-label="Look up a decision">
          <Field label="Decision id">
            <input className="mono" value={draft} onChange={(e) => { setDraft(e.target.value) }} autoComplete="off" />
          </Field>
          <div className="ztax-form-actions">
            <button type="submit" className="ztax-btn-copper" disabled={draft.trim() === ''}>Look up</button>
          </div>
        </form>
      </Card>
      {id === '' ? null : <DecisionView key={id} id={id} onOpenRefund={onOpenRefund} />}
    </>
  )
}

const DecisionView = ({ id, onOpenRefund }: { id: string; onOpenRefund: (id: string) => void }) => {
  const { load, reload } = useLoad((signal) => ztax.getDecision(id, signal), [id])
  return (
    <Loaded load={load}>
      {(d) => (
        <>
          <Card
            title={`Decision ${d.businessKey}`}
            aside={<button type="button" className="ztax-btn-quiet" onClick={reload}>Refresh</button>}
          >
            <DecisionFacts decision={d} />
            <EmittedTable emitted={d.emitted} caption="Emitted, per component" />
            <Raw value={d} label="Full decision" />
          </Card>
          <Columns>
            <ReplayCard id={id} />
            <InclusionCard id={id} />
            <RetentionCard id={id} />
          </Columns>
          <JournalsCard id={id} />
          <RefundForm decision={d} onOpenRefund={onOpenRefund} />
        </>
      )}
    </Loaded>
  )
}

const ReplayCard = ({ id }: { id: string }) => {
  const replay = useAction<S<'ReplayReport'>>()
  return (
    <Card
      title="Replay"
      aside={
        <button type="button" className="ztax-btn-quiet" disabled={replay.busy} onClick={() => { void replay.run(() => ztax.replayDecision(id)) }}>
          {replay.busy ? 'Replaying…' : 'Replay'}
        </button>
      }
    >
      <p className="muted ztax-note">
        Re-evaluates the recorded envelope under the bundle that made it and compares the result byte for byte. Nothing
        is recorded.
      </p>
      <Outcome error={replay.error} done={null} />
      {replay.done === null ? null : (
        <KV
          rows={[
            ['Verdict', <Status value={replay.done.verdict} />],
            ['Envelope', <span className="mono">{replay.done.envelopeDigest}</span>],
            ['Recorded result', <span className="mono">{replay.done.recordedResult}</span>],
            ['Replayed result', <span className="mono">{replay.done.replayedResult ?? '—'}</span>],
            ['First divergence', <span className="mono">{replay.done.divergence ?? 'none'}</span>],
          ]}
        />
      )}
    </Card>
  )
}

const InclusionCard = ({ id }: { id: string }) => {
  const proof = useAction<S<'InclusionProof'>>()
  const notSealed = isNotFound(proof.error)
  return (
    <Card
      title="Inclusion proof"
      aside={
        <button type="button" className="ztax-btn-quiet" disabled={proof.busy} onClick={() => { void proof.run(() => ztax.getDecisionInclusion(id)) }}>
          {proof.busy ? 'Reading…' : 'Get proof'}
        </button>
      }
    >
      <p className="muted ztax-note">
        The audit path from this decision's leaf to the signed root of the seal covering it.
      </p>
      {notSealed ? (
        <p className="ztax-empty-note" role="status">
          No proof yet: the period this decision was recorded in has not been sealed. A proof exists once a seal covers it.
        </p>
      ) : (
        <Outcome error={proof.error} done={null} />
      )}
      {proof.done === null ? null : (
        <>
          <KV
            rows={[
              ['Seal', <span className="mono">{proof.done.sealId}</span>],
              ['Merkle root', <span className="mono">{proof.done.merkleRoot}</span>],
              ['Leaf', `${proof.done.leafIndex} of ${proof.done.leafCount}`],
              ['Leaf digest', <span className="mono">{proof.done.leafDigest}</span>],
              ['Path length', `${proof.done.auditPath.length}`],
            ]}
          />
          <Raw value={proof.done} label="Full proof" />
        </>
      )}
    </Card>
  )
}

const RetentionCard = ({ id }: { id: string }) => {
  const verdict = useAction<S<'RetentionVerdict'>>()
  return (
    <Card
      title="Retention verdict"
      aside={
        <button type="button" className="ztax-btn-quiet" disabled={verdict.busy} onClick={() => { void verdict.run(() => ztax.getDecisionRetention(id)) }}>
          {verdict.busy ? 'Reading…' : 'Ask'}
        </button>
      }
    >
      <p className="muted ztax-note">
        Whether this decision's evidence may be disposed of now. Asking changes nothing. Needs ADMIN or AUDITOR.
      </p>
      <Outcome error={verdict.error} done={null} />
      {verdict.done === null ? null : (
        <KV
          rows={[
            ['Outcome', <Status value={verdict.done.outcome} />],
            ['Record class', verdict.done.recordClass],
            ['Country', verdict.done.country ?? '—'],
            ['Policy', verdict.done.policy === undefined ? '—' : `${verdict.done.policy.id} v${verdict.done.policy.version}`],
            ['Retain until', verdict.done.retainUntil === undefined ? '—' : instant(verdict.done.retainUntil)],
            ['Holds', verdict.done.holds === undefined || verdict.done.holds.length === 0 ? 'none' : verdict.done.holds.join(', ')],
            ['Detail', verdict.done.detail ?? '—'],
            ['Evaluated', instant(verdict.done.evaluatedAt)],
          ]}
        />
      )}
    </Card>
  )
}

const JournalsCard = ({ id }: { id: string }) => {
  const journals = useAction<S<'JournalList'>>()
  return (
    <Card
      title="Subledger journals"
      aside={
        <button type="button" className="ztax-btn-quiet" disabled={journals.busy} onClick={() => { void journals.run(() => ztax.listDecisionJournals(id)) }}>
          {journals.busy ? 'Reading…' : 'Show journals'}
        </button>
      }
    >
      <Outcome error={journals.error} done={null} />
      {journals.done === null ? (
        <p className="ztax-empty-note">The journals this decision posted, and any reversals a correction posted for it.</p>
      ) : journals.done.journals.length === 0 ? (
        <p className="ztax-empty-note">This decision posted no journals.</p>
      ) : (
        journals.done.journals.map((j) => (
          <table className="table" key={j.id}>
            <caption className="table__caption">
              {j.type} · {j.sourceKind} · period {j.legalPeriod} · posted {instant(j.postingDate)} · <Id value={j.id} />
              {j.reversalOf === undefined ? null : <> · reverses <Id value={j.reversalOf} /></>}
            </caption>
            <thead>
              <tr>
                <th scope="col">Account</th>
                <th scope="col">Side</th>
                <th scope="col">Amount</th>
              </tr>
            </thead>
            <tbody>
              {j.lines.map((l, i) => (
                <tr key={i}>
                  <td className="mono">{l.account}</td>
                  <td>{l.side}</td>
                  <td><Amount amount={l.amount} currency={l.currency} /></td>
                </tr>
              ))}
            </tbody>
          </table>
        ))
      )}
    </Card>
  )
}

const RefundForm = ({ decision, onOpenRefund }: { decision: S<'Decision'>; onOpenRefund: (id: string) => void }) => {
  const firstCurrency = Object.values(decision.emitted).find((v) => v.type === 'MONEY')?.currency ?? 'EUR'
  const [amount, setAmount] = useState('')
  const [currency, setCurrency] = useState(firstCurrency)
  const [paymentReference, setPaymentReference] = useState('')
  const [reason, setReason] = useState('')
  const [problem, setProblem] = useState<string | null>(null)
  const refund = useAction<S<'Refund'>>()
  const keys = useIdempotencyKey()

  const submit = (e: FormEvent) => {
    e.preventDefault()
    const p = !isDecimal(amount.trim())
      ? 'The amount must be a decimal such as 10.50.'
      : !CURRENCY.test(currency.trim())
        ? 'The currency must be a three-letter code.'
        : paymentReference.trim() === ''
          ? 'The payment reference is required.'
          : null
    setProblem(p)
    if (p !== null) {
      return
    }
    const body: S<'RefundRequest'> = {
      decisionId: decision.id,
      amount: { amount: amount.trim(), currency: currency.trim() },
      paymentReference: paymentReference.trim(),
      ...(reason.trim() === '' ? {} : { reason: reason.trim() }),
    }
    const key = keys.keyFor(body)
    void refund.run(() => ztax.refundTransaction(key, body)).then((r) => { if (r !== null) keys.succeeded() })
  }

  return (
    <Card title="Refund tax this decision charged">
      <p className="muted ztax-note">
        A refund is not a decision and not a credit: nothing is re-determined. It is recorded as REQUESTED and posts
        nothing until the payment provider reports SUCCEEDED. ZoikoTax moves no money. The amount may be at most the tax
        this decision posted in that currency, less earlier refunds that have not failed.
      </p>
      <form className="ztax-form" onSubmit={submit} aria-label="Refund">
        <Field label="Amount">
          <input inputMode="decimal" value={amount} onChange={(e) => { setAmount(e.target.value) }} placeholder="10.50" autoComplete="off" />
        </Field>
        <Field label="Currency">
          <input value={currency} maxLength={3} onChange={(e) => { setCurrency(e.target.value.toUpperCase()) }} />
        </Field>
        <Field label="Payment reference" help="The provider's charge reference, e.g. psp:ch_3Nf0a1.">
          <input value={paymentReference} onChange={(e) => { setPaymentReference(e.target.value) }} autoComplete="off" />
        </Field>
        <Field label="Reason (optional)">
          <input value={reason} onChange={(e) => { setReason(e.target.value) }} />
        </Field>
        <div className="ztax-form-actions">
          <Submit busy={refund.busy}>Request refund</Submit>
        </div>
      </form>
      <Invalid message={problem} />
      <KeyNote value={keys.current} />
      <Outcome
        error={refund.error}
        done={
          refund.done === null ? null : (
            <div className="ztax-ok-note">
              Refund <span className="mono">{refund.done.id}</span> recorded as <Status value={refund.done.status} /> for{' '}
              <Amount amount={refund.done.amount.amount} currency={refund.done.amount.currency} />.{' '}
              <button type="button" className="ztax-btn-quiet" onClick={() => { if (refund.done !== null) onOpenRefund(refund.done.id) }}>
                Open in Refunds tab
              </button>
            </div>
          )
        }
      />
    </Card>
  )
}
