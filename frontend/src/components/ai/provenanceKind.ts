/**
 * What the provenance ids of an AI answer prove about where it came from.
 * Pure: no React, no I/O.
 *
 * The ids are labels the Gateway attaches from its routing table, not proof of
 * a call. What they do show:
 *  - `test`: the development registry's fake runtime labels its answers
 *    `model:fake-v1` / `provider:fake` (intelligence/config/registry.dev.json:18-19).
 *  - `model`: the live demo labels answers `provider:groq-demo` and a model id
 *    that starts `model:groq-live-demo` (intelligence/tools/serve_live_demo.py:66-67,88-89).
 *    The model name in that id is itself a label set from an environment
 *    variable, so this says "the live demo labelled it", and no more.
 *  - `unknown`: any other id, or no id.
 *
 * Ids are matched whole, never by a word inside them, so an id that merely
 * contains "fake" by accident is `unknown`.
 */
export type ProvenanceKind = 'test' | 'model' | 'unknown'

export interface ProvenanceIds {
  readonly modelProfile?: string | undefined
  readonly providerProfile?: string | undefined
}

const FAKE_PROVIDER = 'provider:fake'
const FAKE_MODEL = /^model:fake(?:-[a-z0-9.]+)*$/
const LIVE_PROVIDER = 'provider:groq-demo'
const LIVE_MODEL_PREFIX = 'model:groq-live-demo'

export function provenanceKind(ids: ProvenanceIds): ProvenanceKind {
  const model = ids.modelProfile ?? ''
  const provider = ids.providerProfile ?? ''
  // A fake id on either side wins: the safer reading when the two disagree.
  if (provider === FAKE_PROVIDER || FAKE_MODEL.test(model)) return 'test'
  if (provider === LIVE_PROVIDER && (model === LIVE_MODEL_PREFIX || model.startsWith(`${LIVE_MODEL_PREFIX}:`))) return 'model'
  return 'unknown'
}
