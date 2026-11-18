import { useState, type FormEvent } from 'react'

import type { S } from '@zoikotax/sdk'

import { ztax } from '../platform/client'
import { Card, Columns, ErrorNote, Field, Id, PageHeader, Submit, useAction } from '../ui/kit'
import type { PageProps } from './types'
import './obligations.css'

type Proposal = S<'ClassificationProposal'>

/** Why the AI plane could not help, said plainly. The item goes to a person either way. */
const reviewReasons: Record<string, string> = {
  AI_GATEWAY_NOT_CONFIGURED:
    'AI assistance is not available here: no approved AI provider is connected.',
  AI_GATEWAY_UNAVAILABLE: 'The AI is down or slow right now.',
  AI_GATEWAY_REPLY_UNEVIDENCED: 'The AI answered without saying which model produced it, so the answer was discarded.',
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
// Reviewer panel: ACCEPT / REJECT / MODIFY / ESCALATE,
// each with a reason code and a note. Demo only — nothing here calls a
// backend endpoint or is saved anywhere; it only shows how the review step
// would apply to the proposal already on screen, which stays visible and
// unchanged above it (the original AI proposal is immutable evidence).
// Starts with nothing preselected: no default action, reason or note.
// ---------------------------------------------------------------------------

type ReviewerActionKind = 'ACCEPT' | 'REJECT' | 'MODIFY' | 'ESCALATE'

const reviewerActions: ReadonlyArray<{ kind: ReviewerActionKind; label: string }> = [
  { kind: 'ACCEPT', label: 'Accept' },
  { kind: 'REJECT', label: 'Reject' },
  { kind: 'MODIFY', label: 'Modify' },
  { kind: 'ESCALATE', label: 'Escalate' },
]

/** A short list for the demo; the governance model asks for reason codes but does not enumerate them. */
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

// ---------------------------------------------------------------------------
// The result: what the AI proposed (or why it could not), with the notices a
// reviewer needs and the provenance of the answer. Everything shown is taken
// from the real response, never invented.
// ---------------------------------------------------------------------------

const ProposalResult = ({ proposal }: { proposal: Proposal }) => {
  const provenance = proposal.provenance
  if (proposal.disposition !== 'PROPOSED' || provenance === undefined) {
    const reasonText = reviewReasons[proposal.reviewReason ?? ''] ?? 'The AI could not help with this item.'
    return (
      <div className="ztax-stepper">
        <p className="ztax-note">
          {reasonText} Deterministic processing is unaffected. No AI output is shown and nothing was changed.
        </p>
        <p className="ztax-note">There is no AI proposal to review. Classify the item by hand.</p>
      </div>
    )
  }
  return (
    <div className="ztax-stepper">
      <p className="ztax-note" style={{ fontWeight: 600 }}>
        AI-generated suggestion. General information, not tax advice or an authoritative determination.
        Deterministic processing decides.{' '}
        It can be wrong. A qualified person must review it before anything relies on it.
      </p>
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
      <ReviewerPanel key={proposal.id} proposal={proposal} />
    </div>
  )
}

export const IntelligencePage = ({ session, route }: PageProps) => {
  const canAsk = session.user.roles.some((r) => r === 'OPERATOR' || r === 'ANALYST')
  const [subjectRef, setSubjectRef] = useState('')
  // A description handed over from Home's assistant card, if any.
  const [description, setDescription] = useState(route?.draft ?? '')
  const action = useAction<Proposal>()

  const submit = (e: FormEvent) => {
    e.preventDefault()
    void action.run(() => ztax.proposeClassification({ subjectRef: subjectRef.trim(), description: description.trim() }))
  }

  const proposal = action.done

  return (
    <div className="ztax-page">
      <PageHeader title="Intelligence" subtitle="AI-assisted classification suggestions · advisory only" />

      <Card title="Advisory, never authoritative">
        <p className="ztax-alert-desc" style={{ margin: 0 }}>
          The AI proposes a code in ontology format for a catalog item; whether that code is a real ontology
          node is not verified here. A proposal is advice: it never becomes a classification decision
          on its own, and no confidence score makes it one. A person reviews it
          and confirms the mapping, or it is nothing. Send catalog text only — the description goes to an AI model,
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
            <p className="ztax-note">Asking for a proposal needs the Operator or Analyst role.</p>
          )}
          {action.error === null ? null : <ErrorNote error={action.error} />}
        </Card>

        <Card
          title="Proposal"
          aside={proposal === null ? undefined : <span className="ztax-advisory-badge">Advisory — never authoritative</span>}
        >
          {proposal === null ? (
            <p className="ztax-empty-note">No proposal yet. Describe an item and ask.</p>
          ) : (
            <ProposalResult proposal={proposal} />
          )}
        </Card>
      </Columns>
    </div>
  )
}