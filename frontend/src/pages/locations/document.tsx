/**
 * One telecom line at a place a person picks, as the fields they fill in and
 * the TaxDocument it becomes, and the location-aware determination the engine
 * returns for it. Try a location quotes with these; Determine quotes and
 * commits with them.
 */
import type { S } from '@zoikotax/sdk'

import { civilDate } from '../../dashboard/format'
import { Amount, Card, Field, Raw, Status } from '../../ui/kit'
import { isDecimal, KV } from '../determine/shared'
import { Citation } from './ComponentsTable'
import { coverageInstant, percent, serviceLabel, SERVICES, today, type Coverage, type Service } from './coverage'
import { US_STATES } from './usStates'
import '../locations.css'

type Determination = S<'DocumentDetermination'>
type EvidenceType = S<'SitusEvidenceType'>

const EVIDENCE: readonly { value: EvidenceType; label: string }[] = [
  { value: 'PRIMARY_PLACE_OF_USE', label: 'Place of primary use (wireless)' },
  { value: 'SERVICE_ADDRESS', label: 'Service address' },
  { value: 'FIXED_LINE_LOCATION', label: 'Fixed line location' },
  { value: 'BILLING_ADDRESS', label: 'Billing address' },
  { value: 'SIM_COUNTRY', label: 'SIM country' },
  { value: 'CUSTOMER_DECLARATION', label: 'Customer declaration' },
  { value: 'IP_GEOLOCATION', label: 'IP geolocation' },
]

/** The currency a country's invoices are usually in; editable on the form. */
export const CURRENCY: Record<string, string> = {
  US: 'USD', CA: 'CAD', GB: 'GBP', CH: 'CHF', NO: 'NOK', SE: 'SEK', DK: 'DKK', PL: 'PLN', CZ: 'CZK', HU: 'HUF',
  RO: 'RON', BG: 'BGN', AU: 'AUD', NZ: 'NZD', SG: 'SGD', JP: 'JPY', ZA: 'ZAR', AE: 'AED', SA: 'SAR',
}

const REGION = (() => {
  try {
    return new Intl.DisplayNames(['en'], { type: 'region' })
  } catch {
    return null
  }
})()

export interface LocationDraft {
  sellerCountry: string
  sellerSubdivision: string
  country: string
  subdivision: string
  evidence: EvidenceType
  business: boolean
  service: Service
  amount: string
  currency: string
  priceMode: S<'PriceMode'>
  date: string
}

export function emptyLocation(): LocationDraft {
  return {
    sellerCountry: 'US',
    sellerSubdivision: '',
    country: 'US',
    subdivision: 'US-CA',
    evidence: 'PRIMARY_PLACE_OF_USE',
    business: false,
    service: 'MOBILE_VOICE',
    amount: '100.00',
    currency: 'USD',
    priceMode: 'EXCLUSIVE',
    date: today(),
  }
}

/** What is wrong with a draft, or null. */
export function locationProblem(draft: LocationDraft): string | null {
  return !isDecimal(draft.amount) ? 'The amount must be a decimal such as 100.00.'
    : !/^[A-Z]{3}$/.test(draft.currency) ? 'The currency must be a three-letter ISO code.'
      : !/^[A-Z]{2}$/.test(draft.country) ? 'Pick the country where the service is used.'
        : !/^\d{4}-\d{2}-\d{2}$/.test(draft.date) ? 'Pick the tax point date.'
          : null
}

/** The tax point a draft is determined at. */
export function locationEventTime(draft: LocationDraft): string {
  return coverageInstant(draft.date)
}

/** The document a draft becomes. Call only once locationProblem(draft) is null. */
export function locationDocument(draft: LocationDraft): S<'TaxDocument'> {
  const where = place(draft.country, draft.subdivision)
  // The engine establishes a business customer only from a validated VAT
  // registration, which the US does not have.
  const customer: S<'TaxParty'> = draft.business && draft.country !== 'US'
    ? { id: 'CUSTOMER', establishment: where, vat: { country: draft.country, validation: 'VALIDATED' } }
    : { id: 'CUSTOMER' }
  return {
    type: 'INVOICE',
    currency: draft.currency,
    priceMode: draft.priceMode,
    seller: { id: 'SELLER', establishment: place(draft.sellerCountry, draft.sellerSubdivision) },
    customer,
    evidence: [{ type: draft.evidence, source: 'SELLER_RECORDED', place: where }],
    lines: [{ key: 'L1', service: draft.service, amount: draft.amount }],
  }
}

/** The countries the content covers, by name. */
export function countriesOf(coverage: Coverage): string[] {
  return coverage.regimes.map((r) => r.country).sort((a, b) => (REGION?.of(a) ?? a).localeCompare(REGION?.of(b) ?? b))
}

/** The subdivisions the content declares for a country, from its components. */
function subdivisionsOf(coverage: Coverage, country: string): { code: string; name: string }[] {
  if (country === 'US') {
    return US_STATES.map((s) => ({ code: `US-${s.code}`, name: s.name })).sort((a, b) => a.name.localeCompare(b.name))
  }
  const regime = coverage.regimes.find((r) => r.country === country)
  const codes = new Set<string>()
  for (const c of regime?.components ?? []) {
    const id = c.jurisdiction.replace(/^jurisdiction:/, '')
    if (id.includes('-')) codes.add(id.toUpperCase())
  }
  return [...codes].sort().map((code) => ({ code, name: code }))
}

function place(country: string, subdivision: string): S<'Place'> {
  return subdivision === '' ? { country } : { subdivision }
}

/** The seller, the place of use and the line, as three fieldsets. */
export const LocationFields = ({ coverage, draft, onChange }: {
  coverage: Coverage
  draft: LocationDraft
  onChange: (draft: LocationDraft) => void
}) => {
  const countries = countriesOf(coverage)
  const subdivisions = subdivisionsOf(coverage, draft.country)
  const sellerSubdivisions = subdivisionsOf(coverage, draft.sellerCountry)
  const set = <K extends keyof LocationDraft>(k: K, v: LocationDraft[K]) => { onChange({ ...draft, [k]: v }) }
  return (
    <div className="loc-form-grid">
      <fieldset className="loc-fieldset">
        <legend>Seller</legend>
        <Field label="Established in">
          <select
            value={draft.sellerCountry}
            onChange={(e) => { onChange({ ...draft, sellerCountry: e.target.value, sellerSubdivision: '' }) }}
          >
            {countries.map((c) => <option key={c} value={c}>{REGION?.of(c) ?? c}</option>)}
          </select>
        </Field>
        {sellerSubdivisions.length === 0 ? null : (
          <Field label="State or province" help="Optional for the seller">
            <select value={draft.sellerSubdivision} onChange={(e) => { set('sellerSubdivision', e.target.value) }}>
              <option value="">—</option>
              {sellerSubdivisions.map((s) => <option key={s.code} value={s.code}>{s.name}</option>)}
            </select>
          </Field>
        )}
      </fieldset>

      <fieldset className="loc-fieldset">
        <legend>Where the service is used</legend>
        <Field label="Country">
          <select
            value={draft.country}
            onChange={(e) => {
              const c = e.target.value
              onChange({ ...draft, country: c, subdivision: '', currency: CURRENCY[c] ?? (draft.currency === 'USD' ? 'EUR' : draft.currency) })
            }}
          >
            {countries.map((c) => <option key={c} value={c}>{REGION?.of(c) ?? c}</option>)}
          </select>
        </Field>
        {subdivisions.length === 0 ? null : (
          <Field label="State or province" help="Leave empty to see what a country-only place does">
            <select value={draft.subdivision} onChange={(e) => { set('subdivision', e.target.value) }}>
              <option value="">— country only —</option>
              {subdivisions.map((s) => <option key={s.code} value={s.code}>{s.name}</option>)}
            </select>
          </Field>
        )}
        <Field label="Evidence of the place">
          <select value={draft.evidence} onChange={(e) => { set('evidence', e.target.value as EvidenceType) }}>
            {EVIDENCE.map((x) => <option key={x.value} value={x.value}>{x.label}</option>)}
          </select>
        </Field>
        <label className="ztax-field ztax-check" title={draft.country === 'US' ? 'The US has no VAT; US state taxes apply to business and consumer customers alike' : undefined}>
          <input
            type="checkbox"
            checked={draft.business && draft.country !== 'US'}
            disabled={draft.country === 'US'}
            onChange={(e) => { set('business', e.target.checked) }}
          />
          <span>Business customer with a validated VAT registration there</span>
        </label>
      </fieldset>

      <fieldset className="loc-fieldset">
        <legend>Line</legend>
        <Field label="Service">
          <select value={draft.service} onChange={(e) => { set('service', e.target.value as Service) }}>
            {SERVICES.map((s) => <option key={s.value} value={s.value}>{s.label}</option>)}
          </select>
        </Field>
        <Field label="Amount">
          <input inputMode="decimal" value={draft.amount} onChange={(e) => { set('amount', e.target.value.trim()) }} />
        </Field>
        <Field label="Currency">
          <input value={draft.currency} maxLength={3} onChange={(e) => { set('currency', e.target.value.toUpperCase()) }} />
        </Field>
        <Field label="Prices">
          <select value={draft.priceMode} onChange={(e) => { set('priceMode', e.target.value as S<'PriceMode'>) }}>
            <option value="EXCLUSIVE">Exclusive of tax</option>
            <option value="INCLUSIVE">Inclusive of tax</option>
          </select>
        </Field>
        <Field label="Tax point">
          <input type="date" value={draft.date} onChange={(e) => { set('date', e.target.value) }} />
        </Field>
      </fieldset>
    </div>
  )
}

/** Where the engine placed each line, under which regime, and every component it applied. */
export const DeterminationCard = ({ determination: d, title = 'What the engine decided' }: {
  determination: Determination
  title?: string
}) => (
  <Card title={title} aside={<Status value={d.state} />}>
    {d.state === 'RESOLVED' ? null : (
      <p className="zp-warn-note" role="note">
        <strong>{d.state.replaceAll('_', ' ')}</strong> ({d.reasonCode}). This is a recorded outcome, never a zero tax.
      </p>
    )}
    {d.totals === undefined ? null : (
      <KV
        rows={[
          ['Net', <Amount key="n" amount={d.totals.net} currency={d.currency} />],
          ['Tax charged to customer', <Amount key="t" amount={d.totals.customerTax} currency={d.currency} />],
          ['Customer total', <Amount key="c" amount={d.totals.customerTotal} currency={d.currency} />],
          ['Provider accrual', <Amount key="p" amount={d.totals.providerAccrual} currency={d.currency} />],
          ['Reverse charge (customer self-assesses)', <Amount key="r" amount={d.totals.reverseCharge} currency={d.currency} />],
        ]}
      />
    )}
    {d.items.map((it) => (
      <div key={it.key} className="loc-item">
        <h3 className="ztax-card-title">
          Line {it.key} · {it.service === undefined ? '—' : serviceLabel(it.service)} · <Status value={it.state} />
        </h3>
        <KV
          rows={[
            ['Place of supply', <span key="p" className="mono">{it.placeOfSupply ?? '—'}</span>],
            ['Regime', <span key="r" className="mono">{it.regime ?? '—'}</span>],
            ['Customer', <span key="c" className="mono">{it.customerRole ?? '—'}</span>],
            ['Supply', <span key="s" className="mono">{it.supply ?? '—'}</span>],
            ...(it.reasonCode === undefined ? [] : [['Reason', <span key="x" className="mono">{it.reasonCode}</span>] as const]),
            ...(it.neededEvidence === undefined || it.neededEvidence.length === 0 ? []
              : [['Needs evidence', <span key="e" className="mono">{it.neededEvidence.join(', ')}</span>] as const]),
          ]}
        />
        {it.components === undefined || it.components.length === 0 ? null : (
          <div className="zp-scroll">
            <table className="table">
              <caption className="table__caption">Components applied to this line.</caption>
              <thead>
                <tr>
                  <th scope="col">Component</th>
                  <th scope="col">Treatment</th>
                  <th scope="col">Rate</th>
                  <th scope="col">Base</th>
                  <th scope="col">Amount</th>
                  <th scope="col">Charged</th>
                  <th scope="col">Source</th>
                </tr>
              </thead>
              <tbody>
                {it.components.map((c) => (
                  <tr key={c.id}>
                    <td>
                      <div>{c.name}</div>
                      <div className="mono muted loc-small">{c.id} · since {civilDate(c.version)}</div>
                    </td>
                    <td>
                      <Status value={c.treatment} />
                      {c.legalReason === undefined ? null : <div className="mono loc-small">{c.legalReason}</div>}
                    </td>
                    <td className="mono">{c.rateKind === 'AD_VALOREM' ? percent(c.rate) : c.rate}</td>
                    <td><Amount amount={c.base} currency={d.currency} /></td>
                    <td><Amount amount={c.amount} currency={d.currency} /></td>
                    <td><Amount amount={c.charged} currency={d.currency} /></td>
                    <td className="loc-small">
                      <details className="loc-cite">
                        <summary className="mono">{c.source}</summary>
                        <Citation text={c.citation} />
                      </details>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    ))}
    <details className="ztax-raw">
      <summary>Trace ({d.trace.length} steps)</summary>
      <ol className="zp-list loc-trace">
        {d.trace.map((s, i) => (
          <li key={i}><span className="mono">{[s.item, s.component, s.step].filter(Boolean).join(' · ')}</span> — {s.detail}</li>
        ))}
      </ol>
    </details>
    <Raw value={d} />
  </Card>
)
