import { ztax } from '../../platform/client'
import { Card, Columns, Empty, Loaded, useLoad } from '../../ui/kit'
import { serviceLabel, type Coverage } from './coverage'
import { countryName } from './places'

const REASON: Record<string, string> = {
  ADVISORY_CELL: 'The cell is advisory, so nothing it determines may be filed.',
  FILING_NOT_READY: 'Determined, but filing for it is not ready.',
  NO_CONTENT_IN_FORCE: 'No content is in force for it.',
}

const MARKET_TONE: Record<string, 'ok' | 'warn' | 'bad'> = { ACTIVE: 'ok', PILOT: 'warn', RESEARCH_ONLY: 'bad' }

/**
 * Coverage intelligence: where the content in force determines tax, and where
 * the tenant's markets fall outside it. Read from GET /v1/coverage and the
 * dashboard's markets and authorities; a gap is a fact the two responses
 * carry, never a guess.
 */
export const GapsTab = ({ coverage, period }: { coverage: Coverage; period: string }) => {
  const { load } = useLoad((signal) => ztax.getDashboard({ period }, signal), [period])
  const determined = new Map<string, Set<string>>()
  for (const r of coverage.regimes) {
    const services = determined.get(r.country) ?? new Set<string>()
    r.services.forEach((s) => services.add(s))
    determined.set(r.country, services)
  }

  return (
    <>
      <Card title="Jurisdictions determined">
        <p className="ztax-note">
          {determined.size} {determined.size === 1 ? 'country has' : 'countries have'} a regime in force in this
          content. Anywhere else a determination is UNSUPPORTED.
        </p>
        {coverage.regimes.length === 0 ? <Empty>No regime in force.</Empty> : (
          <div className="zp-scroll">
            <table className="table">
              <thead>
                <tr>
                  <th scope="col">Country</th>
                  <th scope="col">Regime</th>
                  <th scope="col">Components</th>
                  <th scope="col">Needs</th>
                  <th scope="col">Services</th>
                </tr>
              </thead>
              <tbody>
                {coverage.regimes.map((r) => (
                  <tr key={r.id}>
                    <td><span className="mono">{r.country}</span> {countryName(r.country)}</td>
                    <td><span className="mono">{r.id}</span> <span className="muted">{r.name}</span></td>
                    <td>{r.components.length}</td>
                    <td className="mono">{r.requiresLevel ?? '—'}</td>
                    <td>{r.services.map(serviceLabel).join(', ')}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      <Loaded load={load}>
        {(d) => {
          const markets = d.coverage.markets
          const uncovered = markets.filter((m) => !determined.has(m.country))
          const serviceGaps = markets
            .filter((m) => determined.has(m.country))
            .map((m) => ({ m, missing: m.services.filter((s) => !(determined.get(m.country)?.has(s) ?? false)) }))
            .filter((x) => x.missing.length > 0)
          const notActive = markets.filter((m) => m.status !== 'ACTIVE')
          const authorities = d.jurisdictions.authorities.filter((a) => a.readiness !== 'READY')
          return (
            <>
              <Card title="Your markets">
                <p className="ztax-note">
                  {markets.length} markets{d.coverage.catalogue === undefined ? '' : ` in catalogue ${d.coverage.catalogue}`}:{' '}
                  {d.coverage.active} active · {d.coverage.pilot} pilot · {d.coverage.research} research only.
                </p>
              </Card>
              <Columns>
                <Card title="Markets with no content in force">
                  {uncovered.length === 0 ? <Empty>Every market has a regime in force.</Empty> : (
                    <ul className="zp-list">
                      {uncovered.map((m) => (
                        <li key={m.country}><span className="mono">{m.country}</span> {m.name ?? countryName(m.country)}</li>
                      ))}
                    </ul>
                  )}
                </Card>
                <Card title="Services sold but not covered">
                  {serviceGaps.length === 0 ? <Empty>Every service sold in a covered market is covered.</Empty> : (
                    <ul className="zp-list">
                      {serviceGaps.map(({ m, missing }) => (
                        <li key={m.country}>
                          <span className="mono">{m.country}</span> — {missing.map(serviceLabel).join(', ')}
                        </li>
                      ))}
                    </ul>
                  )}
                </Card>
              </Columns>
              <Columns>
                <Card title="Markets not yet active">
                  {notActive.length === 0 ? <Empty>Every market is active.</Empty> : (
                    <table className="table">
                      <thead><tr><th scope="col">Market</th><th scope="col">Status</th><th scope="col">Why</th></tr></thead>
                      <tbody>
                        {notActive.map((m) => (
                          <tr key={m.country}>
                            <td><span className="mono">{m.country}</span> {m.name ?? ''}</td>
                            <td><span className={`ztax-status ztax-status--${MARKET_TONE[m.status] ?? 'neutral'}`}>{m.status.replaceAll('_', ' ')}</span></td>
                            <td>{m.reason === undefined ? (m.research === undefined ? '—' : m.research.replaceAll('_', ' ')) : REASON[m.reason] ?? m.reason}</td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  )}
                </Card>
                <Card title="Authorities not ready">
                  {authorities.length === 0 ? <Empty>Every authority is ready.</Empty> : (
                    <table className="table">
                      <thead><tr><th scope="col">Country</th><th scope="col">Authority</th><th scope="col">Readiness</th><th scope="col">Why</th></tr></thead>
                      <tbody>
                        {authorities.map((a) => (
                          <tr key={`${a.country}-${a.authority}`}>
                            <td className="mono">{a.country}</td>
                            <td className="mono">{a.authority}</td>
                            <td>{a.readiness === 'BLOCKED' ? <span className="ztax-status ztax-status--bad">BLOCKED</span> : <span className="ztax-status ztax-status--warn">AT RISK</span>}</td>
                            <td>{a.reason ?? '—'}</td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  )}
                </Card>
              </Columns>
            </>
          )
        }}
      </Loaded>
    </>
  )
}
