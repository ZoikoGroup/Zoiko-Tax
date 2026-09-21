import type { Money } from './money'
import { displayAmount } from './money'

/**
 * An estimate, a quote, an AI suggestion or a shadow-run comparison.
 *
 * Deliberately not interchangeable with AuthoritativeAmount (ADR-0019 C2): it
 * takes no decision reference, it requires a basis in words, and it says on
 * screen that it is not a decision. ADR-0006 §2.6 keeps AI output off the
 * authoritative path in the backend; this is the same rule at the presentation
 * layer, where the same High-rated risk shows up as a number a user trusts.
 */
export interface AdvisoryAmountProps {
  readonly value: Money
  readonly label: string
  /** Where the figure came from, in plain language. Required: an advisory
   *  number with no stated basis is indistinguishable from a decision. */
  readonly basis: string
}

export function AdvisoryAmount({ value, label, basis }: AdvisoryAmountProps) {
  return (
    <figure className="amount amount--advisory">
      <figcaption className="amount__label">
        {label} <span className="amount__tag">advisory</span>
      </figcaption>
      <data className="amount__value" value={value.amount}>
        <span className="amount__currency">{value.currency}</span>{' '}
        {displayAmount(value.amount)}
      </data>
      <span className="amount__provenance">{basis}. Not a decision; not filed.</span>
    </figure>
  )
}
