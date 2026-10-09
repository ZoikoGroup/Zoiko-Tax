import { useState, type FormEvent } from 'react'

import type { S } from '@zoikotax/sdk'

import { ztax } from '../../platform/client'
import { civilDate, instant } from '../../dashboard/format'
import { Amount, Card, Field, Id, Loaded, Outcome, Raw, Status, Submit, useAction, useLoad } from '../../ui/kit'
import { Invalid, KeyNote, KV, nowUtcLocal, todayUtc, toTimestamp, useIdempotencyKey } from '../determine/shared'
import { emptyDocLine, LinesEditor, lineRequests, linesProblem, type DocLineDraft } from './lines'

type CorrectionType = S<'CorrectionRequest'>['type']

/** The registered CORRECTION_* reason codes (ZTAX-FIN-REQ-0008). */
export const CORRECTION_REASONS: readonly { code: string; text: string }[] = [
  { code: 'CORRECTION_PRICING_ERROR', text: 'Wrong price or quantity billed' },
  { code: 'CORRECTION_CUSTOMER_CANCELLATION', text: 'The customer cancelled what was billed' },
  { code: 'CORRECTION_DUPLICATE_BILLING', text: 'The same supply was billed twice' },
  { code: 'CORRECTION_SERVICE_NOT_PROVIDED', text: 'The supply was not, or not fully, provided' },
  { code: 'CORRECTION_TAX_REDETERMINED', text: 'The tax was corrected by a later decision' },
  { code: 'CORRECTION_CUSTOMER_DATA', text: 'Wrong customer or document data' },
]

export const DocumentView = ({ id, onOpen }: { id: string; onOpen: (id: string) => void }) => {
  const doc = useLoad((signal) => ztax.getDocument(id, signal), [id])
  const lineage = useLoad((signal) => ztax.getDocumentLineage(id, signal), [id])
  const reload = () => {
    doc.reload()
    lineage.reload()
  }
  return (
    <Loaded load={doc.load}>
      {(d) => (
        <>
          <Card
            title={`${d.type.replaceAll('_', ' ')} ${d.number ?? ''}`.trim()}
            aside={
              <span className="ztax-form-actions">
                <Status value={d.status} />
                <button type="button" className="ztax-btn-quiet" onClick={reload}>Refresh</button>
              </span>
            }
          >
            <KV
              rows={[
                ['Document', <span className="mono">{d.id}</span>],
                ['Type', d.type],
                ['Number', d.number ?? '—'],
                ['Status', <Status value={d.status} />],
                ['Reason', <span className="mono">{d.reasonCode ?? '—'}</span>],
                ['Issue date', civilDate(d.issueDate)],
                ['Tax point', instant(d.taxPoint)],
                ['Currency', d.currency],
                ['Root', <span className="mono">{d.rootId}</span>],
                [
                  'Predecessors',
                  d.predecessors.length === 0 ? '—' : (
                    <span className="ztax-form-actions">
                      {d.predecessors.map((p) => (
                        <button key={p} type="button" className="ztax-btn-quiet" title={p} onClick={() => { onOpen(p) }}>
                          <Id value={p} />
                        </button>
                      ))}
                    </span>
                  ),
                ],
                [
                  'External ref',
                  d.externalRef === undefined ? '—' : (
                    <span className="mono">{d.externalRef.sourceSystem} / {d.externalRef.namespace} / {d.externalRef.value}</span>
                  ),
                ],
                ['Legal entity', <span className="mono">{d.legalEntityId}</span>],
                ['Decisions', <span className="mono">{d.decisionIds.join(', ')}</span>],
                ['Recorded', `${instant(d.recordedAt)}`],
                ['Recorded by', <Id value={d.recordedBy} />],
              ]}
            />

            <table className="table">
              <caption className="table__caption">Lines{d.netTotal.startsWith('-') ? ' — a cancelling document carries negative amounts' : ''}</caption>
              <thead>
                <tr>
                  <th scope="col">Line</th>
                  <th scope="col">Source line</th>
                  <th scope="col">Net</th>
                  <th scope="col">Discount</th>
                  <th scope="col">Taxes</th>
                  <th scope="col">Replaces</th>
                </tr>
              </thead>
              <tbody>
                {d.lines.map((l) => (
                  <tr key={l.id}>
                    <td><Id value={l.id} /></td>
                    <td className="mono">{l.sourceLineRef ?? '—'}</td>
                    <td><Amount amount={l.net} currency={d.currency} /></td>
                    <td><Amount amount={l.discount} currency={d.currency} /></td>
                    <td>
                      {l.taxes.length === 0 ? '—' : (
                        <ul style={{ margin: 0, paddingLeft: 16 }}>
                          {l.taxes.map((t) => (
                            <li key={`${t.decisionId}-${t.component}`}>
                              <span className="mono">{t.component}</span> <Amount amount={t.amount} currency={d.currency} />{' '}
                              <span className="muted">from <Id value={t.decisionId} /></span>
                            </li>
                          ))}
                        </ul>
                      )}
                    </td>
                    <td><Id value={l.predecessorLineId} /></td>
                  </tr>
                ))}
              </tbody>
            </table>

            <KV
              rows={[
                ['Net total', <Amount amount={d.netTotal} currency={d.currency} />],
                ['Tax total', <Amount amount={d.taxTotal} currency={d.currency} />],
                ['Gross total', <Amount amount={d.grossTotal} currency={d.currency} />],
              ]}
            />
            <p className="muted ztax-note">Totals are the server's; nothing on this page adds amounts.</p>

            <table className="table">
              <caption className="table__caption">Status history, oldest first</caption>
              <thead>
                <tr>
                  <th scope="col">#</th>
                  <th scope="col">Status</th>
                  <th scope="col">Caused by</th>
                  <th scope="col">Recorded</th>
                  <th scope="col">By</th>
                </tr>
              </thead>
              <tbody>
                {d.history.map((ev) => (
                  <tr key={ev.seq}>
                    <td>{ev.seq}</td>
                    <td><Status value={ev.status} /></td>
                    <td>
                      {ev.causeId === undefined ? '—' : (
                        <button type="button" className="ztax-btn-quiet" title={ev.causeId} onClick={() => { if (ev.causeId !== undefined) onOpen(ev.causeId) }}>
                          <Id value={ev.causeId} />
                        </button>
                      )}
                    </td>
                    <td>{instant(ev.recordedAt)}</td>
                    <td><Id value={ev.recordedBy} /></td>
                  </tr>
                ))}
              </tbody>
            </table>
            <Raw value={d} label="Full document" />
          </Card>

          <Card title="Lineage">
            <p className="muted ztax-note">The original and every void, credit note and rebill that followed it, in commit order.</p>
            <Loaded load={lineage.load}>
              {(chain) => (
                <ol className="ztax-lineage" aria-label="Document lineage">
                  {chain.documents.map((c) => (
                    <li key={c.id}>
                      <button
                        type="button"
                        className="ztax-btn-quiet"
                        aria-current={c.id === d.id ? 'true' : undefined}
                        title={c.id}
                        onClick={() => { onOpen(c.id) }}
                      >
                        {c.type.replaceAll('_', ' ')} {c.number ?? ''} · {c.status.replaceAll('_', ' ')} ·{' '}
                        <Amount amount={c.grossTotal} currency={c.currency} />
                      </button>
                    </li>
                  ))}
                </ol>
              )}
            </Loaded>
          </Card>

          <CorrectionForm doc={d} onCorrected={onOpen} />
        </>
      )}
    </Loaded>
  )
}

const CorrectionForm = ({ doc, onCorrected }: { doc: S<'FiscalDocument'>; onCorrected: (id: string) => void }) => {
  const [type, setType] = useState<CorrectionType>('VOID')
  const [reason, setReason] = useState(CORRECTION_REASONS[0]?.code ?? '')
  const [number, setNumber] = useState('')
  const [issueDate, setIssueDate] = useState(todayUtc)
  const [taxPoint, setTaxPoint] = useState(nowUtcLocal)
  const [cancel, setCancel] = useState<readonly string[]>([])
  const [lines, setLines] = useState<DocLineDraft[]>(() => [emptyDocLine()])
  const [problem, setProblem] = useState<string | null>(null)
  const correct = useAction<S<'FiscalDocument'>>()
  const keys = useIdempotencyKey()

  const submit = (e: FormEvent) => {
    e.preventDefault()
    const tp = toTimestamp(taxPoint)
    let p: string | null = null
    if (!/^\d{4}-\d{2}-\d{2}$/.test(issueDate)) {
      p = 'The issue date is required.'
    } else if (tp === null) {
      p = 'The tax point must be a date and time.'
    } else if (type === 'REBILL') {
      p = linesProblem(lines, true)
    }
    setProblem(p)
    if (p !== null || tp === null) {
      return
    }
    const body: S<'CorrectionRequest'> = {
      type,
      reason,
      issueDate,
      taxPoint: tp,
      ...(number.trim() === '' ? {} : { number: number.trim() }),
      ...(type === 'CREDIT_NOTE' && cancel.length > 0 ? { cancelLines: [...cancel] } : {}),
      ...(type === 'REBILL' ? { lines: lineRequests(lines) } : {}),
    }
    const key = keys.keyFor(body)
    void correct.run(() => ztax.correctDocument(doc.id, key, body)).then((r) => { if (r !== null) keys.succeeded() })
  }

  return (
    <Card title="Correct this document">
      <p className="muted ztax-note">
        A committed document is never edited: a correction is a new document sharing its root. VOID cancels every line
        exactly (refused once any line is credited). CREDIT_NOTE cancels the chosen lines, or every line not yet cancelled.
        REBILL charges again once the original is voided or fully credited, each line naming the original line it replaces.
      </p>
      <form onSubmit={submit} aria-label="Correct the document" style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
        <div className="ztax-form">
          <Field label="Correction">
            <select value={type} onChange={(e) => { setType(e.target.value as CorrectionType) }}>
              <option value="VOID">VOID</option>
              <option value="CREDIT_NOTE">CREDIT_NOTE</option>
              <option value="REBILL">REBILL</option>
            </select>
          </Field>
          <Field label="Reason">
            <select value={reason} onChange={(e) => { setReason(e.target.value) }}>
              {CORRECTION_REASONS.map((r) => <option key={r.code} value={r.code}>{r.code} — {r.text}</option>)}
            </select>
          </Field>
          <Field label="Number (optional)" help="The correcting document's legal number.">
            <input value={number} onChange={(e) => { setNumber(e.target.value) }} autoComplete="off" />
          </Field>
          <Field label="Issue date">
            <input type="date" value={issueDate} onChange={(e) => { setIssueDate(e.target.value) }} />
          </Field>
          <Field label="Tax point (UTC)">
            <input type="datetime-local" step="1" value={taxPoint} onChange={(e) => { setTaxPoint(e.target.value) }} />
          </Field>
        </div>

        {type === 'CREDIT_NOTE' ? (
          <fieldset className="ztax-line-box">
            <legend>Lines to cancel — none ticked cancels every line not yet cancelled</legend>
            {doc.lines.map((l) => (
              <label key={l.id} className="ztax-field ztax-check" htmlFor={`cancel-${l.id}`}>
                <input
                  id={`cancel-${l.id}`}
                  type="checkbox"
                  checked={cancel.includes(l.id)}
                  onChange={(e) => {
                    setCancel((c) => (e.target.checked ? [...c, l.id] : c.filter((x) => x !== l.id)))
                  }}
                />
                <span className="mono">{l.sourceLineRef ?? l.id}</span>
                <Amount amount={l.net} currency={doc.currency} />
              </label>
            ))}
          </fieldset>
        ) : null}

        {type === 'REBILL' ? (
          <LinesEditor lines={lines} onChange={setLines} currency={doc.currency} predecessors={doc.lines} />
        ) : null}

        <div className="ztax-form-actions">
          <Submit busy={correct.busy}>Commit {type.replace('_', ' ').toLowerCase()}</Submit>
        </div>
      </form>
      <Invalid message={problem} />
      <KeyNote value={keys.current} />
      <Outcome
        error={correct.error}
        done={correct.done === null ? null : (
          <p className="ztax-ok-note">
            Committed {correct.done.type} <span className="mono">{correct.done.id}</span>.{' '}
            <button type="button" className="ztax-btn-quiet" onClick={() => { if (correct.done !== null) onCorrected(correct.done.id) }}>
              Open it
            </button>
          </p>
        )}
      />
    </Card>
  )
}
