/**
 * The AI explanation of one obligation row, shown under the row.
 *
 * It asks the service once when it appears. Closing it, or opening another
 * row's, cancels the request, and a cancelled request never updates the screen.
 * Every word on screen comes from viewOfExplanation: this file prints no
 * status, no reason code and no error text. The panel takes focus when it
 * opens and closes with Escape; the page gives focus back to the row's button.
 */
import { useEffect, useRef, useState } from 'react'

import { EvidenceIdValue } from '../../components/ai/EvidenceIdValue'
import { ztax } from '../../platform/client'
import { Id } from '../../ui/kit'
import '../obligations.css'
import { AI_LABEL } from '../reportsDemo/ExplainPanel'
import {
  viewOfExplanation, viewOfExplanationWaiting, type ExplanationEnding, type ExplanationView,
} from './viewOfExplanation'

type Phase = { readonly kind: 'asking' } | { readonly kind: 'done'; readonly view: ExplanationView }

/** The words of an ending: headline and what to do next. The time is shown apart, outside the live region. */
const EndingText = ({ view }: { view: ExplanationView }) => (
  <div className="ztax-stepper">
    <p className="ztax-note" style={{ fontWeight: 600, marginBottom: 4 }}>{view.headline}</p>
    <p className="ztax-note" style={{ marginTop: 0 }}>{view.sentence}</p>
  </div>
)

export const ObligationExplainPanel = ({ obligationId, onClose }: { obligationId: string; onClose: () => void }) => {
  const [phase, setPhase] = useState<Phase>({ kind: 'asking' })
  const [attempt, setAttempt] = useState(0)
  const [waitedMs, setWaitedMs] = useState(0)
  const panelRef = useRef<HTMLElement>(null)
  const closeRef = useRef(onClose)
  closeRef.current = onClose

  // One request per attempt. The cleanup runs when the panel goes away, and
  // when a retry starts the next attempt: it cancels this one, and a cancelled
  // request is dropped before it can touch the screen.
  useEffect(() => {
    const controller = new AbortController()
    const startedAt = Date.now()
    const timer = setInterval(() => { setWaitedMs(Date.now() - startedAt) }, 1000)
    void (async () => {
      let ending: ExplanationEnding
      try {
        const result = await ztax.explainObligation(obligationId, controller.signal)
        ending = result.ok ? { kind: 'reply', reply: result.data } : { kind: 'failure', error: result.error }
      } catch (error) {
        ending = { kind: 'failure', error }
      }
      clearInterval(timer)
      if (controller.signal.aborted) return
      setPhase({ kind: 'done', view: viewOfExplanation(ending, Date.now() - startedAt, obligationId) })
    })()
    return () => {
      clearInterval(timer)
      controller.abort()
    }
  }, [obligationId, attempt])

  // Focus moves into the panel when it opens.
  useEffect(() => { panelRef.current?.focus() }, [])

  // Escape closes the panel from anywhere on the page while it is open.
  useEffect(() => {
    const onKeyDown = (e: globalThis.KeyboardEvent) => {
      if (e.key === 'Escape') closeRef.current()
    }
    document.addEventListener('keydown', onKeyDown)
    return () => { document.removeEventListener('keydown', onKeyDown) }
  }, [])

  const retry = () => {
    setWaitedMs(0)
    setPhase({ kind: 'asking' })
    setAttempt((n) => n + 1)
  }

  const slow = phase.kind === 'asking' ? viewOfExplanationWaiting(waitedMs) : null
  const view = phase.kind === 'done' ? phase.view : null
  const shownElapsed = view === null ? (slow?.elapsed ?? null) : view.elapsed

  return (
    <section ref={panelRef} tabIndex={-1} className="ztax-stepper" aria-label="AI explanation of this obligation">
      {/* One status region that is always on the panel, so a change in it is announced once. */}
      <div role="status" aria-live="polite">
        {view === null ? (
          <>
            <p className="ztax-empty-note">Asking the AI...</p>
            {slow === null ? null : <EndingText view={slow} />}
          </>
        ) : <EndingText view={view} />}
      </div>
      {/* The seconds count sits outside the live region, so it is not read out every second. */}
      {shownElapsed === null ? null : <p className="ztax-note muted" style={{ marginTop: 0 }}>{shownElapsed}</p>}

      {view === null || view.explanation === null ? null : (
        <>
          <p className="ztax-note" role="note" style={{ fontWeight: 600 }}>{AI_LABEL}</p>
          <p style={{ margin: '8px 0' }}>{view.explanation}</p>
          <dl className="ztax-kv">
            <dt>Cited row</dt>
            <dd>{view.citedIds.map((id) => <span key={id}><Id value={id} />{' '}</span>)}</dd>
            {view.evidenceId === null ? null : (
              <>
                <dt>Evidence id</dt>
                <dd className="mono"><EvidenceIdValue id={view.evidenceId} /></dd>
              </>
            )}
          </dl>
        </>
      )}

      <div className="ztax-form-actions" style={{ justifyContent: 'flex-start', gap: 8 }}>
        {view !== null && view.retry ? (
          <button type="button" className="ztax-btn-quiet" onClick={retry}>Try again</button>
        ) : null}
        <button type="button" className="ztax-btn-quiet" onClick={onClose}>Close</button>
      </div>
    </section>
  )
}
