import { useState, type FormEvent } from 'react'

import type { S } from '@zoikotax/sdk'

import { instant } from '../../dashboard/format'
import { ztax } from '../../platform/client'
import { Card, Columns, ErrorNote, Field, Id, Loaded, Raw, Status, Submit, useAction, useLoad } from '../../ui/kit'
import { Clocks } from './Clocks'
import {
  ACTION_FORMS, closeBlocker, compact, ESCALATIONS, evidenceList, pick, refusalHint, SEVERITIES, TYPES, UUID, utcTimestamp, words,
  type CaseAction, type EscalationTarget, type SupportCase,
} from './model'

type Actionable = Exclude<CaseAction, 'OPENED'>

const eventText = (e: S<'SupportCaseEvent'>): string =>
  [
    e.type === undefined ? null : `type ${words(e.type)}`,
    e.severity === undefined ? null : `severity ${e.severity}`,
    e.fiscalPeriod === undefined ? null : `period ${e.fiscalPeriod}`,
    e.contractRef === undefined ? null : `contract ${e.contractRef}`,
    e.sellerRef === undefined ? null : `seller ${e.sellerRef}`,
    e.legalEntityId === undefined ? null : `legal entity ${e.legalEntityId}`,
    e.statutoryDueAt === undefined ? null : `statutory deadline ${instant(e.statutoryDueAt)}`,
    e.slaDueAt === undefined ? null : `SLA brought forward to ${instant(e.slaDueAt)}`,
    e.assignee === undefined ? null : `assigned ${e.assignee}`,
    e.escalateTo === undefined ? null : `to ${words(e.escalateTo)}`,
    e.evidence === undefined ? null : `evidence ${e.evidence.join(', ')}`,
    e.missingFacts === undefined ? null : `facts requested: ${e.missingFacts.map(words).join(', ')}`,
    e.reopens === undefined ? null : `reopens event ${e.reopens}`,
  ].filter((x) => x !== null).join(' · ')

/** One case: its facts, both clocks, its history, and the acts its status allows. */
export const CaseDetail = ({ id, userId, canAct, onChanged, onClose }: {
  id: string
  userId: string
  canAct: boolean
  onChanged: () => void
  onClose: () => void
}) => {
  const { load, reload } = useLoad((signal) => ztax.getSupportCase(id, signal), [id])
  const refresh = () => {
    reload()
    onChanged()
  }
  return (
    <Loaded load={load}>
      {(c) => (
        <>
          <Columns>
            <Card
              title={c.subject}
              aside={
                <div className="ztax-filter-bar">
                  <button type="button" className="ztax-btn-quiet" onClick={reload}>Refresh</button>
                  <button type="button" className="ztax-btn-quiet" onClick={onClose}>Close panel</button>
                </div>
              }
            >
              <dl className="ztax-kv">
                <dt>Case</dt><dd className="mono">{c.id}</dd>
                <dt>Status</dt><dd><Status value={c.status} />{c.escalatedTo === undefined ? null : <span className="muted"> · escalated to {words(c.escalatedTo)}</span>}</dd>
                <dt>Type</dt><dd>{words(c.type)}{c.openedType === c.type ? null : <span className="muted"> (opened as {words(c.openedType)})</span>}</dd>
                <dt>Severity</dt><dd>{c.severity}</dd>
                <dt>Description</dt><dd style={{ whiteSpace: 'pre-wrap' }}>{c.description}</dd>
                <dt>Product</dt><dd>{c.product ?? '—'}</dd>
                <dt>Legal entity</dt><dd><Id value={c.legalEntityId} /></dd>
                <dt>Fiscal period</dt><dd>{c.fiscalPeriod ?? '—'}</dd>
                <dt>Contract</dt><dd>{c.contractRef ?? '—'}</dd>
                <dt>Seller</dt><dd>{c.sellerRef ?? '—'}</dd>
                <dt>Assignee</dt><dd>{c.assignee === undefined ? <span className="muted">unassigned</span> : <Id value={c.assignee} />}</dd>
                <dt>References</dt>
                <dd>
                  {[['decision', c.references.decisionId], ['document', c.references.documentId], ['obligation', c.references.obligationId],
                    ['remittance', c.references.remittanceId]].filter(([, v]) => v !== undefined).map(([k, v]) => (
                    <div key={k}>{k} <Id value={v} /></div>
                  ))}
                  <span className="muted" style={{ fontSize: 12 }}>Read-only. Support changes no fiscal record.</span>
                </dd>
                <dt>Evidence</dt><dd>{c.evidence.length === 0 ? '—' : c.evidence.map((e) => <div key={e} className="mono">{e}</div>)}</dd>
                {c.missingFacts.length === 0 ? null : (
                  <><dt>Facts requested</dt><dd><Status value="DATA_REQUIRED" /> {c.missingFacts.map(words).join(', ')}</dd></>
                )}
                <dt>Opened</dt><dd>{instant(c.openedAt)} by <Id value={c.openedBy} /></dd>
              </dl>
            </Card>
            <Card title="Clocks">
              <Clocks c={c} />
              <dl className="ztax-kv" style={{ marginTop: 12 }}>
                <dt>Customer response</dt><dd>{c.responded ? 'recorded' : 'not yet, since the last reopening'}</dd>
                <dt>Sign-off</dt>
                <dd>{c.signOffRequired ? (c.signedOff ? 'given by someone other than the opener' : 'required, not yet given') : 'not required'}</dd>
              </dl>
            </Card>
          </Columns>
          {canAct ? <Actions key={c.history.length} c={c} userId={userId} onDone={refresh} /> : (
            <p className="ztax-note">Your roles allow reading this case. ADMIN, OPERATOR or ANALYST may act on it.</p>
          )}
          <Card title={`History · ${c.history.length}`}>
            <div style={{ overflowX: 'auto' }}>
              <table className="ztax-action-table">
                <thead><tr><th>#</th><th>Action</th><th>Then</th><th>Changed</th><th>Note</th><th>By</th><th>When</th></tr></thead>
                <tbody>
                  {c.history.map((e) => (
                    <tr key={e.seq} style={{ verticalAlign: 'top' }}>
                      <td className="mono" style={{ paddingRight: 8 }}>{e.seq}</td>
                      <td style={{ paddingRight: 8 }}>{words(e.action)}</td>
                      <td style={{ paddingRight: 8 }}><Status value={e.status} /></td>
                      <td style={{ paddingRight: 8 }}>{eventText(e) || '—'}</td>
                      <td style={{ paddingRight: 8, whiteSpace: 'pre-wrap', maxWidth: 320 }}>{e.note ?? '—'}</td>
                      <td style={{ paddingRight: 8 }}><Id value={e.recordedBy} /></td>
                      <td style={{ whiteSpace: 'nowrap' }}>{instant(e.recordedAt)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            <Raw value={c} />
          </Card>
        </>
      )}
    </Loaded>
  )
}

/** The acts the case's status allows now, one form at a time. */
const Actions = ({ c, userId, onDone }: { c: SupportCase; userId: string; onDone: () => void }) => {
  const offered = c.actions.filter((a): a is Actionable => a !== 'OPENED')
  const [chosen, setChosen] = useState<Actionable | ''>(offered[0] ?? '')
  const blocker = closeBlocker(c)
  return (
    <Card title="Act on this case">
      {offered.length === 0 ? <p className="muted">Nothing can be done to the case in its current status.</p> : (
        <div className="ztax-filter-bar" style={{ flexWrap: 'wrap' }}>
          {offered.map((a) => (
            <button key={a} type="button" className={a === chosen ? 'ztax-btn-copper' : 'ztax-btn-quiet'} aria-pressed={a === chosen}
              onClick={() => { setChosen(a) }}>{ACTION_FORMS[a].label}</button>
          ))}
        </div>
      )}
      {blocker === null ? null : <p className="ztax-note">Cannot close yet: {blocker}</p>}
      {chosen === '' ? null : <ActionForm key={chosen} c={c} action={chosen} userId={userId} onDone={onDone} />}
    </Card>
  )
}

const ActionForm = ({ c, action, userId, onDone }: { c: SupportCase; action: Actionable; userId: string; onDone: () => void }) => {
  const form = ACTION_FORMS[action]
  const [note, setNote] = useState('')
  const [type, setType] = useState('')
  const [severity, setSeverity] = useState('')
  const [legalEntityId, setLegalEntityId] = useState(c.legalEntityId ?? '')
  const [fiscalPeriod, setFiscalPeriod] = useState(c.fiscalPeriod ?? '')
  const [contractRef, setContractRef] = useState(c.contractRef ?? '')
  const [sellerRef, setSellerRef] = useState(c.sellerRef ?? '')
  const [statutory, setStatutory] = useState('')
  const [assignee, setAssignee] = useState(c.assignee ?? '')
  const [escalateTo, setEscalateTo] = useState<EscalationTarget>('TAX_SPECIALIST')
  const [evidence, setEvidence] = useState('')
  const [problem, setProblem] = useState<string | null>(null)
  const run = useAction<SupportCase>()
  const ownSignOff = action === 'SIGN_OFF' && userId === c.openedBy

  const submit = (e: FormEvent) => {
    e.preventDefault()
    const opt = (v: string) => (v.trim() === '' ? undefined : v.trim())
    if (form.note === 'required' && note.trim() === '') {
      setProblem(`${form.noteLabel ?? 'A note'} is required.`)
      return
    }
    if ((form.assignee && !UUID.test(assignee.trim())) || (form.triage && legalEntityId.trim() !== '' && !UUID.test(legalEntityId.trim()))) {
      setProblem('That is not an identifier.')
      return
    }
    setProblem(null)
    let body = compact<S<'SupportCaseActionRequest'>>({ action, note: opt(note) })
    if (form.triage) {
      body = { ...body, ...compact<Partial<S<'SupportCaseActionRequest'>>>({
        type: pick(TYPES, type) || undefined, severity: pick(SEVERITIES, severity) || undefined,
        legalEntityId: opt(legalEntityId), fiscalPeriod: opt(fiscalPeriod), contractRef: opt(contractRef), sellerRef: opt(sellerRef),
        statutoryDueAt: c.clocks.statutoryDueAt === undefined ? utcTimestamp(statutory) : undefined,
      }) }
    }
    if (form.assignee) {
      body.assignee = assignee.trim()
    }
    if (form.escalate) {
      body.escalateTo = escalateTo
    }
    if (form.evidence) {
      body.evidence = evidenceList(evidence)
    }
    void run.run(() => ztax.actOnSupportCase(c.id, body)).then((done) => {
      if (done !== null) {
        onDone()
      }
    })
  }

  return (
    <form className="ztax-form" onSubmit={submit} style={{ marginTop: 12 }}>
      <p className="muted">{form.help}</p>
      {ownSignOff ? <p className="ztax-note">You opened this case, so the cell will refuse your sign-off. Another person must give it.</p> : null}
      {form.triage ? (
        <>
          <Field label="Type">
            <select value={type} onChange={(e) => { setType(e.target.value) }}>
              <option value="">keep {words(c.type)}</option>
              {TYPES.map((t) => <option key={t} value={t}>{words(t)}</option>)}
            </select>
          </Field>
          <Field label="Severity" help="A raised severity only brings the SLA earlier, measured from opening.">
            <select value={severity} onChange={(e) => { setSeverity(e.target.value) }}>
              <option value="">keep {c.severity}</option>
              {SEVERITIES.map((s) => <option key={s} value={s}>{s}</option>)}
            </select>
          </Field>
          <Field label="Customer legal entity id"><input className="mono" value={legalEntityId} onChange={(e) => { setLegalEntityId(e.target.value) }} /></Field>
          <Field label="Impacted fiscal period" help="YYYY-MM"><input value={fiscalPeriod} onChange={(e) => { setFiscalPeriod(e.target.value) }} /></Field>
          <Field label="Contract reference"><input maxLength={200} value={contractRef} onChange={(e) => { setContractRef(e.target.value) }} /></Field>
          <Field label="Seller reference"><input maxLength={200} value={sellerRef} onChange={(e) => { setSellerRef(e.target.value) }} /></Field>
          {c.clocks.statutoryDueAt === undefined ? (
            <Field label="Statutory deadline, UTC (optional)" help="Recorded once; support never changes it afterwards.">
              <input type="datetime-local" value={statutory} onChange={(e) => { setStatutory(e.target.value) }} />
            </Field>
          ) : null}
        </>
      ) : null}
      {form.assignee ? (
        <Field label="Assignee user id">
          <input className="mono" value={assignee} onChange={(e) => { setAssignee(e.target.value) }} />
          <button type="button" className="ztax-btn-quiet" onClick={() => { setAssignee(userId) }}>Assign to me</button>
        </Field>
      ) : null}
      {form.escalate ? (
        <Field label="Escalate to">
          <select value={escalateTo} onChange={(e) => { setEscalateTo(pick(ESCALATIONS, e.target.value) || 'TAX_SPECIALIST') }}>
            {ESCALATIONS.map((t) => <option key={t} value={t}>{words(t)}</option>)}
          </select>
        </Field>
      ) : null}
      {form.evidence ? (
        <Field label="Evidence references" help="Hashes or document ids, one per line.">
          <textarea rows={2} className="mono" value={evidence} onChange={(e) => { setEvidence(e.target.value) }} />
        </Field>
      ) : null}
      <Field label={`${form.noteLabel ?? 'Note'}${form.note === 'required' ? '' : ' (optional)'}`}>
        <textarea rows={3} maxLength={2000} value={note} onChange={(e) => { setNote(e.target.value) }} />
      </Field>
      <Submit busy={run.busy}>{form.label}</Submit>
      {problem === null ? null : <p className="ztax-error-note" role="alert">{problem}</p>}
      {run.error === null ? null : (
        <>
          <ErrorNote error={run.error} />
          {refusalHint(run.error) === null ? null : <p className="muted">{refusalHint(run.error)}</p>}
        </>
      )}
    </form>
  )
}
