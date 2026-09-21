import type { Money } from './money'
import { displayAmount } from './money'

/**
 * An amount the estate stands behind: the output of a recorded decision, with
 * an evidence reference that can be replayed.
 *
 * ADR-0019 C2 makes this a distinct component rather than a prop on a shared
 * one. A boolean `isAuthoritative` defaults, gets forgotten at a new call site
 * and gets dropped in a refactor; a separate type with a required decision
 * reference cannot be rendered by accident. The UI is where a user decides
 * whether to trust a number, and that decision is made here, structurally.
 */
export interface AuthoritativeAmountProps {
  readonly value: Money
  readonly label: string
  /** The decision this figure came out of. Required — an authoritative amount
   *  that cannot name its decision is not authoritative. */
  readonly decisionId: string
}

export function AuthoritativeAmount({ value, label, decisionId }: AuthoritativeAmountProps) {
  return (
    <figure className="amount amount--authoritative">
      <figcaption className="amount__label">{label}</figcaption>
      <data className="amount__value" value={value.amount}>
        <span className="amount__currency">{value.currency}</span>{' '}
        {displayAmount(value.amount)}
      </data>
      <span className="amount__provenance">
        decision <code>{decisionId}</code>
      </span>
    </figure>
  )
}
