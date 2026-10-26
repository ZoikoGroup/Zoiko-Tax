/**
 * GET /v1/coverage read as places: every jurisdiction the active content names,
 * with its country, region and the components in force there.
 *
 * Coverage is organised by regime (one per country); an administrator looks
 * for a place. Nothing here computes a fiscal value: rates stay the decimal
 * strings the cell sent, and the one figure derived per place is the exact
 * fixed-point sum in coverage.ts.
 */
import { charge, type Charge, type Component, type Coverage, type Regime, type Service } from './coverage'
import { US_STATES } from './usStates'

export type Region = 'eu' | 'us' | 'ca' | 'row'

export const REGIONS: readonly { readonly value: Region | 'all'; readonly label: string }[] = [
  { value: 'all', label: 'All locations' },
  { value: 'eu', label: 'EU member states' },
  { value: 'us', label: 'United States' },
  { value: 'ca', label: 'Canada' },
  { value: 'row', label: 'Rest of world' },
]

const EU = new Set('AT BE BG CY CZ DE DK EE ES FI FR GR HR HU IE IT LT LU LV MT NL PL PT RO SE SI SK'.split(' '))

const CA_PROVINCES: Readonly<Record<string, string>> = {
  AB: 'Alberta', BC: 'British Columbia', MB: 'Manitoba', NB: 'New Brunswick', NL: 'Newfoundland and Labrador',
  NS: 'Nova Scotia', NT: 'Northwest Territories', NU: 'Nunavut', ON: 'Ontario', PE: 'Prince Edward Island',
  QC: 'Quebec', SK: 'Saskatchewan', YT: 'Yukon',
}

const DISPLAY = (() => {
  try {
    return new Intl.DisplayNames(['en'], { type: 'region' })
  } catch {
    return null
  }
})()

export function countryName(code: string): string {
  return DISPLAY?.of(code) ?? code
}

export function regionOf(country: string): Region {
  return country === 'US' ? 'us' : country === 'CA' ? 'ca' : EU.has(country) ? 'eu' : 'row'
}

export interface Place {
  /** The content's jurisdiction id, `jurisdiction:us-ca`. */
  readonly id: string
  /** ISO 3166: `GB`, or `US-CA` for a subdivision. */
  readonly code: string
  readonly name: string
  readonly country: string
  readonly subdivision: boolean
  readonly region: Region
  readonly regime: Regime
  readonly components: readonly Component[]
}

function subdivisionName(country: string, sub: string): string {
  if (country === 'US') return US_STATES.find((s) => s.code === sub)?.name ?? sub
  if (country === 'CA') return CA_PROVINCES[sub] ?? sub
  return sub
}

/** Every place in the coverage: each country, then each subdivision a component names. */
export function placesOf(coverage: Coverage): Place[] {
  const out: Place[] = []
  for (const regime of coverage.regimes) {
    const cc = regime.country
    const countryId = `jurisdiction:${cc.toLowerCase()}`
    const bySub = new Map<string, Component[]>()
    for (const c of regime.components) {
      if (c.jurisdiction === countryId) continue
      bySub.set(c.jurisdiction, [...(bySub.get(c.jurisdiction) ?? []), c])
    }
    out.push({
      id: countryId, code: cc, name: countryName(cc), country: cc, subdivision: false, region: regionOf(cc), regime,
      components: regime.components.filter((c) => c.jurisdiction === countryId),
    })
    for (const [id, components] of bySub) {
      const code = id.replace(/^jurisdiction:/, '').toUpperCase()
      const sub = code.split('-')[1] ?? code
      out.push({
        id, code, name: subdivisionName(cc, sub), country: cc, subdivision: true, region: regionOf(cc), regime, components,
      })
    }
  }
  return out
}

/** What a place charges for a service, at its own level only. */
export function placeCharge(p: Place, service: Service): Charge {
  return charge(p.regime, p.id, service)
}

/** A component in force at a legal zero: exempt, zero-rated or out of scope. */
export function isLegalZero(c: Component): boolean {
  return c.legalReason !== undefined || /^0+(\.0+)?$/.test(c.rate)
}

/** The tax family a component belongs to, for the summary by type. */
export function family(c: Component): string {
  if (c.taxType === 'NONE') return 'Not taxed'
  if (/^(VAT|GST|HST|PST|QST|RST)$/.test(c.taxType)) return c.taxType
  if (/SALES|USE_TAX/.test(c.taxType)) return 'Sales & use'
  return 'Telecom & other'
}

export type Tone = 'vat' | 'sales' | 'excise' | 'none'

export function tone(c: Component): Tone {
  if (c.taxType === 'NONE') return 'none'
  if (/VAT|GST|HST|PST|QST|RST|CONSUMPTION/.test(c.taxType)) return 'vat'
  if (/SALES|USE_TAX/.test(c.taxType)) return 'sales'
  return 'excise'
}

export function taxTypeLabel(t: string): string {
  return t === 'NONE' ? 'Not taxed' : t.replaceAll('_', ' ')
}
