import React, { useState, useEffect } from 'react';
import { useApp } from '../context/AppContext';
import { apiService } from '../services/apiService';
import { StatCard } from '../components/common/StatCard';
import { StatusBadge } from '../components/common/StatusBadge';
import {
  AreaChart,
  Area,
  XAxis,
  YAxis,
  Tooltip,
  ResponsiveContainer,
  PieChart,
  Pie,
  Cell,
} from 'recharts';
import {
  Activity,
  ShieldCheck,
  AlertTriangle,
  ArrowRight,
  TrendingUp,
  FileCheck,
  Zap,
} from 'lucide-react';

export const OverviewPage: React.FC = () => {
  const { setActivePage, activeTenant, theme } = useApp();
  const [metrics, setMetrics] = useState<any>(null);
  const [recentDecisions, setRecentDecisions] = useState<any[]>([]);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    async function loadData() {
      setLoading(true);
      const data = await apiService.getOverviewMetrics();
      const decisions = await apiService.getDecisions();
      setMetrics(data);
      setRecentDecisions(decisions.slice(0, 4));
      setLoading(false);
    }
    loadData();
  }, [activeTenant]);

  if (loading || !metrics) {
    return (
      <div className="flex h-96 items-center justify-center">
        <div className="flex flex-col items-center gap-3">
          <div className="h-8 w-8 animate-spin rounded-full border-2 border-[#dd7134] border-t-transparent" />
          <span className="text-xs theme-text-muted font-mono">Loading fiscal state...</span>
        </div>
      </div>
    );
  }

  return (
    <div className="space-y-6">
      {/* Top Banner with ZoikoTax Branding (High contrast in Light & Dark Theme) */}
      <div className="relative overflow-hidden rounded-2xl border p-6 shadow-xl transition-all border-[#e2d9ec] dark:border-[#432375] bg-gradient-to-r from-[#f5effa] via-[#ffffff] to-[#fff6f0] dark:from-[#1b0838] dark:via-[#150c33] dark:to-[#240c42]">
        <div className="flex flex-col md:flex-row md:items-center md:justify-between gap-4">
          <div>
            <div className="flex items-center gap-2">
              <span className="rounded-md bg-[#bf6735]/15 px-2 py-0.5 font-mono text-[10px] font-bold text-[#dd7134] border border-[#bf6735]/30">
                ACTIVE TENANT: {activeTenant.code}
              </span>
              <span className="text-xs theme-text-muted">•</span>
              <span className="text-xs theme-text-secondary font-medium">{activeTenant.name}</span>
            </div>
            <h1 className="mt-2 text-2xl font-bold tracking-tight text-[#1b1140] dark:text-[#f6f3fb] sm:text-3xl">
              Global Telecom Fiscal Command Center
            </h1>
            <p className="mt-1 text-xs text-[#535054] dark:text-[#d6d0e3] max-w-2xl leading-relaxed">
              Real-time transaction taxability, regulatory USF/PUC surcharges, append-only evidence sealing,
              and statutory obligation monitoring across 11,000+ jurisdictions.
            </p>
          </div>

          <div className="flex flex-wrap gap-2.5">
            <button
              onClick={() => setActivePage('determination')}
              className="inline-flex items-center gap-2 rounded-xl bg-gradient-to-r from-[#bf6735] to-[#dd7134] hover:from-[#dd7134] hover:to-[#f2792a] px-4 py-2.5 text-xs font-semibold text-white shadow-[0_4px_14px_0_rgba(221,113,52,0.35)] transition-all cursor-pointer"
            >
              <Zap className="h-4 w-4" />
              Live Quote Simulator
            </button>
            <button
              onClick={() => setActivePage('decisions')}
              className="inline-flex items-center gap-2 rounded-xl border border-[#d8cedd] dark:border-[#432375] bg-white dark:bg-[#1f1352] px-4 py-2.5 text-xs font-semibold text-[#1b1140] dark:text-[#f6f3fb] hover:border-[#dd7134] dark:hover:border-[#dd7134] transition-all cursor-pointer shadow-sm"
            >
              <FileCheck className="h-4 w-4 text-[#dd7134]" />
              Evidence Ledger
            </button>
          </div>
        </div>
      </div>

      {/* KPI Cards Grid */}
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
        {metrics.kpis.map((kpi: any, idx: number) => (
          <StatCard
            key={idx}
            title={kpi.title}
            value={kpi.value}
            subValue={kpi.subValue}
            trend={kpi.trend}
            trendPercent={kpi.trendPercent}
            description={kpi.description}
            badge={kpi.badge}
          />
        ))}
      </div>

      {/* Charts Section: Volume & Distribution */}
      <div className="grid grid-cols-1 gap-6 lg:grid-cols-3">
        {/* Left: 24h Hourly Tax & Surcharge Determination Throughput */}
        <div className="lg:col-span-2 rounded-xl border theme-card p-5">
          <div className="flex flex-wrap items-center justify-between border-b theme-subtle pb-4 gap-2">
            <div>
              <h2 className="text-sm font-bold theme-text-primary tracking-tight flex items-center gap-2">
                <TrendingUp className="h-4 w-4 text-[#dd7134]" />
                24h Determination Volume & Surcharge Run-Rate
              </h2>
              <p className="text-[11px] theme-text-muted">
                Transaction taxes vs regulatory fees computed on the C0 hot path (sub-15ms p99)
              </p>
            </div>
            <div className="flex items-center gap-3 text-[11px]">
              <span className="flex items-center gap-1.5 theme-text-secondary">
                <span className="h-2 w-2 rounded-full bg-[#dd7134]" />
                Tax
              </span>
              <span className="flex items-center gap-1.5 theme-text-secondary">
                <span className="h-2 w-2 rounded-full bg-[#8b5cf6]" />
                Regulatory (USF)
              </span>
              <span className="rounded theme-subtle px-2 py-0.5 font-mono text-[10px] theme-text-secondary">
                UTC
              </span>
            </div>
          </div>

          <div className="mt-4 h-64 w-full">
            <ResponsiveContainer width="100%" height="100%">
              <AreaChart data={metrics.volumeSeries} margin={{ top: 10, right: 15, left: 5, bottom: 0 }}>
                <defs>
                  <linearGradient id="colorTax" x1="0" y1="0" x2="0" y2="1">
                    <stop offset="5%" stopColor="#dd7134" stopOpacity={0.4} />
                    <stop offset="95%" stopColor="#dd7134" stopOpacity={0} />
                  </linearGradient>
                  <linearGradient id="colorReg" x1="0" y1="0" x2="0" y2="1">
                    <stop offset="5%" stopColor="#5b2a86" stopOpacity={0.4} />
                    <stop offset="95%" stopColor="#5b2a86" stopOpacity={0} />
                  </linearGradient>
                </defs>
                <XAxis dataKey="time" stroke={theme === 'dark' ? '#7e7694' : '#807a89'} fontSize={11} tickLine={false} />
                <YAxis
                  stroke={theme === 'dark' ? '#7e7694' : '#807a89'}
                  fontSize={11}
                  tickLine={false}
                  axisLine={false}
                  width={48}
                  tickFormatter={v => `$${Math.round(v / 1000)}k`}
                />
                <Tooltip
                  contentStyle={{
                    backgroundColor: theme === 'dark' ? '#100030' : '#ffffff',
                    borderColor: theme === 'dark' ? '#38276b' : '#e2d9ec',
                    borderRadius: '8px',
                    fontSize: '11px',
                    color: theme === 'dark' ? '#fff' : '#1b1140',
                    boxShadow: '0 4px 12px rgba(0,0,0,0.1)',
                  }}
                />
                <Area
                  type="monotone"
                  dataKey="taxCalculated"
                  name="Transaction Tax ($)"
                  stroke="#dd7134"
                  strokeWidth={2}
                  fillOpacity={1}
                  fill="url(#colorTax)"
                />
                <Area
                  type="monotone"
                  dataKey="regulatoryFees"
                  name="Regulatory Fees / USF ($)"
                  stroke="#8b5cf6"
                  strokeWidth={2}
                  fillOpacity={1}
                  fill="url(#colorReg)"
                />
              </AreaChart>
            </ResponsiveContainer>
          </div>
        </div>

        {/* Right: Taxability vs Regulatory Fee Allocation Breakdown */}
        <div className="rounded-xl border theme-card p-5 flex flex-col justify-between">
          <div className="border-b theme-subtle pb-3">
            <h2 className="text-sm font-bold theme-text-primary tracking-tight flex items-center gap-2">
              <ShieldCheck className="h-4 w-4 text-[#dd7134]" />
              Liability Composition (ZTAX-CLS-001)
            </h2>
            <p className="text-[11px] theme-text-muted">
              Taxability vs Regulatory Revenue Split
            </p>
          </div>

          <div className="my-1 h-44 w-full">
            <ResponsiveContainer width="100%" height="100%">
              <PieChart>
                <Pie
                  data={metrics.categoryDistribution}
                  cx="50%"
                  cy="50%"
                  innerRadius={44}
                  outerRadius={66}
                  paddingAngle={4}
                  dataKey="value"
                >
                  {metrics.categoryDistribution.map((entry: any, index: number) => (
                    <Cell key={`cell-${index}`} fill={entry.color} />
                  ))}
                </Pie>
                <Tooltip
                  formatter={(value: any) => [`${value}%`, 'Share']}
                  contentStyle={{
                    backgroundColor: theme === 'dark' ? '#100030' : '#ffffff',
                    borderColor: theme === 'dark' ? '#38276b' : '#e2d9ec',
                    borderRadius: '8px',
                    fontSize: '11px',
                    color: theme === 'dark' ? '#fff' : '#1b1140',
                  }}
                />
              </PieChart>
            </ResponsiveContainer>
          </div>

          <div className="space-y-1.5 pt-1 text-xs">
            {metrics.categoryDistribution.map((item: any, i: number) => (
              <div key={i} className="flex items-center justify-between text-[11px]">
                <div className="flex items-center gap-2">
                  <span className="h-2 w-2 rounded-full shrink-0" style={{ backgroundColor: item.color }} />
                  <span className="theme-text-secondary truncate">{item.name}</span>
                </div>
                <div className="font-mono theme-text-muted shrink-0">{item.amount} ({item.value}%)</div>
              </div>
            ))}
          </div>
        </div>
      </div>

      {/* Bottom Split: Recent Decisions Feed & Regional Nexus Heat */}
      <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
        {/* Recent Authoritative Decisions */}
        <div className="rounded-xl border theme-card p-5">
          <div className="flex items-center justify-between border-b theme-border pb-4">
            <div>
              <h2 className="text-sm font-bold theme-text-primary tracking-tight flex items-center gap-2">
                <Activity className="h-4 w-4 text-[#26735b] dark:text-[#34d399]" />
                Live Fiscal Decision Stream
              </h2>
              <p className="text-[11px] theme-text-muted">
                Committed transactions sealed with SHA-256 evidence manifests
              </p>
            </div>
            <button
              onClick={() => setActivePage('decisions')}
              className="text-xs text-[#dd7134] hover:text-[#f2792a] flex items-center gap-1 font-semibold cursor-pointer"
            >
              View All <ArrowRight className="h-3.5 w-3.5" />
            </button>
          </div>

          <div className="mt-4 space-y-3">
            {recentDecisions.map((tx: any) => (
              <div
                key={tx.id}
                onClick={() => setActivePage('decisions')}
                className="group flex flex-col sm:flex-row sm:items-center justify-between gap-2 rounded-xl border theme-subtle p-3.5 hover:border-[#bf6735]/60 transition-all cursor-pointer shadow-sm"
              >
                <div>
                  <div className="flex items-center gap-2">
                    <StatusBadge status={tx.status} />
                    <span className="font-mono text-xs font-bold theme-text-primary">
                      {tx.idempotencyKey}
                    </span>
                    <span className="text-[10px] theme-text-muted font-mono">
                      {tx.originJurisdiction} → {tx.destinationJurisdiction}
                    </span>
                  </div>
                  <div className="mt-1 text-[11px] theme-text-muted">
                    Net: <span className="font-mono theme-text-primary">${tx.netAmount}</span> | Tax:{' '}
                    <span className="font-mono text-[#dd7134]">${tx.totalTaxAmount}</span> | Reg:{' '}
                    <span className="font-mono text-[#5b2a86] dark:text-[#c084fc]">${tx.totalRegulatoryFees}</span>
                  </div>
                </div>

                <div className="text-right">
                  <div className="font-mono text-xs font-bold theme-text-primary">
                    ${tx.grossAmount} {tx.currency}
                  </div>
                  <div className="font-mono text-[9px] theme-text-muted truncate max-w-[130px]">
                    {tx.evidenceManifestHash.slice(0, 16)}...
                  </div>
                </div>
              </div>
            ))}
          </div>
        </div>

        {/* Regional Jurisdictions & Nexus Status */}
        <div className="rounded-xl border theme-card p-5">
          <div className="flex items-center justify-between border-b theme-border pb-4">
            <div>
              <h2 className="text-sm font-bold theme-text-primary tracking-tight flex items-center gap-2">
                <AlertTriangle className="h-4 w-4 text-[#9a5b12] dark:text-[#fbbf24]" />
                Statutory Jurisdiction Nexus & Exposure
              </h2>
              <p className="text-[11px] theme-text-muted">
                Economic nexus thresholds and regulatory filings radar
              </p>
            </div>
            <button
              onClick={() => setActivePage('obligations')}
              className="text-xs text-[#dd7134] hover:text-[#f2792a] flex items-center gap-1 font-semibold cursor-pointer"
            >
              Obligations Engine <ArrowRight className="h-3.5 w-3.5" />
            </button>
          </div>

          <div className="mt-4 divide-y theme-subtle">
            {metrics.jurisdictionHeat.map((j: any) => (
              <div key={j.code} className="flex items-center justify-between py-2.5 text-xs">
                <div className="flex items-center gap-2.5">
                  <span className="font-mono font-bold text-[#dd7134] text-xs w-16">
                    {j.code}
                  </span>
                  <div>
                    <div className="font-semibold theme-text-primary">{j.name}</div>
                    <div className="text-[10px] theme-text-muted">Compliance Rate: {j.complianceRate}</div>
                  </div>
                </div>
                <div className="text-right">
                  <div className="font-mono font-bold theme-text-primary">{j.liability}</div>
                  <StatusBadge status={j.status} size="sm" />
                </div>
              </div>
            ))}
          </div>
        </div>
      </div>
    </div>
  );
};
