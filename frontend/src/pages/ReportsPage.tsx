import { useState } from 'react'

import { civilDate, instant } from '../dashboard/format'
import { ztax } from '../platform/client'
import { Card, Empty, Field, Loaded, PageHeader, Status, useLoad, type Load } from '../ui/kit'
import './obligations.css'

// Reports: read-only registers over what the cell recorded, each
// downloadable as CSV. Every figure is the cell's own canonical string; the
// browser counts rows and computes no amount (ADR-0019 C1).

type Row = readonly string[]
interface Report {
  readonly title: string
  readonly note: string
  readonly header: Row
  readonly rows: readonly Row[]
}

const REPORTS = ['obligations', 'filings', 'payments', 'shadow', 'migration'] as const
type ReportId = (typeof REPORTS)[number]
const TITLES: Record<ReportId, string> = {
  obligations: 'Obligations register', filings: 'Filing status', payments: 'Payments register',
  shadow: 'Shadow assurance runs', migration: 'Migration programs',
}
const isReport = (v: string): v is ReportId => (REPORTS as readonly string[]).includes(v)

const csvCell = (s: string) => (/[",\n]/.test(s) ? `"${s.replaceAll('"', '""')}"` : s)

function download(r: Report) {
  const text = [r.header, ...r.rows].map((row) => row.map(csvCell).join(',')).join('\n') + '\n'
  const url = URL.createObjectURL(new Blob([text], { type: 'text/csv;charset=utf-8' }))
  const a = document.createElement('a')
  a.href = url
  a.download = `${r.title.toLowerCase().replaceAll(' ', '-')}-${new Date().toISOString().slice(0, 10)}.csv`
  a.click()
  URL.revokeObjectURL(url)
}

/** Counts of a column's values, for the summary line. */
function tally(rows: readonly Row[], column: number): string {
  const counts = new Map<string, number>()
  for (const r of rows) {
    const k = r[column] ?? ''
    counts.set(k, (counts.get(k) ?? 0) + 1)
  }
  return [...counts.entries()].map(([k, n]) => `${k.replaceAll('_', ' ')} ${n}`).join(' · ')
}

export const ReportsPage = () => {
  const [which, setWhich] = useState<ReportId>('obligations')
  return (
    <div className="ztax-page">
      <PageHeader title="Reports" subtitle="Read-only registers of what the cell recorded. Figures are shown exactly as recorded; download any as CSV." />
      <Card
        title={TITLES[which]}
        aside={
          <div className="ztax-filter-bar">
            <Field label="Report">
              <select value={which} onChange={(e) => { if (isReport(e.target.value)) setWhich(e.target.value) }}>
                {REPORTS.map((r) => <option key={r} value={r}>{TITLES[r]}</option>)}
              </select>
            </Field>
          </div>
        }
      >
        {which === 'obligations' ? <Obligations /> : which === 'filings' ? <Filings /> : which === 'payments' ? <Payments />
          : which === 'shadow' ? <Shadow /> : <Migration />}
      </Card>
    </div>
  )
}

function ReportView<T>({ load, build, statusColumn }: { load: Load<T>; build: (data: T) => Report; statusColumn: number }) {
  return (
    <Loaded load={load}>
      {(data) => {
        const r = build(data)
        return (
          <>
            <p className="ztax-note">
              {r.note} {r.rows.length} row(s){r.rows.length === 0 ? '' : ` · ${tally(r.rows, statusColumn)}`}.{' '}
              <button type="button" className="ztax-btn-quiet" disabled={r.rows.length === 0} onClick={() => { download(r) }}>Download CSV</button>
            </p>
            {r.rows.length === 0 ? <Empty>Nothing recorded yet.</Empty> : (
              <div style={{ overflowX: 'auto' }}>
                <table className="ztax-action-table">
                  <thead><tr>{r.header.map((h) => <th key={h}>{h}</th>)}</tr></thead>
                  <tbody>
                    {r.rows.map((row, i) => (
                      <tr key={i}>
                        {row.map((c, j) => (
                          <td key={j} style={{ paddingRight: 8 }} className={j === statusColumn ? undefined : 'mono'}>
                            {j === statusColumn ? <Status value={c} /> : c}
                          </td>
                        ))}
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </>
        )
      }}
    </Loaded>
  )
}

const Obligations = () => {
  const { load } = useLoad((signal) => ztax.listObligations({ limit: 500 }, signal), [])
  return <ReportView load={load} statusColumn={5} build={(d) => ({
    title: TITLES.obligations, note: 'Every obligation, by due date, with its status as of today in its legal calendar.',
    header: ['Type', 'Jurisdiction', 'Authority', 'Period start', 'Period end', 'Status', 'Due', 'Assessed', 'Currency'],
    rows: d.obligations.map((o) => [o.type, o.jurisdiction, o.authority, o.periodStart, o.periodEnd, o.effectiveStatus,
      o.dueDate, o.assessedAmount ?? '', o.currency ?? '']),
  })} />
}

const Filings = () => {
  const { load } = useLoad((signal) => ztax.listCompliancePackages({ limit: 500 }, signal), [])
  return <ReportView load={load} statusColumn={3} build={(d) => ({
    title: TITLES.filings, note: 'Every compliance package, in queue order. Statutory and internal deadline breaches are separate columns.',
    header: ['Return', 'Period start', 'Period end', 'Status', 'Priority', 'Authority', 'Statutory deadline', 'Statutory missed',
      'Internal missed', 'Authority reference'],
    rows: d.packages.map((p) => {
      const ack = [...p.history].reverse().find((s) => s.authorityReference !== undefined)
      return [p.obligationType, p.periodStart, p.periodEnd, p.status, p.priority, `${p.filingCountry} ${p.filingAuthority}`,
        p.statutoryDue, p.overdue.statutory ? 'yes' : 'no', p.overdue.internal ? 'yes' : 'no', ack?.authorityReference ?? '']
    }),
  })} />
}

const Payments = () => {
  const { load } = useLoad((signal) => ztax.listRemittances({ limit: 500 }, signal), [])
  return <ReportView load={load} statusColumn={4} build={(d) => ({
    title: TITLES.payments, note: 'Every payment instruction. ZoikoTax instructs the customer’s own provider and holds no funds.',
    header: ['Period start', 'Period end', 'Amount', 'Currency', 'Status', 'Payee', 'Reference', 'Due', 'Overdue', 'Settlement reference'],
    rows: d.remittances.map((r) => {
      const settled = [...r.history].reverse().find((s) => s.status === 'SETTLED')
      return [r.periodStart, r.periodEnd, r.amount, r.currency, r.status, r.beneficiary.legalName, r.paymentReference, r.paymentDue,
        r.overdue ? 'yes' : 'no', settled?.providerReference ?? '']
    }),
  })} />
}

const Shadow = () => {
  const { load } = useLoad((signal) => ztax.listShadowRuns({ limit: 200 }, signal), [])
  return <ReportView load={load} statusColumn={2} build={(d) => ({
    title: TITLES.shadow, note: 'Every comparison run. A match against a comparator claims only what the comparator supports.',
    header: ['Run at', 'Comparator', 'Claim', 'Cases', 'Paired', 'Exact', 'Equivalent', 'Differing', 'Unpaired', 'Material',
      'Absolute variance', 'Currency', 'Blockers'],
    rows: d.runs.map((r) => [instant(r.createdAt), r.comparator.name, r.comparator.claim, String(r.metrics.cases), String(r.metrics.paired),
      String(r.metrics.exact), String(r.metrics.equivalent), String(r.metrics.differing), String(r.metrics.population),
      String(r.metrics.material), r.metrics.absoluteVariance, r.currency, String(r.cutoverBlockers.length)]),
  })} />
}

const Migration = () => {
  const { load } = useLoad((signal) => ztax.listMigrationPrograms(signal), [])
  return <ReportView load={load} statusColumn={1} build={(d) => ({
    title: TITLES.migration, note: 'Every migration program. Open a program under Migration for its runs, reconciliation and cutover.',
    header: ['Name', 'Type', 'Period from', 'Period to', 'Scale', 'Created'],
    rows: d.programs.map((p) => [p.name, p.type, civilDate(p.periodFrom), civilDate(p.periodTo), String(p.scale), instant(p.createdAt)]),
  })} />
}
