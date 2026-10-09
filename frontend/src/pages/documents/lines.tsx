/**
 * Document lines as a person types them, for an invoice or a rebill.
 *
 * A document invents no tax: every tax row names the committed decision and
 * the component it comes from, and must carry exactly the amount decided. The
 * "Fill taxes from decision" helper copies those amounts as the decision states
 * them — strings, never computed (ADR-0019 C1).
 */
import { useState } from 'react'

import type { S } from '@zoikotax/sdk'

import { ztax } from '../../platform/client'
import { ErrorNote, Field } from '../../ui/kit'
import { isDecimal } from '../determine/shared'
import { newKey } from '../../ui/kit'

export interface TaxDraft {
  readonly rowId: string
  readonly decisionId: string
  readonly component: string
  readonly amount: string
}

export interface DocLineDraft {
  readonly rowId: string
  readonly sourceLineRef: string
  readonly net: string
  readonly predecessorLineId: string
  readonly taxes: readonly TaxDraft[]
}

const rowId = () => newKey()

export function emptyTax(decisionId = ''): TaxDraft {
  return { rowId: rowId(), decisionId, component: '', amount: '' }
}

export function emptyDocLine(): DocLineDraft {
  return { rowId: rowId(), sourceLineRef: '', net: '', predecessorLineId: '', taxes: [emptyTax()] }
}

/** What is wrong with the lines, or null. */
export function linesProblem(lines: readonly DocLineDraft[], needPredecessor: boolean): string | null {
  if (lines.length === 0) {
    return 'Add at least one line.'
  }
  for (const [i, line] of lines.entries()) {
    const n = i + 1
    if (!isDecimal(line.net.trim())) {
      return `Line ${n}: the net must be a decimal such as 100.00.`
    }
    if (needPredecessor && line.predecessorLineId === '') {
      return `Line ${n}: a rebill line must name the original line it replaces.`
    }
    for (const [j, tax] of line.taxes.entries()) {
      if (tax.decisionId.trim() === '' || tax.component.trim() === '') {
        return `Line ${n}, tax ${j + 1}: a decision id and a component are required.`
      }
      if (!isDecimal(tax.amount.trim())) {
        return `Line ${n}, tax ${j + 1}: the amount must be a decimal exactly as decided, e.g. 21.00.`
      }
    }
  }
  return null
}

/** The request lines. Call only once linesProblem is null. */
export function lineRequests(lines: readonly DocLineDraft[]): S<'DocumentLineRequest'>[] {
  return lines.map((line) => ({
    ...(line.sourceLineRef.trim() === '' ? {} : { sourceLineRef: line.sourceLineRef.trim() }),
    ...(line.predecessorLineId === '' ? {} : { predecessorLineId: line.predecessorLineId }),
    net: line.net.trim(),
    taxes: line.taxes.map((t) => ({ decisionId: t.decisionId.trim(), component: t.component.trim(), amount: t.amount.trim() })),
  }))
}

export const LinesEditor = ({ lines, onChange, currency, predecessors }: {
  lines: readonly DocLineDraft[]
  onChange: (lines: DocLineDraft[]) => void
  /** The document's currency, to warn when a decision's differs. */
  currency: string
  /** For a rebill: the original's lines a new line may replace. */
  predecessors?: readonly S<'DocumentLine'>[]
}) => {
  const set = (i: number, line: DocLineDraft) => { onChange(lines.map((l, j) => (j === i ? line : l))) }
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
      {lines.map((line, i) => (
        <LineEditor
          key={line.rowId}
          index={i}
          line={line}
          currency={currency}
          predecessors={predecessors}
          onChange={(l) => { set(i, l) }}
          onRemove={lines.length === 1 ? undefined : () => { onChange(lines.filter((_, j) => j !== i)) }}
        />
      ))}
      <div className="ztax-form-actions">
        <button type="button" className="ztax-btn-quiet" onClick={() => { onChange([...lines, emptyDocLine()]) }}>
          Add line
        </button>
      </div>
    </div>
  )
}

const LineEditor = ({ index, line, currency, predecessors, onChange, onRemove }: {
  index: number
  line: DocLineDraft
  currency: string
  predecessors: readonly S<'DocumentLine'>[] | undefined
  onChange: (line: DocLineDraft) => void
  onRemove: (() => void) | undefined
}) => {
  const n = index + 1
  const [fillFrom, setFillFrom] = useState('')
  const [filling, setFilling] = useState(false)
  const [fillError, setFillError] = useState<unknown>(null)
  const [fillNote, setFillNote] = useState<string | null>(null)

  const setTax = (j: number, tax: TaxDraft) => {
    onChange({ ...line, taxes: line.taxes.map((t, k) => (k === j ? tax : t)) })
  }

  const fill = () => {
    const id = fillFrom.trim()
    if (id === '') {
      return
    }
    setFilling(true)
    setFillError(null)
    setFillNote(null)
    void ztax.getDecision(id).then((r) => {
      setFilling(false)
      if (!r.ok) {
        setFillError(r.error)
        return
      }
      const money = Object.entries(r.data.emitted).filter(([, v]) => v.type === 'MONEY' && v.amount !== undefined)
      if (money.length === 0) {
        setFillNote('That decision emitted no money components.')
        return
      }
      // One row per emitted MONEY component, its amount copied as decided.
      const taxes: TaxDraft[] = money.map(([component, v]) => ({
        rowId: rowId(),
        decisionId: r.data.id,
        component,
        amount: v.amount ?? '',
      }))
      const others = money.filter(([, v]) => v.currency !== currency).map(([c, v]) => `${c} (${v.currency ?? '?'})`)
      setFillNote(
        `Filled ${taxes.length} tax ${taxes.length === 1 ? 'row' : 'rows'} from ${r.data.businessKey}.` +
          (others.length === 0 ? '' : ` Note: ${others.join(', ')} not in the document currency ${currency}.`),
      )
      onChange({ ...line, taxes })
    })
  }

  return (
    <fieldset className="ztax-line-box" style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
      <legend>Line {n}</legend>
      <div className="ztax-form">
        <Field label="Source line ref (optional)" help="The billing or ERP line, e.g. bss:order-9/line-1.">
          <input value={line.sourceLineRef} onChange={(e) => { onChange({ ...line, sourceLineRef: e.target.value }) }} autoComplete="off" />
        </Field>
        <Field label="Net">
          <input inputMode="decimal" value={line.net} onChange={(e) => { onChange({ ...line, net: e.target.value }) }} placeholder="100.00" autoComplete="off" />
        </Field>
        {predecessors === undefined ? null : (
          <Field label="Replaces original line">
            <select value={line.predecessorLineId} onChange={(e) => { onChange({ ...line, predecessorLineId: e.target.value }) }}>
              <option value="">— choose —</option>
              {predecessors.map((p) => (
                <option key={p.id} value={p.id}>{p.sourceLineRef ?? p.id} · net {p.net}</option>
              ))}
            </select>
          </Field>
        )}
        {onRemove === undefined ? null : (
          <div className="ztax-form-actions">
            <button type="button" className="ztax-btn-quiet" onClick={onRemove}>Remove line {n}</button>
          </div>
        )}
      </div>

      <div className="ztax-form">
        <Field label={`Fill line ${n} taxes from decision`} help="Reads the decision and copies each emitted money component.">
          <input className="mono" value={fillFrom} onChange={(e) => { setFillFrom(e.target.value) }} placeholder="decision id" autoComplete="off" />
        </Field>
        <div className="ztax-form-actions">
          <button type="button" className="ztax-btn-quiet" onClick={fill} disabled={filling || fillFrom.trim() === ''}>
            {filling ? 'Reading…' : 'Fill taxes from decision'}
          </button>
        </div>
      </div>
      {fillError === null ? null : <ErrorNote error={fillError} />}
      {fillNote === null ? null : <p className="muted ztax-note" role="status">{fillNote}</p>}

      <table className="table">
        <caption className="table__caption">Taxes on line {n} — each exactly as its decision states it</caption>
        <thead>
          <tr>
            <th scope="col">Decision id</th>
            <th scope="col">Component</th>
            <th scope="col">Amount ({currency})</th>
            <th scope="col"><span className="muted">Remove</span></th>
          </tr>
        </thead>
        <tbody>
          {line.taxes.map((tax, j) => (
            <tr key={tax.rowId}>
              <td>
                <input
                  className="mono"
                  aria-label={`Line ${n} tax ${j + 1} decision id`}
                  value={tax.decisionId}
                  onChange={(e) => { setTax(j, { ...tax, decisionId: e.target.value }) }}
                  autoComplete="off"
                />
              </td>
              <td>
                <input
                  className="mono"
                  aria-label={`Line ${n} tax ${j + 1} component`}
                  value={tax.component}
                  onChange={(e) => { setTax(j, { ...tax, component: e.target.value }) }}
                  placeholder="TAX_VAT"
                  autoComplete="off"
                />
              </td>
              <td>
                <input
                  inputMode="decimal"
                  aria-label={`Line ${n} tax ${j + 1} amount`}
                  value={tax.amount}
                  onChange={(e) => { setTax(j, { ...tax, amount: e.target.value }) }}
                  placeholder="21.00"
                  autoComplete="off"
                />
              </td>
              <td>
                <button
                  type="button"
                  className="ztax-btn-quiet"
                  aria-label={`Remove line ${n} tax ${j + 1}`}
                  onClick={() => { onChange({ ...line, taxes: line.taxes.filter((_, k) => k !== j) }) }}
                >
                  ×
                </button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      <div className="ztax-form-actions">
        <button
          type="button"
          className="ztax-btn-quiet"
          onClick={() => { onChange({ ...line, taxes: [...line.taxes, emptyTax(line.taxes[0]?.decisionId ?? '')] }) }}
        >
          Add tax row
        </button>
      </div>
    </fieldset>
  )
}
