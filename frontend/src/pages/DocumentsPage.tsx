import { useState, type FormEvent, type JSX } from 'react'

import { useRoutedTab } from './routedTab'
import type { PageProps } from './types'
import { Card, Field, PageHeader } from '../ui/kit'
import { TabBar } from './determine/shared'
import { DocumentView } from './documents/DocumentView'
import { IssueForm } from './documents/IssueForm'
import { CORRECTION_TYPES, RecentDocuments } from './recent'
import './determine.css'
import './documents.css'

type Tab = 'lookup' | 'issue' | 'corrections'

const TABS: readonly { value: Tab; label: string }[] = [
  { value: 'lookup', label: 'Look up a document' },
  { value: 'issue', label: 'Issue a document' },
  { value: 'corrections', label: 'Rejections & corrections' },
]

// Documents are not per period; the page takes only the route, for its tab.
export const DocumentsPage: (props: PageProps) => JSX.Element = ({ route, onTabChange }) => {
  const [tab, setTab] = useRoutedTab(TABS.map((t) => t.value), 'lookup', route, onTabChange)
  const [id, setId] = useState('')
  const [draft, setDraft] = useState('')
  // Bumped when a document is issued, so the lists read it back.
  const [issued, setIssued] = useState(0)

  const open = (documentId: string) => {
    setId(documentId)
    setDraft(documentId)
    setTab('lookup')
  }
  const submit = (e: FormEvent) => {
    e.preventDefault()
    setId(draft.trim())
  }

  return (
    <div className="ztax-page">
      <PageHeader title="E-Invoicing / CTC" subtitle={<span>Fiscal documents that present committed decisions</span>} />
      <p className="ztax-error-note" role="note">
        No tax authority, network or access point is connected. Documents here are committed to ZoikoTax's own record;
        a submission is prepared under E-Invoicing / CTC › Authority Submissions, where its dispatch and the authority's
        response are recorded by a person with evidence. Nothing is transmitted from this cell.
      </p>
      <TabBar label="E-Invoicing" tabs={TABS} current={tab} onSelect={setTab} />

      <div className="ztax-tab-panel" hidden={tab !== 'lookup'}>
        <Card title="Look up a document">
          <form className="ztax-form" onSubmit={submit} aria-label="Look up a document">
            <Field label="Document id" help="The document's UUID, not its legal number. Every committed document is listed below.">
              <input className="mono" value={draft} onChange={(e) => { setDraft(e.target.value) }} autoComplete="off" />
            </Field>
            <div className="ztax-form-actions">
              <button type="submit" className="ztax-btn-copper" disabled={draft.trim() === ''}>Look up</button>
            </div>
          </form>
        </Card>
        {id === '' ? null : <DocumentView key={id} id={id} onOpen={open} />}
        <RecentDocuments key={`archive-${issued}`} selected={id} onOpen={open} />
      </div>

      <div className="ztax-tab-panel" hidden={tab !== 'issue'}>
        <IssueForm onIssued={(documentId) => { setIssued((n) => n + 1); open(documentId) }} />
      </div>

      <div className="ztax-tab-panel" hidden={tab !== 'corrections'}>
        <RecentDocuments
          key={`corrections-${issued}`}
          title="Corrections, credits and cancellations"
          selected={id}
          onOpen={open}
          only={(d) => CORRECTION_TYPES.has(d.type) || d.status === 'VOIDED' || d.status === 'DISPUTED' || d.status === 'SUSPENDED'}
          empty="No document has been corrected, credited, voided or disputed."
        />
        <p className="muted ztax-note">
          This lists the corrections ZoikoTax itself recorded, and originals that were voided, disputed or suspended.
          A rejection an authority returned is recorded on its submission (status AUTHORITY_REJECTED) under Authority
          Submissions; the correction is then issued here.
        </p>
      </div>
    </div>
  )
}
