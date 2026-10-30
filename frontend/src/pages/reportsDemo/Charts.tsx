/**
 * The Reports (demo) charts: plain SVG, no chart library.
 *
 * Every number drawn as a label is the string the demo service sent. Bar
 * proportions are supplied by the server as per-mille integers (ADR-0019 C1:
 * the server provides derived figures; the browser only positions them).
 * Each bar, column and legend entry carries an "Explain" control.
 */
import type { KeyboardEvent } from 'react'

import { formatMoney, pct } from './derive'
import type { Bucket, ChartGeometry, Figure, TrendPoint } from './model'

/** A keyboard-reachable SVG "Explain" button, positioned in tenths of a percent. */
const ExplainPill = ({
  left, width, y, label, onActivate,
}: { left: bigint; width: bigint; y: string; label: string; onActivate: () => void }) => (
  <g
    className="rd-pill"
    role="button"
    tabIndex={0}
    aria-label={label}
    onClick={onActivate}
    onKeyDown={(e: KeyboardEvent<SVGGElement>) => {
      if (e.key === 'Enter' || e.key === ' ') {
        e.preventDefault()
        onActivate()
      }
    }}
  >
    <rect x={pct(left)} y={y} width={pct(width)} height="20" rx="4" />
    <text x={pct(left + width / 2n)} y={y} dy="14" textAnchor="middle">Explain</text>
  </g>
)

interface ChartProps<T> {
  readonly highlightId: string | null
  readonly onExplain: (item: T) => void
}

// ---------------------------------------------------------------------------
// Chart 1 -- Tax Liability by Major Authority: horizontal bars, sorted descending
// ---------------------------------------------------------------------------

const BAR_ROW = 34
const BAR_LABEL_END = 280n
const BAR_MAX = 300n

export const AuthorityChart = ({
  bars, geometry, highlightId, onExplain,
}: ChartProps<Figure> & { readonly bars: readonly Figure[]; readonly geometry: ChartGeometry | null }) => {
  return (
    <svg
      className="rd-svg"
      role="group"
      aria-label="Tax liability by major authority"
      width="100%"
      height={bars.length * BAR_ROW + 8}
    >
      {bars.map((bar, i) => {
        const y = i * BAR_ROW + 4
        const permille = geometry?.authorityBars[bar.id] ?? 0n
        const length = (BAR_MAX * permille) / 1000n
        return (
          <g key={bar.id} id={`rd-${bar.id}`} className={bar.id === highlightId ? 'rd-svg-hl' : undefined}>
            <text className="rd-svg-label" x="0" y={y + 17}>{bar.authority ?? bar.label}</text>
            <rect className="rd-bar" x={pct(BAR_LABEL_END)} y={y + 3} width={pct(length)} height="20" rx="3" />
            <text className="rd-svg-value" x={pct(BAR_LABEL_END + length + 10n)} y={y + 17}>
              {formatMoney(bar.value, bar.unit)}
            </text>
            <ExplainPill
              left={840n}
              width={150n}
              y={String(y + 3)}
              label={`Explain ${bar.authority ?? bar.label}`}
              onActivate={() => { onExplain(bar) }}
            />
          </g>
        )
      })}
    </svg>
  )
}

// ---------------------------------------------------------------------------
// Chart 2 -- Compliance & Filing Pipeline: one stacked bar and a four-bucket legend
// ---------------------------------------------------------------------------

export const PipelineChart = ({
  buckets, geometry, highlightId, onExplain,
}: ChartProps<Bucket> & { readonly buckets: readonly Bucket[]; readonly geometry: ChartGeometry | null }) => {
  const segments = buckets.map((bucket, index) => {
    const seg = geometry?.pipelineSegments[bucket.id]
    const left = seg?.start ?? 0n
    const width = seg?.width ?? 0n
    return { bucket, index, left, width }
  })
  const filings = buckets[0]?.totalFilings
  return (
    <div>
      <svg className="rd-svg" role="img" aria-label="Filing pipeline, one stacked bar" width="100%" height="40">
        {segments
          .filter((s) => s.width > 0n)
          .map((s) => (
            <rect
              key={s.bucket.id}
              className={`rd-seg rd-seg-${s.index + 1}${s.bucket.id === highlightId ? ' rd-svg-hl-seg' : ''}`}
              x={pct(s.left)}
              y="6"
              width={pct(s.width)}
              height="28"
            >
              <title>{`${s.bucket.label}: ${s.bucket.count} (${s.bucket.percent}%)`}</title>
            </rect>
          ))}
      </svg>
      {filings === undefined ? null : <p className="rd-muted">{filings} filings in total</p>}
      <ul className="rd-legend">
        {buckets.map((bucket, index) => (
          <li key={bucket.id} id={`rd-${bucket.id}`} className={bucket.id === highlightId ? 'rd-hl' : undefined}>
            <span className={`rd-swatch rd-seg-${index + 1}`} aria-hidden="true" />
            <span className="rd-legend-label">{bucket.label}</span>
            <span className="mono">{bucket.count} · {bucket.percent}%</span>
            <button
              type="button"
              className="ztax-btn-quiet rd-explain"
              onClick={() => { onExplain(bucket) }}
              aria-label={`Explain ${bucket.label}`}
            >
              Explain
            </button>
          </li>
        ))}
      </ul>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Chart 3 -- Total tax assessed, last 6 months: a column chart
// ---------------------------------------------------------------------------

const PLOT_BASELINE = 720n
const PLOT_MAX = 580n

export const TrendChart = ({
  points, geometry, highlightId, onExplain,
}: ChartProps<TrendPoint> & { readonly points: readonly TrendPoint[]; readonly geometry: ChartGeometry | null }) => {
  if (points.length === 0) {
    return <p className="rd-muted">No trend points in the fixture.</p>
  }
  const slot = 1000n / BigInt(points.length)
  const columnWidth = (slot * 6n) / 10n
  const pillWidth = (slot * 7n) / 10n
  return (
    <svg
      className="rd-svg"
      role="group"
      aria-label="Total tax assessed, last 6 months"
      width="100%"
      height="230"
    >
      <line className="rd-axis" x1="0%" x2="100%" y1={pct(PLOT_BASELINE)} y2={pct(PLOT_BASELINE)} />
      {points.map((point, i) => {
        const left = BigInt(i) * slot
        const permille = geometry?.trendPoints[point.id] ?? 0n
        const height = (PLOT_MAX * permille) / 1000n
        const top = PLOT_BASELINE - height
        const centre = left + slot / 2n
        return (
          <g key={point.id} id={`rd-${point.id}`} className={point.id === highlightId ? 'rd-svg-hl' : undefined}>
            <rect
              className="rd-bar"
              x={pct(left + (slot - columnWidth) / 2n)}
              y={pct(top)}
              width={pct(columnWidth)}
              height={pct(height)}
              rx="3"
            />
            <text className="rd-svg-value" x={pct(centre)} y={pct(top - 25n)} textAnchor="middle">
              {formatMoney(point.value, point.unit)}
            </text>
            <text className="rd-svg-label" x={pct(centre)} y="79%" textAnchor="middle">{point.month}</text>
            <ExplainPill
              left={left + (slot - pillWidth) / 2n}
              width={pillWidth}
              y="190"
              label={`Explain ${point.label}`}
              onActivate={() => { onExplain(point) }}
            />
          </g>
        )
      })}
    </svg>
  )
}
