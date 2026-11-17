import { useState } from 'react'

import type { S } from '@zoikotax/sdk'

import { civilDate } from '../../dashboard/format'
import { ztax } from '../../platform/client'
import { Card, Empty, Loaded, useLoad } from '../../ui/kit'
import { RateText } from '../locations/ComponentsTable'
import { coverageInstant, serviceLabel, today, type Component, type Coverage } from '../locations/coverage'
import { countryName, taxTypeLabel } from '../locations/places'

/**
 * The content catalogue, read from what the cell serves: GET /v1/coverage
 * (the components in force and their sources), GET /v1/legal-matrix and
 * GET /v1/capabilities (the versions in force). Nothing here is maintained in
 * the browser, and only what the responses carry is shown.
 */

const useCoverage = (date: string) => useLoad((signal) => ztax.getCoverage({ at: coverageInstant(date) }, signal), [date])

const DateField = ({ date, onChange }: { date: string; onChange: (d: string) => void }) => (
  <label className="ztax-field" style={{ flexDirection: 'row', alignItems: 'center', gap: 6 }}>
    <span>In force on</span>
    <input type="date" value={date} onChange={(e) => { if (e.target.value !== '') onChange(e.target.value) }} />
  </label>
)

const matches = (needle: string, ...hay: readonly (string | undefined)[]) =>
  needle === '' || hay.some((h) => h !== undefined && h.toLowerCase().includes(needle))

// ---------------------------------------------------------------------------
// Service & levy codes
// ---------------------------------------------------------------------------

export const CodesTab = () => {
  const [date, setDate] = useState(today)
  const [filter, setFilter] = useState('')
  const { load } = useCoverage(date)
  return (
    <Card title="Service & levy codes" aside={<DateField date={date} onChange={setDate} />}>
      <p className="ztax-note">
        The service families each regime covers and every levy component in force, by its content code. A code is
        what a decision records and what a mapping or comparator points at.
      </p>
      <label className="ztax-field">
        <span>Filter</span>
        <input value={filter} placeholder="Code, name, country or tax type" onChange={(e) => { setFilter(e.target.value) }} />
      </label>
      <Loaded load={load}>
        {(coverage) => <Codes coverage={coverage} needle={filter.trim().toLowerCase()} />}
      </Loaded>
    </Card>
  )
}

const Codes = ({ coverage, needle }: { coverage: Coverage; needle: string }) => {
  const services = [...new Set(coverage.regimes.flatMap((r) => r.services))].sort()
  const rows = coverage.regimes.flatMap((r) => r.components.map((c) => ({ regime: r, c })))
    .filter(({ regime, c }) => matches(needle, c.id, c.name, c.taxType, c.jurisdiction, c.authority, regime.id, regime.country))
  return (
    <>
      <h3 className="ztax-card-title">Service families ({services.length})</h3>
      <p>{services.length === 0 ? '—' : services.map((s) => <span key={s} className="tag" title={s}>{serviceLabel(s)}</span>)}</p>
      <h3 className="ztax-card-title">Levy components ({rows.length})</h3>
      {rows.length === 0 ? <Empty>No component matches.</Empty> : (
        <div className="zp-scroll">
          <table className="table">
            <caption className="table__caption">
              Bundle <span className="mono">{coverage.bundle.bundleId}</span> · profile <span className="mono">{coverage.profileVersion}</span>
            </caption>
            <thead>
              <tr>
                <th scope="col">Code</th>
                <th scope="col">Name</th>
                <th scope="col">Regime</th>
                <th scope="col">Jurisdiction</th>
                <th scope="col">Tax type</th>
                <th scope="col">Rate</th>
                <th scope="col">Treatments</th>
                <th scope="col">Services</th>
                <th scope="col">In force</th>
              </tr>
            </thead>
            <tbody>
              {rows.map(({ regime, c }) => (
                <tr key={`${regime.id}-${c.id}-${c.from}`}>
                  <td className="mono">{c.id}</td>
                  <td>{c.name}</td>
                  <td><span className="mono">{regime.id}</span> <span className="muted">{countryName(regime.country)}</span></td>
                  <td className="mono">{c.jurisdiction}</td>
                  <td title={c.taxType}>{taxTypeLabel(c.taxType)}</td>
                  <td><RateText c={c} /></td>
                  <td className="mono">{c.treatments.join(', ')}</td>
                  <td>{c.services === undefined || c.services.length === 0 ? <span className="muted">all</span> : c.services.map(serviceLabel).join(', ')}</td>
                  <td style={{ whiteSpace: 'nowrap' }}>{civilDate(c.from)}{c.to === undefined ? '' : ` – ${civilDate(c.to)}`}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </>
  )
}

// ---------------------------------------------------------------------------
// Legal source library
// ---------------------------------------------------------------------------

interface SourceEntry {
  readonly source: string
  readonly citations: Set<string>
  readonly components: Component[]
  readonly jurisdictions: Set<string>
}

export const SourcesTab = () => {
  const [date, setDate] = useState(today)
  const [filter, setFilter] = useState('')
  const coverage = useCoverage(date)
  const matrix = useLoad((signal) => ztax.getLegalMatrix(signal), [])
  const needle = filter.trim().toLowerCase()
  return (
    <>
      <Card title="Legal source library" aside={<DateField date={date} onChange={setDate} />}>
        <p className="ztax-note">
          Every legal source the content in force cites, with its citations and the components that rest on it. Each
          rate a decision applies points back to one of these.
        </p>
        <label className="ztax-field">
          <span>Filter</span>
          <input value={filter} placeholder="Source, citation or jurisdiction" onChange={(e) => { setFilter(e.target.value) }} />
        </label>
        <Loaded load={coverage.load}>
          {(c) => <Sources coverage={c} needle={needle} />}
        </Loaded>
      </Card>
      <Card title="Legal opinions behind the service matrix">
        <p className="ztax-note">
          What ZoikoTax may do for a customer in each country rests on a legal opinion; the matrix records which.
        </p>
        <Loaded load={matrix.load}>
          {(m) => {
            const rules = m.rules.filter((r) => matches(needle, r.country, r.authority, r.opinionRef, r.provider))
            return rules.length === 0 ? <Empty>No rule matches.</Empty> : (
              <div className="zp-scroll">
                <table className="table">
                  <caption className="table__caption">Legal matrix <span className="mono">{m.version}</span>{m.draft ? ' (draft)' : ''}</caption>
                  <thead>
                    <tr>
                      <th scope="col">Country</th>
                      <th scope="col">Authority</th>
                      <th scope="col">Service</th>
                      <th scope="col">Opinion</th>
                      <th scope="col">Provider</th>
                      <th scope="col">Effective</th>
                    </tr>
                  </thead>
                  <tbody>
                    {rules.map((r) => (
                      <tr key={`${r.id}-${r.version}`}>
                        <td className="mono">{r.country}</td>
                        <td className="mono">{r.authority}</td>
                        <td className="mono">{r.service}</td>
                        <td className="mono">{r.opinionRef}</td>
                        <td>{r.provider}</td>
                        <td style={{ whiteSpace: 'nowrap' }}>{r.effectiveFrom.slice(0, 10)}{r.effectiveTo === undefined ? '' : ` – ${r.effectiveTo.slice(0, 10)}`}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )
          }}
        </Loaded>
      </Card>
    </>
  )
}

const Sources = ({ coverage, needle }: { coverage: Coverage; needle: string }) => {
  const bySource = new Map<string, SourceEntry>()
  for (const regime of coverage.regimes) {
    for (const c of regime.components) {
      const entry = bySource.get(c.source) ?? { source: c.source, citations: new Set(), components: [], jurisdictions: new Set() }
      entry.citations.add(c.citation)
      entry.components.push(c)
      entry.jurisdictions.add(c.jurisdiction)
      bySource.set(c.source, entry)
    }
  }
  const entries = [...bySource.values()]
    .filter((e) => matches(needle, e.source, ...e.citations, ...e.jurisdictions))
    .sort((a, b) => a.source.localeCompare(b.source))
  if (entries.length === 0) {
    return <Empty>No source matches.</Empty>
  }
  return (
    <div className="zp-scroll">
      <table className="table">
        <caption className="table__caption">{entries.length} sources cited by the content in force</caption>
        <thead>
          <tr>
            <th scope="col">Source</th>
            <th scope="col">Citations</th>
            <th scope="col">Jurisdictions</th>
            <th scope="col">Components</th>
          </tr>
        </thead>
        <tbody>
          {entries.map((e) => (
            <tr key={e.source}>
              <td className="mono">{e.source}</td>
              <td>
                <ul className="zp-list">{[...e.citations].map((c) => <li key={c}>{c}</li>)}</ul>
              </td>
              <td className="mono">{[...e.jurisdictions].join(', ')}</td>
              <td className="mono">{e.components.map((c) => c.id).join(', ')}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Content releases
// ---------------------------------------------------------------------------

type Capabilities = S<'Capabilities'>

export const ReleasesTab = () => {
  const caps = useLoad((signal) => ztax.getCapabilities(signal), [])
  const coverage = useCoverage(today())
  const matrix = useLoad((signal) => ztax.getLegalMatrix(signal), [])
  return (
    <>
      <Card title="Content release in force">
        <p className="ztax-note">
          The release this cell is serving now. The cell exposes the release in force, not a history of earlier ones;
          a decision records the bundle it was made under (Evidence → Provenance).
        </p>
        <Loaded load={caps.load}>
          {(c: Capabilities) => (
            <dl className="ztax-kv">
              <dt>Content train</dt><dd className="mono">{c.trains.content}</dd>
              <dt>Schema train</dt><dd className="mono">{c.trains.schema}</dd>
              <dt>App train</dt><dd className="mono">{c.trains.app}</dd>
              <dt>Bundle</dt>
              <dd>{c.content === undefined ? <span className="ztax-status ztax-status--warn">NONE LOADED</span> : <span className="mono">{c.content.bundleId}</span>}</dd>
              {c.content === undefined ? null : (
                <>
                  <dt>Bundle digest</dt><dd className="mono" style={{ wordBreak: 'break-all' }}>{c.content.digest}</dd>
                  <dt>Rule IR version</dt><dd>{c.content.irVersion}</dd>
                  <dt>Rule graph</dt><dd>{c.content.nodeCount} nodes</dd>
                </>
              )}
            </dl>
          )}
        </Loaded>
      </Card>
      <Card title="Coverage profile">
        <Loaded load={coverage.load}>
          {(c) => (
            <dl className="ztax-kv">
              <dt>Bundle</dt><dd className="mono">{c.bundle.bundleId}</dd>
              <dt>Profile version</dt><dd className="mono">{c.profileVersion}</dd>
              <dt>Graph version</dt><dd className="mono">{c.graphVersion}</dd>
              <dt>Regimes</dt><dd>{c.regimes.length}</dd>
              <dt>Components in force</dt><dd>{c.regimes.reduce((n, r) => n + r.components.length, 0)}</dd>
            </dl>
          )}
        </Loaded>
      </Card>
      <Card title="Legal matrix">
        <Loaded load={matrix.load}>
          {(m) => (
            <dl className="ztax-kv">
              <dt>Version</dt><dd className="mono">{m.version}</dd>
              <dt>Status</dt><dd>{m.draft ? <span className="ztax-status ztax-status--warn">DRAFT</span> : <span className="ztax-status ztax-status--ok">PUBLISHED</span>}</dd>
              <dt>Digest</dt><dd className="mono" style={{ wordBreak: 'break-all' }}>{m.digest ?? '—'}</dd>
              <dt>Rules</dt><dd>{m.rules.length}</dd>
            </dl>
          )}
        </Loaded>
      </Card>
    </>
  )
}
