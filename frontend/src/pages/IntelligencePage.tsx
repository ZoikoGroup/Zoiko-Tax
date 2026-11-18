import { useEffect, useRef, useState, type FormEvent } from 'react'

import type { S } from '@zoikotax/sdk'

import { ztax } from '../platform/client'
import { Card, Columns, Field, Id, PageHeader, Submit, useAction } from '../ui/kit'
import { viewOfProposal, viewOfWaiting, type ProposalView } from './intelligence/viewOfProposal'
import type { PageProps } from './types'
import './obligations.css'

type Proposal = S<'ClassificationProposal'>

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
  if (provenance === undefined) {
    return null
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

/** The plain ending of a call: headline, what to do next, and how long it took. */
const EndingBanner = ({ view }: { view: ProposalView }) => (
  <div className="ztax-stepper" role="status" aria-live="polite">
    <p className="ztax-note" style={{ fontWeight: 600, marginBottom: 4 }}>{view.headline}</p>
    <p className="ztax-note" style={{ marginTop: 0 }}>{view.sentence}</p>
    <p className="ztax-note muted" style={{ marginTop: 0 }}>{view.elapsed}</p>
  </div>
)

export const IntelligencePage = ({ session, route }: PageProps) => {
  const canAsk = session.user.roles.some((r) => r === 'OPERATOR' || r === 'ANALYST')
  const [subjectRef, setSubjectRef] = useState('')
  // A description handed over from Home's assistant card, if any.
  const [description, setDescription] = useState(route?.draft ?? '')
  const action = useAction<Proposal>()
  // How long the call has run (display only; the real limit is the client's own).
  const startedAt = useRef(0)
  const [waitedMs, setWaitedMs] = useState(0)
  const [tookMs, setTookMs] = useState<number | null>(null)

  useEffect(() => {
    if (!action.busy) return undefined
    const timer = setInterval(() => { setWaitedMs(Date.now() - startedAt.current) }, 1000)
    return () => { clearInterval(timer) }
  }, [action.busy])

  const submit = (e: FormEvent) => {
    e.preventDefault()
    startedAt.current = Date.now()
    setWaitedMs(0)
    setTookMs(null)
    void action.run(() => ztax.proposeClassification({ subjectRef: subjectRef.trim(), description: description.trim() }))
      .then(() => { setTookMs(Date.now() - startedAt.current) })
  }

  const proposal = action.done
  const waitingView = action.busy ? viewOfWaiting(waitedMs) : null
  const ending = action.error !== null
    ? { kind: 'failure' as const, error: action.error }
    : proposal !== null
      ? { kind: 'proposal' as const, proposal }
      : null
  const endView = !action.busy && tookMs !== null && ending !== null ? viewOfProposal(ending, tookMs) : null

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
        </Card>

        <Card
          title="Proposal"
          aside={endView?.state === 'proposed' ? <span className="ztax-advisory-badge">Advisory — never authoritative</span> : undefined}
        >
          {action.busy ? (
            <>
              <p className="ztax-empty-note" role="status" aria-live="polite">Asking the AI…</p>
              {waitingView === null ? null : <EndingBanner view={waitingView} />}
            </>
          ) : endView === null ? (
            <p className="ztax-empty-note">No proposal yet. Describe an item and ask.</p>
          ) : (
            <>
              <EndingBanner view={endView} />
              {endView.state === 'proposed' && proposal !== null ? <ProposalResult proposal={proposal} /> : null}
            </>
          )}
        </Card>
      </Columns>
    </div>
  )
}