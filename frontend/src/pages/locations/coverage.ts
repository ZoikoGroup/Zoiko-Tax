/**
 * Reading GET /v1/coverage: which components are in force where, and what
 * they add up to for one service.
 *
 * Rates stay the decimal strings the cell sent (ADR-0019 C1). The one sum
 * this page shows, the state-level rate for a service, is added exactly in
 * fixed point, never in floating point.
 */
import type { S } from '@zoikotax/sdk'

export type Coverage = S<'Coverage'>
export type Regime = S<'CoverageRegime'>
export type Component = S<'CoverageComponent'>
export type Service = S<'ServiceFamily'>

/** The services a person picks from, in the order a telecom bill lists them. */
export const SERVICES: readonly { readonly value: Service; readonly label: string }[] = [
  { value: 'MOBILE_VOICE', label: 'Mobile voice' },
  { value: 'FIXED_VOICE', label: 'Fixed voice' },
  { value: 'VOIP', label: 'VoIP' },
  { value: 'MESSAGING', label: 'Messaging' },
  { value: 'MOBILE_DATA', label: 'Mobile data' },
  { value: 'FIXED_BROADBAND', label: 'Fixed broadband' },
  { value: 'SIP_TRUNKING', label: 'SIP trunking' },
  { value: 'IOT_CONNECTIVITY', label: 'IoT connectivity' },
  { value: 'ROAMING_RETAIL', label: 'Roaming (retail)' },
  { value: 'UCAAS', label: 'UCaaS' },
  { value: 'CCAAS', label: 'CCaaS' },
  { value: 'CPAAS', label: 'CPaaS' },
  { value: 'INSTALLATION_ACTIVATION', label: 'Installation & activation' },
  { value: 'EQUIPMENT_RENTAL', label: 'Equipment rental' },
  { value: 'EQUIPMENT_SALE', label: 'Equipment sale' },
]

export function serviceLabel(value: string): string {
  return SERVICES.find((s) => s.value === value)?.label ?? value.replaceAll('_', ' ')
}

// ---------------------------------------------------------------------------
// exact decimal arithmetic
// ---------------------------------------------------------------------------

const SCALE = 10

/** A non-negative decimal string as an integer scaled by 10^SCALE. */
function scaled(value: string): bigint {
  const [whole = '0', fraction = ''] = value.split('.')
  return BigInt(whole + fraction.padEnd(SCALE, '0').slice(0, SCALE))
}

/** A proportion ("0.0725") as a percentage string ("7.25%"), exactly. */
export function percent(value: string): string {
  return `${percentDigits(scaled(value))}%`
}

function percentDigits(v: bigint): string {
  // v is the proportion × 10^SCALE; the percentage is v × 100 / 10^SCALE.
  const digits = v.toString().padStart(SCALE - 1, '0')
  const whole = digits.slice(0, digits.length - (SCALE - 2)) || '0'
  const fraction = digits.slice(digits.length - (SCALE - 2)).replace(/0+$/, '')
  return fraction === '' ? whole : `${whole}.${fraction}`
}

// ---------------------------------------------------------------------------
// what a jurisdiction charges for a service
// ---------------------------------------------------------------------------

export type Standing =
  /** No component covers this place for this service: a determination is UNSUPPORTED. */
  | 'uncovered'
  /** Covered, and every component in force is a legal zero (exempt, zero-rated, out of scope). */
  | 'exempt'
  /** Covered, with at least one charge in force. */
  | 'taxed'

export interface Charge {
  readonly standing: Standing
  /** The ad valorem customer charges in force, added; "0" when there are none. */
  readonly rate: string
  /** As a fixed-point integer, for sorting and colouring. */
  readonly scaledRate: bigint
  /** Charges that are not a proportion (a flat or per-unit amount). */
  readonly fixed: readonly Component[]
  readonly components: readonly Component[]
}

export function componentsFor(regime: Regime | undefined, jurisdiction: string, service?: Service): Component[] {
  if (regime === undefined) {
    return []
  }
  return regime.components.filter(
    (c) => c.jurisdiction === jurisdiction && (service === undefined || (c.services ?? regime.services).includes(service)),
  )
}

export function charge(regime: Regime | undefined, jurisdiction: string, service: Service): Charge {
  const components = componentsFor(regime, jurisdiction, service)
  let total = 0n
  const fixed: Component[] = []
  let anyCharge = false
  for (const c of components) {
    if (!c.treatments.includes('CUSTOMER_CHARGE')) {
      continue
    }
    if (c.rateKind === 'AD_VALOREM') {
      const r = scaled(c.rate)
      total += r
      anyCharge ||= r > 0n
    } else {
      fixed.push(c)
      anyCharge ||= !/^0(\.0+)?$/.test(c.rate)
    }
  }
  const standing: Standing = components.length === 0 ? 'uncovered' : anyCharge ? 'taxed' : 'exempt'
  return { standing, rate: fromScaled(total), scaledRate: total, fixed, components }
}

function fromScaled(v: bigint): string {
  const digits = v.toString().padStart(SCALE + 1, '0')
  const whole = digits.slice(0, digits.length - SCALE)
  const fraction = digits.slice(digits.length - SCALE).replace(/0+$/, '')
  return fraction === '' ? whole : `${whole}.${fraction}`
}

/** The colour band a summed rate falls in, for the map's legend. */
export type Band = 'uncovered' | 'exempt' | 'low' | 'mid' | 'high' | 'top'

export const BANDS: readonly { readonly band: Band; readonly label: string }[] = [
  { band: 'uncovered', label: 'Not covered' },
  { band: 'exempt', label: 'Exempt (0%)' },
  { band: 'low', label: 'Under 5%' },
  { band: 'mid', label: '5% to under 7%' },
  { band: 'high', label: '7% to under 10%' },
  { band: 'top', label: '10% and over' },
]

export function band(c: Charge): Band {
  if (c.standing !== 'taxed') {
    return c.standing
  }
  const pct = (c.scaledRate * 100n) / 10n ** BigInt(SCALE)
  if (pct < 5n) return 'low'
  if (pct < 7n) return 'mid'
  if (pct < 10n) return 'high'
  return 'top'
}

/** "YYYY-MM-DD" for today, in the browser's own calendar. */
export function today(): string {
  const d = new Date()
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`
}

/** A civil date as the instant the coverage is read at: noon UTC that day. */
export function coverageInstant(date: string): string {
  return `${date}T12:00:00.000000Z`
}
