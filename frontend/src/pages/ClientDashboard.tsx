import { useState } from 'react';
import {
  IconFileText,
  IconDollarSign,
  IconCalendar,
  IconShieldCheck,
  IconAlertCircle,
  IconSparkles,
  IconChevronRight,
} from '../components/common/DashboardIcons';
import {
  clientActionItems,
  clientReadinessGauges,
} from '../data/clientDashboardData';

// Donut Progress Gauge matching client design
const CircularGauge = ({
  percentage,
  statusText,
  size = 96,
  strokeWidth = 8,
}: {
  percentage: number;
  statusText: string;
  size?: number;
  strokeWidth?: number;
}) => {
  const radius = (size - strokeWidth) / 2;
  const circumference = 2 * Math.PI * radius;
  const offset = circumference - (percentage / 100) * circumference;

  return (
    <div style={{ position: 'relative', width: size, height: size, flexShrink: 0 }}>
      <svg width={size} height={size} style={{ transform: 'rotate(-90deg)' }}>
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
        />
      </svg>
      <div
        style={{
          position: 'absolute',
          inset: 0,
          display: 'flex',
          flexDirection: 'column',
          alignItems: 'center',
          justifyContent: 'center',
          textAlign: 'center',
          lineHeight: 1,
        }}
      >
        <span style={{ fontSize: 16, fontWeight: 700, color: '#111827' }}>
          {percentage}%
        </span>
        <span style={{ fontSize: 10, color: '#9ca3af', fontWeight: 500, marginTop: 4 }}>
          {statusText}
        </span>
      </div>
    </div>
  );
};

export const ClientDashboard = () => {
  const [timeRange, setTimeRange] = useState<'7D' | '30D' | '90D'>('90D');

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
      {/* 1. Tax Command Center Title & Status Bar */}
      <div className="ztax-title-bar">
        <div>
          <h1 className="ztax-title-text">
            Tax Command Center
          </h1>
          <div className="ztax-status-line">
            <span className="ztax-green-dot" />
            <span className="ztax-live-text">LIVE</span>
            <span style={{ color: '#d1d5db' }}>|</span>
            <span>Authoritative view</span>
            <span style={{ color: '#d1d5db' }}>|</span>
            <span>Last updated 21 Sep 2026, 10:42 (UTC)</span>
          </div>
        </div>

        {/* Right Status Alert Badge & Action Button */}
        <div className="ztax-title-actions">
          <div className="ztax-attention-chip">
            <span>▲ What needs attention: 3 P0 | 7 P1 items require action</span>
          </div>

          <button type="button" className="ztax-btn-copper">
            Open Action Center
          </button>
        </div>
      </div>

      {/* 2. Priority Action Center Alert Banner */}
      <div className="ztax-alert-banner">
        <IconAlertCircle className="ztax-alert-icon" style={{ width: 16, height: 16 }} />
        <div className="ztax-alert-content">
          <span className="ztax-alert-title">Priority Action Center</span>
          <span className="ztax-alert-desc">
            Critical and high priority items require your attention. Resolve to stay compliant and avoid exposure.
          </span>
        </div>
      </div>

      {/* 3. ROW 1: 4 Key Metric / KPI Cards */}
      <div className="ztax-kpi-grid">
        {/* Card 1: Determinations MTD */}
        <div className="ztax-card">
          <div className="ztax-kpi-header">
            <div className="ztax-kpi-header-left">
              <IconFileText style={{ width: 16, height: 16, color: '#9ca3af' }} />
              <span>Determinations MTD</span>
            </div>
          </div>
          <div className="ztax-kpi-value">
            $ 12,482,360
          </div>
          <div className="ztax-kpi-footer">
            <span className="ztax-delta-green">
              ▲ + 6.3% <span className="ztax-delta-label">vs. last period</span>
            </span>
            <span className="ztax-timestamp">Updated 10:32 UTC</span>
          </div>
        </div>

        {/* Card 2: Gross Fiscal Liability */}
        <div className="ztax-card">
          <div className="ztax-kpi-header">
            <div className="ztax-kpi-header-left">
              <IconDollarSign style={{ width: 16, height: 16, color: '#9ca3af' }} />
              <span>Gross Fiscal Liability</span>
            </div>
          </div>
          <div className="ztax-kpi-value">
            $ 8,764,221
          </div>
          <div className="ztax-kpi-footer">
            <span className="ztax-delta-green">
              ▲ + 4.8% <span className="ztax-delta-label">vs. last period</span>
            </span>
            <span className="ztax-timestamp">Updated 10:28 UTC</span>
          </div>
        </div>

        {/* Card 3: Obligations Due (30D) */}
        <div className="ztax-card">
          <div className="ztax-kpi-header">
            <div className="ztax-kpi-header-left">
              <IconCalendar style={{ width: 16, height: 16, color: '#9ca3af' }} />
              <span>Obligations Due (30D)</span>
            </div>
            <span className="ztax-badge-p0">
              3 P0
            </span>
          </div>
          <div className="ztax-kpi-value">
            $ 3,215,607
          </div>
          <div className="ztax-kpi-footer">
            <span className="ztax-delta-green">
              ▲ + 12.6% <span className="ztax-delta-label">vs. last period</span>
            </span>
            <span className="ztax-timestamp">Updated 10:24 UTC</span>
          </div>
        </div>

        {/* Card 4: Reconciliation Integrity */}
        <div className="ztax-card">
          <div className="ztax-kpi-header">
            <div className="ztax-kpi-header-left">
              <IconShieldCheck style={{ width: 16, height: 16, color: '#9ca3af' }} />
              <span>Reconciliation Integrity</span>
            </div>
          </div>
          <div className="ztax-kpi-value">
            98.7%
          </div>
          <div className="ztax-kpi-footer">
            <span className="ztax-delta-green">
              ▲ + 1.4% <span className="ztax-delta-label">vs. last period</span>
            </span>
            <span className="ztax-timestamp">Updated 10:20 UTC</span>
          </div>
        </div>
      </div>

      {/* 4. ROW 2: Left Trend Sparkline Chart & Right Priority Action Center */}
      <div className="ztax-split-row">
        {/* Left: Throughput & effective liability trend */}
        <div className="ztax-card ztax-chart-card">
          <div>
            {/* Header */}
            <div className="ztax-chart-header">
              <h3 className="ztax-card-title">
                Throughput & effective liability trend
              </h3>
              <div className="ztax-time-toggles">
                <button
                  type="button"
                  onClick={() => setTimeRange('7D')}
                  className={`ztax-toggle-btn ${timeRange === '7D' ? 'active' : ''}`}
                >
                  7D
                </button>
                <button
                  type="button"
                  onClick={() => setTimeRange('30D')}
                  className={`ztax-toggle-btn ${timeRange === '30D' ? 'active' : ''}`}
                >
                  30D
                </button>
                <button
                  type="button"
                  onClick={() => setTimeRange('90D')}
                  className={`ztax-toggle-btn ${timeRange === '90D' ? 'active' : ''}`}
                >
                  90D
                </button>
              </div>
            </div>

            {/* Clean SVG sparkline trend chart */}
            <div className="ztax-sparkline-container">
              <svg viewBox="0 0 500 160" width="100%" height="100%" preserveAspectRatio="none">
                {/* Horizontal guide lines */}
                <line x1="0" y1="30" x2="500" y2="30" stroke="#f3f4f6" strokeWidth="1" strokeDasharray="3 3" />
                <line x1="0" y1="80" x2="500" y2="80" stroke="#f3f4f6" strokeWidth="1" strokeDasharray="3 3" />
                <line x1="0" y1="130" x2="500" y2="130" stroke="#f3f4f6" strokeWidth="1" strokeDasharray="3 3" />

                {/* Blue line: Effective liability */}
                <path
                  d="M 10 110 Q 70 85 130 95 T 250 65 T 370 85 T 450 35 T 495 40"
                  fill="none"
                  stroke="#5065f6"
                  strokeWidth="2.5"
                  strokeLinecap="round"
                />

                {/* Orange line: Throughput */}
                <path
                  d="M 10 140 Q 70 132 130 138 T 250 118 T 370 135 T 450 95 T 495 100"
                  fill="none"
                  stroke="#f97e28"
                  strokeWidth="2.5"
                  strokeLinecap="round"
                />
              </svg>
            </div>
          </div>

          {/* Legend */}
          <div className="ztax-chart-legend">
            <div className="ztax-legend-item">
              <span className="ztax-legend-dot-blue" />
              <span>Effective liability (USD)</span>
            </div>
            <div className="ztax-legend-item">
              <span className="ztax-legend-dot-orange" />
              <span>Throughput (transactions)</span>
            </div>
          </div>
        </div>

        {/* Right: Priority Action Center Table */}
        <div className="ztax-card">
          <div className="ztax-table-header">
            <h3 className="ztax-card-title">Priority Action Center</h3>
            <button type="button" className="ztax-link-blue">
              View all (10)
            </button>
          </div>

          {/* Table */}
          <div style={{ overflowX: 'auto' }}>
            <table className="ztax-action-table">
              <thead>
                <tr>
                  <th>P0/P1</th>
                  <th>Issue</th>
                  <th>Context</th>
                  <th>Exposure</th>
                  <th>Due / SLA</th>
                  <th>Action</th>
                </tr>
              </thead>
              <tbody>
                {clientActionItems.map(item => (
                  <tr key={item.id}>
                    <td style={{ paddingRight: 8 }}>
                      <span
                        className={`ztax-badge-priority ${
                          item.priority === 'P0'
                            ? 'ztax-badge-p0-red'
                            : item.priority === 'P1'
                            ? 'ztax-badge-p1-yellow'
                            : 'ztax-badge-p2-teal'
                        }`}
                      >
                        {item.priority}
                      </span>
                    </td>
                    <td style={{ fontWeight: 500, color: '#111827', paddingRight: 8 }}>
                      {item.issue}
                    </td>
                    <td style={{ color: '#6b7280', paddingRight: 8, whiteSpace: 'nowrap' }}>
                      {item.context}
                    </td>
                    <td style={{ fontWeight: 700, color: '#111827', paddingRight: 8, whiteSpace: 'nowrap' }}>
                      {item.exposure}
                    </td>
                    <td
                      style={{
                        paddingRight: 8,
                        whiteSpace: 'nowrap',
                        color: item.slaUrgent ? '#dc2626' : '#6b7280',
                        fontWeight: item.slaUrgent ? 600 : 400,
                      }}
                    >
                      {item.dueSla}
                    </td>
                    <td style={{ whiteSpace: 'nowrap' }}>
                      <button type="button" className="ztax-table-action-btn">
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
      <div className="ztax-gauges-grid">
        {clientReadinessGauges.map(gauge => (
          <div key={gauge.id} className="ztax-card ztax-gauge-card">
            {/* Header */}
            <div style={{ display: 'flex', alignItems: 'flex-start', justifyContent: 'space-between' }}>
              <div>
                <h3 className="ztax-card-title">{gauge.title}</h3>
                <div style={{ fontSize: 10, color: '#9ca3af', marginTop: 2 }}>{gauge.subtitle}</div>
              </div>
              <button type="button" className="ztax-link-blue">
                View all
              </button>
            </div>

            {/* Gauge & Legend side-by-side */}
            <div className="ztax-gauge-content">
              <CircularGauge
                percentage={gauge.percentage}
                statusText={gauge.statusLabel}
                size={86}
                strokeWidth={7}
              />

              <div className="ztax-gauge-breakdown">
                {gauge.segments.map((seg, idx) => (
                  <div key={idx} className="ztax-breakdown-row">
                    <div className="ztax-breakdown-left">
                      <span
                        style={{
                          width: 6,
                          height: 6,
                          borderRadius: '50%',
                          backgroundColor: seg.color,
                          display: 'inline-block',
                          flexShrink: 0,
                        }}
                      />
                      <span>{seg.label}</span>
                    </div>
                    <span className="ztax-breakdown-count">{seg.count}</span>
                  </div>
                ))}
              </div>
            </div>
          </div>
        ))}

        {/* Card 4: AI ADVISORY */}
        <div className="ztax-card" style={{ display: 'flex', flexDirection: 'column', justifyContent: 'space-between' }}>
          <div>
            {/* Header */}
            <div className="ztax-ai-header">
              <IconSparkles style={{ width: 16, height: 16, color: '#059669' }} />
              <span className="ztax-ai-badge">
                AI ADVISORY
              </span>
            </div>

            {/* Content */}
            <div className="ztax-ai-body">
              Potential exposure detected in BR VAT due to upcoming rate change (Oct 2026).
            </div>

            <div className="ztax-ai-source">
              Source: ZoikoTax Intelligence advises updating rulesets for BR region to local v1.4.3 before Sep 30.
            </div>
          </div>

          {/* Action Link */}
          <div className="ztax-ai-action">
            <button type="button" className="ztax-link-green">
              <span>Review suggestion</span>
              <IconChevronRight style={{ width: 12, height: 12 }} />
            </button>
          </div>
        </div>
      </div>
    </div>
  );
};
