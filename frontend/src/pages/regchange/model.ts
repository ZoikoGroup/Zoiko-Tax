/**
 * Pieces of the Regulatory Intelligence page (Engineering Doc 03 WF-05,
 * WF-09; Sidebar WF-02): the vocabularies, what a status means in words,
 * the acts a change or a recommendation admits for the person looking at it,
 * and the request bodies the forms send.
 *
 * The cell enforces every rule — roles, four eyes, versions, the current
 * assessment. This file decides only what to offer, and why an act is not
 * open to this person instead of a dead button.
 */
import { ZoikoTaxError, type S } from '@zoikotax/sdk'

export type Change = S<'RegulatoryChange'>
export type ChangeKind = S<'RegulatoryChangeKind'>
export type ChangeStatus = S<'RegulatoryChangeStatus'>
export type Priority = S<'RegulatoryChangePriority'>
export type Disposition = S<'RegulatoryChangeDisposition'>
export type HoldReason = S<'RegulatoryChangeHoldReason'>
export type ChangeRequest = S<'RegulatoryChangeRequest'>
export type TransitionRequest = S<'RegulatoryChangeTransitionRequest'>
export type Assessment = S<'ImpactAssessment'>
export type ImpactItem = S<'ImpactItem'>
export type Severity = S<'ImpactSeverity'>
export type Recommendation = S<'ReviewRecommendation'>
export type RecStatus = S<'ReviewRecommendationStatus'>
export type RecAction = S<'ReviewRecommendationAction'>
export type RecTarget = S<'RecommendationTarget'>
export type RecRequest = S<'ReviewRecommendationRequest'>
export type ServiceFamily = S<'ServiceFamily'>

/** The sentence the page repeats wherever an outcome appears. */
export const ADVISORY =
  'Advisory only. An assessment is a read, and an accepted recommendation is a hand-off reference: nothing here publishes a rule pack, changes content or moves a tax result.'

export const KINDS: readonly { value: ChangeKind; label: string }[] = [
  { value: 'NEW_LEVY', label: 'New levy' },
  { value: 'RATE_CHANGE', label: 'Rate change' },
  { value: 'REPEAL_SUNSET', label: 'Repeal / sunset' },
  { value: 'BASE_CHANGE', label: 'Base change' },
  { value: 'CLASS_CHANGE', label: 'Classification change' },
  { value: 'JURISDICTION_CHANGE', label: 'Jurisdiction change' },
  { value: 'BACKDATED_RULE', label: 'Backdated rule' },
  { value: 'RIGHTS_WITHDRAWAL', label: 'Rights withdrawal' },
  { value: 'SOURCE_DISPUTE', label: 'Source dispute' },
]

export const STATUSES: readonly ChangeStatus[] = ['NEW', 'TRIAGED', 'IN_IMPACT_ASSESSMENT', 'ON_HOLD', 'CLOSED', 'NO_IMPACT']
export const PRIORITIES: readonly Priority[] = ['URGENT', 'MATERIAL', 'ORDINARY']
export const HOLD_REASONS: readonly HoldReason[] = ['SOURCE_DISPUTED', 'COUNSEL_QUESTION', 'RIGHTS_HOLD', 'MISSING_FACTS', 'MONITOR_FAILURE']
export const SEVERITIES: readonly Severity[] = ['LOW', 'MEDIUM', 'HIGH', 'CRITICAL']
export const REC_STATUSES: readonly RecStatus[] = ['DRAFT', 'RECOMMENDED', 'CHANGES_REQUESTED', 'ACCEPTED', 'REJECTED', 'WITHDRAWN']

export const ACTIONS: readonly { value: RecAction; label: string; needs: string }[] = [
  { value: 'UPDATE_RULE', label: 'Update rule', needs: 'one or more content components' },
  { value: 'ADD_RULE', label: 'Add rule', needs: 'the coverage gap or components it sits beside' },
  { value: 'RETIRE_RULE', label: 'Retire rule', needs: 'one or more content components' },
  { value: 'NOTIFY_CUSTOMERS', label: 'Notify customers', needs: 'the exposed registrations or obligations' },
  { value: 'RETROSPECTIVE_REASSESSMENT', label: 'Retrospective reassessment', needs: 'the retrospective obligation periods' },
  { value: 'MONITOR', label: 'Monitor', needs: 'nothing' },
  { value: 'NO_ACTION', label: 'No action', needs: 'nothing' },
]

export const STATUS_MEANING: Readonly<Record<ChangeStatus, string>> = {
  NEW: 'Reported, not yet triaged. A reported change is not a confirmed legal effect.',
  TRIAGED: 'Given a priority. Its impact is assessed next.',
  IN_IMPACT_ASSESSMENT: 'Assessed against the active content and your records; recommendations are made on the current assessment.',
  ON_HOLD: 'Held with a tracked reason. It resumes to the status it was held from.',
  CLOSED: 'Decided: change needed (handed off) or monitor.',
  NO_IMPACT: 'Decided: not applicable.',
}

export const isKind = (v: string): v is ChangeKind => KINDS.some((k) => k.value === v)
export const isStatus = (v: string): v is ChangeStatus => (STATUSES as readonly string[]).includes(v)
export const isPriority = (v: string): v is Priority => (PRIORITIES as readonly string[]).includes(v)
export const isHold = (v: string): v is HoldReason => (HOLD_REASONS as readonly string[]).includes(v)
export const isSeverity = (v: string): v is Severity => (SEVERITIES as readonly string[]).includes(v)
export const isRecStatus = (v: string): v is RecStatus => (REC_STATUSES as readonly string[]).includes(v)
export const isAction = (v: string): v is RecAction => ACTIONS.some((a) => a.value === v)

export const reasonOf = (error: unknown): string | null => (error instanceof ZoikoTaxError ? error.reasonCode : null)

/** Plain words for the reason codes this page expects. */
export function refusalText(reason: string | null): string | null {
  switch (reason) {
    case 'MAKER_CHECKER':
      return 'Four eyes: the recommender and anyone who wrote a version cannot review it. A different person must.'
    case 'VERSION_CONFLICT':
      return 'The record changed since you opened it. Re-read it and act on the current version.'
    case 'ASSESSMENT_SUPERSEDED':
      return 'The change has been reassessed. Withdraw this recommendation and draft one on the current assessment.'
    case 'CHANGE_DEDUPE_CONFLICT':
      return 'This source edition’s change of this kind is already recorded with different details. Open the recorded change.'
    case 'NO_CONTENT_BUNDLE':
      return 'The cell has no active content bundle, so no impact can be assessed.'
    case 'IDEMPOTENCY_KEY_REUSE':
      return 'This form was already sent with different details. Start a new one.'
    default:
      return null
  }
}

const has = (roles: readonly string[], ...wanted: readonly string[]) => wanted.some((r) => roles.includes(r))

/** An act the page can offer, or the reason it does not. */
export interface Offer {
  readonly act: string
  readonly label: string
  readonly blocked: string | null
}

const SPECIALIST_ONLY = 'Needs the ANALYST role.'

/** The acts a change admits for this person. */
export function changeOffers(c: Change, roles: readonly string[]): Offer[] {
  const specialist = has(roles, 'ANALYST')
  const need = (why: string | null) => (!specialist ? SPECIALIST_ONLY : why)
  const out: Offer[] = []
  switch (c.status) {
    case 'NEW':
      out.push({ act: 'TRIAGED', label: 'Triage', blocked: need(null) })
      out.push({ act: 'NO_IMPACT', label: 'Not applicable', blocked: need(null) })
      out.push({ act: 'ON_HOLD', label: 'Hold', blocked: need(null) })
      break
    case 'TRIAGED':
      out.push({ act: 'ASSESS', label: 'Run impact assessment', blocked: need(null) })
      out.push({ act: 'NO_IMPACT', label: 'Not applicable', blocked: need(null) })
      out.push({ act: 'ON_HOLD', label: 'Hold', blocked: need(null) })
      break
    case 'IN_IMPACT_ASSESSMENT':
      out.push({ act: 'ASSESS', label: 'Reassess', blocked: need(null) })
      out.push({ act: 'MONITOR', label: 'Close: monitor', blocked: need(null) })
      out.push({
        act: 'CHANGE_NEEDED', label: 'Close: change needed',
        blocked: need(c.acceptedRecommendationCount > 0 ? null
          : 'Only once a recommendation on the current assessment has been independently accepted.'),
      })
      out.push({ act: 'NO_IMPACT', label: 'Not applicable', blocked: need(null) })
      out.push({ act: 'ON_HOLD', label: 'Hold', blocked: need(null) })
      break
    case 'ON_HOLD':
      out.push({ act: c.resumesTo ?? 'NEW', label: `Resume to ${(c.resumesTo ?? 'NEW').replaceAll('_', ' ')}`, blocked: need(null) })
      break
    default:
      break
  }
  return out
}

/** The transition body an offered act sends. */
export function transitionBody(c: Change, act: string, form: { priority: Priority; hold: HoldReason; reason: string }): TransitionRequest | { invalid: string } {
  const reason = form.reason.trim()
  if (reason === '') return { invalid: 'Every act gives its rationale.' }
  const base = { reason, expectedVersion: c.version }
  switch (act) {
    case 'TRIAGED':
      return { ...base, to: 'TRIAGED', priority: form.priority }
    case 'ON_HOLD':
      return { ...base, to: 'ON_HOLD', holdReason: form.hold }
    case 'NO_IMPACT':
      return { ...base, to: 'NO_IMPACT', disposition: 'NOT_APPLICABLE' }
    case 'MONITOR':
      return { ...base, to: 'CLOSED', disposition: 'MONITOR' }
    case 'CHANGE_NEEDED':
      return { ...base, to: 'CLOSED', disposition: 'CHANGE_NEEDED' }
    default:
      return isStatus(act) ? { ...base, to: act } : { invalid: `${act} is not a change act.` }
  }
}

/** The acts a recommendation admits for this person. */
export function recommendationOffers(r: Recommendation, roles: readonly string[], me: string): Offer[] {
  const specialist = has(roles, 'ANALYST')
  const stale = r.stale ? 'The change has been reassessed since; withdraw this and draft on the current assessment.' : null
  const authored = r.history.some((e) => (e.kind === 'DRAFTED' || e.kind === 'REVISED') && e.recordedBy === me)
  const out: Offer[] = []
  const gate = (why: string | null) => (!specialist ? SPECIALIST_ONLY : why)
  switch (r.status) {
    case 'DRAFT':
      out.push({ act: 'RECOMMEND', label: 'Recommend', blocked: gate(stale) })
      out.push({ act: 'REVISE', label: 'Revise', blocked: gate(stale) })
      break
    case 'CHANGES_REQUESTED':
      out.push({ act: 'REVISE', label: 'Revise', blocked: gate(stale) })
      break
    case 'RECOMMENDED': {
      const review = gate(stale ?? (r.recommendedBy === me ? 'You recommended this; an independent reviewer decides it.'
        : authored ? 'You wrote a version of this; an independent reviewer decides it.' : null))
      out.push({ act: 'ACCEPT', label: 'Accept', blocked: review })
      out.push({ act: 'REQUEST_CHANGES', label: 'Request changes', blocked: review })
      out.push({ act: 'REJECT', label: 'Reject', blocked: review })
      break
    }
    default:
      return out
  }
  out.push({ act: 'WITHDRAW', label: 'Withdraw', blocked: gate(null) })
  return out
}

/** Which assessed items an action may be about. */
export function targetsFor(action: RecAction, items: readonly ImpactItem[]): ImpactItem[] {
  switch (action) {
    case 'UPDATE_RULE':
    case 'RETIRE_RULE':
      return items.filter((i) => i.kind === 'CONTENT_COMPONENT')
    case 'ADD_RULE':
      return items.filter((i) => i.kind === 'CONTENT_COMPONENT' || i.kind === 'COVERAGE_GAP')
    case 'NOTIFY_CUSTOMERS':
      return items.filter((i) => i.kind === 'TENANT_REGISTRATION' || i.kind === 'TENANT_OBLIGATION')
    case 'RETROSPECTIVE_REASSESSMENT':
      return items.filter((i) => i.retrospective)
    default:
      return [...items]
  }
}

export const targetKey = (t: { kind: string; ref: string }) => `${t.kind} ${t.ref}`

export interface ChangeForm {
  jurisdiction: string
  kind: ChangeKind
  title: string
  summary: string
  authority: string
  citation: string
  url: string
  edition: string
  sha256: string
  publishedOn: string
  effectiveFrom: string
  detectedBy: 'HUMAN' | 'MONITOR'
  monitorRef: string
  services: string
  taxTypes: string
}

export const EMPTY_CHANGE: ChangeForm = {
  jurisdiction: '', kind: 'RATE_CHANGE', title: '', summary: '', authority: '', citation: '', url: '', edition: '',
  sha256: '', publishedOn: '', effectiveFrom: '', detectedBy: 'HUMAN', monitorRef: '', services: '', taxTypes: '',
}

const codes = (s: string) => [...new Set(s.split(/[\s,]+/).map((x) => x.trim().toUpperCase()).filter((x) => x !== ''))]

/** The record body the change form sends, or why it cannot be sent. */
export function changeBody(f: ChangeForm): ChangeRequest | { invalid: string } {
  const jurisdiction = f.jurisdiction.trim().toUpperCase()
  if (!/^[A-Z]{2}(-[A-Z0-9]{1,3})?$/.test(jurisdiction)) return { invalid: 'A jurisdiction is a country, or a country and one subdivision: US, US-NY, GB.' }
  if (!/^[0-9a-f]{64}$/.test(f.sha256)) return { invalid: 'Choose the captured source document so its SHA-256 is recorded. The file is not uploaded.' }
  if (f.effectiveFrom === '' || f.publishedOn === '') return { invalid: 'A change states its publication and effective dates; they are never assumed equal.' }
  if (f.kind === 'BACKDATED_RULE' && !(f.effectiveFrom < f.publishedOn)) return { invalid: 'A backdated rule takes effect before it was published.' }
  if (f.detectedBy === 'MONITOR' && f.monitorRef.trim() === '') return { invalid: 'A monitor-detected change names the watcher alert it came from.' }
  const url = f.url.trim()
  if (url !== '' && !url.startsWith('https://')) return { invalid: 'A source URL is an https:// address.' }
  const summary = f.summary.trim()
  return {
    jurisdiction, kind: f.kind, title: f.title.trim(),
    ...(summary === '' ? {} : { summary }),
    source: {
      authority: f.authority.trim(), citation: f.citation.trim(), edition: f.edition.trim(), documentSha256: f.sha256,
      ...(url === '' ? {} : { url }),
    },
    publishedOn: f.publishedOn, effectiveFrom: f.effectiveFrom, detectedBy: f.detectedBy,
    ...(f.detectedBy === 'MONITOR' ? { monitorRef: f.monitorRef.trim() } : {}),
    serviceFamilies: codes(f.services) as ServiceFamily[],
    taxTypes: codes(f.taxTypes),
  }
}

export async function sha256Hex(file: Blob): Promise<string> {
  const digest = await crypto.subtle.digest('SHA-256', await file.arrayBuffer())
  return Array.from(new Uint8Array(digest), (b) => b.toString(16).padStart(2, '0')).join('')
}
