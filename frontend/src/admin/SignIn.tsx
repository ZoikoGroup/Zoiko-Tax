import { useId, useState } from 'react'

import { BrandMark } from '../components/layout/BrandMark'
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
    <main className="zt-signin">
      <section className="zt-signin-brand" aria-label="ZoikoTax">
        <div className="zt-rail-brand">
          <span className="zt-rail-mark"><BrandMark /></span>
          <div>
            <div className="zt-rail-name">ZoikoTax<br />Command Center</div>
            <div className="zt-rail-sub">Tenant administration</div>
          </div>
        </div>
        <div>
          <div className="zt-signin-eyebrow">ZoikoTax Intelligence</div>
          <h1>Answers for a complex tax world.</h1>
          <p>Telecom tax clarity, across jurisdictions.</p>
        </div>
        <ol className="zt-signin-flow" aria-label="Fiscal pipeline">
          <li>Determine</li><li>Obligate</li><li>Comply</li><li>Reconcile</li><li>Prove</li>
        </ol>
      </section>

      <form
        className="zt-signin-form"
        onSubmit={(event) => {
          event.preventDefault()
          setBusy(true)
          setError('')
          void onSignIn(tenant.trim(), email.trim(), password)
            .catch((cause: unknown) => { setError(describeError(cause)) })
            .finally(() => { setBusy(false) })
        }}
      >
        <img className="zt-signin-logo" src="/zoikotax-logo.png" alt="" />
        <h2>Sign in</h2>
        <p className="zt-signin-lead">Use your tenant handle and the address your administrator registered.</p>

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

        <button type="submit" className="zt-btn zt-btn--primary zt-signin-submit" disabled={busy}>
          {busy ? 'Signing in…' : 'Sign in'}
        </button>
      </form>
    </main>
  )
}
