import { useState, type FormEvent } from 'react'

import type { S } from '@zoikotax/sdk'

import { ztax } from '../platform/client'
import { Card, Columns, ErrorNote, Field, Id, PageHeader, Raw, Status, Submit, useAction } from '../ui/kit'
import type { PageProps } from './types'
import './obligations.css'

type Proposal = S<'ClassificationProposal'>

/** Why the AI plane could not help, said plainly. The item goes to a person either way. */
const reviewReasons: Record<string, string> = {
  AI_GATEWAY_NOT_CONFIGURED:
    'AI assistance is not available in this cell: it has no Model Gateway, or the Gateway has no approved provider.',
  AI_GATEWAY_UNAVAILABLE: 'The Model Gateway is down or slow right now.',
  AI_GATEWAY_REPLY_UNEVIDENCED: 'The Model Gateway answered without saying which model produced it, so the answer was discarded.',
  AI_PROPOSAL_NOT_AN_ONTOLOGY_NODE: 'The model proposed a code that is not an ontology node, so the proposal was discarded.',
}

/**
 * Four invented, public, non-personal catalog items plus one deliberately
 * ambiguous bundle (spans voice, messaging, data and content at once), so a
 * reviewer can see both a clean proposal and a harder case. Picking one
 * fills the form below; the fields stay editable afterwards.
 */
const sampleItems: ReadonlyArray<{ subjectRef: string; description: string }> = [
  { subjectRef: 'sku:PLAN-UNL-5G', description: 'Unlimited 5G mobile plan with 20 GB hotspot' },
  { subjectRef: 'sku:PLAN-PRE-VOICE-500', description: 'Prepaid voice bundle, 500 minutes, no data included' },
  { subjectRef: 'sku:PLAN-FIBER-500', description: 'Fixed broadband plan, 500 Mbps symmetric fiber' },
  { subjectRef: 'sku:ADDON-SMS-1000', description: 'SMS add-on, 1000 messages for 30 days' },
  {
    subjectRef: 'sku:BUNDLE-AMBIGUOUS-01',
    description: 'Monthly bundle: unlimited calls, 50 SMS, 10 GB data and a streaming video subscription',
  },
]

// ---------------------------------------------------------------------------
// Reviewer panel (ZTAX-AIGOV-001 §12): ACCEPT / REJECT / MODIFY / ESCALATE,
// each with a reason code and a note. Demo only — nothing here calls a
// backend endpoint or is saved anywhere; it only shows how the review step
// would apply to the proposal already on screen, which stays visible and
// unchanged beside it (the original AI proposal is immutable evidence).
// ---------------------------------------------------------------------------

type ReviewerActionKind = 'ACCEPT' | 'REJECT' | 'MODIFY' | 'ESCALATE'

const reviewerActions: ReadonlyArray<{ kind: ReviewerActionKind; label: string }> = [
  { kind: 'ACCEPT', label: 'Accept' },
  { kind: 'REJECT', label: 'Reject' },
  { kind: 'MODIFY', label: 'Modify' },
  { kind: 'ESCALATE', label: 'Escalate' },
]

/** A short list for the demo — ZTAX-AIGOV-001 §12 asks for reason codes but does not enumerate them. */
const reviewerReasonCodes: ReadonlyArray<{ value: string; label: string }> = [
  { value: 'MATCHES_CATALOG_DESCRIPTION', label: 'Matches the catalog description' },
  { value: 'ONTOLOGY_NODE_LOOKS_WRONG', label: 'The proposed ontology node looks wrong' },
  { value: 'DESCRIPTION_WAS_AMBIGUOUS', label: 'The item description was ambiguous' },
  { value: 'NEEDS_SPECIALIST_REVIEW', label: 'Needs a tax specialist to confirm' },
  { value: 'OTHER', label: 'Other (see note)' },
]

type ReviewerDecision = { action: ReviewerActionKind; reasonCode: string; note: string; modifiedCode: string }

const ReviewerPanel = ({ proposal }: { proposal: Proposal }) => {
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

  const canRecord = reasonCode !== '' && note.trim() !== '' && (pending !== 'MODIFY' || modifiedCode.trim() !== '')

  const record = (e: FormEvent) => {
    e.preventDefault()
    if (pending === null || !canRecord) {
      return
    }
    setDecision({ action: pending, reasonCode, note: note.trim(), modifiedCode: modifiedCode.trim() })
    setPending(null)
  }

  return (
    <>
      <p className="ztax-note">
        Demo only: a reviewer action here calls no backend endpoint and is not saved. It shows how ZTAX-AIGOV-001
        §12&apos;s Accept / Reject / Modify / Escalate review step would apply to the proposal beside it.
      </p>

      {decision === null ? null : (
        <>
          <h3 className="ztax-card-title" style={{ marginTop: 16 }}>Reviewer decision</h3>
          <span className="ztax-advisory-badge">Demo only: decision is not saved</span>
          <dl className="ztax-kv" style={{ marginTop: 8 }}>
            <dt>Action</dt>
            <dd className="mono">{decision.action}</dd>
            <dt>Reason</dt>
            <dd className="mono">{decision.reasonCode}</dd>
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
            <Field label="Reviewer's ontology code" help="An existing ontology node, e.g. ontology:telecom/voice/mobile.">
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

const ReviewRequired = ({ proposal }: { proposal: Proposal }) => (
  <>
    <p className="ztax-error-note" role="status">
      Needs human review — no AI suggestion. {reviewReasons[proposal.reviewReason ?? ''] ?? ''} Classify the item by
      hand; deterministic processing is unaffected.
      <span className="mono muted"> {proposal.reviewReason}</span>
    </p>
    <dl className="ztax-kv">
      <dt>Subject</dt>
      <dd className="mono">{proposal.subjectRef}</dd>
      <dt>Disposition</dt>
      <dd className="mono">{proposal.disposition}</dd>
    </dl>
    <Raw value={proposal} />
  </>
)

export const IntelligencePage = ({ session }: PageProps) => {
  const canAsk = session.user.roles.some((r) => r === 'OPERATOR' || r === 'ANALYST')
  const [subjectRef, setSubjectRef] = useState('')
  const [description, setDescription] = useState('')
  const action = useAction<Proposal>()

  const submit = (e: FormEvent) => {
    e.preventDefault()
    void action.run(() => ztax.proposeClassification({ subjectRef: subjectRef.trim(), description: description.trim() }))
  }

  const proposal = action.done

  return (
    <div className="ztax-page">
      <PageHeader title="Intelligence" subtitle="Classification assistance from the governed AI plane · advisory only" />

      <Card title="Advisory, never authoritative">
        <p className="ztax-alert-desc" style={{ margin: 0 }}>
          The AI plane proposes an existing ontology node for a catalog item. Under ADR-0006 a proposal is advice: it
          never becomes a classification decision on its own, and no confidence score makes it one. A person reviews it
          and confirms the mapping, or it is nothing. Send catalog text only — the description goes to the Model Gateway,
          so it must never contain customer data.
        </p>
      </Card>

      <Columns>
        <Card title="Ask for a proposal">
          <form className="ztax-form" onSubmit={submit}>
            <Field label="Sample item" help="Fills the fields below. You can still edit them, or type your own.">
              <select
                value=""
                onChange={(e) => {
                  const item = sampleItems.find((s) => s.subjectRef === e.target.value)
                  if (item !== undefined) {
                    setSubjectRef(item.subjectRef)
                    setDescription(item.description)
                  }
                }}
              >
                <option value="">Choose a sample…</option>
                {sampleItems.map((s) => (
                  <option key={s.subjectRef} value={s.subjectRef}>{s.description}</option>
                ))}
              </select>
            </Field>
            <Field label="Subject reference" help="Your reference for the item, e.g. a catalog SKU.">
              <input
                type="text"
                value={subjectRef}
                required
                placeholder="sku:PLAN-UNL-5G"
                onChange={(e) => { setSubjectRef(e.target.value) }}
              />
            </Field>
            <Field label="Description" help="The item's commercial description. Catalog text, never customer data.">
              <textarea
                value={description}
                required
                rows={3}
                placeholder="Unlimited 5G mobile plan with 20 GB hotspot"
                onChange={(e) => { setDescription(e.target.value) }}
              />
            </Field>
            <div className="ztax-form-actions">
              <Submit busy={action.busy}>Propose a classification</Submit>
            </div>
          </form>
          {canAsk ? null : (
            <p className="ztax-note">Asking for a proposal needs the OPERATOR or ANALYST role; the cell will refuse it.</p>
          )}
          {action.error === null ? null : <ErrorNote error={action.error} />}
        </Card>

        <Card
          title="Proposal"
          aside={proposal === null ? undefined : <span className="ztax-advisory-badge">Advisory — never authoritative</span>}
        >
          {proposal === null ? (
            <p className="ztax-empty-note">No proposal yet. Describe an item and ask.</p>
          ) : proposal.disposition === 'REVIEW_REQUIRED' || proposal.provenance === undefined ? (
            <ReviewRequired proposal={proposal} />
          ) : (
            <>
              <p className="ztax-status-line" role="status">
                <Status value="REVIEW_REQUIRED" />
                <span>An automated proposal exists, but a person must adopt or reject it before anything relies on it.</span>
              </p>
              <dl className="ztax-kv">
                <dt>Subject</dt>
                <dd className="mono">{proposal.subjectRef}</dd>
                <dt>Proposed code</dt>
                <dd className="ztax-proposed-code mono">{proposal.proposedCode}</dd>
                <dt>Confidence</dt>
                <dd className="mono">{proposal.confidence ?? '—'}</dd>
                <dt>Authoritative</dt>
                <dd className="mono">{String(proposal.authoritative)}</dd>
                <dt>Proposal</dt>
                <dd>{proposal.id === undefined ? '—' : <Id value={proposal.id} />}</dd>
              </dl>
              <p className="ztax-note">
                The confidence is the model&apos;s own score, not a threshold. A person confirms or rejects the mapping
                before anything relies on it. This use case always asks the Model Gateway at authority A1 — that is
                fixed in the governed request and is not a field the reply itself returns.
              </p>

              <h3 className="ztax-card-title" style={{ marginTop: 16 }}>Provenance</h3>
              <table className="ztax-action-table">
                <tbody>
                  <tr><th scope="row">Use case</th><td className="mono">{proposal.provenance.useCase}</td></tr>
                  <tr><th scope="row">Model profile</th><td className="mono">{proposal.provenance.modelProfile}</td></tr>
                  <tr><th scope="row">Provider profile</th><td className="mono">{proposal.provenance.providerProfile}</td></tr>
                  <tr><th scope="row">Prompt profile</th><td className="mono">{proposal.provenance.promptProfile}</td></tr>
                  <tr><th scope="row">AI train</th><td className="mono">{proposal.provenance.aiTrainVersion}</td></tr>
                </tbody>
              </table>
              <Raw value={proposal} />
            </>
          )}
        </Card>

        {proposal === null || proposal.disposition !== 'PROPOSED' || proposal.provenance === undefined ? null : (
          <Card title="Reviewer decision">
            <ReviewerPanel key={proposal.id} proposal={proposal} />
          </Card>
        )}
      </Columns>
    </div>
  )
}
