import { useState, type FormEvent } from 'react'

import type { S } from '@zoikotax/sdk'

import { civilDate, instant } from '../../dashboard/format'
import { ztax } from '../../platform/client'
import { Card, Empty, Field, Id, Loaded, Outcome, Status, Submit, useAction, useLoad } from '../../ui/kit'
import { fromDateInput, lines } from '../evidence/time'
import { LegalStatusTag } from './GoverningCards'

type Authorization = S<'CustomerAuthorization'>
type AuthType = S<'AuthorizationType'>
type Permission = S<'AuthorizationPermission'>
type Service = S<'ServiceState'>
type Check = S<'AuthorizationCheck'>

const AUTH_TYPES: readonly AuthType[] = ['NONE', 'CONTRACT', 'DECLARATION', 'POA', 'TAX_INFORMATION', 'PORTAL_DELEGATION', 'FILING_MANDATE']
const PERMISSIONS: readonly Permission[] = ['READ_INFO', 'PREPARE', 'SUBMIT', 'RECEIVE_NOTICE', 'REPRESENT', 'SIGN']
const SERVICES: readonly Service[] = ['INFORM', 'COMPUTE', 'PREPARE', 'FILE', 'REPRESENT', 'ADVISE']

const PERIOD = /^\d{4}-(0[1-9]|1[0-2])$/

const periodsText = (a: Authorization) =>
  a.periodFrom === undefined && a.periodTo === undefined ? 'any' : `${a.periodFrom ?? '…'} → ${a.periodTo ?? '…'}`

// ---------------------------------------------------------------------------
// list, revoke
// ---------------------------------------------------------------------------

export const AuthorizationsCard = ({ admin, reloadKey, onReload }: { admin: boolean; reloadKey: number; onReload: () => void }) => {
  const { load } = useLoad((signal) => ztax.listAuthorizations(signal), [reloadKey])
  const [revoking, setRevoking] = useState<string | null>(null)

  return (
    <Card title="Customer authorizations" aside={<button type="button" className="ztax-btn-quiet" onClick={onReload}>Refresh</button>}>
      <p className="muted zp-intro">
        Each customer grant to one authority, newest first, with whether it is in force now: active, effective and not
        expired.
      </p>
      <Loaded load={load}>
        {(data) =>
          data.authorizations.length === 0 ? (
            <Empty>No authorization is recorded. Every action a rule puts under DIRECT WITH AUTH is blocked.</Empty>
          ) : (
            <div className="zp-scroll">
              <table className="table">
                <thead>
                  <tr>
                    <th scope="col">Authority</th>
                    <th scope="col">Type</th>
                    <th scope="col">Status</th>
                    <th scope="col">Permissions</th>
                    <th scope="col">Periods</th>
                    <th scope="col">Matters</th>
                    <th scope="col">Effective</th>
                    <th scope="col">Expires</th>
                    <th scope="col">Id</th>
                    {admin ? <th scope="col"><span className="zp-sr-only">Revoke</span></th> : null}
                  </tr>
                </thead>
                <tbody>
                  {data.authorizations.map((a) => (
                    <AuthorizationRows
                      key={a.id}
                      a={a}
                      admin={admin}
                      open={revoking === a.id}
                      onToggle={() => { setRevoking((cur) => (cur === a.id ? null : a.id)) }}
                      onRevoked={() => {
                        setRevoking(null)
                        onReload()
                      }}
                    />
                  ))}
                </tbody>
              </table>
            </div>
          )
        }
      </Loaded>
    </Card>
  )
}

const AuthorizationRows = ({ a, admin, open, onToggle, onRevoked }: {
  a: Authorization
  admin: boolean
  open: boolean
  onToggle: () => void
  onRevoked: () => void
}) => {
  const cols = admin ? 10 : 9
  return (
    <>
      <tr>
        <td>
          {a.country} · <span className="mono">{a.authority}</span>
        </td>
        <td>{a.type.replaceAll('_', ' ')}</td>
        <td>
          <Status value={a.status} />{' '}
          {a.inForce ? <span className="tag tag--ok">in force</span> : <span className="tag tag--warn">not in force</span>}
        </td>
        <td>{a.permissions.map((p) => <span key={p} className="tag">{p}</span>)}</td>
        <td className="mono">{periodsText(a)}</td>
        <td>{a.matters.length === 0 ? 'any' : a.matters.join(', ')}</td>
        <td style={{ whiteSpace: 'nowrap' }}>{civilDate(a.effectiveFrom.slice(0, 10))}</td>
        <td style={{ whiteSpace: 'nowrap' }}>{a.expiresAt === undefined ? 'no expiry' : civilDate(a.expiresAt.slice(0, 10))}</td>
        <td><Id value={a.id} /></td>
        {admin ? (
          <td>
            {a.status === 'ACTIVE' ? (
              <button type="button" className="ztax-btn-quiet" aria-expanded={open} onClick={onToggle}>
                {open ? 'Cancel' : 'Revoke'}
              </button>
            ) : null}
          </td>
        ) : null}
      </tr>
      <tr className="zp-subrow">
        <td colSpan={cols}>
          <details>
            <summary className="muted" style={{ cursor: 'pointer', fontSize: 12 }}>
              Evidence and history ({a.history.length})
            </summary>
            <dl className="ztax-kv">
              <dt>Legal entity</dt>
              <dd className="mono">{a.legalEntityId}</dd>
              {a.representative === undefined ? null : (
                <>
                  <dt>Representative</dt>
                  <dd>{a.representative}</dd>
                </>
              )}
              <dt>Evidence</dt>
              <dd>{a.evidence.map((e) => <span key={e} className="tag mono">{e}</span>)}</dd>
              <dt>Credential</dt>
              <dd className="mono">{a.credentialRef ?? '—'}</dd>
              {a.supersedes === undefined ? null : (
                <>
                  <dt>Supersedes</dt>
                  <dd className="mono">{a.supersedes}</dd>
                </>
              )}
            </dl>
            <ol className="zp-list">
              {a.history.map((h) => (
                <li key={h.seq}>
                  <strong>{h.kind.toLowerCase()}</strong> · {instant(h.recordedAt)}
                  {h.reason === undefined ? null : <> · “{h.reason}”</>}
                  {h.supersededBy === undefined ? null : <> · by <span className="mono">{h.supersededBy}</span></>}
                </li>
              ))}
            </ol>
          </details>
        </td>
      </tr>
      {open ? (
        <tr className="zp-subrow">
          <td colSpan={cols}>
            <RevokeForm a={a} onRevoked={onRevoked} />
          </td>
        </tr>
      ) : null}
    </>
  )
}

const RevokeForm = ({ a, onRevoked }: { a: Authorization; onRevoked: () => void }) => {
  const [reason, setReason] = useState('')
  const revoke = useAction<Authorization>()

  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    if (!window.confirm(`Revoke the ${a.type.replaceAll('_', ' ')} for ${a.authority}? This is final and immediate: every action that depends on it is blocked from now on.`)) {
      return
    }
    void revoke.run(() => ztax.revokeAuthorization(a.id, { reason: reason.trim() })).then((r) => {
      if (r !== null) onRevoked()
    })
  }

  return (
    <form className="ztax-form" onSubmit={onSubmit} aria-label={`Revoke authorization ${a.id}`}>
      <Field label="Why it is revoked" help="Final and immediate. The grant and its history stay.">
        <input value={reason} required maxLength={1000} placeholder="mandate withdrawn" onChange={(e) => { setReason(e.target.value) }} />
      </Field>
      <div className="ztax-form-actions">
        <Submit busy={revoke.busy}>Revoke</Submit>
      </div>
      <Outcome error={revoke.error} done={null} />
    </form>
  )
}

// ---------------------------------------------------------------------------
// grant
// ---------------------------------------------------------------------------

export const GrantForm = ({ admin, onGranted }: { admin: boolean; onGranted: () => void }) => {
  const [country, setCountry] = useState('')
  const [authority, setAuthority] = useState('')
  const [type, setType] = useState<AuthType>('FILING_MANDATE')
  const [permissions, setPermissions] = useState<Permission[]>([])
  const [matters, setMatters] = useState('')
  const [periodFrom, setPeriodFrom] = useState('')
  const [periodTo, setPeriodTo] = useState('')
  const [representative, setRepresentative] = useState('')
  const [effectiveFrom, setEffectiveFrom] = useState('')
  const [expiresAt, setExpiresAt] = useState('')
  const [evidence, setEvidence] = useState('')
  const [credentialRef, setCredentialRef] = useState('')
  const [legalEntityId, setLegalEntityId] = useState('')
  const [supersedes, setSupersedes] = useState('')
  const [local, setLocal] = useState<string | null>(null)
  const grant = useAction<Authorization>()

  if (!admin) {
    return (
      <Card title="Record a customer authorization">
        <p className="muted zp-intro">
          Recording or revoking an authorization needs the ADMIN role, signed in as yourself — never system work or a
          model.
        </p>
      </Card>
    )
  }

  const toggle = (p: Permission) => {
    setPermissions((cur) => (cur.includes(p) ? cur.filter((x) => x !== p) : [...cur, p]))
  }

  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    const from = fromDateInput(effectiveFrom)
    const refs = lines(evidence)
    if (from === null) return setLocal('Give the date the grant takes effect.')
    if (permissions.length === 0) return setLocal('Choose at least one permission.')
    if (refs.length === 0) return setLocal('Name at least one piece of evidence of the grant.')
    if ((periodFrom !== '' && !PERIOD.test(periodFrom)) || (periodTo !== '' && !PERIOD.test(periodTo))) {
      return setLocal('Periods are months, YYYY-MM.')
    }
    const body: S<'AuthorizationRequest'> = {
      country: country.trim().toUpperCase(),
      authority: authority.trim(),
      type,
      permissions: PERMISSIONS.filter((p) => permissions.includes(p)),
      effectiveFrom: from,
      evidence: refs,
    }
    const ms = lines(matters)
    if (ms.length > 0) body.matters = ms
    if (periodFrom !== '') body.periodFrom = periodFrom
    if (periodTo !== '') body.periodTo = periodTo
    if (representative.trim() !== '') body.representative = representative.trim()
    if (expiresAt !== '') {
      const exp = fromDateInput(expiresAt)
      if (exp === null) return setLocal('The expiry is a date.')
      body.expiresAt = exp
    }
    if (credentialRef.trim() !== '') body.credentialRef = credentialRef.trim()
    if (legalEntityId.trim() !== '') body.legalEntityId = legalEntityId.trim()
    if (supersedes.trim() !== '') body.supersedes = supersedes.trim()
    setLocal(null)
    void grant.run(() => ztax.grantAuthorization(body)).then((a) => {
      if (a !== null) onGranted()
    })
  }

  return (
    <Card title="Record a customer authorization">
      <p className="muted zp-intro">
        A customer's grant to one authority: a power of attorney, a filing mandate, a portal delegation, an information
        authorization (which is not representation). A credential is a vault reference, never the credential. Naming a
        grant it supersedes closes that grant in the same act.
      </p>
      <form className="ztax-form" onSubmit={onSubmit} aria-label="Record a customer authorization">
        <Field label="Country">
          <input value={country} required pattern="[A-Za-z]{2}" maxLength={2} placeholder="DE" onChange={(e) => { setCountry(e.target.value) }} />
        </Field>
        <Field label="Authority">
          <input className="mono" value={authority} required maxLength={128} placeholder="DE-ELSTER" onChange={(e) => { setAuthority(e.target.value) }} />
        </Field>
        <Field label="Type">
          <select value={type} onChange={(e) => { setType(e.target.value as AuthType) }}>
            {AUTH_TYPES.map((t) => <option key={t} value={t}>{t.replaceAll('_', ' ')}</option>)}
          </select>
        </Field>
        <fieldset className="zp-checks">
          <legend>Permissions</legend>
          {PERMISSIONS.map((p) => (
            <label key={p}>
              <input type="checkbox" checked={permissions.includes(p)} onChange={() => { toggle(p) }} />
              {p.replaceAll('_', ' ')}
            </label>
          ))}
        </fieldset>
        <Field label="Effective from (UTC)">
          <input type="date" value={effectiveFrom} required onChange={(e) => { setEffectiveFrom(e.target.value) }} />
        </Field>
        <Field label="Expires (UTC, optional)">
          <input type="date" value={expiresAt} onChange={(e) => { setExpiresAt(e.target.value) }} />
        </Field>
        <Field label="Period from (optional)">
          <input type="month" value={periodFrom} onChange={(e) => { setPeriodFrom(e.target.value) }} />
        </Field>
        <Field label="Period to (optional)">
          <input type="month" value={periodTo} onChange={(e) => { setPeriodTo(e.target.value) }} />
        </Field>
        <Field label="Matters (one per line, optional)">
          <textarea rows={2} value={matters} placeholder="UStVA" onChange={(e) => { setMatters(e.target.value) }} />
        </Field>
        <Field label="Evidence (one reference per line)" help="The signed artifact, consent proof or acknowledgement.">
          <textarea className="mono" rows={2} value={evidence} required placeholder="doc:mandate-2026.pdf" onChange={(e) => { setEvidence(e.target.value) }} />
        </Field>
        <Field label="Credential reference (optional)" help="vault://, awssm://, gcpsm:// or azkv://">
          <input
            className="mono"
            value={credentialRef}
            pattern="(vault|awssm|gcpsm|azkv)://[A-Za-z0-9._/\-]{1,240}"
            placeholder="vault://tax/de/elster/cert"
            onChange={(e) => { setCredentialRef(e.target.value) }}
          />
        </Field>
        <Field label="Representative (optional)">
          <input value={representative} maxLength={255} onChange={(e) => { setRepresentative(e.target.value) }} />
        </Field>
        <Field label="Legal entity id (optional)" help="The tenant's default entity when empty.">
          <input className="mono" value={legalEntityId} onChange={(e) => { setLegalEntityId(e.target.value) }} />
        </Field>
        <Field label="Supersedes (optional)" help="An authorization id this grant replaces.">
          <input className="mono" value={supersedes} onChange={(e) => { setSupersedes(e.target.value) }} />
        </Field>
        <div className="ztax-form-actions">
          <Submit busy={grant.busy}>Record authorization</Submit>
        </div>
      </form>
      {local === null ? null : <p className="ztax-error-note" role="alert">{local}</p>}
      <Outcome
        error={grant.error}
        done={
          grant.done === null ? null : (
            <p className="ztax-ok-note" role="status">
              Recorded <span className="mono">{grant.done.id}</span>
              {grant.done.inForce ? ', in force now.' : ', not yet in force.'}
            </p>
          )
        }
      />
    </Card>
  )
}

// ---------------------------------------------------------------------------
// check
// ---------------------------------------------------------------------------

export interface CheckDraft {
  country: string
  authority: string
  service: Service
  period: string
  legalEntityId: string
  matter: string
}

export const CheckForm = ({ draft, onChange }: { draft: CheckDraft; onChange: (d: CheckDraft) => void }) => {
  const check = useAction<Check>()

  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    const body: S<'AuthorizationCheckRequest'> = {
      country: draft.country.trim().toUpperCase(),
      authority: draft.authority.trim(),
      service: draft.service,
    }
    if (draft.period !== '') body.period = draft.period
    if (draft.legalEntityId.trim() !== '') body.legalEntityId = draft.legalEntityId.trim()
    if (draft.matter.trim() !== '') body.matter = draft.matter.trim()
    void check.run(() => ztax.checkAuthorization(body))
  }

  const c = check.error === null ? check.done : null

  return (
    <Card title="Check an action">
      <p className="muted zp-intro">
        The gate every legally sensitive action passes, asked directly. It fails closed and names its reason. Nothing is
        recorded by asking.
      </p>
      <form className="ztax-form" onSubmit={onSubmit} aria-label="Check whether an action may execute">
        <Field label="Country">
          <input value={draft.country} required pattern="[A-Za-z]{2}" maxLength={2} placeholder="DE" onChange={(e) => { onChange({ ...draft, country: e.target.value }) }} />
        </Field>
        <Field label="Authority">
          <input className="mono" value={draft.authority} required maxLength={128} placeholder="DE-ELSTER" onChange={(e) => { onChange({ ...draft, authority: e.target.value }) }} />
        </Field>
        <Field label="Service">
          <select value={draft.service} onChange={(e) => { onChange({ ...draft, service: e.target.value as Service }) }}>
            {SERVICES.map((s) => <option key={s} value={s}>{s}</option>)}
          </select>
        </Field>
        <Field label="Period (optional)">
          <input type="month" value={draft.period} onChange={(e) => { onChange({ ...draft, period: e.target.value }) }} />
        </Field>
        <Field label="Matter (optional)">
          <input value={draft.matter} maxLength={255} onChange={(e) => { onChange({ ...draft, matter: e.target.value }) }} />
        </Field>
        <Field label="Legal entity id (optional)">
          <input className="mono" value={draft.legalEntityId} onChange={(e) => { onChange({ ...draft, legalEntityId: e.target.value }) }} />
        </Field>
        <div className="ztax-form-actions">
          <Submit busy={check.busy}>Check</Submit>
        </div>
      </form>
      <Outcome
        error={check.error}
        done={
          c === null ? null : (
            <dl className="ztax-kv" aria-live="polite">
              <dt>Allowed</dt>
              <dd>
                {c.allowed ? (
                  <span className="ztax-status ztax-status--ok">ALLOWED</span>
                ) : (
                  <span className="ztax-status ztax-status--bad">BLOCKED</span>
                )}
              </dd>
              <dt>Status</dt>
              <dd><LegalStatusTag value={c.status} /></dd>
              <dt>Reason</dt>
              <dd className="mono">{c.reason ?? '—'}</dd>
              {c.detail === undefined ? null : (
                <>
                  <dt>Detail</dt>
                  <dd>{c.detail}</dd>
                </>
              )}
              <dt>Rule</dt>
              <dd className="mono">{c.rule === undefined ? '—' : `${c.rule.id} v${c.rule.version}`}</dd>
              <dt>Authorization</dt>
              <dd className="mono">{c.authorizationId ?? '—'}</dd>
              <dt>Matrix</dt>
              <dd className="mono">
                {c.matrixVersion === undefined || c.matrixVersion === '' ? '—' : c.matrixVersion}
                {c.matrixDigest === undefined ? null : <span className="muted"> · {c.matrixDigest.slice(0, 16)}…</span>}
              </dd>
              <dt>Checked at</dt>
              <dd>{instant(c.checkedAt)}</dd>
            </dl>
          )
        }
      />
    </Card>
  )
}
