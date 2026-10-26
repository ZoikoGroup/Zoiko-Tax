import { useState } from 'react'

import { ztax } from '../platform/client'
import { Loaded, PageHeader, useLoad } from '../ui/kit'
import { TabBar } from './determine/shared'
import { CountriesTab } from './locations/CountriesTab'
import { coverageInstant, today, type Service } from './locations/coverage'
import { OverviewTab } from './locations/OverviewTab'
import { TryLocationTab, type Preset } from './locations/TryLocationTab'
import { UsStatesTab } from './locations/UsStatesTab'
import './evidence/pages.css'
import './determine.css'
import './locations.css'
import './locations/overview.css'

type Tab = 'overview' | 'us' | 'countries' | 'try'

const TABS: readonly { value: Tab; label: string }[] = [
  { value: 'overview', label: 'Overview' },
  { value: 'us', label: 'United States map' },
  { value: 'countries', label: 'All countries' },
  { value: 'try', label: 'Try a location' },
]

/**
 * Where the active content determines tax: by country, and in the United
 * States by state. Everything here is read from GET /v1/coverage at the date
 * the person picks, so a rate change shows on the day it takes effect.
 */
export const LocationsPage = () => {
  const [tab, setTab] = useState<Tab>('overview')
  const [date, setDate] = useState(today)
  const [preset, setPreset] = useState<Preset | null>(null)
  const { load, reload } = useLoad((signal) => ztax.getCoverage({ at: coverageInstant(date) }, signal), [date])

  const tryPlace = (country: string, subdivision: string, service: Service) => {
    setPreset({ country, subdivision, service, nonce: Date.now() })
    setTab('try')
  }
  const tryState = (state: string, service: Service) => { tryPlace('US', `US-${state}`, service) }

  return (
    <div className="ztax-page">
      <PageHeader
        title="Location taxation"
        subtitle={<span>Every telecom tax, rate and rule, by the place it applies</span>}
        actions={
          <div className="loc-header-actions">
            <label className="ztax-field loc-inline">
              <span>Rates in force on</span>
              <input type="date" value={date} onChange={(e) => { if (e.target.value !== '') setDate(e.target.value) }} />
            </label>
            <button type="button" className="ztax-btn-quiet" onClick={reload}>Refresh</button>
          </div>
        }
      />
      <Loaded load={load}>
        {(coverage) => (
          <>
            <p className="loc-bundle muted">
              Bundle <span className="mono">{coverage.bundle.bundleId}</span> · profile{' '}
              <span className="mono">{coverage.profileVersion}</span> · graph{' '}
              <span className="mono">{coverage.graphVersion}</span>
            </p>
            <p className="zp-warn-note" role="note">
              <strong>Research content.</strong> Every rate cites its authority, but none has been through the
              qualified review a production pack needs. Decisions from it are advisory and must not be invoiced or
              filed from.
            </p>
            <TabBar label="Locations" tabs={TABS} current={tab} onSelect={setTab} />
            <div className="ztax-tab-panel" hidden={tab !== 'overview'}>
              <OverviewTab coverage={coverage} onTry={tryPlace} />
            </div>
            <div className="ztax-tab-panel" hidden={tab !== 'us'}>
              <UsStatesTab coverage={coverage} onTry={tryState} />
            </div>
            <div className="ztax-tab-panel" hidden={tab !== 'countries'}>
              <CountriesTab coverage={coverage} />
            </div>
            <div className="ztax-tab-panel" hidden={tab !== 'try'}>
              <TryLocationTab coverage={coverage} preset={preset} />
            </div>
          </>
        )}
      </Loaded>
    </div>
  )
}
