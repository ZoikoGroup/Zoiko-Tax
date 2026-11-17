import { useEffect, useState } from 'react'

import { instant } from '../dashboard/format'
import { readProbes, type ProbeResult } from '../platform/health'
import { ztax } from '../platform/client'
import { Card, Columns, Id, Loaded, PageHeader, Status, useLoad } from '../ui/kit'
import { words } from './workspace'
import './obligations.css'

// System Health: the cell's liveness and readiness probes, as it answered
// them, and what it says it is (GET /v1/capabilities). ADR-0019 C3: an
// unreachable or not-ready cell renders as itself, not as a spinner.

type Probes = Awaited<ReturnType<typeof readProbes>>

const EVERY_MS = 15_000

const ProbeRow = ({ name, meaning, p }: { name: string; meaning: string; p: ProbeResult }) => (
  <tr>
    <td className="mono" style={{ paddingRight: 8 }}>{name}</td>
    <td style={{ paddingRight: 8 }}>{meaning}</td>
    <td style={{ paddingRight: 8 }}>
      {p.answered ? <Status value={p.ok ? 'SUCCEEDED' : 'FAILED'} /> : <Status value="MISSING" />}
    </td>
    <td className="mono" style={{ paddingRight: 8 }}>{p.answered ? `HTTP ${p.status}` : 'no response'}</td>
    <td className="mono" style={{ paddingRight: 8, whiteSpace: 'nowrap' }}>{p.ms} ms</td>
    <td className="mono" style={{ maxWidth: 360, overflowWrap: 'anywhere' }}>{p.answered ? p.body || '—' : p.detail}</td>
  </tr>
)

function verdict(p: Probes): { state: string; words: string } {
  if (!p.healthz.answered && !p.readyz.answered) {
    return { state: 'MISSING', words: 'Unreachable. No response from the cell; this does not resolve on its own.' }
  }
  const live = p.healthz.answered && p.healthz.ok
  const ready = p.readyz.answered && p.readyz.ok
  if (live && ready) {
    return { state: 'ACTIVE', words: 'Live and ready. The cell answers liveness and readiness.' }
  }
  if (live) {
    return { state: 'PARTIAL', words: 'Degraded. The cell is running but not ready to serve; requests may fail.' }
  }
  return { state: 'FAILED', words: 'Degraded. The cell does not report itself live.' }
}

export const SystemHealthPage = () => {
  const [probes, setProbes] = useState<Probes | null>(null)
  const [attempt, setAttempt] = useState(0)
  const caps = useLoad((signal) => ztax.getCapabilities(signal), [attempt])

  useEffect(() => {
    let current = true
    const check = () => {
      void readProbes().then((next) => {
        if (current) {
          setProbes(next)
        }
      })
    }
    check()
    const timer = setInterval(check, EVERY_MS)
    return () => {
      current = false
      clearInterval(timer)
    }
  }, [attempt])

  const v = probes === null ? null : verdict(probes)

  return (
    <div className="ztax-page">
      <PageHeader
        title="System Health"
        subtitle={probes === null ? 'Asking the cell…' : <span>Probes re-checked every 15 s · last {instant(probes.checkedAt)}</span>}
        actions={<button type="button" className="ztax-btn-quiet" onClick={() => { setAttempt((n) => n + 1) }}>Check now</button>}
      />

      <Card title="Regional cell" aside={v === null ? undefined : <Status value={v.state} />}>
        {probes === null || v === null ? <p className="muted" aria-live="polite">No answer from the cell yet.</p> : (
          <>
            <p className={v.state === 'ACTIVE' ? 'ztax-ok-note' : 'ztax-error-note'} role="status">{v.words}</p>
            <div style={{ overflowX: 'auto', marginTop: 12 }}>
              <table className="ztax-action-table">
                <thead>
                  <tr><th>Probe</th><th>Asks</th><th>Result</th><th>Answer</th><th>Took</th><th>Body</th></tr>
                </thead>
                <tbody>
                  <ProbeRow name="/healthz" meaning="Is the process alive?" p={probes.healthz} />
                  <ProbeRow name="/readyz" meaning="Can it serve: database, content, dependencies?" p={probes.readyz} />
                </tbody>
              </table>
            </div>
          </>
        )}
      </Card>

      <Loaded load={caps.load}>
        {(c) => (
          <>
            <Columns>
              <Card title="Deployment">
                <dl className="ztax-kv">
                  <dt>Environment</dt><dd className="mono">{c.environment}</dd>
                  <dt>Cell</dt><dd className="mono">{c.cell}</dd>
                  <dt>Region</dt><dd className="mono">{c.region}</dd>
                  <dt>Authoritative</dt>
                  <dd>
                    {c.authoritative ? <Status value="ACTIVE" /> : <Status value="ADVISORY" />}{' '}
                    <span className="muted">
                      {c.authoritative
                        ? 'This deployment may produce authoritative fiscal output.'
                        : 'Figures are advisory and must not be filed.'}
                    </span>
                  </dd>
                  <dt>Canonicalization</dt><dd className="mono">{c.canonProfile}</dd>
                  <dt>Content bundle</dt>
                  <dd>
                    {c.content === undefined ? <span className="muted">none loaded — determination is unavailable</span> : (
                      <><span className="mono">{c.content.bundleId}</span> · IR v{c.content.irVersion} · <Id value={c.content.digest} /></>
                    )}
                  </dd>
                </dl>
              </Card>
              <Card title="Release trains">
                <p className="ztax-note">Together these name the exact code, content, models, adapters, infrastructure, schemas and migrations serving you.</p>
                <table className="ztax-action-table">
                  <thead><tr><th>Train</th><th>Version</th></tr></thead>
                  <tbody>
                    {Object.entries(c.trains).map(([k, ver]) => (
                      <tr key={k}><td style={{ paddingRight: 8 }}>{k}</td><td className="mono">{ver}</td></tr>
                    ))}
                  </tbody>
                </table>
              </Card>
            </Columns>
            <Columns>
              <Card title={`Reason codes · ${c.reasonCodes.length}`}>
                <details className="ztax-raw">
                  <summary>Every reason code this deployment can emit</summary>
                  <p className="mono" style={{ lineHeight: 1.8 }}>{c.reasonCodes.join(' · ')}</p>
                </details>
              </Card>
              <Card title={`Event types · ${c.eventTypes.length}`}>
                <details className="ztax-raw">
                  <summary>Every event type a webhook may subscribe to</summary>
                  <p className="mono" style={{ lineHeight: 1.8 }}>{c.eventTypes.map(words).join(' · ')}</p>
                </details>
              </Card>
            </Columns>
          </>
        )}
      </Loaded>
    </div>
  )
}
