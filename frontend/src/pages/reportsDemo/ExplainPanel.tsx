/**
 * The Explain side panel: one answer from the demo AI service, shown with the
 * same seven-step view the Intelligence page uses (its StepRow, not a copy).
 *
 * The AI only ever explains; nothing here edits or computes a number. When the
 * outcome is not EXPLAINED the panel shows the reason plainly and never the
 * explanation text, and says a person should check.
 */
import { StepRow } from '../IntelligencePage'
import '../obligations.css'
import { explainSteps } from './derive'
import type { ExplainReply, ExplainTarget, Provenance } from './model'

export type PanelState =
  | { readonly status: 'closed' }
  | { readonly status: 'loading'; readonly target: ExplainTarget }
  | { readonly status: 'unavailable'; readonly target: ExplainTarget }
  | { readonly status: 'done'; readonly target: ExplainTarget; readonly reply: ExplainReply }

export const AI_LABEL =
  'AI-generated explanation. General information, not tax advice or an authoritative determination. ' +
  'The figures come from the deterministic register.'

const ProvenanceTable = ({ provenance }: { provenance: Provenance }) => (
  <table className="ztax-action-table" style={{ marginTop: 8 }}>
    <tbody>
      <tr><th scope="row">Use case</th><td className="mono">{provenance.useCase}</td></tr>
      <tr><th scope="row">Model</th><td className="mono">{provenance.model}</td></tr>
      <tr><th scope="row">Provider</th><td className="mono">{provenance.provider}</td></tr>
      <tr><th scope="row">Prompt</th><td className="mono">{provenance.prompt}</td></tr>
      <tr><th scope="row">AI train</th><td className="mono">{provenance.aiTrain}</td></tr>
    </tbody>
  </table>
)

const Answer = ({
  target, reply, onCite,
}: { target: ExplainTarget; reply: ExplainReply; onCite: (id: string) => void }) => (
  <>
    <p className="ztax-note rd-ai-label" role="note">{AI_LABEL}</p>

    {reply.outcome === 'EXPLAINED' ? (
      <>
        <p className="rd-explanation">{reply.explanation}</p>
        <p className="rd-cited">
          <span className="rd-muted">Cited rows: </span>
          {reply.citedIds.length === 0 ? (
            <span className="rd-muted">none</span>
          ) : (
            reply.citedIds.map((id) => (
              <button
                key={id}
                type="button"
                className="ztax-btn-quiet mono rd-cite"
                onClick={() => { onCite(id) }}
                aria-label={`Highlight ${id}`}
              >
                {id}
              </button>
            ))
          )}
        </p>
      </>
    ) : (
      <>
        <p className="ztax-error-note" role="status">
          <strong>{reply.outcome.replaceAll('_', ' ')}.</strong> {reply.reason}
        </p>
        <p className="rd-check"><strong>A person should check this.</strong></p>
      </>
    )}

    <h3 className="ztax-card-title" style={{ marginTop: 16 }}>Workflow</h3>
    <div className="ztax-stepper" style={{ marginTop: 8 }}>
      {explainSteps(target, reply).map((step) => (
        <StepRow key={step.n} n={step.n} title={step.title} state={step.state} note={step.note}>
          {step.n === 7 && reply.provenance !== null ? <ProvenanceTable provenance={reply.provenance} /> : null}
        </StepRow>
      ))}
    </div>
  </>
)

export const ExplainPanel = ({
  state, onClose, onCite,
}: { state: PanelState; onClose: () => void; onCite: (id: string) => void }) => {
  if (state.status === 'closed') {
    return null
  }
  return (
    <aside className="rd-panel" aria-label="AI explanation" aria-live="polite">
      <div className="rd-panel-head">
        <h2 className="ztax-card-title">Explain: {state.target.title}</h2>
        <button type="button" className="ztax-btn-quiet" onClick={onClose}>Close</button>
      </div>

      {state.status === 'loading' ? <p className="rd-muted">Asking the demo AI service…</p> : null}

      {state.status === 'unavailable' ? (
        <p className="ztax-error-note" role="alert">
          The AI service is unavailable. The figures on this page are deterministic and unaffected.
        </p>
      ) : null}

      {state.status === 'done' ? <Answer target={state.target} reply={state.reply} onCite={onCite} /> : null}
    </aside>
  )
}
