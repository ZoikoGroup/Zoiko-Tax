import { useMemo, useState } from 'react'

import { Card, Empty } from '../../ui/kit'
import { ComponentsTable } from './ComponentsTable'
import {
  BANDS, band, charge, componentsFor, percent, serviceLabel, SERVICES,
  type Charge, type Component, type Coverage, type Service,
} from './coverage'
import { stateJurisdiction, US_STATES, type UsState } from './usStates'

const STANDING_TEXT: Record<Charge['standing'], string> = {
  uncovered: 'Not covered — a determination here is UNSUPPORTED',
  exempt: 'Covered — exempt at the state level',
  taxed: 'Covered — taxed at the state level',
}

type Sort = 'name' | 'rate'

/**
 * The fifty states and DC for one service: what the active content charges
 * at the state level, where it is silent, and the citation for each figure.
 */
export const UsStatesTab = ({ coverage, onTry }: {
  coverage: Coverage
  onTry: (state: string, service: Service) => void
}) => {
  const regime = coverage.regimes.find((r) => r.country === 'US')
  const services = SERVICES.filter((s) => regime?.services.includes(s.value) ?? false)
  const [service, setService] = useState<Service>('MOBILE_VOICE')
  const [selected, setSelected] = useState<string>('CA')
  const [sort, setSort] = useState<Sort>('name')

  const rows = useMemo(
    () => US_STATES.map((s) => ({ state: s, charge: charge(regime, stateJurisdiction(s.code), service) })),
    [regime, service],
  )
  const sorted = useMemo(() => {
    const copy = [...rows]
    if (sort === 'rate') {
      copy.sort((a, b) => (a.charge.scaledRate === b.charge.scaledRate
        ? a.state.name.localeCompare(b.state.name)
        : a.charge.scaledRate > b.charge.scaledRate ? -1 : 1))
    } else {
      copy.sort((a, b) => a.state.name.localeCompare(b.state.name))
    }
    return copy
  }, [rows, sort])

  if (regime === undefined) {
    return (
      <Card title="United States">
        <Empty>
          The active content does not cover the United States, so every US place of supply is UNSUPPORTED. Load the
          global-telecom pack, which models the fifty states and DC.
        </Empty>
      </Card>
    )
  }

  const covered = rows.filter((r) => r.charge.standing !== 'uncovered').length
  const taxed = rows.filter((r) => r.charge.standing === 'taxed').length
  const current = rows.find((r) => r.state.code === selected)

  return (
    <>
      <Card title="United States by state">
        <p className="muted zp-intro">
          {regime.name}. A US place of supply must resolve to a state ({regime.requiresLevel ?? 'STATE'} level); a
          country-only US address is held for review, never taxed at zero. State-level taxes only — county, city and
          special-district taxes, 911 fees and the federal USF are not modelled.
        </p>

        <div className="loc-toolbar">
          <label className="ztax-field loc-inline">
            <span>Service</span>
            <select value={service} onChange={(e) => { setService(e.target.value as Service) }}>
              {services.map((s) => <option key={s.value} value={s.value}>{s.label}</option>)}
            </select>
          </label>
          <span className="muted loc-small">
            {covered} of {US_STATES.length} covered for {serviceLabel(service).toLowerCase()} · {taxed} charge tax ·{' '}
            {covered - taxed} exempt
          </span>
        </div>

        <div className="loc-map-wrap">
          <div className="loc-map" role="group" aria-label={`US states, coloured by the state-level rate on ${serviceLabel(service)}`}>
            {rows.map(({ state, charge: c }) => (
              <StateTile
                key={state.code}
                state={state}
                charge={c}
                selected={state.code === selected}
                onSelect={() => { setSelected(state.code) }}
              />
            ))}
          </div>
          <ul className="loc-legend" aria-label="Legend">
            {BANDS.map((b) => (
              <li key={b.band}><span className={`loc-swatch loc-band--${b.band}`} aria-hidden="true" />{b.label}</li>
            ))}
          </ul>
        </div>
      </Card>

      {current === undefined ? null : (
        <StateDetail
          state={current.state}
          charge={current.charge}
          service={service}
          all={componentsFor(regime, stateJurisdiction(current.state.code))}
          onTry={() => { onTry(current.state.code, service) }}
        />
      )}

      <Card
        title="Every state"
        aside={
          <label className="ztax-field loc-inline">
            <span>Sort by</span>
            <select value={sort} onChange={(e) => { setSort(e.target.value as Sort) }}>
              <option value="name">Name</option>
              <option value="rate">Rate, highest first</option>
            </select>
          </label>
        }
      >
        <div className="zp-scroll">
          <table className="table">
            <caption className="table__caption">
              State-level rate on {serviceLabel(service).toLowerCase()}: the ad valorem customer charges in force,
              added. Select a row to see its components.
            </caption>
            <thead>
              <tr>
                <th scope="col">State</th>
                <th scope="col">Standing</th>
                <th scope="col">State-level rate</th>
                <th scope="col">Components</th>
              </tr>
            </thead>
            <tbody>
              {sorted.map(({ state, charge: c }) => (
                <tr key={state.code} className={state.code === selected ? 'loc-row loc-row--selected' : 'loc-row'}>
                  <td>
                    <button type="button" className="loc-link" onClick={() => { setSelected(state.code) }}>
                      {state.name}
                    </button>{' '}
                    <span className="mono muted">US-{state.code}</span>
                  </td>
                  <td><StandingTag charge={c} /></td>
                  <td className="mono">{c.standing === 'uncovered' ? '—' : percent(c.rate)}</td>
                  <td className="loc-small">{c.components.map((x) => x.name).join(' + ') || '—'}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </Card>
    </>
  )
}

const StateTile = ({ state, charge: c, selected, onSelect }: {
  state: UsState
  charge: Charge
  selected: boolean
  onSelect: () => void
}) => (
  <button
    type="button"
    className={`loc-tile loc-band--${band(c)}${selected ? ' loc-tile--selected' : ''}`}
    style={{ gridRow: state.row + 1, gridColumn: state.col + 1 }}
    aria-pressed={selected}
    title={`${state.name}: ${c.standing === 'uncovered' ? 'not covered' : percent(c.rate)}`}
    onClick={onSelect}
  >
    <span className="loc-tile-code">{state.code}</span>
    <span className="loc-tile-rate">{c.standing === 'uncovered' ? '—' : percent(c.rate)}</span>
  </button>
)

const StandingTag = ({ charge: c }: { charge: Charge }) => {
  const tone = c.standing === 'taxed' ? 'ok' : c.standing === 'exempt' ? 'neutral' : 'warn'
  const text = c.standing === 'taxed' ? 'TAXED' : c.standing === 'exempt' ? 'EXEMPT' : 'NOT COVERED'
  return <span className={`ztax-status ztax-status--${tone}`}>{text}</span>
}

const StateDetail = ({ state, charge: c, service, all, onTry }: {
  state: UsState
  charge: Charge
  service: Service
  all: readonly Component[]
  onTry: () => void
}) => (
  <Card
    title={`${state.name} (US-${state.code})`}
    aside={
      c.standing === 'uncovered' ? null : (
        <button type="button" className="ztax-btn-copper" onClick={onTry}>
          Try a {serviceLabel(service).toLowerCase()} sale here
        </button>
      )
    }
  >
    <div className="loc-detail-head">
      <StandingTag charge={c} />
      <span>{STANDING_TEXT[c.standing]}</span>
      {c.standing === 'uncovered' ? null : (
        <span className="loc-big mono" aria-label="State-level rate">{percent(c.rate)}</span>
      )}
    </div>
    {c.fixed.length === 0 ? null : (
      <p className="zp-warn-note" role="note">
        {c.fixed.length} charge{c.fixed.length === 1 ? ' is' : 's are'} a fixed amount, not a proportion, and{' '}
        {c.fixed.length === 1 ? 'is' : 'are'} not in the rate above.
      </p>
    )}
    <ComponentsTable
      components={c.components}
      caption={`In force for ${serviceLabel(service).toLowerCase()}.`}
    />
    {all.length > c.components.length ? (
      <details className="ztax-raw">
        <summary>Every component for {state.name} ({all.length})</summary>
        <ComponentsTable components={all} caption={`Every component the content declares for ${state.name}.`} />
      </details>
    ) : null}
  </Card>
)
