import React, { useState, useEffect } from 'react';
import { apiService } from '../services/apiService';
import { AIGovernanceLog } from '../types/domain';
import { DataTable, Column } from '../components/common/DataTable';
import { StatusBadge } from '../components/common/StatusBadge';
import { StatCard } from '../components/common/StatCard';
import {
  ShieldAlert,
  Lock,
} from 'lucide-react';

export const AIGovernancePage: React.FC = () => {
  const [logs, setLogs] = useState<AIGovernanceLog[]>([]);
  const [metrics, setMetrics] = useState<any>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    async function load() {
      setLoading(true);
      const res = await apiService.getAIGovernanceData();
      setLogs(res.logs);
      setMetrics(res.metrics);
      setLoading(false);
    }
    load();
  }, []);

  if (loading || !metrics) {
    return (
      <div className="flex h-96 items-center justify-center">
        <div className="h-8 w-8 animate-spin rounded-full border-2 border-[#dd7134] border-t-transparent" />
      </div>
    );
  }

  const columns: Column<AIGovernanceLog>[] = [
    {
      key: 'taskType',
      header: 'Task & Model Identifier',
      sortable: true,
      render: row => (
        <div>
          <div className="font-bold theme-text-primary text-xs">{row.taskType}</div>
          <div className="text-[10px] font-mono text-[#dd7134]">{row.modelIdentifier}</div>
        </div>
      ),
    },
    {
      key: 'modelRiskTier',
      header: 'Model Risk Tier',
      sortable: true,
      render: row => <StatusBadge status={row.modelRiskTier} />,
    },
    {
      key: 'aiProposal',
      header: 'AI Proposal / Discovery',
      render: row => (
        <div className="max-w-xs truncate text-xs theme-text-secondary" title={row.aiProposal}>
          {row.aiProposal}
        </div>
      ),
    },
    {
      key: 'deterministicDAGDecision',
      header: 'Deterministic Rule DAG Outcome',
      render: row => (
        <div className="max-w-xs truncate font-mono text-[11px] text-[#34d399]" title={row.deterministicDAGDecision}>
          {row.deterministicDAGDecision}
        </div>
      ),
    },
    {
      key: 'varianceStatus',
      header: 'Shadow Assurance Status',
      sortable: true,
      render: row => <StatusBadge status={row.varianceStatus} />,
    },
    {
      key: 'humanReviewerStatus',
      header: 'Human Sign-off',
      sortable: true,
      align: 'right',
      render: row => <StatusBadge status={row.humanReviewerStatus} />,
    },
  ];

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="rounded-2xl border theme-card p-6 backdrop-blur-md">
        <div className="flex flex-col md:flex-row md:items-center md:justify-between gap-4">
          <div>
            <div className="flex items-center gap-2">
              <span className="rounded bg-[#bf6735]/15 px-2 py-0.5 font-mono text-[10px] font-semibold text-[#dd7134] border border-[#bf6735]/40">
                SPECIFICATION: ZTAX-AIGOV-001
              </span>
              <span className="text-xs theme-text-muted">•</span>
              <span className="text-xs theme-text-secondary font-medium">Constitutional Authority Boundary</span>
            </div>
            <h1 className="mt-2 text-2xl font-bold tracking-tight theme-text-primary">
              AI Governance & Shadow Assurance Fabric
            </h1>
            <p className="mt-1 text-xs theme-text-muted max-w-2xl leading-relaxed">
              AI may discover, extract, classify, rank, recommend, and explain; but AI is strictly forbidden from
              directly modifying authoritative tax rates or ledger formulas. Shadow Assurance continuously monitors
              production decisions against AI models to detect anomalies.
            </p>
          </div>

          <div className="flex items-center gap-2">
            <span className="rounded-lg bg-[#26735b]/20 px-3 py-1.5 font-mono text-xs font-semibold text-[#34d399] border border-[#26735b]/40 flex items-center gap-1.5">
              <Lock className="h-4 w-4" />
              Boundary Enforcement: 100.00%
            </span>
          </div>
        </div>
      </div>

      {/* KPI Cards */}
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <StatCard
          title="Constitutional Boundary Rate"
          value={metrics.authorityBoundaryEnforcementRate}
          subValue="0 Unauthorized Mutations"
          trend="up"
          trendPercent="Guaranteed"
          badge="ADR-0006 Sealed"
        />
        <StatCard
          title="Shadow Audited Transactions"
          value={metrics.totalTransactionsAuditedByShadowAssurance}
          subValue="Real-time parallel comparison"
          trend="up"
          trendPercent="99.96% Concurrence"
          badge="Dual Pipeline"
        />
        <StatCard
          title="Anomaly Detection Rate"
          value={metrics.anomalyDetectionRate}
          subValue="Flagged for human tax review"
          trend="down"
          trendPercent="0.04% Outliers"
          badge="Supervised"
        />
        <StatCard
          title="Human Certified Sign-offs"
          value={metrics.humanInTheLoopSignoffs.toString()}
          subValue="2 Pending proposals in queue"
          trend="neutral"
          trendPercent="Active Review"
          badge="Certified"
        />
      </div>

      {/* Constitutional Principles Box */}
      <div className="rounded-xl border theme-card p-4 text-xs">
        <div className="font-bold theme-text-primary uppercase tracking-wider text-[11px] mb-2 flex items-center gap-2">
          <ShieldAlert className="h-4 w-4 text-[#fbbf24]" />
          The Constitutional AI Rules of ZoikoTax (ZTAX-AIGOV-001 §1)
        </div>
        <div className="grid grid-cols-1 md:grid-cols-3 gap-3 theme-text-muted">
          <div className="rounded-lg theme-subtle p-2.5 border border-[var(--border-main)]">
            <span className="font-semibold theme-text-primary">1. Separation of Concerns: </span>
            `internal/domain/fiscal` cannot import `internal/domain/ai`. No direct function converts AI output into a tax liability.
          </div>
          <div className="rounded-lg theme-subtle p-2.5 border border-[var(--border-main)]">
            <span className="font-semibold theme-text-primary">2. Mandatory Human Gate: </span>
            The only path from an AI catalog suggestion to an authoritative production tax rule runs through certified human tax review.
          </div>
          <div className="rounded-lg theme-subtle p-2.5 border border-[var(--border-main)]">
            <span className="font-semibold theme-text-primary">3. Shadow Assurance: </span>
            AI runs asynchronously as an auditor, alerting compliance officers to discrepancies between billing feeds and regulatory guidelines.
          </div>
        </div>
      </div>

      {/* Shadow Assurance Table */}
      <DataTable
        data={logs}
        columns={columns}
        searchPlaceholder="Search AI audit logs by model, prompt snippet, or outcome..."
        searchKey={r => `${r.modelIdentifier} ${r.inputSnippet} ${r.aiProposal} ${r.taskType}`}
      />
    </div>
  );
};
