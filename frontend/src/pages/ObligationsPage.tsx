import React, { useState, useEffect } from 'react';
import { apiService } from '../services/apiService';
import { FilingObligation, NexusThresholdStatus, ResponsibilityRoleAssignment } from '../types/domain';
import { DataTable, Column } from '../components/common/DataTable';
import { StatusBadge } from '../components/common/StatusBadge';
import {
  CalendarCheck,
  Clock,
  ShieldCheck,
  CheckCircle2,
} from 'lucide-react';

export const ObligationsPage: React.FC = () => {
  const [data, setData] = useState<{
    responsibilities: ResponsibilityRoleAssignment[];
    nexusThresholds: NexusThresholdStatus[];
    filingObligations: FilingObligation[];
  } | null>(null);
  const [loading, setLoading] = useState(true);
  const [activeTab, setActiveTab] = useState<'calendar' | 'nexus' | 'responsibility'>('calendar');

  useEffect(() => {
    async function load() {
      setLoading(true);
      const res = await apiService.getObligationsData();
      setData(res);
      setLoading(false);
    }
    load();
  }, []);

  if (loading || !data) {
    return (
      <div className="flex h-96 items-center justify-center">
        <div className="h-8 w-8 animate-spin rounded-full border-2 border-[#dd7134] border-t-transparent" />
      </div>
    );
  }

  const obligationColumns: Column<FilingObligation>[] = [
    {
      key: 'formNumber',
      header: 'Statutory Form & Authority',
      sortable: true,
      render: row => (
        <div>
          <div className="font-bold theme-text-primary text-xs">{row.formNumber}</div>
          <div className="text-[10px] theme-text-muted">{row.authorityName}</div>
        </div>
      ),
    },
    {
      key: 'jurisdiction',
      header: 'Jurisdiction',
      sortable: true,
      render: row => (
        <span className="font-mono text-[#dd7134] font-bold text-xs">{row.jurisdiction}</span>
      ),
    },
    {
      key: 'period',
      header: 'Reporting Period',
      render: row => <span className="font-mono theme-text-secondary text-xs">{row.period}</span>,
    },
    {
      key: 'dueDate',
      header: 'Filing Due Date',
      sortable: true,
      render: row => (
        <div className="flex items-center gap-1.5 font-mono text-xs text-[#fbbf24]">
          <Clock className="h-3.5 w-3.5" />
          {row.dueDate}
        </div>
      ),
    },
    {
      key: 'accruedAmount',
      header: 'Accrued Liability',
      sortable: true,
      align: 'right',
      render: row => (
        <span className="font-mono font-bold theme-text-primary text-xs">{row.accruedAmount}</span>
      ),
    },
    {
      key: 'filingStatus',
      header: 'Status',
      sortable: true,
      align: 'right',
      render: row => <StatusBadge status={row.filingStatus} />,
    },
  ];

  return (
    <div className="space-y-6">
      {/* Page Header */}
      <div className="rounded-2xl border theme-card p-6 backdrop-blur-md">
        <div className="flex flex-col md:flex-row md:items-center md:justify-between gap-4">
          <div>
            <div className="flex items-center gap-2">
              <span className="rounded bg-[#bf6735]/15 px-2 py-0.5 font-mono text-[10px] font-semibold text-[#dd7134] border border-[#bf6735]/40">
                SPECIFICATION: ZTAX-OBL-001
              </span>
              <span className="text-xs theme-text-muted">•</span>
              <span className="text-xs theme-text-secondary font-medium">Statutory Compliance Lifecycle</span>
            </div>
            <h1 className="mt-2 text-2xl font-bold tracking-tight theme-text-primary">
              Responsibility & Obligation Engine
            </h1>
            <p className="mt-1 text-xs theme-text-muted max-w-2xl leading-relaxed">
              Determine who owes what, to which authority, and by when. Evaluates the 5-Role Responsibility model,
              tracks state economic nexus thresholds, and schedules periodic filings.
            </p>
          </div>

          {/* Tab Controls */}
          <div className="flex items-center gap-1 rounded-xl border border-[var(--border-main)] theme-subtle p-1">
            <button
              onClick={() => setActiveTab('calendar')}
              className={`rounded-lg px-3 py-1.5 text-xs font-semibold transition-all cursor-pointer ${
                activeTab === 'calendar'
                  ? 'bg-gradient-to-r from-[#bf6735] to-[#dd7134] text-white shadow'
                  : 'theme-text-muted hover:theme-text-primary'
              }`}
            >
              Filing Calendar
            </button>
            <button
              onClick={() => setActiveTab('nexus')}
              className={`rounded-lg px-3 py-1.5 text-xs font-semibold transition-all cursor-pointer ${
                activeTab === 'nexus'
                  ? 'bg-gradient-to-r from-[#bf6735] to-[#dd7134] text-white shadow'
                  : 'theme-text-muted hover:theme-text-primary'
              }`}
            >
              Nexus Accumulators
            </button>
            <button
              onClick={() => setActiveTab('responsibility')}
              className={`rounded-lg px-3 py-1.5 text-xs font-semibold transition-all cursor-pointer ${
                activeTab === 'responsibility'
                  ? 'bg-gradient-to-r from-[#bf6735] to-[#dd7134] text-white shadow'
                  : 'theme-text-muted hover:theme-text-primary'
              }`}
            >
              5-Role Responsibility
            </button>
          </div>
        </div>
      </div>

      {/* Tab 1: Statutory Filing Calendar */}
      {activeTab === 'calendar' && (
        <div className="space-y-4">
          <div className="grid grid-cols-1 sm:grid-cols-3 gap-4">
            <div className="rounded-xl border theme-card p-4">
              <div className="flex items-center justify-between text-xs theme-text-muted">
                <span>Accrued Statutory Obligations</span>
                <CalendarCheck className="h-4 w-4 text-[#dd7134]" />
              </div>
              <div className="mt-2 font-mono text-2xl font-bold theme-text-primary">$716,170.00</div>
              <div className="mt-1 text-[11px] theme-text-muted">Across 5 open periodic returns</div>
            </div>

            <div className="rounded-xl border theme-card p-4">
              <div className="flex items-center justify-between text-xs theme-text-muted">
                <span>Next Statutory Deadline</span>
                <Clock className="h-4 w-4 text-[#fbbf24]" />
              </div>
              <div className="mt-2 font-mono text-2xl font-bold text-[#fbbf24]">Oct 07, 2026</div>
              <div className="mt-1 text-[11px] theme-text-muted">HMRC VAT 100 Return (16 days)</div>
            </div>

            <div className="rounded-xl border theme-card p-4">
              <div className="flex items-center justify-between text-xs theme-text-muted">
                <span>Direct USF Contributor</span>
                <CheckCircle2 className="h-4 w-4 text-[#34d399]" />
              </div>
              <div className="mt-2 font-mono text-2xl font-bold text-[#34d399]">Active (499-Q)</div>
              <div className="mt-1 text-[11px] theme-text-muted">De minimis threshold exceeded</div>
            </div>
          </div>

          <DataTable
            data={data.filingObligations}
            columns={obligationColumns}
            searchPlaceholder="Search statutory returns by form or authority..."
          />
        </div>
      )}

      {/* Tab 2: Economic Nexus Accumulators */}
      {activeTab === 'nexus' && (
        <div className="space-y-4">
          <div className="rounded-xl border theme-card p-5">
            <h3 className="text-xs font-bold uppercase tracking-wider theme-text-primary mb-4">
              State Telecom Economic Nexus & De Minimis Threshold Monitoring
            </h3>
            <div className="space-y-4">
              {data.nexusThresholds.map((n, i) => (
                <div
                  key={i}
                  className="rounded-xl border theme-subtle p-4 text-xs space-y-2"
                >
                  <div className="flex items-center justify-between">
                    <div className="flex items-center gap-2">
                      <span className="font-mono font-bold text-[#dd7134] text-sm">
                        {n.jurisdictionCode}
                      </span>
                      <span className="font-semibold theme-text-primary">{n.jurisdictionName}</span>
                    </div>
                    <StatusBadge status={n.registrationStatus} />
                  </div>

                  <div className="flex items-center justify-between text-[11px] theme-text-muted pt-1">
                    <span>
                      Accumulated Gross: <b className="theme-text-primary">{n.accumulatedSales}</b> / Threshold:{' '}
                      {n.economicThresholdAmount}
                    </span>
                    <span className="font-mono font-bold text-[#dd7134]">
                      {n.percentThresholdReached}% Threshold
                    </span>
                  </div>

                  {/* Progress Bar */}
                  <div className="h-2 w-full overflow-hidden rounded-full bg-[var(--border-main)]">
                    <div
                      className={`h-full transition-all ${
                        n.percentThresholdReached >= 100
                          ? 'bg-[#34d399]'
                          : n.percentThresholdReached >= 85
                          ? 'bg-[#fbbf24]'
                          : 'bg-[#dd7134]'
                      }`}
                      style={{ width: `${Math.min(100, n.percentThresholdReached)}%` }}
                    />
                  </div>

                  <div className="flex items-center justify-between text-[10px] theme-text-muted pt-1">
                    <span>Transactions: {n.accumulatedTransactions.toLocaleString()}</span>
                    <span>
                      {n.isNexusEstablished ? 'Nexus Legally Established' : 'Under Statutory Threshold'}
                    </span>
                  </div>
                </div>
              ))}
            </div>
          </div>
        </div>
      )}

      {/* Tab 3: 5-Role Responsibility Matrix */}
      {activeTab === 'responsibility' && (
        <div className="space-y-4">
          <div className="rounded-xl border theme-card p-5">
            <h3 className="text-xs font-bold uppercase tracking-wider theme-text-primary mb-1">
              Five-Role Responsibility Decision Matrix (ZTAX-OBL-001 §3)
            </h3>
            <p className="text-xs theme-text-muted mb-4">
              Determines statutory remittance liability across multi-tier telecom supply chains:
              Supplier vs Facility Carrier vs MVNO/Reseller vs End-User.
            </p>

            <div className="space-y-4">
              {data.responsibilities.map((r, i) => (
                <div
                  key={i}
                  className="rounded-xl border theme-subtle p-4 text-xs space-y-3"
                >
                  <div className="flex items-center justify-between border-b border-[var(--border-main)] pb-2">
                    <div>
                      <div className="font-bold theme-text-primary text-sm">{r.transactionScope}</div>
                      <div className="text-[11px] theme-text-muted">{r.productFamily}</div>
                    </div>
                    <span className="rounded bg-[#bf6735]/15 px-2.5 py-1 font-mono text-xs font-bold text-[#dd7134] border border-[#bf6735]/40">
                      REMITTANCE ROLE: {r.statutoryRemittanceRole}
                    </span>
                  </div>

                  <div className="grid grid-cols-2 sm:grid-cols-4 gap-3 text-[11px]">
                    <div>
                      <span className="theme-text-muted">1. Supplier:</span>
                      <div className="font-medium theme-text-primary">{r.supplier}</div>
                    </div>
                    <div>
                      <span className="theme-text-muted">2. Carrier Network:</span>
                      <div className="font-medium theme-text-primary">{r.carrier}</div>
                    </div>
                    <div>
                      <span className="theme-text-muted">3. Intermediary / MVNO:</span>
                      <div className="font-medium theme-text-primary">{r.resellerMvno}</div>
                    </div>
                    <div>
                      <span className="theme-text-muted">4. End Customer:</span>
                      <div className="font-medium theme-text-primary">{r.endCustomer}</div>
                    </div>
                  </div>

                  <div className="rounded theme-card p-2.5 border border-[var(--border-main)] text-[11px] font-mono theme-text-secondary flex items-center gap-2">
                    <ShieldCheck className="h-4 w-4 text-[#34d399] flex-shrink-0" />
                    <span>Citation: {r.legalBasisCitation}</span>
                  </div>
                </div>
              ))}
            </div>
          </div>
        </div>
      )}
    </div>
  );
};
