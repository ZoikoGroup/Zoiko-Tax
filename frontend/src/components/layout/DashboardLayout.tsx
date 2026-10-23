import { useState, type ReactNode } from 'react'

import { ContextStrip, TopBar, type HeaderProps } from './Header'
import { Hero } from './Hero'
import { Sidebar, type SidebarProps } from './Sidebar'

export type DashboardLayoutProps = HeaderProps & SidebarProps & { readonly children: ReactNode }

export const DashboardLayout = ({ children, view, onNavigate, onSignOut, ...context }: DashboardLayoutProps) => {
  const [menu, setMenu] = useState(false)
  const go = (v: typeof view) => { setMenu(false); onNavigate(v) }
  const advisory = context.capabilities === null ? null : !context.capabilities.authoritative

  return (
    <div className="zt-app">
      <TopBar
        session={context.session}
        view={view}
        onNavigate={go}
        onSignOut={onSignOut}
        menuOpen={menu}
        onMenu={() => { setMenu((m) => !m) }}
      />
      <div className="zt-body">
        <Sidebar view={view} onNavigate={go} tenant={context.session.tenant.displayName} advisory={advisory} open={menu} />
        {menu ? <button type="button" className="zt-scrim" aria-label="Close navigation" onClick={() => { setMenu(false) }} /> : null}
        <div className="zt-main">
          <Hero view={view} />
          <ContextStrip {...context} />
          <main className="ztax-dashboard-viewport zt-viewport">{children}</main>
          <footer className="zt-foot">General information until A4. Confirm applicable rules and effective dates before acting.</footer>
        </div>
      </div>
    </div>
  )
}
