import React, { useState } from 'react';
import {
  FileText,
  DollarSign,
  Calendar,
  ShieldCheck,
  AlertCircle,
  Sparkles,
  ChevronRight,
  TrendingUp,
} from 'lucide-react';
import {
  ResponsiveContainer,
  LineChart,
  Line,
} from 'recharts';
import {
  clientKpiMetrics,
  clientActionItems,
  clientReadinessGauges,
  clientTrendSplineData,
} from '../data/clientDashboardData';

// Donut Progress Gauge matching client design
const CircularGauge: React.FC<{
  percentage: number;
  statusText: string;
  size?: number;
  strokeWidth?: number;
}> = ({ percentage, statusText, size = 96, strokeWidth = 8 }) => {
  const radius = (size - strokeWidth) / 2;
  const circumference = 2 * Math.PI * radius;
  const offset = circumference - (percentage / 100) * circumference;

  return (
    <div className="relative flex items-center justify-center shrink-0" style={{ width: size, height: size }}>
      <svg width={size} height={size} className="-rotate-90 transform">
        <circle
          cx={size / 2}
          cy={size / 2}
          r={radius}
          stroke="#e5e7eb"
          strokeWidth={strokeWidth}
          fill="transparent"
        />
        <circle
          cx={size / 2}
          cy={size / 2}
          r={radius}
          stroke="#10b981"
          strokeWidth={strokeWidth}
          strokeDasharray={circumference}
          strokeDashoffset={offset}
          strokeLinecap="round"
          fill="transparent"
          className="transition-all duration-700 ease-out"
        />
      </svg>
      <div className="absolute inset-0 flex flex-col items-center justify-center text-center leading-none">
        <span className="text-base font-bold text-gray-900 font-sans">
          {percentage}%
        </span>
        <span className="text-[10px] text-gray-400 font-medium mt-1">
          {statusText}
        </span>
      </div>
    </div>
  );
};

export const ClientDashboard: React.FC = () => {
  const [timeRange, setTimeRange] = useState<'7D' | '30D' | '90D'>('90D');

  return (
    <div className="space-y-4 text-gray-900 select-none pb-8">
      {/* 1. Tax Command Center Title & Status Bar */}
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <h1 className="text-xl font-bold tracking-tight text-gray-900 sm:text-2xl">
            Tax Command Center
          </h1>
          <div className="mt-1 flex items-center gap-1.5 text-xs text-gray-400">
            <span className="h-2 w-2 rounded-full bg-[#10b981]"></span>
            <span className="font-bold text-[#10b981]">LIVE</span>
            <span className="text-gray-300">|</span>
            <span>Authoritative view</span>
            <span className="text-gray-300">|</span>
            <span>Last updated 21 Sep 2026, 10:42 (UTC)</span>
          </div>
        </div>

        {/* Right Status Alert Badge & Action Button */}
        <div className="flex flex-wrap items-center gap-3">
          <div className="flex items-center gap-1.5 rounded-lg border border-red-200 bg-[#fee2e2] px-3 py-1.5 text-xs font-semibold text-[#dc2626]">
            <span>▲ What needs attention: 3 P0 | 7 P1 items require action</span>
          </div>

          <button className="rounded-lg bg-[#c86a3b] px-4 py-2 text-xs font-semibold text-white shadow-2xs hover:bg-[#b55d30] transition-colors cursor-pointer">
            Open Action Center
          </button>
        </div>
      </div>

      {/* 2. Priority Action Center Alert Banner */}
      <div className="flex items-center gap-2.5 rounded-lg border border-red-200 bg-[#fee2e2] px-3.5 py-2.5 text-xs">
        <AlertCircle className="h-4 w-4 text-red-600 shrink-0" />
        <div className="flex flex-col sm:flex-row sm:items-center sm:gap-2">
          <span className="font-bold text-red-900">Priority Action Center</span>
          <span className="text-red-800 text-[11px] sm:text-xs">
            Critical and high priority items require your attention. Resolve to stay compliant and avoid exposure.
          </span>
        </div>
      </div>

      {/* 3. ROW 1: 4 Key Metric / KPI Cards */}
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4">
        {/* Card 1: Determinations MTD */}
        <div className="rounded-xl border border-gray-200 bg-white p-4 shadow-2xs">
          <div className="flex items-center gap-2 text-xs text-gray-500 font-medium">
            <FileText className="h-4 w-4 text-gray-400" />
            <span>Determinations MTD</span>
          </div>
          <div className="mt-2 text-2xl font-bold tracking-tight text-gray-900">
            $ 12,482,360
          </div>
          <div className="mt-3 flex items-center justify-between text-xs">
            <span className="font-bold text-[#10b981]">▲ + 6.3% <span className="font-normal text-gray-400">vs. last period</span></span>
            <span className="text-[10px] text-gray-400 font-mono">Updated 10:32 UTC</span>
          </div>
        </div>

        {/* Card 2: Gross Fiscal Liability */}
        <div className="rounded-xl border border-gray-200 bg-white p-4 shadow-2xs">
          <div className="flex items-center gap-2 text-xs text-gray-500 font-medium">
            <DollarSign className="h-4 w-4 text-gray-400" />
            <span>Gross Fiscal Liability</span>
          </div>
          <div className="mt-2 text-2xl font-bold tracking-tight text-gray-900">
            $ 8,764,221
          </div>
          <div className="mt-3 flex items-center justify-between text-xs">
            <span className="font-bold text-[#10b981]">▲ + 4.8% <span className="font-normal text-gray-400">vs. last period</span></span>
            <span className="text-[10px] text-gray-400 font-mono">Updated 10:28 UTC</span>
          </div>
        </div>

        {/* Card 3: Obligations Due (30D) */}
        <div className="rounded-xl border border-gray-200 bg-white p-4 shadow-2xs">
          <div className="flex items-center justify-between text-xs text-gray-500 font-medium">
            <div className="flex items-center gap-2">
              <Calendar className="h-4 w-4 text-gray-400" />
              <span>Obligations Due (30D)</span>
            </div>
            <span className="rounded bg-[#fef3c7] px-1.5 py-0.5 text-[10px] font-bold text-[#d97706]">
              3 P0
            </span>
          </div>
          <div className="mt-2 text-2xl font-bold tracking-tight text-gray-900">
            $ 3,215,607
          </div>
          <div className="mt-3 flex items-center justify-between text-xs">
            <span className="font-bold text-[#10b981]">▲ + 12.6% <span className="font-normal text-gray-400">vs. last period</span></span>
            <span className="text-[10px] text-gray-400 font-mono">Updated 10:24 UTC</span>
          </div>
        </div>

        {/* Card 4: Reconciliation Integrity */}
        <div className="rounded-xl border border-gray-200 bg-white p-4 shadow-2xs">
          <div className="flex items-center gap-2 text-xs text-gray-500 font-medium">
            <ShieldCheck className="h-4 w-4 text-gray-400" />
            <span>Reconciliation Integrity</span>
          </div>
          <div className="mt-2 text-2xl font-bold tracking-tight text-gray-900">
            98.7%
          </div>
          <div className="mt-3 flex items-center justify-between text-xs">
            <span className="font-bold text-[#10b981]">▲ + 1.4% <span className="font-normal text-gray-400">vs. last period</span></span>
            <span className="text-[10px] text-gray-400 font-mono">Updated 10:20 UTC</span>
          </div>
        </div>
      </div>

      {/* 4. ROW 2: Left Trend Line Chart & Right Priority Action Center */}
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-12">
        {/* Left: Throughput & effective liability trend (58%) */}
        <div className="flex flex-col justify-between rounded-xl border border-gray-200 bg-white p-4 shadow-2xs lg:col-span-6 xl:col-span-6">
          <div>
            {/* Header */}
            <div className="flex items-center justify-between">
              <h3 className="text-xs font-bold text-gray-900">
                Throughput & effective liability trend
              </h3>
              <div className="flex items-center gap-2 text-xs">
                <button
                  onClick={() => setTimeRange('7D')}
                  className={`px-1.5 py-0.5 rounded text-[11px] font-medium cursor-pointer ${
                    timeRange === '7D' ? 'bg-[#1c1236] text-white font-bold' : 'text-gray-400 hover:text-gray-900'
                  }`}
                >
                  7D
                </button>
                <button
                  onClick={() => setTimeRange('30D')}
                  className={`px-1.5 py-0.5 rounded text-[11px] font-medium cursor-pointer ${
                    timeRange === '30D' ? 'bg-[#1c1236] text-white font-bold' : 'text-gray-400 hover:text-gray-900'
                  }`}
                >
                  30D
                </button>
                <button
                  onClick={() => setTimeRange('90D')}
                  className={`px-2 py-0.5 rounded text-[11px] cursor-pointer ${
                    timeRange === '90D' ? 'bg-[#1c1236] text-white font-bold' : 'text-gray-400 hover:text-gray-900'
                  }`}
                >
                  90D
                </button>
              </div>
            </div>

            {/* Clean minimalist trend line chart */}
            <div className="mt-4 h-[180px] w-full">
              <ResponsiveContainer width="100%" height="100%">
                <LineChart data={clientTrendSplineData} margin={{ top: 15, right: 10, left: 10, bottom: 5 }}>
                  <Line
                    type="monotone"
                    dataKey="effectiveLiability"
                    stroke="#5065f6"
                    strokeWidth={2.5}
                    dot={false}
                  />
                  <Line
                    type="monotone"
                    dataKey="throughput"
                    stroke="#f97e28"
                    strokeWidth={2.5}
                    dot={false}
                  />
                </LineChart>
              </ResponsiveContainer>
            </div>
          </div>

          {/* Legend matching client screenshot */}
          <div className="mt-2 flex items-center gap-5 text-[11px] text-gray-600">
            <div className="flex items-center gap-1.5">
              <span className="h-2 w-2 rounded-full bg-[#5065f6]"></span>
              <span>Effective liability (USD)</span>
            </div>
            <div className="flex items-center gap-1.5">
              <span className="h-2 w-2 rounded-full bg-[#f97e28]"></span>
              <span>Throughput (transactions)</span>
            </div>
          </div>
        </div>

        {/* Right: Priority Action Center Table (42%) */}
        <div className="rounded-xl border border-gray-200 bg-white p-4 shadow-2xs lg:col-span-6 xl:col-span-6">
          <div className="flex items-center justify-between">
            <h3 className="text-xs font-bold text-gray-900">Priority Action Center</h3>
            <button className="text-xs font-semibold text-[#3b82f6] hover:underline cursor-pointer">
              View all (10)
            </button>
          </div>

          {/* Table */}
          <div className="mt-3 overflow-x-auto">
            <table className="w-full text-left text-[11px]">
              <thead>
                <tr className="border-b border-gray-100 text-gray-400 text-[10px]">
                  <th className="pb-1.5 font-normal">P0/P1</th>
                  <th className="pb-1.5 font-normal">Issue</th>
                  <th className="pb-1.5 font-normal">Context</th>
                  <th className="pb-1.5 font-normal">Exposure</th>
                  <th className="pb-1.5 font-normal">Due / SLA</th>
                  <th className="pb-1.5 font-normal">Action</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-gray-50">
                {clientActionItems.map(item => (
                  <tr key={item.id} className="hover:bg-gray-50/60 transition-colors">
                    <td className="py-2 pr-2">
                      <span
                        className={`rounded px-1.5 py-0.5 text-[9px] font-bold ${
                          item.priority === 'P0'
                            ? 'bg-[#fee2e2] text-[#dc2626]'
                            : item.priority === 'P1'
                            ? 'bg-[#fef3c7] text-[#d97706]'
                            : 'bg-[#ccfbf1] text-[#0f766e]'
                        }`}
                      >
                        {item.priority}
                      </span>
                    </td>
                    <td className="py-2 pr-2 font-medium text-gray-900 truncate max-w-[140px]">
                      {item.issue}
                    </td>
                    <td className="py-2 pr-2 text-gray-500 whitespace-nowrap">
                      {item.context}
                    </td>
                    <td className="py-2 pr-2 font-bold text-gray-900 whitespace-nowrap">
                      {item.exposure}
                    </td>
                    <td
                      className={`py-2 pr-2 whitespace-nowrap ${
                        item.slaUrgent ? 'text-[#dc2626] font-medium' : 'text-gray-500'
                      }`}
                    >
                      {item.dueSla}
                    </td>
                    <td className="py-2 whitespace-nowrap">
                      <button className="font-semibold text-gray-900 hover:text-[#c86a3b] cursor-pointer">
                        {item.actionText}
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      </div>

      {/* 5. ROW 3: 3 Gauges + 1 AI Advisory Card */}
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
        {clientReadinessGauges.map(gauge => (
          <div
            key={gauge.id}
            className="flex flex-col justify-between rounded-xl border border-gray-200 bg-white p-4 shadow-2xs"
          >
            {/* Header */}
            <div className="flex items-start justify-between">
              <div>
                <h3 className="text-xs font-bold text-gray-900">{gauge.title}</h3>
                <div className="text-[10px] text-gray-400 mt-0.5">{gauge.subtitle}</div>
              </div>
              <button className="text-xs font-semibold text-[#3b82f6] hover:underline cursor-pointer">
                View all
              </button>
            </div>

            {/* Gauge & Legend side-by-side */}
            <div className="my-3 flex items-center justify-between gap-2">
              <CircularGauge
                percentage={gauge.percentage}
                statusText={gauge.statusLabel}
                size={86}
                strokeWidth={7}
              />

              <div className="flex flex-col space-y-1 text-[11px] min-w-[100px]">
                {gauge.segments.map((seg, idx) => (
                  <div key={idx} className="flex items-center justify-between gap-2">
                    <div className="flex items-center gap-1.5">
                      <span
                        className="h-1.5 w-1.5 rounded-full shrink-0"
                        style={{ backgroundColor: seg.color }}
                      ></span>
                      <span className="text-gray-500">{seg.label}</span>
                    </div>
                    <span className="font-bold text-gray-900">{seg.count}</span>
                  </div>
                ))}
              </div>
            </div>
          </div>
        ))}

        {/* Card 4: AI ADVISORY */}
        <div className="flex flex-col justify-between rounded-xl border border-gray-200 bg-white p-4 shadow-2xs">
          <div>
            {/* Header */}
            <div className="flex items-center gap-1.5">
              <Sparkles className="h-4 w-4 text-[#059669]" />
              <span className="text-[11px] font-bold uppercase tracking-wider text-[#059669]">
                AI ADVISORY
              </span>
            </div>

            {/* Content */}
            <div className="mt-2 text-xs font-bold leading-snug text-gray-900">
              Potential exposure detected in BR VAT due to upcoming rate change (Oct 2026).
            </div>

            <div className="mt-2 text-[10px] leading-relaxed text-gray-400">
              Source: ZoikoTax Intelligence advises updating rulesets for BR region to local v1.4.3 before Sep 30.
            </div>
          </div>

          {/* Action Link */}
          <div className="mt-3">
            <button className="flex items-center gap-0.5 text-xs font-bold text-[#059669] hover:underline cursor-pointer">
              <span>Review suggestion</span>
              <ChevronRight className="h-3 w-3" />
            </button>
          </div>
        </div>
      </div>
    </div>
  );
};
