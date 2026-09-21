import React, { useState } from 'react';
import { Sidebar } from './Sidebar';
import { Header } from './Header';
import { useApp } from '../../context/AppContext';
import { X, CheckCircle, AlertTriangle, AlertCircle, Info } from 'lucide-react';

export const DashboardLayout: React.FC<{ children: React.ReactNode }> = ({ children }) => {
  const [collapsed, setCollapsed] = useState(false);
  const { toasts, removeToast } = useApp();

  return (
    <div className="min-h-screen flex flex-col font-sans antialiased transition-colors">
      {/* Sidebar Navigation */}
      <Sidebar collapsed={collapsed} setCollapsed={setCollapsed} />

      {/* Main Column */}
      <div
        className={`flex flex-1 flex-col transition-all duration-300 ${
          collapsed ? 'lg:pl-18' : 'lg:pl-72'
        }`}
      >
        {/* Top Header */}
        <Header />

        {/* Toast Notification Container */}
        <div className="fixed bottom-4 right-4 z-50 flex flex-col gap-2 max-w-md w-full px-4 pointer-events-none">
          {toasts.map(toast => (
            <div
              key={toast.id}
              className={`pointer-events-auto flex items-start gap-3 rounded-xl border p-4 shadow-2xl backdrop-blur-xl transition-all ${
                toast.type === 'success'
                  ? 'border-[#26735b]/40 bg-white/95 dark:bg-[#150c33]/95 text-[#26735b] dark:text-[#34d399]'
                  : toast.type === 'warning'
                  ? 'border-[#9a5b12]/40 bg-white/95 dark:bg-[#150c33]/95 text-[#9a5b12] dark:text-[#fbbf24]'
                  : toast.type === 'error'
                  ? 'border-[#ef4444]/40 bg-white/95 dark:bg-[#150c33]/95 text-[#ef4444] dark:text-[#f87171]'
                  : 'border-[#bf6735]/40 bg-white/95 dark:bg-[#150c33]/95 text-[#dd7134]'
              }`}
            >
              <div className="mt-0.5">
                {toast.type === 'success' && <CheckCircle className="h-4 w-4" />}
                {toast.type === 'warning' && <AlertTriangle className="h-4 w-4" />}
                {toast.type === 'error' && <AlertCircle className="h-4 w-4" />}
                {toast.type === 'info' && <Info className="h-4 w-4" />}
              </div>
              <div className="flex-1 text-xs">
                <div className="font-bold theme-text-primary">{toast.title}</div>
                <div className="mt-0.5 theme-text-secondary text-[11px] leading-relaxed">
                  {toast.message}
                </div>
              </div>
              <button
                onClick={() => removeToast(toast.id)}
                className="theme-text-muted hover:theme-text-primary transition-colors cursor-pointer"
              >
                <X className="h-4 w-4" />
              </button>
            </div>
          ))}
        </div>

        {/* Viewport Content */}
        <main className="flex-1 p-4 sm:p-6 lg:p-8 max-w-7xl w-full mx-auto">
          {children}
        </main>
      </div>
    </div>
  );
};
