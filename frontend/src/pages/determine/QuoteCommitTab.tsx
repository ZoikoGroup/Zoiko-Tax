import { useState, type FormEvent } from 'react'

import type { S } from '@zoikotax/sdk'

import { ztax } from '../../platform/client'
import { instant } from '../../dashboard/format'
import { Card, Columns, Field, Id, Outcome, Raw, Status, Submit, useAction } from '../../ui/kit'
import { commitBody, emptyLine, LineFields, lineProblem, quoteBody, type LineDraft } from './line'
import { EmittedTable, Invalid, KeyNote, KV, useIdempotencyKey } from './shared'

/** The header facts of a recorded decision. */
export const DecisionFacts = ({ decision }: { decision: S<'Decision'> }) => (
  <KV
    rows={[
      ['Decision', <span key="id" className="mono">{decision.id}</span>],
      ['Business key', <span key="bk" className="mono">{decision.businessKey}</span>],
      ['Supersedes', decision.supersedes === undefined ? '—' : <span key="s" className="mono">{decision.supersedes}</span>],
      ['Outcome', <Status key="o" value={decision.outcome} />],
      ['Reason', <span key="r" className="mono">{decision.reasonCode}</span>],
      ['Authoritative', decision.authoritative ? 'yes — may be filed' : 'no — advisory, must not be filed'],
      ['Event time', instant(decision.eventTime)],
      ['Recorded', instant(decision.recordedAt)],
      ['Bundle', <span key="b" className="mono">{decision.bundle.bundleId} · IR v{decision.bundle.irVersion}</span>],
      ['Bundle digest', <span key="bd" className="mono">{decision.bundle.digest}</span>],
      ['Input digest', <span key="di" className="mono">{decision.digests.input}</span>],
      ['Envelope digest', <span key="de" className="mono">{decision.digests.envelope}</span>],
      ['Result digest', <span key="dr" className="mono">{decision.digests.result}</span>],
    ]}
  />
)

export const QuoteCommitTab = ({ onOpenDecision }: { onOpenDecision: (id: string) => void }) => {
  const [line, setLine] = useState<LineDraft>(() => emptyLine())
  const [supersedes, setSupersedes] = useState('')
  const [problem, setProblem] = useState<string | null>(null)
  const quote = useAction<S<'Quote'>>()
  const commit = useAction<S<'Decision'>>()
  const commitKey = useIdempotencyKey()
  const adjustKey = useIdempotencyKey()
  const adjusting = supersedes.trim() !== ''

  const onQuote = () => {
    const p = lineProblem(line, false)
    setProblem(p)
    if (p !== null) {
      return
    }
    void quote.run(() => ztax.createQuote(quoteBody(line)))
  }

  const onCommit = (e: FormEvent) => {
    e.preventDefault()
    const p = lineProblem(line, true)
    setProblem(p)
    if (p !== null) {
      return
    }
    // :commit and :adjust are separate key scopes (ADR-0013 §2.2), so each
    // keeps its own key.
    const body = commitBody(line, supersedes)
    const keys = adjusting ? adjustKey : commitKey
    const key = keys.keyFor(body)
    void commit
      .run(() => (adjusting ? ztax.adjustTransaction(key, body) : ztax.commitTransaction(key, body)))
      .then((d) => { if (d !== null) keys.succeeded() })
  }

  return (
    <>
      <Card title="One line of the active worked pack">
        <p className="muted ztax-note">
          A quote evaluates the line and records nothing. A commit records an immutable decision and posts it to the Tax
          Control Subledger. Accumulators are read by the cell itself and are not sent.
        </p>
        <form className="ztax-form" onSubmit={onCommit} aria-label="Quote or commit a line">
          <LineFields line={line} onChange={setLine} idPrefix="qc" />
          <Field
            label="Supersedes (optional)"
            help="A decision id. When set, Commit becomes an adjustment: a correction under the content that made that decision."
          >
            <input value={supersedes} onChange={(e) => { setSupersedes(e.target.value) }} autoComplete="off" className="mono" />
          </Field>
          <div className="ztax-form-actions">
            <button type="button" className="ztax-btn-quiet" onClick={onQuote} disabled={quote.busy}>
              {quote.busy ? 'Quoting…' : 'Quote'}
            </button>
            <Submit busy={commit.busy}>{adjusting ? 'Commit adjustment' : 'Commit'}</Submit>
          </div>
        </form>
        <Invalid message={problem} />
        <KeyNote value={adjusting ? adjustKey.current : commitKey.current} />
      </Card>

      <Columns>
        <Card title="Quote" aside={quote.done === null ? undefined : <Status value={quote.done.outcome} />}>
          <Outcome error={quote.error} done={null} />
          {quote.done === null ? (
            <p className="ztax-empty-note">No quote yet.</p>
          ) : (
            <>
              <p className="ztax-ok-note" style={{ margin: 0 }}>
                Advisory estimate ({quote.done.reasonCode}) — a quote is never a decision and must not be filed or
                invoiced from.
              </p>
              <EmittedTable emitted={quote.done.emitted} caption={`Quoted ${instant(quote.done.quotedAt)} against ${quote.done.bundle.bundleId}`} />
              <Raw value={quote.done} label="Full quote" />
            </>
          )}
        </Card>

        <Card title="Recorded decision" aside={commit.done === null ? undefined : <Id value={commit.done.id} />}>
          <Outcome error={commit.error} done={null} />
          {commit.done === null ? (
            <p className="ztax-empty-note">Nothing committed from this form yet.</p>
          ) : (
            <>
              <DecisionFacts decision={commit.done} />
              <EmittedTable emitted={commit.done.emitted} caption="Emitted by the decision" />
              <div className="ztax-form-actions">
                <button
                  type="button"
                  className="ztax-btn-copper"
                  onClick={() => { if (commit.done !== null) onOpenDecision(commit.done.id) }}
                >
                  Open in Decision tab
                </button>
                <button
                  type="button"
                  className="ztax-btn-quiet"
                  onClick={() => { if (commit.done !== null) setSupersedes(commit.done.id) }}
                >
                  Correct this decision
                </button>
              </div>
              <Raw value={commit.done} label="Full decision" />
            </>
          )}
        </Card>
      </Columns>
    </>
  )
}
