import React from 'react';
import {
  Home,
  Activity,
  Compass,
  FileText,
  Shield,
  RotateCw,
  CreditCard,
  FileCheck,
  Eye,
  Archive,
  Settings2,
  Database,
  BarChart3,
  GitFork,
  Sliders,
} from 'lucide-react';

export const Sidebar: React.FC = () => {
  const [activeItem, setActiveItem] = React.useState('home');

  const navGroups = [
    {
      title: 'WORKSPACE',
      items: [
        { id: 'home', label: 'Home', icon: <Home className="h-4 w-4" /> },
        { id: 'action-center', label: 'Action Center', icon: <Activity className="h-4 w-4" /> },
        { id: 'determine', label: 'Determine', icon: <Compass className="h-4 w-4" /> },
        { id: 'obligations', label: 'Obligations', icon: <FileText className="h-4 w-4" /> },
        { id: 'compliance', label: 'Compliance', icon: <Shield className="h-4 w-4" /> },
        { id: 'reconcile', label: 'Reconcile', icon: <RotateCw className="h-4 w-4" /> },
      ],
    },
    {
      title: 'SETTLE & PROVE',
      items: [
        { id: 'remittance', label: 'Remittance', icon: <CreditCard className="h-4 w-4" /> },
        { id: 'e-invoicing', label: 'E-Invoicing / CTC', icon: <FileCheck className="h-4 w-4" /> },
        { id: 'shadow-assurance', label: 'Shadow Assurance', icon: <Eye className="h-4 w-4" /> },
        { id: 'evidence', label: 'Evidence', icon: <Archive className="h-4 w-4" /> },
      ],
    },
    {
      title: 'INTELLIGENCE',
      items: [
        { id: 'intelligence', label: 'Intelligence', icon: <Settings2 className="h-4 w-4" /> },
        { id: 'content', label: 'Content', icon: <Database className="h-4 w-4" /> },
        { id: 'reports', label: 'Reports', icon: <BarChart3 className="h-4 w-4" /> },
      ],
    },
    {
      title: 'PLATFORM',
      items: [
        { id: 'migration', label: 'Migration', icon: <GitFork className="h-4 w-4" /> },
        { id: 'administration', label: 'Administration', icon: <Sliders className="h-4 w-4" /> },
      ],
    },
  ];

  return (
    <aside className="fixed left-0 top-0 z-40 flex h-screen w-56 flex-col bg-[#21153b] text-white select-none border-r border-[#2d1d4f]">
      {/* Top Logo Container: White background matching header bar */}
      <div className="flex h-[58px] w-full items-center bg-white px-4 border-b border-gray-200">
        <img
          src="/logo.png"
          alt="ZoikoTax"
          className="h-6 w-auto object-contain"
        />
      </div>

      {/* Navigation Sections */}
      <div className="flex-1 overflow-y-auto px-3 py-3 space-y-4 no-scrollbar">
        {navGroups.map(group => (
          <div key={group.title} className="space-y-0.5">
            <div className="px-2.5 py-1 text-[10px] font-bold tracking-wider text-[#7e6d98] uppercase">
              {group.title}
            </div>

            {group.items.map(item => {
              const isActive = activeItem === item.id;
              return (
                <button
                  key={item.id}
                  onClick={() => setActiveItem(item.id)}
                  className={`flex w-full items-center gap-3 rounded-lg px-2.5 py-1.5 text-xs transition-colors cursor-pointer ${
                    isActive
                      ? 'bg-[#3d2f5a] text-white font-medium shadow-xs'
                      : 'text-[#a292be] hover:bg-[#2c1c4b] hover:text-white font-normal'
                  }`}
                >
                  <span className={`shrink-0 ${isActive ? 'text-white' : 'text-[#a292be]'}`}>
                    {item.icon}
                  </span>
                  <span className="truncate">{item.label}</span>
                </button>
              );
            })}
          </div>
        ))}
      </div>

      {/* Footer Legal & Confidentiality */}
      <div className="border-t border-[#2d1d4f] p-3 text-[9px] text-[#6b5a88] leading-tight space-y-0.5">
        <div>ZoikoTax is a trading name of Zoiko Tech Inc.</div>
        <div>Confidential - Product & Engineering</div>
      </div>
    </aside>
  );
};
