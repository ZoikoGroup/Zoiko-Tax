/**
 * The reviewer's choices and the rules about them. Pure: no React, no I/O.
 * Nothing here is sent or saved; the buttons are UI only.
 */
export type ReviewerActionKind = 'ACCEPT' | 'REJECT' | 'MODIFY' | 'ESCALATE'

export const reviewerActions: ReadonlyArray<{ kind: ReviewerActionKind; label: string }> = [
  { kind: 'ACCEPT', label: 'Accept' },
  { kind: 'REJECT', label: 'Reject' },
  { kind: 'MODIFY', label: 'Modify' },
  { kind: 'ESCALATE', label: 'Escalate' },
]

/** A short list for the demo; the governance model asks for reason codes but does not enumerate them. */
export const reviewerReasonCodes: ReadonlyArray<{ value: string; label: string }> = [
  { value: 'MATCHES_CATALOG_DESCRIPTION', label: 'Matches the catalog description' },
  { value: 'ONTOLOGY_NODE_LOOKS_WRONG', label: 'The proposed ontology node looks wrong' },
  { value: 'DESCRIPTION_WAS_AMBIGUOUS', label: 'The item description was ambiguous' },
  { value: 'NEEDS_SPECIALIST_REVIEW', label: 'Needs a tax specialist to confirm' },
  { value: 'OTHER', label: 'Other (see note)' },
]

/** A decision may be recorded once it has a reason and a note (and, for Modify, a code). */
export function canRecordDecision(
  action: ReviewerActionKind | null,
  reasonCode: string,
  note: string,
  modifiedCode: string,
): boolean {
  if (action === null) return false
  return reasonCode !== '' && note.trim() !== '' && (action !== 'MODIFY' || modifiedCode.trim() !== '')
}

/**
 * The code to hand to an `onAccept` callback for a recorded decision: the
 * proposal's own code for Accept, and nothing for Reject, Modify or Escalate.
 */
export function acceptedCode(action: ReviewerActionKind, proposedCode: string | undefined): string | null {
  if (action !== 'ACCEPT') return null
  return typeof proposedCode === 'string' && proposedCode !== '' ? proposedCode : null
}
