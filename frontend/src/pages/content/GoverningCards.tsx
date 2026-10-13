import type { S } from '@zoikotax/sdk'

import { civilDate } from '../../dashboard/format'
import { ztax } from '../../platform/client'
import { Card, Loaded, Status, useLoad } from '../../ui/kit'

type Rule = S<'LegalRule'>

const TRAIN_ORDER = ['app', 'content', 'ai', 'adapter', 'infra', 'schema', 'migration'] as const

const LEGAL_TONE: Record<S<'LegalStatus'>, 'ok' | 'warn' | 'bad' | 'neutral'> = {
  DIRECT_ALLOWED: 'ok',
  DIRECT_WITH_AUTH: 'ok',
  QUALIFIED_PERSON_REQUIRED: 'warn',
  PARTNER_REQUIRED: 'warn',
  CUSTOMER_ONLY: 'warn',
  COUNSEL_PENDING: 'warn',
  PROHIBITED: 'bad',
  SUSPENDED: 'bad',
}

export const LegalStatusTag = ({ value }: { value: S<'LegalStatus'> }) => (
  <span className={`ztax-status ztax-status--${LEGAL_TONE[value]}`}>{value.replaceAll('_', ' ')}</span>
)

export const CapabilitiesCard = () => {
  const { load, reload } = useLoad((signal) => ztax.getCapabilities(signal), [])
  return (
    <Card title="This cell" aside={<button type="button" className="ztax-btn-quiet" onClick={reload}>Refresh</button>}>
      <Loaded load={load}>
        {(c) => (
          <>
            {c.authoritative ? null : (
              <p className="zp-warn-note" role="note">
                <strong>Advisory deployment.</strong> This cell reports authoritative: false. Its figures are advisory and
                must not be filed.
              </p>
            )}
            <dl className="ztax-kv">
              <dt>Cell</dt>
              <dd className="mono">{c.cell}</dd>
              <dt>Region</dt>
              <dd className="mono">{c.region}</dd>
              <dt>Environment</dt>
              <dd className="mono">{c.environment}</dd>
              <dt>Canonicalization</dt>
              <dd className="mono">{c.canonProfile}</dd>
              <dt>Authoritative</dt>
              <dd>{c.authoritative ? <Status value="ACTIVE" /> : <span className="ztax-status ztax-status--warn">ADVISORY</span>}</dd>
              <dt>Content bundle</dt>
              <dd>
                {c.content === undefined ? (
                  <span className="ztax-status ztax-status--warn">NONE LOADED</span>
                ) : (
                  <span className="mono">{c.content.bundleId}</span>
                )}
              </dd>
              {c.content === undefined ? null : (
                <>
                  <dt>Bundle digest</dt>
                  <dd className="mono" style={{ wordBreak: 'break-all' }}>{c.content.digest}</dd>
                  <dt>Rule IR version</dt>
                  <dd>{c.content.irVersion}</dd>
                  <dt>Rule graph</dt>
                  <dd>{c.content.nodeCount} nodes</dd>
                </>
              )}
              <dt>Reason codes</dt>
              <dd>{c.reasonCodes.length} this deployment can emit</dd>
            </dl>

            <h3 className="ztax-card-title">Release trains</h3>
            <table className="table">
              <caption className="table__caption">The seven versions that together make a response replayable.</caption>
              <thead>
                <tr>
                  <th scope="col">Train</th>
                  <th scope="col">Version</th>
                </tr>
              </thead>
              <tbody>
                {TRAIN_ORDER.map((t) => (
                  <tr key={t}>
                    <td>{t}</td>
                    <td className="mono">{c.trains[t]}</td>
                  </tr>
                ))}
              </tbody>
            </table>

            <details className="ztax-raw">
              <summary>Event types ({c.eventTypes.length})</summary>
              <ul className="zp-list mono">
                {c.eventTypes.map((e) => <li key={e}>{e}</li>)}
              </ul>
            </details>
            <details className="ztax-raw">
              <summary>Reason codes ({c.reasonCodes.length})</summary>
              <p className="mono" style={{ fontSize: 11, lineHeight: 1.7 }}>
                {c.reasonCodes.map((r) => <span key={r} className="tag">{r}</span>)}
              </p>
            </details>
          </>
        )}
      </Loaded>
    </Card>
  )
}

export const MatrixCard = ({ onCheck }: { onCheck: (rule: Rule) => void }) => {
  const { load, reload } = useLoad((signal) => ztax.getLegalMatrix(signal), [])
  return (
    <Card
      title="LegalAuthorization matrix"
      aside={<button type="button" className="ztax-btn-quiet" onClick={reload}>Refresh</button>}
    >
      <p className="muted zp-intro">
        For each country, authority and service, whether ZoikoTax may act directly, only with the customer's
        authorization, only through a qualified person or partner, not at all, or whether counsel has yet to say. A
        content pack in production for COMPUTE implies nothing here.
      </p>
      <Loaded load={load}>
        {(m) => (
          <>
            <dl className="ztax-kv">
              <dt>Edition</dt>
              <dd className="mono">{m.version === '' ? '—' : m.version}</dd>
              <dt>Approval</dt>
              <dd>
                {m.draft ? (
                  <span className="ztax-status ztax-status--warn">DRAFT · NOT APPROVED, DEVELOPMENT ONLY</span>
                ) : (
                  <Status value="ACTIVE" />
                )}
              </dd>
              <dt>Digest</dt>
              <dd className="mono" style={{ wordBreak: 'break-all' }}>{m.digest ?? '—'}</dd>
            </dl>
            {m.rules.length === 0 ? (
              <p className="ztax-error-note" role="note">
                This cell has no matrix. Every legally sensitive action is blocked.
              </p>
            ) : (
              <div className="zp-scroll">
                <table className="table">
                  <thead>
                    <tr>
                      <th scope="col">Country</th>
                      <th scope="col">Authority</th>
                      <th scope="col">Service</th>
                      <th scope="col">Status</th>
                      <th scope="col">Provider</th>
                      <th scope="col">Authorization</th>
                      <th scope="col">Opinion</th>
                      <th scope="col">Effective</th>
                      <th scope="col"><span className="zp-sr-only">Check</span></th>
                    </tr>
                  </thead>
                  <tbody>
                    {m.rules.map((r) => (
                      <tr key={`${r.id}-${r.version}`}>
                        <td>{r.country}</td>
                        <td className="mono">{r.authority}</td>
                        <td>{r.service}</td>
                        <td><LegalStatusTag value={r.status} /></td>
                        <td>{r.provider}</td>
                        <td>
                          {r.authorizationType.replaceAll('_', ' ')}
                          {r.requiresPeriods ? <div className="muted">names its periods</div> : null}
                          {r.requiresMatters ? <div className="muted">names its matters</div> : null}
                          {r.credential === undefined ? null : <div className="muted">credential: {r.credential}</div>}
                        </td>
                        <td>{r.opinionRef}</td>
                        <td style={{ whiteSpace: 'nowrap' }}>
                          {civilDate(r.effectiveFrom.slice(0, 10))}
                          {r.effectiveTo === undefined ? null : <> – {civilDate(r.effectiveTo.slice(0, 10))}</>}
                        </td>
                        <td>
                          <button
                            type="button"
                            className="ztax-btn-quiet"
                            aria-label={`Check ${r.country} ${r.authority} ${r.service}`}
                            onClick={() => { onCheck(r) }}
                          >
                            Check
                          </button>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </>
        )}
      </Loaded>
    </Card>
  )
}
