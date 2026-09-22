import React from 'react';
import { AppProvider } from './context/AppContext';
import { DashboardLayout } from './components/layout/DashboardLayout';
import { ClientDashboard } from './pages/ClientDashboard';

export function App() {
  return (
    <AppProvider>
      <DashboardLayout>
        <ClientDashboard />
      </DashboardLayout>
    </AppProvider>
  );
}

export default App;
