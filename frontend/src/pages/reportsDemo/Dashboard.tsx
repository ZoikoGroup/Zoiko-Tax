/**
 * The Reports (demo) dashboard, drawn entirely from the demo fixture.
 *
 * Every figure, label and row is a string the demo service sent -- this file
 * types no number. "Explain" asks the demo AI service for a plain-language
 * explanation of the rows behind a figure; the AI never edits or computes
 * anything on the page, and if it is unavailable the page works regardless.
 */
import { useEffect, useRef, useState, type KeyboardEvent, type ReactNode } from 'react'

import { Card, Columns, PageHeader, Status } from '../../ui/kit'
import '../obligations.css'
import './reportsDemo.css'
import { requestExplain } from './api'
import { AuthorityChart, PipelineChart, TrendChart } from './Charts'
import {
  FORECAST_TOOLTIP,
  PAID_TOOLTIP,
  SETTLED_TOOLTIP,
  TABS,
  csvTable,
  formatAmount,
  formatFigure,
  formatMoney,
  tabOfId,
  toCsv,
  type TabId,
} from './derive'
import { ExplainPanel, type PanelState } from './ExplainPanel'
import type { ExplainTarget, Figure, Fixture } from './model'

// ---------------------------------------------------------------------------
// header (also shown while the fixture loads, or when it cannot be fetched)
// ---------------------------------------------------------------------------

export const DemoHeader = ({ actions }: { actions?: ReactNode }) => (
  <PageHeader
    title="Reports & Executive Compliance Dashboard"
    subtitle="Synthetic register: liabilities by authority, period and status — filing calendar and remittance register"
    actions={
      <>
        <span className="rd-badge">DEMO FIXTURE</span>
        <span className="rd-badge-note">Synthetic demo data, not tax data</span>
        {actions}
      </>
    }
  />
)

const ExplainButton = ({ label, onClick }: { label: string; onClick: () => void }) => (
  <button type="button" className="ztax-btn-quiet rd-explain" aria-label={`Explain ${label}`} onClick={onClick}>
    Explain
  </button>
)

/** A status badge; the tooltip text is also present for screen readers. */
const StatusTip = ({ value, tip }: { value: string; tip: string | undefined }) =>
  tip === undefined ? (
    <Status value={value} />
  ) : (
    <span title={tip}>
      <Status value={value} /> <span aria-hidden="true">ⓘ</span>
      <span className="rd-sr"> — {tip}</span>
    </span>
  )

function download(filename: string, text: string): void {
  const url = URL.createObjectURL(new Blob([text], { type: 'text/csv;charset=utf-8' }))
  const link = document.createElement('a')
  link.href = url
  link.download = filename
  link.click()
  URL.revokeObjectURL(url)
}

// ---------------------------------------------------------------------------
// tier 1 -- KPI cards
// ---------------------------------------------------------------------------

const KpiCard = ({
  figure, fixture, first, highlightId, onExplain,
}: {
  figure: Figure
  fixture: Fixture
  first: boolean
  highlightId: string | null
  onExplain: (target: ExplainTarget) => void
}) => {
  const vs = fixture.vsLastMonth
  const arrow = vs.direction === 'decrease' ? '▼' : '▲'
  const ids = first ? [figure.id, vs.id] : [figure.id]
  return (
    <section
      id={`rd-${figure.id}`}
      className={`rd-kpi${ids.includes(highlightId ?? '') ? ' rd-hl' : ''}`}
      aria-label={figure.label}
    >
      <div className="rd-kpi-head">
        <h3 className="rd-kpi-label">{figure.label}</h3>
        <ExplainButton
          label={figure.label}
          onClick={() => { onExplain({ title: figure.label, kind: 'report-figure', ids }) }}
        />
      </div>
      <p className="rd-kpi-value mono">{formatFigure(figure.value, figure.unit)}</p>

      {first ? (
        <p className="rd-kpi-sub">
          <span className="rd-delta">
            {arrow} {vs.value}%
            {/* vs.unit is '%' (the figure is a percentage); the amount is in the fixture's currency */}
            {vs.changeAmount === undefined ? '' : ` (${formatMoney(vs.changeAmount, fixture.currency)})`}
          </span>{' '}
          vs last month{' '}
          <span className="rd-forecast" title={FORECAST_TOOLTIP}>
            ⓘ (Month-on-month)<span className="rd-sr"> — {FORECAST_TOOLTIP}</span>
          </span>
        </p>
      ) : null}
      {figure.filedCount !== undefined && figure.totalFilings !== undefined ? (
        <p className="rd-kpi-sub">{figure.filedCount} of {figure.totalFilings} filings filed &amp; acknowledged</p>
      ) : null}
      {figure.windowDays !== undefined && figure.asOf !== undefined ? (
        <p className="rd-kpi-sub">Due within {figure.windowDays} days of {figure.asOf} (the fixture&apos;s as-of date)</p>
      ) : null}
      {figure.authorities !== undefined ? <p className="rd-kpi-sub">{figure.authorities.join(' · ')}</p> : null}
    </section>
  )
}

// ---------------------------------------------------------------------------
// tier 3 -- the four register tabs
// ---------------------------------------------------------------------------

interface TabProps {
  readonly fixture: Fixture
  readonly highlightId: string | null
  readonly onExplain: (target: ExplainTarget) => void
  readonly onCite: (id: string) => void
}

const rowClass = (id: string, highlightId: string | null) => (id === highlightId ? 'ztax-row-selected' : undefined)

const ObligationsTab = ({ fixture, highlightId, onExplain }: TabProps) => (
  <table className="ztax-action-table">
    <thead>
      <tr>
        <th>Type</th><th>Jurisdiction</th><th>Authority</th><th>Period</th><th>Status</th>
        <th>Due Date</th><th>Assessed Amt</th><th><span className="rd-sr">Explain</span></th>
      </tr>
    </thead>
    <tbody>
      {fixture.obligations.map((o) => (
        <tr key={o.id} id={`rd-row-${o.id}`} className={rowClass(o.id, highlightId)}>
          <td>{o.type}<div className="rd-muted mono">{o.id}</div></td>
          <td className="mono">{o.jurisdiction}</td>
          <td>{o.authority}</td>
          <td className="mono">{o.period}</td>
          <td><StatusTip value={o.status} tip={o.status === 'PAID' ? PAID_TOOLTIP : undefined} /></td>
          <td className="mono">{o.dueDate}</td>
          <td>
            <span className="mono">{formatMoney(o.amount, o.currency)}</span>
            {o.currency === fixture.currency ? null : (
              <div className="rd-muted">
                ≈ {formatMoney(o.amountUsd, fixture.currency)}
                {o.fxRate === undefined ? '' : ` at the fixed demo rate ${o.fxRate}`}
              </div>
            )}
          </td>
          <td>
            <ExplainButton
              label={o.id}
              onClick={() => { onExplain({ title: `${o.id} · ${o.type}, ${o.authority}`, kind: 'obligation-row', ids: [o.id] }) }}
            />
          </td>
        </tr>
      ))}
    </tbody>
  </table>
)

const FilingTab = ({ fixture, highlightId, onExplain, onCite }: TabProps) => (
  <table className="ztax-action-table">
    <thead>
      <tr><th>Bucket</th><th>Count</th><th>Percent</th><th>Obligations</th><th><span className="rd-sr">Explain</span></th></tr>
    </thead>
    <tbody>
      {fixture.buckets.map((b) => (
        <tr key={b.id} id={`rd-row-${b.id}`} className={rowClass(b.id, highlightId)}>
          <td>{b.label}</td>
          <td className="mono">{b.count}</td>
          <td className="mono">{b.percent}%</td>
          <td>
            {b.sourceIds.map((id) => (
              <button
                key={id}
                type="button"
                className="ztax-btn-quiet mono rd-cite"
                onClick={() => { onCite(id) }}
                aria-label={`Show ${id} in the Obligations Register`}
              >
                {id}
              </button>
            ))}
          </td>
          <td>
            <ExplainButton
              label={b.label}
              onClick={() => { onExplain({ title: b.label, kind: 'filing-bucket', ids: [b.id] }) }}
            />
          </td>
        </tr>
      ))}
    </tbody>
  </table>
)

const PaymentsTab = ({ fixture, highlightId, onExplain, onCite }: TabProps) => (
  <table className="ztax-action-table">
    <thead>
      <tr>
        <th>Payment id</th><th>Obligation</th><th>Amount</th><th>Currency</th><th>Date</th><th>Status</th>
        <th><span className="rd-sr">Explain</span></th>
      </tr>
    </thead>
    <tbody>
      {fixture.payments.map((p) => (
        <tr key={p.id} id={`rd-row-${p.id}`} className={rowClass(p.id, highlightId)}>
          <td className="mono">{p.id}</td>
          <td>
            <button
              type="button"
              className="ztax-btn-quiet mono rd-cite"
              onClick={() => { onCite(p.obligationId) }}
              aria-label={`Show ${p.obligationId} in the Obligations Register`}
            >
              {p.obligationId}
            </button>
          </td>
          <td className="mono">{formatAmount(p.amount)}</td>
          <td className="mono">{p.currency}</td>
          <td className="mono">{p.date}</td>
          <td><StatusTip value={p.status} tip={p.status === 'SETTLED' ? SETTLED_TOOLTIP : undefined} /></td>
          <td>
            <ExplainButton
              label={p.id}
              onClick={() => { onExplain({ title: `${p.id} · payment for ${p.obligationId}`, kind: 'payment-row', ids: [p.id] }) }}
            />
          </td>
        </tr>
      ))}
    </tbody>
  </table>
)

const ShadowTab = ({ fixture, highlightId, onExplain, onCite }: TabProps) => (
  <table className="ztax-action-table">
    <thead>
      <tr>
        <th>Id</th><th>Obligation</th><th>Comparator value</th><th>Zoiko value</th><th>Absolute variance</th>
        <th>Result</th><th>Case state</th><th><span className="rd-sr">Explain</span></th>
      </tr>
    </thead>
    <tbody>
      {fixture.shadow.map((s) => (
        <tr key={s.id} id={`rd-row-${s.id}`} className={rowClass(s.id, highlightId)}>
          <td className="mono">{s.id}</td>
          <td>
            <button
              type="button"
              className="ztax-btn-quiet mono rd-cite"
              onClick={() => { onCite(s.obligationId) }}
              aria-label={`Show ${s.obligationId} in the Obligations Register`}
            >
              {s.obligationId}
            </button>
          </td>
          <td className="mono">{formatMoney(s.comparatorValue, s.currency)}</td>
          <td className="mono">{formatMoney(s.zoikoValue, s.currency)}</td>
          <td className="mono">{formatMoney(s.absoluteVariance, s.currency)}</td>
          <td><Status value={s.result} /></td>
          <td>{s.caseState === '' ? '—' : <Status value={s.caseState} />}</td>
          <td>
            <ExplainButton
              label={s.id}
              onClick={() => { onExplain({ title: `${s.id} · variance for ${s.obligationId}`, kind: 'shadow-variance', ids: [s.id] }) }}
            />
          </td>
        </tr>
      ))}
    </tbody>
  </table>
)

const TAB_VIEWS: Readonly<Record<TabId, (props: TabProps) => ReactNode>> = {
  obligations: ObligationsTab,
  filing: FilingTab,
  payments: PaymentsTab,
  shadow: ShadowTab,
}

// ---------------------------------------------------------------------------
// the page body
// ---------------------------------------------------------------------------

export const Dashboard = ({
  fixture, initialTab = 'obligations',
}: { fixture: Fixture; initialTab?: TabId }) => {
  const [tab, setTab] = useState<TabId>(initialTab)
  const [highlightId, setHighlightId] = useState<string | null>(null)
  const [panel, setPanel] = useState<PanelState>({ status: 'closed' })
  const inFlight = useRef<AbortController | null>(null)

  useEffect(() => () => { inFlight.current?.abort() }, [])

  useEffect(() => {
    if (highlightId === null) {
      return
    }
    const element = document.getElementById(`rd-row-${highlightId}`) ?? document.getElementById(`rd-${highlightId}`)
    element?.scrollIntoView({ block: 'nearest' })
  }, [highlightId, tab])

  const explain = (target: ExplainTarget) => {
    inFlight.current?.abort()
    const controller = new AbortController()
    inFlight.current = controller
    setPanel({ status: 'loading', target })
    void requestExplain(target, controller.signal).then((result) => {
      if (controller.signal.aborted || result.status === 'aborted') {
        return
      }
      setPanel(
        result.status === 'reply'
          ? { status: 'done', target, reply: result.reply }
          : { status: 'unavailable', target },
      )
    })
  }

  const cite = (id: string) => {
    const target = tabOfId(id)
    if (target !== null) {
      setTab(target)
    }
    setHighlightId(id)
  }

  // Left/Right (wrapping), Home and End move to a tab and select it; only the
  // selected tab is in the Tab order (roving tabindex).
  const onTabKeyDown = (e: KeyboardEvent<HTMLButtonElement>) => {
    const index = TABS.findIndex((t) => t.id === tab)
    const last = TABS.length - 1
    let next: number
    switch (e.key) {
      case 'ArrowRight': next = index === last ? 0 : index + 1; break
      case 'ArrowLeft': next = index === 0 ? last : index - 1; break
      case 'Home': next = 0; break
      case 'End': next = last; break
      default: return
    }
    e.preventDefault()
    const target = TABS[next]
    if (target !== undefined) {
      setTab(target.id)
      document.getElementById(`rd-tab-${target.id}`)?.focus()
    }
  }

  const kpis = fixture.figures.filter((f) => f.group === 'kpi')
  const bars = fixture.figures.filter((f) => f.group === 'by_authority')
  const TabView = TAB_VIEWS[tab]

  return (
    <div className="ztax-page">
      <DemoHeader
        actions={
          <button
            type="button"
            className="ztax-btn-copper"
            onClick={() => { download(`reports-demo-${tab}.csv`, toCsv(csvTable(fixture, tab))) }}
          >
            Download CSV
          </button>
        }
      />

      <section className="rd-kpis" aria-label="Key figures">
        {kpis.map((figure, i) => (
          <KpiCard
            key={figure.id}
            figure={figure}
            fixture={fixture}
            first={i === 0}
            highlightId={highlightId}
            onExplain={explain}
          />
        ))}
      </section>

      <Columns>
        <Card title="Tax Liability by Major Authority">
          <AuthorityChart
            bars={bars}
            geometry={fixture.chartGeometry}
            highlightId={highlightId}
            onExplain={(bar) => { explain({ title: bar.label, kind: 'report-figure', ids: [bar.id] }) }}
          />
        </Card>
        <Card title="Compliance & Filing Pipeline">
          <PipelineChart
            buckets={fixture.buckets}
            geometry={fixture.chartGeometry}
            highlightId={highlightId}
            onExplain={(bucket) => { explain({ title: bucket.label, kind: 'filing-bucket', ids: [bucket.id] }) }}
          />
        </Card>
      </Columns>

      <Card title="Total tax assessed, last 6 months">
        <TrendChart
          points={fixture.trend}
          geometry={fixture.chartGeometry}
          highlightId={highlightId}
          onExplain={(point) => { explain({ title: point.label, kind: 'trend-point', ids: [point.id] }) }}
        />
      </Card>

      <Card>
        <div className="rd-tabs" role="tablist" aria-label="Register">
          {TABS.map((t) => (
            <button
              key={t.id}
              type="button"
              role="tab"
              id={`rd-tab-${t.id}`}
              aria-selected={tab === t.id}
              aria-controls="rd-tabpanel"
              tabIndex={tab === t.id ? 0 : -1}
              className={`rd-tab${tab === t.id ? ' rd-tab--on' : ''}`}
              onClick={() => { setTab(t.id) }}
              onKeyDown={onTabKeyDown}
            >
              {t.label}
            </button>
          ))}
        </div>
        <div id="rd-tabpanel" role="tabpanel" aria-labelledby={`rd-tab-${tab}`}>
          <TabView fixture={fixture} highlightId={highlightId} onExplain={explain} onCite={cite} />
        </div>
        <p className="rd-muted">
          Rate: {fixture.fxDemoRate.rate} {fixture.fxDemoRate.from}→{fixture.fxDemoRate.to} ({fixture.fxDemoRate.label}).
          As of {fixture.asOf}. {fixture.disclaimer}.
        </p>
      </Card>

      <ExplainPanel
        state={panel}
        onClose={() => {
          inFlight.current?.abort()
          setPanel({ status: 'closed' })
        }}
        onCite={cite}
      />
    </div>
  )
}
