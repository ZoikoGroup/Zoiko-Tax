import { useState, type FormEvent } from 'react'

import type { S } from '@zoikotax/sdk'

import { ztax } from '../../platform/client'
import { Card, Field, Outcome, Submit, useAction } from '../../ui/kit'
import {
  compact, evidenceList, pick, SEVERITIES, SLA_TARGET, TYPES, UUID, utcTimestamp, words,
  type CaseType, type Severity, type SupportCase,
} from './model'

/** Opens a case. The SLA starts when the cell records it. */
export const OpenCaseForm = ({ onOpened }: { onOpened: (c: SupportCase) => void }) => {
  const [type, setType] = useState<CaseType>('INVOICE_EXPLANATION')
  const [severity, setSeverity] = useState<Severity>('S3')
  const [subject, setSubject] = useState('')
  const [description, setDescription] = useState('')
  const [product, setProduct] = useState('')
  const [legalEntityId, setLegalEntityId] = useState('')
  const [fiscalPeriod, setFiscalPeriod] = useState('')
  const [contractRef, setContractRef] = useState('')
  const [sellerRef, setSellerRef] = useState('')
  const [statutory, setStatutory] = useState('')
  const [decisionId, setDecisionId] = useState('')
  const [documentId, setDocumentId] = useState('')
  const [obligationId, setObligationId] = useState('')
  const [remittanceId, setRemittanceId] = useState('')
  const [evidence, setEvidence] = useState('')
  const [problem, setProblem] = useState<string | null>(null)
  const action = useAction<SupportCase>()

  const submit = (e: FormEvent) => {
    e.preventDefault()
    const ids: [string, string][] = [['legal entity', legalEntityId], ['decision', decisionId], ['document', documentId],
      ['obligation', obligationId], ['remittance', remittanceId]]
    const bad = ids.find(([, v]) => v.trim() !== '' && !UUID.test(v.trim()))
    if (bad !== undefined) {
      setProblem(`The ${bad[0]} reference is not an identifier.`)
      return
    }
    if (fiscalPeriod !== '' && !/^\d{4}-(0[1-9]|1[0-2])$/.test(fiscalPeriod)) {
      setProblem('The fiscal period is a month, YYYY-MM.')
      return
    }
    setProblem(null)
    const opt = (v: string) => (v.trim() === '' ? undefined : v.trim())
    const references = compact<S<'SupportCaseReferences'>>({
      decisionId: opt(decisionId), documentId: opt(documentId), obligationId: opt(obligationId), remittanceId: opt(remittanceId),
    })
    const body = compact<S<'SupportCaseRequest'>>({
      type, severity, subject: subject.trim(), description: description.trim(), product: opt(product),
      legalEntityId: opt(legalEntityId), fiscalPeriod: opt(fiscalPeriod), contractRef: opt(contractRef), sellerRef: opt(sellerRef),
      statutoryDueAt: utcTimestamp(statutory), references, evidence: evidenceList(evidence),
    })
    void action.run(() => ztax.openSupportCase(body)).then((c) => {
      if (c !== null) {
        setSubject('')
        setDescription('')
        setEvidence('')
        onOpened(c)
      }
    })
  }

  return (
    <Card title="Open a case">
      <p className="ztax-note">
        A case routes and prioritises a request. It names decisions, documents, obligations or payments only as references:
        nothing done here refunds, files, pays or changes a rule.
      </p>
      <form className="ztax-form" onSubmit={submit}>
        <Field label="Type">
          <select value={type} onChange={(e) => { setType(pick(TYPES, e.target.value) || 'INVOICE_EXPLANATION') }}>
            {TYPES.map((t) => <option key={t} value={t}>{words(t)}</option>)}
          </select>
        </Field>
        <Field label="Severity" help={`Support SLA: first response within ${SLA_TARGET[severity]}.`}>
          <select value={severity} onChange={(e) => { setSeverity(pick(SEVERITIES, e.target.value) || 'S3') }}>
            {SEVERITIES.map((s) => <option key={s} value={s}>{s} · {SLA_TARGET[s]}</option>)}
          </select>
        </Field>
        <Field label="Subject">
          <input required maxLength={200} value={subject} onChange={(e) => { setSubject(e.target.value) }} />
        </Field>
        <Field label="Description">
          <textarea required maxLength={5000} rows={4} value={description} onChange={(e) => { setDescription(e.target.value) }} />
        </Field>
        <Field label="Product (optional)">
          <input maxLength={200} value={product} onChange={(e) => { setProduct(e.target.value) }} />
        </Field>
        <Field label="Customer legal entity id (optional)">
          <input className="mono" value={legalEntityId} onChange={(e) => { setLegalEntityId(e.target.value) }} />
        </Field>
        <Field label="Impacted fiscal period (optional)" help="YYYY-MM">
          <input value={fiscalPeriod} placeholder="2026-09" onChange={(e) => { setFiscalPeriod(e.target.value) }} />
        </Field>
        <Field label="Contract reference (optional)">
          <input maxLength={200} value={contractRef} onChange={(e) => { setContractRef(e.target.value) }} />
        </Field>
        <Field label="Seller reference (optional)" help="When the seller is not a registered legal entity.">
          <input maxLength={200} value={sellerRef} onChange={(e) => { setSellerRef(e.target.value) }} />
        </Field>
        <Field label="Statutory deadline, UTC (optional)" help="Kept separate from the support SLA. Takes precedence when earlier.">
          <input type="datetime-local" value={statutory} onChange={(e) => { setStatutory(e.target.value) }} />
        </Field>
        <Field label="Decision id (optional)">
          <input className="mono" value={decisionId} onChange={(e) => { setDecisionId(e.target.value) }} />
        </Field>
        <Field label="Document id (optional)">
          <input className="mono" value={documentId} onChange={(e) => { setDocumentId(e.target.value) }} />
        </Field>
        <Field label="Obligation id (optional)">
          <input className="mono" value={obligationId} onChange={(e) => { setObligationId(e.target.value) }} />
        </Field>
        <Field label="Remittance id (optional)">
          <input className="mono" value={remittanceId} onChange={(e) => { setRemittanceId(e.target.value) }} />
        </Field>
        <Field label="Evidence references (optional)" help="Hashes or document ids, one per line.">
          <textarea rows={2} className="mono" value={evidence} onChange={(e) => { setEvidence(e.target.value) }} />
        </Field>
        <Submit busy={action.busy}>Open case</Submit>
      </form>
      {problem === null ? null : <p className="ztax-error-note" role="alert">{problem}</p>}
      <Outcome error={action.error} done={action.done === null ? null : <p className="muted">Opened. It is NEW until the requester is verified.</p>} />
    </Card>
  )
}
