import { useState } from 'react'

import type { PageProps } from './types'
import { periodLabel } from '../dashboard/format'
import { PageHeader } from '../ui/kit'
import { BatchTab } from './determine/BatchTab'
import { DecisionTab } from './determine/DecisionTab'
import { QuoteCommitTab } from './determine/QuoteCommitTab'
import { RefundsTab } from './determine/RefundsTab'
import { SubledgerTab } from './determine/SubledgerTab'
import { TabBar } from './determine/shared'
import './determine.css'

type Tab = 'quote' | 'decision' | 'refunds' | 'subledger' | 'batch'

const TABS: readonly { value: Tab; label: string }[] = [
  { value: 'quote', label: 'Quote & commit' },
  { value: 'decision', label: 'Decision' },
  { value: 'refunds', label: 'Refunds' },
  { value: 'subledger', label: 'Subledger' },
  { value: 'batch', label: 'Batch' },
]

export const DeterminePage = ({ period }: PageProps) => {
  const [tab, setTab] = useState<Tab>('quote')
  const [decisionId, setDecisionId] = useState('')
  const [refundId, setRefundId] = useState('')

  const openDecision = (id: string) => {
    setDecisionId(id)
    setTab('decision')
  }
  const openRefund = (id: string) => {
    setRefundId(id)
    setTab('refunds')
  }

  return (
    <div className="ztax-page">
      <PageHeader
        title="Determine"
        subtitle={<span>Quote, commit and correct lines against the active content · subledger period {periodLabel(period)}</span>}
      />
      <TabBar label="Determination" tabs={TABS} current={tab} onSelect={setTab} />
      {/* The forms stay mounted while hidden, so a draft or a result survives a
          look at another tab. */}
      <div className="ztax-tab-panel" hidden={tab !== 'quote'}>
        <QuoteCommitTab onOpenDecision={openDecision} />
      </div>
      <div className="ztax-tab-panel" hidden={tab !== 'decision'}>
        <DecisionTab key={decisionId} id={decisionId} onOpen={setDecisionId} onOpenRefund={openRefund} />
      </div>
      <div className="ztax-tab-panel" hidden={tab !== 'refunds'}>
        <RefundsTab key={refundId} id={refundId} onOpen={setRefundId} onOpenDecision={openDecision} />
      </div>
      {tab === 'subledger' ? <SubledgerTab period={period} /> : null}
      <div className="ztax-tab-panel" hidden={tab !== 'batch'}>
        <BatchTab onOpenDecision={openDecision} />
      </div>
    </div>
  )
}
