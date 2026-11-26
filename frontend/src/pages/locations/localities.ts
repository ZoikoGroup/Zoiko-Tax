import type { Coverage } from './coverage'

/**
 * The county and city jurisdictions the content declares inside a subdivision,
 * from its components: a locality's taxes apply only when the evidence names
 * it, on top of the subdivision's.
 */
export function localitiesOf(coverage: Coverage, subdivision: string): { id: string; name: string }[] {
  if (subdivision === '') return []
  const prefix = `jurisdiction:${subdivision.toLowerCase()}-`
  const country = subdivision.slice(0, 2)
  const names = new Map<string, string>()
  for (const c of coverage.regimes.filter((r) => r.country === country).flatMap((r) => r.components)) {
    if (!c.jurisdiction.startsWith(prefix) || names.has(c.jurisdiction)) continue
    const slug = c.jurisdiction.slice(prefix.length)
    // A county's own component names it, keeping capitals such as McDowell.
    const county = slug.endsWith('-county') ? /^(.+? County) /.exec(c.name)?.[1] : undefined
    names.set(c.jurisdiction, county ?? slug.split('-').map((w) => w.charAt(0).toUpperCase() + w.slice(1)).join(' '))
  }
  return [...names].map(([id, name]) => ({ id, name })).sort((a, b) => a.name.localeCompare(b.name))
}
