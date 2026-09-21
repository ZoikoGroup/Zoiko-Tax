import React from 'react';
import { AppProvider, useApp } from './context/AppContext';
import { DashboardLayout } from './components/layout/DashboardLayout';
import { OverviewPage } from './pages/OverviewPage';
import { DeterminationPage } from './pages/DeterminationPage';
import { CatalogClassificationPage } from './pages/CatalogClassificationPage';
import { ObligationsPage } from './pages/ObligationsPage';
import { DecisionsReplayPage } from './pages/DecisionsReplayPage';
import { SubledgerPage } from './pages/SubledgerPage';
import { CountryPacksPage } from './pages/CountryPacksPage';
import { AIGovernancePage } from './pages/AIGovernancePage';

const DashboardContent: React.FC = () => {
  const { activePage } = useApp();

  switch (activePage) {
    case 'overview':
      return <OverviewPage />;
    case 'determination':
      return <DeterminationPage />;
    case 'catalog':
      return <CatalogClassificationPage />;
    case 'obligations':
      return <ObligationsPage />;
    case 'decisions':
      return <DecisionsReplayPage />;
    case 'subledger':
      return <SubledgerPage />;
    case 'packs':
      return <CountryPacksPage />;
    case 'ai_governance':
      return <AIGovernancePage />;
    default:
      return <OverviewPage />;
  }
};

export function App() {
  return (
    <AppProvider>
      <DashboardLayout>
        <DashboardContent />
      </DashboardLayout>
    </AppProvider>
  );
}

export default App;
