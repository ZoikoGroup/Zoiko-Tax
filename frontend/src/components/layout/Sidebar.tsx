import { useState } from 'react';
import {
  IconHome,
  IconActivity,
  IconCompass,
  IconFileText,
  IconShield,
  IconRotateCw,
  IconCreditCard,
  IconFileCheck,
  IconEye,
  IconArchive,
  IconSettings,
  IconDatabase,
  IconBarChart,
  IconGitFork,
  IconSliders,
} from '../common/DashboardIcons';

export const Sidebar = () => {
  const [activeItem, setActiveItem] = useState('home');

  const navGroups = [
    {
      title: 'WORKSPACE',
      items: [
        { id: 'home', label: 'Home', icon: <IconHome /> },
        { id: 'action-center', label: 'Action Center', icon: <IconActivity /> },
        { id: 'determine', label: 'Determine', icon: <IconCompass /> },
        { id: 'obligations', label: 'Obligations', icon: <IconFileText /> },
        { id: 'compliance', label: 'Compliance', icon: <IconShield /> },
        { id: 'reconcile', label: 'Reconcile', icon: <IconRotateCw /> },
      ],
    },
    {
      title: 'SETTLE & PROVE',
      items: [
        { id: 'remittance', label: 'Remittance', icon: <IconCreditCard /> },
        { id: 'e-invoicing', label: 'E-Invoicing / CTC', icon: <IconFileCheck /> },
        { id: 'shadow-assurance', label: 'Shadow Assurance', icon: <IconEye /> },
        { id: 'evidence', label: 'Evidence', icon: <IconArchive /> },
      ],
    },
    {
      title: 'INTELLIGENCE',
      items: [
        { id: 'intelligence', label: 'Intelligence', icon: <IconSettings /> },
        { id: 'content', label: 'Content', icon: <IconDatabase /> },
        { id: 'reports', label: 'Reports', icon: <IconBarChart /> },
      ],
    },
    {
      title: 'PLATFORM',
      items: [
        { id: 'migration', label: 'Migration', icon: <IconGitFork /> },
        { id: 'administration', label: 'Administration', icon: <IconSliders /> },
      ],
    },
  ];

  return (
    <aside className="ztax-sidebar">
      {/* Top Logo Container */}
      <div className="ztax-sidebar-logo">
        <img
          src="/zoikotax_logo_client.png"
          alt="ZoikoTax"
        />
      </div>

      {/* Navigation Sections */}
      <div className="ztax-sidebar-nav">
        {navGroups.map(group => (
          <div key={group.title} className="ztax-sidebar-group">
            <div className="ztax-sidebar-group-title">
              {group.title}
            </div>

            {group.items.map(item => {
              const isActive = activeItem === item.id;
              return (
                <button
                  type="button"
                  key={item.id}
                  onClick={() => setActiveItem(item.id)}
                  className={`ztax-nav-item ${isActive ? 'active' : ''}`}
                >
                  {item.icon}
                  <span>{item.label}</span>
                </button>
              );
            })}
          </div>
        ))}
      </div>

      {/* Footer Legal & Confidentiality */}
      <div className="ztax-sidebar-footer">
        <div>ZoikoTax is a trading name of Zoiko Tech Inc.</div>
        <div>Confidential - Product & Engineering</div>
      </div>
    </aside>
  );
};
