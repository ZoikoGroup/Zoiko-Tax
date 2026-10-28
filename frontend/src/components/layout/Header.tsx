import { useEffect, useRef, useState } from 'react'

import type { Capabilities } from '@zoikotax/sdk'

import { periodLabel } from '../../dashboard/format'
import type { SessionInfo } from '../../platform/api'
import { IconArrowRight, IconChevronDown, IconMenu, IconSearch } from '../common/DashboardIcons'
import { NAV_GROUPS, type View } from './Sidebar'

export interface HeaderProps {
  readonly session: SessionInfo
  readonly capabilities: Capabilities | null
  readonly period: string
  readonly periods: readonly string[]
  readonly onPeriodChange: (period: string) => void
  readonly onSignOut: () => void
}

/** Two letters for the avatar: the user's own name, not a stock photo. */
function initials(name: string): string {
  const parts = name.trim().split(/\s+/)
  const first = parts[0]?.[0] ?? '?'
  const last = parts.length > 1 ? (parts[parts.length - 1]?.[0] ?? '') : ''
  return `${first}${last}`.toUpperCase()
}

/** The open-menu key for the account menu; workspace menus use their group title. */
const USER_MENU = '__user'

interface TopBarProps {
  readonly session: SessionInfo
  readonly view: View
  readonly onNavigate: (view: View) => void
  readonly onSignOut: () => void
  readonly onMenu: () => void
  readonly menuOpen: boolean
}

/** The white bar across the top: wordmark, the workspaces as menus, the user. */
export const TopBar = ({ session, view, onNavigate, onSignOut, onMenu, menuOpen }: TopBarProps) => {
  const [open, setOpen] = useState<string | null>(null)
  const ref = useRef<HTMLElement>(null)

  useEffect(() => {
    if (open === null) return
    const close = (e: MouseEvent) => { if (!ref.current?.contains(e.target as Node)) setOpen(null) }
    const esc = (e: KeyboardEvent) => { if (e.key === 'Escape') setOpen(null) }
    document.addEventListener('mousedown', close)
    document.addEventListener('keydown', esc)
    return () => { document.removeEventListener('mousedown', close); document.removeEventListener('keydown', esc) }
  }, [open])

  return (
    <header className="zt-top" ref={ref}>
      <button type="button" className="zt-top-menu" aria-label="Open navigation" aria-expanded={menuOpen} onClick={onMenu}>
        <IconMenu />
      </button>
      <button type="button" className="zt-top-logo" onClick={() => { onNavigate('dashboard') }} aria-label="ZoikoTax home">
        <img src="/zoikotax-logo.png" alt="ZoikoTax" />
      </button>

      <nav className="zt-top-nav" aria-label="Workspace menus">
        {NAV_GROUPS.map((g) => {
          const here = g.items.some((i) => i.view === view)
          return (
            <div key={g.title} className="zt-top-drop">
              <button
                type="button"
                className={`zt-top-link${here ? ' zt-top-link--here' : ''}`}
                aria-expanded={open === g.title}
                onClick={() => { setOpen(open === g.title ? null : g.title) }}
              >
                {g.title}
                <IconChevronDown />
              </button>
              {open === g.title ? (
                <div className="zt-top-panel">
                  {g.items.map((i) => (
                    <button
                      key={i.id}
                      type="button"
                      disabled={i.view === undefined}
                      aria-current={i.view === view ? 'page' : undefined}
                      onClick={() => { if (i.view !== undefined) { onNavigate(i.view); setOpen(null) } }}
                    >
                      {i.icon}
                      <span>{i.label}</span>
                    </button>
                  ))}
                </div>
              ) : null}
            </div>
          )
        })}
      </nav>

      <div className="zt-top-right">
        <button type="button" className="zt-top-search" aria-label="Find a location or tax" onClick={() => { onNavigate('locations') }}>
          <IconSearch />
          <span className="zt-top-search-text">Find a location or tax</span>
        </button>
        <button type="button" className="zt-btn zt-btn--primary zt-top-cta" onClick={() => { onNavigate('determine') }}>
          New determination <IconArrowRight />
        </button>
        <div className="zt-top-drop">
          <button
            type="button"
            className="zt-top-user"
            aria-label={`Account: ${session.user.displayName}`}
            aria-expanded={open === USER_MENU}
            onClick={() => { setOpen(open === USER_MENU ? null : USER_MENU) }}
          >
            <span className="zt-top-avatar" aria-hidden="true">{initials(session.user.displayName)}</span>
            <span className="zt-top-username">{session.user.displayName}</span>
            <IconChevronDown />
          </button>
          {open === USER_MENU ? (
            <div className="zt-top-panel zt-top-panel--end">
              <div className="zt-top-account">
                <b>{session.user.displayName}</b>
                <span>{session.user.email}</span>
                <span>{session.tenant.displayName} · {session.user.roles.join(', ')}</span>
              </div>
              <button type="button" onClick={() => { setOpen(null); onSignOut() }}>Sign out</button>
            </div>
          ) : null}
        </div>
      </div>
    </header>
  )
}

/** Who and where: tenant, period, environment, content and output, in one strip. */
export const ContextStrip = ({ session, capabilities, period, periods, onPeriodChange }: Omit<HeaderProps, 'onSignOut'>) => {
  const content = capabilities?.content
  return (
    <div className="zt-context" aria-label="Session context">
      <Ctx k="Tenant" v={session.tenant.displayName} />
      <label className="zt-ctx">
        <span className="zt-ctx-k">Tax period</span>
        <select className="zt-ctx-select" value={period} onChange={(e) => { onPeriodChange(e.target.value) }}>
          {periods.map((p) => <option key={p} value={p}>{periodLabel(p)}</option>)}
        </select>
      </label>
      <Ctx k="Environment" v={capabilities === null ? '…' : `${capabilities.environment} · ${capabilities.cell}`} />
      <Ctx k="Residency" v={session.tenant.residencyRegion} />
      <Ctx k="Content bundle" v={capabilities === null ? '…' : content === undefined ? 'None loaded' : content.bundleId} mono />
      <Ctx k="Roles" v={session.user.roles.join(', ')} />
      <span className="zt-ctx-live">
        <i />
        Session until {session.expiresAt.slice(11, 16)} UTC
      </span>
    </div>
  )
}

const Ctx = ({ k, v, mono = false }: { k: string; v: string; mono?: boolean }) => (
  <div className="zt-ctx">
    <span className="zt-ctx-k">{k}</span>
    <span className={`zt-ctx-v${mono ? ' zt-mono' : ''}`}>{v}</span>
  </div>
)
