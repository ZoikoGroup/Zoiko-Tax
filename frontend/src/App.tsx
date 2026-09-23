import React, { useState } from 'react';
import { DashboardLayout } from './components/layout/DashboardLayout';
import { ClientDashboard } from './pages/ClientDashboard';
import { AdminPanel } from './admin/AdminPanel';
import { SignIn } from './admin/SignIn';
import { useSession } from './admin/useSession';
import { CellHealth } from './platform/CellHealth';

/**
 * ZoikoTax Shell.
 *
 * Renders the authoritative Tax Command Center Dashboard, with support for the
 * administrative session workspace when requested.
 */
export function App() {
  const [viewMode, setViewMode] = useState<'dashboard' | 'admin'>('dashboard');
  const { state, session, detail, refresh, signIn, signOut } = useSession();

  if (viewMode === 'admin') {
    if (state === 'unreachable') {
      return (
        <main className="shell shell--narrow">
          <header className="shell__header">
            <h1>ZoikoTax Administration</h1>
            <button
              type="button"
              className="btn btn--secondary mt-2"
              onClick={() => setViewMode('dashboard')}
            >
              ← Back to Tax Command Center
            </button>
          </header>
          <section className="panel" aria-labelledby="unreachable-heading">
            <h2 id="unreachable-heading">The cell is not responding</h2>
            <p className="panel__note">
              Administration is unavailable until it does. This is not a sign-in failure, and
              retrying your password will not help.
            </p>
            <p className="mono muted">{detail}</p>
            <button type="button" className="btn" onClick={() => { void refresh() }}>
              Try again
            </button>
            <CellHealth />
          </section>
        </main>
      );
    }

    if (state === 'checking') {
      return (
        <main className="shell shell--narrow">
          <p className="muted" aria-live="polite">Checking your session…</p>
        </main>
      );
    }

    if (state === 'anonymous' || session === null) {
      return (
        <div>
          <div className="p-3 text-right bg-white border-b">
            <button
              type="button"
              className="text-xs text-blue-600 hover:underline cursor-pointer"
              onClick={() => setViewMode('dashboard')}
            >
              ← Back to Tax Command Center
            </button>
          </div>
          <SignIn onSignIn={signIn} />
        </div>
      );
    }

    return (
      <div>
        <div className="p-3 text-right bg-white border-b">
          <button
            type="button"
            className="text-xs text-blue-600 hover:underline cursor-pointer"
            onClick={() => setViewMode('dashboard')}
          >
            ← View Tax Command Center
          </button>
        </div>
        <AdminPanel session={session} onSignOut={signOut} onRefresh={refresh} />
      </div>
    );
  }

  return (
    <DashboardLayout>
      <ClientDashboard />
    </DashboardLayout>
  );
}

export default App;
