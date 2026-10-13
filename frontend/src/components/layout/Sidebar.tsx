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
  IconHome,
  IconRotateCw,
  IconSettings,
  IconShield,
  IconSliders,
} from '../common/DashboardIcons'

/** The views this build serves. Every other entry has no backend yet. */
export type View =
  | 'dashboard'
  | 'actions'
  | 'determine'
  | 'obligations'
  | 'reconcile'
  | 'documents'
  | 'evidence'
  | 'content'
  | 'intelligence'
  | 'admin'

interface NavItem {
  readonly id: string
  readonly label: string
  readonly icon: ReactNode
  /** The view the item opens; absent for a workspace not built yet. */
  readonly view?: View
}

const NAV_GROUPS: readonly { title: string; items: readonly NavItem[] }[] = [
  {
    title: 'WORKSPACE',
    items: [
      { id: 'home', label: 'Home', icon: <IconHome />, view: 'dashboard' },
      { id: 'action-center', label: 'Action Center', icon: <IconActivity />, view: 'actions' },
      { id: 'determine', label: 'Determine', icon: <IconCompass />, view: 'determine' },
      { id: 'obligations', label: 'Obligations', icon: <IconFileText />, view: 'obligations' },
      { id: 'compliance', label: 'Compliance', icon: <IconShield /> },
      { id: 'reconcile', label: 'Reconcile', icon: <IconRotateCw />, view: 'reconcile' },
    ],
  },
  {
    title: 'SETTLE & PROVE',
    items: [
      { id: 'remittance', label: 'Remittance', icon: <IconCreditCard /> },
      { id: 'e-invoicing', label: 'E-Invoicing / CTC', icon: <IconFileCheck />, view: 'documents' },
      { id: 'shadow-assurance', label: 'Shadow Assurance', icon: <IconEye /> },
      { id: 'evidence', label: 'Evidence', icon: <IconArchive />, view: 'evidence' },
    ],
  },
  {
    title: 'INTELLIGENCE',
    items: [
      { id: 'intelligence', label: 'Intelligence', icon: <IconSettings />, view: 'intelligence' },
      { id: 'content', label: 'Content', icon: <IconDatabase />, view: 'content' },
      { id: 'reports', label: 'Reports', icon: <IconBarChart /> },
    ],
  },
  {
    title: 'PLATFORM',
    items: [
      { id: 'migration', label: 'Migration', icon: <IconGitFork /> },
      { id: 'administration', label: 'Administration', icon: <IconSliders />, view: 'admin' },
    ],
  },
]

export interface SidebarProps {
  readonly view: View
  readonly onNavigate: (view: View) => void
}

export const Sidebar = ({ view, onNavigate }: SidebarProps) => (
  <aside className="ztax-sidebar">
    <div className="ztax-sidebar-logo">
      <img src="/zoikotax_logo_client.png" alt="ZoikoTax" />
    </div>

    <nav className="ztax-sidebar-nav" aria-label="Workspaces">
      {NAV_GROUPS.map((group) => (
        <div key={group.title} className="ztax-sidebar-group">
          <div className="ztax-sidebar-group-title">{group.title}</div>
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
                className={`ztax-nav-item ${isActive ? 'active' : ''}`}
              >
                {item.icon}
                <span>{item.label}</span>
              </button>
            )
          })}
        </div>
      ))}
    </nav>

    <div className="ztax-sidebar-footer">
      <div>ZoikoTax is a trading name of Zoiko Tech Inc.</div>
      <div>Confidential - Product & Engineering</div>
    </div>
  </aside>
)
