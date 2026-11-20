/**
 * Pieces of the Registrations & Exemptions page (Engineering Doc 17): the
 * vocabularies, what each status means in words, and which acts a record
 * admits for the person looking at it.
 *
 * The cell enforces every rule — roles, four eyes, versions, dates. What this
 * file decides is only what to offer, and when an act is not open to this
 * person, the reason to show instead of a dead button (Doc 17 "Accessible /
 * usable design").
 */
import { ZoikoTaxError, type S } from '@zoikotax/sdk'

export type Registration = S<'Registration'>
export type RegistrationStatus = S<'RegistrationStatus'>
export type RegistrationScheme = S<'RegistrationScheme'>
export type Certificate = S<'ExemptionEvidence'>
export type CertificateStatus = S<'ExemptionEvidenceStatus'>
export type CertificateType = S<'ExemptionEvidenceType'>
export type EvidenceFlag = S<'EvidenceFlag'>
export type EvidenceDocument = S<'EvidenceDocument'>
export type CheckSource = S<'AuthorityCheckSource'>
export type CheckOutcome = S<'AuthorityCheckOutcome'>
export type CheckResult = S<'ScreeningCheckResult'>
export type ScreeningResult = S<'ScreeningResult'>

/** The one sentence the page repeats wherever a status appears (NAV-008). */
export const NOT_TAX_TREATMENT =
  'A registration or an approved certificate is evidence. Neither changes how any transaction is taxed: the determination engine applies its own approved rules, and nothing already committed is rewritten.'

export const SCHEMES: readonly { value: RegistrationScheme; label: string; hint: string; sources: readonly CheckSource[] }[] = [
  { value: 'US_FCC_FRN', label: 'US FCC Registration Number (FRN)', hint: '10 digits', sources: ['FCC'] },
  { value: 'US_USAC_499_FILER_ID', label: 'US USAC 499 Filer ID', hint: '6 digits', sources: ['USAC'] },
  { value: 'US_USAC_498_ID', label: 'US USAC 498 ID / SPIN', hint: '9 digits starting 143', sources: ['USAC'] },
  { value: 'US_STATE_SALES_TAX_PERMIT', label: 'US state sales-tax permit', hint: 'as issued; jurisdiction US-XX', sources: ['STATE_REVENUE_AUTHORITY'] },
  { value: 'US_STATE_TELECOM_REGISTRATION', label: 'US state telecom registration', hint: 'as issued; jurisdiction US-XX', sources: ['STATE_REVENUE_AUTHORITY', 'OTHER_AUTHORITY'] },
  { value: 'UK_VAT', label: 'UK VAT (HMRC)', hint: 'GB and 9 or 12 digits', sources: ['HMRC'] },
  { value: 'EU_VAT', label: 'EU VAT (VIES)', hint: 'member-state or XI prefix', sources: ['VIES'] },
  { value: 'CA_GST_HST', label: 'Canada GST/HST', hint: '9 digits, RT, 4 digits', sources: ['CRA'] },
  { value: 'OTHER_REGISTRATION', label: 'Other registration', hint: 'as issued', sources: ['FCC', 'USAC', 'HMRC', 'VIES', 'CRA', 'STATE_REVENUE_AUTHORITY', 'OTHER_AUTHORITY'] },
]

export const REGISTRATION_STATUSES: readonly RegistrationStatus[] = [
  'DRAFT', 'SUBMITTED', 'VERIFIED', 'ACTIVE', 'SUSPENDED', 'EXPIRED', 'REVOKED', 'WITHDRAWN',
]

export const CERTIFICATE_STATUSES: readonly CertificateStatus[] = [
  'SUBMITTED', 'SCREENING', 'REVIEW_REQUIRED', 'APPROVED', 'REJECTED', 'EXPIRED', 'SUSPENDED', 'REVOKED', 'SUPERSEDED',
]

export const CERTIFICATE_TYPES: readonly { value: CertificateType; label: string }[] = [
  { value: 'RESALE', label: 'Resale' },
  { value: 'WHOLESALE_TELECOM', label: 'Wholesale telecom' },
  { value: 'EXEMPT_ORGANIZATION', label: 'Exempt organization' },
  { value: 'GOVERNMENT_ENTITY', label: 'Government entity' },
  { value: 'DIPLOMATIC', label: 'Diplomatic' },
  { value: 'DIRECT_PAY_PERMIT', label: 'Direct-pay permit' },
  { value: 'OTHER_EXEMPTION', label: 'Other exemption' },
]

export const OUTCOMES: readonly { value: CheckOutcome; label: string }[] = [
  { value: 'VERIFIED', label: 'Verified — the authority confirmed the number' },
  { value: 'NEGATIVE', label: 'Negative — the authority did not confirm it' },
  { value: 'INCONCLUSIVE', label: 'Inconclusive — no clear answer' },
  { value: 'UNAVAILABLE', label: 'Unavailable — the authority service was down or throttled' },
]

export const CRITERIA: readonly { key: 'sellerMatches' | 'buyerMatches' | 'jurisdictionMatches' | 'formAccepted' | 'signaturePresent' | 'datesValid' | 'numberPresent'; label: string }[] = [
  { key: 'sellerMatches', label: 'Seller matches the legal entity' },
  { key: 'buyerMatches', label: 'Buyer matches the customer' },
  { key: 'jurisdictionMatches', label: 'Jurisdiction matches' },
  { key: 'formAccepted', label: "The authority's form is used" },
  { key: 'signaturePresent', label: 'Signed where required' },
  { key: 'datesValid', label: 'Dates present and in force' },
  { key: 'numberPresent', label: 'Certificate number present' },
]

export const FLAG_TEXT: Readonly<Record<EvidenceFlag, string>> = {
  EXPIRING_SOON: 'Expires within 60 days',
  PAST_EXPIRY: 'Past its end date — record the expiry or a renewal',
  NOT_YET_EFFECTIVE: 'Not yet effective',
  PENDING_VALIDATION: 'Authority check inconclusive or unavailable — not valid',
  AUTHORITY_NEGATIVE: 'Authority did not confirm the number — review',
  AWAITING_ACTIVATION: 'Verified; awaiting a second person to activate',
  CORRECTION_REQUESTED: 'Incomplete — waiting for a corrected document',
  AWAITING_SECOND_REVIEW: 'Recommended; awaiting a different checker',
}

/** What a registration status means, in a sentence. */
export const REGISTRATION_MEANING: Readonly<Record<RegistrationStatus, string>> = {
  DRAFT: 'Recorded, not submitted. Not a registration claim.',
  SUBMITTED: 'Submitted for review. Not verified, not active.',
  VERIFIED: 'The authority confirmed the number. Not active until a second person activates it.',
  ACTIVE: 'Active evidence of registration within its dates. Not authority to file.',
  SUSPENDED: 'Suspended pending review. Not in force.',
  EXPIRED: 'Ended. Kept as recorded.',
  REVOKED: 'Revoked. Kept as recorded.',
  WITHDRAWN: 'Withdrawn before review. Kept as recorded.',
}

export const CERTIFICATE_MEANING: Readonly<Record<CertificateStatus, string>> = {
  SUBMITTED: 'Received, not screened. No tax effect.',
  SCREENING: 'Screened incomplete or inconsistent; waiting for a corrected document.',
  REVIEW_REQUIRED: 'Screened; waiting for a recommendation and a second reviewer.',
  APPROVED: 'Approved as evidence for its buyer, seller, jurisdiction and dates. Not a tax exemption.',
  REJECTED: 'Rejected. Kept as recorded.',
  EXPIRED: 'Ended. Kept as recorded; a renewal is a new certificate.',
  SUSPENDED: 'Suspended pending review. Not in force.',
  REVOKED: 'Revoked. Kept as recorded.',
  SUPERSEDED: 'Replaced by an approved renewal. Kept as recorded.',
}

export const reasonOf = (error: unknown): string | null => (error instanceof ZoikoTaxError ? error.reasonCode : null)

/** Plain words for the reason codes this page expects. */
export function refusalText(reason: string | null): string | null {
  switch (reason) {
    case 'MAKER_CHECKER':
      return 'Four eyes: a different person must do this.'
    case 'VERSION_CONFLICT':
      return 'The record changed since you opened it. Re-read it and review the current version.'
    case 'REGISTRATION_SCHEME_MISMATCH':
      return 'That number belongs to a different scheme. It is never converted; enter it under its own scheme.'
    case 'EVIDENCE_PROVENANCE_CONFLICT':
      return 'This evidence already belongs to a different buyer or legal entity.'
    case 'IDEMPOTENCY_KEY_REUSE':
      return 'This form was already sent with different details. Start a new one.'
    default:
      return null
  }
}

/** An act the page can offer, or the reason it does not. */
export interface Offer {
  readonly act: string
  readonly label: string
  /** Null when the act is open to this person; otherwise why it is not. */
  readonly blocked: string | null
}

const has = (roles: readonly string[], ...wanted: readonly string[]) => wanted.some((r) => roles.includes(r))

/**
 * The acts a registration admits for this person. Mirrors the cell's rules so
 * the page never shows a dead button; the cell still decides.
 */
export function registrationOffers(r: Registration, roles: readonly string[], me: string): Offer[] {
  const recorder = has(roles, 'OPERATOR', 'ANALYST')
  const specialist = has(roles, 'ANALYST')
  const out: Offer[] = []
  const need = (ok: boolean, why: string) => (ok ? null : why)
  const specialistOnly = 'Needs the ANALYST role.'
  const lastSuspender = [...r.history].reverse().find((e) => e.kind === 'SUSPENDED')?.recordedBy
  switch (r.status) {
    case 'DRAFT':
      out.push({ act: 'SUBMITTED', label: 'Submit for review', blocked: need(recorder, 'Needs the OPERATOR or ANALYST role.') })
      out.push({ act: 'WITHDRAWN', label: 'Withdraw', blocked: need(recorder, 'Needs the OPERATOR or ANALYST role.') })
      break
    case 'SUBMITTED':
      out.push({ act: 'CHECK', label: 'Record authority check', blocked: need(specialist, specialistOnly) })
      out.push({ act: 'DRAFT', label: 'Return for evidence', blocked: need(specialist, specialistOnly) })
      out.push({ act: 'WITHDRAWN', label: 'Withdraw', blocked: need(recorder, 'Needs the OPERATOR or ANALYST role.') })
      break
    case 'VERIFIED':
      out.push({
        act: 'ACTIVE', label: 'Activate',
        blocked: !specialist ? specialistOnly
          : r.submittedBy === me ? 'You submitted this registration; a different person activates it.'
          : r.flags.includes('NOT_YET_EFFECTIVE') ? `Effective from ${r.effectiveFrom}; it is activated on or after that date.` : null,
      })
      out.push({ act: 'CHECK', label: 'Record authority check', blocked: need(specialist, specialistOnly) })
      out.push({ act: 'DRAFT', label: 'Return for evidence', blocked: need(specialist, specialistOnly) })
      break
    case 'ACTIVE':
      out.push({ act: 'CHECK', label: 'Recheck with the authority', blocked: need(specialist, specialistOnly) })
      out.push({ act: 'SUSPENDED', label: 'Suspend', blocked: need(specialist, specialistOnly) })
      out.push({ act: 'EXPIRED', label: 'Record expiry', blocked: !specialist ? specialistOnly : r.flags.includes('PAST_EXPIRY') ? null : 'Only once its end date has passed.' })
      out.push({ act: 'REVOKED', label: 'Revoke', blocked: need(specialist, specialistOnly) })
      break
    case 'SUSPENDED':
      out.push({
        act: 'ACTIVE', label: 'Reinstate',
        blocked: !specialist ? specialistOnly : lastSuspender === me ? 'You suspended it; a different person reinstates it.' : null,
      })
      out.push({ act: 'EXPIRED', label: 'Record expiry', blocked: !specialist ? specialistOnly : r.flags.includes('PAST_EXPIRY') ? null : 'Only once its end date has passed.' })
      out.push({ act: 'REVOKED', label: 'Revoke', blocked: need(specialist, specialistOnly) })
      break
    default:
      break
  }
  if (!['EXPIRED', 'REVOKED', 'WITHDRAWN'].includes(r.status)) {
    out.push({ act: 'ATTACH', label: 'Add a supporting document', blocked: need(recorder, 'Needs the OPERATOR or ANALYST role.') })
  }
  return out
}

/** The acts a certificate admits for this person. */
export function certificateOffers(c: Certificate, roles: readonly string[], me: string): Offer[] {
  const recorder = has(roles, 'OPERATOR', 'ANALYST')
  const specialist = has(roles, 'ANALYST')
  const specialistOnly = 'Needs the ANALYST role.'
  const uploader = [...c.history].reverse().find((e) => e.document !== undefined)?.recordedBy
  const lastSuspender = [...c.history].reverse().find((e) => e.kind === 'SUSPENDED')?.recordedBy
  const out: Offer[] = []
  if (['SUBMITTED', 'SCREENING', 'REVIEW_REQUIRED'].includes(c.status)) {
    out.push({ act: 'VERSION', label: 'Replace with a corrected document', blocked: recorder ? null : 'Needs the OPERATOR or ANALYST role.' })
  }
  switch (c.status) {
    case 'SUBMITTED':
      out.push({ act: 'SCREEN', label: 'Screen for completeness', blocked: specialist ? null : specialistOnly })
      break
    case 'REVIEW_REQUIRED':
      if (c.pendingRecommendation === undefined) {
        out.push({ act: 'RECOMMEND', label: 'Recommend', blocked: specialist ? null : specialistOnly })
      } else {
        const p = c.pendingRecommendation
        const blocked = !specialist ? specialistOnly
          : p.recordedBy === me ? 'You made this recommendation; a different person confirms or returns it.'
          : uploader === me ? 'You recorded this document; a different person approves it.' : null
        out.push({ act: 'CONFIRM', label: p.recommendation === 'APPROVE' ? 'Confirm approval' : 'Confirm rejection', blocked })
        out.push({ act: 'RETURN', label: 'Return the recommendation', blocked })
      }
      break
    case 'APPROVED':
      out.push({ act: 'SUSPENDED', label: 'Suspend', blocked: specialist ? null : specialistOnly })
      out.push({ act: 'EXPIRED', label: 'Record expiry', blocked: !specialist ? specialistOnly : c.flags.includes('PAST_EXPIRY') ? null : 'Only once its evidence period has ended.' })
      out.push({ act: 'REVOKED', label: 'Revoke', blocked: specialist ? null : specialistOnly })
      break
    case 'SUSPENDED':
      out.push({ act: 'APPROVED', label: 'Reinstate', blocked: !specialist ? specialistOnly : lastSuspender === me ? 'You suspended it; a different person reinstates it.' : null })
      out.push({ act: 'EXPIRED', label: 'Record expiry', blocked: !specialist ? specialistOnly : c.flags.includes('PAST_EXPIRY') ? null : 'Only once its evidence period has ended.' })
      out.push({ act: 'REVOKED', label: 'Revoke', blocked: specialist ? null : specialistOnly })
      break
    default:
      break
  }
  return out
}

/** A registration's or certificate's end, as the renewal queue sorts it. */
export function endOf(r: { effectiveTo?: string; expiresOn?: string; approval?: { effectiveTo?: string } }): string {
  return r.approval?.effectiveTo ?? r.effectiveTo ?? r.expiresOn ?? '9999-12-31'
}

/**
 * The SHA-256 of a file, as lowercase hex, computed in the browser. The file
 * is read locally and never uploaded.
 */
export async function sha256Hex(file: Blob): Promise<string> {
  const digest = await crypto.subtle.digest('SHA-256', await file.arrayBuffer())
  return Array.from(new Uint8Array(digest), (b) => b.toString(16).padStart(2, '0')).join('')
}

export const ACCEPTED_TYPES = ['application/pdf', 'image/png', 'image/jpeg', 'image/tiff', 'image/webp'] as const
export const MAX_BYTES = 25 * 1024 * 1024

/** Reads a file as evidence by digest, or says why it is not accepted. */
export async function documentOf(file: File): Promise<EvidenceDocument | string> {
  const mediaType = file.type.toLowerCase()
  if (!(ACCEPTED_TYPES as readonly string[]).includes(mediaType)) {
    return 'Only a PDF or an image (PNG, JPEG, TIFF, WebP) is accepted as evidence.'
  }
  if (file.size <= 0 || file.size > MAX_BYTES) {
    return 'A document is between 1 byte and 25 MiB.'
  }
  return { sha256: await sha256Hex(file), filename: file.name.slice(0, 255), mediaType, bytes: file.size }
}
