import { instant } from '../../dashboard/format'
import type { ClockState, SupportCase } from './model'
import { words } from './model'

const TONE: Record<ClockState, 'ok' | 'warn' | 'bad' | 'neutral'> = {
  NONE: 'neutral', ON_TRACK: 'ok', AT_RISK: 'warn', BREACHED: 'bad', MET: 'ok', PASSED: 'bad',
}

export const ClockChip = ({ state }: { state: ClockState }) => (
  <span className={`ztax-status ztax-status--${TONE[state]}`}>{words(state)}</span>
)

/**
 * The two clocks of a case, side by side and never merged: the internal
 * support SLA and the law's deadline. The earlier statutory one is flagged
 * because it is the one the case must beat (SUP-06).
 */
export const Clocks = ({ c }: { c: SupportCase }) => {
  const k = c.clocks
  return (
    <dl className="ztax-kv">
      <dt>Support SLA</dt>
      <dd>
        <ClockChip state={k.sla} /> due {instant(k.slaDueAt)}
        {k.firstResponseAt === undefined ? null : <span className="muted"> · first response {instant(k.firstResponseAt)}</span>}
        <div className="muted" style={{ fontSize: 12 }}>Internal promise from severity {c.severity}. Not a legal deadline.</div>
      </dd>
      <dt>Statutory deadline</dt>
      <dd>
        {k.statutoryDueAt === undefined ? <span className="muted">None recorded</span> : (
          <>
            <ClockChip state={k.statutory} /> due {instant(k.statutoryDueAt)}
            {k.statutoryPrecedes ? (
              <span className="ztax-status ztax-status--bad" style={{ marginLeft: 6 }}>takes precedence</span>
            ) : null}
          </>
        )}
        <div className="muted" style={{ fontSize: 12 }}>The law’s deadline. Support never moves it.</div>
      </dd>
      <dt>Work to</dt>
      <dd>{instant(k.dueAt)} <span className="muted">({k.statutoryPrecedes ? 'statutory' : 'support SLA'})</span></dd>
    </dl>
  )
}
