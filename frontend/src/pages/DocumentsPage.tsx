import { useState, type FormEvent, type JSX } from 'react'

import type { PageProps } from './types'
import { Card, Field, PageHeader } from '../ui/kit'
import { TabBar } from './determine/shared'
import { DocumentView } from './documents/DocumentView'
import { IssueForm } from './documents/IssueForm'
import './determine.css'
import './documents.css'

type Tab = 'lookup' | 'issue'

const TABS: readonly { value: Tab; label: string }[] = [
  { value: 'lookup', label: 'Look up a document' },
  { value: 'issue', label: 'Issue a document' },
]

// Documents are not per period, so the page takes none of the shell's props.
export const DocumentsPage: (props: PageProps) => JSX.Element = () => {
  const [tab, setTab] = useState<Tab>('lookup')
  const [id, setId] = useState('')
  const [draft, setDraft] = useState('')

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
        Submission to tax authorities — CTC clearance, e-invoice transmission to a network or portal — is not built in the
        backend. Documents here are committed to ZoikoTax's own record only; nothing is sent to any authority.
      </p>
      <TabBar label="E-Invoicing" tabs={TABS} current={tab} onSelect={setTab} />

      <div className="ztax-tab-panel" hidden={tab !== 'lookup'}>
        <Card title="Look up a document">
          <form className="ztax-form" onSubmit={submit} aria-label="Look up a document">
            <Field label="Document id" help="The document's UUID, not its legal number. There is no list of documents.">
              <input className="mono" value={draft} onChange={(e) => { setDraft(e.target.value) }} autoComplete="off" />
            </Field>
            <div className="ztax-form-actions">
              <button type="submit" className="ztax-btn-copper" disabled={draft.trim() === ''}>Look up</button>
            </div>
          </form>
        </Card>
        {id === '' ? null : <DocumentView key={id} id={id} onOpen={open} />}
      </div>

      <div className="ztax-tab-panel" hidden={tab !== 'issue'}>
        <IssueForm onIssued={open} />
      </div>
    </div>
  )
}
