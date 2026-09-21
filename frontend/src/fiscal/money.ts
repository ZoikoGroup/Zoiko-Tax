/**
 * Fiscal values in the browser, as ADR-0019 C1 requires them: displayed, never
 * computed.
 *
 * JavaScript has no decimal type. A browser-side subtotal is a binary-float tax
 * calculation with no rounding policy, no evidence record and no replay
 * (ADR-0001 §4.2, ADR-0002 §2.3). So the amount never becomes a number here.
 *
 * The enforcement is the type. `FiscalAmount` is a branded string: it cannot be
 * assigned from a plain string without passing through the parser, and it
 * exposes nothing that adds, multiplies or rounds. `Number(amount)` is a type
 * error before it is a lint failure, and the lint rule in eslint.config.js is
 * the second line rather than the first.
 *
 * Where a UI needs a derived figure, the server provides it.
 */

declare const fiscalBrand: unique symbol

/** A canonical decimal string from the API (ADR-0011 §2.2). Not a number. */
export type FiscalAmount = string & { readonly [fiscalBrand]: 'FiscalAmount' }

/** An ISO 4217 alphabetic code, carried beside every amount. */
export type Currency = string & { readonly [fiscalBrand]: 'Currency' }

export interface Money {
  readonly amount: FiscalAmount
  readonly currency: Currency
}

/**
 * The canonical decimal form: an optional sign, digits, and an optional
 * fractional part. No exponent notation, because scale is semantic here —
 * "45.00" and "45" are the same number and different fiscal statements, and the
 * string that arrived is the one that gets shown.
 */
const CANONICAL_DECIMAL = /^-?(0|[1-9][0-9]*)(\.[0-9]+)?$/
const ISO_4217 = /^[A-Z]{3}$/

export class FiscalFormatError extends Error {}

/**
 * Accepts a canonical decimal string from the API and refuses anything else.
 *
 * A JSON number never reaches this function, because it cannot: the parameter
 * is typed `string`. That is deliberate — ADR-0010 §2.9 has the API send
 * amounts as strings precisely so the browser cannot receive one that has
 * already passed through an IEEE 754 double.
 */
export function fiscalAmount(value: string): FiscalAmount {
  if (!CANONICAL_DECIMAL.test(value)) {
    throw new FiscalFormatError(`not a canonical decimal string: ${JSON.stringify(value)}`)
  }
  return value as FiscalAmount
}

export function currency(code: string): Currency {
  if (!ISO_4217.test(code)) {
    throw new FiscalFormatError(`not an ISO 4217 alphabetic code: ${JSON.stringify(code)}`)
  }
  return code as Currency
}

export function money(amount: string, code: string): Money {
  return { amount: fiscalAmount(amount), currency: currency(code) }
}

/**
 * Renders an amount for display. Grouping separators are inserted into the
 * integer digits as text; the fractional part is passed through untouched, so
 * the scale the server sent is the scale the user sees. Nothing here converts
 * to a number, and nothing here rounds.
 *
 * Locale-aware grouping is a W3 lane N decision alongside UX-001. It will be a
 * string transformation too — Intl.NumberFormat takes a number, which is the
 * one input this module will not produce.
 */
export function displayAmount(value: FiscalAmount): string {
  const negative = value.startsWith('-')
  const unsigned = negative ? value.slice(1) : value
  const [whole = '', fraction] = unsigned.split('.')

  let grouped = ''
  for (let i = 0; i < whole.length; i += 1) {
    const fromEnd = whole.length - i
    grouped += whole[i]
    if (fromEnd > 1 && fromEnd % 3 === 1) {
      grouped += ','
    }
  }

  const body = fraction === undefined ? grouped : `${grouped}.${fraction}`
  return negative ? `-${body}` : body
}
