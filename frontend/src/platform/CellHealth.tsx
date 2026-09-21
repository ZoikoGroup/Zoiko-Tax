import { useEffect, useState } from 'react'
import type { CellHealth as Health } from './health'
import { readCellHealth } from './health'

/**
 * ADR-0019 C3: degraded, stale and unreachable render as themselves.
 *
 * There is no spinner in this component. "Checking" is a state with its own
 * words, not an animation standing in for one, and an unreachable cell says so
 * rather than spinning forever while the user waits for a resolution that needs
 * someone to act.
 */
const WORDING: Record<Health['state'], { title: string; meaning: string }> = {
  checking: { title: 'Checking', meaning: 'No answer from the cell yet.' },
  live: { title: 'Live', meaning: 'The cell answers liveness and readiness.' },
  degraded: {
    title: 'Degraded',
    meaning: 'The cell is running but is not ready to serve. Requests may fail.',
  },
  unreachable: {
    title: 'Unreachable',
    meaning: 'No response. This does not resolve on its own.',
  },
}

export function CellHealth() {
  const [health, setHealth] = useState<Health>({
    state: 'checking',
    detail: '',
    checkedAt: '',
  })

  useEffect(() => {
    let current = true
    const check = () => {
      void readCellHealth().then((next) => {
        if (current) {
          setHealth(next)
        }
      })
    }
    check()
    const timer = setInterval(check, 15_000)
    return () => {
      current = false
      clearInterval(timer)
    }
  }, [])

  const wording = WORDING[health.state]

  return (
    <section className="panel" aria-labelledby="cell-health-heading">
      <h2 id="cell-health-heading">Regional cell</h2>
      <p className={`state state--${health.state}`} role="status">
        <span className="state__title">{wording.title}</span>
        <span className="state__meaning">{wording.meaning}</span>
      </p>
      {health.detail !== '' && (
        <pre className="state__detail" aria-label="last response from the cell">
          {health.detail}
        </pre>
      )}
      {health.checkedAt !== '' && (
        <p className="state__checked">
          Checked <time dateTime={health.checkedAt}>{health.checkedAt}</time>
        </p>
      )}
    </section>
  )
}
