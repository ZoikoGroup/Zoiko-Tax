import { useState, type FormEvent } from 'react'

import { ZoikoTaxError, type Decision, type S } from '@zoikotax/sdk'

import { instant } from '../../dashboard/format'
import { ztax } from '../../platform/client'
import { Card, ErrorNote, Field, Raw, Status, Submit, useAction } from '../../ui/kit'
import { EmittedTable } from '../determine/shared'

type Component = S<'TaxComponentResult'>

const isNotFound = (error: unknown) => error instanceof ZoikoTaxError && error.status === 404

/**
 * Source provenance: where a decision came from. The content bundle and its
 * digest, the rule IR and profile versions, the digests of the input and the
 * result, and every legal source and citation the components it applied
 * record. All of it is read from the decision as recorded.
 */
export const ProvenanceTab = () => {
  const [decisionId, setDecisionId] = useState('')
  const [asked, setAsked] = useState('')
  const lookup = useAction<Decision>()

  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    const id = decisionId.trim()
    setAsked(id)
    void lookup.run(() => ztax.getDecision(id))
  }

  return (
    <>
      <Card title="Trace a decision to its sources">
        <p className="muted zp-intro">
          A decision records the content bundle it was made under, the versions that bundle carried, and for each
          component it applied the legal source and citation it rests on.
        </p>
        <form className="ztax-form" onSubmit={onSubmit} aria-label="Find a decision's provenance">
          <Field label="Decision id">
            <input
              className="mono"
              value={decisionId}
              required
              placeholder="01920a4b-7c3e-7d21-9f40-3c1a2b4d5e6f"
              onChange={(e) => { setDecisionId(e.target.value) }}
            />
          </Field>
          <div className="ztax-form-actions">
            <Submit busy={lookup.busy}>Show provenance</Submit>
          </div>
        </form>
        {lookup.error === null ? null : isNotFound(lookup.error) ? (
          <p className="zp-warn-note" role="status">
            No decision <span className="mono">{asked}</span> in this tenant.
          </p>
        ) : (
          <ErrorNote error={lookup.error} />
        )}
      </Card>
      {lookup.done === null || lookup.error !== null ? null : <ProvenanceView d={lookup.done} />}
    </>
  )
}

/** Every component the decision applied, across its items, once each. */
function componentsOf(d: Decision): Component[] {
  const seen = new Map<string, Component>()
  for (const item of d.document?.items ?? []) {
    for (const c of item.components ?? []) {
      const key = `${c.id}@${c.version}`
      if (!seen.has(key)) {
        seen.set(key, c)
      }
    }
  }
  return [...seen.values()]
}

const ProvenanceView = ({ d }: { d: Decision }) => {
  const components = componentsOf(d)
  const doc = d.document
  return (
    <>
      <Card title="Provenance" aside={<Status value={d.outcome} />}>
        <dl className="ztax-kv">
          <dt>Decision</dt>
          <dd className="mono">{d.id}</dd>
          <dt>Business key</dt>
          <dd className="mono">{d.businessKey}</dd>
          {d.supersedes === undefined ? null : (
            <>
              <dt>Supersedes</dt>
              <dd className="mono">{d.supersedes}</dd>
            </>
          )}
          <dt>Authoritative</dt>
          <dd>{d.authoritative ? <Status value="ACTIVE" /> : <span className="ztax-status ztax-status--warn">ADVISORY</span>}</dd>
          <dt>Reason code</dt>
          <dd className="mono">{d.reasonCode}</dd>
          <dt>Event time</dt>
          <dd>{instant(d.eventTime)}</dd>
          <dt>Recorded</dt>
          <dd>{instant(d.recordedAt)}</dd>
          <dt>Content bundle</dt>
          <dd className="mono">{d.bundle.bundleId}</dd>
          <dt>Bundle digest</dt>
          <dd className="mono" style={{ wordBreak: 'break-all' }}>{d.bundle.digest}</dd>
          <dt>Rule IR version</dt>
          <dd>{d.bundle.irVersion}</dd>
          {doc === undefined ? null : (
            <>
              <dt>Profile version</dt>
              <dd className="mono">{doc.profileVersion}</dd>
              <dt>Graph version</dt>
              <dd className="mono">{doc.graphVersion}</dd>
            </>
          )}
          <dt>Input digest</dt>
          <dd className="mono" style={{ wordBreak: 'break-all' }}>{d.digests.input}</dd>
          <dt>Envelope digest</dt>
          <dd className="mono" style={{ wordBreak: 'break-all' }}>{d.digests.envelope}</dd>
          <dt>Result digest</dt>
          <dd className="mono" style={{ wordBreak: 'break-all' }}>{d.digests.result}</dd>
        </dl>
      </Card>

      <Card title="Legal sources">
        {components.length === 0 ? (
          <p className="ztax-empty-note">This decision applied no tax component, so it cites no source.</p>
        ) : (
          <div className="zp-scroll">
            <table className="table">
              <caption className="table__caption">Each component applied, with the source and citation the content records</caption>
              <thead>
                <tr>
                  <th scope="col">Component</th>
                  <th scope="col">Version</th>
                  <th scope="col">Jurisdiction</th>
                  <th scope="col">Authority</th>
                  <th scope="col">Tax type</th>
                  <th scope="col">Legal reason</th>
                  <th scope="col">Source</th>
                  <th scope="col">Citation</th>
                </tr>
              </thead>
              <tbody>
                {components.map((c) => (
                  <tr key={`${c.id}@${c.version}`}>
                    <td><span className="mono">{c.id}</span><div className="muted">{c.name}</div></td>
                    <td className="mono">{c.version}</td>
                    <td className="mono">{c.jurisdiction}</td>
                    <td className="mono">{c.authority}</td>
                    <td className="mono">{c.taxType}</td>
                    <td className="mono">{c.legalReason ?? '—'}</td>
                    <td className="mono">{c.source}</td>
                    <td>{c.citation}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      {doc === undefined ? null : (
        <Card title="Inputs and rules applied">
          <div className="zp-scroll">
            <table className="table">
              <caption className="table__caption">Per line: what the decision recorded about place of supply and regime</caption>
              <thead>
                <tr>
                  <th scope="col">Item</th>
                  <th scope="col">Service</th>
                  <th scope="col">State</th>
                  <th scope="col">Situs rule</th>
                  <th scope="col">Place of supply</th>
                  <th scope="col">Regime</th>
                  <th scope="col">Customer role</th>
                </tr>
              </thead>
              <tbody>
                {doc.items.map((it) => (
                  <tr key={it.key}>
                    <td className="mono">{it.line}{it.part === undefined ? '' : `/${it.part}`}</td>
                    <td className="mono">{it.service ?? '—'}</td>
                    <td><Status value={it.state} /></td>
                    <td className="mono">{it.situsRule ?? it.situsOutcome ?? '—'}</td>
                    <td className="mono">{it.placeOfSupply ?? '—'}</td>
                    <td className="mono">{it.regime ?? '—'}</td>
                    <td className="mono">{it.customerRole ?? '—'}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {doc.trace.length === 0 ? null : (
            <details className="ztax-raw">
              <summary>Evaluation trace ({doc.trace.length} steps)</summary>
              <ol className="zp-list">
                {doc.trace.map((s, i) => (
                  <li key={i}>
                    <span className="mono">{s.step}</span>
                    {s.item === undefined ? '' : ` · ${s.item}`}
                    {s.component === undefined ? '' : ` · ${s.component}`} — {s.detail}
                  </li>
                ))}
              </ol>
            </details>
          )}
        </Card>
      )}

      <Card title="Emitted">
        <EmittedTable emitted={d.emitted} caption="The values this decision emitted" />
        <Raw value={d} />
      </Card>
    </>
  )
}
