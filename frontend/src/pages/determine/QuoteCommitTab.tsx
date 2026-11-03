import { useState, type FormEvent } from 'react'

import type { S } from '@zoikotax/sdk'

import { ztax } from '../../platform/client'
import { instant } from '../../dashboard/format'
import { Card, Columns, Field, Id, Loaded, Outcome, Raw, Status, Submit, useAction, useLoad } from '../../ui/kit'
import { coverageInstant, today } from '../locations/coverage'
import {
  DeterminationCard, emptyLocation, LocationFields, locationDocument, locationEventTime, locationProblem,
  type LocationDraft,
} from '../locations/document'
import { commitBody, emptyLine, LineFields, lineProblem, quoteBody, type LineDraft } from './line'
import { EmittedTable, Invalid, KeyNote, KV, TabBar, useIdempotencyKey } from './shared'

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

/**
 * What a line is determined from: a telecom document at a place (content with
 * location-aware rules), or the named values of a rule-graph pack.
 */
type Mode = 'location' | 'pack'

const MODES: readonly { value: Mode; label: string }[] = [
  { value: 'location', label: 'At a location' },
  { value: 'pack', label: 'Worked pack inputs' },
]

export const QuoteCommitTab = ({ onOpenDecision }: { onOpenDecision: (id: string) => void }) => {
  const [mode, setMode] = useState<Mode>('location')
  const [line, setLine] = useState<LineDraft>(() => emptyLine())
  const [location, setLocation] = useState<LocationDraft>(emptyLocation)
  const [businessKey, setBusinessKey] = useState('')
  const [supersedes, setSupersedes] = useState('')
  const [problem, setProblem] = useState<string | null>(null)
  // Only the countries and subdivisions the active content covers are offered.
  const coverage = useLoad((signal) => ztax.getCoverage({ at: coverageInstant(today()) }, signal), [])
  const quote = useAction<S<'Quote'>>()
  const commit = useAction<S<'Decision'>>()
  const commitKey = useIdempotencyKey()
  const adjustKey = useIdempotencyKey()
  const adjusting = supersedes.trim() !== ''

  const problemOf = (needKey: boolean): string | null =>
    mode === 'pack' ? lineProblem(line, needKey)
      : needKey && businessKey.trim() === '' ? 'A business key is required to commit.'
        : locationProblem(location)

  const onQuote = () => {
    const p = problemOf(false)
    setProblem(p)
    if (p !== null) {
      return
    }
    const body: S<'QuoteRequest'> = mode === 'pack'
      ? quoteBody(line)
      : { eventTime: locationEventTime(location), document: locationDocument(location) }
    void quote.run(() => ztax.createQuote(body))
  }

  const onCommit = (e: FormEvent) => {
    e.preventDefault()
    const p = problemOf(true)
    setProblem(p)
    if (p !== null) {
      return
    }
    // :commit and :adjust are separate key scopes (ADR-0013 §2.2), so each
    // keeps its own key.
    const body = mode === 'pack' ? commitBody(line, supersedes) : locationCommitBody(location, businessKey, supersedes)
    const keys = adjusting ? adjustKey : commitKey
    const key = keys.keyFor(body)
    void commit
      .run(() => (adjusting ? ztax.adjustTransaction(key, body) : ztax.commitTransaction(key, body)))
      .then((d) => { if (d !== null) keys.succeeded() })
  }

  const supersedesField = (
    <Field
      label="Supersedes (optional)"
      help="A decision id. When set, Commit becomes an adjustment: a correction under the content that made that decision."
    >
      <input value={supersedes} onChange={(e) => { setSupersedes(e.target.value) }} autoComplete="off" className="mono" />
    </Field>
  )
  const actions = (
    <div className="ztax-form-actions">
      <button type="button" className="ztax-btn-quiet" onClick={onQuote} disabled={quote.busy}>
        {quote.busy ? 'Quoting…' : 'Quote'}
      </button>
      <Submit busy={commit.busy}>{adjusting ? 'Commit adjustment' : 'Commit'}</Submit>
    </div>
  )

  return (
    <>
      <Card title={mode === 'location' ? 'One telecom line at a location' : 'One line of the active worked pack'}>
        <TabBar label="Determine from" tabs={MODES} current={mode} onSelect={(m) => { setMode(m); setProblem(null) }} />
        <p className="muted ztax-note">
          {mode === 'location'
            ? 'The engine places the line from the seller, the customer and the evidence of where the service is used, then applies every component in force there.'
            : 'Named values for a rule-graph pack. Accumulators are read by the cell itself and are not sent.'}
          {' '}A quote evaluates the line and records nothing. A commit records an immutable decision and posts it to the
          Tax Control Subledger.
        </p>
        {mode === 'location' ? (
          <form className="ztax-form loc-form" onSubmit={onCommit} aria-label="Quote or commit a line at a location">
            <div className="loc-form-grid">
              <Field label="Business key" help="Your stable reference for the line, e.g. INV-0001/1.">
                <input value={businessKey} onChange={(e) => { setBusinessKey(e.target.value) }} autoComplete="off" />
              </Field>
              {supersedesField}
            </div>
            <Loaded load={coverage.load}>
              {(c) => <LocationFields coverage={c} draft={location} onChange={setLocation} />}
            </Loaded>
            {actions}
          </form>
        ) : (
          <form className="ztax-form" onSubmit={onCommit} aria-label="Quote or commit a line">
            <LineFields line={line} onChange={setLine} idPrefix="qc" />
            {supersedesField}
            {actions}
          </form>
        )}
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
              {quote.done.document === undefined ? (
                <EmittedTable emitted={quote.done.emitted} caption={`Quoted ${instant(quote.done.quotedAt)} against ${quote.done.bundle.bundleId}`} />
              ) : (
                <p className="muted" style={{ margin: 0 }}>
                  Quoted {instant(quote.done.quotedAt)} against {quote.done.bundle.bundleId}; the location result is below.
                </p>
              )}
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
              {commit.done.document === undefined ? (
                <EmittedTable emitted={commit.done.emitted} caption="Emitted by the decision" />
              ) : null}
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

      {quote.done?.document === undefined ? null : (
        <DeterminationCard determination={quote.done.document} title="Quoted at the location" />
      )}
      {commit.done?.document === undefined ? null : (
        <DeterminationCard determination={commit.done.document} title="Decided at the location" />
      )}
    </>
  )
}

/** The commit (or adjust) body for a location. Call only once the draft has no problem. */
function locationCommitBody(location: LocationDraft, businessKey: string, supersedes: string): S<'CommitRequest'> {
  const base: S<'CommitRequest'> = {
    businessKey: businessKey.trim(),
    eventTime: locationEventTime(location),
    document: locationDocument(location),
  }
  const s = supersedes.trim()
  return s === '' ? base : { ...base, supersedes: s }
}
