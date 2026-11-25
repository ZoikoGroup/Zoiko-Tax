import { useRef, useState, type FormEvent } from 'react'

import type { S } from '@zoikotax/sdk'

import '../../pages/obligations.css'
import '../../pages/intelligence/intelligence.css'
import { ztax } from '../../platform/client'
import { Field, Id, Submit } from '../../ui/kit'
import { CODE_MAX, NOTE_MAX, REASON_MAX, buildDecisionRequest } from './decisionRequest'
import { EvidenceIdValue } from './EvidenceIdValue'
import { evidenceIdText } from './evidenceId'
import { provenanceKind } from './provenanceKind'
import { provenanceNote } from './provenanceNote'
import {
  acceptedCode,
  canRecordDecision,
  reviewerActions,
  type ReviewerActionKind,
} from './reviewDecision'
import { viewOfDecision, type DecisionView } from './viewOfDecision'

type Proposal = S<'ClassificationProposal'>

// ---------------------------------------------------------------------------
// Reviewer panel: ACCEPT / REJECT / MODIFY / ESCALATE, each with a reason and
// an optional note. Recording sends the decision to the service, which saves it
// with who decided and when (the session names the reviewer; this screen never
// sends one). One decision is kept per proposal and it cannot be changed. The
// original AI proposal stays visible and unchanged above it. Starts with
// nothing preselected. `onAccept`, when given, is called with the proposed code
// only after an Accept was saved; Reject, Modify and Escalate never call it.
// ---------------------------------------------------------------------------

type Saved = S<'ClassificationDecision'>

const ReviewerPanel = ({ proposal, onAccept }: { proposal: Proposal; onAccept?: (code: string) => void }) => {
  const [pending, setPending] = useState<ReviewerActionKind | null>(null)
  const [reason, setReason] = useState('')
  const [note, setNote] = useState('')
  const [modifiedCode, setModifiedCode] = useState('')
  const [saving, setSaving] = useState(false)
  const inFlight = useRef(false)
  const [problem, setProblem] = useState<string | null>(null)
  const [failure, setFailure] = useState<DecisionView | null>(null)
  // Set once a decision is on record: the view, and the saved record when this screen saved it.
  const [onRecord, setOnRecord] = useState<{ view: DecisionView; saved: Saved | null } | null>(null)

  const startAction = (kind: ReviewerActionKind) => {
    setPending(kind)
    setReason('')
    setNote('')
    setModifiedCode('')
    setProblem(null)
    setFailure(null)
  }

  const canRecord = canRecordDecision(pending, reason, modifiedCode)

  const record = async (e: FormEvent) => {
    e.preventDefault()
    if (pending === null || !canRecord || inFlight.current) {
      return
    }
    const built = buildDecisionRequest(proposal, { action: pending, reason, note, modifiedCode })
    if (!built.ok) {
      setProblem(built.sentence)
      return
    }
    inFlight.current = true
    setSaving(true)
    setProblem(null)
    setFailure(null)
    try {
      const result = await ztax.recordClassificationDecision(built.request)
      const view = viewOfDecision(result.ok ? { ok: true } : { ok: false, error: result.error })
      if (!view.locked) {
        setFailure(view)
        return
      }
      setOnRecord({ view, saved: result.ok ? result.data : null })
      setPending(null)
      const code = result.ok ? acceptedCode(pending, proposal.proposedCode) : null
      if (code !== null && onAccept !== undefined) {
        onAccept(code)
      }
    } catch {
      setFailure(viewOfDecision({ ok: false, error: null }))
    } finally {
      inFlight.current = false
      setSaving(false)
    }
  }

  return (
    <>
      {onRecord === null ? null : (
        <>
          <p className="ztax-note" style={{ fontWeight: 600, marginBottom: 4 }}>{onRecord.view.headline}</p>
          <p className="ztax-note" style={{ marginTop: 0 }}>{onRecord.view.sentence}</p>
          {onRecord.saved === null ? null : (
            <dl className="ztax-kv" style={{ marginTop: 8 }}>
              <dt>Action</dt>
              <dd>{reviewerActions.find((a) => a.kind === onRecord.saved?.action)?.label ?? onRecord.saved.action}</dd>
              <dt>Reason</dt>
              <dd>{onRecord.saved.reason}</dd>
              {onRecord.saved.action === 'MODIFY' ? (
                <>
                  <dt>Original code (unchanged)</dt>
                  <dd className="mono">{proposal.proposedCode}</dd>
                  <dt>Reviewer&apos;s code</dt>
                  <dd className="mono">{onRecord.saved.modifiedCode}</dd>
                </>
              ) : null}
              {onRecord.saved.note === undefined ? null : (
                <>
                  <dt>Note</dt>
                  <dd>{onRecord.saved.note}</dd>
                </>
              )}
              <dt>Decided</dt>
              <dd className="mono">{onRecord.saved.decidedAt}</dd>
            </dl>
          )}
        </>
      )}

      {onRecord !== null ? null : pending === null ? (
        <div className="ztax-form-actions" style={{ justifyContent: 'flex-start', gap: 8, marginTop: 0 }}>
          {reviewerActions.map((a) => (
            <button key={a.kind} type="button" className="ztax-btn-quiet" disabled={saving} onClick={() => { startAction(a.kind) }}>
              {a.label}
            </button>
          ))}
        </div>
      ) : (
        <form className="ztax-form" onSubmit={(e) => { void record(e) }} style={{ marginTop: 12 }}>
          <Field label="Reason" help={`Required. At most ${String(REASON_MAX)} characters.`}>
            <textarea
              value={reason}
              required
              rows={2}
              maxLength={REASON_MAX}
              placeholder="Why this decision"
              onChange={(e) => { setReason(e.target.value) }}
            />
          </Field>
          {pending === 'MODIFY' ? (
            <Field label="Reviewer's ontology code" help="A code in ontology format, e.g. ontology:telecom/voice/mobile. Whether it exists is not verified here.">
              <input
                type="text"
                value={modifiedCode}
                required
                maxLength={CODE_MAX}
                placeholder="ontology:…"
                onChange={(e) => { setModifiedCode(e.target.value) }}
              />
            </Field>
          ) : null}
          <Field label="Note (optional)" help={`At most ${String(NOTE_MAX)} characters.`}>
            <textarea
              value={note}
              rows={2}
              maxLength={NOTE_MAX}
              onChange={(e) => { setNote(e.target.value) }}
            />
          </Field>
          {problem === null ? null : <p className="ztax-error-note" role="alert">{problem}</p>}
          {failure === null ? null : (
            <div className="ztax-error-note" role="alert">
              <strong>{failure.headline}</strong>
              <p style={{ margin: 0 }}>{failure.sentence}</p>
            </div>
          )}
          <div className="ztax-form-actions">
            <button type="button" className="ztax-btn-quiet" disabled={saving} onClick={() => { setPending(null) }}>Cancel</button>
            <Submit busy={saving}>Record decision</Submit>
          </div>
        </form>
      )}
    </>
  )
}

/**
 * A proposed classification as a reviewer needs it: the notices, the code, the
 * provenance and the review panel. Shown only for a proposed result. With no
 * `onAccept` it behaves exactly as the Intelligence page always has.
 */
export const ProposalCard = ({ proposal, onAccept, elapsed }: { proposal: Proposal; onAccept?: (code: string) => void; elapsed?: string | null }) => {
  const provenance = proposal.provenance
  if (provenance === undefined || provenance === null) {
    return null
  }
  const note = provenanceNote(provenanceKind(provenance))
  const evidenceId = evidenceIdText(provenance)
  return (
    <div className="ztax-stepper">
      {/* ── Header: title + badges ── */}
      <div className="ztax-intel-proposal-header">
        <span className="ztax-intel-proposal-title">Proposal</span>
        <div className="ztax-intel-badges">
          <span className="ztax-advisory-badge">Advisory — never authoritative</span>
          {/* TODO: replace always-shown with real approval flag when D-01 and Q10 are decided */}
          <span className="ztax-intel-badge ztax-intel-badge--warning">Test model — not approved</span>
        </div>
      </div>

      {/* ── Single result block (replaces four separate grey boxes) ── */}
      <div className="ztax-intel-result-block">
        <div className="ztax-intel-result-headline">
          <span className="ztax-intel-result-label">The AI suggested a code</span>
          {elapsed != null ? <span className="ztax-intel-timing">{elapsed}</span> : null}
        </div>

        <div className="ztax-intel-code-chip">{proposal.proposedCode}</div>

        <div className="ztax-intel-confidence-row">
          <span className="ztax-intel-badge ztax-intel-badge--neutral">Confidence: not calibrated</span>
        </div>

        <p className="ztax-intel-disclosure" style={{ marginTop: 4, marginBottom: 4 }}>
          No measured accuracy exists yet, so no confidence figure is shown.
        </p>

        {/* provenanceNote badge for test/unknown runtime */}
        {note === null ? null : note.tone === 'badge' ? (
          <p className="ztax-error-note" role="note" style={{ marginTop: 8, marginBottom: 0 }}><strong>{note.text}</strong></p>
        ) : (
          <p className="ztax-note muted" style={{ marginTop: 8, marginBottom: 0 }}>{note.text}</p>
        )}

        <p className="ztax-intel-disclosure">
          AI-generated suggestion. General information, not tax advice or an authoritative determination.
          Deterministic processing decides. It can be wrong. A qualified person must review it before anything relies on it.
        </p>
      </div>

      <div className="ztax-intel-divider" />

      {evidenceId === null ? null : (
        <p className="ztax-intel-evidence-row">
          <span className="ztax-intel-evidence-label">Evidence id</span>
          <span className="mono"><EvidenceIdValue id={evidenceId} /></span>
        </p>
      )}

      <div className="ztax-intel-divider" />

      {/* ── Review — directly under Evidence id ── */}
      <h3 className="ztax-intel-review-heading">Review</h3>
      <p className="ztax-intel-review-hint">
        Record Accept, Reject, Modify or Escalate with a reason. Saving a decision does not change the item&apos;s classification.
      </p>
      <ReviewerPanel key={proposal.id} proposal={proposal} {...(onAccept === undefined ? {} : { onAccept })} />

      <div className="ztax-intel-divider" />

      {/* ── Technical & provenance details — collapsed by default ── */}
      <details key={String(proposal.id)} className="ztax-intel-details">
        <summary>Technical details</summary>
        <table className="ztax-action-table">
          <tbody>
            <tr><th scope="row">Use case</th><td className="mono">{provenance.useCase}</td></tr>
            <tr><th scope="row">Model</th><td className="mono">{provenance.modelProfile}</td></tr>
            <tr><th scope="row">Provider</th><td className="mono">{provenance.providerProfile}</td></tr>
            <tr><th scope="row">Prompt</th><td className="mono">{provenance.promptProfile}</td></tr>
            <tr><th scope="row">AI release</th><td className="mono">{provenance.aiTrainVersion}</td></tr>
            {proposal.id === undefined ? null : (
              <tr><th scope="row">Proposal id</th><td><Id value={proposal.id} /></td></tr>
            )}
          </tbody>
        </table>
        <p>
          The code is checked for ontology format; whether it is an approved ontology node is checked only when an approved list is configured.
        </p>
        <p>The full audit record is kept by the AI service and is not shown on this page.</p>
      </details>
    </div>
  )
}
