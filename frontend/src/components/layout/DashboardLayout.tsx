import type { ReactNode } from 'react';
import { Sidebar } from './Sidebar';
import { Header } from './Header';

export const DashboardLayout = ({ children }: { children: ReactNode }) => {
  return (
    <div className="ztax-layout">
      {/* Left Sidebar */}
      <Sidebar />

      {/* Main Content Area */}
      <div className="ztax-main-container">
        {/* Top Header */}
        <Header />

        {/* Page Content Viewport */}
        <main className="ztax-dashboard-viewport">
          {children}
        </main>
      </div>
    </div>
  );
};
