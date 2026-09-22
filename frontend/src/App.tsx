import { AdminPanel } from './admin/AdminPanel'
import { SignIn } from './admin/SignIn'
import { useSession } from './admin/useSession'
import { CellHealth } from './platform/CellHealth'

/**
 * The shell.
 *
 * ADR-0019 defers the 38-screen inventory to W3 lane N alongside UX-001. What
 * exists here is the administration workspace, which is not part of that
 * inventory: it is the surface the tenant-authentication work needs in order to
 * be usable, and it presupposes none of the interaction decisions UX-001 owns.
 *
 * The four states below are the whole router. There is no route table because
 * there are no routes: the server decides whether you are signed in, and this
 * component renders the answer. That is a consequence of ADR-0019 C8 rather
 * than a simplification — with no client-side session state there is nothing to
 * navigate between before the server has answered.
 */
export function App() {
  const { state, session, detail, refresh, signIn, signOut } = useSession()

  // C3: an unreachable cell renders as unreachable. Showing a sign-in form here
  // would invite the user to enter a password that has nowhere to go, and then
  // blame them for it.
  if (state === 'unreachable') {
    return (
      <main className="shell shell--narrow">
        <header className="shell__header">
          <h1>ZoikoTax</h1>
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
    )
  }

  // The one legitimate wait: the server has not yet said who this is. It is a
  // single round trip on load, and it is the price of keeping identity out of
  // browser storage entirely.
  if (state === 'checking') {
    return (
      <main className="shell shell--narrow">
        <p className="muted" aria-live="polite">Checking your session…</p>
      </main>
    )
  }

  if (state === 'anonymous' || session === null) {
    return <SignIn onSignIn={signIn} />
  }

  return <AdminPanel session={session} onSignOut={signOut} onRefresh={refresh} />
}
