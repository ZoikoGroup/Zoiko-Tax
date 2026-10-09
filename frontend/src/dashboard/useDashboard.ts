import { useCallback, useEffect, useState } from 'react'

import { ZoikoTaxError, type Dashboard } from '@zoikotax/sdk'

import { ztax } from '../platform/client'

/**
 * The Tax Command Center for one period.
 *
 * 'forbidden' is its own state rather than an error: a signed-in user whose
 * roles do not include a fiscal read (ADMIN alone, by design) should be told
 * which role it takes, not shown a failure.
 */
export type DashboardState =
  | { readonly status: 'loading' }
  | { readonly status: 'ready'; readonly data: Dashboard }
  | { readonly status: 'forbidden' }
  | { readonly status: 'error'; readonly detail: string; readonly retryable: boolean }

export function useDashboard(period: string): { state: DashboardState; reload: () => void } {
  const [state, setState] = useState<DashboardState>({ status: 'loading' })
  const [attempt, setAttempt] = useState(0)

  useEffect(() => {
    const controller = new AbortController()
    setState({ status: 'loading' })
    void ztax.getDashboard({ period }, controller.signal).then((result) => {
      if (controller.signal.aborted) {
        return
      }
      if (result.ok) {
        setState({ status: 'ready', data: result.data })
        return
      }
      const error = result.error
      if (error instanceof ZoikoTaxError && error.reasonCode === 'FORBIDDEN') {
        setState({ status: 'forbidden' })
        return
      }
      setState({
        status: 'error',
        detail: error instanceof ZoikoTaxError ? `${error.reasonCode}: ${error.message}` : error.message,
        retryable: error instanceof ZoikoTaxError ? error.retryable : true,
      })
    })
    return () => { controller.abort() }
  }, [period, attempt])

  const reload = useCallback(() => { setAttempt((n) => n + 1) }, [])
  return { state, reload }
}
