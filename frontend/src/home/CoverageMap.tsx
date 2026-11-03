import { geoNaturalEarth1, geoPath } from 'd3-geo'
import { feature } from 'topojson-client'
import world from 'world-atlas/countries-110m.json'

import type { DashboardMarket } from '@zoikotax/sdk'

import { countryName } from '../pages/locations/places'
import { ISO_NUMERIC } from './isoNumeric'
import { STATUS_LABEL } from './status'

const WIDTH = 960
const HEIGHT = 470

/**
 * The country outlines, projected once. Antarctica is left out: no telecom
 * tax is determined there and it would take a fifth of the height.
 */
const SHAPES = (() => {
  const countries = feature(world, world.objects.countries)
  const features = countries.features.filter((f) => f.id !== '010')
  const projection = geoNaturalEarth1().fitSize([WIDTH, HEIGHT], { type: 'FeatureCollection', features })
  const path = geoPath(projection)
  // A few shapes (Kosovo, Northern Cyprus, Somaliland) carry no ISO code; they
  // key by name and are never a reported market.
  return features.map((f) => ({ id: f.id === undefined ? '' : String(f.id), key: String(f.id ?? f.properties.name), name: f.properties.name, d: path(f) ?? '' }))
})()

/**
 * Markets by state on a world map. Colour is never the only carrier: each
 * country names its state in a tooltip, and the table beside the map lists
 * every market the coverage reports.
 */
const CoverageMap = ({ markets }: { markets: readonly DashboardMarket[] }) => {
  const byNumeric = new Map<string, DashboardMarket>()
  for (const m of markets) {
    const n = ISO_NUMERIC[m.country]
    if (n !== undefined) byNumeric.set(n, m)
  }
  return (
    <svg
      className="zh-map"
      viewBox={`0 0 ${WIDTH} ${HEIGHT}`}
      role="img"
      aria-label={`World map of ${markets.length} markets by coverage state; the table lists each one.`}
    >
      {SHAPES.map((s) => {
        const m = s.id === '' ? undefined : byNumeric.get(s.id)
        const status = m?.status ?? 'NONE'
        return (
          <path key={s.key} d={s.d} className={`zh-map-country zh-map--${status.toLowerCase()}`}>
            <title>{`${m === undefined ? s.name : (m.name ?? countryName(m.country))}: ${STATUS_LABEL[status]}`}</title>
          </path>
        )
      })}
    </svg>
  )
}

// Default export for React.lazy: the geometry is a chunk of its own, loaded
// with Home rather than with every page.
export default CoverageMap
