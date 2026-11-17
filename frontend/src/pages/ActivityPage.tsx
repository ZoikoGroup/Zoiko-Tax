import { useEffect, useState } from 'react'

import { instant } from '../dashboard/format'
import { api, type AuditRecord } from '../platform/api'
import { Card, Empty, Field, Id, PageHeader } from '../ui/kit'
import type { PageProps } from './types'
import { AdminErrorNote, countBy, downloadCsv, hasRole } from './workspace'
import './obligations.css'

// Recent Activity: the tenant's append-only audit trail (GET /v1/admin/audit),
// newest first. The cell serves it to ADMIN and AUDITOR only.

const FORBIDDEN = 'Recent activity is the tenant’s audit trail, which only the ADMIN and AUDITOR roles may read. Your roles do not include either.'

type Audit =
  | { readonly status: 'loading' }
  | { readonly status: 'ready'; readonly records: readonly AuditRecord[] }
  | { readonly status: 'error'; readonly error: unknown }

/** The audit trail, read through the administration client. */
export function useAudit(enabled: boolean): { audit: Audit; reload: () => void } {
  const [audit, setAudit] = useState<Audit>({ status: 'loading' })
  const [attempt, setAttempt] = useState(0)
  useEffect(() => {
    if (!enabled) {
      return
    }
    let current = true
    setAudit({ status: 'loading' })
    api.listAudit().then(
      (r) => { if (current) setAudit({ status: 'ready', records: r.records }) },
      (error: unknown) => { if (current) setAudit({ status: 'error', error }) },
    )
    return () => { current = false }
  }, [enabled, attempt])
  return { audit, reload: () => { setAttempt((n) => n + 1) } }
}

export const AUDIT_HEADER = ['Recorded at', 'Action', 'Subject type', 'Subject id', 'Actor', 'Detail'] as const

export const auditRow = (r: AuditRecord): string[] =>
  [r.recordedAt, r.action, r.subjectType, r.subjectId, r.actorUserId ?? '', r.detail]

/** The audit trail as a filterable table with a CSV download. */
export const AuditTrail = ({ canRead, title }: { canRead: boolean; title: string }) => {
  const { audit, reload } = useAudit(canRead)
  const [text, setText] = useState('')
  const [action, setAction] = useState('ALL')

  if (!canRead) {
    return <Card title={title}><p className="ztax-note" role="note">{FORBIDDEN}</p></Card>
  }

  const records = audit.status === 'ready' ? audit.records : []
  const needle = text.trim().toLowerCase()
  const shown = records.filter((r) =>
    (action === 'ALL' || r.action === action)
    && (needle === '' || auditRow(r).some((c) => c.toLowerCase().includes(needle))))
  const actions = countBy(records, (r) => r.action)

  return (
    <Card
      title={audit.status === 'ready' ? `${shown.length} of ${records.length} records` : title}
      aside={
        <div className="ztax-filter-bar">
          <Field label="Search">
            <input value={text} placeholder="action, subject, actor, detail" onChange={(e) => { setText(e.target.value) }} />
          </Field>
          <Field label="Action">
            <select value={action} onChange={(e) => { setAction(e.target.value) }}>
              <option value="ALL">All</option>
              {actions.map(([a, n]) => <option key={a} value={a}>{a} ({n})</option>)}
            </select>
          </Field>
          <button type="button" className="ztax-btn-quiet" onClick={reload}>Refresh</button>
          <button type="button" className="ztax-btn-quiet" disabled={shown.length === 0}
            onClick={() => { downloadCsv('audit-trail', AUDIT_HEADER, shown.map(auditRow)) }}>
            Download CSV
          </button>
        </div>
      }
    >
      {audit.status === 'loading' ? <p className="muted" aria-live="polite">Reading…</p>
        : audit.status === 'error' ? <AdminErrorNote error={audit.error} forbidden={FORBIDDEN} />
        : records.length === 0 ? <Empty>No administrative activity is recorded yet.</Empty>
        : shown.length === 0 ? <Empty>No record matches this filter.</Empty>
        : (
          <div style={{ overflowX: 'auto' }}>
            <table className="ztax-action-table">
              <thead>
                <tr><th>When</th><th>Action</th><th>Subject</th><th>Actor</th><th>Detail</th></tr>
              </thead>
              <tbody>
                {shown.map((r) => (
                  <tr key={r.id}>
                    <td style={{ paddingRight: 8, whiteSpace: 'nowrap' }}>{instant(r.recordedAt)}</td>
                    <td style={{ paddingRight: 8 }}><span className="mono">{r.action}</span></td>
                    <td style={{ paddingRight: 8 }}>{r.subjectType} <Id value={r.subjectId} /></td>
                    <td style={{ paddingRight: 8 }}>{r.actorUserId === undefined ? <span className="muted">system</span> : <Id value={r.actorUserId} />}</td>
                    <td className="mono" style={{ maxWidth: 420, overflowWrap: 'anywhere' }}>{r.detail}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      {audit.status === 'ready' ? (
        <p className="ztax-note" style={{ marginTop: 12 }}>
          The most recent {records.length} records. The trail is append-only: records are never changed or removed and carry no credentials.
        </p>
      ) : null}
    </Card>
  )
}

export const ActivityPage = ({ session }: PageProps) => (
  <div className="ztax-page">
    <PageHeader
      title="Recent Activity"
      subtitle="Every administrative action in this tenant, newest first: sign-ins, users, roles, sessions."
    />
    <AuditTrail canRead={hasRole(session.user.roles, 'ADMIN', 'AUDITOR')} title="Audit trail" />
  </div>
)
