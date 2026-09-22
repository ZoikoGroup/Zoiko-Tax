import { useId, useState } from 'react'

import { describeError } from '../platform/api'

/**
 * Sign-in.
 *
 * The tenant handle is a field because sign-in is per tenant: the same address
 * may hold accounts in two tenants and they share nothing. Inferring the tenant
 * from the hostname is the usual alternative and it is a W3 decision, not one to
 * take here by accident.
 *
 * The form deliberately says nothing about which part was wrong. The service
 * returns one error for an unknown tenant, an unknown address and a wrong
 * password, and repeating that single message is the frontend's share of not
 * building an account-enumeration oracle.
 */
export interface SignInProps {
  readonly onSignIn: (tenant: string, email: string, password: string) => Promise<void>
}

export function SignIn({ onSignIn }: SignInProps) {
  const [tenant, setTenant] = useState('')
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const tenantId = useId()
  const emailId = useId()
  const passwordId = useId()
  const errorId = useId()

  return (
    <main className="shell shell--narrow">
      <header className="shell__header">
        <h1>ZoikoTax</h1>
        <p className="shell__subtitle">Tenant administration</p>
      </header>

      <form
        className="panel"
        onSubmit={(event) => {
          event.preventDefault()
          setBusy(true)
          setError('')
          void onSignIn(tenant.trim(), email.trim(), password)
            .catch((cause: unknown) => { setError(describeError(cause)) })
            .finally(() => { setBusy(false) })
        }}
      >
        <h2>Sign in</h2>

        <div className="field">
          <label htmlFor={tenantId}>Tenant</label>
          <input
            id={tenantId}
            name="tenant"
            value={tenant}
            autoComplete="organization"
            required
            onChange={(e) => { setTenant(e.target.value) }}
          />
        </div>

        <div className="field">
          <label htmlFor={emailId}>Email</label>
          <input
            id={emailId}
            name="email"
            type="email"
            value={email}
            autoComplete="username"
            required
            onChange={(e) => { setEmail(e.target.value) }}
          />
        </div>

        <div className="field">
          <label htmlFor={passwordId}>Password</label>
          <input
            id={passwordId}
            name="password"
            type="password"
            value={password}
            autoComplete="current-password"
            required
            onChange={(e) => { setPassword(e.target.value) }}
            aria-describedby={error ? errorId : undefined}
          />
        </div>

        {/* aria-live so assistive technology announces the failure without the
            focus having to move to it (C6). */}
        <p className="field__error" id={errorId} role="alert" aria-live="polite">
          {error}
        </p>

        <button type="submit" className="btn btn--primary" disabled={busy}>
          {busy ? 'Signing in…' : 'Sign in'}
        </button>
      </form>
    </main>
  )
}
