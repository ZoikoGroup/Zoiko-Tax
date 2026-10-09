import type { Capabilities } from '@zoikotax/sdk'

import { periodLabel } from '../../dashboard/format'
import type { SessionInfo } from '../../platform/api'
import { IconBox, IconDollarSign, IconGlobe, IconShieldCheck } from '../common/DashboardIcons'

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

export const Header = ({ session, capabilities, period, periods, onPeriodChange, onSignOut }: HeaderProps) => {
  const content = capabilities?.content
  return (
    <div className="ztax-header-wrapper">
      <header className="ztax-header-tier1">
        <div className="ztax-selector-group">
          <div className="ztax-selector-card">
            <span className="ztax-selector-label">Tenant</span>
            <div className="ztax-selector-value">
              <span>{session.tenant.displayName}</span>
            </div>
          </div>

          <div className="ztax-selector-card">
            <span className="ztax-selector-label">Legal Entity</span>
            <div className="ztax-selector-value">
              <span>Default entity</span>
            </div>
          </div>

          <label className="ztax-selector-card">
            <span className="ztax-selector-label">Tax Period</span>
            <select
              className="ztax-selector-select"
              value={period}
              onChange={(e) => { onPeriodChange(e.target.value) }}
            >
              {periods.map((p) => (
                <option key={p} value={p}>
                  {periodLabel(p)}
                </option>
              ))}
            </select>
          </label>

          <div className="ztax-selector-card">
            <span className="ztax-selector-label">Environment</span>
            <div className="ztax-selector-value">
              <span>{capabilities === null ? '…' : `${capabilities.environment} · ${capabilities.cell}`}</span>
            </div>
          </div>

          <div className="ztax-selector-card">
            <span className="ztax-selector-label">Roles</span>
            <div className="ztax-selector-value">
              <span>{session.user.roles.join(', ')}</span>
            </div>
          </div>
        </div>

        <div className="ztax-header-actions">
          <div className="ztax-user-profile">
            <span className="ztax-user-avatar ztax-user-initials" aria-hidden="true">
              {initials(session.user.displayName)}
            </span>
            <div className="ztax-user-info">
              <span className="ztax-user-name">{session.user.displayName}</span>
              <span className="ztax-user-role">{session.user.email}</span>
            </div>
          </div>
          <button type="button" className="ztax-link-blue" onClick={onSignOut}>
            Sign out
          </button>
        </div>
      </header>

      <div className="ztax-header-tier2">
        <div className="ztax-scope-group">
          <div className="ztax-scope-card">
            <IconGlobe style={{ width: 16, height: 16 }} />
            <div className="ztax-scope-texts">
              <span className="ztax-scope-label">Residency</span>
              <span className="ztax-scope-value">{session.tenant.residencyRegion}</span>
            </div>
          </div>

          <div className="ztax-scope-card">
            <IconShieldCheck style={{ width: 16, height: 16 }} />
            <div className="ztax-scope-texts">
              <span className="ztax-scope-label">Content Bundle</span>
              <span className="ztax-scope-value">
                {capabilities === null ? '…' : content === undefined ? 'None loaded' : content.bundleId}
              </span>
            </div>
          </div>

          <div className="ztax-scope-card">
            <IconDollarSign style={{ width: 16, height: 16 }} />
            <div className="ztax-scope-texts">
              <span className="ztax-scope-label">Output</span>
              <span className="ztax-scope-value">
                {capabilities === null ? '…' : capabilities.authoritative ? 'Authoritative' : 'Advisory (before A4)'}
              </span>
            </div>
          </div>

          <div className="ztax-scope-card">
            <IconBox style={{ width: 16, height: 16 }} />
            <div className="ztax-scope-texts">
              <span className="ztax-scope-label">Canonical Form</span>
              <span className="ztax-scope-value">{capabilities?.canonProfile ?? '…'}</span>
            </div>
          </div>
        </div>

        <div className="ztax-scope-badge">
          <span className="ztax-green-dot" />
          <span>Session active until {session.expiresAt.slice(11, 16)} UTC</span>
        </div>
      </div>
    </div>
  )
}
