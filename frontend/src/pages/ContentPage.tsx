import { useEffect, useState } from 'react'

import { PageHeader } from '../ui/kit'
import { AuthorizationsCard, CheckForm, GrantForm, type CheckDraft } from './content/Authorizations'
import { CodesTab, ReleasesTab, SourcesTab } from './content/CatalogueTabs'
import { CapabilitiesCard, MatrixCard } from './content/GoverningCards'
import { TabBar } from './determine/shared'
import './evidence/pages.css'
import { useRoutedTab } from './routedTab'
import type { PageProps } from './types'

type Tab = 'packs' | 'codes' | 'sources' | 'releases'

const TABS: readonly { value: Tab; label: string }[] = [
  { value: 'packs', label: 'Rule packs' },
  { value: 'codes', label: 'Service & levy codes' },
  { value: 'sources', label: 'Legal sources' },
  { value: 'releases', label: 'Content releases' },
]

export const ContentPage = ({ session, period, route, onTabChange }: PageProps) => {
  const admin = session.user.roles.includes('ADMIN')
  const [tab, setTab] = useRoutedTab(TABS.map((t) => t.value), 'packs', route, onTabChange)
  const [reloadKey, setReloadKey] = useState(0)
  const [check, setCheck] = useState<CheckDraft>({
    country: '',
    authority: '',
    service: 'FILE',
    period,
    legalEntityId: '',
    matter: '',
  })

  useEffect(() => { setCheck((c) => ({ ...c, period })) }, [period])

  const reload = () => { setReloadKey((n) => n + 1) }

  return (
    <div className="ztax-page">
      <PageHeader
        title="Content"
        subtitle={<span>What governs this cell: its rule content, the legal matrix and the customer's authorizations</span>}
      />
      <TabBar label="Content" tabs={TABS} current={tab} onSelect={setTab} />
      {tab === 'packs' ? (
        <>
          <CapabilitiesCard />
          <MatrixCard
            onCheck={(rule) => {
              setCheck((c) => ({ ...c, country: rule.country, authority: rule.authority, service: rule.service }))
              document.getElementById('content-check')?.scrollIntoView({ behavior: 'smooth', block: 'start' })
            }}
          />
          <div id="content-check">
            <CheckForm draft={check} onChange={setCheck} />
          </div>
          <AuthorizationsCard admin={admin} reloadKey={reloadKey} onReload={reload} />
          <GrantForm admin={admin} onGranted={reload} />
        </>
      ) : null}
      {tab === 'codes' ? <CodesTab /> : null}
      {tab === 'sources' ? <SourcesTab /> : null}
      {tab === 'releases' ? <ReleasesTab /> : null}
    </div>
  )
}
