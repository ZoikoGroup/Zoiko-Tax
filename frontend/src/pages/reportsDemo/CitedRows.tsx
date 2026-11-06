/**
 * CitedRows: shows the deterministic register rows whose IDs were cited by the
 * AI explanation.  Rendered only when the outcome is EXPLAINED and citedIds is
 * non-empty.  Uses a native <details> so the user can expand or collapse the
 * block without any JS state.
 *
 * Hard rules (ADR-0019):
 *  C1 — no parseFloat / parseInt / Number() / toFixed / arithmetic.
 *       String concatenation of an amount and its currency is allowed.
 *  C4 — no fetch / XMLHttpRequest.
 *  jsx-a11y — native elements, th scope, keyboard-operable via <details>.
 */
import type { Figure, Obligation, Payment, ShadowRow } from './model'

export type RegisterRow = Figure | Obligation | Payment | ShadowRow

/** Discriminate the union by a field that is unique to each concrete type. */
function rowFields(row: RegisterRow): Array<[string, string]> {
  if ('caseState' in row) {
    // ShadowRow
    return [
      ['ID', row.id],
      ['Obligation', row.obligationId],
      ['Comparator value', row.comparatorValue + ' ' + row.currency],
      ['Zoiko value', row.zoikoValue + ' ' + row.currency],
      ['Absolute variance', row.absoluteVariance + ' ' + row.currency],
      ['Result', row.result],
      ['Case state', row.caseState],
    ]
  }
  if ('date' in row) {
    // Payment
    return [
      ['ID', row.id],
      ['Obligation', row.obligationId],
      ['Amount', row.amount + ' ' + row.currency],
      ['Date', row.date],
      ['Status', row.status],
    ]
  }
  if ('jurisdiction' in row) {
    // Obligation
    const fields: Array<[string, string]> = [
      ['ID', row.id],
      ['Type', row.type],
      ['Jurisdiction', row.jurisdiction],
      ['Authority', row.authority],
      ['Period', row.period],
      ['Status', row.status],
      ['Due date', row.dueDate],
      ['Amount', row.amount + ' ' + row.currency],
      ['Amount USD', row.amountUsd],
    ]
    if (row.fxRate !== undefined) {
      fields.push(['FX rate', row.fxRate])
    }
    return fields
  }
  // Figure (fallback — all string fields shown)
  const fields: Array<[string, string]> = [
    ['ID', row.id],
    ['Label', row.label],
    ['Value', row.value + ' ' + row.unit],
    ['Basis', row.basis],
    ['Authority', row.authority ?? '—'],
    ['As of', row.asOf ?? '—'],
  ]
  return fields
}

interface CitedRowsProps {
  citedIds: readonly string[]
  resolveRow: (id: string) => RegisterRow | null
}

export const CitedRows = ({ citedIds, resolveRow }: CitedRowsProps) => (
  <details className="rd-cited-rows">
    <summary>Show the cited rows</summary>
    <p className="ztax-note rd-cited-rows-heading">
      These rows come from the deterministic register. The AI did not calculate them.
    </p>
    {citedIds.map((id) => {
      const row = resolveRow(id)
      return (
        <div key={id} className="rd-cited-row-block">
          <p className="mono rd-cited-row-id">{id}</p>
          {row === null ? (
            <p className="rd-muted">Row not found in this register snapshot.</p>
          ) : (
            <table className="ztax-action-table rd-cited-row-table">
              <tbody>
                {rowFields(row).map(([label, value]) => (
                  <tr key={label}>
                    <th scope="row">{label}</th>
                    <td className="mono">{value}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      )
    })}
  </details>
)

