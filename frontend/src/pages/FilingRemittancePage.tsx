import { useState } from 'react'

import { civilDate } from '../dashboard/format'
import { ztax } from '../platform/client'
import { Amount, Card, Empty, Field, Loaded, PageHeader, Status, useLoad } from '../ui/kit'
import { PAIR_HEADER, PAIR_LABEL, PAIR_STATES, PAIR_TONE, pairFilings, pairRow, type PairState } from './filingPairs'
import type { PageProps } from './types'
import { both, downloadCsv, Tiles } from './workspace'
import './obligations.css'

// Filing-to-Remittance: each return filed with an authority beside the
// payment made for it. Pairs are matched by obligation (or, where the
// obligation row has since been superseded, by entity, period and authority).
// Amounts are shown as the cell recorded them; "same figure" is string
// equality of the filed control total and the instructed amount.

const isState = (v: string): v is PairState => (PAIR_STATES as readonly string[]).includes(v)

const FIGURE_TEXT = { same: 'same figure', differs: 'figures differ', 'n/a': '—' } as const

export const FilingRemittancePage = ({ onNavigate }: PageProps) => {
  const [state, setState] = useState<PairState | 'ALL'>('ALL')
  const { load, reload } = useLoad(
    (signal) => both(ztax.listCompliancePackages({ limit: 500 }, signal), ztax.listRemittances({ limit: 500 }, signal)),
    [],
  )

  return (
    <div className="ztax-page">
      <PageHeader
        title="Filing-to-Remittance"
        subtitle="Every return filed with an authority beside the payment made for it, newest period first. Figures are shown as recorded and never recomputed."
      />
      <Loaded load={load}>
        {([packages, remittances]) => {
          const pairs = pairFilings(packages.packages, remittances.remittances)
          const shown = pairs.filter((x) => state === 'ALL' || x.state === state)
          const differs = pairs.filter((x) => x.figure === 'differs').length
          return (
            <>
              <Tiles tiles={[
                { label: 'Filed and paid', count: pairs.filter((x) => x.state === 'FILED_AND_PAID').length, tone: 'ok' },
                { label: 'In progress', count: pairs.filter((x) => x.state === 'IN_PROGRESS' || x.state === 'NOT_YET_PAYABLE').length },
                { label: 'Accepted, unpaid', count: pairs.filter((x) => x.state === 'ACCEPTED_UNPAID').length, tone: 'warn' },
                { label: 'Exceptions', count: pairs.filter((x) => PAIR_TONE[x.state] === 'bad').length, tone: 'bad',
                  note: 'Rejected, returned, uncertain, or a payment with no filing' },
                { label: 'Filed and instructed figures differ', count: differs, tone: 'warn',
                  note: 'A correction after filing is the usual cause' },
              ]} />
              <Card
                title={`${shown.length} of ${pairs.length} pairs`}
                aside={
                  <div className="ztax-filter-bar">
                    <Field label="State">
                      <select value={state} onChange={(e) => { setState(isState(e.target.value) ? e.target.value : 'ALL') }}>
                        <option value="ALL">All</option>
                        {PAIR_STATES.map((s) => <option key={s} value={s}>{PAIR_LABEL[s]}</option>)}
                      </select>
                    </Field>
                    <button type="button" className="ztax-btn-quiet" onClick={reload}>Refresh</button>
                    <button type="button" className="ztax-btn-quiet" disabled={shown.length === 0}
                      onClick={() => { downloadCsv('filing-to-remittance', PAIR_HEADER, shown.map(pairRow)) }}>
                      Download CSV
                    </button>
                  </div>
                }
              >
                {shown.length === 0 ? (
                  <Empty>{pairs.length === 0 ? 'No filing or payment is on record yet.' : 'No pair is in this state.'}</Empty>
                ) : (
                  <div style={{ overflowX: 'auto' }}>
                    <table className="ztax-action-table">
                      <thead>
                        <tr>
                          <th>Return</th><th>Period</th><th>Filing</th><th>Filed total</th><th>Payment</th><th>Instructed</th>
                          <th>Figure</th><th>State</th><th>Open</th>
                        </tr>
                      </thead>
                      <tbody>
                        {shown.map((x) => (
                          <tr key={x.key} style={{ verticalAlign: 'top' }}>
                            <td style={{ fontWeight: 500, color: '#111827', paddingRight: 8 }}>
                              {x.pkg === undefined ? <span className="muted">no filing</span> : (
                                <>{x.pkg.obligationType} <span className="mono muted">{x.pkg.filingCountry} · {x.pkg.filingAuthority}</span></>
                              )}
                              {x.basis === 'entity-period-authority'
                                ? <div className="muted" style={{ fontSize: 10 }}>matched on entity, period and authority</div> : null}
                            </td>
                            <td style={{ paddingRight: 8, whiteSpace: 'nowrap' }}>
                              {civilDate(x.pkg?.periodStart ?? x.rem?.periodStart ?? '')} – {civilDate(x.pkg?.periodEnd ?? x.rem?.periodEnd ?? '')}
                            </td>
                            <td style={{ paddingRight: 8 }}>{x.pkg === undefined ? '—' : <Status value={x.pkg.status} />}</td>
                            <td style={{ paddingRight: 8, whiteSpace: 'nowrap' }}>
                              {x.filed === undefined ? <span className="muted">not prepared</span> : (
                                <>{x.filed.version === undefined ? null : <span className="muted">v{x.filed.version} · </span>}
                                  <Amount amount={x.filed.amount} currency={x.filed.currency} /></>
                              )}
                            </td>
                            <td style={{ paddingRight: 8 }}>
                              {x.rem === undefined ? <span className="muted">none</span> : (
                                <>
                                  <Status value={x.rem.status} />
                                  <div className="mono muted" style={{ fontSize: 11 }}>{x.rem.paymentReference}</div>
                                </>
                              )}
                              {x.others.length === 0 ? null
                                : <div className="muted" style={{ fontSize: 10 }}>+{x.others.length} earlier instruction{x.others.length === 1 ? '' : 's'}</div>}
                            </td>
                            <td style={{ paddingRight: 8, whiteSpace: 'nowrap' }}>
                              {x.rem === undefined ? '—' : <Amount amount={x.rem.amount} currency={x.rem.currency} />}
                            </td>
                            <td style={{ paddingRight: 8 }}>
                              {x.figure === 'n/a' ? <span className="muted">—</span> : (
                                <span className={`ztax-status ztax-status--${x.figure === 'same' ? 'ok' : 'warn'}`}>{FIGURE_TEXT[x.figure]}</span>
                              )}
                            </td>
                            <td style={{ paddingRight: 8 }}>
                              <span className={`ztax-status ztax-status--${PAIR_TONE[x.state]}`}>{PAIR_LABEL[x.state]}</span>
                            </td>
                            <td style={{ whiteSpace: 'nowrap' }}>
                              {x.pkg === undefined ? null : (
                                <button type="button" className="ztax-table-action-btn" onClick={() => { onNavigate('compliance', { tab: 'queue' }) }}>Filing</button>
                              )}{' '}
                              {x.rem === undefined ? null : (
                                <button type="button" className="ztax-table-action-btn" onClick={() => { onNavigate('remittance', { tab: 'evidence' }) }}>Payment</button>
                              )}
                            </td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                )}
              </Card>
              <p className="ztax-note">
                “Same figure” compares the filed version’s control total and the instructed amount as the cell recorded them, character
                for character, with their currencies. When they differ, the obligation was usually corrected after filing; the authority’s
                assessment decides what is owed.
              </p>
            </>
          )
        }}
      </Loaded>
    </div>
  )
}
