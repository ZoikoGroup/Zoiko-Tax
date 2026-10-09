import { useEffect, useState } from 'react'

import { PageHeader } from '../ui/kit'
import { AuthorizationsCard, CheckForm, GrantForm, type CheckDraft } from './content/Authorizations'
import { CapabilitiesCard, MatrixCard } from './content/GoverningCards'
import './evidence/pages.css'
import type { PageProps } from './types'

export const ContentPage = ({ session, period }: PageProps) => {
  const admin = session.user.roles.includes('ADMIN')
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
    </div>
  )
}
