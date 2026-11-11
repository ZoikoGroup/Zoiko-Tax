/**
 * ClassifyPanel — "Classification with approved-list check (demo)" panel.
 *
 * States: idle, busy, reply (proposed, set aside, blocked, not configured, could
 * not be interpreted), rejected input, timeout, unavailable.
 *
 * It switches on `kind` from outcomes.ts and never on a wire string, and it shows
 * only fixed text: nothing the server sends is displayed except the proposed code
 * of a passed reply, the list version and the evidence id.
 *
 * DEMO ONLY. Synthetic data only. Never computes a real fiscal determination.
 */
import { useEffect, useRef, useState, type FormEvent } from 'react'

import { DEMO_AI_TIMEOUT_MS } from '../../platform/demoAi'
import { Id } from '../../ui/kit'
import { DESCRIPTION_MAX, requestClassify, SUBJECT_REF_MAX, type ClassifyResult } from './api'
import type { ClassifyReply } from './model'
import { viewOf, type ClassifyView } from './outcomes'

// ---------------------------------------------------------------------------
// Prefill text (synthetic demo data)
// ---------------------------------------------------------------------------

const PREFILL_SUBJECT_REF = 'sku:DEMO-PLAN-5G-UNLIMITED'
const PREFILL_DESCRIPTION =
  'Unlimited 5G mobile plan with 20 GB mobile hotspot and international roaming (synthetic demo data)'

const TIMEOUT_SECONDS = Math.round(DEMO_AI_TIMEOUT_MS / 1000)

type ClassifyState =
  | { readonly kind: 'idle' }
  | { readonly kind: 'busy' }
  | { readonly kind: 'rejected' }
  | { readonly kind: 'timeout' }
  | { readonly kind: 'unavailable' }
  | { readonly kind: 'reply'; readonly reply: ClassifyReply }

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
      Synthetic demo. Checked against the SYNTHETIC list <span className="mono">{listVersion}</span>.
      This is not the approved ontology list (pending D-05).
    </p>
  ) : (
    <p className="rd-cl-body rd-muted">Synthetic demo data only.</p>
  )

const EvidenceLine = ({ evidenceId }: { evidenceId: string | null }) => (
  <p className="rd-cl-body rd-muted">
    {evidenceId !== null ? (
      <>
        Evidence: <Id value={evidenceId} />
      </>
    ) : (
      'Evidence id not available'
    )}
  </p>
)

const ReplyView = ({ reply }: { reply: ClassifyReply }) => {
  const view = viewOf(reply)
  const showsEvidence = view.kind === 'proposed' || view.kind === 'abstain'
  return (
    <div className={`rd-cl-result rd-cl-result--${toneOf(view)}`}>
      <div className="rd-cl-result-head">
        <span className="rd-cl-headline">{view.headline}</span>
        <span className="rd-badge rd-cl-synthetic-badge">Synthetic demo data</span>
      </div>
      {view.code !== null ? <p className="rd-cl-code mono">{view.code}</p> : null}
      <p className="rd-cl-body">{view.sentence}</p>
      <SyntheticNote view={view} listVersion={reply.list_version} />
      {view.kind === 'proposed' ? <p className="rd-cl-body rd-muted">Confidence: not calibrated</p> : null}
      {showsEvidence ? <EvidenceLine evidenceId={reply.evidence_id} /> : null}
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
  const inFlight = useRef<AbortController | null>(null)

  const isBusy = state.kind === 'busy'

  // Leaving the page cancels the request that is still waiting.
  useEffect(
    () => () => {
      inFlight.current?.abort()
    },
    [],
  )

  /** Sends the current form values. Used by the form and by "Try again" (manual retry only). */
  const run = () => {
    // Abort any in-flight request before starting a new one
    inFlight.current?.abort()
    const controller = new AbortController()
    inFlight.current = controller
    setState({ kind: 'busy' })

    void requestClassify({ subjectRef: subjectRef.trim(), description: description.trim() }, controller.signal)
      .then((result: ClassifyResult) => {
        // Discard the result if this request was superseded or the page was left
        if (controller.signal.aborted) return
        switch (result.status) {
          case 'reply':
            setState({ kind: 'reply', reply: result.reply })
            break
          case 'rejected':
            setState({ kind: 'rejected' })
            break
          case 'timeout':
            setState({ kind: 'timeout' })
            break
          case 'unavailable':
            setState({ kind: 'unavailable' })
            break
          case 'aborted':
            // Superseded — do nothing
            break
        }
      })
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
        Synthetic data only. Do not enter real customer data.
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
            {isBusy ? 'Classifying…' : 'Classify'}
          </button>
        </div>
      </form>

      {/* The one live region of the panel. Nothing inside it carries its own role. */}
      <div className="rd-cl-output" aria-live="polite" aria-atomic="true">
        {state.kind === 'rejected' ? (
          <p className="ztax-error-note rd-cl-result">
            The request was not accepted. Check the fields and try again.
          </p>
        ) : null}

        {state.kind === 'timeout' ? (
          <div className="rd-cl-result rd-cl-result--neutral">
            <p className="rd-cl-body">
              The AI service is taking longer than expected (over {TIMEOUT_SECONDS} seconds). It may
              still finish in the background. You can try again. Deterministic processing is
              unaffected.
            </p>
            <button type="button" className="ztax-btn-quiet" onClick={run}>
              Try again
            </button>
          </div>
        ) : null}

        {state.kind === 'unavailable' ? (
          <p className="ztax-error-note rd-cl-result">
            The AI service is unavailable. Deterministic processing is unaffected.
          </p>
        ) : null}

        {state.kind === 'reply' ? <ReplyView reply={state.reply} /> : null}
      </div>
    </section>
  )
}
