import { useEffect, useMemo, useState } from 'react'

import type { Capabilities } from '@zoikotax/sdk'

import { AdminPanel } from './admin/AdminPanel'
import { SignIn } from './admin/SignIn'
import { useSession } from './admin/useSession'
import { DashboardLayout } from './components/layout/DashboardLayout'
import type { View } from './components/layout/Sidebar'
import { recentPeriods } from './dashboard/format'
import { ActionCenterPage } from './pages/ActionCenterPage'
import { ClientDashboard } from './pages/ClientDashboard'
import { ContentPage } from './pages/ContentPage'
import { DeterminePage } from './pages/DeterminePage'
import { DocumentsPage } from './pages/DocumentsPage'
import { EvidencePage } from './pages/EvidencePage'
import { IntelligencePage } from './pages/IntelligencePage'
import { ObligationsPage } from './pages/ObligationsPage'
import { ReconcilePage } from './pages/ReconcilePage'
import { CellHealth } from './platform/CellHealth'
import { ztax } from './platform/client'

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
  const [view, setView] = useState<View>('dashboard')
  const periods = useMemo(() => recentPeriods(new Date(), 12), [])
  const [period, setPeriod] = useState(periods[0] ?? '')
  const [capabilities, setCapabilities] = useState<Capabilities | null>(null)

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

  if (state === 'anonymous' || session === null) {
    return <SignIn onSignIn={signIn} />
  }

  return (
    <DashboardLayout
      view={view}
      onNavigate={setView}
      session={session}
      capabilities={capabilities}
      period={period}
      periods={periods}
      onPeriodChange={setPeriod}
      onSignOut={() => {
        setView('dashboard')
        void signOut()
      }}
    >
      {(() => {
        const page = { session, period, onNavigate: setView }
        switch (view) {
          case 'admin':
            return <AdminPanel session={session} onSignOut={signOut} onRefresh={refresh} />
          case 'actions':
            return <ActionCenterPage {...page} />
          case 'determine':
            return <DeterminePage {...page} />
          case 'obligations':
            return <ObligationsPage {...page} />
          case 'reconcile':
            return <ReconcilePage {...page} />
          case 'documents':
            return <DocumentsPage {...page} />
          case 'evidence':
            return <EvidencePage {...page} />
          case 'content':
            return <ContentPage {...page} />
          case 'intelligence':
            return <IntelligencePage {...page} />
          case 'dashboard':
            return <ClientDashboard period={period} />
        }
      })()}
    </DashboardLayout>
  )
}

export default App
