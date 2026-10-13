import type { ReactNode } from 'react'

import type { Dashboard, DashboardAction } from '@zoikotax/sdk'

import {
  IconAlertCircle,
  IconCalendar,
  IconDollarSign,
  IconFileText,
  IconShieldCheck,
  IconSparkles,
} from '../components/common/DashboardIcons'
import { civilDate, countChange, instant, moneyLines, moneyText, percent, periodLabel } from '../dashboard/format'
import { useDashboard } from '../dashboard/useDashboard'

// Donut progress gauge. `percentage` is a ratio of counts, never of amounts.
const CircularGauge = ({
  percentage,
  statusText,
  size = 96,
  strokeWidth = 8,
}: {
  percentage: number | null
  statusText: string
  size?: number
  strokeWidth?: number
}) => {
  const radius = (size - strokeWidth) / 2
  const circumference = 2 * Math.PI * radius
  const offset = circumference - ((percentage ?? 0) / 100) * circumference

  return (
    <div style={{ position: 'relative', width: size, height: size, flexShrink: 0 }}>
      <svg width={size} height={size} style={{ transform: 'rotate(-90deg)' }} aria-hidden="true">
        <circle cx={size / 2} cy={size / 2} r={radius} stroke="#e5e7eb" strokeWidth={strokeWidth} fill="transparent" />
        <circle
          cx={size / 2}
          cy={size / 2}
          r={radius}
          stroke="#10b981"
          strokeWidth={strokeWidth}
          strokeDasharray={circumference}
          strokeDashoffset={offset}
          strokeLinecap="round"
          fill="transparent"
        />
      </svg>
      <div
        style={{
          position: 'absolute',
          inset: 0,
          display: 'flex',
          flexDirection: 'column',
          alignItems: 'center',
          justifyContent: 'center',
          textAlign: 'center',
          lineHeight: 1,
        }}
      >
        <span style={{ fontSize: 16, fontWeight: 700, color: '#111827' }}>
          {percentage === null ? '—' : `${percentage}%`}
        </span>
        <span style={{ fontSize: 10, color: '#9ca3af', fontWeight: 500, marginTop: 4 }}>{statusText}</span>
      </div>
    </div>
  )
}

interface Segment {
  readonly label: string
  readonly count: number
  readonly color: string
}

const Gauge = ({ title, subtitle, percentage, statusText, segments }: {
  title: string
  subtitle: string
  percentage: number | null
  statusText: string
  segments: readonly Segment[]
}) => (
  <div className="ztax-card ztax-gauge-card">
    <div>
      <h3 className="ztax-card-title">{title}</h3>
      <div style={{ fontSize: 10, color: '#9ca3af', marginTop: 2 }}>{subtitle}</div>
    </div>
    <div className="ztax-gauge-content">
      <CircularGauge percentage={percentage} statusText={statusText} size={86} strokeWidth={7} />
      <div className="ztax-gauge-breakdown">
        {segments.map((seg) => (
          <div key={seg.label} className="ztax-breakdown-row">
            <div className="ztax-breakdown-left">
              <span
                style={{
                  width: 6,
                  height: 6,
                  borderRadius: '50%',
                  backgroundColor: seg.color,
                  display: 'inline-block',
                  flexShrink: 0,
                }}
              />
              <span>{seg.label}</span>
            </div>
            <span className="ztax-breakdown-count">{seg.count}</span>
          </div>
        ))}
      </div>
    </div>
  </div>
)

const priorityClass = (p: DashboardAction['priority']) =>
  p === 'P0' ? 'ztax-badge-p0-red' : p === 'P1' ? 'ztax-badge-p1-yellow' : 'ztax-badge-p2-teal'

const ACTION_VERB: Record<DashboardAction['kind'], string> = {
  OBLIGATION_OVERDUE: 'File now',
  OBLIGATION_ATTENTION: 'Resolve',
  OBLIGATION_DUE: 'Prepare',
  RECONCILIATION_BREAK: 'Investigate',
  REFUND_UNCERTAIN: 'Confirm with provider',
  REFUND_FAILED: 'Review',
  DECISION_REVIEW: 'Review',
  WEBHOOK_DEAD: 'Replay',
}

/** Two SVG paths from the trend: liability from the server's 0–100 index,
 *  throughput from decision counts scaled to the busiest period. */
function trendPaths(d: Dashboard): { liability: string; throughput: string } {
  const points = d.trend.points
  const width = 500
  const top = 20
  const bottom = 140
  const step = points.length > 1 ? (width - 20) / (points.length - 1) : 0
  const most = points.reduce((m, p) => Math.max(m, p.decisions), 0)
  const y = (share: number) => bottom - ((bottom - top) * share) / 100
  const path = (shares: number[]) =>
    shares.map((s, i) => `${i === 0 ? 'M' : 'L'} ${10 + i * step} ${y(s)}`).join(' ')
  return {
    liability: path(points.map((p) => p.liabilityIndex)),
    throughput: path(points.map((p) => (most > 0 ? (p.decisions * 100) / most : 0))),
  }
}

const Kpi = ({ icon, title, lines, footer, badge }: {
  icon: ReactNode
  title: string
  lines: string[]
  footer: ReactNode
  badge?: string | undefined
}) => (
  <div className="ztax-card">
    <div className="ztax-kpi-header">
      <div className="ztax-kpi-header-left">
        {icon}
        <span>{title}</span>
      </div>
      {badge === undefined ? null : <span className="ztax-badge-p0">{badge}</span>}
    </div>
    <div className="ztax-kpi-value">
      {lines.map((line) => (
        <div key={line}>{line}</div>
      ))}
    </div>
    <div className="ztax-kpi-footer">{footer}</div>
  </div>
)

const iconStyle = { width: 16, height: 16, color: '#9ca3af' }

export interface ClientDashboardProps {
  readonly period: string
}

export const ClientDashboard = ({ period }: ClientDashboardProps) => {
  const { state, reload } = useDashboard(period)

  if (state.status === 'loading') {
    return <p className="muted" aria-live="polite">Reading the record for {periodLabel(period)}…</p>
  }
  if (state.status === 'forbidden') {
    return (
      <div className="ztax-card" role="alert">
        <h2 className="ztax-card-title">The dashboard needs a fiscal role</h2>
        <p className="ztax-alert-desc">
          It reads decisions, journals and obligations, which <strong>OPERATOR</strong>, <strong>ANALYST</strong> or{' '}
          <strong>AUDITOR</strong> may see. Administration alone does not include them. Ask an administrator to grant one.
        </p>
      </div>
    )
  }
  if (state.status === 'error') {
    return (
      <div className="ztax-card" role="alert">
        <h2 className="ztax-card-title">The dashboard could not be read</h2>
        <p className="mono muted">{state.detail}</p>
        <button type="button" className="ztax-btn-copper" onClick={reload}>
          Try again
        </button>
      </div>
    )
  }

  const d = state.data
  const det = d.determinations
  const p0 = d.actions.filter((a) => a.priority === 'P0').length
  const p1 = d.actions.filter((a) => a.priority === 'P1').length
  const change = countChange(det.decisions, det.previousDecisions)
  const liability = d.liability[0]
  const rec = d.reconciliation
  const recTotal = rec.matched + rec.breaks + rec.open
  const jur = d.jurisdictions
  const jurTotal = jur.ready + jur.atRisk + jur.blocked
  const ev = d.evidence
  const paths = trendPaths(d)

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
      <div className="ztax-title-bar">
        <div>
          <h1 className="ztax-title-text">Tax Command Center</h1>
          <div className="ztax-status-line">
            <span className="ztax-green-dot" />
            <span className="ztax-live-text">LIVE</span>
            <span style={{ color: '#d1d5db' }}>|</span>
            <span>{periodLabel(d.period)} · advisory view</span>
            <span style={{ color: '#d1d5db' }}>|</span>
            <span>Read {instant(d.asOf)}</span>
          </div>
        </div>
        <div className="ztax-title-actions">
          {d.actions.length === 0 ? (
            <div className="ztax-scope-badge">
              <span className="ztax-green-dot" />
              <span>Nothing needs attention</span>
            </div>
          ) : (
            <div className="ztax-attention-chip">
              <span>▲ What needs attention: {p0} P0 | {p1} P1 items require action</span>
            </div>
          )}
          <button type="button" className="ztax-btn-copper" onClick={reload}>
            Refresh
          </button>
        </div>
      </div>

      {p0 > 0 ? (
        <div className="ztax-alert-banner">
          <IconAlertCircle className="ztax-alert-icon" style={{ width: 16, height: 16 }} />
          <div className="ztax-alert-content">
            <span className="ztax-alert-title">Priority Action Center</span>
            <span className="ztax-alert-desc">
              {p0} critical {p0 === 1 ? 'item needs' : 'items need'} a person now. Resolve to stay compliant and avoid
              exposure.
            </span>
          </div>
        </div>
      ) : null}

      <div className="ztax-kpi-grid">
        <Kpi
          icon={<IconFileText style={iconStyle} />}
          title={`Determinations · ${periodLabel(d.period)}`}
          lines={moneyLines(det.tax)}
          footer={
            <>
              <span className={change?.up === false ? 'ztax-delta-label' : 'ztax-delta-green'}>
                {det.decisions} decisions{' '}
                {change === null ? null : (
                  <>
                    {change.text} <span className="ztax-delta-label">vs. {periodLabel(det.previousPeriod)}</span>
                  </>
                )}
              </span>
              {det.reviewRequired > 0 ? <span className="ztax-timestamp">{det.reviewRequired} need review</span> : null}
            </>
          }
        />
        <Kpi
          icon={<IconDollarSign style={iconStyle} />}
          title="Gross Fiscal Liability"
          lines={d.liability.length === 0 ? ['—'] : d.liability.map((b) => moneyText({ amount: b.credits, currency: b.currency }))}
          footer={
            <>
              <span className="ztax-delta-label">
                {liability === undefined
                  ? 'Nothing posted this period'
                  : `Reversed ${d.liability.map((b) => moneyText({ amount: b.debits, currency: b.currency })).join(', ')}`}
              </span>
              <span className="ztax-timestamp">TAX_COLLECTED_LIABILITY</span>
            </>
          }
        />
        <Kpi
          icon={<IconCalendar style={iconStyle} />}
          title={`Obligations Due (${d.obligations.windowDays}D)`}
          badge={d.obligations.overdue > 0 ? `${d.obligations.overdue} overdue` : undefined}
          lines={moneyLines(d.obligations.amounts)}
          footer={
            <span className="ztax-delta-label">
              {d.obligations.due} {d.obligations.due === 1 ? 'return' : 'returns'} not yet filed
            </span>
          }
        />
        <Kpi
          icon={<IconShieldCheck style={iconStyle} />}
          title="Reconciliation Integrity"
          lines={[percent(rec.matched, recTotal) === null ? '—' : `${percent(rec.matched, recTotal)}%`]}
          footer={
            <span className="ztax-delta-label">
              {rec.runs === 0 ? 'No reconciliation run yet' : `${rec.matched} of ${recTotal} items matched · ${rec.breaks} breaks`}
            </span>
          }
        />
      </div>

      <div className="ztax-split-row">
        <div className="ztax-card ztax-chart-card">
          <div>
            <div className="ztax-chart-header">
              <h3 className="ztax-card-title">Throughput & liability trend</h3>
              <span className="ztax-timestamp">
                {periodLabel(d.trend.points[0]?.period ?? d.period)} – {periodLabel(d.period)}
              </span>
            </div>
            <div className="ztax-sparkline-container">
              <svg
                viewBox="0 0 500 160"
                width="100%"
                height="100%"
                preserveAspectRatio="none"
                role="img"
                aria-label={`Decisions and tax credited per month, ${d.trend.points.map((p) => `${p.period}: ${p.decisions} decisions`).join('; ')}`}
              >
                <line x1="0" y1="30" x2="500" y2="30" stroke="#f3f4f6" strokeWidth="1" strokeDasharray="3 3" />
                <line x1="0" y1="80" x2="500" y2="80" stroke="#f3f4f6" strokeWidth="1" strokeDasharray="3 3" />
                <line x1="0" y1="130" x2="500" y2="130" stroke="#f3f4f6" strokeWidth="1" strokeDasharray="3 3" />
                <path d={paths.liability} fill="none" stroke="#5065f6" strokeWidth="2.5" strokeLinecap="round" />
                <path d={paths.throughput} fill="none" stroke="#f97e28" strokeWidth="2.5" strokeLinecap="round" />
              </svg>
            </div>
          </div>
          <div className="ztax-chart-legend">
            <div className="ztax-legend-item">
              <span className="ztax-legend-dot-blue" />
              <span>Tax credited ({d.trend.currency ?? 'none posted'})</span>
            </div>
            <div className="ztax-legend-item">
              <span className="ztax-legend-dot-orange" />
              <span>Throughput (decisions)</span>
            </div>
          </div>
        </div>

        <div className="ztax-card">
          <div className="ztax-table-header">
            <h3 className="ztax-card-title">Priority Action Center</h3>
            <span className="ztax-timestamp">{d.actions.length} open</span>
          </div>
          {d.actions.length === 0 ? (
            <p className="ztax-empty-note">Nothing in the record needs a person right now.</p>
          ) : (
            <div style={{ overflowX: 'auto' }}>
              <table className="ztax-action-table">
                <thead>
                  <tr>
                    <th>Priority</th>
                    <th>Issue</th>
                    <th>Context</th>
                    <th>Exposure</th>
                    <th>Due</th>
                    <th>Action</th>
                  </tr>
                </thead>
                <tbody>
                  {d.actions.map((item, i) => (
                    <tr key={item.subjectId ?? `${item.kind}-${i}`}>
                      <td style={{ paddingRight: 8 }}>
                        <span className={`ztax-badge-priority ${priorityClass(item.priority)}`}>{item.priority}</span>
                      </td>
                      <td style={{ fontWeight: 500, color: '#111827', paddingRight: 8 }}>{item.title}</td>
                      <td style={{ color: '#6b7280', paddingRight: 8, whiteSpace: 'nowrap' }}>{item.context}</td>
                      <td style={{ fontWeight: 700, color: '#111827', paddingRight: 8, whiteSpace: 'nowrap' }}>
                        {item.exposure === undefined ? '—' : moneyText(item.exposure)}
                      </td>
                      <td
                        style={{
                          paddingRight: 8,
                          whiteSpace: 'nowrap',
                          color: item.priority === 'P0' ? '#dc2626' : '#6b7280',
                          fontWeight: item.priority === 'P0' ? 600 : 400,
                        }}
                      >
                        {item.due === undefined ? '—' : civilDate(item.due)}
                      </td>
                      <td style={{ whiteSpace: 'nowrap' }}>
                        <span className="ztax-table-action-btn">{ACTION_VERB[item.kind]}</span>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </div>
      </div>

      <div className="ztax-gauges-grid">
        <Gauge
          title="Jurisdiction Readiness"
          subtitle={`Filing, ${jurTotal} ${jurTotal === 1 ? 'authority' : 'authorities'} in the legal matrix`}
          percentage={percent(jur.ready, jurTotal)}
          statusText={jurTotal === 0 ? 'No matrix' : 'Ready'}
          segments={[
            { label: 'Ready', count: jur.ready, color: '#10b981' },
            { label: 'At risk', count: jur.atRisk, color: '#f59e0b' },
            { label: 'Blocked', count: jur.blocked, color: '#ef4444' },
          ]}
        />
        <Gauge
          title="Reconciliation & Breaks"
          subtitle={`Latest run of ${rec.runs} ${rec.runs === 1 ? 'period' : 'periods'}`}
          percentage={percent(rec.matched, recTotal)}
          statusText={recTotal === 0 ? 'No items' : 'Reconciled'}
          segments={[
            { label: 'Reconciled', count: rec.matched, color: '#0d9488' },
            { label: 'Breaks', count: rec.breaks, color: '#f59e0b' },
            { label: 'Open', count: rec.open, color: '#ef4444' },
          ]}
        />
        <Gauge
          title="Evidence & Seals"
          subtitle={`${ev.seals} period ${ev.seals === 1 ? 'seal' : 'seals'} · immutable ledger`}
          percentage={percent(ev.sealed, ev.decisions)}
          statusText={ev.decisions === 0 ? 'No decisions' : 'Sealed'}
          segments={[
            { label: 'Sealed', count: ev.sealed, color: '#10b981' },
            { label: 'Not yet sealed', count: ev.decisions - ev.sealed, color: '#f59e0b' },
          ]}
        />
        <div className="ztax-card" style={{ display: 'flex', flexDirection: 'column', justifyContent: 'space-between' }}>
          <div>
            <div className="ztax-ai-header">
              <IconSparkles style={{ width: 16, height: 16, color: '#059669' }} />
              <span className="ztax-ai-badge">AUTHORITIES</span>
            </div>
            {jur.authorities.length === 0 ? (
              <div className="ztax-ai-body">No LegalAuthorization matrix is loaded; every filing is blocked.</div>
            ) : (
              <ul className="ztax-ai-body" style={{ margin: 0, paddingLeft: 16 }}>
                {jur.authorities.map((a) => (
                  <li key={`${a.country}-${a.authority}`}>
                    {a.country} · {a.authority}: {a.readiness.replace('_', ' ').toLowerCase()}
                    {a.reason === undefined ? '' : ` (${a.reason.replaceAll('_', ' ').toLowerCase()})`}
                  </li>
                ))}
              </ul>
            )}
          </div>
          <div className="ztax-ai-source">Resolved through the same gate every filing passes.</div>
        </div>
      </div>
    </div>
  )
}
