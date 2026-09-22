export interface KpiMetric {
  id: string;
  title: string;
  value: string;
  change: string;
  isPositive: boolean;
  timestamp: string;
  badge?: {
    text: string;
    type: 'danger' | 'warning' | 'info';
  };
}

export interface ClientActionItem {
  id: string;
  priority: 'P0' | 'P1' | 'P2';
  issue: string;
  context: string;
  exposure: string;
  dueSla: string;
  slaUrgent: boolean;
  actionText: string;
}

export interface ReadinessMetric {
  id: string;
  title: string;
  subtitle: string;
  percentage: number;
  statusLabel: string;
  segments: {
    label: string;
    count: number;
    color: string;
  }[];
}

export interface TrendDataPoint {
  index: number;
  effectiveLiability: number;
  throughput: number;
}

export const clientKpiMetrics: KpiMetric[] = [
  {
    id: 'determinations-mtd',
    title: 'Determinations MTD',
    value: '$ 12,482,360',
    change: '+ 6.3% vs. last period',
    isPositive: true,
    timestamp: 'Updated 10:32 UTC',
  },
  {
    id: 'gross-fiscal-liability',
    title: 'Gross Fiscal Liability',
    value: '$ 8,764,221',
    change: '+ 4.8% vs. last period',
    isPositive: true,
    timestamp: 'Updated 10:28 UTC',
  },
  {
    id: 'obligations-due',
    title: 'Obligations Due (30D)',
    value: '$ 3,215,607',
    change: '+ 12.6% vs. last period',
    isPositive: true,
    timestamp: 'Updated 10:24 UTC',
    badge: {
      text: '3 P0',
      type: 'warning',
    },
  },
  {
    id: 'reconciliation-integrity',
    title: 'Reconciliation Integrity',
    value: '98.7%',
    change: '+ 1.4% vs. last period',
    isPositive: true,
    timestamp: 'Updated 10:20 UTC',
  },
];

export const clientActionItems: ClientActionItem[] = [
  {
    id: 'ACT-001',
    priority: 'P0',
    issue: 'VAT filing overdue',
    context: 'DE | Q2 2026',
    exposure: '$ 1,240,000',
    dueSla: '21 Sep 2026 (0d)',
    slaUrgent: true,
    actionText: 'File now',
  },
  {
    id: 'ACT-002',
    priority: 'P1',
    issue: 'Mismatch in intercom...',
    context: 'US | Jun 2026',
    exposure: '$ 782,400',
    dueSla: '23 Sep 2026 (2d)',
    slaUrgent: true,
    actionText: 'Review & adjust',
  },
  {
    id: 'ACT-003',
    priority: 'P1',
    issue: 'E-invoice validation e...',
    context: 'BR | Jun 2026',
    exposure: '$ 415,220',
    dueSla: '24 Sep 2026 (3d)',
    slaUrgent: true,
    actionText: 'Resolve errors',
  },
  {
    id: 'ACT-004',
    priority: 'P1',
    issue: 'Reconciliation break',
    context: 'GB | May 2026',
    exposure: '$ 298,760',
    dueSla: '26 Sep 2026 (5d)',
    slaUrgent: false,
    actionText: 'Investigate',
  },
  {
    id: 'ACT-005',
    priority: 'P2',
    issue: 'Shadow tax variance',
    context: 'ZA | Jun 2026',
    exposure: '$ 186,300',
    dueSla: '30 Sep 2026 (9d)',
    slaUrgent: false,
    actionText: 'Review',
  },
  {
    id: 'ACT-006',
    priority: 'P2',
    issue: 'Evidence not found',
    context: 'CA | May 2026',
    exposure: '$ 124,500',
    dueSla: '02 Oct 2026 (11d)',
    slaUrgent: false,
    actionText: 'Request evidence',
  },
];

export const clientReadinessGauges: ReadinessMetric[] = [
  {
    id: 'jurisdiction-readiness',
    title: 'Jurisdiction Readiness',
    subtitle: 'Global (120 jurisdictions)',
    percentage: 92,
    statusLabel: 'Ready',
    segments: [
      { label: 'Ready', count: 110, color: '#10b981' },
      { label: 'At risk', count: 6, color: '#f59e0b' },
      { label: 'Blocked', count: 4, color: '#ef4444' },
    ],
  },
  {
    id: 'reconciliation-breaks',
    title: 'Reconciliation & Breaks',
    subtitle: 'All entities & periods',
    percentage: 96,
    statusLabel: 'Ready',
    segments: [
      { label: 'Reconciled', count: 112, color: '#0d9488' },
      { label: 'Breaks', count: 5, color: '#f59e0b' },
      { label: 'Open', count: 2, color: '#ef4444' },
    ],
  },
  {
    id: 'evidence-replay',
    title: 'Evidence & Replay',
    subtitle: 'Immutable ledger',
    percentage: 94,
    statusLabel: 'Ready',
    segments: [
      { label: 'Complete', count: 318, color: '#10b981' },
      { label: 'Partial', count: 14, color: '#f59e0b' },
      { label: 'Missing', count: 8, color: '#ef4444' },
    ],
  },
];

// Matches the exact spline shape in client screenshot
export const clientTrendSplineData: TrendDataPoint[] = [
  { index: 1, effectiveLiability: 54, throughput: 34 },
  { index: 2, effectiveLiability: 62, throughput: 39 },
  { index: 3, effectiveLiability: 58, throughput: 37 },
  { index: 4, effectiveLiability: 70, throughput: 44 },
  { index: 5, effectiveLiability: 66, throughput: 40 },
  { index: 6, effectiveLiability: 82, throughput: 50 },
  { index: 7, effectiveLiability: 78, throughput: 48 },
  { index: 8, effectiveLiability: 80, throughput: 49 },
];
