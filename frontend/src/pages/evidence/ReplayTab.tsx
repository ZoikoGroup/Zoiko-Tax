import { useState, type FormEvent } from 'react'

import { ZoikoTaxError, type Decision, type ReplayReport, type Result } from '@zoikotax/sdk'

import { instant } from '../../dashboard/format'
import { ztax } from '../../platform/client'
import { Card, ErrorNote, Field, Status, Submit, useAction } from '../../ui/kit'
import { EmittedTable } from '../determine/shared'

const isNotFound = (error: unknown) => error instanceof ZoikoTaxError && error.status === 404

/**
 * Historical replay: evaluates a recorded decision's envelope again under the
 * content bundle it was made with, and compares the result digest with the one
 * recorded. Nothing is recomputed here; the cell reports the verdict.
 */
export const ReplayTab = () => {
  const [decisionId, setDecisionId] = useState('')
  const [asked, setAsked] = useState('')
  const replay = useAction<{ report: ReplayReport; decision: Decision | null }>()

  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    const id = decisionId.trim()
    setAsked(id)
    void replay.run(async (): Promise<Result<{ report: ReplayReport; decision: Decision | null }>> => {
      const [report, decision] = await Promise.all([ztax.replayDecision(id), ztax.getDecision(id)])
      if (!report.ok) {
        return report
      }
      return { ok: true, data: { report: report.data, decision: decision.ok ? decision.data : null } }
    })
  }

  const done = replay.done

  return (
    <>
      <Card title="Replay a decision">
        <p className="muted zp-intro">
          The cell evaluates the decision&apos;s recorded input envelope again, under the exact content bundle that made
          it, and compares the new result digest with the recorded one. A match is evidence the decision is reproducible;
          a divergence is evidence that something changed what the same input yields.
        </p>
        <form className="ztax-form" onSubmit={onSubmit} aria-label="Replay a decision">
          <Field label="Decision id">
            <input
              className="mono"
              value={decisionId}
              required
              placeholder="01920a4b-7c3e-7d21-9f40-3c1a2b4d5e6f"
              onChange={(e) => { setDecisionId(e.target.value) }}
            />
          </Field>
          <div className="ztax-form-actions">
            <Submit busy={replay.busy}>Replay</Submit>
          </div>
        </form>
        {replay.error === null ? null : isNotFound(replay.error) ? (
          <p className="zp-warn-note" role="status">
            No decision <span className="mono">{asked}</span> in this tenant.
          </p>
        ) : (
          <ErrorNote error={replay.error} />
        )}
      </Card>

      {done === null || replay.error !== null ? null : <ReplayView report={done.report} decision={done.decision} />}
    </>
  )
}

const VERDICT_NOTE: Record<ReplayReport['verdict'], string> = {
  MATCH: 'The replayed result digest equals the recorded one: the same input under the same content yields the same result.',
  DIVERGED: 'The replayed result differs from the recorded one. The recorded decision stands; the divergence is evidence to investigate.',
  BUNDLE_UNAVAILABLE: 'The content bundle that made this decision is not loaded in this cell, so it could not be replayed. This is not a mismatch.',
}

const ReplayView = ({ report, decision }: { report: ReplayReport; decision: Decision | null }) => (
  <Card title="Replay report" aside={<Status value={report.verdict} />}>
    <p className={report.verdict === 'MATCH' ? 'ztax-ok-note' : 'zp-warn-note'} role="status">
      {VERDICT_NOTE[report.verdict]}
    </p>
    <dl className="ztax-kv">
      <dt>Decision</dt>
      <dd className="mono">{report.decisionId}</dd>
      <dt>Envelope digest</dt>
      <dd className="mono" style={{ wordBreak: 'break-all' }}>{report.envelopeDigest}</dd>
      <dt>Recorded result</dt>
      <dd className="mono" style={{ wordBreak: 'break-all' }}>{report.recordedResult}</dd>
      <dt>Replayed result</dt>
      <dd className="mono" style={{ wordBreak: 'break-all' }}>
        {report.replayedResult ?? '—'}
        {report.replayedResult === undefined ? null : report.replayedResult === report.recordedResult ? (
          <span className="tag tag--ok" style={{ marginLeft: 6 }}>same</span>
        ) : (
          <span className="tag tag--warn" style={{ marginLeft: 6 }}>differs</span>
        )}
      </dd>
      {report.divergence === undefined ? null : (
        <>
          <dt>Divergence</dt>
          <dd>{report.divergence}</dd>
        </>
      )}
      {decision === null ? null : (
        <>
          <dt>Bundle</dt>
          <dd>
            <span className="mono">{decision.bundle.bundleId}</span> · IR v{decision.bundle.irVersion}
          </dd>
          <dt>Bundle digest</dt>
          <dd className="mono" style={{ wordBreak: 'break-all' }}>{decision.bundle.digest}</dd>
          <dt>Recorded</dt>
          <dd>{instant(decision.recordedAt)} · event time {instant(decision.eventTime)}</dd>
          <dt>Outcome</dt>
          <dd><Status value={decision.outcome} /> <span className="mono">{decision.reasonCode}</span></dd>
        </>
      )}
    </dl>
    {decision === null ? null : (
      <EmittedTable emitted={decision.emitted} caption="The recorded components, as the decision emitted them" />
    )}
  </Card>
)
