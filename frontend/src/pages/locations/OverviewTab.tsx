import { useMemo, useState } from 'react'

import { civilDate } from '../../dashboard/format'
import { Citation, RateText } from './ComponentsTable'
import { percent, serviceLabel, SERVICES, type Component, type Coverage, type Service } from './coverage'
import {
  family, isLegalZero, placeCharge, placesOf, REGIONS, taxTypeLabel, tone, type Place, type Region,
} from './places'

const TREATMENT: Readonly<Record<string, readonly [string, string]>> = {
  CUSTOMER_CHARGE: ['Charge customer', 'lo-t--charge'],
  REVERSE_CHARGE: ['Reverse charge', 'lo-t--reverse'],
  PROVIDER_COST: ['Provider cost', 'lo-t--cost'],
  EXEMPT: ['Exempt', 'lo-t--quiet'],
}

const Treatment = ({ value }: { value: string }) => {
  const [label, cls] = TREATMENT[value] ?? [value.replaceAll('_', ' ').toLowerCase(), 'lo-t--quiet']
  return <span className={`lo-pill ${cls}`}>{label}</span>
}

const GROUPS: readonly { readonly region: Region; readonly label: string }[] = [
  { region: 'eu', label: 'EU member states' },
  { region: 'row', label: 'Rest of world' },
  { region: 'ca', label: 'Canada' },
  { region: 'us', label: 'United States' },
]

export interface OverviewProps {
  readonly coverage: Coverage
  /** Open the quote form at this place for this service. */
  readonly onTry: (country: string, subdivision: string, service: Service) => void
}

/**
 * The administrator's view of location taxation: what the active content
 * covers, place by place, and what each place charges, with the rule and
 * citation behind every rate. Read-only; a change is made in the content pack.
 */
export const OverviewTab = ({ coverage, onTry }: OverviewProps) => {
  const places = useMemo(() => placesOf(coverage), [coverage])
  const byId = useMemo(() => new Map(places.map((p) => [p.id, p])), [places])
  const all = useMemo(() => coverage.regimes.flatMap((r) => r.components), [coverage])

  const [region, setRegion] = useState<Region | 'all'>('all')
  const [query, setQuery] = useState('')
  const [service, setService] = useState<Service>('MOBILE_VOICE')
  const [selected, setSelected] = useState<string>(() => (places.find((p) => p.code === 'GB') ?? places[0])?.id ?? '')
  const [open, setOpen] = useState<string | null>(null)

  const visible = useMemo(() => {
    const q = query.trim().toLowerCase()
    return places.filter((p) => (region === 'all' || p.region === region)
      && (q === '' || p.name.toLowerCase().includes(q) || p.code.toLowerCase().includes(q)
        || p.components.some((c) => c.taxType.toLowerCase().includes(q))))
  }, [places, region, query])

  const place = byId.get(selected)
  const missing = places.filter((p) => !p.subdivision && p.components.length === 0)
  const subdivisions = places.filter((p) => p.subdivision).length
  const zeros = all.filter(isLegalZero).length
  const uncovered = places.filter((p) => (p.subdivision || p.regime.requiresLevel === undefined)
    && placeCharge(p, service).standing === 'uncovered')

  const families = useMemo(() => {
    const m = new Map<string, number>()
    for (const c of all) m.set(family(c), (m.get(family(c)) ?? 0) + 1)
    return [...m.entries()].sort((a, b) => b[1] - a[1])
  }, [all])
  const widest = families[0]?.[1] ?? 1

  const pick = (id: string) => { setSelected(id); setOpen(null) }
  const chooseRegion = (r: Region | 'all') => {
    setRegion(r)
    const first = places.find((p) => r === 'all' || p.region === r)
    if (first !== undefined && place?.region !== r && r !== 'all') pick(first.id)
  }

  return (
    <div className="lo">
      <div className="lo-main">
        <section className="lo-card lo-stats" aria-label="Coverage summary">
          <Stat k="Jurisdictions" v={places.length} d={`${coverage.regimes.length} countries · ${subdivisions} states and provinces`} />
          <Stat k="Tax components" v={all.length} d={`in force on ${civilDate(coverage.at.slice(0, 10))}`} />
          <Stat k="Exempt or out of scope" v={zeros} d="each recorded with a legal reason" />
          <Stat k={`Not covered for ${serviceLabel(service).toLowerCase()}`} v={uncovered.length} d="a determination there is UNSUPPORTED" />
        </section>

        <section className="lo-card">
          <header className="lo-card-h">
            <div>
              <h2>Jurisdictions</h2>
              <p>Choose a market to review its taxes, rates and the rule behind each one.</p>
            </div>
            <label className="lo-select">
              <span>Rates for</span>
              <select value={service} onChange={(e) => { setService(e.target.value as Service) }}>
                {SERVICES.map((s) => <option key={s.value} value={s.value}>{s.label}</option>)}
              </select>
            </label>
          </header>
          <div className="lo-chips" role="group" aria-label="Region">
            {REGIONS.map((r) => (
              <button
                key={r.value}
                type="button"
                className="lo-chip"
                aria-pressed={region === r.value}
                onClick={() => { chooseRegion(r.value) }}
              >
                {r.label}
                <span className="lo-chip-n">{r.value === 'all' ? places.length : places.filter((p) => p.region === r.value).length}</span>
              </button>
            ))}
          </div>

          <div className="lo-explorer">
            <div className="lo-list-col">
              <label className="lo-filter">
                <span className="zp-sr-only">Filter locations</span>
                <input type="search" placeholder="Filter by place, code or tax type" value={query} onChange={(e) => { setQuery(e.target.value) }} />
              </label>
              <div className="lo-list">
                {GROUPS.map((g) => {
                  const items = visible.filter((p) => p.region === g.region)
                    .sort((a, b) => (a.subdivision ? 1 : 0) - (b.subdivision ? 1 : 0) || a.name.localeCompare(b.name))
                  if (items.length === 0) return null
                  return (
                    <div key={g.region}>
                      <div className="lo-list-head">{g.label}</div>
                      {items.map((p) => (
                        <button
                          key={p.id}
                          type="button"
                          className={`lo-row${p.subdivision ? ' lo-row--sub' : ''}${p.id === selected ? ' lo-row--on' : ''}`}
                          aria-current={p.id === selected ? 'true' : undefined}
                          onClick={() => { pick(p.id) }}
                        >
                          <span className="lo-code">{p.subdivision ? p.code.split('-')[1] : p.code}</span>
                          <span className="lo-row-name">{p.name}</span>
                          <RowRate place={p} service={service} />
                        </button>
                      ))}
                    </div>
                  )
                })}
                {visible.length === 0 ? <p className="lo-empty">No location matches that filter.</p> : null}
              </div>
            </div>

            <div className="lo-detail">
              {place === undefined ? <p className="lo-empty">Select a location.</p> : (
                <PlaceDetail
                  place={place}
                  subs={places.filter((p) => p.subdivision && p.country === place.country)}
                  service={service}
                  open={open}
                  onOpen={setOpen}
                  onPick={pick}
                  onTry={() => { onTry(place.country, place.subdivision ? place.code : '', service) }}
                />
              )}
            </div>
          </div>
        </section>
      </div>

      <aside className="lo-side">
        <section className="lo-card lo-pad">
          <h2>Content in force</h2>
          <p className="lo-lead">Every rate on this page is read from the cell's active bundle.</p>
          <dl className="lo-kv">
            <dt>Bundle</dt><dd>{coverage.bundle.bundleId}</dd>
            <dt>Profile</dt><dd>{coverage.profileVersion}</dd>
            <dt>Graph</dt><dd>{coverage.graphVersion}</dd>
            <dt>Digest</dt><dd title={coverage.bundle.digest}>{coverage.bundle.digest.slice(0, 18)}…</dd>
            <dt>Read at</dt><dd>{coverage.at.slice(0, 16).replace('T', ' ')} UTC</dd>
          </dl>
        </section>

        <section className="lo-card lo-pad">
          <h2>Needs attention</h2>
          <p className="lo-lead">Where a determination will stop for review instead of taxing.</p>
          <div className="lo-attn">
            {missing.map((p) => (
              <button key={p.id} type="button" onClick={() => { setRegion('all'); pick(p.id) }}>
                <i className="lo-sev lo-sev--warn" />
                <span>
                  <b>{p.name} needs a {p.regime.requiresLevel?.toLowerCase() ?? 'subdivision'}</b>
                  <span>No country-level component. A place of supply naming only the country is held for review.</span>
                </span>
              </button>
            ))}
            {uncovered.slice(0, 4).map((p) => (
              <button key={p.id} type="button" onClick={() => { setRegion('all'); pick(p.id) }}>
                <i className="lo-sev lo-sev--crit" />
                <span>
                  <b>{p.name}: {serviceLabel(service)} not covered</b>
                  <span>No component in force for this service. A determination is UNSUPPORTED.</span>
                </span>
              </button>
            ))}
            {uncovered.length > 4 ? <p className="lo-more">and {uncovered.length - 4} more places for this service</p> : null}
            {missing.length === 0 && uncovered.length === 0 ? (
              <div className="lo-attn-ok"><i className="lo-sev lo-sev--ok" /><span>Every place covers {serviceLabel(service).toLowerCase()}.</span></div>
            ) : null}
          </div>
        </section>

        <section className="lo-card lo-pad">
          <h2>Components by tax type</h2>
          <div className="lo-bars">
            {families.map(([k, n]) => (
              <div key={k} className="lo-bar">
                <span>{k}</span>
                <span className="lo-bar-track"><i style={{ width: `${Math.max(4, Math.round((n * 100) / widest))}%` }} /></span>
                <span className="lo-bar-n">{n}</span>
              </div>
            ))}
          </div>
        </section>
      </aside>
    </div>
  )
}

const Stat = ({ k, v, d }: { k: string; v: number; d: string }) => (
  <div className="lo-stat">
    <div className="lo-stat-k">{k}</div>
    <div className="lo-stat-v">{v}</div>
    <div className="lo-stat-d">{d}</div>
  </div>
)

const RowRate = ({ place, service }: { place: Place; service: Service }) => {
  if (!place.subdivision && place.components.length === 0) {
    return <span className="lo-row-rate lo-muted">by {place.regime.requiresLevel?.toLowerCase() ?? 'state'}</span>
  }
  const c = placeCharge(place, service)
  if (c.standing === 'uncovered') return <span className="lo-row-rate lo-muted">—</span>
  return <span className="lo-row-rate">{percent(c.rate)}</span>
}

interface DetailProps {
  readonly place: Place
  readonly subs: readonly Place[]
  readonly service: Service
  readonly open: string | null
  readonly onOpen: (id: string | null) => void
  readonly onPick: (id: string) => void
  readonly onTry: () => void
}

const PlaceDetail = ({ place, subs, service, open, onOpen, onPick, onTry }: DetailProps) => {
  const authorities = [...new Set(place.components.map((c) => c.authority.replace(/^authority:/, '')))]
  const c = placeCharge(place, service)
  const opened = place.components.find((x) => x.id === open)

  return (
    <>
      <div className="lo-dhead">
        <span className="lo-code lo-code--lg">{place.code}</span>
        <div className="lo-dtitle">
          <h3>{place.name}</h3>
          <div className="lo-meta">
            <span>Level <b>{place.subdivision ? 'State / province' : 'Country'}</b></span>
            <span>Regime <b>{place.regime.name}</b></span>
            {authorities.length > 0 ? <span>Authority <b>{authorities.join(', ')}</b></span> : null}
            <span>Inclusive prices <b>{place.regime.inclusivePricing ? 'can be extracted' : 'cannot be extracted'}</b></span>
          </div>
        </div>
        <button type="button" className="lo-btn lo-btn--primary" onClick={onTry}>Quote here</button>
      </div>

      {place.components.length > 0 ? (
        <div className={`lo-charge lo-charge--${c.standing}`}>
          <span className="lo-charge-k">{serviceLabel(service)}</span>
          <span className="lo-charge-v">
            {c.standing === 'uncovered' ? 'Not covered' : c.standing === 'exempt' ? 'Exempt' : percent(c.rate)}
          </span>
          <span className="lo-charge-d">
            {c.standing === 'uncovered'
              ? 'No component in force here covers this service.'
              : `Ad valorem customer charges at this level, added exactly. ${c.components.length} component${c.components.length === 1 ? '' : 's'} apply.`}
          </span>
        </div>
      ) : null}

      {place.components.length === 0 ? (
        <div className="lo-empty">
          <p>No components at country level. {place.name} taxes by {place.regime.requiresLevel?.toLowerCase() ?? 'subdivision'}; pick one to see its rules.</p>
          <div className="lo-subgrid">
            {subs.slice().sort((a, b) => a.name.localeCompare(b.name)).map((s) => (
              <button key={s.id} type="button" className="lo-chip" onClick={() => { onPick(s.id) }}>
                {s.name}<span className="lo-chip-n">{percent(placeCharge(s, service).rate)}</span>
              </button>
            ))}
          </div>
        </div>
      ) : (
        <div className="lo-table-wrap">
          <table className="lo-table">
            <caption className="zp-sr-only">Components in force in {place.name}. Select one for its rule and citation.</caption>
            <thead>
              <tr>
                <th scope="col">Tax type</th>
                <th scope="col">Component</th>
                <th scope="col">Treatment</th>
                <th scope="col">Scope</th>
                <th scope="col">In force from</th>
                <th scope="col" className="lo-num">Rate</th>
              </tr>
            </thead>
            <tbody>
              {place.components.map((x) => (
                <tr key={x.id} className={x.id === open ? 'lo-tr--on' : undefined}>
                  <td><span className={`lo-pill lo-type--${tone(x)}`}>{taxTypeLabel(x.taxType)}</span></td>
                  <td>
                    <button
                      type="button"
                      className="lo-linkish"
                      aria-expanded={x.id === open}
                      onClick={() => { onOpen(x.id === open ? null : x.id) }}
                    >
                      {x.name}
                    </button>
                    <div className="lo-id">{x.id}</div>
                  </td>
                  <td><span className="lo-pills">{x.treatments.map((t) => <Treatment key={t} value={t} />)}</span></td>
                  <td className="lo-nowrap">{x.services === undefined ? 'Whole regime' : `${x.services.length} services`}</td>
                  <td className="lo-nowrap lo-mono">{civilDate(x.from)}{x.to === undefined ? null : <div className="lo-muted">until {civilDate(x.to)}</div>}</td>
                  <td className="lo-num">
                    {x.legalReason !== undefined && isLegalZero(x)
                      ? <span className="lo-pill lo-type--none">{x.legalReason.replaceAll('_', ' ').toLowerCase()}</span>
                      : <RateText c={x} />}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {opened === undefined
        ? (place.components.length > 0 ? <p className="lo-hint">Select a component to see its services, record and legal citation.</p> : null)
        : <ComponentSheet c={opened} services={opened.services ?? place.regime.services} />}
    </>
  )
}

const ComponentSheet = ({ c, services }: { c: Component; services: readonly Service[] }) => (
  <div className="lo-sheet">
    <div>
      <h4>Record</h4>
      <dl className="lo-kv">
        <dt>Component</dt><dd>{c.id}</dd>
        <dt>Authority</dt><dd>{c.authority.replace(/^authority:/, '')}</dd>
        <dt>Rate kind</dt><dd>{c.rateKind.replaceAll('_', ' ').toLowerCase()}</dd>
        <dt>Source</dt><dd>{c.source}</dd>
        {c.legalReason === undefined ? null : <><dt>Legal reason</dt><dd>{c.legalReason}</dd></>}
      </dl>
    </div>
    <div>
      <h4>Treatments</h4>
      <div className="lo-pills">{c.treatments.map((t) => <Treatment key={t} value={t} />)}</div>
      <h4 className="lo-h4-gap">Services in scope</h4>
      <div className="lo-svc">{services.map((s) => <span key={s}>{serviceLabel(s)}</span>)}</div>
    </div>
    <div className="lo-sheet-full">
      <h4>Legal citation</h4>
      <Citation text={c.citation} />
    </div>
  </div>
)
