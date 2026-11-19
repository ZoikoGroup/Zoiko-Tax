/**
 * The Gateway's evidence record id for a call, as the card may show it. Pure:
 * no React. The id is optional on the reply, so every unusable value gives
 * null and the card then shows nothing. The shape is the one the Go gateway
 * accepts: 1 to 64 letters, digits or dashes.
 */
const SHAPE = /^[A-Za-z0-9-]{1,64}$/

export function evidenceIdText(provenance: { readonly invocationId?: unknown } | null | undefined): string | null {
  if (provenance === undefined || provenance === null) return null
  const raw = provenance.invocationId
  if (typeof raw !== 'string') return null
  const id = raw.trim()
  return SHAPE.test(id) ? id : null
}
