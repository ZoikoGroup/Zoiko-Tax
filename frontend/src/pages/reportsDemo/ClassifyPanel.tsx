/**
 * ClassifyPanel — "Classification with approved-list check (demo)" panel.
 *
 * Covers all 7 outcome states: PROPOSED (pass), ABSTAIN (set aside),
 * NOT_CONFIGURED, BLOCKED/REFUSED, timeout (>30 s), unreachable, unknown.
 *
 * DEMO ONLY. Synthetic data only. Never computes a real fiscal determination.
 */
import { useRef, useState, type FormEvent } from 'react'

import { Id } from '../../ui/kit'
import { requestClassify, type ClassifyResult } from './api'
import type { ClassifyReply } from './model'
import { lookupOutcome, NEEDS_REVIEW_ENTRY } from './outcomes'

// ---------------------------------------------------------------------------
// Prefill text (synthetic demo data)
// ---------------------------------------------------------------------------

const PREFILL_SUBJECT_REF = 'sku:DEMO-PLAN-5G-UNLIMITED'
const PREFILL_DESCRIPTION =
  'Unlimited 5G mobile plan with 20 GB mobile hotspot and international roaming (synthetic demo data)'

// ---------------------------------------------------------------------------
// Per-reason plain English sentences for ABSTAIN
// ---------------------------------------------------------------------------

const ABSTAIN_REASON_TEXT: Readonly<Record<string, string>> = {
  GUARD_ONTOLOGY_UNKNOWN:
    'The proposed ontology code is not recognised in the approved list for this demo service.',
  GUARD_ONTOLOGY_EXPIRED:
    'The proposed ontology code has expired and is no longer valid in the approved list.',
  GUARD_ONTOLOGY_NOT_YET_EFFECTIVE:
    'The proposed ontology code is not yet effective in the approved list.',
}

const DEFAULT_ABSTAIN_TEXT = 'The proposal was set aside. A person should review it.'

// ---------------------------------------------------------------------------
// Sub-components
// ---------------------------------------------------------------------------

type ClassifyState =
  | { readonly kind: 'idle' }
  | { readonly kind: 'busy' }
  | { readonly kind: 'timeout' }
  | { readonly kind: 'unavailable' }
  | { readonly kind: 'reply'; readonly reply: ClassifyReply }

const ProposedResult = ({ reply }: { reply: ClassifyReply }) => {
  const entry = lookupOutcome(reply.outcome)
  // PROPOSED with non-PASS verdict degrades to NEEDS_REVIEW
  const isPass = reply.outcome === 'PROPOSED' && reply.verdict === 'PASS'
  const effective = isPass ? entry : NEEDS_REVIEW_ENTRY

  if (!isPass) {
    return (
      <div className="rd-cl-result rd-cl-result--neutral">
        <p className="rd-cl-headline">{effective.headline}</p>
        <p className="rd-cl-body">
          This result could not be interpreted. A person should check it.
        </p>
      </div>
    )
  }

  return (
    <div className="rd-cl-result rd-cl-result--pass">
      <div className="rd-cl-result-head">
        <span className="rd-cl-headline">{effective.headline}</span>
        {reply.synthetic === true ? (
          <span className="rd-badge rd-cl-synthetic-badge">Synthetic demo data</span>
        ) : null}
      </div>
      {reply.proposed_code !== undefined ? (
        <p className="rd-cl-code mono">{reply.proposed_code}</p>
      ) : null}
      {reply.list_version !== null ? (
        <p className="rd-cl-body rd-muted">
          Checked against the SYNTHETIC demo list{' '}
          <span className="mono">{reply.list_version}</span>. This is not the
          approved ontology list (pending D-05).
        </p>
      ) : null}
      <p className="rd-cl-body rd-muted">Confidence: not calibrated</p>
      {reply.evidence_id !== null ? (
        <p className="rd-cl-body rd-muted">
          Evidence: <Id value={reply.evidence_id} />
        </p>
      ) : null}
      <p className="rd-check">A person should review this proposal before it is used.</p>
    </div>
  )
}

const AbstainResult = ({ reply }: { reply: ClassifyReply }) => {
  const reasonText = ABSTAIN_REASON_TEXT[reply.reason_code] ?? DEFAULT_ABSTAIN_TEXT
  return (
    <div className="rd-cl-result rd-cl-result--caution">
      <p className="rd-cl-headline">Set aside — not classified</p>
      <p className="rd-cl-body">{reasonText}</p>
      {reply.evidence_id !== null ? (
        <p className="rd-cl-body rd-muted">
          Evidence: <Id value={reply.evidence_id} />
        </p>
      ) : null}
      <p className="rd-check"><strong>A person should check this.</strong></p>
    </div>
  )
}

const BlockedResult = () => (
  <div className="rd-cl-result rd-cl-result--caution">
    <p className="rd-cl-headline">Blocked by governance</p>
    <p className="rd-cl-body">
      This request was blocked by a governance rule. The rule does not permit this
      operation on the demo service.
    </p>
    <p className="rd-check"><strong>A person should check this.</strong></p>
  </div>
)

const NotConfiguredResult = () => (
  <div className="rd-cl-result rd-cl-result--neutral">
    <p className="rd-cl-headline">Not configured</p>
    <p className="rd-cl-body">
      The approved-list check is not set up on this demo service.
    </p>
    <p className="rd-check"><strong>A person should check this.</strong></p>
  </div>
)

const NeedsReviewResult = () => (
  <div className="rd-cl-result rd-cl-result--neutral">
    <p className="rd-cl-headline">Result could not be interpreted</p>
    <p className="rd-cl-body">
      This result could not be interpreted. A person should check it.
    </p>
  </div>
)

// ---------------------------------------------------------------------------
// Outcome router — the ONLY place outcome strings are dispatched
// ---------------------------------------------------------------------------

const ReplyView = ({ reply }: { reply: ClassifyReply }) => {
  const normalised =
    reply.outcome === 'ABSTAINED' ? 'ABSTAIN'
    : reply.outcome === 'REFUSED' || reply.outcome === 'BLOCK' ? 'BLOCKED'
    : reply.outcome

  if (normalised === 'PROPOSED') return <ProposedResult reply={reply} />
  if (normalised === 'ABSTAIN') return <AbstainResult reply={reply} />
  if (normalised === 'BLOCKED') return <BlockedResult />
  if (normalised === 'NOT_CONFIGURED') return <NotConfiguredResult />
  // REVIEW_REQUIRED or any truly unknown outcome string
  const entry = lookupOutcome(normalised)
  if (entry === NEEDS_REVIEW_ENTRY || normalised === 'REVIEW_REQUIRED') {
    return <NeedsReviewResult />
  }
  return <NeedsReviewResult />
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

  const submit = (e: FormEvent) => {
    e.preventDefault()
    // Abort any in-flight request before starting a new one
    inFlight.current?.abort()
    const controller = new AbortController()
    inFlight.current = controller
    setState({ kind: 'busy' })

    void requestClassify({ subjectRef: subjectRef.trim(), description: description.trim() }, controller.signal)
      .then((result: ClassifyResult) => {
        // Discard result if this request was superseded
        if (controller.signal.aborted) return
        switch (result.status) {
          case 'reply':
            setState({ kind: 'reply', reply: result.reply })
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

  const retry = () => {
    setState({ kind: 'idle' })
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
            maxLength={200}
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
            maxLength={2000}
            rows={3}
            required
            disabled={isBusy}
            onChange={(e) => { setDescription(e.target.value) }}
          />
        </div>

        <div className="ztax-form-actions">
          <button
            type="submit"
            className="ztax-btn-copper"
            disabled={isBusy}
          >
            {isBusy ? 'Classifying…' : 'Classify'}
          </button>
        </div>
      </form>

      <div className="rd-cl-output" aria-live="polite" aria-atomic="true">
        {state.kind === 'timeout' ? (
          <div className="rd-cl-result rd-cl-result--neutral" role="status">
            <p className="rd-cl-body">
              The AI service is taking longer than expected (over 30 seconds). It may
              still finish in the background. You can try again. Deterministic
              processing is unaffected.
            </p>
            <button
              type="button"
              className="ztax-btn-quiet"
              onClick={retry}
            >
              Try again
            </button>
          </div>
        ) : null}

        {state.kind === 'unavailable' ? (
          <p className="ztax-error-note rd-cl-result" role="alert">
            The AI service is unavailable. Deterministic processing is unaffected.
          </p>
        ) : null}

        {state.kind === 'reply' ? <ReplyView reply={state.reply} /> : null}
      </div>
    </section>
  )
}

