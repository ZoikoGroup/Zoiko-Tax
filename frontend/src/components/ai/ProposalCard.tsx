import { useState, type FormEvent } from 'react'

import type { S } from '@zoikotax/sdk'

import '../../pages/obligations.css'
import { Field, Id, Submit } from '../../ui/kit'
import { provenanceKind } from './provenanceKind'
import {
  acceptedCode,
  canRecordDecision,
  reviewerActions,
  reviewerReasonCodes,
  type ReviewerActionKind,
} from './reviewDecision'

type Proposal = S<'ClassificationProposal'>

type ReviewerDecision = { action: ReviewerActionKind; reasonCode: string; note: string; modifiedCode: string }

// ---------------------------------------------------------------------------
// Reviewer panel: ACCEPT / REJECT / MODIFY / ESCALATE, each with a reason code
// and a note. Demo only: nothing here calls a backend endpoint or is saved
// anywhere; it only shows how the review step would apply to the proposal
// already on screen, which stays visible and unchanged above it (the original
// AI proposal is immutable evidence). Starts with nothing preselected.
// `onAccept`, when given, is called with the proposed code only when a
// decision recorded as Accept; Reject, Modify and Escalate never call it.
// ---------------------------------------------------------------------------

const ReviewerPanel = ({ proposal, onAccept }: { proposal: Proposal; onAccept?: (code: string) => void }) => {
  const [pending, setPending] = useState<ReviewerActionKind | null>(null)
  const [reasonCode, setReasonCode] = useState('')
  const [note, setNote] = useState('')
  const [modifiedCode, setModifiedCode] = useState('')
  const [decision, setDecision] = useState<ReviewerDecision | null>(null)

  const startAction = (kind: ReviewerActionKind) => {
    setPending(kind)
    setReasonCode('')
    setNote('')
    setModifiedCode('')
  }

  const canRecord = canRecordDecision(pending, reasonCode, note, modifiedCode)

  const record = (e: FormEvent) => {
    e.preventDefault()
    if (pending === null || !canRecord) {
      return
    }
    setDecision({ action: pending, reasonCode, note: note.trim(), modifiedCode: modifiedCode.trim() })
    const code = acceptedCode(pending, proposal.proposedCode)
    if (code !== null && onAccept !== undefined) {
      onAccept(code)
    }
    setPending(null)
  }

  return (
    <>
      {decision === null ? null : (
        <>
          <span className="ztax-advisory-badge">Demo only: decision is not saved</span>
          <dl className="ztax-kv" style={{ marginTop: 8 }}>
            <dt>Action</dt>
            <dd>{reviewerActions.find((a) => a.kind === decision.action)?.label ?? decision.action}</dd>
            <dt>Reason</dt>
            <dd>{reviewerReasonCodes.find((c) => c.value === decision.reasonCode)?.label ?? decision.reasonCode}</dd>
            {decision.action === 'MODIFY' ? (
              <>
                <dt>Original code (unchanged)</dt>
                <dd className="mono">{proposal.proposedCode}</dd>
                <dt>Reviewer&apos;s code</dt>
                <dd className="mono">{decision.modifiedCode}</dd>
              </>
            ) : null}
            <dt>Note</dt>
            <dd>{decision.note}</dd>
          </dl>
        </>
      )}

      {pending === null ? (
        <div className="ztax-form-actions" style={{ justifyContent: 'flex-start', gap: 8, marginTop: decision === null ? 0 : 16 }}>
          {reviewerActions.map((a) => (
            <button key={a.kind} type="button" className="ztax-btn-quiet" onClick={() => { startAction(a.kind) }}>
              {a.label}
            </button>
          ))}
        </div>
      ) : (
        <form className="ztax-form" onSubmit={record} style={{ marginTop: 12 }}>
          <Field label="Reason code">
            <select value={reasonCode} required onChange={(e) => { setReasonCode(e.target.value) }}>
              <option value="">Choose a reason…</option>
              {reviewerReasonCodes.map((r) => (
                <option key={r.value} value={r.value}>{r.label}</option>
              ))}
            </select>
          </Field>
          {pending === 'MODIFY' ? (
            <Field label="Reviewer's ontology code" help="A code in ontology format, e.g. ontology:telecom/voice/mobile. Whether it exists is not verified here.">
              <input
                type="text"
                value={modifiedCode}
                required
                placeholder="ontology:…"
                onChange={(e) => { setModifiedCode(e.target.value) }}
              />
            </Field>
          ) : null}
          <Field label="Note">
            <textarea
              value={note}
              required
              rows={2}
              placeholder="Why this decision"
              onChange={(e) => { setNote(e.target.value) }}
            />
          </Field>
          <div className="ztax-form-actions">
            <button type="button" className="ztax-btn-quiet" onClick={() => { setPending(null) }}>Cancel</button>
            <Submit busy={false}>Record decision</Submit>
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
export const ProposalCard = ({ proposal, onAccept }: { proposal: Proposal; onAccept?: (code: string) => void }) => {
  const provenance = proposal.provenance
  if (provenance === undefined) {
    return null
  }
  const kind = provenanceKind(provenance)
  return (
    <div className="ztax-stepper">
      <p className="ztax-note" style={{ fontWeight: 600 }}>
        AI-generated suggestion. General information, not tax advice or an authoritative determination.
        Deterministic processing decides.{' '}
        It can be wrong. A qualified person must review it before anything relies on it.
      </p>
      {kind === 'test' ? (
        <p className="ztax-error-note" role="note"><strong>Test runtime: not a real AI answer</strong></p>
      ) : null}
      {kind === 'unknown' ? (
        <p className="ztax-note muted">The source of this answer could not be confirmed from its profile ids.</p>
      ) : null}
      <dl className="ztax-kv" style={{ marginTop: 8 }}>
        <dt>Proposed code</dt>
        <dd className="ztax-proposed-code mono">{proposal.proposedCode}</dd>
        <dt>Confidence</dt>
        <dd className="mono">Not calibrated</dd>
        <dt>Authoritative</dt>
        <dd className="mono">{String(proposal.authoritative)}</dd>
        <dt>Proposal</dt>
        <dd>{proposal.id === undefined ? '—' : <Id value={proposal.id} />}</dd>
      </dl>
      <p className="ztax-note">
        No measured accuracy exists for this proposal yet, so no confidence figure is shown. The code is checked for
        ontology format only; whether it is an approved ontology node is not verified here. A person confirms or
        rejects the mapping before anything relies on it.
      </p>
      <table className="ztax-action-table" style={{ marginTop: 8 }}>
        <tbody>
          <tr><th scope="row">Use case</th><td className="mono">{provenance.useCase}</td></tr>
          <tr><th scope="row">Model profile</th><td className="mono">{provenance.modelProfile}</td></tr>
          <tr><th scope="row">Provider profile</th><td className="mono">{provenance.providerProfile}</td></tr>
          <tr><th scope="row">Prompt profile</th><td className="mono">{provenance.promptProfile}</td></tr>
          <tr><th scope="row">AI train</th><td className="mono">{provenance.aiTrainVersion}</td></tr>
        </tbody>
      </table>
      <p className="ztax-note">The full audit record is kept by the AI service and is not shown on this page.</p>

      <h3 className="ztax-card-title" style={{ marginTop: 16 }}>Review</h3>
      <p className="ztax-note">
        A qualified person reviews the proposal and records Accept, Reject, Modify or Escalate with a reason.
        Demo only: nothing below is sent anywhere or saved.
      </p>
      <ReviewerPanel key={proposal.id} proposal={proposal} {...(onAccept === undefined ? {} : { onAccept })} />
    </div>
  )
}
