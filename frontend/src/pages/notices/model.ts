import { ZoikoTaxError, type S } from '@zoikotax/sdk'

// Regulatory notices (W17, NAV-013): the vocabulary the page shows and the
// helpers its forms use. Nothing here computes a fiscal figure: amounts are
// the cell's strings, compared on the server.

export type Notice = S<'Notice'>
export type NoticeType = S<'NoticeType'>
export type NoticeStatus = S<'NoticeStatus'>
export type Authenticity = S<'NoticeAuthenticity'>
export type DeadlineSource = S<'NoticeDeadlineSource'>
export type Flag = S<'NoticeFlag'>
export type Action = S<'NoticeAction'>
export type Outcome = S<'NoticeOutcome'>
export type Settlement = S<'NoticeSettlement'>
export type EscalationReason = S<'NoticeEscalationReason'>
export type LinkKind = S<'NoticeLinkKind'>
export type Deadline = S<'NoticeDeadline'>

export const TYPES: readonly NoticeType[] = ['NOTICE', 'ASSESSMENT', 'AUDIT', 'APPEAL', 'INFORMATION_REQUEST']
export const STATUSES: readonly NoticeStatus[] = [
  'RECEIVED', 'AUTHENTICITY_REVIEW', 'LEGAL_REVIEW', 'RESPONSE_APPROVED', 'SUBMITTED', 'OUTCOME_RECORDED', 'APPEAL', 'CLOSED',
]
export const AUTHENTICITY: readonly Authenticity[] = ['UNKNOWN', 'VERIFIED', 'UNVERIFIABLE', 'NOT_AUTHENTIC']
export const SOURCES: readonly DeadlineSource[] = [
  'STATED_ON_NOTICE', 'STATUTE', 'AUTHORITY_CONFIRMATION', 'AUTHORITY_PORTAL', 'ESTIMATED', 'UNKNOWN',
]
export const OUTCOMES: readonly Outcome[] = ['ACCEPTED', 'ADJUSTED', 'REJECTED', 'WITHDRAWN']
export const SETTLEMENTS: readonly Settlement[] = ['AUTHORITY_CONFIRMED', 'PAYMENT_SETTLED', 'PAYMENT_INSTRUCTION_PENDING']
export const ESCALATIONS: readonly EscalationReason[] = [
  'LEGAL_JUDGEMENT', 'DEADLINE_UNVERIFIED', 'AUTHENTICITY_UNRESOLVED', 'ENTITY_UNRESOLVED',
]
export const LINK_KINDS: readonly LinkKind[] = [
  'RETURN', 'SUBMISSION_RECEIPT', 'PAYMENT', 'LEDGER_ENTRY', 'RULE_VERSION', 'CORRESPONDENCE', 'OBLIGATION', 'DECISION',
]

export const words = (s: string) => s.replaceAll('_', ' ')

export const pick = <T extends string>(values: readonly T[], v: string, fallback: T): T =>
  (values as readonly string[]).includes(v) ? (v as T) : fallback

/** What each flag says to a person, and how loudly. */
export const FLAG: Record<Flag, { label: string; tone: 'bad' | 'warn' | 'neutral' }> = {
  URGENT_LEGAL_REVIEW: { label: 'Urgent legal review', tone: 'bad' },
  DEADLINE_UNKNOWN: { label: 'Deadline unknown', tone: 'bad' },
  DEADLINE_UNVERIFIED: { label: 'Deadline unverified', tone: 'warn' },
  AUTHENTICITY_UNVERIFIED: { label: 'Authenticity unverified', tone: 'warn' },
  AUTHENTICITY_DISPUTED: { label: 'Authenticity disputed', tone: 'bad' },
  ENTITY_UNRESOLVED: { label: 'Recipient unresolved', tone: 'bad' },
  DEADLINE_SOON: { label: 'Deadline soon', tone: 'warn' },
  STATUTORY_OVERDUE: { label: 'Statutory deadline passed', tone: 'bad' },
  SLA_BREACHED: { label: 'Internal SLA breached', tone: 'warn' },
  LEGAL_ESCALATED: { label: 'With legal', tone: 'neutral' },
  HEARING_SCHEDULED: { label: 'Hearing scheduled', tone: 'neutral' },
}

/** The order flags are shown in: the loudest first. */
export const FLAG_ORDER: readonly Flag[] = [
  'URGENT_LEGAL_REVIEW', 'STATUTORY_OVERDUE', 'DEADLINE_UNKNOWN', 'AUTHENTICITY_DISPUTED', 'ENTITY_UNRESOLVED',
  'DEADLINE_SOON', 'DEADLINE_UNVERIFIED', 'AUTHENTICITY_UNVERIFIED', 'SLA_BREACHED', 'HEARING_SCHEDULED', 'LEGAL_ESCALATED',
]

/** A deadline as words: its date and zone, or that nobody knows it. */
export function deadlineText(d: Deadline): string {
  if (d.dueDate === undefined) {
    return 'unknown'
  }
  return `${d.dueDate} (end of day, ${d.timezone})`
}

/**
 * What a refusal means for the next step, keyed by the cell's reason code.
 * The cell's own words follow it; this only says what to do.
 */
export function refusalHint(error: unknown): string | null {
  if (!(error instanceof ZoikoTaxError)) {
    return null
  }
  switch (error.reasonCode) {
    case 'SEGREGATION_OF_DUTIES':
      return 'Four-eye rule: someone other than the person who prepared the response package must approve it.'
    case 'NOTICE_LEGAL_VALIDATION_REQUIRED':
      return 'Verify the notice’s authenticity and resolve its recipient legal entity, or escalate it to legal, before answering.'
    case 'NOTICE_DEADLINE_UNVERIFIED':
      return 'Confirm the statutory deadline with evidence first. A confirmation never moves a date; only an evidenced extension does.'
    case 'NOTICE_SETTLEMENT_UNPROVEN':
      return 'A pending payment instruction does not settle a notice. Record it unsettled, or attach the authority’s confirmation.'
    case 'NOTICE_CLOSURE_BLOCKED':
      return 'Close only on the authority’s recorded outcome, or after a legal escalation that documents the disposition.'
    case 'PREPARATION_VERSION_STALE':
      return 'A newer response package exists; reload and approve the current version.'
    case 'STATE_TRANSITION_INVALID':
      return 'The notice has moved on since this screen was read; reload it.'
    default:
      return null
  }
}

/**
 * The SHA-256 of a file, as lowercase hex, computed in the browser. The file
 * is read locally and never uploaded; only its digest, name and type are
 * recorded as evidence.
 */
export async function sha256Hex(file: Blob): Promise<string> {
  const digest = await crypto.subtle.digest('SHA-256', await file.arrayBuffer())
  return Array.from(new Uint8Array(digest), (b) => b.toString(16).padStart(2, '0')).join('')
}

/** The media type the cell accepts: lowercase type/subtype, or a safe default. */
export function mediaTypeOf(file: File): string {
  const t = file.type.toLowerCase()
  return /^[a-z0-9][a-z0-9!#$&^_.+-]{0,63}\/[a-z0-9][a-z0-9!#$&^_.+-]{0,126}$/.test(t) ? t : 'application/octet-stream'
}

/** Leaves out an empty optional field, as exactOptionalPropertyTypes wants. */
export function opt<K extends string>(key: K, value: string): { [P in K]?: string } {
  const v = value.trim()
  return (v === '' ? {} : { [key]: v }) as { [P in K]?: string }
}

export const TIMEZONES: readonly string[] = [
  'Europe/Berlin', 'Europe/London', 'Europe/Paris', 'Europe/Madrid', 'Europe/Rome', 'Europe/Amsterdam', 'Europe/Dublin',
  'Europe/Warsaw', 'Europe/Stockholm', 'America/New_York', 'America/Chicago', 'America/Denver', 'America/Los_Angeles',
  'America/Toronto', 'America/Sao_Paulo', 'America/Mexico_City', 'Asia/Kolkata', 'Asia/Singapore', 'Asia/Tokyo',
  'Australia/Sydney', 'UTC',
]
