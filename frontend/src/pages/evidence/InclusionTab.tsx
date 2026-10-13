import { useState, type FormEvent } from 'react'

import { ZoikoTaxError, type S } from '@zoikotax/sdk'

import { instant } from '../../dashboard/format'
import { ztax } from '../../platform/client'
import { Card, ErrorNote, Field, Submit, useAction } from '../../ui/kit'

type Proof = S<'InclusionProof'>

const isNotFound = (error: unknown) => error instanceof ZoikoTaxError && error.status === 404

export const InclusionTab = ({ onOpenSeal }: { onOpenSeal: (sealId: string) => void }) => {
  const [decisionId, setDecisionId] = useState('')
  const [asked, setAsked] = useState('')
  const proof = useAction<Proof>()

  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    const id = decisionId.trim()
    setAsked(id)
    void proof.run(() => ztax.getDecisionInclusion(id))
  }

  const p = proof.done

  return (
    <>
      <Card title="Prove a decision is sealed">
        <p className="muted zp-intro">
          The RFC 6962 audit path from a decision's leaf to the root of the seal covering the instant it was recorded.
          With the seal's signed payload and the published keyring, anyone outside the cell can confirm the decision was
          sealed without seeing any other. Check the path against the <strong>signed</strong> root and leaf count, never
          against the sizes the proof itself states.
        </p>
        <form className="ztax-form" onSubmit={onSubmit} aria-label="Find a decision's inclusion proof">
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
            <Submit busy={proof.busy}>Find proof</Submit>
          </div>
        </form>
        {proof.error === null ? null : isNotFound(proof.error) ? (
          <p className="zp-warn-note" role="status">
            No proof for <span className="mono">{asked}</span>: the period it was recorded in has not been sealed yet (or
            no such decision exists in this tenant). Seal the period on the Seals tab, then ask again.
          </p>
        ) : (
          <ErrorNote error={proof.error} />
        )}
      </Card>

      {p === null || proof.error !== null ? null : (
        <Card
          title="Inclusion proof"
          aside={
            <button type="button" className="ztax-btn-quiet" onClick={() => { onOpenSeal(p.sealId) }}>
              Open its seal
            </button>
          }
        >
          <dl className="ztax-kv">
            <dt>Decision</dt>
            <dd className="mono">{p.leaf.decisionId}</dd>
            <dt>Recorded at</dt>
            <dd>{instant(p.leaf.recordedAt)}</dd>
            <dt>Result digest</dt>
            <dd className="mono" style={{ wordBreak: 'break-all' }}>{p.leaf.resultDigest}</dd>
            <dt>Leaf digest</dt>
            <dd className="mono" style={{ wordBreak: 'break-all' }}>{p.leafDigest}</dd>
            <dt>Position</dt>
            <dd>leaf {p.leafIndex} of {p.leafCount} (zero-based index)</dd>
            <dt>Seal</dt>
            <dd className="mono">{p.sealId}</dd>
            <dt>Merkle root</dt>
            <dd className="mono" style={{ wordBreak: 'break-all' }}>{p.merkleRoot}</dd>
          </dl>
          <h3 className="ztax-card-title">Audit path ({p.auditPath.length} {p.auditPath.length === 1 ? 'sibling' : 'siblings'}, leaf to root)</h3>
          {p.auditPath.length === 0 ? (
            <p className="muted zp-intro">Empty: this is the only leaf in its seal, so its hash is the root.</p>
          ) : (
            <ol className="zp-list mono">
              {p.auditPath.map((h, i) => (
                <li key={`${i}-${h}`} style={{ wordBreak: 'break-all' }}>{h}</li>
              ))}
            </ol>
          )}
        </Card>
      )}
    </>
  )
}
