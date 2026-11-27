/**
 * Pieces of the E-Invoicing / CTC page (Engineering Doc 21, G-030): the
 * vocabularies, what each status means in words, and which acts a record or
 * submission admits for the person looking at it.
 *
 * The cell enforces every rule — roles, four eyes, versions, gates, digests.
 * What this file decides is only what to offer, and when an act is not open
 * to this person, the reason to show instead of a dead button (Doc 21 §04
 * "Disabled controls always display the missing gate").
 */
import { ZoikoTaxError, type S } from '@zoikotax/sdk'

export type Applicability = S<'CTCApplicability'>
export type ApplicabilityStatus = S<'CTCApplicabilityStatus'>
export type Terms = S<'CTCTerms'>
export type Duty = S<'CTCDuty'>
export type Model = S<'CTCModel'>
export type Profile = S<'CTCProfile'>
export type Gate = S<'CTCGate'>
export type BuyerKind = S<'CTCBuyerKind'>
export type DocumentType = S<'CTCDocumentType'>
export type Validation = S<'CTCValidation'>
export type Submission = S<'CTCSubmission'>
export type SubmissionStatus = S<'CTCSubmissionStatus'>
export type ResponseKind = S<'CTCResponseKind'>
export type Action = S<'CTCAction'>

/** The sentence the page repeats wherever an outcome appears. */
export const NOT_CERTIFICATION =
  'No authority or partner connector is wired. A dispatch is the recorded, four-eyed authorisation of one attempt; what the rail and the authority answered is recorded by a person with its evidence. A PASSED validation is a structural pre-check, not certification.'

export const DOCUMENT_TYPES: readonly DocumentType[] = ['INVOICE', 'CREDIT_NOTE', 'DEBIT_NOTE', 'REBILL', 'ADJUSTMENT']
export const BUYER_KINDS: readonly BuyerKind[] = ['B2B', 'B2G', 'B2C']
export const DUTIES: readonly Duty[] = ['REQUIRED', 'OPTIONAL', 'PROHIBITED']
export const MODELS: readonly Model[] = ['CLEARANCE', 'REAL_TIME_REPORTING', 'POST_AUDIT', 'NETWORK_EXCHANGE', 'NONE']
export const APPLICABILITY_STATUSES: readonly ApplicabilityStatus[] = ['DRAFT', 'IN_REVIEW', 'ACTIVE', 'SUSPENDED', 'RETIRED']
export const SUBMISSION_STATUSES: readonly SubmissionStatus[] = [
  'PREPARED', 'SENT', 'TRANSPORT_RECEIVED', 'AUTHORITY_ACCEPTED', 'AUTHORITY_REJECTED', 'UNCERTAIN_EXTERNAL_STATE', 'WITHDRAWN',
]

/** The profiles and the one country a national profile belongs to. */
export const PROFILES: readonly { value: Profile; label: string; country: string | null }[] = [
  { value: 'PEPPOL_BIS_3', label: 'Peppol BIS Billing 3 (EN 16931)', country: null },
  { value: 'FR_EN16931', label: 'France — approved platform (EN 16931)', country: 'FR' },
  { value: 'IT_FATTURAPA', label: 'Italy — SdI FatturaPA', country: 'IT' },
  { value: 'PL_KSEF_FA', label: 'Poland — KSeF FA', country: 'PL' },
  { value: 'BR_NFCOM_62', label: 'Brazil — NFCom model 62', country: 'BR' },
  { value: 'SA_ZATCA_UBL', label: 'Saudi Arabia — ZATCA FATOORA', country: 'SA' },
  { value: 'IN_GST_IRP', label: 'India — GST IRP', country: 'IN' },
  { value: 'NONE', label: 'None (prohibited duty)', country: null },
]

/** The profiles a record for a country may name. */
export function profilesFor(country: string, duty: Duty): readonly Profile[] {
  if (duty === 'PROHIBITED') {
    return ['NONE']
  }
  return PROFILES.filter((p) => p.value !== 'NONE' && (p.country === null || p.country === country)).map((p) => p.value)
}

export const GATES: readonly { value: Gate; label: string }[] = [
  { value: 'G0_SCOPE_AUTHORITY', label: 'G0 Scope / authority' },
  { value: 'G1_LEGAL_CONTENT_RIGHTS', label: 'G1 Legal content / rights' },
  { value: 'G2_TECHNICAL_QA', label: 'G2 Technical / independent QA' },
  { value: 'G3_SECURITY_OPERATIONS', label: 'G3 Security / operations' },
  { value: 'G4_CONTROLLED_PRODUCTION', label: 'G4 Controlled production' },
]

export const gateLabel = (g: Gate) => GATES.find((x) => x.value === g)?.label ?? g

export const APPLICABILITY_MEANING: Record<ApplicabilityStatus, string> = {
  DRAFT: 'Not activated. Decides nothing.',
  IN_REVIEW: 'Not activated. Awaiting G0–G4 sign-offs and activation by another person.',
  ACTIVE: 'In force for its cell and dates.',
  SUSPENDED: 'Suspended: nothing new is validated or dispatched under it.',
  RETIRED: 'Closed. Kept as recorded.',
}

export const SUBMISSION_MEANING: Record<SubmissionStatus, string> = {
  PREPARED: 'Sealed; not yet dispatched.',
  SENT: 'An attempt was dispatched; no answer recorded yet.',
  TRANSPORT_RECEIVED: 'The rail received it. The authority outcome is still pending.',
  AUTHORITY_ACCEPTED: 'The authority accepted it.',
  AUTHORITY_REJECTED: 'The authority rejected it. Correct it with a new document in Rejections & Corrections.',
  UNCERTAIN_EXTERNAL_STATE: 'Nobody knows whether the authority has it. Enquire before anything is sent again.',
  WITHDRAWN: 'Withdrawn before dispatch.',
}

export const reasonOf = (error: unknown): string | null => (error instanceof ZoikoTaxError ? error.reasonCode : null)

/** Plain words for the reason codes this page expects. */
export function refusalText(reason: string | null): string | null {
  switch (reason) {
    case 'MAKER_CHECKER':
      return 'Four eyes: a different person must do this.'
    case 'VERSION_CONFLICT':
      return 'The record changed since you opened it. Re-read it and review the current version.'
    case 'CTC_GATE_SIGNOFF_MISSING':
      return 'A required release gate is not signed on the current version. The record stays NOT_ACTIVATED.'
    case 'CTC_EVIDENCE_MISSING':
      return 'Attach the legal source evidence before activation.'
    case 'CTC_APPLICABILITY_CONFLICT':
      return 'Another active or suspended record covers the same cell for overlapping dates.'
    case 'CTC_PROFILE_COUNTRY_MISMATCH':
      return 'That national format profile belongs to another country.'
    case 'CTC_APPLICABILITY_NOT_ACTIVE':
      return 'The record is not ACTIVE. Nothing is validated for dispatch or submitted under it.'
    case 'CTC_SUBMISSION_PROHIBITED':
      return 'The record says this document is PROHIBITED for its cell.'
    case 'CTC_DOCUMENT_MISMATCH':
      return 'The document is another legal entity’s or another document type.'
    case 'CTC_VALIDATION_REQUIRED':
      return 'Validate the document under this record first; a failed validation blocks submission.'
    case 'CTC_VALIDATION_STALE':
      return 'The document, the record version or the ruleset changed since validation. Validate and prepare again.'
    case 'CTC_PAYLOAD_DIGEST_MISMATCH':
      return 'The submission is not the one you reviewed. Re-read it.'
    case 'CTC_OUTCOME_UNCERTAIN':
      return 'The outcome of the last attempt is unknown. Record a status enquiry first.'
    case 'CTC_FINAL_OUTCOME_RECORDED':
      return 'The authority’s final outcome is already recorded. A later answer never overwrites it.'
    case 'CTC_RULE_OUT_OF_EFFECT':
      return 'The document’s issue date is outside the record’s effective dates.'
    default:
      return null
  }
}

/** An act the page can offer, or the reason it does not. */
export interface Offer {
  readonly act: Action | 'SIGN' | 'EVIDENCE' | 'REVISE'
  readonly label: string
  /** Null when the act is open to this person; otherwise why it is not. */
  readonly blocked: string | null
}

const has = (roles: readonly string[], ...wanted: readonly string[]) => wanted.some((r) => roles.includes(r))

/** Who recorded the current terms version. */
const author = (a: Applicability) => a.versions[a.versions.length - 1]?.recordedBy

/**
 * The acts a record admits for this person. Mirrors the cell's rules so the
 * page never shows a dead button; the cell still decides.
 */
export function recordOffers(a: Applicability, roles: readonly string[], me: string): Offer[] {
  const maker = has(roles, 'OPERATOR', 'ANALYST')
  const specialist = has(roles, 'ANALYST')
  const makerOnly = 'Needs the OPERATOR or ANALYST role.'
  const specialistOnly = 'Needs the ANALYST role (tax specialist).'
  const ownRecord = a.createdBy === me || author(a) === me
  const own = 'You recorded this record or its current terms; a different person signs and activates it.'
  const out: Offer[] = []
  if (a.status === 'DRAFT' || a.status === 'IN_REVIEW') {
    out.push({ act: 'REVISE', label: 'Revise terms', blocked: maker ? null : makerOnly })
  }
  switch (a.status) {
    case 'DRAFT':
      out.push({ act: 'SUBMIT', label: 'Submit for review', blocked: maker ? null : makerOnly })
      break
    case 'IN_REVIEW':
      out.push({
        act: 'SIGN', label: 'Sign a release gate',
        blocked: !specialist ? specialistOnly : ownRecord ? own : a.missingGates.length === 0 ? 'Every required gate is signed on this version.' : null,
      })
      out.push({
        act: 'ACTIVATE', label: `Activate version ${a.currentVersion}`,
        blocked: !specialist ? specialistOnly : ownRecord ? own
          : a.missingGates.length > 0 ? `Not signed: ${a.missingGates.map(gateLabel).join(', ')}.`
          : a.evidence.length === 0 ? 'No evidence attached.' : null,
      })
      out.push({ act: 'RETURN', label: 'Return to draft', blocked: specialist ? null : specialistOnly })
      break
    case 'ACTIVE':
      out.push({ act: 'SUSPEND', label: 'Suspend (emergency)', blocked: maker ? null : makerOnly })
      break
    case 'SUSPENDED': {
      const suspender = [...a.history].reverse().find((e) => e.kind === 'SUSPENDED')?.recordedBy
      out.push({
        act: 'RESUME', label: 'Resume',
        blocked: !specialist ? specialistOnly : suspender === me ? 'You suspended it; a different person resumes it.' : null,
      })
      break
    }
    default:
      break
  }
  if (a.status !== 'RETIRED') {
    out.push({ act: 'EVIDENCE', label: 'Attach evidence', blocked: maker ? null : makerOnly })
    out.push({ act: 'RETIRE', label: 'Retire', blocked: specialist ? null : specialistOnly })
  }
  return out
}

/** Whether a record can carry documents to an authority now. */
export const dispatches = (a: Applicability) => a.status === 'ACTIVE' && a.terms.duty !== 'PROHIBITED'

/** The answers that apply to a submission in its current state. */
export function responseKinds(s: Submission): ResponseKind[] {
  switch (s.status) {
    case 'SENT':
      return ['TRANSPORT_RECEIVED', 'AUTHORITY_ACCEPTED', 'AUTHORITY_REJECTED', 'OUTCOME_UNKNOWN']
    case 'TRANSPORT_RECEIVED':
      return ['AUTHORITY_ACCEPTED', 'AUTHORITY_REJECTED', 'OUTCOME_UNKNOWN']
    case 'UNCERTAIN_EXTERNAL_STATE':
      return ['STATUS_NOT_FOUND', 'TRANSPORT_RECEIVED', 'AUTHORITY_ACCEPTED', 'AUTHORITY_REJECTED']
    default:
      return []
  }
}

/** The latest dispatch attempt, which an answer is about. */
export const lastAttempt = (s: Submission): string | undefined =>
  [...s.history].reverse().find((e) => e.kind === 'DISPATCHED')?.attemptId

/** Whether this person may dispatch the submission now, or why not. */
export function dispatchBlocked(s: Submission, roles: readonly string[], me: string): string | null {
  if (!has(roles, 'OPERATOR', 'ANALYST')) {
    return 'Needs the OPERATOR or ANALYST role.'
  }
  const last = s.history[s.history.length - 1]
  const resendable = s.status === 'UNCERTAIN_EXTERNAL_STATE' && last?.kind === 'STATUS_NOT_FOUND'
  if (s.status !== 'PREPARED' && !resendable) {
    return s.status === 'UNCERTAIN_EXTERNAL_STATE'
      ? 'The outcome is unknown: record a status enquiry that found nothing before sending again.'
      : `A ${s.status.replaceAll('_', ' ')} submission is not dispatched.`
  }
  return s.preparedBy === me ? 'You prepared this submission; a different person dispatches it.' : null
}

/** What each answer must carry, for the form. */
export function responseNeeds(kind: ResponseKind): { transportRef: boolean; authorityRef: boolean; authorityCode: boolean; evidence: boolean; detail: boolean } {
  return {
    transportRef: kind === 'TRANSPORT_RECEIVED' || kind === 'STATUS_NOT_FOUND',
    authorityRef: kind === 'AUTHORITY_ACCEPTED',
    authorityCode: kind === 'AUTHORITY_REJECTED',
    evidence: kind === 'AUTHORITY_ACCEPTED' || kind === 'AUTHORITY_REJECTED',
    detail: kind === 'AUTHORITY_REJECTED' || kind === 'OUTCOME_UNKNOWN' || kind === 'STATUS_NOT_FOUND',
  }
}

/** A validation's results counted by outcome. */
export function ruleCounts(v: Validation): { pass: number; fail: number; fatal: number; notEvaluated: number } {
  let pass = 0
  let fail = 0
  let fatal = 0
  let notEvaluated = 0
  for (const r of v.results) {
    if (r.outcome === 'PASS') {
      pass++
    } else if (r.outcome === 'FAIL') {
      fail++
      if (r.severity === 'FATAL') {
        fatal++
      }
    } else {
      notEvaluated++
    }
  }
  return { pass, fail, fatal, notEvaluated }
}

const DIGEST = /^[0-9a-f]{64}$/

/** A pasted SHA-256 of a raw receipt, normalised, or null when not one. */
export function receiptDigest(value: string): string | null {
  const v = value.trim().toLowerCase()
  return DIGEST.test(v) ? v : null
}

/** SHA-256 of a file, hex, computed here in the browser. */
export async function sha256Of(file: Blob): Promise<string> {
  const buf = await crypto.subtle.digest('SHA-256', await file.arrayBuffer())
  return Array.from(new Uint8Array(buf), (b) => b.toString(16).padStart(2, '0')).join('')
}
