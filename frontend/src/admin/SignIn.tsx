import { useId, useState, type FormEvent, type ReactNode } from 'react'

import { BrandMark } from '../components/layout/BrandMark'
import { api, ApiError, describeError } from '../platform/api'

/**
 * Sign-in, and the three things a person without a session can do besides:
 * ask for a password reset, set a password from an emailed link, and write to
 * us.
 *
 * The tenant handle is a field because sign-in is per tenant: the same address
 * may hold accounts in two tenants and they share nothing. Inferring the tenant
 * from the hostname is the usual alternative and it is a W3 decision, not one to
 * take here by accident.
 *
 * The forms deliberately say nothing about which part was wrong. The service
 * returns one error for an unknown tenant, an unknown address and a wrong
 * password, and one answer to every reset request; repeating that is the
 * frontend's share of not building an account-enumeration oracle.
 */
export interface SignInProps {
  readonly onSignIn: (tenant: string, email: string, password: string) => Promise<void>
  /** The secret from a set-password link, when the page was opened from one. */
  readonly link?: string | null | undefined
  /** Called once the link has set a password, or been abandoned. */
  readonly onLinkDone?: (() => void) | undefined
}

type Mode = 'sign-in' | 'forgot' | 'contact' | 'set-password'

export function SignIn({ onSignIn, link, onLinkDone }: SignInProps) {
  const [mode, setMode] = useState<Mode>(link ? 'set-password' : 'sign-in')
  const [tenant, setTenant] = useState('')
  const [email, setEmail] = useState('')
  const [notice, setNotice] = useState('')

  const show = (next: Mode, message = '') => {
    setNotice(message)
    setMode(next)
  }

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

      {mode === 'sign-in' && (
        <SignInForm
          tenant={tenant}
          email={email}
          notice={notice}
          onTenant={setTenant}
          onEmail={setEmail}
          onSignIn={onSignIn}
          onForgot={() => { show('forgot') }}
          onContact={() => { show('contact') }}
        />
      )}
      {mode === 'forgot' && (
        <ForgotForm
          tenant={tenant}
          email={email}
          onTenant={setTenant}
          onEmail={setEmail}
          onBack={(message) => { show('sign-in', message) }}
        />
      )}
      {mode === 'contact' && <ContactForm onBack={(message) => { show('sign-in', message) }} />}
      {mode === 'set-password' && link && (
        <SetPasswordForm
          link={link}
          onDone={(result) => {
            onLinkDone?.()
            if (result) {
              setTenant(result.tenant)
              setEmail(result.email)
              show('sign-in', 'Your password is set. Sign in with it now.')
            } else {
              show('forgot')
            }
          }}
          onCancel={() => {
            onLinkDone?.()
            show('sign-in')
          }}
        />
      )}
    </main>
  )
}

// ---------------------------------------------------------------------------

function Card({ title, lead, onSubmit, children }: {
  title: string
  lead: string
  onSubmit: (event: FormEvent) => void
  children: ReactNode
}) {
  return (
    <form className="zt-signin-form" onSubmit={onSubmit}>
      <img className="zt-signin-logo" src="/zoikotax-logo.png" alt="" />
      <h2>{title}</h2>
      <p className="zt-signin-lead">{lead}</p>
      {children}
    </form>
  )
}

function Field({ label, children, hint }: { label: string; children: (id: string) => ReactNode; hint?: string }) {
  const id = useId()
  return (
    <div className="field">
      <label htmlFor={id}>{label}</label>
      {children(id)}
      {hint && <p className="field__hint">{hint}</p>}
    </div>
  )
}

/** aria-live so assistive technology announces the outcome without the focus
 *  having to move to it (C6). */
function Problem({ id, text }: { id: string; text: string }) {
  return <p className="field__error" id={id} role="alert" aria-live="polite">{text}</p>
}

function useSubmit() {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const run = (event: FormEvent, action: () => Promise<void>) => {
    event.preventDefault()
    setBusy(true)
    setError('')
    void action()
      .catch((cause: unknown) => { setError(describeError(cause)) })
      .finally(() => { setBusy(false) })
  }
  return { busy, error, setError, run }
}

// ---------------------------------------------------------------------------

function SignInForm({ tenant, email, notice, onTenant, onEmail, onSignIn, onForgot, onContact }: {
  tenant: string
  email: string
  notice: string
  onTenant: (v: string) => void
  onEmail: (v: string) => void
  onSignIn: SignInProps['onSignIn']
  onForgot: () => void
  onContact: () => void
}) {
  const [password, setPassword] = useState('')
  const { busy, error, run } = useSubmit()
  const errorId = useId()

  return (
    <Card
      title="Sign in"
      lead="Use your tenant handle and the address your administrator registered."
      onSubmit={(e) => { run(e, () => onSignIn(tenant.trim(), email.trim(), password)) }}
    >
      {notice && <p className="zt-signin-done" role="status">{notice}</p>}
      <Field label="Tenant">
        {(id) => (
          <input id={id} name="tenant" value={tenant} autoComplete="organization" required
            onChange={(e) => { onTenant(e.target.value) }} />
        )}
      </Field>
      <Field label="Email">
        {(id) => (
          <input id={id} name="email" type="email" value={email} autoComplete="username" required
            onChange={(e) => { onEmail(e.target.value) }} />
        )}
      </Field>
      <Field label="Password">
        {(id) => (
          <input id={id} name="password" type="password" value={password} autoComplete="current-password" required
            onChange={(e) => { setPassword(e.target.value) }} aria-describedby={error ? errorId : undefined} />
        )}
      </Field>
      <Problem id={errorId} text={error} />
      <button type="submit" className="zt-btn zt-btn--primary zt-signin-submit" disabled={busy}>
        {busy ? 'Signing in…' : 'Sign in'}
      </button>
      <div className="zt-signin-links">
        <button type="button" className="zt-signin-link" onClick={onForgot}>Forgot your password?</button>
        <button type="button" className="zt-signin-link" onClick={onContact}>Contact us</button>
      </div>
    </Card>
  )
}

function ForgotForm({ tenant, email, onTenant, onEmail, onBack }: {
  tenant: string
  email: string
  onTenant: (v: string) => void
  onEmail: (v: string) => void
  onBack: (message?: string) => void
}) {
  const { busy, error, run } = useSubmit()
  const errorId = useId()

  return (
    <Card
      title="Reset your password"
      lead="We'll email a link that lets you choose a new password. It works once and expires in an hour."
      onSubmit={(e) => {
        run(e, async () => {
          await api.requestPasswordReset(tenant.trim(), email.trim())
          onBack('If that account exists, a reset link is on its way. Check your inbox.')
        })
      }}
    >
      <Field label="Tenant">
        {(id) => (
          <input id={id} name="tenant" value={tenant} autoComplete="organization" required
            onChange={(e) => { onTenant(e.target.value) }} />
        )}
      </Field>
      <Field label="Email">
        {(id) => (
          <input id={id} name="email" type="email" value={email} autoComplete="username" required
            onChange={(e) => { onEmail(e.target.value) }} aria-describedby={error ? errorId : undefined} />
        )}
      </Field>
      <Problem id={errorId} text={error} />
      <button type="submit" className="zt-btn zt-btn--primary zt-signin-submit" disabled={busy}>
        {busy ? 'Sending…' : 'Email me a link'}
      </button>
      <div className="zt-signin-links">
        <button type="button" className="zt-signin-link" onClick={() => { onBack() }}>Back to sign in</button>
      </div>
    </Card>
  )
}

function SetPasswordForm({ link, onDone, onCancel }: {
  link: string
  onDone: (result: { tenant: string; email: string } | null) => void
  onCancel: () => void
}) {
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [dead, setDead] = useState(false)
  const { busy, error, setError, run } = useSubmit()
  const errorId = useId()

  return (
    <Card
      title="Choose your password"
      lead="At least 12 characters. A passphrase of a few unrelated words is easier to remember and harder to guess."
      onSubmit={(e) => {
        if (password !== confirm) {
          e.preventDefault()
          setError('The two passwords do not match.')
          return
        }
        run(e, async () => {
          try {
            onDone(await api.setPasswordWithLink(link, password))
          } catch (cause) {
            if (cause instanceof ApiError && cause.reasonCode === 'CREDENTIAL_LINK_INVALID') {
              setDead(true)
            }
            throw cause
          }
        })
      }}
    >
      <Field label="New password">
        {(id) => (
          <input id={id} name="new-password" type="password" value={password} autoComplete="new-password"
            minLength={12} required onChange={(e) => { setPassword(e.target.value) }} />
        )}
      </Field>
      <Field label="Confirm password">
        {(id) => (
          <input id={id} name="confirm-password" type="password" value={confirm} autoComplete="new-password"
            minLength={12} required onChange={(e) => { setConfirm(e.target.value) }}
            aria-describedby={error ? errorId : undefined} />
        )}
      </Field>
      <Problem id={errorId} text={error} />
      {dead ? (
        <button type="button" className="zt-btn zt-btn--primary zt-signin-submit" onClick={() => { onDone(null) }}>
          Ask for a new link
        </button>
      ) : (
        <button type="submit" className="zt-btn zt-btn--primary zt-signin-submit" disabled={busy}>
          {busy ? 'Saving…' : 'Set password'}
        </button>
      )}
      <div className="zt-signin-links">
        <button type="button" className="zt-signin-link" onClick={onCancel}>Back to sign in</button>
      </div>
    </Card>
  )
}

function ContactForm({ onBack }: { onBack: (message?: string) => void }) {
  const [name, setName] = useState('')
  const [email, setEmail] = useState('')
  const [company, setCompany] = useState('')
  const [message, setMessage] = useState('')
  const { busy, error, run } = useSubmit()
  const errorId = useId()

  return (
    <Card
      title="Contact us"
      lead="Questions about ZoikoTax, a demonstration, or access for your team. We reply by email."
      onSubmit={(e) => {
        run(e, async () => {
          await api.submitContact({
            name: name.trim(),
            email: email.trim(),
            ...(company.trim() === '' ? {} : { company: company.trim() }),
            message: message.trim(),
          })
          onBack('Thank you — your message is on its way. We will reply by email.')
        })
      }}
    >
      <Field label="Name">
        {(id) => (
          <input id={id} name="name" value={name} autoComplete="name" maxLength={200} required
            onChange={(e) => { setName(e.target.value) }} />
        )}
      </Field>
      <Field label="Email">
        {(id) => (
          <input id={id} name="email" type="email" value={email} autoComplete="email" required
            onChange={(e) => { setEmail(e.target.value) }} />
        )}
      </Field>
      <Field label="Company (optional)">
        {(id) => (
          <input id={id} name="company" value={company} autoComplete="organization" maxLength={200}
            onChange={(e) => { setCompany(e.target.value) }} />
        )}
      </Field>
      <Field label="Message" hint={`${String(message.length)} / 5000`}>
        {(id) => (
          <textarea id={id} name="message" value={message} maxLength={5000} required
            onChange={(e) => { setMessage(e.target.value) }} aria-describedby={error ? errorId : undefined} />
        )}
      </Field>
      <Problem id={errorId} text={error} />
      <button type="submit" className="zt-btn zt-btn--primary zt-signin-submit" disabled={busy}>
        {busy ? 'Sending…' : 'Send message'}
      </button>
      <div className="zt-signin-links">
        <button type="button" className="zt-signin-link" onClick={() => { onBack() }}>Back to sign in</button>
      </div>
    </Card>
  )
}
