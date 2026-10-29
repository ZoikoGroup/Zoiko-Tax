/**
 * Reports (demo): a synthetic Reports & Executive Compliance Dashboard drawn
 * from the demo AI service's fixture (intelligence/tools/report_fixture.py),
 * with an "Explain" control that asks the same service for a plain-language,
 * guard-checked explanation of the rows behind a figure.
 *
 * DEMO ONLY. It is a separate page from ReportsPage; it shares no state or
 * data with it. If the demo service cannot be reached the page says so and
 * shows nothing else -- it never falls back to numbers of its own.
 */
import { useEffect, useState } from 'react'

import { loadFixture } from './reportsDemo/api'
import { Dashboard, DemoHeader } from './reportsDemo/Dashboard'
import type { Fixture } from './reportsDemo/model'

type Load =
  | { readonly status: 'loading' }
  | { readonly status: 'ready'; readonly fixture: Fixture }
  | { readonly status: 'unavailable' }

export const ReportsDemoPage = () => {
  const [load, setLoad] = useState<Load>({ status: 'loading' })

  useEffect(() => {
    const controller = new AbortController()
    void loadFixture(controller.signal).then((result) => {
      if (controller.signal.aborted) {
        return
      }
      setLoad(result.ok ? { status: 'ready', fixture: result.fixture } : { status: 'unavailable' })
    })
    return () => { controller.abort() }
  }, [])

  if (load.status === 'ready') {
    return <Dashboard fixture={load.fixture} />
  }
  return (
    <div className="ztax-page">
      <DemoHeader />
      {load.status === 'loading' ? (
        <p className="muted" aria-live="polite">Reading…</p>
      ) : (
        <p className="ztax-error-note" role="alert">
          The demo data service is unavailable.
          <span className="muted">
            {' '}Start the live-demo stack (intelligence/LIVE_DEMO.md). The service only answers a page served from
            http://localhost:13000.
          </span>
        </p>
      )}
    </div>
  )
}
