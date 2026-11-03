import { lazy, Suspense, useState, type FormEvent, type ReactNode } from 'react'

import type { Capabilities, Dashboard, DashboardAction, DashboardDeadline, DashboardMarket } from '@zoikotax/sdk'

import {
  IconArrowRight,
  IconBarChart,
  IconCalendar,
  IconCompass,
  IconDollarSign,
  IconFileCheck,
  IconFileText,
  IconGlobe,
  IconRotateCw,
  IconSearch,
  IconShieldCheck,
  IconSparkles,
} from '../components/common/DashboardIcons'
import { civilDate, moneyText, percent, periodLabel } from '../dashboard/format'
import { useDashboard } from '../dashboard/useDashboard'
import { countryName } from '../pages/locations/places'
import type { PageProps } from '../pages/types'
import { STATUS_LABEL } from './status'
import './home.css'

const CoverageMap = lazy(() => import('./CoverageMap'))

/**
 * Home: the fiscal overview of Navigation v2.0 §02 01 and the Home design.
 *
 * Every figure is one the cell computed and sent (ADR-0019 C1). The browser
 * counts and compares counts — markets, returns, actions — and nothing else;
 * amounts are displayed as the API wrote them, and the liability donut is
 * drawn from shares the server calculated.
 */
export const HomePage = ({ session, period, onNavigate, capabilities }: PageProps & { readonly capabilities: Capabilities | null }) => {
  const { state, reload } = useDashboard(period)
  const first = session.user.displayName.trim().split(/\s+/)[0] ?? session.user.displayName

  if (state.status === 'loading') {
    return <p className="muted" aria-live="polite">Reading the record for {periodLabel(period)}…</p>
  }
  if (state.status === 'forbidden') {
    return (
      <div className="ztax-card" role="alert">
        <h2 className="ztax-card-title">Home needs a fiscal role</h2>
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
        <h2 className="ztax-card-title">The overview could not be read</h2>
        <p className="mono muted">{state.detail}</p>
        <button type="button" className="ztax-btn-copper" onClick={reload}>Try again</button>
      </div>
    )
  }

  const d = state.data
  return (
    <div className="zh">
      <header className="zh-head">
        <div>
          <div className="zh-crumb">Home</div>
          <h1 className="zh-title">{greeting()}, {first}</h1>
          <p className="zh-sub">Your tax compliance overview for {periodLabel(d.period)}.</p>
        </div>
        <div className="zh-asof">
          <b>{civilDate(d.asOf.slice(0, 10))}</b>
          <span>
            Last updated {d.asOf.slice(11, 16)} UTC
            <button type="button" className="zh-icon-btn" onClick={reload} aria-label="Refresh the overview">
              <IconRotateCw />
            </button>
          </span>
        </div>
      </header>

      <Kpis d={d} />

      <div className="zh-row zh-row--wide">
        <Coverage d={d} advisory={capabilities?.authoritative === false} onNavigate={onNavigate} />
        <Assistant onNavigate={onNavigate} />
      </div>

      <div className="zh-row zh-row--three">
        <Actions actions={d.actions} onNavigate={onNavigate} />
        <Calendar calendar={d.calendar} asOf={d.asOf} onNavigate={onNavigate} />
        <Liability d={d} />
      </div>

      <QuickLinks onNavigate={onNavigate} />
    </div>
  )
}

function greeting(): string {
  const hour = new Date().getHours()
  return hour < 12 ? 'Good morning' : hour < 18 ? 'Good afternoon' : 'Good evening'
}

// ---------------------------------------------------------------------------
// KPI tiles
// ---------------------------------------------------------------------------

const Kpi = ({ icon, label, value, foot, tone, children }: {
  icon: ReactNode
  label: string
  value: ReactNode
  foot: ReactNode
  tone?: 'warn' | 'ok' | undefined
  children?: ReactNode
}) => (
  <div className="zh-kpi">
    <span className="zh-kpi-icon" aria-hidden="true">{icon}</span>
    <div className="zh-kpi-body">
      <span className="zh-kpi-label">{label}</span>
      <span className="zh-kpi-value">{value}</span>
      {children}
      <span className={`zh-kpi-foot${tone === undefined ? '' : ` zh-kpi-foot--${tone}`}`}>{foot}</span>
    </div>
  </div>
)

const Kpis = ({ d }: { d: Dashboard }) => {
  const c = d.coverage
  const compliance = percent(d.compliance.filed, d.compliance.due)
  const collected = d.liabilityMix.map((m) => m.collected)
  const [firstCollected, ...otherCollected] = collected
  const advisoryPilots = c.markets.filter((m) => m.reason === 'ADVISORY_CELL').length
  return (
    <section className="zh-kpis" aria-label="Key figures">
      <Kpi
        icon={<IconGlobe />}
        label="Active coverage"
        value={c.active}
        foot={c.active === 0 ? 'No market is production-ready' : 'Content, authority and filing ready'}
        tone={c.active === 0 ? undefined : 'ok'}
      />
      <Kpi
        icon={<IconCompass />}
        label="In pilot"
        value={c.pilot}
        foot={advisoryPilots > 0 ? 'Advisory output, not for filing' : 'Content in force'}
      />
      <Kpi
        icon={<IconSearch />}
        label="Research only"
        value={c.research}
        foot={c.catalogue === undefined ? 'No research catalogue loaded' : 'Not available for calculation'}
      />
      <Kpi
        icon={<IconCalendar />}
        label="Tax obligations due"
        value={d.obligations.due}
        foot={d.obligations.overdue > 0 ? `${d.obligations.overdue} overdue` : `None overdue · next ${d.obligations.windowDays} days`}
        tone={d.obligations.overdue > 0 ? 'warn' : undefined}
      />
      <Kpi
        icon={<IconFileText />}
        label="Fiscal documents issued"
        value={d.documents.issued}
        foot={`${d.documents.corrections} ${d.documents.corrections === 1 ? 'correction' : 'corrections'} · ${periodLabel(d.period)}`}
      />
      <Kpi
        icon={<IconDollarSign />}
        label="Customer tax collected"
        value={firstCollected === undefined ? '—' : moneyText(firstCollected)}
        foot={otherCollected.length > 0
          ? `+ ${otherCollected.length} more ${otherCollected.length === 1 ? 'currency' : 'currencies'} · see Tax liability`
          : `Credited · ${periodLabel(d.period)}`}
      />
      <Kpi
        icon={<IconShieldCheck />}
        label="Compliance status"
        value={compliance === null ? '—' : `${compliance}%`}
        foot={d.compliance.due === 0
          ? 'No return fell due this period'
          : `${d.compliance.filed} of ${d.compliance.due} returns filed`}
        tone={compliance === null ? undefined : compliance === 100 ? 'ok' : 'warn'}
      >
        {compliance === null ? null : (
          <span className="zh-meter" role="presentation"><i style={{ width: `${compliance}%` }} /></span>
        )}
      </Kpi>
    </section>
  )
}

// ---------------------------------------------------------------------------
// coverage
// ---------------------------------------------------------------------------

const STATUS_ORDER = { ACTIVE: 0, PILOT: 1, RESEARCH_ONLY: 2 } as const

const marketName = (m: DashboardMarket) => m.name ?? countryName(m.country)

const REASON: Readonly<Record<string, string>> = {
  ADVISORY_CELL: 'Advisory cell',
  FILING_NOT_READY: 'Filing not ready',
  NO_CONTENT_IN_FORCE: 'No content in force',
}

const Coverage = ({ d, advisory, onNavigate }: { d: Dashboard; advisory: boolean; onNavigate: PageProps['onNavigate'] }) => {
  const [all, setAll] = useState(false)
  const c = d.coverage
  const live = c.markets.filter((m) => m.status !== 'RESEARCH_ONLY')
  const rows = [...(all ? c.markets : live)].sort((a, b) =>
    STATUS_ORDER[a.status] - STATUS_ORDER[b.status] || marketName(a).localeCompare(marketName(b)))

  return (
    <section className="ztax-card zh-coverage" aria-labelledby="zh-coverage-title">
      <div className="zh-card-head">
        <span className="zh-card-icon" aria-hidden="true"><IconGlobe /></span>
        <div>
          <h2 id="zh-coverage-title" className="zh-card-title">Global coverage</h2>
          <p className="zh-card-sub">Markets by coverage state, as the cell can serve them now</p>
        </div>
      </div>
      <div className="zh-coverage-body">
        <ul className="zh-legend" aria-label="Markets per state">
          <Legend status="active" label={STATUS_LABEL.ACTIVE} count={c.active} />
          <Legend status="pilot" label={STATUS_LABEL.PILOT} count={c.pilot} />
          <Legend status="research_only" label={STATUS_LABEL.RESEARCH_ONLY} count={c.research} />
          <li className="zh-legend-item">
            <i className="zh-dot zh-dot--none" />
            <span><b>{STATUS_LABEL.NONE}</b><small>Every other country</small></span>
          </li>
        </ul>
        <div className="zh-map-wrap">
          <Suspense fallback={<div className="zh-map-loading" aria-hidden="true" />}>
            <CoverageMap markets={c.markets} />
          </Suspense>
          <p className="zh-map-note">
            {advisory ? 'This cell is advisory, so no market can be active. ' : ''}
            A research profile is never coverage.
            {c.catalogue === undefined ? '' : ` Research catalogue ${c.catalogue}.`}
          </p>
        </div>
        <div className="zh-markets">
          <div className="zh-seg" role="group" aria-label="Which markets to list">
            <button type="button" aria-pressed={!all} onClick={() => { setAll(false) }}>Content ({live.length})</button>
            <button type="button" aria-pressed={all} onClick={() => { setAll(true) }}>All ({c.markets.length})</button>
          </div>
          {rows.length === 0 ? (
            <p className="ztax-empty-note">{all ? 'The cell reports no market.' : 'No content is in force for any market.'}</p>
          ) : (
            <table className="zh-table zh-table--markets">
              <caption className="zt-visually-hidden">Markets and their coverage state</caption>
              <thead>
                <tr><th scope="col">Market</th><th scope="col">State</th></tr>
              </thead>
              <tbody>
                {rows.map((m) => (
                  <tr key={m.country}>
                    <td>
                      <span className="zh-cc">{m.country}</span>
                      {marketName(m)}
                    </td>
                    <td>
                      <span className={`zh-state zh-state--${m.status.toLowerCase()}`} title={m.reason === undefined ? undefined : REASON[m.reason]}>
                        {STATUS_LABEL[m.status]}
                      </span>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
          <button type="button" className="zh-link" onClick={() => { onNavigate('locations', { tab: 'countries' }) }}>
            View the coverage directory <IconArrowRight />
          </button>
        </div>
      </div>
    </section>
  )
}

const Legend = ({ status, label, count }: { status: string; label: string; count: number }) => (
  <li className="zh-legend-item">
    <i className={`zh-dot zh-dot--${status}`} />
    <span><b>{label}</b><small>{count} {count === 1 ? 'market' : 'markets'}</small></span>
  </li>
)

// ---------------------------------------------------------------------------
// assistant
// ---------------------------------------------------------------------------

const SUGGESTIONS = [
  'Unlimited 5G mobile plan with 20 GB hotspot',
  'Fixed broadband plan, 500 Mbps symmetric fiber',
  'SMS add-on, 1000 messages for 30 days',
  'Monthly bundle: unlimited calls, 50 SMS, 10 GB data and a streaming video subscription',
]

/**
 * The governed assistant's one capability today: proposing a service
 * classification for a catalog item. The card hands the description to the
 * Intelligence workspace, where the proposal is made and reviewed.
 */
const Assistant = ({ onNavigate }: { onNavigate: PageProps['onNavigate'] }) => {
  const [text, setText] = useState('')
  const ask = (description: string) => { onNavigate('intelligence', { draft: description }) }
  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (text.trim() !== '') ask(text.trim())
  }
  return (
    <section className="ztax-card zh-assistant" aria-labelledby="zh-assistant-title">
      <div className="zh-card-head">
        <span className="zh-card-icon zh-card-icon--ai" aria-hidden="true"><IconSparkles /></span>
        <h2 id="zh-assistant-title" className="zh-card-title">AI Assistant</h2>
        <span className="zh-beta">Beta</span>
      </div>
      <p className="zh-card-sub">Describe a catalog item and get a proposed service classification to review.</p>
      <ul className="zh-prompts">
        {SUGGESTIONS.map((s) => (
          <li key={s}>
            <button type="button" onClick={() => { ask(s) }}>{s}</button>
          </li>
        ))}
      </ul>
      <form className="zh-ask" onSubmit={submit}>
        <label className="zt-visually-hidden" htmlFor="zh-ask">Describe a catalog item</label>
        <input
          id="zh-ask"
          type="text"
          value={text}
          placeholder="Describe a catalog item…"
          onChange={(e) => { setText(e.target.value) }}
        />
        <button type="submit" aria-label="Ask for a proposal" disabled={text.trim() === ''}><IconArrowRight /></button>
      </form>
      <p className="zh-fine">Advisory only, never a tax determination. Catalog text only, never customer data.</p>
    </section>
  )
}

// ---------------------------------------------------------------------------
// actions, calendar, liability
// ---------------------------------------------------------------------------

const ACTION_VERB: Record<DashboardAction['kind'], string> = {
  OBLIGATION_OVERDUE: 'File now',
  OBLIGATION_ATTENTION: 'Resolve',
  OBLIGATION_DUE: 'Prepare',
  RECONCILIATION_BREAK: 'Investigate',
  REFUND_UNCERTAIN: 'Confirm',
  REFUND_FAILED: 'Review',
  DECISION_REVIEW: 'Review',
  WEBHOOK_DEAD: 'Replay',
}

const PRIORITY_LABEL = { P0: 'Now', P1: 'This week', P2: 'This month' } as const

const Actions = ({ actions, onNavigate }: { actions: readonly DashboardAction[]; onNavigate: PageProps['onNavigate'] }) => {
  const [only, setOnly] = useState<DashboardAction['priority'] | null>(null)
  const count = (p: DashboardAction['priority']) => actions.filter((a) => a.priority === p).length
  const rows = actions.filter((a) => only === null || a.priority === only).slice(0, 6)
  return (
    <section className="ztax-card zh-panel" aria-labelledby="zh-actions-title">
      <div className="zh-panel-head">
        <h2 id="zh-actions-title" className="zh-card-title">Action Center</h2>
        <button type="button" className="zh-link" onClick={() => { onNavigate('actions') }}>View all <IconArrowRight /></button>
      </div>
      <div className="zh-chips" role="group" aria-label="Filter by priority">
        <button type="button" aria-pressed={only === null} onClick={() => { setOnly(null) }}>All ({actions.length})</button>
        {(['P0', 'P1', 'P2'] as const).map((p) => (
          <button key={p} type="button" className={`zh-chip--${p.toLowerCase()}`} aria-pressed={only === p} onClick={() => { setOnly(p) }}>
            {PRIORITY_LABEL[p]} ({count(p)})
          </button>
        ))}
      </div>
      {rows.length === 0 ? (
        <p className="ztax-empty-note">Nothing in the record needs a person right now.</p>
      ) : (
        <div className="zh-table-scroll">
          <table className="zh-table">
            <caption className="zt-visually-hidden">Open actions, most urgent first</caption>
            <thead>
              <tr><th scope="col">Priority</th><th scope="col">Item</th><th scope="col">Due</th><th scope="col">Action</th></tr>
            </thead>
            <tbody>
              {rows.map((a, i) => (
                <tr key={a.subjectId ?? `${a.kind}-${i}`}>
                  <td><span className={`zh-pri zh-pri--${a.priority.toLowerCase()}`}>{a.priority}</span></td>
                  <td>
                    <span className="zh-item">{a.title}</span>
                    <small className="zh-item-ctx" title={a.context}>{a.context}</small>
                  </td>
                  <td className="zh-nowrap">{a.due === undefined ? '—' : civilDate(a.due)}</td>
                  <td><span className="zh-verb">{ACTION_VERB[a.kind]}</span></td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  )
}

const DAY = 24 * 60 * 60 * 1000

/** Whole days from the as-of date to a due date, both civil dates. */
function daysUntil(due: string, asOf: string): number {
  return Math.round((Date.parse(`${due}T00:00:00Z`) - Date.parse(`${asOf.slice(0, 10)}T00:00:00Z`)) / DAY)
}

function deadlineState(c: DashboardDeadline, asOf: string): { label: string; tone: string } {
  if (c.status === 'OVERDUE') return { label: 'Overdue', tone: 'late' }
  if (c.status === 'REJECTED' || c.status === 'AMENDMENT_REQUIRED' || c.status === 'DATA_REQUIRED') return { label: 'Needs attention', tone: 'late' }
  return daysUntil(c.due, asOf) <= 7 ? { label: 'Due soon', tone: 'soon' } : { label: 'On track', tone: 'ok' }
}

const Calendar = ({ calendar, asOf, onNavigate }: { calendar: readonly DashboardDeadline[]; asOf: string; onNavigate: PageProps['onNavigate'] }) => (
  <section className="ztax-card zh-panel" aria-labelledby="zh-calendar-title">
    <div className="zh-panel-head">
      <h2 id="zh-calendar-title" className="zh-card-title">Compliance calendar</h2>
      <button type="button" className="zh-link" onClick={() => { onNavigate('obligations') }}>View all <IconArrowRight /></button>
    </div>
    {calendar.length === 0 ? (
      <p className="ztax-empty-note">No unfiled return falls due in the window.</p>
    ) : (
      <ul className="zh-cal">
        {calendar.slice(0, 5).map((c) => {
          const [day, month] = civilDate(c.due).split(' ')
          const st = deadlineState(c, asOf)
          return (
            <li key={c.obligationId}>
              <span className="zh-cal-date"><b>{day}</b>{month}</span>
              <span className="zh-cal-what">
                <b>{c.type.replaceAll('_', ' ')}</b>
                <small>{c.jurisdiction}{c.exposure === undefined ? '' : ` · ${moneyText(c.exposure)}`}</small>
              </span>
              <span className={`zh-tag zh-tag--${st.tone}`}>{st.label}</span>
            </li>
          )
        })}
      </ul>
    )}
  </section>
)

const SEGMENTS = [
  { key: 'collected', share: 'collectedShare', label: 'Customer collected taxes', tone: 'collected' },
  { key: 'accrued', share: 'accruedShare', label: 'Provider-borne (accrued)', tone: 'accrued' },
  { key: 'adjustments', share: 'adjustmentsShare', label: 'Adjustments', tone: 'adjustments' },
] as const

const Liability = ({ d }: { d: Dashboard }) => {
  const [currency, setCurrency] = useState(d.liabilityMix[0]?.currency ?? '')
  const mix = d.liabilityMix.find((m) => m.currency === currency) ?? d.liabilityMix[0]
  return (
    <section className="ztax-card zh-panel" aria-labelledby="zh-liability-title">
      <div className="zh-panel-head">
        <h2 id="zh-liability-title" className="zh-card-title">Tax liability · {periodLabel(d.period)}</h2>
        {d.liabilityMix.length > 1 ? (
          <label className="zh-select">
            <span className="zt-visually-hidden">Currency</span>
            <select value={mix?.currency} onChange={(e) => { setCurrency(e.target.value) }}>
              {d.liabilityMix.map((m) => <option key={m.currency} value={m.currency}>{m.currency}</option>)}
            </select>
          </label>
        ) : null}
      </div>
      {mix === undefined ? (
        <p className="ztax-empty-note">Nothing was credited to a liability account this period.</p>
      ) : (
        <div className="zh-liab">
          <div className="zh-liab-figures">
            <span className="zh-liab-total">{moneyText(mix.collected)}</span>
            <span className="zh-liab-caption">collected from customers</span>
            <ul className="zh-liab-legend">
              {SEGMENTS.map((s) => (
                <li key={s.key}>
                  <i className={`zh-dot zh-dot--${s.tone}`} />
                  <span>{s.label}</span>
                  <b>{moneyText(mix[s.key])}</b>
                  <small>{mix[s.share]}%</small>
                </li>
              ))}
            </ul>
          </div>
          <Donut shares={SEGMENTS.map((s) => ({ tone: s.tone, share: mix[s.share] }))} />
        </div>
      )}
      <p className="zh-fine">Provider-borne tax is owed on the provider's own account; it is never shown as customer tax.</p>
    </section>
  )
}

/** A ring from the server's whole-percentage shares. */
const Donut = ({ shares }: { shares: readonly { tone: string; share: number }[] }) => {
  const r = 42
  const circumference = 2 * Math.PI * r
  let offset = 0
  return (
    <svg className="zh-donut" viewBox="0 0 110 110" aria-hidden="true">
      <circle cx="55" cy="55" r={r} className="zh-donut-track" />
      {shares.map((s) => {
        const length = (s.share / 100) * circumference
        const el = s.share === 0 ? null : (
          <circle
            key={s.tone}
            cx="55"
            cy="55"
            r={r}
            className={`zh-donut-seg zh-donut--${s.tone}`}
            strokeDasharray={`${length} ${circumference - length}`}
            strokeDashoffset={-offset}
          />
        )
        offset += length
        return el
      })}
    </svg>
  )
}

// ---------------------------------------------------------------------------
// quick links
// ---------------------------------------------------------------------------

const QuickLinks = ({ onNavigate }: { onNavigate: PageProps['onNavigate'] }) => {
  const links: readonly { label: string; sub: string; icon: ReactNode; go: () => void }[] = [
    { label: 'Tax preview', sub: 'Determine', icon: <IconCompass />, go: () => { onNavigate('determine', { tab: 'quote' }) } },
    { label: 'Filing status', sub: 'Compliance', icon: <IconShieldCheck />, go: () => { onNavigate('compliance') } },
    { label: 'Fiscal documents', sub: 'E-Invoicing', icon: <IconFileCheck />, go: () => { onNavigate('documents', { tab: 'lookup' }) } },
    { label: 'Reconciliation', sub: 'Reconcile', icon: <IconRotateCw />, go: () => { onNavigate('reconcile') } },
    { label: 'Coverage directory', sub: 'Locations', icon: <IconGlobe />, go: () => { onNavigate('locations', { tab: 'countries' }) } },
    { label: 'Reports', sub: 'Reports', icon: <IconBarChart />, go: () => { onNavigate('reports') } },
  ]
  return (
    <section className="zh-quick" aria-labelledby="zh-quick-title">
      <h2 id="zh-quick-title" className="zh-card-title">Quick links</h2>
      <ul>
        {links.map((l) => (
          <li key={l.label}>
            <button type="button" onClick={l.go}>
              <span className="zh-quick-icon" aria-hidden="true">{l.icon}</span>
              <span><b>{l.label}</b><small>{l.sub}</small></span>
            </button>
          </li>
        ))}
      </ul>
    </section>
  )
}
