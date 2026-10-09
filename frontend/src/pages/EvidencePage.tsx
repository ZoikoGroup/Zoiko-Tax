import { useState } from 'react'

import { PageHeader } from '../ui/kit'
import { HoldsTab } from './evidence/HoldsTab'
import { InclusionTab } from './evidence/InclusionTab'
import { RetentionTab } from './evidence/RetentionTab'
import { SealsTab } from './evidence/SealsTab'
import './evidence/pages.css'
import type { PageProps } from './types'

type Tab = 'seals' | 'inclusion' | 'holds' | 'retention'

const TABS: readonly { id: Tab; label: string }[] = [
  { id: 'seals', label: 'Seals' },
  { id: 'inclusion', label: 'Inclusion' },
  { id: 'holds', label: 'Legal holds' },
  { id: 'retention', label: 'Retention' },
]

export const EvidencePage = ({ session }: PageProps) => {
  const admin = session.user.roles.includes('ADMIN')
  const [tab, setTab] = useState<Tab>('seals')
  const [sealId, setSealId] = useState<string | null>(null)

  const openSeal = (id: string) => {
    setSealId(id)
    setTab('seals')
  }

  return (
    <div className="ztax-page">
      <PageHeader
        title="Evidence"
        subtitle={<span>Period seals, inclusion proofs, legal holds and retention</span>}
      />

      <div className="tabs" role="tablist" aria-label="Evidence">
        {TABS.map((t) => (
          <button
            key={t.id}
            type="button"
            role="tab"
            id={`evidence-tab-${t.id}`}
            aria-selected={tab === t.id}
            aria-controls={`evidence-panel-${t.id}`}
            className={tab === t.id ? 'tab tab--selected' : 'tab'}
            onClick={() => { setTab(t.id) }}
          >
            {t.label}
          </button>
        ))}
      </div>

      <div role="tabpanel" id={`evidence-panel-${tab}`} aria-labelledby={`evidence-tab-${tab}`} className="ztax-page">
        {tab === 'seals' ? <SealsTab admin={admin} selected={sealId} onSelect={setSealId} /> : null}
        {tab === 'inclusion' ? <InclusionTab onOpenSeal={openSeal} /> : null}
        {tab === 'holds' ? <HoldsTab admin={admin} /> : null}
        {tab === 'retention' ? <RetentionTab admin={admin} /> : null}
      </div>
    </div>
  )
}
