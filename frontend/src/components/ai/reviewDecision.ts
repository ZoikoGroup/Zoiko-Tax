/**
 * The reviewer's choices and the rules about them. Pure: no React, no I/O.
 * The request that saves a decision is built in decisionRequest.ts.
 */
export type ReviewerActionKind = 'ACCEPT' | 'REJECT' | 'MODIFY' | 'ESCALATE'

export const reviewerActions: ReadonlyArray<{ kind: ReviewerActionKind; label: string }> = [
  { kind: 'ACCEPT', label: 'Accept' },
  { kind: 'REJECT', label: 'Reject' },
  { kind: 'MODIFY', label: 'Modify' },
  { kind: 'ESCALATE', label: 'Escalate' },
]

/** A decision may be recorded once it has a reason (and, for Modify, a code). */
export function canRecordDecision(action: ReviewerActionKind | null, reason: string, modifiedCode: string): boolean {
  if (action === null) return false
  return reason.trim() !== '' && (action !== 'MODIFY' || modifiedCode.trim() !== '')
}

/**
 * The code to hand to an `onAccept` callback for a recorded decision: the
 * proposal's own code for Accept, and nothing for Reject, Modify or Escalate.
 */
export function acceptedCode(action: ReviewerActionKind, proposedCode: string | undefined): string | null {
  if (action !== 'ACCEPT') return null
  return typeof proposedCode === 'string' && proposedCode !== '' ? proposedCode : null
}
