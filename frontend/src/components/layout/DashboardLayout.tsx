import type { ReactNode } from 'react'

import { Header, type HeaderProps } from './Header'
import { Sidebar, type SidebarProps } from './Sidebar'

export type DashboardLayoutProps = HeaderProps & SidebarProps & { readonly children: ReactNode }

export const DashboardLayout = ({ children, view, onNavigate, ...header }: DashboardLayoutProps) => (
  <div className="ztax-layout">
    <Sidebar view={view} onNavigate={onNavigate} />
    <div className="ztax-main-container">
      <Header {...header} />
      <main className="ztax-dashboard-viewport">{children}</main>
    </div>
  </div>
)
