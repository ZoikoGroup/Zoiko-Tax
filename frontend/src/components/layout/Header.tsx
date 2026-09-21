import React from 'react';
import { useApp } from '../../context/AppContext';
import { mockTenants } from '../../data/mockTenants';
import { UserPersona } from '../../types/domain';
import {
  Building2,
  ShieldCheck,
  User,
  Radio,
  Bell,
  RefreshCw,
  Sun,
  Moon,
} from 'lucide-react';

export const Header: React.FC = () => {
  const {
    activeTenant,
    setActiveTenant,
    activePersona,
    setActivePersona,
    cellHealth,
    refreshCellHealth,
    toggleApiMode,
    theme,
    toggleTheme,
    toasts,
  } = useApp();

  const personaLabels: Record<UserPersona, { label: string; role: string }> = {
    tax_director: { label: 'Sarah Chen', role: 'Head of Tax & Legal' },
    compliance_manager: { label: 'Marcus Vance', role: 'Regulatory & USF Mgr' },
    telecom_ops_lead: { label: 'David Ross', role: 'Telecom Billing Lead' },
    financial_controller: { label: 'Elena Rostova', role: 'Subledger Controller' },
    content_engineer: { label: 'Alex Thorne', role: 'Rules & Pack Auditor' },
  };

  return (
    <header className="sticky top-0 z-30 flex h-16 w-full items-center justify-between border-b theme-header px-4 sm:px-6 backdrop-blur-xl transition-colors">
      {/* Left: Tenant Selector & Cell Indicator */}
      <div className="flex items-center gap-3 sm:gap-4">
        {/* Tenant Selector */}
        <div className="flex items-center gap-2 rounded-lg border theme-subtle px-2.5 py-1.5 text-xs">
          <Building2 className="h-4 w-4 text-[#dd7134]" />
          <select
            value={activeTenant.id}
            onChange={e => {
              const selected = mockTenants.find(t => t.id === e.target.value);
              if (selected) setActiveTenant(selected);
            }}
            className="bg-transparent font-semibold theme-text-primary focus:outline-none cursor-pointer text-xs"
          >
            {mockTenants.map(t => (
              <option
                key={t.id}
                value={t.id}
                className={theme === 'dark' ? 'bg-[#150c33] text-white' : 'bg-white text-[#1b1140]'}
              >
                {t.name} ({t.type})
              </option>
            ))}
          </select>
          <span className="hidden sm:inline-block rounded theme-subtle px-1.5 py-0.5 font-mono text-[10px] theme-text-muted">
            {activeTenant.code}
          </span>
        </div>

        {/* Regional Execution Cell Probe */}
        <div
          onClick={toggleApiMode}
          title="Click to toggle between Mock Sandbox and Live Go Cell (http://localhost:8080)"
          className="hidden md:flex items-center gap-2 rounded-lg border theme-subtle px-2.5 py-1.5 text-xs cursor-pointer hover:border-[#bf6735]/60 transition-colors"
        >
          <Radio
            className={`h-3.5 w-3.5 ${
              cellHealth.liveness === 'healthy'
                ? 'text-[#26735b] dark:text-[#34d399] animate-pulse'
                : 'text-[#9a5b12] dark:text-[#fbbf24]'
            }`}
          />
          <span className="font-mono text-[11px] theme-text-secondary font-medium">
            {cellHealth.activeCell}
          </span>
          <span
            className={`rounded px-1.5 py-0.5 font-mono text-[9px] font-bold ${
              cellHealth.mode === 'LIVE_CELL'
                ? 'bg-[#26735b]/20 text-[#26735b] dark:text-[#34d399] border border-[#26735b]/40'
                : 'bg-[#bf6735]/15 text-[#bf6735] dark:text-[#ff9a52] border border-[#bf6735]/30'
            }`}
          >
            {cellHealth.mode === 'LIVE_CELL' ? 'LIVE CELL' : 'SANDBOX'}
          </span>
        </div>
      </div>

      {/* Right: Theme Toggle, Persona Switcher, Notifications, User */}
      <div className="flex items-center gap-2 sm:gap-3">
        {/* Theme Toggle Button (Light / Dark) */}
        <button
          onClick={toggleTheme}
          title={theme === 'dark' ? 'Switch to Light Mode (zoikotax.com)' : 'Switch to Dark Mode'}
          className="flex items-center gap-1.5 rounded-lg border theme-subtle px-3 py-1.5 text-xs font-semibold theme-text-primary hover:border-[#bf6735]/60 hover:theme-text-primary transition-all cursor-pointer shadow-sm"
        >
          {theme === 'dark' ? (
            <>
              <Sun className="h-4 w-4 text-[#ff9a52]" />
              <span className="hidden md:inline text-[11px]">Light Mode</span>
            </>
          ) : (
            <>
              <Moon className="h-4 w-4 text-[#bf6735]" />
              <span className="hidden md:inline text-[11px]">Dark Mode</span>
            </>
          )}
        </button>

        {/* Refresh Health Button */}
        <button
          onClick={() => refreshCellHealth()}
          title="Refresh cell health probe"
          className="rounded-lg p-2 theme-text-muted hover:theme-subtle hover:theme-text-primary transition-colors cursor-pointer"
        >
          <RefreshCw className="h-4 w-4" />
        </button>

        {/* Persona Switcher */}
        <div className="flex items-center gap-2 rounded-lg border theme-subtle px-2.5 py-1.5 text-xs">
          <ShieldCheck className="h-4 w-4 text-[#5b2a86] dark:text-[#c084fc]" />
          <span className="hidden lg:inline theme-text-muted font-medium">Role:</span>
          <select
            value={activePersona}
            onChange={e => setActivePersona(e.target.value as UserPersona)}
            className="bg-transparent font-medium theme-text-primary focus:outline-none cursor-pointer text-xs"
          >
            <option value="tax_director" className={theme === 'dark' ? 'bg-[#150c33] text-white' : 'bg-white text-[#1b1140]'}>
              Tax Director (Sarah Chen)
            </option>
            <option value="compliance_manager" className={theme === 'dark' ? 'bg-[#150c33] text-white' : 'bg-white text-[#1b1140]'}>
              Regulatory Manager (Marcus Vance)
            </option>
            <option value="telecom_ops_lead" className={theme === 'dark' ? 'bg-[#150c33] text-white' : 'bg-white text-[#1b1140]'}>
              Billing Ops Lead (David Ross)
            </option>
            <option value="financial_controller" className={theme === 'dark' ? 'bg-[#150c33] text-white' : 'bg-white text-[#1b1140]'}>
              Subledger Controller (Elena Rostova)
            </option>
            <option value="content_engineer" className={theme === 'dark' ? 'bg-[#150c33] text-white' : 'bg-white text-[#1b1140]'}>
              Rules Auditor (Alex Thorne)
            </option>
          </select>
        </div>

        {/* Notification Bell */}
        <div className="relative">
          <button className="rounded-lg p-2 theme-text-muted hover:theme-subtle hover:theme-text-primary transition-colors cursor-pointer">
            <Bell className="h-4 w-4" />
            {toasts.length > 0 && (
              <span className="absolute top-1 right-1 h-2 w-2 rounded-full bg-[#f2792a] animate-ping" />
            )}
          </button>
        </div>

        {/* Active Profile Info */}
        <div className="hidden sm:flex items-center gap-2.5 border-l theme-subtle pl-3">
          <div className="flex h-8 w-8 items-center justify-center rounded-full bg-gradient-to-tr from-[#bf6735] to-[#dd7134] text-white font-bold text-xs shadow-md">
            <User className="h-4 w-4" />
          </div>
          <div className="text-left">
            <div className="text-xs font-semibold theme-text-primary">
              {personaLabels[activePersona].label}
            </div>
            <div className="text-[10px] theme-text-muted">
              {personaLabels[activePersona].role}
            </div>
          </div>
        </div>
      </div>
    </header>
  );
};
