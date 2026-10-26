import { civilDate } from '../../dashboard/format'
import { Empty } from '../../ui/kit'
import { percent, serviceLabel, type Component } from './coverage'

/** One component's rate as the content states it: a proportion, or an amount. */
export const RateText = ({ c }: { c: Component }) => {
  if (c.rateKind === 'AD_VALOREM') {
    return <span className="mono">{percent(c.rate)}</span>
  }
  return (
    <span className="mono">
      {c.rate} {c.currency ?? ''}{c.unit === undefined ? '' : ` / ${c.unit}`}
    </span>
  )
}

/**
 * The components in force, each with the citation that puts it there. A
 * legal zero names its reason, so an exemption never reads as a missing rate.
 */
export const ComponentsTable = ({ components, caption, showServices = true }: {
  components: readonly Component[]
  caption: string
  showServices?: boolean
}) => {
  if (components.length === 0) {
    return <Empty>No component in force.</Empty>
  }
  return (
    <div className="zp-scroll">
      <table className="table">
        <caption className="table__caption">{caption}</caption>
        <thead>
          <tr>
            <th scope="col">Component</th>
            <th scope="col">Type</th>
            <th scope="col">Rate</th>
            <th scope="col">Authority</th>
            {showServices ? <th scope="col">Services</th> : null}
            <th scope="col">In force from</th>
            <th scope="col">Source</th>
          </tr>
        </thead>
        <tbody>
          {components.map((c) => (
            <tr key={c.id}>
              <td>
                <div>{c.name}</div>
                <div className="mono muted loc-small">{c.id}</div>
              </td>
              <td className="mono">
                {c.taxType}
                {c.treatments.includes('PROVIDER_COST') ? (
                  <div title="Borne by the provider: accrued as its cost, never on the customer's invoice">
                    <span className="ztax-status ztax-status--neutral">PROVIDER COST</span>
                  </div>
                ) : null}
              </td>
              <td>
                <RateText c={c} />
                {c.legalReason === undefined ? null : (
                  <div><span className="ztax-status ztax-status--neutral">{c.legalReason.replaceAll('_', ' ')}</span></div>
                )}
              </td>
              <td className="mono loc-small">{c.authority.replace(/^authority:/, '')}</td>
              {showServices ? (
                <td className="loc-small">{(c.services ?? []).map(serviceLabel).join(', ') || 'All in the regime'}</td>
              ) : null}
              <td className="mono">
                {civilDate(c.from)}
                {c.to === undefined ? null : <div className="muted">until {civilDate(c.to)}</div>}
              </td>
              <td className="loc-small">
                <div className="mono">{c.source}</div>
                <details className="loc-cite">
                  <summary>Citation</summary>
                  <Citation text={c.citation} />
                </details>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

/** A citation with any URL in it as a link. */
export const Citation = ({ text }: { text: string }) => {
  const parts = text.split(/(https?:\/\/\S+)/g)
  return (
    <p className="loc-cite-text">
      {parts.map((p, i) =>
        /^https?:\/\//.test(p) ? (
          <a key={i} href={p.replace(/[).,;]+$/, '')} target="_blank" rel="noreferrer noopener">
            {p}
          </a>
        ) : (
          <span key={i}>{p}</span>
        ),
      )}
    </p>
  )
}
