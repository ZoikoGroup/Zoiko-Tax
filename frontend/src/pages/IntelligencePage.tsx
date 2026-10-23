import { useState, type FormEvent } from 'react'

import type { S } from '@zoikotax/sdk'

import { ztax } from '../platform/client'
import { Card, Columns, ErrorNote, Field, Id, PageHeader, Raw, Submit, useAction } from '../ui/kit'
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
                before anything relies on it.
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
      </Columns>
    </div>
  )
}
