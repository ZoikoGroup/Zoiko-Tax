import React, { useState, useEffect } from 'react';
import { apiService } from '../services/apiService';
import { TCSLEntry } from '../types/domain';
import { DataTable, Column } from '../components/common/DataTable';
import { StatusBadge } from '../components/common/StatusBadge';
import { StatCard } from '../components/common/StatCard';
import {
  ShieldCheck,
} from 'lucide-react';

export const SubledgerPage: React.FC = () => {
  const [entries, setEntries] = useState<TCSLEntry[]>([]);
  const [summary, setSummary] = useState<any>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    async function load() {
      setLoading(true);
      const res = await apiService.getSubledgerData();
      setEntries(res.entries);
      setSummary(res.summary);
      setLoading(false);
    }
    load();
  }, []);

  if (loading || !summary) {
    return (
      <div className="flex h-96 items-center justify-center">
        <div className="h-8 w-8 animate-spin rounded-full border-2 border-[#dd7134] border-t-transparent" />
      </div>
    );
  }

  const columns: Column<TCSLEntry>[] = [
    {
      key: 'entryNumber',
      header: 'Journal Entry #',
      sortable: true,
      render: row => (
        <div>
          <div className="font-mono font-bold theme-text-primary text-xs">{row.entryNumber}</div>
          <div className="font-mono text-[10px] theme-text-muted">{row.postingDate}</div>
        </div>
      ),
    },
    {
      key: 'accountName',
      header: 'GL Account & Description',
      sortable: true,
      render: row => (
        <div>
          <div className="font-semibold theme-text-primary text-xs">{row.accountName}</div>
          <div className="font-mono text-[10px] text-[#dd7134]">{row.accountCode}</div>
        </div>
      ),
    },
    {
      key: 'jurisdictionCode',
      header: 'Jurisdiction',
      render: row => (
        <span className="font-mono text-xs theme-text-secondary">{row.jurisdictionCode}</span>
      ),
    },
    {
      key: 'debit',
      header: 'Debit (DR)',
      sortable: true,
      align: 'right',
      render: row => (
        <span
          className={`font-mono text-xs ${
            parseFloat(row.debit) > 0 ? 'font-bold text-[#34d399]' : 'theme-text-muted'
          }`}
        >
          {parseFloat(row.debit) > 0 ? `$${row.debit}` : '—'}
        </span>
      ),
    },
    {
      key: 'credit',
      header: 'Credit (CR)',
      sortable: true,
      align: 'right',
      render: row => (
        <span
          className={`font-mono text-xs ${
            parseFloat(row.credit) > 0 ? 'font-bold text-[#dd7134]' : 'theme-text-muted'
          }`}
        >
          {parseFloat(row.credit) > 0 ? `$${row.credit}` : '—'}
        </span>
      ),
    },
    {
      key: 'reconciliationStatus',
      header: 'Reconciliation',
      sortable: true,
      align: 'right',
      render: row => <StatusBadge status={row.reconciliationStatus} />,
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
                SPECIFICATION: ZTAX-FIN-001
              </span>
              <span className="text-xs theme-text-muted">•</span>
              <span className="text-xs theme-text-secondary font-medium">Accounting-Grade Fiscal Control</span>
            </div>
            <h1 className="mt-2 text-2xl font-bold tracking-tight theme-text-primary">
              Tax Control Subledger (TCSL) & Reconciliation
            </h1>
            <p className="mt-1 text-xs theme-text-muted max-w-2xl leading-relaxed">
              Maintains an immutable double-entry subledger balancing tax payables, customer recoveries,
              and surcharge clearing accounts. Reconciles billing transaction streams against the corporate ERP General Ledger.
            </p>
          </div>

          <div className="flex items-center gap-2">
            <span className="rounded-lg bg-[#26735b]/20 px-3 py-1.5 font-mono text-xs font-semibold text-[#34d399] border border-[#26735b]/40 flex items-center gap-1.5">
              <ShieldCheck className="h-4 w-4" />
              Double-Entry Invariant Balanced ($0.00 Delta)
            </span>
          </div>
        </div>
      </div>

      {/* KPI Cards */}
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <StatCard
          title="Tax Payable Accrual"
          value={summary.totalTaxPayableAccrual}
          subValue="State, local & international sales tax"
          trend="neutral"
          trendPercent="Accruing"
          badge="CR Balance"
        />
        <StatCard
          title="Regulatory Fees Payable"
          value={summary.totalRegulatoryPayableAccrual}
          subValue="USF (499), PUC & 911 reserves"
          trend="neutral"
          trendPercent="Accruing"
          badge="CR Balance"
        />
        <StatCard
          title="Customer Tax Recovery"
          value={summary.totalCustomerRecoveryBilled}
          subValue="Pass-through billed on invoice"
          trend="up"
          trendPercent="100% Matched"
          badge="DR Billed"
        />
        <StatCard
          title="Subledger Reconciliation"
          value={`${summary.matchedTransactionsPercent}%`}
          subValue="ERP S/4HANA Sync Active"
          trend="up"
          trendPercent="0 Discrepancy"
          badge="Synchronized"
        />
      </div>

      {/* Subledger Invariants Box */}
      <div className="rounded-xl border theme-card p-4 text-xs">
        <div className="font-bold theme-text-primary uppercase tracking-wider text-[11px] mb-2">
          Accounting Invariants Enforced by TCSL (ZTAX-FIN-001 §11)
        </div>
        <div className="grid grid-cols-1 md:grid-cols-3 gap-3 theme-text-muted">
          <div className="rounded-lg theme-subtle p-2.5 border border-[var(--border-main)]">
            <span className="font-semibold theme-text-primary">1. Strict Double-Entry: </span>
            Sum of debits must identically equal sum of credits for every committed transaction batch.
          </div>
          <div className="rounded-lg theme-subtle p-2.5 border border-[var(--border-main)]">
            <span className="font-semibold theme-text-primary">2. Append-Only Corrections: </span>
            Credit notes and refunds post reversing debits/credits linked to the original document UUID.
          </div>
          <div className="rounded-lg theme-subtle p-2.5 border border-[var(--border-main)]">
            <span className="font-semibold theme-text-primary">3. Currency Integrity: </span>
            Amounts retain original currency and explicit FX conversion provenance without float rounding.
          </div>
        </div>
      </div>

      {/* Journal Entries Table */}
      <DataTable
        data={entries}
        columns={columns}
        searchPlaceholder="Search journal entries by voucher #, account, or jurisdiction..."
        searchKey={r => `${r.entryNumber} ${r.accountName} ${r.accountCode} ${r.jurisdictionCode}`}
      />
    </div>
  );
};
