import { useEffect, useMemo, useState } from 'react'

import type { Capabilities } from '@zoikotax/sdk'

import { AdminPanel } from './admin/AdminPanel'
import { SignIn } from './admin/SignIn'
import { useSession } from './admin/useSession'
import { DashboardLayout } from './components/layout/DashboardLayout'
import type { Navigate, Route } from './components/layout/Sidebar'
import { recentPeriods } from './dashboard/format'
import { ActionCenterPage } from './pages/ActionCenterPage'
import { HomePage } from './home/HomePage'
import { CompliancePage } from './pages/CompliancePage'
import { ContentPage } from './pages/ContentPage'
import { DeterminePage } from './pages/DeterminePage'
import { DocumentsPage } from './pages/DocumentsPage'
import { EvidencePage } from './pages/EvidencePage'
import { IntelligencePage } from './pages/IntelligencePage'
import { LocationsPage } from './pages/LocationsPage'
import { MigrationPage } from './pages/MigrationPage'
import { ObligationsPage } from './pages/ObligationsPage'
import { ReconcilePage } from './pages/ReconcilePage'
import { RemittancePage } from './pages/RemittancePage'
import { ReportsDemoPage } from './pages/ReportsDemoPage'
import { ReportsPage } from './pages/ReportsPage'
import { ShadowPage } from './pages/ShadowPage'
import type { PageProps } from './pages/types'
import { CellHealth } from './platform/CellHealth'
import { ztax } from './platform/client'

/**
 * The secret from a set-password link, read once when the page loads. The
 * link carries it in the fragment, which never reaches a server or a Referer;
 * it is taken out of the address bar at once, so it does not linger in the
 * history or get copied along with the URL.
 */
const LINK = ((): string | null => {
  const match = /^#set-password=(.+)$/.exec(window.location.hash)
  if (!match?.[1]) {
    return null
  }
  window.history.replaceState(null, '', window.location.pathname + window.location.search)
  try {
    return decodeURIComponent(match[1])
  } catch {
    return null
  }
})()

/** Where a session starts, and where signing out returns. */
const HOME: Route = { view: 'dashboard', navId: 'home.overview', seq: 0 }

/**
 * ZoikoTax shell.
 *
 * Everything behind the session: the Tax Command Center reads the tenant's
 * record and administration changes it, so neither renders for an anonymous
 * visitor. The session is the HttpOnly cookie the cell set at sign-in; the
 * client learns who it is from GET /v1/auth/session and holds nothing else
 * (ADR-0019 C8).
 */
export function App() {
  const { state, session, detail, refresh, signIn, signOut } = useSession()
  const [route, setRoute] = useState<Route>(HOME)
  const periods = useMemo(() => recentPeriods(new Date(), 12), [])
  const [period, setPeriod] = useState(periods[0] ?? '')
  const [capabilities, setCapabilities] = useState<Capabilities | null>(null)
  const [link, setLink] = useState(LINK)

  useEffect(() => {
    if (state !== 'signed-in') {
      return
    }
    const controller = new AbortController()
    void ztax.getCapabilities(controller.signal).then((result) => {
      if (result.ok) {
        setCapabilities(result.data)
      }
    })
    return () => { controller.abort() }
  }, [state])

  if (state === 'checking') {
    return (
      <main className="shell shell--narrow">
        <p className="muted" aria-live="polite">Checking your session…</p>
      </main>
    )
  }

  if (state === 'unreachable') {
    return (
      <main className="shell shell--narrow">
        <section className="panel" aria-labelledby="unreachable-heading">
          <h2 id="unreachable-heading">The cell is not responding</h2>
          <p className="panel__note">
            ZoikoTax is unavailable until it does. This is not a sign-in failure, and retrying your password will not
            help.
          </p>
          <p className="mono muted">{detail}</p>
          <button type="button" className="btn" onClick={() => { void refresh() }}>
            Try again
          </button>
          <CellHealth />
        </section>
      </main>
    )
  }

  // A set-password link is honoured whoever is signed in: setting the password
  // revokes the account's sessions, and the session check that follows sorts
  // out what to show next.
  if (link !== null || state === 'anonymous' || session === null) {
    return (
      <SignIn
        onSignIn={signIn}
        link={link}
        onLinkDone={() => {
          setLink(null)
          void refresh()
        }}
      />
    )
  }

  const navigate: Navigate = (target, navId) => {
    setRoute((r) => ({ ...target, ...(navId === undefined ? {} : { navId }), seq: r.seq + 1 }))
  }
  const openView: PageProps['onNavigate'] = (view, options = {}) => {
    setRoute((r) => ({ view, ...options, seq: r.seq + 1 }))
  }
  // A tab chosen inside a page moves the menu, not the page: no new seq.
  const onTabChange = (tab: string) => { setRoute((r) => ({ ...r, tab })) }

  return (
    <DashboardLayout
      route={route}
      onNavigate={navigate}
      session={session}
      capabilities={capabilities}
      period={period}
      periods={periods}
      onPeriodChange={setPeriod}
      onSignOut={() => {
        setRoute(HOME)
        void signOut()
      }}
    >
      {(() => {
        const page: PageProps = { session, period, onNavigate: openView, route, onTabChange }
        switch (route.view) {
          case 'admin':
            return <AdminPanel session={session} onSignOut={signOut} onRefresh={refresh} />
          case 'actions':
            return <ActionCenterPage {...page} />
          case 'determine':
            return <DeterminePage {...page} />
          case 'obligations':
            return <ObligationsPage {...page} />
          case 'compliance':
            return <CompliancePage {...page} />
          case 'reconcile':
            return <ReconcilePage {...page} />
          case 'remittance':
            return <RemittancePage {...page} />
          case 'shadow':
            return <ShadowPage {...page} />
          case 'migration':
            return <MigrationPage {...page} />
          case 'reports':
            return <ReportsPage />
          case 'reports-demo':
            return <ReportsDemoPage />
          case 'documents':
            return <DocumentsPage {...page} />
          case 'evidence':
            return <EvidencePage {...page} />
          case 'content':
            return <ContentPage {...page} />
          case 'locations':
            return <LocationsPage {...page} />
          case 'intelligence':
            return <IntelligencePage {...page} />
          case 'dashboard':
            return <HomePage {...page} capabilities={capabilities} />
        }
      })()}
    </DashboardLayout>
  )
}

export default App
