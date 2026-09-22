import React from 'react';
import { Sidebar } from './Sidebar';
import { Header } from './Header';

export const DashboardLayout: React.FC<{ children: React.ReactNode }> = ({ children }) => {
  return (
    <div className="min-h-screen flex bg-[#f5f6fa] font-sans antialiased text-gray-900">
      {/* Left Sidebar */}
      <Sidebar />

      {/* Main Content Area */}
      <div className="flex flex-1 flex-col pl-56 min-w-0">
        {/* Top Header (Tier 1 and Tier 2) */}
        <Header />

        {/* Page Content Viewport */}
        <main className="flex-1 p-5 w-full">
          {children}
        </main>
      </div>
    </div>
  );
};
