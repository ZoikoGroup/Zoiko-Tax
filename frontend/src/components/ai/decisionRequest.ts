/**
 * The request that saves a reviewer's decision, built from the proposal on
 * screen and the form. Pure: no React, no I/O.
 *
 * The limits are the Go service's (backend domain/classification/review_decision.go),
 * counted in characters. The request never carries a reviewer or a tenant: both
 * come from the session on the server. Every refusal is a fixed sentence that
 * never repeats what the person typed.
 */
import { evidenceIdText } from './evidenceId'
import type { ReviewerActionKind } from './reviewDecision'

export const REASON_MAX = 500
export const NOTE_MAX = 2000
export const CODE_MAX = 256

export interface DecisionRequest {
  readonly proposalId: string
  readonly subjectRef: string
  readonly proposedCode: string
  readonly action: ReviewerActionKind
  readonly reason: string
  readonly note?: string
  readonly modifiedCode?: string
  readonly evidenceId?: string
}

/** The part of a proposal this builder reads. */
export interface ProposalForDecision {
  readonly id?: string
  readonly subjectRef?: string
  readonly proposedCode?: string
  readonly provenance?: { readonly invocationId?: unknown } | null
}

export interface DecisionForm {
  readonly action: ReviewerActionKind
  readonly reason: string
  readonly note: string
  readonly modifiedCode: string
}

export type DecisionProblem =
  | 'NO_PROPOSAL' | 'REASON_BLANK' | 'REASON_LONG' | 'NOTE_LONG' | 'CODE_MISSING' | 'CODE_BAD' | 'CODE_LONG'

export type BuiltDecision =
  | { readonly ok: true; readonly request: DecisionRequest }
  | { readonly ok: false; readonly problem: DecisionProblem; readonly sentence: string }

const SENTENCES: Readonly<Record<DecisionProblem, string>> = {
  NO_PROPOSAL: 'This proposal has no identifier, so a decision cannot be saved.',
  REASON_BLANK: 'Write a reason before saving.',
  REASON_LONG: `The reason can be at most ${String(REASON_MAX)} characters.`,
  NOTE_LONG: `The note can be at most ${String(NOTE_MAX)} characters.`,
  CODE_MISSING: "Enter the reviewer's ontology code for Modify.",
  CODE_BAD: "The reviewer's code must start with ontology: and name a code.",
  CODE_LONG: `The reviewer's code can be at most ${String(CODE_MAX)} characters.`,
}

const refuse = (problem: DecisionProblem): BuiltDecision => ({ ok: false, problem, sentence: SENTENCES[problem] })

const chars = (s: string): number => [...s].length

/** The same check as the Go side (OntologyID.Validate): the prefix, and something after it. */
export function isOntologyCode(code: string): boolean {
  return code.startsWith('ontology:') && code.length > 'ontology:'.length
}

export function buildDecisionRequest(proposal: ProposalForDecision, form: DecisionForm): BuiltDecision {
  if (
    typeof proposal.id !== 'string' || proposal.id === '' ||
    typeof proposal.subjectRef !== 'string' || proposal.subjectRef === '' ||
    typeof proposal.proposedCode !== 'string' || proposal.proposedCode === ''
  ) {
    return refuse('NO_PROPOSAL')
  }
  const reason = form.reason.trim()
  const note = form.note.trim()
  if (reason === '') return refuse('REASON_BLANK')
  if (chars(reason) > REASON_MAX) return refuse('REASON_LONG')
  if (chars(note) > NOTE_MAX) return refuse('NOTE_LONG')

  const request: { -readonly [K in keyof DecisionRequest]: DecisionRequest[K] } = {
    proposalId: proposal.id,
    subjectRef: proposal.subjectRef,
    proposedCode: proposal.proposedCode,
    action: form.action,
    reason,
  }
  if (note !== '') request.note = note
  if (form.action === 'MODIFY') {
    const code = form.modifiedCode.trim()
    if (code === '') return refuse('CODE_MISSING')
    if (!isOntologyCode(code)) return refuse('CODE_BAD')
    if (chars(code) > CODE_MAX) return refuse('CODE_LONG')
    request.modifiedCode = code
  }
  const evidenceId = evidenceIdText(proposal.provenance)
  if (evidenceId !== null) request.evidenceId = evidenceId
  return { ok: true, request }
}
