import { useState, type ReactNode } from 'react'

import {
  IconActivity,
  IconArchive,
  IconBarChart,
  IconBell,
  IconChevronDown,
  IconCompass,
  IconCreditCard,
  IconDatabase,
  IconEye,
  IconFileCheck,
  IconFileText,
  IconGitFork,
  IconGlobe,
  IconHome,
  IconPlus,
  IconRotateCw,
  IconSettings,
  IconShield,
  IconSparkles,
} from '../common/DashboardIcons'
import { Wordmark } from './BrandMark'

/** The views this build serves. Every other entry has no backend yet. */
export type View =
  | 'dashboard'
  | 'actions'
  | 'determine'
  | 'obligations'
  | 'compliance'
  | 'remittance'
  | 'shadow'
  | 'migration'
  | 'reports'
  | 'reports-demo'
  | 'reconcile'
  | 'documents'
  | 'evidence'
  | 'content'
  | 'locations'
  | 'intelligence'
  | 'admin'

/** A screen: a view, and the tab inside it when the view has tabs. */
export interface NavTarget {
  readonly view: View
  readonly tab?: string
}

/** Where the shell is: the screen, and the menu entry that opened it. */
export interface Route extends NavTarget {
  readonly navId?: string
  /** Text another screen hands over, e.g. an item to classify. */
  readonly draft?: string
  /** Bumped on every navigation, so a page re-applies a tab it was sent to. */
  readonly seq: number
}

export interface NavChild {
  readonly id: string
  readonly label: string
  /** Absent for a destination this build does not serve yet. */
  readonly target?: NavTarget
}

export interface NavDomain {
  readonly id: string
  readonly label: string
  readonly icon: ReactNode
  readonly children: readonly NavChild[]
}

const at = (view: View, tab?: string): NavTarget => (tab === undefined ? { view } : { view, tab })

/**
 * The fifteen platform domains and their headings, Sidebar & Navigation
 * Architecture v2.0 §02. Names are stable; a heading without a target is a
 * destination this build has no screen for, shown disabled with the reason
 * rather than hidden, so the map of the product stays legible.
 */
export const NAV_DOMAINS: readonly NavDomain[] = [
  {
    id: 'home', label: 'Home', icon: <IconHome />, children: [
      { id: 'home.overview', label: 'Fiscal Overview', target: at('dashboard') },
      { id: 'home.coverage', label: 'My Coverage', target: at('locations', 'overview') },
      { id: 'home.readiness', label: 'Jurisdiction Readiness', target: at('locations', 'countries') },
      { id: 'home.health', label: 'System Health' },
      { id: 'home.activity', label: 'Recent Activity' },
    ],
  },
  {
    id: 'actions', label: 'Action Center', icon: <IconBell />, children: [
      { id: 'actions.tasks', label: 'My Tasks', target: at('actions') },
      { id: 'actions.exceptions', label: 'Tax Exceptions' },
      { id: 'actions.approvals', label: 'Pending Approvals' },
      { id: 'actions.notices', label: 'Regulatory Notices' },
      { id: 'actions.cases', label: 'Support Cases' },
    ],
  },
  {
    id: 'determine', label: 'Determine', icon: <IconCompass />, children: [
      { id: 'determine.preview', label: 'Tax Preview', target: at('determine', 'quote') },
      { id: 'determine.transactions', label: 'Tax Transactions', target: at('determine', 'decision') },
      { id: 'determine.classification', label: 'Service Classification', target: at('intelligence') },
      { id: 'determine.situs', label: 'Situs & Place of Supply', target: at('locations', 'try') },
      { id: 'determine.liability', label: 'Liability & Recovery' },
      { id: 'determine.corrections', label: 'Corrections', target: at('determine', 'refunds') },
    ],
  },
  {
    id: 'obligations', label: 'Obligations', icon: <IconFileText />, children: [
      { id: 'obligations.register', label: 'Obligation Register', target: at('obligations') },
      { id: 'obligations.contributions', label: 'Provider Contributions' },
      { id: 'obligations.calendar', label: 'Compliance Calendar' },
      { id: 'obligations.workpapers', label: 'Regulatory Workpapers' },
      { id: 'obligations.exceptions', label: 'Obligation Exceptions' },
    ],
  },
  {
    id: 'compliance', label: 'Compliance', icon: <IconShield />, children: [
      { id: 'compliance.preparation', label: 'Filing Preparation', target: at('compliance') },
      { id: 'compliance.returns', label: 'Regulatory Returns' },
      { id: 'compliance.acks', label: 'Authority Acknowledgements' },
      { id: 'compliance.registrations', label: 'Registrations & Exemptions' },
      { id: 'compliance.status', label: 'Compliance Status' },
    ],
  },
  {
    id: 'reconcile', label: 'Reconcile', icon: <IconRotateCw />, children: [
      { id: 'reconcile.subledger', label: 'Tax Control Subledger', target: at('determine', 'subledger') },
      { id: 'reconcile.billing', label: 'Billing-to-Tax', target: at('reconcile') },
      { id: 'reconcile.erp', label: 'ERP Reconciliation' },
      { id: 'reconcile.filing', label: 'Filing-to-Remittance' },
      { id: 'reconcile.variances', label: 'Variances' },
    ],
  },
  {
    id: 'remittance', label: 'Remittance', icon: <IconCreditCard />, children: [
      { id: 'remittance.authorisations', label: 'Payment Authorisations', target: at('remittance') },
      { id: 'remittance.queue', label: 'Remittance Queue' },
      { id: 'remittance.status', label: 'Payment Status' },
      { id: 'remittance.settlement', label: 'Settlement Reconciliation' },
      { id: 'remittance.evidence', label: 'Evidence' },
    ],
  },
  {
    id: 'e-invoicing', label: 'E-Invoicing / CTC', icon: <IconFileCheck />, children: [
      { id: 'ctc.applicability', label: 'Country Applicability' },
      { id: 'ctc.processing', label: 'Invoice Processing', target: at('documents', 'issue') },
      { id: 'ctc.validation', label: 'Format Validation' },
      { id: 'ctc.submissions', label: 'Authority Submissions' },
      { id: 'ctc.rejections', label: 'Rejections & Corrections' },
      { id: 'ctc.archive', label: 'Fiscal Archive', target: at('documents', 'lookup') },
    ],
  },
  {
    id: 'shadow', label: 'Shadow', icon: <IconEye />, children: [
      { id: 'shadow.runs', label: 'Comparison Runs', target: at('shadow') },
      { id: 'shadow.discrepancies', label: 'Tax Discrepancies' },
      { id: 'shadow.historical', label: 'Historical Comparisons' },
      { id: 'shadow.assurance', label: 'Assurance Cases' },
      { id: 'shadow.reports', label: 'Reports' },
    ],
  },
  {
    id: 'evidence', label: 'Evidence', icon: <IconArchive />, children: [
      { id: 'evidence.centre', label: 'Evidence Centre', target: at('evidence', 'seals') },
      { id: 'evidence.audit', label: 'Decision Audit Trail', target: at('evidence', 'inclusion') },
      { id: 'evidence.provenance', label: 'Source Provenance' },
      { id: 'evidence.replay', label: 'Historical Replay' },
      { id: 'evidence.exports', label: 'Verified Exports' },
    ],
  },
  {
    id: 'intelligence', label: 'Intelligence', icon: <IconSparkles />, children: [
      { id: 'intelligence.feed', label: 'Regulatory Change Feed' },
      { id: 'intelligence.impact', label: 'Tax Impact Analysis' },
      { id: 'intelligence.coverage', label: 'Coverage Intelligence' },
      { id: 'intelligence.assistant', label: 'AI Assistant', target: at('intelligence') },
      { id: 'intelligence.recommendations', label: 'Review Recommendations' },
    ],
  },
  {
    id: 'content', label: 'Content', icon: <IconDatabase />, children: [
      { id: 'content.packs', label: 'Jurisdiction Rule Packs', target: at('content') },
      { id: 'content.codes', label: 'Service & Levy Codes' },
      { id: 'content.sources', label: 'Legal Source Library' },
      { id: 'content.crosswalks', label: 'Vendor Crosswalks' },
      { id: 'content.approvals', label: 'Rule Approvals' },
      { id: 'content.releases', label: 'Content Releases' },
    ],
  },
  {
    id: 'migration', label: 'Migration', icon: <IconGitFork />, children: [
      { id: 'migration.import', label: 'Data Import', target: at('migration') },
      { id: 'migration.mapping', label: 'Mapping Validation' },
      { id: 'migration.parallel', label: 'Parallel Runs' },
      { id: 'migration.cutover', label: 'Cutover Readiness' },
      { id: 'migration.rollback', label: 'Rollback & History' },
    ],
  },
  {
    id: 'reports', label: 'Reports', icon: <IconBarChart />, children: [
      { id: 'reports.liability', label: 'Tax Liability', target: at('reports') },
      { id: 'reports.obligations', label: 'Regulatory Obligations' },
      { id: 'reports.filing', label: 'Filing Reports' },
      { id: 'reports.reconciliation', label: 'Reconciliation' },
      { id: 'reports.analytics', label: 'Executive Analytics (demo)', target: at('reports-demo') },
      { id: 'reports.exports', label: 'Audit Exports' },
    ],
  },
  {
    id: 'administration', label: 'Administration', icon: <IconSettings />, children: [
      { id: 'admin.organisation', label: 'Organisation & Legal Entities' },
      { id: 'admin.users', label: 'Users & Permissions', target: at('admin') },
      { id: 'admin.entitlements', label: 'Entitlements' },
      { id: 'admin.integrations', label: 'Integrations & APIs' },
      { id: 'admin.subscriptions', label: 'Subscriptions & Usage' },
      { id: 'admin.security', label: 'Security Settings' },
    ],
  },
]

const ALL_CHILDREN = NAV_DOMAINS.flatMap((d) => d.children.map((c) => ({ domain: d, child: c })))

const matches = (t: NavTarget | undefined, route: NavTarget) =>
  t !== undefined && t.view === route.view && (t.tab === undefined || t.tab === route.tab)

/**
 * The menu entry a route stands on. The entry that opened it wins while it
 * still describes the screen; otherwise the first entry that does, so a
 * screen reached by a link inside a page still lights its place in the menu.
 */
export function activeEntry(route: NavTarget & { readonly navId?: string }): { domain: NavDomain; child: NavChild } | undefined {
  const opened = ALL_CHILDREN.find((e) => e.child.id === route.navId)
  if (opened !== undefined && matches(opened.child.target, route)) {
    return opened
  }
  return ALL_CHILDREN.find((e) => matches(e.child.target, route) && e.child.target?.tab === route.tab)
    ?? ALL_CHILDREN.find((e) => matches(e.child.target, route))
}

/** The first screen a domain opens on. */
function entryOf(domain: NavDomain): NavChild | undefined {
  return domain.children.find((c) => c.target !== undefined)
}

/** The domain and heading a route sits in, for the hero's eyebrow. */
export function placeOf(route: NavTarget & { readonly navId?: string }): { group: string; label: string } {
  const e = activeEntry(route) ?? ALL_CHILDREN.find((x) => x.child.target?.view === route.view)
  return { group: e?.domain.label ?? 'Home', label: e?.child.label ?? 'Home' }
}

export type Navigate = (target: NavTarget, navId?: string) => void

interface RailProps {
  readonly route: Route
  readonly onNavigate: Navigate
  readonly advisory: boolean | null
  readonly open: boolean
}

const NOT_BUILT = 'Not available in this build yet'

export const Sidebar = ({ route, onNavigate, advisory, open }: RailProps) => {
  const here = activeEntry(route)
  // Domains the user opened by hand; the active one is always open.
  const [expanded, setExpanded] = useState<ReadonlySet<string>>(() => new Set())
  const isOpen = (d: NavDomain) => expanded.has(d.id) || here?.domain.id === d.id

  const toggle = (d: NavDomain) => {
    setExpanded((prev) => {
      const next = new Set(prev)
      if (next.has(d.id)) next.delete(d.id)
      else next.add(d.id)
      return next
    })
  }

  const openDomain = (d: NavDomain) => {
    const first = entryOf(d)
    if (here?.domain.id === d.id || first?.target === undefined) {
      toggle(d)
      return
    }
    onNavigate(first.target, first.id)
  }

  return (
    <aside className={`zt-rail${open ? ' zt-rail--open' : ''}`} aria-label="Workspaces">
      <button type="button" className="zt-rail-logo" onClick={() => { onNavigate({ view: 'dashboard' }, 'home.overview') }} aria-label="ZoikoTax home">
        <Wordmark />
      </button>

      <button type="button" className="zt-rail-cta" onClick={() => { onNavigate({ view: 'determine', tab: 'quote' }, 'determine.preview') }}>
        <IconPlus />
        <span>New determination</span>
      </button>

      <nav className="zt-rail-nav">
        {NAV_DOMAINS.map((d) => {
          const active = here?.domain.id === d.id
          const shown = isOpen(d)
          const listId = `zt-rail-${d.id}`
          return (
            <div key={d.id} className="zt-rail-group">
              <div className={`zt-rail-domain${active ? ' zt-rail-domain--on' : ''}`}>
                <button type="button" className="zt-rail-item" onClick={() => { openDomain(d) }}>
                  {d.icon}
                  <span>{d.label}</span>
                </button>
                <button
                  type="button"
                  className="zt-rail-toggle"
                  aria-expanded={shown}
                  aria-controls={listId}
                  aria-label={`${shown ? 'Collapse' : 'Expand'} ${d.label}`}
                  onClick={() => { toggle(d) }}
                >
                  <IconChevronDown />
                </button>
              </div>
              {shown ? (
                <ul id={listId} className="zt-rail-sub">
                  {d.children.map((c) => {
                    const on = here?.child.id === c.id
                    return (
                      <li key={c.id}>
                        <button
                          type="button"
                          className={`zt-rail-subitem${on ? ' zt-rail-subitem--on' : ''}`}
                          aria-current={on ? 'page' : undefined}
                          aria-disabled={c.target === undefined ? true : undefined}
                          title={c.target === undefined ? NOT_BUILT : undefined}
                          onClick={() => { if (c.target !== undefined) onNavigate(c.target, c.id) }}
                        >
                          {c.label}
                          {c.target === undefined ? <span className="zt-visually-hidden"> ({NOT_BUILT})</span> : null}
                        </button>
                      </li>
                    )
                  })}
                </ul>
              ) : null}
            </div>
          )
        })}
      </nav>

      <div className="zt-rail-foot">
        <div className="zt-rail-card">
          {advisory === false ? <IconActivity /> : <IconGlobe />}
          <div>
            <b>{advisory === null ? 'Checking output…' : advisory ? 'Advisory output' : 'Authoritative output'}</b>
            <span>{advisory === false ? 'Decisions may be filed' : 'Not for filing before A4'}</span>
          </div>
        </div>
        <div className="zt-rail-legal">ZoikoTax is a trading name of Zoiko Tech Inc.</div>
      </div>
    </aside>
  )
}
