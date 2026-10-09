import { useState, type FormEvent } from 'react'

import type { S } from '@zoikotax/sdk'

import { civilDate, instant } from '../../dashboard/format'
import { ztax } from '../../platform/client'
import { Card, Empty, Field, Id, Loaded, Outcome, Submit, useAction, useLoad } from '../../ui/kit'
import { fromDateInput } from './time'

type Policy = S<'RetentionPolicy'>
type Verdict = S<'RetentionVerdict'>
type RecordClass = S<'RetentionRecordClass'>
type Trigger = S<'RetentionTrigger'>

const RECORD_CLASSES: readonly RecordClass[] = ['DECISION', 'DOCUMENT', 'JOURNAL', 'REFUND']

const TRIGGERS: readonly { value: Trigger; label: string }[] = [
  { value: 'EVENT_YEAR_END', label: 'End of the calendar year of the event' },
  { value: 'EVENT_TIME', label: 'The taxable event' },
  { value: 'RECORDED_AT', label: 'The record being made' },
]

const OUTCOME_TONE: Record<Verdict['outcome'], string> = {
  RETAIN: 'ok',
  HELD: 'neutral',
  ELIGIBLE: 'warn',
  NO_POLICY: 'warn',
  CONFLICTED: 'bad',
}

const OUTCOME_TEXT: Record<Verdict['outcome'], string> = {
  RETAIN: 'Inside its retention period: keep.',
  HELD: 'Under an active legal hold: keep, whatever the period says.',
  ELIGIBLE: 'Past its retention period and under no hold: eligible for disposal.',
  NO_POLICY: 'No policy governs this record. It is kept, and a person must resolve it.',
  CONFLICTED: 'Several policies disagree. It is kept, and a person must resolve it.',
}

export const RetentionTab = ({ admin }: { admin: boolean }) => {
  const { load, reload } = useLoad((signal) => ztax.listRetentionPolicies(signal), [])

  return (
    <>
      <Card
        title="Retention policies"
        aside={<button type="button" className="ztax-btn-quiet" onClick={reload}>Refresh</button>}
      >
        <p className="muted zp-intro">
          A policy keeps one class of record of one country's legal entities for a number of years from a trigger, on a
          cited authority. Every version is listed, oldest first per policy, because a verdict names the version it used.
          There is no default period.
        </p>
        <Loaded load={load}>
          {(data) =>
            data.policies.length === 0 ? (
              <Empty>No retention policy is recorded. Every record is kept and reported NO_POLICY.</Empty>
            ) : (
              <div className="zp-scroll">
                <table className="table">
                  <thead>
                    <tr>
                      <th scope="col">Policy</th>
                      <th scope="col">Version</th>
                      <th scope="col">Record class</th>
                      <th scope="col">Country</th>
                      <th scope="col">Years</th>
                      <th scope="col">Runs from</th>
                      <th scope="col">Effective from</th>
                      <th scope="col">Citation</th>
                      <th scope="col">Recorded</th>
                    </tr>
                  </thead>
                  <tbody>
                    {data.policies.map((p) => (
                      <tr key={`${p.id}-${p.version}`}>
                        <td className="mono">{p.id}</td>
                        <td>v{p.version}</td>
                        <td>{p.recordClass}</td>
                        <td>{p.country}</td>
                        <td>{p.years}</td>
                        <td>{p.trigger.replaceAll('_', ' ').toLowerCase()}</td>
                        <td>{civilDate(p.effectiveFrom.slice(0, 10))}</td>
                        <td>{p.citation}</td>
                        <td>{instant(p.recordedAt)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )
          }
        </Loaded>
      </Card>

      <VerdictLookup />

      {admin ? (
        <PolicyForm onRecorded={reload} />
      ) : (
        <Card title="Record a policy version">
          <p className="muted zp-intro">Recording a retention policy needs the ADMIN role.</p>
        </Card>
      )}
    </>
  )
}

const PolicyForm = ({ onRecorded }: { onRecorded: () => void }) => {
  const [id, setId] = useState('')
  const [recordClass, setRecordClass] = useState<RecordClass>('DECISION')
  const [country, setCountry] = useState('')
  const [years, setYears] = useState(10)
  const [trigger, setTrigger] = useState<Trigger>('EVENT_YEAR_END')
  const [effectiveFrom, setEffectiveFrom] = useState('')
  const [citation, setCitation] = useState('')
  const [local, setLocal] = useState<string | null>(null)
  const record = useAction<Policy>()

  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    const from = fromDateInput(effectiveFrom)
    if (from === null) {
      setLocal('Give the date the policy takes effect.')
      return
    }
    if (!Number.isInteger(years) || years < 1 || years > 100) {
      setLocal('Years is a whole number from 1 to 100.')
      return
    }
    setLocal(null)
    void record
      .run(() =>
        ztax.recordRetentionPolicy({
          id: id.trim(),
          recordClass,
          country: country.trim().toUpperCase(),
          years,
          trigger,
          effectiveFrom: from,
          citation: citation.trim(),
        }),
      )
      .then((p) => {
        if (p !== null) onRecorded()
      })
  }

  return (
    <Card title="Record a policy version">
      <p className="muted zp-intro">
        Recording under an id that exists makes its next version; nothing is overwritten. The act is written to the
        administrative audit trail.
      </p>
      <form className="ztax-form" onSubmit={onSubmit} aria-label="Record a retention policy">
        <Field label="Policy id">
          <input className="mono" value={id} required maxLength={128} placeholder="de-decisions" onChange={(e) => { setId(e.target.value) }} />
        </Field>
        <Field label="Record class">
          <select value={recordClass} onChange={(e) => { setRecordClass(e.target.value as RecordClass) }}>
            {RECORD_CLASSES.map((c) => <option key={c} value={c}>{c}</option>)}
          </select>
        </Field>
        <Field label="Country (ISO 3166-1 alpha-2)">
          <input value={country} required pattern="[A-Za-z]{2}" maxLength={2} placeholder="DE" onChange={(e) => { setCountry(e.target.value) }} />
        </Field>
        <Field label="Years">
          <input
            type="number"
            min={1}
            max={100}
            step={1}
            required
            value={Number.isNaN(years) ? '' : years}
            onChange={(e) => { setYears(e.target.valueAsNumber) }}
          />
        </Field>
        <Field label="Runs from">
          <select value={trigger} onChange={(e) => { setTrigger(e.target.value as Trigger) }}>
            {TRIGGERS.map((t) => <option key={t.value} value={t.value}>{t.label}</option>)}
          </select>
        </Field>
        <Field label="Effective from (UTC)">
          <input type="date" value={effectiveFrom} required onChange={(e) => { setEffectiveFrom(e.target.value) }} />
        </Field>
        <Field label="Citation" help="The statute, regulation or contract the period comes from.">
          <input value={citation} required maxLength={1000} placeholder="AO §147(3)" onChange={(e) => { setCitation(e.target.value) }} />
        </Field>
        <div className="ztax-form-actions">
          <Submit busy={record.busy}>Record version</Submit>
        </div>
      </form>
      {local === null ? null : <p className="ztax-error-note" role="alert">{local}</p>}
      <Outcome
        error={record.error}
        done={
          record.done === null ? null : (
            <p className="ztax-ok-note" role="status">
              Recorded <span className="mono">{record.done.id}</span> version {record.done.version}.
            </p>
          )
        }
      />
    </Card>
  )
}

const VerdictLookup = () => {
  const [decisionId, setDecisionId] = useState('')
  const verdict = useAction<Verdict>()

  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    const id = decisionId.trim()
    void verdict.run(() => ztax.getDecisionRetention(id))
  }

  const v = verdict.error === null ? verdict.done : null

  return (
    <Card title="Retention verdict for a decision">
      <p className="muted zp-intro">
        The verdict a disposal job takes immediately before it acts. Asking changes nothing about the decision.
      </p>
      <form className="ztax-form" onSubmit={onSubmit} aria-label="Look up a decision's retention verdict">
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
          <Submit busy={verdict.busy}>Get verdict</Submit>
        </div>
      </form>
      <Outcome
        error={verdict.error}
        done={
          v === null ? null : (
            <dl className="ztax-kv" aria-live="polite">
              <dt>Outcome</dt>
              <dd>
                <span className={`ztax-status ztax-status--${OUTCOME_TONE[v.outcome]}`}>{v.outcome.replaceAll('_', ' ')}</span>{' '}
                {OUTCOME_TEXT[v.outcome]}
              </dd>
              <dt>Decision</dt>
              <dd className="mono">{v.decisionId}</dd>
              <dt>Record class</dt>
              <dd>{v.recordClass}{v.country === undefined ? '' : ` · ${v.country}`}</dd>
              <dt>Policy</dt>
              <dd>{v.policy === undefined ? '—' : <span className="mono">{v.policy.id} v{v.policy.version}</span>}</dd>
              <dt>Retain until</dt>
              <dd>{v.retainUntil === undefined ? '—' : instant(v.retainUntil)}</dd>
              <dt>Holds</dt>
              <dd>
                {(v.holds ?? []).length === 0 ? 'none' : v.holds?.map((h) => <span key={h} className="tag"><Id value={h} /></span>)}
              </dd>
              {(v.candidates ?? []).length === 0 ? null : (
                <>
                  <dt>Disagreeing policies</dt>
                  <dd>
                    {v.candidates?.map((c) => (
                      <span key={`${c.id}-${c.version}`} className="tag mono">{c.id} v{c.version}</span>
                    ))}
                  </dd>
                </>
              )}
              {v.detail === undefined ? null : (
                <>
                  <dt>Detail</dt>
                  <dd>{v.detail}</dd>
                </>
              )}
              <dt>Evaluated at</dt>
              <dd>{instant(v.evaluatedAt)}</dd>
            </dl>
          )
        }
      />
    </Card>
  )
}
