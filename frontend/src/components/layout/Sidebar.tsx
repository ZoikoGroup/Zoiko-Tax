import React from 'react';
import { useApp, NavigationPage } from '../../context/AppContext';
import {
  LayoutDashboard,
  Calculator,
  Layers,
  CalendarCheck,
  History,
  BookOpenCheck,
  Globe2,
  Sparkles,
  ChevronLeft,
  ChevronRight,
} from 'lucide-react';

interface SidebarProps {
  collapsed: boolean;
  setCollapsed: (v: boolean) => void;
}

export const Sidebar: React.FC<SidebarProps> = ({ collapsed, setCollapsed }) => {
  const { activePage, setActivePage } = useApp();

  const navItems: {
    id: NavigationPage;
    label: string;
    icon: React.ReactNode;
    badge?: string;
    spec: string;
  }[] = [
    {
      id: 'overview',
      label: 'Executive Overview',
      icon: <LayoutDashboard className="h-4 w-4" />,
      spec: 'ZTAX-PRD-000',
    },
    {
      id: 'determination',
      label: 'Determination & Quotes',
      icon: <Calculator className="h-4 w-4" />,
      badge: 'C0 Hot Path',
      spec: 'ADR-0002',
    },
    {
      id: 'catalog',
      label: 'Dual Classification',
      icon: <Layers className="h-4 w-4" />,
      badge: 'Ontology',
      spec: 'ZTAX-CLS-001',
    },
    {
      id: 'obligations',
      label: 'Obligations & Nexus',
      icon: <CalendarCheck className="h-4 w-4" />,
      badge: '5-Role',
      spec: 'ZTAX-OBL-001',
    },
    {
      id: 'decisions',
      label: 'Decisions & Replay',
      icon: <History className="h-4 w-4" />,
      badge: 'Evidence',
      spec: 'ZTAX-EVID-001',
    },
    {
      id: 'subledger',
      label: 'Tax Subledger (TCSL)',
      icon: <BookOpenCheck className="h-4 w-4" />,
      spec: 'ZTAX-FIN-001',
    },
    {
      id: 'packs',
      label: 'Country Regulatory Packs',
      icon: <Globe2 className="h-4 w-4" />,
      badge: '5 Certified',
      spec: 'ZTAX-CONT-001',
    },
    {
      id: 'ai_governance',
      label: 'AI Governance & Shadow',
      icon: <Sparkles className="h-4 w-4" />,
      badge: 'Assurance',
      spec: 'ZTAX-AIGOV-001',
    },
  ];

  return (
    <aside
      className={`fixed left-0 top-0 z-40 flex h-screen flex-col border-r theme-sidebar transition-all duration-300 ${
        collapsed ? 'w-18' : 'w-72'
      }`}
    >
      {/* Brand Header with Official Logo */}
      <div className="flex h-16 items-center justify-between border-b theme-sidebar px-4">
        {!collapsed ? (
          <div className="flex items-center gap-2.5">
            <img
              src="/zoikotax-logo.png"
              alt="ZoikoTax"
              className="h-8 w-auto object-contain"
              onError={e => {
                (e.target as HTMLElement).style.display = 'none';
              }}
            />
            <span className="rounded bg-[#bf6735]/15 px-1.5 py-0.5 font-mono text-[9px] font-bold text-[#dd7134] border border-[#bf6735]/30">
              CORE
            </span>
          </div>
        ) : (
          <div className="mx-auto flex h-9 w-9 items-center justify-center rounded-xl bg-gradient-to-tr from-[#bf6735] to-[#dd7134] font-bold text-white shadow-lg">
            <span className="font-mono text-sm font-black">ZT</span>
          </div>
        )}

        <button
          onClick={() => setCollapsed(!collapsed)}
          className="hidden lg:flex rounded-lg p-1.5 theme-text-muted hover:theme-subtle hover:theme-text-primary transition-colors cursor-pointer"
        >
          {collapsed ? <ChevronRight className="h-4 w-4" /> : <ChevronLeft className="h-4 w-4" />}
        </button>
      </div>

      {/* Navigation Items */}
      <div className="flex-1 overflow-y-auto px-3 py-4 space-y-1.5">
        <div className={`mb-2 px-2 text-[10px] font-bold uppercase tracking-wider theme-text-muted ${collapsed ? 'text-center' : ''}`}>
          {collapsed ? '•' : 'Platform Modules'}
        </div>

        {navItems.map(item => {
          const isActive = activePage === item.id;
          return (
            <button
              key={item.id}
              onClick={() => setActivePage(item.id)}
              title={collapsed ? item.label : undefined}
              className={`group flex w-full items-center gap-3 rounded-xl px-3 py-2.5 text-xs font-semibold transition-all cursor-pointer ${
                isActive
                  ? 'bg-gradient-to-r from-[#bf6735]/20 to-[#dd7134]/10 text-[#dd7134] border border-[#bf6735]/50 shadow-sm'
                  : 'theme-text-secondary hover:bg-[#f3eef7] dark:hover:bg-[#1f1352] hover:text-[#1b1140] dark:hover:text-[#f6f3fb] border border-transparent'
              }`}
            >
              <div
                className={`shrink-0 ${
                  isActive ? 'text-[#dd7134]' : 'theme-text-muted group-hover:theme-text-primary'
                }`}
              >
                {item.icon}
              </div>

              {!collapsed && (
                <div className="flex flex-1 items-center justify-between text-left min-w-0">
                  <div className="min-w-0 pr-1">
                    <div className="font-semibold theme-text-primary text-xs whitespace-nowrap">{item.label}</div>
                    <div className="text-[10px] theme-text-muted font-mono">{item.spec}</div>
                  </div>
                  {item.badge && (
                    <span
                      className={`ml-1.5 shrink-0 rounded px-1.5 py-0.5 text-[9px] font-mono font-medium ${
                        isActive
                          ? 'bg-[#bf6735]/25 text-[#dd7134] border border-[#bf6735]/40'
                          : 'bg-[#f0e8f7] dark:bg-[#1a0f3d] theme-text-muted border border-[#e2d9ec] dark:border-[#38276b]'
                      }`}
                    >
                      {item.badge}
                    </span>
                  )}
                </div>
              )}
            </button>
          );
        })}
      </div>

      {/* Constitutional Invariant Badge in Footer */}
      {!collapsed && (
        <div className="border-t theme-sidebar p-3.5 m-2 rounded-xl border theme-subtle text-[11px]">
          <div className="flex items-center justify-between theme-text-muted font-mono text-[10px]">
            <span>ENGINE STATUS</span>
            <span className="text-[#26735b] dark:text-[#34d399] font-bold">W0 SEALED</span>
          </div>
          <div className="mt-1 text-[10px] theme-text-muted leading-tight">
            ADR-0001 & ADR-0002: IEEE 754 float forbidden. Decimal arithmetic strictly enforced.
          </div>
        </div>
      )}
    </aside>
  );
};
