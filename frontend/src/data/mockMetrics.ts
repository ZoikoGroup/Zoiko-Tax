export interface MetricCardData {
  title: string;
  value: string;
  subValue: string;
  trend: 'up' | 'down' | 'neutral';
  trendPercent: string;
  description: string;
  badge?: string;
}

export const mockOverviewMetrics: MetricCardData[] = [
  {
    title: 'Transactions Processed (MTD)',
    value: '48.2M',
    subValue: 'p99 Latency: 11.4 ms',
    trend: 'up',
    trendPercent: '+12.4%',
    description: 'Hot path C0 determination throughput across active cells',
    badge: '100% Deterministic',
  },
  {
    title: 'Gross Liability Calculated',
    value: '$3,842,910.45',
    subValue: 'Net Tax: $2.14M | Regulatory: $1.70M',
    trend: 'up',
    trendPercent: '+8.1%',
    description: 'Combined telecom transaction tax & regulatory fees',
    badge: 'Decimal Math',
  },
  {
    title: 'Active Certified Country Packs',
    value: '5 Packs',
    subValue: '28,390 Active Rules',
    trend: 'up',
    trendPercent: '100% Golden Tests',
    description: 'Federal, State, Municipal, UK-HMRC, and EU Packs',
    badge: 'v2026.9 Pinned',
  },
  {
    title: 'Subledger Balance & Replay Integrity',
    value: '100.00%',
    subValue: '0 Discrepancy | 48.2M Manifests',
    trend: 'up',
    trendPercent: '0 Replay Drift',
    description: 'TCSL double-entry match & cryptographic audit seals',
    badge: 'Auditor Sealed',
  },
];

export const mockVolumeTimeSeries = [
  { time: '00:00', volume: 1840, taxCalculated: 14200, regulatoryFees: 11200 },
  { time: '03:00', volume: 1120, taxCalculated: 8900, regulatoryFees: 6900 },
  { time: '06:00', volume: 2980, taxCalculated: 24100, regulatoryFees: 18900 },
  { time: '09:00', volume: 5410, taxCalculated: 46200, regulatoryFees: 38400 },
  { time: '12:00', volume: 6890, taxCalculated: 58900, regulatoryFees: 49100 },
  { time: '15:00', volume: 6320, taxCalculated: 54100, regulatoryFees: 44300 },
  { time: '18:00', volume: 4910, taxCalculated: 41800, regulatoryFees: 34200 },
  { time: '21:00', volume: 3150, taxCalculated: 27500, regulatoryFees: 21900 },
];

// Aligned with official zoikotax.com brand colors
export const mockTaxCategoryDistribution = [
  { name: 'US Federal USF (499-A)', value: 38.4, color: '#dd7134', amount: '$1,475,677' },
  { name: 'State & Local Sales Tax', value: 32.1, color: '#f4a261', amount: '$1,233,574' },
  { name: 'State PUC / Surcharges', value: 14.5, color: '#5b2a86', amount: '$557,222' },
  { name: '911 / E911 Emergency Fees', value: 8.2, color: '#26735b', amount: '$315,118' },
  { name: 'UK / EU VAT', value: 6.8, color: '#315b9a', amount: '$261,319' },
];

export const mockJurisdictionHeat = [
  { code: 'US-FED', name: 'Federal (FCC / USAC)', liability: '$1,475,677', complianceRate: '100%', status: 'COMPLIANT' },
  { code: 'US-CA', name: 'California (CDTFA & CPUC)', liability: '$742,190', complianceRate: '100%', status: 'COMPLIANT' },
  { code: 'US-TX', name: 'Texas (Comptroller & TUSF)', liability: '$512,840', complianceRate: '100%', status: 'COMPLIANT' },
  { code: 'US-NY', name: 'New York (DTF & MTA)', liability: '$388,410', complianceRate: '99.8%', status: 'MONITORING' },
  { code: 'GB-UK', name: 'United Kingdom (HMRC)', liability: '£208,400', complianceRate: '100%', status: 'COMPLIANT' },
  { code: 'US-FL', name: 'Florida (CST)', liability: '$198,300', complianceRate: '100%', status: 'COMPLIANT' },
];
