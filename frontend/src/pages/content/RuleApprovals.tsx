import { useState, type FormEvent } from 'react'

import { ztax } from '../../platform/client'
import { Card, Empty, ErrorNote, Field, Id, Loaded, Status, Submit, newKey, useAction, useLoad } from '../../ui/kit'
import { sha256Hex } from '../registrations/model'
import {
  AUTHORITY_CLASSES, CHANGE_TYPES, NOT_PUBLISHED, RULE_CHANGE_MEANING, RULE_CHANGE_STATUSES,
  reasonOf, refusalText, ruleChangeOffers,
  type AuthorityClass, type Offer, type RuleChange, type RuleChangeStatus, type RuleChangeType,
} from './model'

/**
 * Rule Approvals (Doc 03 §07; Sidebar WF-02 "Specialist rule review"): the
 * queue of proposed rule-pack changes, each with its source citation, three
 * clocks and test evidence; specialist recommendation and independent
 * approval for release. Served by /v1/content/rule-changes. No act here
 * publishes a pack.
 */

const Refusal = ({ error }: { error: unknown }) => {
  if (error === null || error === undefined) return null
  const text = refusalText(reasonOf(error))
  return text === null ? <ErrorNote error={error} /> : <p className="ztax-error-note" role="alert">{text}</p>
}

export const RuleApprovalsTab = ({ roles, me }: { roles: readonly string[]; me: string }) => {
  const [status, setStatus] = useState<RuleChangeStatus | ''>('')
  const [selected, setSelected] = useState<string | null>(null)
  const [revising, setRevising] = useState<RuleChange | null>(null)
  const { load, reload } = useLoad(
    (signal) => ztax.listRuleChanges({ ...(status === '' ? {} : { status }), limit: 200 }, signal), [status])
  const proposer = roles.includes('OPERATOR') || roles.includes('ANALYST')
  return (
    <>
      <Card title="Rule approval queue" aside={
        <label className="ztax-field" style={{ flexDirection: 'row', alignItems: 'center', gap: 6 }}>
          <span>Status</span>
          <select value={status} onChange={(e) => { setStatus(RULE_CHANGE_STATUSES.find((s) => s === e.target.value) ?? '') }}>
            <option value="">Any</option>
            {RULE_CHANGE_STATUSES.map((s) => <option key={s} value={s}>{s.replaceAll('_', ' ')}</option>)}
          </select>
        </label>
      }>
        <p className="ztax-note" role="note">{NOT_PUBLISHED}</p>
        <Loaded load={load}>
          {(list) => list.ruleChanges.length === 0 ? <Empty>The queue is empty.</Empty> : (
            <div className="zp-scroll">
              <table className="table">
                <thead>
                  <tr>
                    <th scope="col">Pack</th><th scope="col">Jurisdiction</th><th scope="col">Change</th>
                    <th scope="col">Effective</th><th scope="col">Authority</th><th scope="col">Status</th><th scope="col" />
                  </tr>
                </thead>
                <tbody>
                  {list.ruleChanges.map((c) => (
                    <tr key={c.id}>
                      <td className="mono">{c.packId}@{c.baseVersion}</td><td>{c.jurisdiction}</td><td>{c.summary}</td>
                      <td>{c.effectiveFrom}</td><td>{c.citation.authorityClass}</td><td><Status value={c.status} /></td>
                      <td><button type="button" className="ztax-btn-quiet" onClick={() => { setSelected(c.id) }}>Open</button></td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </Loaded>
      </Card>
      {selected === null ? null : (
        <ChangeDetail id={selected} roles={roles} me={me} onChanged={reload}
          onRevise={(c) => { setRevising(c); document.getElementById('rule-change-form')?.scrollIntoView({ behavior: 'smooth' }) }} />
      )}
      {proposer ? (
        <div id="rule-change-form">
          <ProposeChange key={revising?.id ?? 'new'} revising={revising}
            onDone={(c) => { setRevising(null); setSelected(c.id); reload() }} />
        </div>
      ) : null}
    </>
  )
}

const ChangeDetail = ({ id, roles, me, onChanged, onRevise }: {
  id: string; roles: readonly string[]; me: string; onChanged: () => void; onRevise: (c: RuleChange) => void
}) => {
  const { load, reload } = useLoad((signal) => ztax.getRuleChange(id, signal), [id])
  const [act, setAct] = useState<Offer | null>(null)
  const changed = () => { setAct(null); reload(); onChanged() }
  return (
    <Loaded load={load}>
      {(c) => {
        const offers = ruleChangeOffers(c, roles, me)
        return (
          <Card title={`${c.packId}@${c.baseVersion} · ${c.summary}`} aside={<Status value={c.status} />}>
            <p className="muted">{RULE_CHANGE_MEANING[c.status]}</p>
            <dl className="ztax-kv">
              <dt>Rule</dt><dd className="mono">{c.ruleId ?? 'new rule'}</dd>
              <dt>Change type</dt><dd>{CHANGE_TYPES.find((t) => t.value === c.changeType)?.label}</dd>
              <dt>Jurisdiction</dt><dd>{c.jurisdiction}</dd>
              <dt>Source</dt>
              <dd><span className="mono">{c.citation.sourceId}</span> — {c.citation.reference}, edition {c.citation.edition} ({c.citation.authorityClass})</dd>
              <dt>Published</dt><dd>{c.publishedOn ?? '—'}</dd>
              <dt>Effective</dt><dd>{c.effectiveFrom} → {c.effectiveTo ?? 'open'}</dd>
              <dt>Recorded</dt><dd>{c.createdAt} by <Id value={c.createdBy} /></dd>
              <dt>Test evidence</dt><dd>{c.testEvidence === undefined ? 'none yet' : <>{c.testEvidence.ref}{c.testEvidence.sha256 === undefined ? '' : <> · <Id value={c.testEvidence.sha256} /></>}</>}</dd>
              {c.originRef === undefined ? null : <><dt>Origin</dt><dd className="mono">{c.originRef}</dd></>}
              {c.supersedes === undefined ? null : <><dt>Revises</dt><dd><Id value={c.supersedes} /></dd></>}
              <dt>Release eligible</dt><dd>{c.releaseEligible ? 'Yes — not published' : 'No'}</dd>
            </dl>
            <h3 className="ztax-card-title">Proposed change</h3>
            <p style={{ whiteSpace: 'pre-wrap' }}>{c.detail}</p>
            {c.impact === undefined ? null : <><h3 className="ztax-card-title">Impact</h3><p style={{ whiteSpace: 'pre-wrap' }}>{c.impact}</p></>}
            <details>
              <summary>Review history ({c.history.length})</summary>
              <ol>
                {c.history.map((e) => (
                  <li key={e.seq}>{e.kind.replaceAll('_', ' ')} · {e.recordedAt} · <Id value={e.recordedBy} />
                    {e.reason === undefined ? '' : ` — ${e.reason}`}{e.testEvidence === undefined ? '' : ` — ${e.testEvidence.ref}`}</li>
                ))}
              </ol>
            </details>
            {offers.length === 0 ? null : (
              <div className="ztax-form-actions" style={{ flexWrap: 'wrap', gap: 8 }}>
                {offers.map((o) => (
                  <button key={o.act} type="button" className="ztax-btn-quiet" disabled={o.blocked !== null}
                    title={o.blocked ?? undefined} onClick={() => { setAct(o) }}>{o.label}</button>
                ))}
                {c.status === 'CHANGES_REQUESTED' && c.createdBy === me
                  ? <button type="button" className="ztax-btn-quiet" onClick={() => { onRevise(c) }}>Propose a revision</button> : null}
              </div>
            )}
            {offers.filter((o) => o.blocked !== null).map((o) => <p key={o.act} className="muted">{o.label}: {o.blocked}</p>)}
            {act === null ? null : act.act === 'EVIDENCE'
              ? <EvidenceForm change={c} onDone={changed} />
              : <ChangeMove change={c} offer={act} onDone={changed} />}
          </Card>
        )
      }}
    </Loaded>
  )
}

const ChangeMove = ({ change, offer, onDone }: { change: RuleChange; offer: Offer; onDone: () => void }) => {
  const [reason, setReason] = useState('')
  const action = useAction<RuleChange>()
  const submit = (e: FormEvent) => {
    e.preventDefault()
    const to = RULE_CHANGE_STATUSES.find((s) => s === offer.act)
    if (to === undefined) return
    void action.run(() => ztax.transitionRuleChange(change.id, { to, expectedRevision: change.revision, reason: reason.trim() }))
      .then((done) => { if (done !== null) onDone() })
  }
  return (
    <form className="ztax-form" onSubmit={submit} style={{ marginTop: 12 }}>
      <Field label="Rationale" help={`Recorded against revision ${String(change.revision)} exactly; nothing is edited.`}>
        <textarea value={reason} required maxLength={2000} onChange={(e) => { setReason(e.target.value) }} />
      </Field>
      <div className="ztax-form-actions"><Submit busy={action.busy}>{offer.label}</Submit></div>
      <Refusal error={action.error} />
    </form>
  )
}

const EvidenceForm = ({ change, onDone }: { change: RuleChange; onDone: () => void }) => {
  const [ref, setRef] = useState('')
  const [sha, setSha] = useState('')
  const action = useAction<RuleChange>()
  const submit = (e: FormEvent) => {
    e.preventDefault()
    void action.run(() => ztax.recordRuleChangeTestEvidence(change.id, {
      testEvidence: { ref: ref.trim(), ...(sha === '' ? {} : { sha256: sha }) }, expectedRevision: change.revision,
    })).then((done) => { if (done !== null) onDone() })
  }
  return (
    <form className="ztax-form" onSubmit={submit} style={{ marginTop: 12 }}>
      <Field label="Test run reference" help="The golden-suite run or regression diff the change was verified by.">
        <input value={ref} required maxLength={200} onChange={(e) => { setRef(e.target.value) }} />
      </Field>
      <Field label="Test report (optional)" help={sha === '' ? 'Hashed here; never uploaded.' : `SHA-256 ${sha.slice(0, 12)}…`}>
        <input type="file" onChange={(e) => {
          const f = e.target.files?.[0]
          if (f === undefined) { setSha(''); return }
          void sha256Hex(f).then(setSha)
        }} />
      </Field>
      <div className="ztax-form-actions"><Submit busy={action.busy}>Attach evidence</Submit></div>
      <Refusal error={action.error} />
    </form>
  )
}

const ProposeChange = ({ revising, onDone }: { revising: RuleChange | null; onDone: (c: RuleChange) => void }) => {
  const [key, setKey] = useState(newKey)
  const [pack, setPack] = useState(revising?.packId ?? '')
  const [base, setBase] = useState(revising?.baseVersion ?? '')
  const [rule, setRule] = useState(revising?.ruleId ?? '')
  const [jurisdiction, setJurisdiction] = useState(revising?.jurisdiction ?? '')
  const [changeType, setChangeType] = useState<RuleChangeType>(revising?.changeType ?? 'RATE_CHANGE')
  const [summary, setSummary] = useState(revising?.summary ?? '')
  const [detail, setDetail] = useState(revising?.detail ?? '')
  const [impact, setImpact] = useState(revising?.impact ?? '')
  const [sourceId, setSourceId] = useState(revising?.citation.sourceId ?? '')
  const [reference, setReference] = useState(revising?.citation.reference ?? '')
  const [edition, setEdition] = useState(revising?.citation.edition ?? '')
  const [authority, setAuthority] = useState<AuthorityClass>(revising?.citation.authorityClass ?? 'A0')
  const [publishedOn, setPublishedOn] = useState(revising?.publishedOn ?? '')
  const [effectiveFrom, setEffectiveFrom] = useState(revising?.effectiveFrom ?? '')
  const [effectiveTo, setEffectiveTo] = useState(revising?.effectiveTo ?? '')
  const [originRef, setOriginRef] = useState(revising?.originRef ?? '')
  const [testRef, setTestRef] = useState('')
  const action = useAction<RuleChange>()
  const opt = (k: string, v: string) => (v.trim() === '' ? {} : { [k]: v.trim() })
  const submit = (e: FormEvent) => {
    e.preventDefault()
    void action.run(() => ztax.proposeRuleChange(key, {
      packId: pack.trim().toUpperCase(), baseVersion: base.trim(), jurisdiction: jurisdiction.trim().toUpperCase(), changeType,
      summary: summary.trim(), detail: detail.trim(), effectiveFrom,
      citation: { sourceId: sourceId.trim(), reference: reference.trim(), edition: edition.trim(), authorityClass: authority },
      ...opt('ruleId', rule), ...opt('impact', impact), ...opt('publishedOn', publishedOn), ...opt('effectiveTo', effectiveTo),
      ...opt('originRef', originRef), ...(revising === null ? {} : { supersedes: revising.id }),
      ...(testRef.trim() === '' ? {} : { testEvidence: { ref: testRef.trim() } }),
    })).then((done) => {
      if (done !== null) {
        setKey(newKey())
        onDone(done)
      }
    })
  }
  return (
    <Card title={revising === null ? 'Propose a rule change' : 'Propose a revision'}>
      {revising === null ? null : <p className="ztax-note" role="note">Revises <Id value={revising.id} />, which becomes SUPERSEDED.</p>}
      <form className="ztax-form" onSubmit={submit}>
        <Field label="Rule pack"><input value={pack} required placeholder="US-NY-TELECOM" onChange={(e) => { setPack(e.target.value) }} /></Field>
        <Field label="Against pack version"><input value={base} required placeholder="3.2.0" pattern="[0-9]+\.[0-9]+\.[0-9]+" onChange={(e) => { setBase(e.target.value) }} /></Field>
        <Field label="Rule (optional)" help="Empty for a change that adds a rule."><input value={rule} placeholder="ZTAX-RULE-…" onChange={(e) => { setRule(e.target.value) }} /></Field>
        <Field label="Jurisdiction"><input value={jurisdiction} required placeholder="US-NY" onChange={(e) => { setJurisdiction(e.target.value) }} /></Field>
        <Field label="Change type">
          <select value={changeType} onChange={(e) => { setChangeType(CHANGE_TYPES.find((t) => t.value === e.target.value)?.value ?? 'OTHER') }}>
            {CHANGE_TYPES.map((t) => <option key={t.value} value={t.value}>{t.label}</option>)}
          </select>
        </Field>
        <Field label="Summary"><input value={summary} required maxLength={500} onChange={(e) => { setSummary(e.target.value) }} /></Field>
        <Field label="Proposed change" help="The before and after of the rule, rate or predicate."><textarea rows={4} value={detail} required maxLength={8000} onChange={(e) => { setDetail(e.target.value) }} /></Field>
        <Field label="Impact (optional)"><textarea rows={2} value={impact} maxLength={8000} onChange={(e) => { setImpact(e.target.value) }} /></Field>
        <Field label="Source id"><input value={sourceId} required maxLength={200} onChange={(e) => { setSourceId(e.target.value) }} /></Field>
        <Field label="Cited passage"><input value={reference} required maxLength={200} onChange={(e) => { setReference(e.target.value) }} /></Field>
        <Field label="Edition read"><input value={edition} required maxLength={200} onChange={(e) => { setEdition(e.target.value) }} /></Field>
        <Field label="Authority class" help="Only A0 or A1 can be approved for release.">
          <select value={authority} onChange={(e) => { setAuthority(AUTHORITY_CLASSES.find((a) => a.value === e.target.value)?.value ?? 'U') }}>
            {AUTHORITY_CLASSES.map((a) => <option key={a.value} value={a.value}>{a.label}</option>)}
          </select>
        </Field>
        <Field label="Published by the authority (optional)"><input type="date" value={publishedOn} onChange={(e) => { setPublishedOn(e.target.value) }} /></Field>
        <Field label="Legal effect from"><input type="date" value={effectiveFrom} required onChange={(e) => { setEffectiveFrom(e.target.value) }} /></Field>
        <Field label="Legal effect to (optional, exclusive)"><input type="date" value={effectiveTo} onChange={(e) => { setEffectiveTo(e.target.value) }} /></Field>
        <Field label="Origin (optional)" help="A change-feed item or review recommendation reference. Provenance only."><input value={originRef} maxLength={200} onChange={(e) => { setOriginRef(e.target.value) }} /></Field>
        <Field label="Test run reference (optional now)"><input value={testRef} maxLength={200} onChange={(e) => { setTestRef(e.target.value) }} /></Field>
        <div className="ztax-form-actions"><Submit busy={action.busy}>Propose</Submit></div>
        <Refusal error={action.error} />
      </form>
    </Card>
  )
}
