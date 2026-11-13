/**
 * One classify request at a time, with no React in it so it can be tested alone.
 *
 * - A second `start` while one is in flight is refused (returns false) and sends
 *   nothing, so two quick submits make one request.
 * - `cancel` (leaving the page) aborts the request and drops its answer.
 * - An answer is delivered once, and only to the request that is still current.
 */
import { requestClassify, type ClassifyResult } from './api'
import type { ClassifyReply } from './model'
import type { FailureKind } from './outcomes'

export type ClassifyEnding =
  | { readonly kind: 'reply'; readonly reply: ClassifyReply }
  | { readonly kind: 'failure'; readonly failure: FailureKind }

type Send = (
  params: { readonly subjectRef: string; readonly description: string },
  signal: AbortSignal,
) => Promise<ClassifyResult>

function endingOf(result: ClassifyResult): ClassifyEnding {
  switch (result.status) {
    case 'reply':
      return { kind: 'reply', reply: result.reply }
    case 'aborted':
      // Not cancelled by us (that path never gets here), so no answer came.
      return { kind: 'failure', failure: 'network' }
    default:
      return { kind: 'failure', failure: result.status }
  }
}

export interface ClassifyRunner {
  /** True while a request is in flight. */
  readonly busy: () => boolean
  /** Sends the request unless one is in flight. Returns whether it was sent. */
  readonly start: (
    params: { readonly subjectRef: string; readonly description: string },
    onEnding: (ending: ClassifyEnding) => void,
  ) => boolean
  /** Aborts the request in flight, if any, and drops its answer. */
  readonly cancel: () => void
}

export function createClassifyRunner(send: Send = requestClassify): ClassifyRunner {
  let current: AbortController | null = null
  return {
    busy: () => current !== null,
    start: (params, onEnding) => {
      if (current !== null) return false
      const controller = new AbortController()
      current = controller
      void send(params, controller.signal)
        .catch((): ClassifyResult => ({ status: 'unreadable' })) // never leave the runner stuck busy
        .then((result) => {
          if (current !== controller) return // cancelled meanwhile: a late answer is dropped
          current = null
          onEnding(endingOf(result))
        })
      return true
    },
    cancel: () => {
      current?.abort()
      current = null
    },
  }
}
