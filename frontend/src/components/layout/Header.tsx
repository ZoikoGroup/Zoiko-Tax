import {
  IconChevronDown,
  IconSearch,
  IconBell,
  IconBox,
  IconGlobe,
  IconDollarSign,
  IconShieldCheck,
} from '../common/DashboardIcons';

export const Header = () => {
  return (
    <div className="ztax-header-wrapper">
      {/* TIER 1: Enterprise Header Row */}
      <header className="ztax-header-tier1">
        {/* Left: 5 Enterprise Selectors */}
        <div className="ztax-selector-group">
          {/* Selector 1 */}
          <div className="ztax-selector-card">
            <span className="ztax-selector-label">Global Telecom Group</span>
            <div className="ztax-selector-value">
              <span>Global Telecom Group</span>
              <IconChevronDown style={{ width: 12, height: 12, color: '#6b7280' }} />
            </div>
          </div>

          {/* Selector 2 */}
          <div className="ztax-selector-card">
            <span className="ztax-selector-label">Legal Entity</span>
            <div className="ztax-selector-value">
              <span>Zoiko Telecom Ltd</span>
              <IconChevronDown style={{ width: 12, height: 12, color: '#6b7280' }} />
            </div>
          </div>

          {/* Selector 3 */}
          <div className="ztax-selector-card">
            <span className="ztax-selector-label">Tax Period</span>
            <div className="ztax-selector-value">
              <span>Apr 2026 - Jun 2026</span>
              <IconChevronDown style={{ width: 12, height: 12, color: '#6b7280' }} />
            </div>
          </div>

          {/* Selector 4 */}
          <div className="ztax-selector-card">
            <span className="ztax-selector-label">Environment</span>
            <div className="ztax-selector-value">
              <span>Production</span>
              <IconChevronDown style={{ width: 12, height: 12, color: '#6b7280' }} />
            </div>
          </div>

          {/* Selector 5 */}
          <div className="ztax-selector-card">
            <span className="ztax-selector-label">Role / User</span>
            <div className="ztax-selector-value">
              <span>Tax Manager</span>
              <IconChevronDown style={{ width: 12, height: 12, color: '#6b7280' }} />
            </div>
          </div>
        </div>

        {/* Right: Search, Notifications, User profile */}
        <div className="ztax-header-actions">
          <button type="button" className="ztax-icon-btn" aria-label="Search">
            <IconSearch style={{ width: 16, height: 16 }} />
          </button>

          <button type="button" className="ztax-icon-btn" aria-label="Notifications">
            <IconBell style={{ width: 16, height: 16 }} />
            <span className="ztax-dot-badge" />
          </button>

          <div className="ztax-user-profile">
            <img
              src="/avatar_hreynolds.png"
              alt="H. Reynolds"
              className="ztax-user-avatar"
            />
            <div className="ztax-user-info">
              <span className="ztax-user-name">H. Reynolds</span>
              <span className="ztax-user-role">TAX DIRECTOR</span>
            </div>
          </div>
        </div>
      </header>

      {/* TIER 2: Secondary Context / Scope Filter Row */}
      <div className="ztax-header-tier2">
        <div className="ztax-scope-group">
          {/* Card 1 */}
          <div className="ztax-scope-card">
            <IconBox style={{ width: 16, height: 16 }} />
            <div className="ztax-scope-texts">
              <span className="ztax-scope-label">Product / Service</span>
              <span className="ztax-scope-value">Mobile | Fixed | Digital Services</span>
            </div>
            <IconChevronDown style={{ width: 12, height: 12, color: '#9ca3af' }} />
          </div>

          {/* Card 2 */}
          <div className="ztax-scope-card">
            <IconGlobe style={{ width: 16, height: 16 }} />
            <div className="ztax-scope-texts">
              <span className="ztax-scope-label">Jurisdiction Scope</span>
              <span className="ztax-scope-value">Global (120 jurisdictions)</span>
            </div>
            <IconChevronDown style={{ width: 12, height: 12, color: '#9ca3af' }} />
          </div>

          {/* Card 3 */}
          <div className="ztax-scope-card">
            <IconDollarSign style={{ width: 16, height: 16 }} />
            <div className="ztax-scope-texts">
              <span className="ztax-scope-label">Currency</span>
              <span className="ztax-scope-value">USD (US Dollar)</span>
            </div>
            <IconChevronDown style={{ width: 12, height: 12, color: '#9ca3af' }} />
          </div>

          {/* Card 4 */}
          <div className="ztax-scope-card">
            <IconShieldCheck style={{ width: 16, height: 16 }} />
            <div className="ztax-scope-texts">
              <span className="ztax-scope-label">Certified Ruleset</span>
              <span className="ztax-scope-value">EU + OECD + Local (v1.4.2)</span>
            </div>
            <IconChevronDown style={{ width: 12, height: 12, color: '#9ca3af' }} />
          </div>
        </div>

        {/* Right Status Badge */}
        <div className="ztax-scope-badge">
          <span className="ztax-green-dot" />
          <span>Scope & trust active</span>
        </div>
      </div>
    </div>
  );
};
