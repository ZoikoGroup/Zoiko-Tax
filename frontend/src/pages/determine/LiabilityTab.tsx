import { useState, type FormEvent } from 'react'

import type { Result, S } from '@zoikotax/sdk'

import { instant } from '../../dashboard/format'
import { ztax } from '../../platform/client'
import { Amount, Card, Empty, ErrorNote, Field, Loaded, Raw, Status, Submit, useAction, useLoad } from '../../ui/kit'
import { isNotFound } from './shared'

type Levy = S<'LevyResponsibility'>
type Journal = S<'Journal'>
type Holder = S<'RoleHolder'>

const holderText = (h: Holder | undefined) =>
  h === undefined ? '—' : `${h.kind}${h.legalEntityId === undefined ? '' : ` ${h.legalEntityId}`}${h.ref === undefined ? '' : ` (${h.ref})`}`

/**
 * Liability & recovery: what the tenant owes and may recover, by control
 * account, and — for one decision — who bears, collects, reports and remits
 * each levy it applied, and the journals it posted. Every figure is the
 * cell's; debits and credits are shown apart, never netted here.
 */
export const LiabilityTab = ({ onOpenDecision }: { onOpenDecision: (id: string) => void }) => (
  <>
    <BalancesCard />
    <DecisionLiability onOpenDecision={onOpenDecision} />
  </>
)

const LIABILITY: readonly string[] = ['TAX_COLLECTED_LIABILITY', 'TAX_ACCRUED_LIABILITY', 'TAX_REMITTANCE_CLEARING', 'TAX_RETURN_CLEARING']
const RECOVERY: readonly string[] = ['TAX_RECOVERABLE', 'TAX_RECEIVABLE_CONTROL']

const BalancesCard = () => {
  const { load, reload } = useLoad((signal) => ztax.getSubledgerBalances(signal), [])
  return (
    <Card title="Liability and recovery balances" aside={<button type="button" className="ztax-btn-quiet" onClick={reload}>Refresh</button>}>
      <p className="muted ztax-note">
        The Tax Control Subledger&apos;s liability accounts (tax collected or accrued and owed on) and recovery accounts
        (tax recoverable or receivable), as the cell sums them from immutable journal lines.
      </p>
      <Loaded load={load}>
        {(b) => {
          const groups = [
            { title: 'Liability', rows: b.balances.filter((r) => LIABILITY.includes(r.account)) },
            { title: 'Recovery', rows: b.balances.filter((r) => RECOVERY.includes(r.account)) },
            { title: 'Other control accounts', rows: b.balances.filter((r) => !LIABILITY.includes(r.account) && !RECOVERY.includes(r.account)) },
          ]
          return b.balances.length === 0 ? <Empty>Nothing has posted yet.</Empty> : (
            <table className="table">
              <caption className="table__caption">Legal entity <span className="mono">{b.legalEntityId}</span></caption>
              <thead>
                <tr>
                  <th scope="col">Account</th>
                  <th scope="col">Currency</th>
                  <th scope="col">Debits</th>
                  <th scope="col">Credits</th>
                </tr>
              </thead>
              {groups.filter((g) => g.rows.length > 0).map((g) => (
                <tbody key={g.title}>
                  <tr><th scope="rowgroup" colSpan={4}>{g.title}</th></tr>
                  {g.rows.map((row) => (
                    <tr key={`${row.account}-${row.currency}`}>
                      <td className="mono">{row.account}</td>
                      <td>{row.currency}</td>
                      <td><Amount amount={row.debits} currency={row.currency} /></td>
                      <td><Amount amount={row.credits} currency={row.currency} /></td>
                    </tr>
                  ))}
                </tbody>
              ))}
            </table>
          )
        }}
      </Loaded>
    </Card>
  )
}

const DecisionLiability = ({ onOpenDecision }: { onOpenDecision: (id: string) => void }) => {
  const [decisionId, setDecisionId] = useState('')
  const [asked, setAsked] = useState('')
  const lookup = useAction<{ levies: Levy[]; journals: Journal[] }>()

  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    const id = decisionId.trim()
    setAsked(id)
    void lookup.run(async (): Promise<Result<{ levies: Levy[]; journals: Journal[] }>> => {
      const [levies, journals] = await Promise.all([ztax.listDecisionResponsibility(id), ztax.listDecisionJournals(id)])
      if (!levies.ok) {
        return levies
      }
      if (!journals.ok) {
        return journals
      }
      return { ok: true, data: { levies: levies.data.levies, journals: journals.data.journals } }
    })
  }

  const done = lookup.done

  return (
    <>
      <Card title="One decision's liability">
        <p className="muted ztax-note">
          Who carries each levy a decision applied — incidence, collection, reporting, remittance and the economic
          bearer — and the journals the decision posted to the subledger.
        </p>
        <form className="ztax-form" onSubmit={onSubmit} aria-label="Find a decision's liability">
          <Field label="Decision id">
            <input
              className="mono"
              value={decisionId}
              required
              placeholder="01920a4b-7c3e-7d21-9f40-3c1a2b4d5e6f"
              onChange={(e) => { setDecisionId(e.target.value) }}
            />
          </Field>
          <div className="ztax-form-actions">
            <Submit busy={lookup.busy}>Show liability</Submit>
          </div>
        </form>
        {lookup.error === null ? null : isNotFound(lookup.error) ? (
          <p className="ztax-error-note" role="status">No decision <span className="mono">{asked}</span> in this tenant.</p>
        ) : (
          <ErrorNote error={lookup.error} />
        )}
      </Card>

      {done === null || lookup.error !== null ? null : (
        <>
          <Card
            title="Levy responsibility"
            aside={<button type="button" className="ztax-btn-quiet" onClick={() => { onOpenDecision(asked) }}>Open decision</button>}
          >
            {done.levies.length === 0 ? <Empty>This decision applied no levy.</Empty> : done.levies.map((l) => (
              <div key={l.id} style={{ borderTop: '1px solid #e5e7eb', padding: '12px 0' }}>
                <p>
                  <span className="mono">{l.component}</span> <span className="muted">v{l.componentVersion}</span> ·{' '}
                  {l.taxType} · <span className="mono">{l.jurisdiction}</span> / <span className="mono">{l.authority}</span> ·{' '}
                  <span className="mono">{l.treatment}</span> <Status value={l.status} />
                </p>
                <p className="muted">
                  Item <span className="mono">{l.item}</span> · legal entity <span className="mono">{l.legalEntityId}</span> · rule{' '}
                  <span className="mono">{l.rule.id}@{l.rule.version}</span> · effective {instant(l.effectiveFrom)}
                </p>
                <table className="table">
                  <thead>
                    <tr>
                      <th scope="col">Role</th>
                      <th scope="col">Status</th>
                      <th scope="col">Statutory</th>
                      <th scope="col">Agent</th>
                      <th scope="col">Detail</th>
                    </tr>
                  </thead>
                  <tbody>
                    {l.roles.map((r) => (
                      <tr key={r.role}>
                        <td className="mono">{r.role}</td>
                        <td><Status value={r.status} /></td>
                        <td className="mono">{holderText(r.statutory)}</td>
                        <td className="mono">{holderText(r.agent)}</td>
                        <td>{r.detail ?? '—'}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            ))}
          </Card>
          <Card title="Journals posted">
            {done.journals.length === 0 ? <Empty>This decision posted no journal.</Empty> : done.journals.map((j) => (
              <div key={j.id} style={{ borderTop: '1px solid #e5e7eb', padding: '12px 0' }}>
                <p>
                  <span className="mono">{j.type}</span> · posted {instant(j.postingDate)} · period{' '}
                  <span className="mono">{j.legalPeriod}</span> · {j.currency}
                  {j.reversalOf === undefined ? null : <> · reverses <span className="mono">{j.reversalOf}</span></>}
                  <span className="muted"> · profile {j.profile.id}@{j.profile.version}</span>
                </p>
                <table className="table">
                  <thead>
                    <tr>
                      <th scope="col">Account</th>
                      <th scope="col">Debit</th>
                      <th scope="col">Credit</th>
                    </tr>
                  </thead>
                  <tbody>
                    {j.lines.map((line, i) => (
                      <tr key={i}>
                        <td className="mono">{line.account}</td>
                        <td>{line.side === 'DEBIT' ? <Amount amount={line.amount} currency={line.currency} /> : null}</td>
                        <td>{line.side === 'CREDIT' ? <Amount amount={line.amount} currency={line.currency} /> : null}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            ))}
            <Raw value={done} />
          </Card>
        </>
      )}
    </>
  )
}
