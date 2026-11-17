import { useState } from 'react'

import type { Result, S } from '@zoikotax/sdk'

import { instant } from '../../dashboard/format'
import { ztax } from '../../platform/client'
import { Card, Empty, Id, Loaded, Status, useLoad } from '../../ui/kit'

type SealDocument = S<'SealDocument'>
type Verification = S<'SealVerification'>

/**
 * Verified exports: a period seal's signed document, handed over only after
 * the cell has verified it end to end, with the verdict beside it. The file
 * is the document exactly as the cell returned it — nothing is added to it or
 * recomputed — and nothing is kept in the browser.
 */
export const ExportsTab = () => {
  const { load, reload } = useLoad((signal) => ztax.listSeals({ limit: 100 }, signal), [])
  const [selected, setSelected] = useState<string | null>(null)

  return (
    <>
      <Card title="Verified exports" aside={<button type="button" className="ztax-btn-quiet" onClick={reload}>Refresh</button>}>
        <p className="muted zp-intro">
          Choose a seal. The cell verifies it — signature, index row, root against today&apos;s record and every result
          digest — and only then is its signed document offered for download, so what leaves is known to verify.
        </p>
        <Loaded load={load}>
          {(data) =>
            data.seals.length === 0 ? <Empty>No period has been sealed yet.</Empty> : (
              <div className="zp-scroll">
                <table className="table">
                  <thead>
                    <tr>
                      <th scope="col">Period start</th>
                      <th scope="col">Period end</th>
                      <th scope="col">Leaves</th>
                      <th scope="col">Key</th>
                      <th scope="col">Sealed at</th>
                      <th scope="col"><span className="zp-sr-only">Verify</span></th>
                    </tr>
                  </thead>
                  <tbody>
                    {data.seals.map((s) => (
                      <tr key={s.id} className={s.id === selected ? 'row--current' : undefined}>
                        <td>{instant(s.periodStart)}</td>
                        <td>{instant(s.periodEnd)}</td>
                        <td>{s.leafCount}</td>
                        <td className="mono">{s.keyId}</td>
                        <td>{instant(s.sealedAt)}</td>
                        <td>
                          <button
                            type="button"
                            className="ztax-btn-quiet"
                            aria-pressed={s.id === selected}
                            onClick={() => { setSelected(s.id) }}
                          >
                            {s.id === selected ? 'Selected' : 'Verify & export'}
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
      {selected === null ? null : <VerifiedExport key={selected} sealId={selected} />}
    </>
  )
}

function download(doc: SealDocument) {
  const text = JSON.stringify(doc, null, 2) + '\n'
  const url = URL.createObjectURL(new Blob([text], { type: 'application/json;charset=utf-8' }))
  const a = document.createElement('a')
  a.href = url
  a.download = `seal-${doc.seal.id}.json`
  a.click()
  URL.revokeObjectURL(url)
}

const VerifiedExport = ({ sealId }: { sealId: string }) => {
  const { load } = useLoad(async (signal): Promise<Result<{ doc: SealDocument; verification: Verification }>> => {
    const [doc, verification] = await Promise.all([ztax.getSeal(sealId, signal), ztax.verifySeal(sealId, signal)])
    if (!doc.ok) {
      return doc
    }
    if (!verification.ok) {
      return verification
    }
    return { ok: true, data: { doc: doc.data, verification: verification.data } }
  }, [sealId])

  return (
    <Card title="Seal export" aside={<Id value={sealId} />}>
      <Loaded load={load}>
        {({ doc, verification }) => <ExportView doc={doc} v={verification} />}
      </Loaded>
    </Card>
  )
}

const ExportView = ({ doc, v }: { doc: SealDocument; v: Verification }) => {
  const valid = v.verdict === 'VALID'
  const [acknowledged, setAcknowledged] = useState(false)
  return (
    <>
      <dl className="ztax-kv" aria-live="polite">
        <dt>Verdict</dt>
        <dd>
          {valid ? <Status value="VALID" /> : <span className="ztax-status ztax-status--bad">{v.verdict.replaceAll('_', ' ')}</span>}
        </dd>
        <dt>Period</dt>
        <dd>[{instant(doc.seal.periodStart)} → {instant(doc.seal.periodEnd)})</dd>
        <dt>Cell</dt>
        <dd className="mono">{doc.seal.cell}</dd>
        <dt>Leaves</dt>
        <dd>{v.leafCount}</dd>
        <dt>Signature</dt>
        <dd><span className="mono">{doc.signature.algorithm}</span> under key <span className="mono">{doc.signature.keyId}</span></dd>
        <dt>Signed root</dt>
        <dd className="mono" style={{ wordBreak: 'break-all' }}>{v.signedRoot}</dd>
        <dt>Recomputed root</dt>
        <dd className="mono" style={{ wordBreak: 'break-all' }}>{v.recomputedRoot ?? '—'}</dd>
        {v.detail === undefined ? null : (
          <>
            <dt>Detail</dt>
            <dd>{v.detail}</dd>
          </>
        )}
      </dl>
      {valid ? (
        <p className="ztax-ok-note" role="status">Verified. The document below is the one the cell signed.</p>
      ) : (
        <>
          <p className="ztax-error-note" role="alert">
            This seal did not verify. Its document can still be exported as evidence of the failure, but it must not be
            relied on as proof of the period.
          </p>
          <label className="ztax-field" style={{ flexDirection: 'row', gap: 8, alignItems: 'center' }}>
            <input type="checkbox" checked={acknowledged} onChange={(e) => { setAcknowledged(e.target.checked) }} />
            <span>I understand this export failed verification ({v.verdict.replaceAll('_', ' ')}).</span>
          </label>
        </>
      )}
      <div className="ztax-form-actions">
        <button
          type="button"
          className="ztax-btn-copper"
          disabled={!valid && !acknowledged}
          onClick={() => { download(doc) }}
        >
          Download signed seal (JSON)
        </button>
        <span className="muted" style={{ fontSize: 12 }}>
          The seal, its base64 signed payload and signature, exactly as returned by the cell.
        </span>
      </div>
    </>
  )
}
