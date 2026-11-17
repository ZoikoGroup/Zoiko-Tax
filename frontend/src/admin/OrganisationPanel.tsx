import { useCallback, useEffect, useState } from 'react'

import { api, ApiError, describeError, type SessionInfo, type Tenant } from '../platform/api'
import { ztax } from '../platform/client'
import { Loaded, useLoad } from '../ui/kit'

/**
 * Organisation & legal entities: the tenant as the cell records it
 * (GET /v1/admin/tenant), and the legal entity its fiscal surface works for.
 *
 * The cell has no legal-entity register yet. What it does expose is the
 * default legal entity a tenant's dashboard and subledger are kept for, the
 * markets that entity operates in, and the authorities it answers to — so that
 * is what is shown, and nothing is invented beyond it.
 */
export function OrganisationPanel({ session }: { session: SessionInfo }) {
  return (
    <>
      <TenantSection fallback={session.tenant} />
      <LegalEntitySection />
    </>
  )
}

function TenantSection({ fallback }: { fallback: Tenant }) {
  const [tenant, setTenant] = useState<Tenant | null>(null)
  const [error, setError] = useState('')
  const load = useCallback(() => {
    api
      .getTenant()
      .then((t) => { setTenant(t); setError('') })
      .catch((cause: unknown) => {
        setError(cause instanceof ApiError && cause.isForbidden ? 'Your roles do not include reading the tenant.' : describeError(cause))
      })
  }, [])
  useEffect(load, [load])
  const t = tenant ?? fallback

  return (
    <section className="panel" aria-labelledby="tenant-heading">
      <h2 id="tenant-heading">Organisation</h2>
      <p className="panel__note">
        The tenant is the organisation: every user, role, session and fiscal record here belongs to it, and its
        residency region decides where that data lives.
      </p>
      {error ? <p className="field__error" role="alert">{error} Showing what the session carries.</p> : null}
      <table className="table">
        <caption className="table__caption">{tenant === null && error === '' ? 'Reading the tenant…' : 'The tenant as the cell records it'}</caption>
        <tbody>
          <tr><th scope="row">Name</th><td>{t.displayName}</td></tr>
          <tr><th scope="row">Slug</th><td className="mono">{t.slug}</td></tr>
          <tr><th scope="row">Tenant id</th><td className="mono">{t.id}</td></tr>
          <tr><th scope="row">Residency region</th><td className="mono">{t.residencyRegion}</td></tr>
          <tr>
            <th scope="row">Status</th>
            <td><span className={t.status === 'ACTIVE' ? 'tag tag--ok' : 'tag tag--warn'}>{t.status}</span></td>
          </tr>
        </tbody>
      </table>
    </section>
  )
}

const READINESS_TAG: Record<string, string> = { READY: 'tag tag--ok', AT_RISK: 'tag', BLOCKED: 'tag tag--warn' }
const MARKET_TAG: Record<string, string> = { ACTIVE: 'tag tag--ok', PILOT: 'tag', RESEARCH_ONLY: 'tag tag--warn' }

function LegalEntitySection() {
  const { load } = useLoad((signal) => ztax.getDashboard({}, signal), [])
  return (
    <section className="panel" aria-labelledby="entity-heading">
      <h2 id="entity-heading">Legal entity</h2>
      <p className="panel__note">
        The legal entity the tenant&apos;s determinations, subledger and obligations are kept for. Additional entities
        are not yet administered here.
      </p>
      <Loaded load={load}>
        {(d) => (
          <>
            <table className="table">
              <caption className="table__caption">Default legal entity</caption>
              <tbody>
                <tr><th scope="row">Legal entity id</th><td className="mono">{d.legalEntityId}</td></tr>
                <tr><th scope="row">Markets</th><td>{d.coverage.markets.length} ({d.coverage.active} active · {d.coverage.pilot} pilot · {d.coverage.research} research only)</td></tr>
                <tr><th scope="row">Authorities</th><td>{d.jurisdictions.ready} ready · {d.jurisdictions.atRisk} at risk · {d.jurisdictions.blocked} blocked</td></tr>
              </tbody>
            </table>

            {d.coverage.markets.length === 0 ? null : (
              <table className="table">
                <caption className="table__caption">Markets the entity operates in</caption>
                <thead>
                  <tr>
                    <th scope="col">Country</th>
                    <th scope="col">Region</th>
                    <th scope="col">Status</th>
                    <th scope="col">Services</th>
                  </tr>
                </thead>
                <tbody>
                  {d.coverage.markets.map((m) => (
                    <tr key={m.country}>
                      <td><span className="mono">{m.country}</span> {m.name ?? ''}</td>
                      <td>{m.region ?? '—'}</td>
                      <td><span className={MARKET_TAG[m.status] ?? 'tag'}>{m.status.replaceAll('_', ' ')}</span></td>
                      <td className="mono">{m.services.length === 0 ? '—' : m.services.join(', ')}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}

            {d.jurisdictions.authorities.length === 0 ? null : (
              <table className="table">
                <caption className="table__caption">Authorities the entity answers to</caption>
                <thead>
                  <tr>
                    <th scope="col">Country</th>
                    <th scope="col">Authority</th>
                    <th scope="col">Readiness</th>
                    <th scope="col">Why</th>
                  </tr>
                </thead>
                <tbody>
                  {d.jurisdictions.authorities.map((a) => (
                    <tr key={`${a.country}-${a.authority}`}>
                      <td className="mono">{a.country}</td>
                      <td className="mono">{a.authority}</td>
                      <td><span className={READINESS_TAG[a.readiness] ?? 'tag'}>{a.readiness.replaceAll('_', ' ')}</span></td>
                      <td>{a.reason ?? '—'}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </>
        )}
      </Loaded>
    </section>
  )
}
