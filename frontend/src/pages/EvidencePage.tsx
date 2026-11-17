import { useState } from 'react'

import { PageHeader } from '../ui/kit'
import { ExportsTab } from './evidence/ExportsTab'
import { HoldsTab } from './evidence/HoldsTab'
import { InclusionTab } from './evidence/InclusionTab'
import { ProvenanceTab } from './evidence/ProvenanceTab'
import { ReplayTab } from './evidence/ReplayTab'
import { RetentionTab } from './evidence/RetentionTab'
import { SealsTab } from './evidence/SealsTab'
import './evidence/pages.css'
import { useRoutedTab } from './routedTab'
import type { PageProps } from './types'

type Tab = 'seals' | 'inclusion' | 'provenance' | 'replay' | 'exports' | 'holds' | 'retention'

const TABS: readonly { id: Tab; label: string }[] = [
  { id: 'seals', label: 'Seals' },
  { id: 'inclusion', label: 'Inclusion' },
  { id: 'provenance', label: 'Provenance' },
  { id: 'replay', label: 'Replay' },
  { id: 'exports', label: 'Verified exports' },
  { id: 'holds', label: 'Legal holds' },
  { id: 'retention', label: 'Retention' },
]

export const EvidencePage = ({ session, route, onTabChange }: PageProps) => {
  const admin = session.user.roles.includes('ADMIN')
  const [tab, setTab] = useRoutedTab(TABS.map((t) => t.id), 'seals', route, onTabChange)
  const [sealId, setSealId] = useState<string | null>(null)

  const openSeal = (id: string) => {
    setSealId(id)
    setTab('seals')
  }

  return (
    <div className="ztax-page">
      <PageHeader
        title="Evidence"
        subtitle={<span>Period seals, inclusion proofs, provenance, replay, verified exports, legal holds and retention</span>}
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
        {tab === 'provenance' ? <ProvenanceTab /> : null}
        {tab === 'replay' ? <ReplayTab /> : null}
        {tab === 'exports' ? <ExportsTab /> : null}
        {tab === 'holds' ? <HoldsTab admin={admin} /> : null}
        {tab === 'retention' ? <RetentionTab admin={admin} /> : null}
      </div>
    </div>
  )
}
