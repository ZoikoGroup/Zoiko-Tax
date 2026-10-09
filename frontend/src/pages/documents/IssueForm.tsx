import { useState, type FormEvent } from 'react'

import type { S } from '@zoikotax/sdk'

import { ztax } from '../../platform/client'
import { Card, Field, Outcome, Submit, useAction } from '../../ui/kit'
import { CURRENCY, Invalid, KeyNote, nowUtcLocal, todayUtc, toTimestamp, useIdempotencyKey } from '../determine/shared'
import { emptyDocLine, LinesEditor, lineRequests, linesProblem, type DocLineDraft } from './lines'

type IssueType = S<'DocumentRequest'>['type']

export const IssueForm = ({ onIssued }: { onIssued: (id: string) => void }) => {
  const [type, setType] = useState<IssueType>('INVOICE')
  const [number, setNumber] = useState('')
  const [issueDate, setIssueDate] = useState(todayUtc)
  const [taxPoint, setTaxPoint] = useState(nowUtcLocal)
  const [currency, setCurrency] = useState('EUR')
  const [refSystem, setRefSystem] = useState('')
  const [refNamespace, setRefNamespace] = useState('')
  const [refValue, setRefValue] = useState('')
  const [lines, setLines] = useState<DocLineDraft[]>(() => [emptyDocLine()])
  const [problem, setProblem] = useState<string | null>(null)
  const issue = useAction<S<'FiscalDocument'>>()
  const keys = useIdempotencyKey()

  const refParts = [refSystem.trim(), refNamespace.trim(), refValue.trim()]
  const refGiven = refParts.filter((p) => p !== '').length

  const submit = (e: FormEvent) => {
    e.preventDefault()
    const tp = toTimestamp(taxPoint)
    let p: string | null = null
    if (!/^\d{4}-\d{2}-\d{2}$/.test(issueDate)) {
      p = 'The issue date is required.'
    } else if (tp === null) {
      p = 'The tax point must be a date and time.'
    } else if (!CURRENCY.test(currency.trim())) {
      p = 'The currency must be a three-letter ISO 4217 code.'
    } else if (refGiven !== 0 && refGiven !== 3) {
      p = 'An external reference needs all three parts — source system, namespace and value — or none.'
    } else {
      p = linesProblem(lines, false)
    }
    setProblem(p)
    if (p !== null || tp === null) {
      return
    }
    const body: S<'DocumentRequest'> = {
      type,
      issueDate,
      taxPoint: tp,
      currency: currency.trim(),
      lines: lineRequests(lines),
      ...(number.trim() === '' ? {} : { number: number.trim() }),
      ...(refGiven === 3 ? { externalRef: { sourceSystem: refParts[0] ?? '', namespace: refParts[1] ?? '', value: refParts[2] ?? '' } } : {}),
    }
    const key = keys.keyFor(body)
    void issue.run(() => ztax.issueDocument(key, body)).then((d) => { if (d !== null) keys.succeeded() })
  }

  return (
    <Card title="Issue a document">
      <p className="muted ztax-note">
        A document presents committed decisions and invents no tax. For each decision and component it must carry exactly
        what was decided, or it is refused as CONFLICTED and nothing is committed — correct the decision, not the
        document. Each decision may be billed by one standing document at a time. Use “Fill taxes from decision” to copy
        the decided amounts.
      </p>
      <form onSubmit={submit} aria-label="Issue a document" style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
        <div className="ztax-form">
          <Field label="Type">
            <select value={type} onChange={(e) => { setType(e.target.value as IssueType) }}>
              <option value="INVOICE">INVOICE</option>
              <option value="DEBIT_NOTE">DEBIT_NOTE</option>
              <option value="ADJUSTMENT">ADJUSTMENT</option>
            </select>
          </Field>
          <Field label="Number (optional)" help="The legal, customer-visible number, e.g. INV-2026-0001.">
            <input value={number} onChange={(e) => { setNumber(e.target.value) }} autoComplete="off" />
          </Field>
          <Field label="Issue date">
            <input type="date" value={issueDate} onChange={(e) => { setIssueDate(e.target.value) }} />
          </Field>
          <Field label="Tax point (UTC)">
            <input type="datetime-local" step="1" value={taxPoint} onChange={(e) => { setTaxPoint(e.target.value) }} />
          </Field>
          <Field label="Currency">
            <input value={currency} maxLength={3} onChange={(e) => { setCurrency(e.target.value.toUpperCase()) }} />
          </Field>
        </div>
        <fieldset className="ztax-form ztax-line-box">
          <legend>External reference (optional) — another system's id, never parsed or assumed unique</legend>
          <Field label="Source system">
            <input value={refSystem} onChange={(e) => { setRefSystem(e.target.value) }} placeholder="bss" autoComplete="off" />
          </Field>
          <Field label="Namespace">
            <input value={refNamespace} onChange={(e) => { setRefNamespace(e.target.value) }} placeholder="invoice" autoComplete="off" />
          </Field>
          <Field label="Value">
            <input value={refValue} onChange={(e) => { setRefValue(e.target.value) }} placeholder="88231" autoComplete="off" />
          </Field>
        </fieldset>

        <LinesEditor lines={lines} onChange={setLines} currency={currency.trim()} />

        <div className="ztax-form-actions">
          <Submit busy={issue.busy}>Commit {type.replace('_', ' ').toLowerCase()}</Submit>
        </div>
      </form>
      <Invalid message={problem} />
      <KeyNote value={keys.current} />
      <Outcome
        error={issue.error}
        done={issue.done === null ? null : (
          <p className="ztax-ok-note">
            Committed {issue.done.type} <span className="mono">{issue.done.id}</span>.{' '}
            <button type="button" className="ztax-btn-quiet" onClick={() => { if (issue.done !== null) onIssued(issue.done.id) }}>
              Open it
            </button>
          </p>
        )}
      />
    </Card>
  )
}
