import { Fragment, useMemo, useState } from 'react'

import { Card } from '../../ui/kit'
import { ComponentsTable } from './ComponentsTable'
import { serviceLabel, type Coverage, type Regime } from './coverage'

const REGION = (() => {
  try {
    return new Intl.DisplayNames(['en'], { type: 'region' })
  } catch {
    return null
  }
})()

function countryName(code: string): string {
  return REGION?.of(code) ?? code
}

/**
 * Every country the active content covers. A country that is not listed is
 * UNSUPPORTED on a determination; nothing about it is assumed.
 */
export const CountriesTab = ({ coverage }: { coverage: Coverage }) => {
  const [filter, setFilter] = useState('')
  const [open, setOpen] = useState<string | null>(null)

  const regimes = useMemo(() => {
    const q = filter.trim().toLowerCase()
    return [...coverage.regimes]
      .sort((a, b) => countryName(a.country).localeCompare(countryName(b.country)))
      .filter((r) => q === '' || countryName(r.country).toLowerCase().includes(q) || r.country.toLowerCase() === q
        || r.name.toLowerCase().includes(q))
  }, [coverage, filter])

  return (
    <Card
      title={`Countries covered (${coverage.regimes.length})`}
      aside={
        <label className="ztax-field loc-inline">
          <span className="zp-sr-only">Filter countries</span>
          <input type="search" placeholder="Filter by country" value={filter} onChange={(e) => { setFilter(e.target.value) }} />
        </label>
      }
    >
      <p className="muted zp-intro">
        A place of supply in a country not listed here is UNSUPPORTED: recorded as such and never taxed at zero.
        Where a country needs a state or province, a country-only place is held for review.
      </p>
      <div className="zp-scroll">
        <table className="table">
          <caption className="table__caption">One regime per country. Open a row for its components in force.</caption>
          <thead>
            <tr>
              <th scope="col">Country</th>
              <th scope="col">Regime</th>
              <th scope="col">Needs</th>
              <th scope="col">Services</th>
              <th scope="col">Components</th>
              <th scope="col"><span className="zp-sr-only">Open</span></th>
            </tr>
          </thead>
          <tbody>
            {regimes.map((r) => (
              <Fragment key={r.id}>
                <tr className={open === r.id ? 'loc-row loc-row--selected' : 'loc-row'}>
                  <td>
                    {countryName(r.country)} <span className="mono muted">{r.country}</span>
                  </td>
                  <td className="loc-small">{r.name}</td>
                  <td className="mono loc-small">{r.requiresLevel ?? 'COUNTRY'}</td>
                  <td className="loc-small">{r.services.length}</td>
                  <td className="loc-small">{r.components.length} in {jurisdictionCount(r)} jurisdiction{jurisdictionCount(r) === 1 ? '' : 's'}</td>
                  <td>
                    <button
                      type="button"
                      className="ztax-btn-quiet"
                      aria-expanded={open === r.id}
                      onClick={() => { setOpen(open === r.id ? null : r.id) }}
                    >
                      {open === r.id ? 'Close' : 'Open'}
                    </button>
                  </td>
                </tr>
                {open === r.id ? (
                  <tr className="zp-subrow">
                    <td colSpan={6}>
                      <RegimeDetail regime={r} />
                    </td>
                  </tr>
                ) : null}
              </Fragment>
            ))}
          </tbody>
        </table>
      </div>
    </Card>
  )
}

function jurisdictionCount(r: Regime): number {
  return new Set(r.components.map((c) => c.jurisdiction)).size
}

const RegimeDetail = ({ regime }: { regime: Regime }) => {
  const byJurisdiction = useMemo(() => {
    const m = new Map<string, Regime['components']>()
    for (const c of regime.components) {
      m.set(c.jurisdiction, [...(m.get(c.jurisdiction) ?? []), c])
    }
    return [...m.entries()].sort(([a], [b]) => a.localeCompare(b))
  }, [regime])

  return (
    <div className="loc-regime">
      <p className="loc-small">
        <strong>Services:</strong> {regime.services.map(serviceLabel).join(', ')}.{' '}
        {regime.inclusivePricing ? 'Inclusive prices can be extracted.' : 'Inclusive prices cannot be extracted.'}
      </p>
      {byJurisdiction.map(([j, cs]) => (
        <div key={j} className="loc-jurisdiction">
          <h3 className="ztax-card-title mono">{j.replace(/^jurisdiction:/, '').toUpperCase()}</h3>
          <ComponentsTable components={cs} caption={`Components in force in ${j}.`} />
        </div>
      ))}
    </div>
  )
}
