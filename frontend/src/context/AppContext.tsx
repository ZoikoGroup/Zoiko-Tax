import React, { createContext, useContext, useState, useEffect } from 'react';
import { Tenant, UserPersona } from '../types/domain';
import { mockTenants } from '../data/mockTenants';
import { apiService, CellHealthStatus } from '../services/apiService';

export type NavigationPage =
  | 'overview'
  | 'determination'
  | 'catalog'
  | 'obligations'
  | 'decisions'
  | 'subledger'
  | 'packs'
  | 'ai_governance';

export interface ToastMessage {
  id: string;
  type: 'success' | 'info' | 'warning' | 'error';
  title: string;
  message: string;
}

export type ThemeMode = 'dark' | 'light';

interface AppContextType {
  activePage: NavigationPage;
  setActivePage: (page: NavigationPage) => void;
  activeTenant: Tenant;
  setActiveTenant: (tenant: Tenant) => void;
  activePersona: UserPersona;
  setActivePersona: (persona: UserPersona) => void;
  searchQuery: string;
  setSearchQuery: (query: string) => void;
  cellHealth: CellHealthStatus;
  refreshCellHealth: () => Promise<void>;
  toggleApiMode: () => void;
  theme: ThemeMode;
  toggleTheme: () => void;
  toasts: ToastMessage[];
  addToast: (type: ToastMessage['type'], title: string, message: string) => void;
  removeToast: (id: string) => void;
}

const AppContext = createContext<AppContextType | undefined>(undefined);

export const AppProvider: React.FC<{ children: React.ReactNode }> = ({ children }) => {
  const [activePage, setActivePage] = useState<NavigationPage>('overview');
  const [activeTenant, setActiveTenant] = useState<Tenant>(mockTenants[0]);
  const [activePersona, setActivePersona] = useState<UserPersona>('tax_director');
  const [searchQuery, setSearchQuery] = useState('');
  const [theme, setTheme] = useState<ThemeMode>(() => {
    const saved = localStorage.getItem('ztax_theme') as ThemeMode;
    return saved === 'light' ? 'light' : 'dark';
  });

  const [cellHealth, setCellHealth] = useState<CellHealthStatus>({
    liveness: 'healthy',
    readiness: 'ready',
    activeCell: 'us-east-1-demo-cell',
    bundleVersion: 'v2026.9.1-signed',
    mode: 'MOCK_SANDBOX',
  });

  const [toasts, setToasts] = useState<ToastMessage[]>([
    {
      id: 'init-1',
      type: 'info',
      title: 'ZoikoTax Cell Active',
      message: 'Running ADR-0002 decimal arithmetic context with signed Country Pack v2026.9.1.',
    },
  ]);

  const toggleTheme = () => {
    const nextTheme: ThemeMode = theme === 'dark' ? 'light' : 'dark';
    setTheme(nextTheme);
    localStorage.setItem('ztax_theme', nextTheme);
    addToast(
      'info',
      `Switched to ${nextTheme === 'light' ? 'Light Theme (zoikotax.com)' : 'Dark Theme'}`,
      `Color palette now matching official ${nextTheme === 'light' ? 'light design system' : 'deep night palette'}.`
    );
  };

  useEffect(() => {
    if (theme === 'light') {
      document.documentElement.classList.remove('dark');
      document.documentElement.classList.add('light');
    } else {
      document.documentElement.classList.remove('light');
      document.documentElement.classList.add('dark');
    }
  }, [theme]);

  const refreshCellHealth = async () => {
    const status = await apiService.checkHealth();
    setCellHealth(status);
  };

  const toggleApiMode = () => {
    const newMode = cellHealth.mode === 'MOCK_SANDBOX' ? 'LIVE_CELL' : 'MOCK_SANDBOX';
    apiService.setMode(newMode);
    addToast(
      'info',
      `Switched to ${newMode}`,
      newMode === 'LIVE_CELL'
        ? 'Attempting live probes to http://localhost:8080'
        : 'Running in high-fidelity mock sandbox mode.'
    );
    refreshCellHealth();
  };

  const addToast = (type: ToastMessage['type'], title: string, message: string) => {
    const id = Math.random().toString(36).substring(2, 9);
    setToasts(prev => [...prev, { id, type, title, message }]);
    setTimeout(() => {
      removeToast(id);
    }, 6000);
  };

  const removeToast = (id: string) => {
    setToasts(prev => prev.filter(t => t.id !== id));
  };

  useEffect(() => {
    refreshCellHealth();
  }, []);

  return (
    <AppContext.Provider
      value={{
        activePage,
        setActivePage,
        activeTenant,
        setActiveTenant,
        activePersona,
        setActivePersona,
        searchQuery,
        setSearchQuery,
        cellHealth,
        refreshCellHealth,
        toggleApiMode,
        theme,
        toggleTheme,
        toasts,
        addToast,
        removeToast,
      }}
    >
      {children}
    </AppContext.Provider>
  );
};

export const useApp = () => {
  const context = useContext(AppContext);
  if (!context) {
    throw new Error('useApp must be used within an AppProvider');
  }
  return context;
};
