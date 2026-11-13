/**
 * ClassifyPanel — "Classification with approved-list check (demo)" panel.
 *
 * States: idle, busy, and one ending: a reply (proposed, set aside, blocked, not
 * configured, could not be interpreted, service problem) or a failure (input not
 * accepted, route missing, timeout, not reachable, service error, unreadable reply).
 *
 * It switches on `kind` from outcomes.ts and never on a wire string, and it holds
 * no sentence of its own: every text comes from outcomes.ts. Nothing the server
 * sends is displayed except the proposed code of a passed reply, the list version
 * and the evidence id. One request at a time is enforced by classifyRun.ts.
 *
 * DEMO ONLY. Synthetic data only. Never computes a real fiscal determination.
 */
import { useEffect, useRef, useState, type FormEvent } from 'react'

import { Id } from '../../ui/kit'
import { DESCRIPTION_MAX, SUBJECT_REF_MAX } from './api'
import { createClassifyRunner, type ClassifyEnding, type ClassifyRunner } from './classifyRun'
import type { ClassifyReply } from './model'
import {
  BUSY_LABEL,
  CONFIDENCE_NOTE,
  DATA_NOTE,
  EVIDENCE_LABEL,
  EVIDENCE_MISSING,
  LIST_NOTE_AFTER,
  LIST_NOTE_BEFORE,
  RETRY_LABEL,
  SUBMIT_LABEL,
  SYNTHETIC_BADGE,
  SYNTHETIC_ONLY_NOTE,
  viewOf,
  viewOfFailure,
  type ClassifyView,
} from './outcomes'

// ---------------------------------------------------------------------------
// Prefill text (synthetic demo data)
// ---------------------------------------------------------------------------

const PREFILL_SUBJECT_REF = 'sku:DEMO-PLAN-5G-UNLIMITED'
const PREFILL_DESCRIPTION =
  'Unlimited 5G mobile plan with 20 GB mobile hotspot and international roaming (synthetic demo data)'

type ClassifyState =
  | { readonly kind: 'idle' }
  | { readonly kind: 'busy' }
  | { readonly kind: 'ended'; readonly ending: ClassifyEnding }

// ---------------------------------------------------------------------------
// Result view
// ---------------------------------------------------------------------------

const toneOf = (view: ClassifyView): 'pass' | 'caution' | 'neutral' => {
  if (view.kind === 'proposed') return 'pass'
  if (view.kind === 'abstain' || view.kind === 'blocked') return 'caution'
  return 'neutral'
}

const SyntheticNote = ({ view, listVersion }: { view: ClassifyView; listVersion: string | null }) =>
  view.listChecked && listVersion !== null ? (
    <p className="rd-cl-body rd-muted">
      {LIST_NOTE_BEFORE} <span className="mono">{listVersion}</span>
      {LIST_NOTE_AFTER}
    </p>
  ) : (
    <p className="rd-cl-body rd-muted">{SYNTHETIC_ONLY_NOTE}</p>
  )

const EvidenceLine = ({ evidenceId }: { evidenceId: string | null }) => (
  <p className="rd-cl-body rd-muted">
    {evidenceId !== null ? (
      <>
        {EVIDENCE_LABEL} <Id value={evidenceId} />
      </>
    ) : (
      EVIDENCE_MISSING
    )}
  </p>
)

const ResultView = ({
  view,
  reply,
  onRetry,
}: {
  view: ClassifyView
  reply: ClassifyReply | null
  onRetry: () => void
}) => {
  const showsEvidence = reply !== null && (view.kind === 'proposed' || view.kind === 'abstain')
  return (
    <div className={`rd-cl-result rd-cl-result--${toneOf(view)}`}>
      <div className="rd-cl-result-head">
        <span className="rd-cl-headline">{view.headline}</span>
        <span className="rd-badge rd-cl-synthetic-badge">{SYNTHETIC_BADGE}</span>
      </div>
      {view.code !== null ? <p className="rd-cl-code mono">{view.code}</p> : null}
      <p className="rd-cl-body">{view.sentence}</p>
      <SyntheticNote view={view} listVersion={reply === null ? null : reply.list_version} />
      {view.kind === 'proposed' ? <p className="rd-cl-body rd-muted">{CONFIDENCE_NOTE}</p> : null}
      {showsEvidence ? <EvidenceLine evidenceId={reply.evidence_id} /> : null}
      {view.canRetry ? (
        <button type="button" className="ztax-btn-quiet" onClick={onRetry}>
          {RETRY_LABEL}
        </button>
      ) : null}
    </div>
  )
}

// ---------------------------------------------------------------------------
// Main panel
// ---------------------------------------------------------------------------

export const ClassifyPanel = () => {
  const [subjectRef, setSubjectRef] = useState(PREFILL_SUBJECT_REF)
  const [description, setDescription] = useState(PREFILL_DESCRIPTION)
  const [state, setState] = useState<ClassifyState>({ kind: 'idle' })
  const runner = useRef<ClassifyRunner | null>(null)
  runner.current ??= createClassifyRunner()

  const isBusy = state.kind === 'busy'

  // Leaving the page cancels the request that is still waiting.
  useEffect(
    () => () => {
      runner.current?.cancel()
    },
    [],
  )

  /**
   * Sends the current form values. Used by the form and by the retry button (manual retry
   * only). While a request is in flight this sends nothing: two quick submits make one.
   */
  const run = () => {
    // A refused start (one is already in flight) leaves the state as it is: already busy.
    runner.current?.start(
      { subjectRef: subjectRef.trim(), description: description.trim() },
      (ending) => { setState({ kind: 'ended', ending }) },
    )
    setState({ kind: 'busy' })
  }

  const submit = (e: FormEvent) => {
    e.preventDefault()
    run()
  }

  return (
    <section className="rd-cl-panel" aria-labelledby="rd-cl-title">
      <h2 className="ztax-card-title" id="rd-cl-title">
        Classification with approved-list check (demo)
      </h2>
      <p className="rd-cl-data-note ztax-note" role="note">
        {DATA_NOTE}
      </p>

      <form className="ztax-form rd-cl-form" onSubmit={submit}>
        <div className="ztax-field">
          <label htmlFor="rd-cl-subject-ref" className="ztax-label">
            Subject reference
          </label>
          <input
            id="rd-cl-subject-ref"
            type="text"
            className="ztax-input"
            value={subjectRef}
            maxLength={SUBJECT_REF_MAX}
            required
            disabled={isBusy}
            onChange={(e) => { setSubjectRef(e.target.value) }}
          />
        </div>

        <div className="ztax-field">
          <label htmlFor="rd-cl-description" className="ztax-label">
            Description
          </label>
          <textarea
            id="rd-cl-description"
            className="ztax-input rd-cl-textarea"
            value={description}
            maxLength={DESCRIPTION_MAX}
            rows={3}
            required
            disabled={isBusy}
            onChange={(e) => { setDescription(e.target.value) }}
          />
        </div>

        <div className="ztax-form-actions">
          <button type="submit" className="ztax-btn-copper" disabled={isBusy}>
            {isBusy ? BUSY_LABEL : SUBMIT_LABEL}
          </button>
        </div>
      </form>

      {/* The one live region of the panel. Nothing inside it carries its own role. */}
      <div className="rd-cl-output" aria-live="polite" aria-atomic="true">
        {state.kind === 'ended' ? (
          state.ending.kind === 'reply' ? (
            <ResultView view={viewOf(state.ending.reply)} reply={state.ending.reply} onRetry={run} />
          ) : (
            <ResultView view={viewOfFailure(state.ending.failure)} reply={null} onRetry={run} />
          )
        ) : null}
      </div>
    </section>
  )
}
