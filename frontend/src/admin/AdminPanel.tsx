import { useCallback, useEffect, useState } from 'react'

import {
  ALL_ROLES,
  api,
  describeError,
  type AuditRecord,
  type Role,
  type Session,
  type SessionInfo,
  type User,
} from '../platform/api'
import { Confirm } from './Confirm'

/**
 * The administration workspace.
 *
 * Four surfaces, and they exist because the auth model makes them real: a
 * tenant, its users, their roles, their live sessions, and an append-only
 * record of every change to those. Nothing here presupposes fiscal data, which
 * is why it can be built before W3 lane N chooses the stack for the 38 screens —
 * these are administrative screens, and they are the ones the auth work needs in
 * order to be usable at all.
 *
 * No fiscal amount appears anywhere in this file, and none can: the api module's
 * types have no way to express one (C1 is a type-level property here, not a
 * discipline).
 */

type Tab = 'users' | 'sessions' | 'audit'

export interface AdminPanelProps {
  readonly session: SessionInfo
  readonly onSignOut: () => Promise<void>
  readonly onRefresh: () => Promise<void>
}

export function AdminPanel({ session, onSignOut, onRefresh }: AdminPanelProps) {
  const [tab, setTab] = useState<Tab>('users')
  const isAdmin = session.user.roles.includes('ADMIN')
  const isAuditor = session.user.roles.includes('AUDITOR')

  return (
    <main className="shell">
      <header className="shell__header shell__header--row">
        <div>
          <h1>{session.tenant.displayName}</h1>
          <p className="shell__subtitle">
            <span className="tag">{session.tenant.slug}</span>
            <span className="tag">residency: {session.tenant.residencyRegion}</span>
            <span className={session.tenant.status === 'ACTIVE' ? 'tag tag--ok' : 'tag tag--warn'}>
              {session.tenant.status}
            </span>
          </p>
        </div>
        <div className="shell__identity">
          <span>{session.user.displayName}</span>
          <span className="shell__email">{session.user.email}</span>
          <button type="button" className="btn" onClick={() => { void onSignOut() }}>
            Sign out
          </button>
        </div>
      </header>

      {/* ADR-0019 C3 and the A4 gate: the deployment's authority is stated, not
          assumed. A user must not be able to mistake this for a system that can
          produce a figure they may file. */}
      <p className="banner banner--advisory" role="status">
        This deployment is not authorized to produce authoritative fiscal output. Administration
        only.
      </p>

      <nav className="tabs" aria-label="Administration">
        <TabButton current={tab} value="users" onSelect={setTab}>Users</TabButton>
        {isAdmin && <TabButton current={tab} value="sessions" onSelect={setTab}>Sessions</TabButton>}
        {(isAdmin || isAuditor) && <TabButton current={tab} value="audit" onSelect={setTab}>Audit</TabButton>}
      </nav>

      {tab === 'users' && <UsersPanel canManage={isAdmin} onChanged={onRefresh} />}
      {tab === 'sessions' && isAdmin && <SessionsPanel onSignedOutSelf={onRefresh} />}
      {tab === 'audit' && (isAdmin || isAuditor) && <AuditPanel />}
    </main>
  )
}

function TabButton({
  current,
  value,
  onSelect,
  children,
}: {
  current: Tab
  value: Tab
  onSelect: (t: Tab) => void
  children: React.ReactNode
}) {
  const selected = current === value
  return (
    <button
      type="button"
      className={selected ? 'tab tab--selected' : 'tab'}
      // aria-current rather than a colour alone, so the selected tab is
      // announced and not merely visible (C6).
      aria-current={selected ? 'page' : undefined}
      onClick={() => { onSelect(value) }}
    >
      {children}
    </button>
  )
}

/** A small hook for the load-and-refresh cycle every panel shares. */
function useList<T>(load: () => Promise<T>): {
  data: T | null
  error: string
  loading: boolean
  reload: () => void
} {
  const [data, setData] = useState<T | null>(null)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)

  const reload = useCallback(() => {
    setLoading(true)
    load()
      .then((result) => { setData(result); setError('') })
      .catch((cause: unknown) => { setError(describeError(cause)) })
      .finally(() => { setLoading(false) })
  }, [load])

  useEffect(reload, [reload])
  return { data, error, loading, reload }
}

// ---------------------------------------------------------------------------
// Users
// ---------------------------------------------------------------------------

function UsersPanel({ canManage, onChanged }: { canManage: boolean; onChanged: () => Promise<void> }) {
  const load = useCallback(() => api.listUsers(), [])
  const { data, error, loading, reload } = useList(load)
  const [actionError, setActionError] = useState('')

  const run = (work: Promise<unknown>) => {
    setActionError('')
    void work
      .then(() => { reload(); return onChanged() })
      .catch((cause: unknown) => { setActionError(describeError(cause)) })
  }

  return (
    <section className="panel" aria-labelledby="users-heading">
      <h2 id="users-heading">Users</h2>
      {canManage && <CreateUser onCreated={() => { run(Promise.resolve()) }} />}

      <Status loading={loading} error={error || actionError} empty={data?.users.length === 0} what="users" />

      {data && data.users.length > 0 && (
        <table className="table">
          <caption className="table__caption">Users in this tenant, newest first</caption>
          <thead>
            <tr>
              <th scope="col">Name</th>
              <th scope="col">Email</th>
              <th scope="col">Status</th>
              <th scope="col">Roles</th>
              {canManage && <th scope="col">Actions</th>}
            </tr>
          </thead>
          <tbody>
            {data.users.map((user) => (
              <tr key={user.id}>
                <td>{user.displayName}</td>
                <td className="mono">{user.email}</td>
                <td>
                  <span className={statusClass(user.status)}>{user.status}</span>
                </td>
                <td>
                  <RoleEditor user={user} canManage={canManage} onChange={(work) => { run(work) }} />
                </td>
                {canManage && (
                  <td>
                    <UserActions user={user} onAction={(work) => { run(work) }} />
                  </td>
                )}
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </section>
  )
}

function statusClass(status: User['status']): string {
  if (status === 'ACTIVE') return 'tag tag--ok'
  if (status === 'DISABLED') return 'tag tag--warn'
  return 'tag'
}

function RoleEditor({
  user,
  canManage,
  onChange,
}: {
  user: User
  canManage: boolean
  onChange: (work: Promise<unknown>) => void
}) {
  if (!canManage) {
    return <span>{user.roles.join(', ') || '—'}</span>
  }
  return (
    <div className="roles">
      {ALL_ROLES.map((role) => {
        const held = user.roles.includes(role)
        return (
          <label key={role} className={held ? 'chip chip--on' : 'chip'}>
            <input
              type="checkbox"
              checked={held}
              onChange={() => {
                onChange(held ? api.revokeRole(user.id, role) : api.grantRole(user.id, role))
              }}
            />
            {role}
          </label>
        )
      })}
    </div>
  )
}

function UserActions({ user, onAction }: { user: User; onAction: (work: Promise<unknown>) => void }) {
  if (user.status === 'DISABLED') {
    return (
      <Confirm
        action="Enable"
        effect={<>Restore access for {user.displayName}. They will be able to sign in again.</>}
        onConfirm={() => { onAction(api.setUserStatus(user.id, 'ACTIVE')) }}
      />
    )
  }
  return (
    <Confirm
      action="Disable"
      severe
      // C5: the effect in plain language, including the part a generic
      // confirmation would leave out — that it takes effect immediately rather
      // than at the next sign-in.
      effect={
        <>
          Withdraw access for {user.displayName} ({user.email}). Every session they currently hold
          is ended immediately, and they will not be able to sign in.
        </>
      }
      onConfirm={() => { onAction(api.setUserStatus(user.id, 'DISABLED')) }}
    />
  )
}

function CreateUser({ onCreated }: { onCreated: () => void }) {
  const [open, setOpen] = useState(false)
  const [email, setEmail] = useState('')
  const [displayName, setDisplayName] = useState('')
  const [roles, setRoles] = useState<Role[]>([])
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  if (!open) {
    return (
      <button type="button" className="btn btn--primary" onClick={() => { setOpen(true) }}>
        Add user
      </button>
    )
  }

  return (
    <form
      className="subpanel"
      onSubmit={(event) => {
        event.preventDefault()
        setBusy(true)
        setError('')
        api
          .createUser({
            email: email.trim(),
            displayName: displayName.trim(),
            roles,
            ...(password === '' ? {} : { password }),
          })
          .then(() => {
            setOpen(false)
            setEmail(''); setDisplayName(''); setRoles([]); setPassword('')
            onCreated()
          })
          .catch((cause: unknown) => { setError(describeError(cause)) })
          .finally(() => { setBusy(false) })
      }}
    >
      <h3>Add user</h3>

      <div className="field">
        <label htmlFor="new-user-name">Name</label>
        <input id="new-user-name" value={displayName} required onChange={(e) => { setDisplayName(e.target.value) }} />
      </div>

      <div className="field">
        <label htmlFor="new-user-email">Email</label>
        <input id="new-user-email" type="email" value={email} required onChange={(e) => { setEmail(e.target.value) }} />
      </div>

      <fieldset className="field">
        <legend>Roles</legend>
        <div className="roles">
          {ALL_ROLES.map((role) => (
            <label key={role} className={roles.includes(role) ? 'chip chip--on' : 'chip'}>
              <input
                type="checkbox"
                checked={roles.includes(role)}
                onChange={() => {
                  setRoles((current) =>
                    current.includes(role) ? current.filter((r) => r !== role) : [...current, role],
                  )
                }}
              />
              {role}
            </label>
          ))}
        </div>
      </fieldset>

      <div className="field">
        <label htmlFor="new-user-password">Password</label>
        <input
          id="new-user-password"
          type="password"
          value={password}
          autoComplete="new-password"
          onChange={(e) => { setPassword(e.target.value) }}
          aria-describedby="new-user-password-help"
        />
        {/* Leaving it blank is a real choice with a real meaning, so it is
            stated rather than left for someone to discover. */}
        <p className="field__help" id="new-user-password-help">
          Leave blank to create an invited user who cannot sign in until a password is set.
          Minimum 12 characters.
        </p>
      </div>

      <p className="field__error" role="alert" aria-live="polite">{error}</p>

      <div className="confirm__actions">
        <button type="submit" className="btn btn--primary" disabled={busy}>
          {busy ? 'Creating…' : 'Create user'}
        </button>
        <button type="button" className="btn" onClick={() => { setOpen(false) }}>Cancel</button>
      </div>
    </form>
  )
}

// ---------------------------------------------------------------------------
// Sessions
// ---------------------------------------------------------------------------

function SessionsPanel({ onSignedOutSelf }: { onSignedOutSelf: () => Promise<void> }) {
  const load = useCallback(() => api.listSessions(), [])
  const { data, error, loading, reload } = useList(load)
  const [actionError, setActionError] = useState('')

  const live = data?.sessions.filter((s) => !s.revoked) ?? []

  return (
    <section className="panel" aria-labelledby="sessions-heading">
      <h2 id="sessions-heading">Sessions</h2>
      <p className="panel__note">
        Every session currently established in this tenant. Revoking one ends it immediately; the
        holder is signed out on their next request.
      </p>

      <Status loading={loading} error={error || actionError} empty={live.length === 0} what="live sessions" />

      {live.length > 0 && (
        <table className="table">
          <caption className="table__caption">Live sessions, newest first</caption>
          <thead>
            <tr>
              <th scope="col">Started</th>
              <th scope="col">Expires</th>
              <th scope="col">Client</th>
              <th scope="col">Address</th>
              <th scope="col">Actions</th>
            </tr>
          </thead>
          <tbody>
            {live.map((s) => (
              <tr key={s.id} className={s.current ? 'row--current' : undefined}>
                <td>{formatInstant(s.createdAt)}</td>
                <td>{formatInstant(s.expiresAt)}</td>
                <td>
                  {s.current && <span className="tag tag--ok">this session</span>}{' '}
                  <span className="mono">{truncate(s.userAgent ?? '—', 44)}</span>
                </td>
                <td className="mono">{s.clientIp ?? '—'}</td>
                <td>
                  <RevokeSession
                    session={s}
                    onRevoke={() => {
                      setActionError('')
                      void api
                        .revokeSession(s.id)
                        .then(() => {
                          // Revoking your own session signs you out. Refreshing
                          // identity is what turns that into a sign-in screen
                          // rather than a page of failing requests.
                          if (s.current) return onSignedOutSelf()
                          reload()
                          return undefined
                        })
                        .catch((cause: unknown) => { setActionError(describeError(cause)) })
                    }}
                  />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </section>
  )
}

function RevokeSession({ session, onRevoke }: { session: Session; onRevoke: () => void }) {
  return (
    <Confirm
      action="Revoke"
      severe={session.current}
      effect={
        session.current ? (
          <>
            End <strong>the session you are using right now</strong>. You will be signed out
            immediately and will need to sign in again.
          </>
        ) : (
          <>
            End this session immediately. Whoever holds it is signed out on their next request and
            must sign in again.
          </>
        )
      }
      onConfirm={onRevoke}
    />
  )
}

// ---------------------------------------------------------------------------
// Audit
// ---------------------------------------------------------------------------

function AuditPanel() {
  const load = useCallback(() => api.listAudit(), [])
  const { data, error, loading } = useList(load)

  return (
    <section className="panel" aria-labelledby="audit-heading">
      <h2 id="audit-heading">Audit</h2>
      <p className="panel__note">
        Every administrative action in this tenant, append-only. Records are never modified or
        removed, and they carry no credential material.
      </p>

      <Status loading={loading} error={error} empty={data?.records.length === 0} what="audit records" />

      {data && data.records.length > 0 && (
        <table className="table">
          <caption className="table__caption">Administrative audit, newest first</caption>
          <thead>
            <tr>
              <th scope="col">When</th>
              <th scope="col">Action</th>
              <th scope="col">Subject</th>
              <th scope="col">Detail</th>
            </tr>
          </thead>
          <tbody>
            {data.records.map((record: AuditRecord) => (
              <tr key={record.id}>
                <td>{formatInstant(record.recordedAt)}</td>
                <td><span className="tag">{record.action}</span></td>
                <td className="mono">{record.subjectType}/{truncate(record.subjectId, 12)}</td>
                <td className="mono">{truncate(record.detail, 72)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </section>
  )
}

// ---------------------------------------------------------------------------
// Shared
// ---------------------------------------------------------------------------

/**
 * The three states a list can be in besides having data.
 *
 * C3: each renders as itself. An error is not a spinner, and an empty list is
 * not a failure — conflating them is how a user waits for something that is
 * never going to arrive.
 */
function Status({
  loading,
  error,
  empty,
  what,
}: {
  loading: boolean
  error: string
  empty: boolean | undefined
  what: string
}) {
  if (error) {
    return <p className="field__error" role="alert">{error}</p>
  }
  if (loading) {
    return <p className="muted" aria-live="polite">Loading {what}…</p>
  }
  if (empty === true) {
    return <p className="muted">No {what}.</p>
  }
  return null
}

/**
 * Timestamps arrive as RFC 3339 UTC with six fractional digits (ADR-0011 P2)
 * and are rendered in the viewer's own zone, because an administrator reading
 * "who signed in at what time" is reasoning in their own day.
 *
 * Note what this is not doing: no arithmetic on the value, and no formatting of
 * anything fiscal. Intl.DateTimeFormat on a date is unrelated to C1's ban on
 * Intl.NumberFormat, which is about numbers losing precision.
 */
function formatInstant(iso: string): string {
  const parsed = new Date(iso)
  if (Number.isNaN(parsed.getTime())) return iso
  return new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' }).format(parsed)
}

function truncate(value: string, max: number): string {
  return value.length <= max ? value : `${value.slice(0, max - 1)}…`
}
