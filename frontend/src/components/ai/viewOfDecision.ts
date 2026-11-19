/**
 * How saving a reviewer's decision ends, in plain words. Pure: no React, no I/O.
 *
 * Only a saved reply (201) or a 409 (a decision is already on record) counts as
 * saved. Every other ending is a failure with its own sentence. No sentence
 * here ever contains the reason or note a person typed, or any error text.
 */
export type DecisionEnding =
  | { readonly ok: true }
  | { readonly ok: false; readonly error: unknown }

export interface DecisionView {
  readonly state: 'saved' | 'already' | 'failed'
  /** True for 'saved' and 'already': a decision is on record. */
  readonly onRecord: boolean
  /** True when the form must stay closed and no button is shown: a decision exists. */
  readonly locked: boolean
  readonly headline: string
  readonly sentence: string
}

function statusOf(error: unknown): number | null {
  if (typeof error === 'object' && error !== null && 'status' in error) {
    const status = error.status
    return typeof status === 'number' ? status : null
  }
  return null
}

const failed = (headline: string, sentence: string): DecisionView => ({
  state: 'failed', onRecord: false, locked: false, headline, sentence: `${sentence} Nothing was saved.`,
})

export function viewOfDecision(ending: DecisionEnding): DecisionView {
  if (ending.ok) {
    return {
      state: 'saved', onRecord: true, locked: true,
      headline: 'Decision saved',
      sentence: 'Your decision was saved with who decided and when. It cannot be changed here.',
    }
  }
  switch (statusOf(ending.error)) {
    case 409:
      return {
        state: 'already', onRecord: true, locked: true,
        headline: 'A decision is already on record',
        sentence: 'A decision for this proposal is already on record. It cannot be changed here.',
      }
    case 400:
      return failed('The decision was not accepted', 'Check the fields and try again.')
    case 401:
      return failed('Your session has ended', 'Sign in again.')
    case 403:
      return failed('You are not allowed to record a decision', 'Ask an operator or an analyst.')
    case 503:
      return failed('Saving is not available here', 'Saving decisions is not available in this cell.')
    default:
      return failed('The decision could not be saved', 'The service could not be reached or failed. Try again.')
  }
}
