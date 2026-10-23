import type { ReactNode } from 'react'

import {
  IconActivity,
  IconArchive,
  IconBarChart,
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
  IconSliders,
} from '../common/DashboardIcons'
import { BrandMark } from './BrandMark'

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
  | 'reconcile'
  | 'documents'
  | 'evidence'
  | 'content'
  | 'locations'
  | 'intelligence'
  | 'admin'

export interface NavItem {
  readonly id: string
  readonly label: string
  readonly icon: ReactNode
  /** The view the item opens; absent for a workspace not built yet. */
  readonly view?: View
}

export const NAV_GROUPS: readonly { title: string; items: readonly NavItem[] }[] = [
  {
    title: 'Workspace',
    items: [
      { id: 'home', label: 'Home', icon: <IconHome />, view: 'dashboard' },
      { id: 'action-center', label: 'Action Center', icon: <IconActivity />, view: 'actions' },
      { id: 'determine', label: 'Determine', icon: <IconCompass />, view: 'determine' },
      { id: 'obligations', label: 'Obligations', icon: <IconFileText />, view: 'obligations' },
      { id: 'compliance', label: 'Compliance', icon: <IconShield />, view: 'compliance' },
      { id: 'reconcile', label: 'Reconcile', icon: <IconRotateCw />, view: 'reconcile' },
    ],
  },
  {
    title: 'Settle & prove',
    items: [
      { id: 'remittance', label: 'Remittance', icon: <IconCreditCard />, view: 'remittance' },
      { id: 'e-invoicing', label: 'E-Invoicing / CTC', icon: <IconFileCheck />, view: 'documents' },
      { id: 'shadow-assurance', label: 'Shadow Assurance', icon: <IconEye />, view: 'shadow' },
      { id: 'evidence', label: 'Evidence', icon: <IconArchive />, view: 'evidence' },
    ],
  },
  {
    title: 'Intelligence',
    items: [
      { id: 'intelligence', label: 'Intelligence', icon: <IconSettings />, view: 'intelligence' },
      { id: 'content', label: 'Content', icon: <IconDatabase />, view: 'content' },
      { id: 'locations', label: 'Location taxation', icon: <IconGlobe />, view: 'locations' },
      { id: 'reports', label: 'Reports', icon: <IconBarChart />, view: 'reports' },
    ],
  },
  {
    title: 'Platform',
    items: [
      { id: 'migration', label: 'Migration', icon: <IconGitFork />, view: 'migration' },
      { id: 'administration', label: 'Administration', icon: <IconSliders />, view: 'admin' },
    ],
  },
]

/** The group a view sits in, for the hero's eyebrow. */
export function groupOf(view: View): string {
  return NAV_GROUPS.find((g) => g.items.some((i) => i.view === view))?.title ?? 'Workspace'
}

export function labelOf(view: View): string {
  return NAV_GROUPS.flatMap((g) => g.items).find((i) => i.view === view)?.label ?? 'Home'
}

export interface SidebarProps {
  readonly view: View
  readonly onNavigate: (view: View) => void
}

interface RailProps extends SidebarProps {
  readonly tenant: string
  readonly advisory: boolean | null
  readonly open: boolean
}

export const Sidebar = ({ view, onNavigate, tenant, advisory, open }: RailProps) => (
  <aside className={`zt-rail${open ? ' zt-rail--open' : ''}`} aria-label="Workspaces">
    <div className="zt-rail-brand">
      <span className="zt-rail-mark"><BrandMark /></span>
      <div>
        <div className="zt-rail-name">ZoikoTax<br />Command Center</div>
        <div className="zt-rail-sub">{tenant}</div>
      </div>
    </div>

    <button type="button" className="zt-rail-cta" onClick={() => { onNavigate('determine') }}>
      <IconPlus />
      <span>New determination</span>
    </button>

    <nav className="zt-rail-nav">
      {NAV_GROUPS.map((group) => (
        <div key={group.title} className="zt-rail-group">
          <div className="zt-rail-group-title">{group.title}</div>
          {group.items.map((item) => {
            const target = item.view
            const isActive = target === view
            return (
              <button
                type="button"
                key={item.id}
                disabled={target === undefined}
                title={target === undefined ? 'No backend for this workspace yet' : undefined}
                aria-current={isActive ? 'page' : undefined}
                onClick={() => { if (target !== undefined) onNavigate(target) }}
                className={`zt-rail-item${isActive ? ' zt-rail-item--on' : ''}`}
              >
                {item.icon}
                <span>{item.label}</span>
              </button>
            )
          })}
        </div>
      ))}
    </nav>

    <div className="zt-rail-foot">
      <div className="zt-rail-card">
        <IconGlobe />
        <div>
          <b>{advisory === null ? 'Checking output…' : advisory ? 'Advisory output' : 'Authoritative output'}</b>
          <span>{advisory === false ? 'Decisions may be filed' : 'Not for filing before A4'}</span>
        </div>
      </div>
      <div className="zt-rail-legal">ZoikoTax is a trading name of Zoiko Tech Inc.</div>
    </div>
  </aside>
)
