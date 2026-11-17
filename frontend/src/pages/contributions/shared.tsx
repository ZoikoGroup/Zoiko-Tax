/**
 * Pieces of the Provider Contributions page: vocabularies, the reason codes
 * a write is refused with, and the CSV of a period's control totals.
 *
 * Every amount is the string the cell computed. Nothing here adds, compares
 * or rounds one (ADR-0019 C1, UAT-OBL-034).
 */
import { ZoikoTaxError, type S } from '@zoikotax/sdk'

import { downloadCsv } from '../workspace'

export type Contribution = S<'ProviderContribution'>
export type Regime = S<'ContributionRegime'>
export type SourceType = S<'ContributionSourceType'>
export type ContributionStatus = S<'ContributionStatus'>
export type Summary = S<'ContributionSummary'>

export const REGIMES: readonly { value: Regime; label: string }[] = [
  { value: 'US_USF', label: 'US USF (USAC)' },
  { value: 'CA_CRTC_NCF', label: 'Canada CRTC NCF' },
  { value: 'OTHER', label: 'Other fund' },
]

export const SOURCE_TYPES: readonly { value: SourceType; label: string }[] = [
  { value: 'BSS_INVOICE', label: 'BSS invoice' },
  { value: 'RATED_USAGE', label: 'Rated usage' },
  { value: 'REVENUE_RECOGNITION_ADJUSTMENT', label: 'Revenue-recognition adjustment' },
  { value: 'LICENCE_ACTIVITY', label: 'Licence-activity event' },
  { value: 'MANUAL_CERTIFIED_SCHEDULE', label: 'Manual certified schedule' },
]

export const STATUSES: readonly ContributionStatus[] = ['ACCEPTED', 'QUARANTINED']

export const isRegime = (v: string): v is Regime => REGIMES.some((r) => r.value === v)
export const isSourceType = (v: string): v is SourceType => SOURCE_TYPES.some((s) => s.value === v)
export const isStatus = (v: string): v is ContributionStatus => (STATUSES as readonly string[]).includes(v)

export const reasonOf = (error: unknown): string | null => (error instanceof ZoikoTaxError ? error.reasonCode : null)

/** The one sentence the page repeats wherever totals appear (UAT-OBL-033). */
export const NOT_RECOVERY =
  'A provider contribution is the operator’s own obligation on its eligible revenue. Recovery charged to customers on a surcharge line is a separate receivable and is not the contribution payable.'

/** A period's control totals as CSV, every amount exactly as the cell sent it. */
export function downloadSummary(summary: Summary) {
  downloadCsv(
    `contribution-control-totals-${summary.period}`,
    [
      'period', 'legalEntityId', 'regime', 'currency', 'status', 'incompleteReasons', 'acceptedCount',
      'quarantinedCount', 'reversalCount', 'amendmentCount', 'eligibleTotal', 'excludedTotal', 'reversedTotal',
      'periodDigest', 'sourceChecksums',
    ],
    summary.groups.map((g) => [
      summary.period, g.legalEntityId, g.regime, g.currency, g.status, g.incompleteReasons.join(' '),
      `${g.acceptedCount}`, `${g.quarantinedCount}`, `${g.reversalCount}`, `${g.amendmentCount}`,
      g.eligibleTotal, g.excludedTotal, g.reversedTotal, g.periodDigest, g.sourceChecksums.join(' '),
    ]),
  )
}
