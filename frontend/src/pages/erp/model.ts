/**
 * Pieces of the ERP Reconciliation page: vocabularies, tones, and the parser
 * for the GL balances a person pastes from an ERP report.
 *
 * Every amount is the string the cell computed or the person typed. Nothing
 * here adds, compares or rounds one (ADR-0019 C1); a pasted amount is checked
 * for shape only, and the cell parses it exactly.
 */
import type { S } from '@zoikotax/sdk'

export type Bridge = S<'ERPBridge'>
export type Export = S<'ERPExport'>
export type ExportStatus = S<'ERPExportStatus'>
export type ResponseKind = S<'ERPResponseKind'>
export type Vendor = S<'ERPVendor'>
export type ControlAccount = S<'ControlAccount'>
export type Snapshot = S<'GLSnapshot'>
export type GLReconciliation = S<'GLReconciliation'>
export type GLItem = S<'GLReconciliationItem'>
export type BalanceLine = S<'GLBalanceLine'>

export const VENDORS: readonly { value: Vendor; label: string }[] = [
  { value: 'SAP', label: 'SAP' },
  { value: 'ORACLE', label: 'Oracle' },
  { value: 'NETSUITE', label: 'NetSuite' },
  { value: 'DYNAMICS', label: 'Microsoft Dynamics' },
  { value: 'OTHER', label: 'Other ERP' },
]

export const CONTROL_ACCOUNTS: readonly ControlAccount[] = [
  'TAX_COLLECTED_LIABILITY', 'TAX_ACCRUED_LIABILITY', 'TAX_RECOVERABLE', 'TAX_RECEIVABLE_CONTROL', 'TAX_CASH_CLEARING',
  'TAX_RETURN_CLEARING', 'TAX_REMITTANCE_CLEARING', 'TAX_ADJUSTMENT_CONTROL', 'FX_CONTROL', 'ROUNDING_CONTROL',
  'SUSPENSE_EXCEPTION', 'CUSTOMER_GL_BRIDGE',
]

export const EXPORT_STATUSES: readonly ExportStatus[] = [
  'PREPARED', 'TRANSMITTED', 'ACKNOWLEDGED', 'ERP_POSTED', 'REJECTED', 'OUTCOME_UNKNOWN', 'WITHDRAWN',
]

/** The ERP answers a person records, with what each one needs. */
export const RESPONSES: readonly { value: ResponseKind; label: string; help: string }[] = [
  { value: 'ACKNOWLEDGED', label: 'Transport receipt', help: 'The ERP endpoint received the batch. Names the ERP’s receipt or message reference. Not a posting.' },
  { value: 'POSTED', label: 'Posted to the GL', help: 'The ERP posted the journal. Names the ERP’s posted journal or document number.' },
  { value: 'REJECTED', label: 'Rejected by the ERP', help: 'Says why. Opens a correction task and holds the period’s GL reconciliation.' },
  { value: 'OUTCOME_UNKNOWN', label: 'Outcome unknown', help: 'Says why nobody knows — a timeout, say. The batch is not sent again until a status query is recorded.' },
  { value: 'STATUS_NOT_FOUND', label: 'Status query: not found', help: 'A status query by the batch’s external reference found nothing. Names the query’s reference; allows one more attempt.' },
]

export const isVendor = (v: string): v is Vendor => VENDORS.some((x) => x.value === v)
export const isExportStatus = (v: string): v is ExportStatus => (EXPORT_STATUSES as readonly string[]).includes(v)
export const isResponseKind = (v: string): v is ResponseKind => RESPONSES.some((x) => x.value === v)

export type Tone = 'ok' | 'warn' | 'bad' | 'neutral'

const TONES: Readonly<Record<string, Tone>> = {
  PREPARED: 'neutral', TRANSMITTED: 'warn', ACKNOWLEDGED: 'warn', ERP_POSTED: 'ok', REJECTED: 'bad',
  OUTCOME_UNKNOWN: 'bad', WITHDRAWN: 'neutral', RECONCILED: 'ok', VARIANCES: 'bad', HELD: 'warn',
  MATCHED: 'ok', TOLERANCE_MATCH: 'warn', RESOLVED: 'ok', EXPLAINED: 'ok', PENDING: 'warn',
}

/** The tone a transfer state, outcome or item status is shown in. */
export const toneOf = (value: string): Tone => TONES[value] ?? 'bad'

/** What a hold reason means, in a sentence. */
export const HOLDS: Readonly<Record<S<'GLHold'>['reason'], string>> = {
  UNEXPORTED_JOURNALS: 'posted in the subledger and in no live export batch',
  AWAITING_DISPATCH: 'in a prepared batch not yet dispatched',
  AWAITING_ERP_POSTING: 'sent or received, and not yet posted by the ERP',
  OUTCOME_UNKNOWN: 'in a batch whose ERP outcome is unknown',
  REJECTED_CORRECTION_OPEN: 'in a batch the ERP rejected, with its correction still open',
}

const GL_ACCOUNT = /^[A-Za-z0-9._:/-]{1,60}$/
const UNSIGNED = /^(0|[1-9][0-9]*)(\.[0-9]+)?$/

/**
 * Reads pasted GL balances: one account per line, `account, debits, credits`
 * (commas, tabs or semicolons). Blank lines are skipped. Returns the lines or
 * the first problem, by line number.
 */
export function parseBalances(text: string): { ok: true; lines: BalanceLine[] } | { ok: false; error: string } {
  const lines: BalanceLine[] = []
  const seen = new Set<string>()
  const rows = text.split(/\r?\n/)
  for (let i = 0; i < rows.length; i++) {
    const row = (rows[i] ?? '').trim()
    if (row === '') continue
    const cells = row.split(/[,;\t]/).map((c) => c.trim())
    const [glAccount = '', debits = '', credits = ''] = cells
    if (cells.length !== 3) return { ok: false, error: `Line ${i + 1}: write "account, debits, credits".` }
    if (!GL_ACCOUNT.test(glAccount)) return { ok: false, error: `Line ${i + 1}: "${glAccount}" is not a GL account code.` }
    if (seen.has(glAccount)) return { ok: false, error: `Line ${i + 1}: ${glAccount} is listed twice.` }
    if (!UNSIGNED.test(debits) || !UNSIGNED.test(credits)) {
      return { ok: false, error: `Line ${i + 1}: debits and credits are non-negative decimals such as 1250.00.` }
    }
    seen.add(glAccount)
    lines.push({ glAccount, debits, credits })
  }
  if (lines.length === 0) return { ok: false, error: 'Paste at least one GL account.' }
  return { ok: true, lines }
}

/** The dispatch attempts of a batch, oldest first. */
export const attemptsOf = (batch: Export): string[] =>
  batch.history.flatMap((e) => (e.kind === 'DISPATCHED' && e.attemptId !== undefined ? [e.attemptId] : []))

/** Whether a dispatch of the batch would make a new attempt, by its state. */
export function dispatchable(batch: Export): boolean {
  if (batch.status === 'PREPARED') return true
  const last = batch.history[batch.history.length - 1]
  return batch.status === 'OUTCOME_UNKNOWN' && last?.kind === 'STATUS_NOT_FOUND'
}
