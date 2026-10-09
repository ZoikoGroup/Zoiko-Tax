import { useState, type FormEvent } from 'react'

import type { S } from '@zoikotax/sdk'

import { instant } from '../../dashboard/format'
import { ztax } from '../../platform/client'
import { Card, Empty, ErrorNote, Field, Id, Loaded, Outcome, Status, Submit, useAction, useLoad } from '../../ui/kit'
import { fromLocalInput, utcMidnight } from './time'

type Seal = S<'Seal'>
type Verification = S<'SealVerification'>

/** Decoded size of a base64 string, a count of bytes. */
function base64Bytes(b64: string): number {
  const pad = b64.endsWith('==') ? 2 : b64.endsWith('=') ? 1 : 0
  return Math.floor((b64.length * 3) / 4) - pad
}

const Digest = ({ value }: { value: string | undefined }) =>
  value === undefined ? <span className="mono">—</span> : (
    <span className="mono" title={value}>{value.length > 24 ? `${value.slice(0, 16)}…${value.slice(-6)}` : value}</span>
  )

export const SealsTab = ({ admin, selected, onSelect }: {
  admin: boolean
  selected: string | null
  onSelect: (id: string) => void
}) => {
  const { load, reload } = useLoad((signal) => ztax.listSeals({ limit: 100 }, signal), [])

  return (
    <>
      <Card
        title="Period seals"
        aside={<button type="button" className="ztax-btn-quiet" onClick={reload}>Refresh</button>}
      >
        <p className="muted zp-intro">
          A seal is a signed statement that the decisions recorded in [start, end) were exactly these, with exactly
          these results. It is integrity evidence, not a legal finality marker.
        </p>
        <Loaded load={load}>
          {(data) =>
            data.seals.length === 0 ? (
              <Empty>No period has been sealed yet.</Empty>
            ) : (
              <div className="zp-scroll">
                <table className="table">
                  <thead>
                    <tr>
                      <th scope="col">Period start</th>
                      <th scope="col">Period end</th>
                      <th scope="col">Leaves</th>
                      <th scope="col">Merkle root</th>
                      <th scope="col">Key</th>
                      <th scope="col">Sealed at</th>
                      <th scope="col"><span className="zp-sr-only">Open</span></th>
                    </tr>
                  </thead>
                  <tbody>
                    {data.seals.map((s) => (
                      <tr key={s.id} className={s.id === selected ? 'row--current' : undefined}>
                        <td>{instant(s.periodStart)}</td>
                        <td>{instant(s.periodEnd)}</td>
                        <td>{s.leafCount}</td>
                        <td><Digest value={s.merkleRoot} /></td>
                        <td className="mono">{s.keyId}</td>
                        <td>{instant(s.sealedAt)}</td>
                        <td>
                          <button
                            type="button"
                            className="ztax-btn-quiet"
                            aria-pressed={s.id === selected}
                            onClick={() => { onSelect(s.id) }}
                          >
                            {s.id === selected ? 'Showing' : 'Open'}
                          </button>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )
          }
        </Loaded>
      </Card>

      {selected === null ? null : <SealDetail key={selected} sealId={selected} />}

      {admin ? (
        <SealForm
          onSealed={(seal) => {
            reload()
            onSelect(seal.id)
          }}
        />
      ) : (
        <Card title="Seal a period">
          <p className="muted zp-intro">Sealing a period needs the ADMIN role.</p>
        </Card>
      )}
    </>
  )
}

const SealDetail = ({ sealId }: { sealId: string }) => {
  const { load } = useLoad((signal) => ztax.getSeal(sealId, signal), [sealId])
  const verify = useAction<Verification>()

  return (
    <Card title="Seal" aside={<Id value={sealId} />}>
      <Loaded load={load}>
        {(doc) => (
          <>
            <dl className="ztax-kv">
              <dt>Seal id</dt>
              <dd className="mono">{doc.seal.id}</dd>
              <dt>Period</dt>
              <dd>[{instant(doc.seal.periodStart)} → {instant(doc.seal.periodEnd)})</dd>
              <dt>Cell</dt>
              <dd className="mono">{doc.seal.cell}</dd>
              <dt>Leaves</dt>
              <dd>{doc.seal.leafCount}</dd>
              <dt>Merkle root</dt>
              <dd className="mono" style={{ wordBreak: 'break-all' }}>{doc.seal.merkleRoot}</dd>
              <dt>Signature</dt>
              <dd>
                <span className="mono">{doc.signature.algorithm}</span> under key{' '}
                <span className="mono">{doc.signature.keyId}</span>
              </dd>
              <dt>Signed payload</dt>
              <dd>
                {base64Bytes(doc.signedPayload)} bytes ({doc.signedPayload.length} base64 characters), the canonical
                payload exactly as signed
              </dd>
              <dt>Sealed at</dt>
              <dd>{instant(doc.seal.sealedAt)}</dd>
            </dl>
            <div className="ztax-form-actions">
              <button
                type="button"
                className="ztax-btn-copper"
                disabled={verify.busy}
                onClick={() => { void verify.run(() => ztax.verifySeal(sealId)) }}
              >
                {verify.busy ? 'Verifying…' : 'Verify end to end'}
              </button>
              <span className="muted" style={{ fontSize: 12 }}>
                Checks the signature, the index row, the root against today's record and every result digest.
              </span>
            </div>
            <Outcome error={verify.error} done={verify.done === null ? null : <VerdictView v={verify.done} />} />
          </>
        )}
      </Loaded>
    </Card>
  )
}

const VerdictView = ({ v }: { v: Verification }) => (
  <div aria-live="polite">
    <dl className="ztax-kv">
      <dt>Verdict</dt>
      <dd>
        {v.verdict === 'VALID' ? (
          <Status value="VALID" />
        ) : (
          <span className="ztax-status ztax-status--bad">{v.verdict.replaceAll('_', ' ')}</span>
        )}
      </dd>
      <dt>Key</dt>
      <dd className="mono">{v.keyId}</dd>
      <dt>Leaves</dt>
      <dd>{v.leafCount}</dd>
      <dt>Signed root</dt>
      <dd className="mono" style={{ wordBreak: 'break-all' }}>{v.signedRoot}</dd>
      <dt>Recomputed root</dt>
      <dd className="mono" style={{ wordBreak: 'break-all' }}>
        {v.recomputedRoot ?? '—'}
        {v.recomputedRoot === undefined ? null : v.recomputedRoot === v.signedRoot ? (
          <span className="tag tag--ok" style={{ marginLeft: 6 }}>same</span>
        ) : (
          <span className="tag tag--warn" style={{ marginLeft: 6 }}>differs</span>
        )}
      </dd>
      {v.detail === undefined ? null : (
        <>
          <dt>Detail</dt>
          <dd>{v.detail}</dd>
        </>
      )}
    </dl>
  </div>
)

const SealForm = ({ onSealed }: { onSealed: (seal: Seal) => void }) => {
  const [start, setStart] = useState(() => utcMidnight(-1))
  const [end, setEnd] = useState(() => utcMidnight(0))
  const [local, setLocal] = useState<string | null>(null)
  const seal = useAction<Seal>()

  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    const periodStart = fromLocalInput(start)
    const periodEnd = fromLocalInput(end)
    if (periodStart === null || periodEnd === null) {
      setLocal('Give both instants as a date and a time.')
      return
    }
    setLocal(null)
    void seal.run(() => ztax.sealPeriod({ periodStart, periodEnd })).then((s) => {
      if (s !== null) onSealed(s)
    })
  }

  return (
    <Card title="Seal a period">
      <p className="muted zp-intro">
        Signs the decisions recorded in [start, end). A period can be sealed only once it has settled, five minutes after
        its end, so a transaction still committing is not left out. Seals never overlap: a decision is vouched for by one
        seal or none. Both instants are UTC.
      </p>
      <form className="ztax-form" onSubmit={onSubmit} aria-label="Seal a period">
        <Field label="Period start (UTC)">
          <input type="datetime-local" value={start} required onChange={(e) => { setStart(e.target.value) }} />
        </Field>
        <Field label="Period end (UTC, exclusive)">
          <input type="datetime-local" value={end} required onChange={(e) => { setEnd(e.target.value) }} />
        </Field>
        <div className="ztax-form-actions">
          <Submit busy={seal.busy}>Seal period</Submit>
        </div>
      </form>
      {local === null ? null : <ErrorNote error={new Error(local)} />}
      <Outcome
        error={seal.error}
        done={
          seal.done === null ? null : (
            <p className="ztax-ok-note" role="status">
              Sealed {seal.done.leafCount} {seal.done.leafCount === 1 ? 'decision' : 'decisions'} under key{' '}
              <span className="mono">{seal.done.keyId}</span>.
            </p>
          )
        }
      />
    </Card>
  )
}
