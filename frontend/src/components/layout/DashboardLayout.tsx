import { useState, type ReactNode } from 'react'

import { ContextStrip, TopBar, type HeaderProps } from './Header'
import { Hero } from './Hero'
import { Sidebar, type Navigate, type Route } from './Sidebar'

export type DashboardLayoutProps = HeaderProps & {
  readonly route: Route
  readonly onNavigate: Navigate
  readonly children: ReactNode
}

export const DashboardLayout = ({ children, route, onNavigate, onSignOut, ...context }: DashboardLayoutProps) => {
  const [menu, setMenu] = useState(false)
  const go: Navigate = (target, navId) => { setMenu(false); onNavigate(target, navId) }
  const advisory = context.capabilities === null ? null : !context.capabilities.authoritative

  return (
    <div className="zt-app">
      <Sidebar route={route} onNavigate={go} advisory={advisory} open={menu} />
      <TopBar
        session={context.session}
        onNavigate={go}
        onSignOut={onSignOut}
        menuOpen={menu}
        onMenu={() => { setMenu((m) => !m) }}
      />
      <div className="zt-body">
        {menu ? <button type="button" className="zt-scrim" aria-label="Close navigation" onClick={() => { setMenu(false) }} /> : null}
        <div className="zt-main">
          {/* Home carries its own greeting; every other workspace its band. */}
          {route.view === 'dashboard' ? null : <Hero route={route} />}
          <ContextStrip {...context} />
          <main className="ztax-dashboard-viewport zt-viewport">{children}</main>
          <footer className="zt-foot">General information until A4. Confirm applicable rules and effective dates before acting.</footer>
        </div>
      </div>
    </div>
  )
}
