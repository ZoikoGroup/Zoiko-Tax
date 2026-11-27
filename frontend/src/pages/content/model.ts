/**
 * Pieces of the content workbench tabs — Vendor Crosswalks (Doc 02 WF-06,
 * WF-07) and Rule Approvals (Doc 03 §07; Sidebar WF-02): the vocabularies,
 * what each status means, and which acts a record admits for the person
 * looking at it.
 *
 * The cell enforces every rule — roles, four eyes, revisions, rights. This
 * file decides only what to offer, and when an act is not open to this
 * person, the reason to show instead of a dead button.
 */
import { ZoikoTaxError, type S } from '@zoikotax/sdk'

export type Crosswalk = S<'Crosswalk'>
export type CrosswalkVersion = S<'CrosswalkVersion'>
export type CrosswalkEntry = S<'CrosswalkEntry'>
export type CrosswalkStatus = S<'CrosswalkStatus'>
export type CrosswalkMatch = S<'CrosswalkMatch'>
export type CrosswalkRightsBasis = S<'CrosswalkRightsBasis'>
export type CrosswalkResolution = S<'CrosswalkResolution'>
export type CrosswalkLookup = S<'CrosswalkLookup'>
export type RuleChange = S<'RuleChange'>
export type RuleChangeStatus = S<'RuleChangeStatus'>
export type RuleChangeType = S<'RuleChangeType'>
export type AuthorityClass = S<'SourceAuthorityClass'>

/** The sentence the crosswalk tab repeats (Doc 02 §02; Doc 12 FED-04). */
export const NOT_FISCAL_AUTHORITY =
  'A crosswalk translates a vendor’s code into a Zoiko canonical service code for a person to classify with. It is never fiscal authority: it does not decide taxability, a levy or a rate, and the determination engine never reads it.'

/** The sentence the approvals tab repeats (WF-02). */
export const NOT_PUBLISHED =
  'Approval makes a change eligible for a content release. It does not compile, sign, publish or activate a rule pack, and changes no tax result until a signed release does.'

export const MATCHES: readonly { value: CrosswalkMatch; label: string }[] = [
  { value: 'EXACT', label: 'Exact' },
  { value: 'PARTIAL', label: 'Partial — under a predicate' },
  { value: 'NO_EQUIVALENT', label: 'No equivalent' },
]

export const RIGHTS_BASES: readonly { value: CrosswalkRightsBasis; label: string }[] = [
  { value: 'VENDOR_LICENCE', label: 'Vendor licence' },
  { value: 'CUSTOMER_SUPPLIED', label: 'Customer-supplied configuration' },
  { value: 'PUBLIC_DOCUMENTATION', label: 'Vendor’s public API documentation' },
]

export const CROSSWALK_STATUSES: readonly CrosswalkStatus[] = [
  'DRAFT', 'IN_REVIEW', 'APPROVED', 'CHANGES_REQUESTED', 'REJECTED', 'WITHDRAWN', 'SUSPENDED', 'SUPERSEDED',
]

export const CROSSWALK_MEANING: Readonly<Record<CrosswalkStatus, string>> = {
  DRAFT: 'Written, not submitted. Translates nothing.',
  IN_REVIEW: 'Waiting for a reviewer other than its author and submitter.',
  APPROVED: 'In use as a translation aid. Not fiscal authority.',
  CHANGES_REQUESTED: 'Sent back. A correction is the next version.',
  REJECTED: 'Rejected. Kept as recorded.',
  WITHDRAWN: 'Withdrawn by its author. Kept as recorded.',
  SUSPENDED: 'Suspended: lookups are quarantined, nothing falls back.',
  SUPERSEDED: 'Replaced by a later approved version. Still reproduces historical translations.',
}

export const RESOLUTION_TEXT: Readonly<Record<CrosswalkResolution, string>> = {
  MAPPED: 'One exact mapping',
  REVIEW_REQUIRED: 'Conditional or several candidates — establish the facts',
  NO_EQUIVALENT: 'Reviewed: no canonical equivalent',
  UNMAPPED: 'Not mapped — no default, no nearest match',
  NOT_IN_FORCE: 'Outside the version’s effective window',
  QUARANTINED: 'Quarantined — suspended version or lapsed grant',
}

export const CHANGE_TYPES: readonly { value: RuleChangeType; label: string }[] = [
  { value: 'RATE_CHANGE', label: 'Rate change' },
  { value: 'BASE_CHANGE', label: 'Taxable base change' },
  { value: 'NEW_LEVY', label: 'New levy' },
  { value: 'LEVY_REPEAL', label: 'Levy repeal' },
  { value: 'EXEMPTION_CHANGE', label: 'Exemption change' },
  { value: 'THRESHOLD_CHANGE', label: 'Threshold change' },
  { value: 'SITUS_CHANGE', label: 'Situs / sourcing change' },
  { value: 'RESPONSIBILITY_CHANGE', label: 'Responsibility change' },
  { value: 'OBLIGATION_CHANGE', label: 'Obligation change' },
  { value: 'OTHER', label: 'Other' },
]

export const AUTHORITY_CLASSES: readonly { value: AuthorityClass; label: string }[] = [
  { value: 'A0', label: 'A0 — binding primary (statute, regulation, judgment)' },
  { value: 'A1', label: 'A1 — official operational' },
  { value: 'A2', label: 'A2 — official explanatory' },
  { value: 'B1', label: 'B1 — licensed specialist' },
  { value: 'B2', label: 'B2 — institutional specialist' },
  { value: 'C1', label: 'C1 — unverified secondary' },
  { value: 'U', label: 'U — unknown' },
]

export const RULE_CHANGE_STATUSES: readonly RuleChangeStatus[] = [
  'PROPOSED', 'RECOMMENDED', 'APPROVED_FOR_RELEASE', 'CHANGES_REQUESTED', 'REJECTED', 'WITHDRAWN', 'SUPERSEDED',
]

export const RULE_CHANGE_MEANING: Readonly<Record<RuleChangeStatus, string>> = {
  PROPOSED: 'Waiting for specialist review by someone other than the proposer.',
  RECOMMENDED: 'Recommended; waiting for an independent approver.',
  APPROVED_FOR_RELEASE: 'Eligible for a content release. Not published, not active.',
  CHANGES_REQUESTED: 'Sent back. The proposer answers with a revised proposal.',
  REJECTED: 'Rejected. Kept as recorded.',
  WITHDRAWN: 'Withdrawn by the proposer. Kept as recorded.',
  SUPERSEDED: 'Replaced by a revised proposal.',
}

export const reasonOf = (error: unknown): string | null => (error instanceof ZoikoTaxError ? error.reasonCode : null)

/** Plain words for the reason codes these tabs expect. */
export function refusalText(reason: string | null): string | null {
  switch (reason) {
    case 'MAKER_CHECKER':
      return 'Four eyes: a different person must do this (conflict of interest).'
    case 'VERSION_CONFLICT':
      return 'The record changed since you opened it. Re-read it and decide on the current revision.'
    case 'CROSSWALK_UNRESOLVED_AMBIGUITY':
      return 'A code is mapped more than once without a predicate that tells the targets apart, or a partial match names no predicate.'
    case 'CROSSWALK_RIGHTS_LAPSED':
      return 'The grant this vendor data is held under has ended. Write a version with a current grant.'
    case 'RULE_CHANGE_TEST_EVIDENCE_REQUIRED':
      return 'Attach the test evidence first.'
    case 'SOURCE_AUTHORITY_INSUFFICIENT':
      return 'Only A0 or A1 authority grounds a production rule. Cite the governing instrument in a revised proposal.'
    case 'IDEMPOTENCY_KEY_REUSE':
      return 'This form was already sent with different details. Start a new one.'
    case 'ALREADY_EXISTS':
      return 'That vendor release is already mapped. Write its next version instead.'
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
  /** Whether the act needs a reason. */
  readonly reason: boolean
}

const has = (roles: readonly string[], ...wanted: readonly string[]) => wanted.some((r) => roles.includes(r))
const need = (ok: boolean, why: string) => (ok ? null : why)

/** Who submitted a crosswalk version. */
export const submitterOf = (v: CrosswalkVersion): string | undefined =>
  [...v.history].reverse().find((e) => e.kind === 'SUBMITTED')?.recordedBy

/** The acts a crosswalk version admits for this person. */
export function crosswalkOffers(v: CrosswalkVersion, roles: readonly string[], me: string): Offer[] {
  const author = has(roles, 'OPERATOR', 'ANALYST')
  const reviewer = has(roles, 'ANALYST')
  const own = v.createdBy === me || submitterOf(v) === me
  const review = need(reviewer, 'Needs the ANALYST role.') ?? (own ? 'You wrote or submitted this version; a different person reviews it.' : null)
  switch (v.status) {
    case 'DRAFT':
      return [
        { act: 'IN_REVIEW', label: 'Submit for review', blocked: need(author, 'Needs the OPERATOR or ANALYST role.'), reason: false },
        { act: 'WITHDRAWN', label: 'Withdraw', blocked: need(v.createdBy === me, 'Only the version’s author withdraws it.'), reason: true },
      ]
    case 'IN_REVIEW':
      return [
        { act: 'APPROVED', label: 'Approve', blocked: review, reason: true },
        { act: 'CHANGES_REQUESTED', label: 'Request changes', blocked: review, reason: true },
        { act: 'REJECTED', label: 'Reject', blocked: review, reason: true },
        { act: 'WITHDRAWN', label: 'Withdraw', blocked: need(v.createdBy === me, 'Only the version’s author withdraws it.'), reason: true },
      ]
    case 'APPROVED':
      return [{ act: 'SUSPENDED', label: 'Suspend', blocked: need(reviewer, 'Needs the ANALYST role.'), reason: true }]
    case 'SUSPENDED':
      return [{ act: 'APPROVED', label: 'Reinstate', blocked: need(reviewer, 'Needs the ANALYST role.'), reason: true }]
    default:
      return []
  }
}

/** The acts a rule change admits for this person. */
export function ruleChangeOffers(c: RuleChange, roles: readonly string[], me: string): Offer[] {
  const proposerRole = has(roles, 'OPERATOR', 'ANALYST')
  const reviewer = has(roles, 'ANALYST')
  const mine = c.createdBy === me
  const conflict = 'You proposed this change; a different person reviews it (conflict of interest).'
  const reviewBlock = need(reviewer, 'Needs the ANALYST role.') ?? (mine ? conflict : null)
  const withdraw: Offer = { act: 'WITHDRAWN', label: 'Withdraw', blocked: need(mine, 'Only the proposer withdraws a change.'), reason: true }
  switch (c.status) {
    case 'PROPOSED':
      return [
        { act: 'EVIDENCE', label: 'Attach test evidence', blocked: need(proposerRole, 'Needs the OPERATOR or ANALYST role.'), reason: false },
        {
          act: 'RECOMMENDED', label: 'Recommend', reason: true,
          blocked: reviewBlock ?? (c.testEvidence === undefined ? 'Attach the test evidence before recommending.' : null),
        },
        { act: 'CHANGES_REQUESTED', label: 'Request changes', blocked: reviewBlock, reason: true },
        { act: 'REJECTED', label: 'Reject', blocked: reviewBlock, reason: true },
        withdraw,
      ]
    case 'RECOMMENDED': {
      const recommended = c.recommendedBy === me
      const authority = c.citation.authorityClass === 'A0' || c.citation.authorityClass === 'A1'
      return [
        {
          act: 'APPROVED_FOR_RELEASE', label: 'Approve for release', reason: true,
          blocked: reviewBlock ?? (recommended ? 'You recommended this change; an independent approver approves it.'
            : authority ? null : 'Only A0 or A1 authority grounds a production rule.'),
        },
        { act: 'CHANGES_REQUESTED', label: 'Request changes', blocked: reviewBlock, reason: true },
        { act: 'REJECTED', label: 'Reject', blocked: reviewBlock, reason: true },
        withdraw,
      ]
    }
    case 'CHANGES_REQUESTED':
      return [withdraw]
    default:
      return []
  }
}

/** Parse the entries editor: one entry per line, `code | description | match | target | predicate`. */
export function parseEntries(text: string): { entries: CrosswalkEntry[]; error: string | null } {
  const entries: CrosswalkEntry[] = []
  const lines = text.split('\n').map((l) => l.trim()).filter((l) => l !== '' && !l.startsWith('#'))
  for (const [i, line] of lines.entries()) {
    const [code = '', description = '', match = '', target = '', predicate = ''] = line.split('|').map((p) => p.trim())
    const m = MATCHES.find((x) => x.value === match.toUpperCase())?.value
    if (code === '' || description === '' || m === undefined) {
      return { entries: [], error: `Line ${String(i + 1)}: write code | description | EXACT, PARTIAL or NO_EQUIVALENT | target | predicate.` }
    }
    if (m !== 'NO_EQUIVALENT' && !target.startsWith('ontology:')) {
      return { entries: [], error: `Line ${String(i + 1)}: an ${m} match names a canonical service code (ontology:…).` }
    }
    entries.push({
      externalCode: code, description, match: m,
      ...(m === 'NO_EQUIVALENT' ? {} : { serviceTarget: target }),
      ...(predicate === '' ? {} : { predicate }),
    })
  }
  return { entries, error: entries.length === 0 ? 'Map at least one code.' : null }
}
