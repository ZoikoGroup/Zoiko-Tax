import type { ProvenanceKind } from './provenanceKind'

/**
 * What the proposal card says about where an answer came from. Pure: no React.
 * `badge` is a clear warning, `quiet` is a plain muted note, and `model` says
 * nothing: the ids prove no more than the table already shows.
 */
export interface ProvenanceNote {
  readonly tone: 'badge' | 'quiet'
  readonly text: string
}

export const TEST_RUNTIME_TEXT = 'Test runtime: not a real AI answer'
export const UNKNOWN_SOURCE_TEXT = 'The source of this answer could not be confirmed from its profile ids.'

export function provenanceNote(kind: ProvenanceKind): ProvenanceNote | null {
  if (kind === 'test') return { tone: 'badge', text: TEST_RUNTIME_TEXT }
  if (kind === 'unknown') return { tone: 'quiet', text: UNKNOWN_SOURCE_TEXT }
  return null
}
