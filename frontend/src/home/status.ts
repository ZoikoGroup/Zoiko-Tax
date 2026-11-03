import type { DashboardMarketStatus } from '@zoikotax/sdk'

/** A market state as the customer console names it (Doc 16 §04). */
export const STATUS_LABEL: Readonly<Record<DashboardMarketStatus | 'NONE', string>> = {
  ACTIVE: 'Active',
  PILOT: 'In pilot',
  RESEARCH_ONLY: 'Research only',
  NONE: 'Not available',
}
