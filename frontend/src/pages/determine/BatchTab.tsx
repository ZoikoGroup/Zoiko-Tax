import { useEffect, useState, type FormEvent } from 'react'

import type { S } from '@zoikotax/sdk'

import { ztax } from '../../platform/client'
import { instant } from '../../dashboard/format'
import { Card, ErrorNote, Field, Id, Outcome, Raw, Status, Submit, useAction } from '../../ui/kit'
import { commitBody, emptyLine, LineFields, lineProblem, type LineDraft } from './line'
import { Invalid, KeyNote, useIdempotencyKey } from './shared'

const POLL_MS = 1500

export const BatchTab = ({ onOpenDecision }: { onOpenDecision: (id: string) => void }) => {
  const [lines, setLines] = useState<LineDraft[]>(() => [emptyLine()])
  const [problem, setProblem] = useState<string | null>(null)
  const [jobId, setJobId] = useState('')
  const [lookup, setLookup] = useState('')
  const submit = useAction<S<'Job'>>()
  const keys = useIdempotencyKey()

  const setLine = (i: number, line: LineDraft) => { setLines((ls) => ls.map((l, j) => (j === i ? line : l))) }
  const removeLine = (i: number) => { setLines((ls) => ls.filter((_, j) => j !== i)) }

  const send = (e: FormEvent) => {
    e.preventDefault()
    let p: string | null = lines.length === 0 ? 'Add at least one line.' : null
    for (const [i, l] of lines.entries()) {
      const q = lineProblem(l, true)
      if (p === null && q !== null) {
        p = `Line ${i + 1}: ${q}`
      }
    }
    setProblem(p)
    if (p !== null) {
      return
    }
    const body: S<'BatchRequest'> = { operation: 'COMMIT', items: lines.map((l) => commitBody(l)) }
    const key = keys.keyFor(body)
    void submit.run(() => ztax.submitBatch(key, body)).then((job) => {
      if (job !== null) {
        keys.succeeded()
        setJobId(job.id)
        setLookup(job.id)
      }
    })
  }

  return (
    <>
      <Card title="Submit a batch of commits">
        <p className="muted ztax-note">
          Each line is exactly a commit, queued as one job and run in order by a worker. A refused line is recorded with its
          reason code and the job carries on. Up to 1000 lines.
        </p>
        <form onSubmit={send} aria-label="Batch of commits" style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
          {lines.map((line, i) => (
            <fieldset key={i} className="ztax-form ztax-line-box">
              <legend>Line {i + 1}</legend>
              <LineFields line={line} onChange={(l) => { setLine(i, l) }} idPrefix={`batch-${i}`} />
              <div className="ztax-form-actions">
                <button type="button" className="ztax-btn-quiet" onClick={() => { removeLine(i) }} disabled={lines.length === 1}>
                  Remove line {i + 1}
                </button>
              </div>
            </fieldset>
          ))}
          <div className="ztax-form-actions">
            <button
              type="button"
              className="ztax-btn-quiet"
              onClick={() => {
                setLines((ls) => {
                  const last = ls[ls.length - 1]
                  return [...ls, last === undefined ? emptyLine() : { ...last, businessKey: '' }]
                })
              }}
            >
              Add line
            </button>
            <Submit busy={submit.busy}>Submit batch ({lines.length})</Submit>
          </div>
        </form>
        <Invalid message={problem} />
        <KeyNote value={keys.current} />
        <Outcome
          error={submit.error}
          done={submit.done === null ? null : <p className="ztax-ok-note">Queued as job <span className="mono">{submit.done.id}</span>.</p>}
        />
      </Card>

      <Card title="Job">
        <form
          className="ztax-form"
          aria-label="Look up a job"
          onSubmit={(e) => { e.preventDefault(); setJobId(lookup.trim()) }}
        >
          <Field label="Job id">
            <input className="mono" value={lookup} onChange={(e) => { setLookup(e.target.value) }} autoComplete="off" />
          </Field>
          <div className="ztax-form-actions">
            <button type="submit" className="ztax-btn-copper" disabled={lookup.trim() === ''}>Look up</button>
          </div>
        </form>
        {jobId === '' ? null : <JobView key={jobId} id={jobId} onOpenDecision={onOpenDecision} />}
      </Card>
    </>
  )
}

/** A job, read again every POLL_MS until it is COMPLETED. */
const JobView = ({ id, onOpenDecision }: { id: string; onOpenDecision: (id: string) => void }) => {
  const [job, setJob] = useState<S<'Job'> | null>(null)
  const [error, setError] = useState<unknown>(null)
  const [attempt, setAttempt] = useState(0)

  useEffect(() => {
    const controller = new AbortController()
    void ztax.getJob(id, controller.signal).then((r) => {
      if (controller.signal.aborted) {
        return
      }
      if (r.ok) {
        setJob(r.data)
        setError(null)
      } else {
        setError(r.error)
      }
    })
    return () => { controller.abort() }
  }, [id, attempt])

  const running = job !== null && job.status !== 'COMPLETED' && error === null
  useEffect(() => {
    if (!running) {
      return
    }
    const timer = setTimeout(() => { setAttempt((n) => n + 1) }, POLL_MS)
    return () => { clearTimeout(timer) }
  }, [running, job])

  if (job === null) {
    return error === null ? <p className="muted" aria-live="polite">Reading…</p> : <ErrorNote error={error} />
  }
  return (
    <>
      {error === null ? null : <ErrorNote error={error} />}
      <div className="ztax-form-actions" aria-live="polite">
        <Status value={job.status} />
        <span style={{ fontSize: 12 }}>
          {job.itemCount} items · {job.succeeded} succeeded · {job.failed} failed · {job.pending} pending
        </span>
        <span className="muted" style={{ fontSize: 12 }}>
          requested {instant(job.requestedAt)}
          {job.finishedAt === undefined ? '' : ` · finished ${instant(job.finishedAt)}`}
          {running ? ' · refreshing' : ''}
        </span>
        <button type="button" className="ztax-btn-quiet" onClick={() => { setAttempt((n) => n + 1) }}>Refresh</button>
      </div>
      <table className="table">
        <caption className="table__caption">Items, in submission order</caption>
        <thead>
          <tr>
            <th scope="col">#</th>
            <th scope="col">Business key</th>
            <th scope="col">Status</th>
            <th scope="col">Decision</th>
            <th scope="col">Reason</th>
          </tr>
        </thead>
        <tbody>
          {job.items.map((item) => (
            <tr key={item.index}>
              <td>{item.index + 1}</td>
              <td className="mono">{item.businessKey}</td>
              <td><Status value={item.status} /></td>
              <td>
                {item.decisionId === undefined ? '—' : (
                  <button
                    type="button"
                    className="ztax-btn-quiet"
                    title={item.decisionId}
                    onClick={() => { if (item.decisionId !== undefined) onOpenDecision(item.decisionId) }}
                  >
                    <Id value={item.decisionId} />
                  </button>
                )}
              </td>
              <td className="mono">{item.reasonCode ?? '—'}</td>
            </tr>
          ))}
        </tbody>
      </table>
      <Raw value={job} label="Full job" />
    </>
  )
}
