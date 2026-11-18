import { useEffect, useRef, useState, type FormEvent } from 'react'

import type { S } from '@zoikotax/sdk'

import { ProposalCard } from '../components/ai/ProposalCard'
import { ztax } from '../platform/client'
import { Card, Columns, Field, PageHeader, Submit, useAction } from '../ui/kit'
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
              {endView.state === 'proposed' && proposal !== null ? <ProposalCard proposal={proposal} /> : null}
            </>
          )}
        </Card>
      </Columns>
    </div>
  )
}