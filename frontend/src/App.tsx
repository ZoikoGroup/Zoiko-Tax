import { CellHealth } from './platform/CellHealth'
import { AuthoritativeAmount } from './fiscal/AuthoritativeAmount'
import { AdvisoryAmount } from './fiscal/AdvisoryAmount'
import { money } from './fiscal/money'

/**
 * The shell, and nothing more. ADR-0019 defers the screens to W3 lane N
 * alongside UX-001; what exists here is the smallest application that runs,
 * talks to the cell, and demonstrates the constraints that are binding today —
 * so the first real screen inherits them rather than retrofitting them across
 * 38.
 *
 * The two amounts below are fixed strings rather than API responses: the
 * determination surface opens in W2 lane K, and until the generated SDK exists
 * there is nothing legitimate to call. They are on screen because C2 is far
 * easier to hold when the difference between a decision and an estimate is
 * visible from the first day.
 */
export function App() {
  return (
    <main className="shell">
      <header className="shell__header">
        <h1>ZoikoTax</h1>
        <p className="shell__subtitle">
          W0 skeleton. No authoritative fiscal output is permitted before the A4 gate.
        </p>
      </header>

      <CellHealth />

      <section className="panel" aria-labelledby="amounts-heading">
        <h2 id="amounts-heading">Two kinds of number</h2>
        <p className="panel__note">
          Illustrative constants, not API responses. They are here because an authoritative
          decision and an estimate are different components with different types (ADR-0019
          C2), and because every amount crosses the wire as a decimal string and is
          displayed rather than computed (C1).
        </p>
        <div className="amounts">
          <AuthoritativeAmount
            label="Tax determined"
            value={money('1.75', 'EUR')}
            decisionId="ztx_dec_000000000000000000000000000"
          />
          <AdvisoryAmount
            label="Estimated tax"
            value={money('1.75', 'EUR')}
            basis="Indicative quote, no jurisdiction resolved"
          />
        </div>
      </section>
    </main>
  )
}
