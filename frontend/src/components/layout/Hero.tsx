import { groupOf, labelOf, type View } from './Sidebar'

const STAGES = ['Determine', 'Obligate', 'Comply', 'Reconcile', 'Prove'] as const
type Stage = (typeof STAGES)[number]

/** Where a workspace sits in the fiscal pipeline, when it sits in one. */
const STAGE: Partial<Record<View, Stage>> = {
  determine: 'Determine',
  locations: 'Determine',
  obligations: 'Obligate',
  compliance: 'Comply',
  documents: 'Comply',
  remittance: 'Comply',
  reconcile: 'Reconcile',
  shadow: 'Prove',
  evidence: 'Prove',
}

/**
 * The band at the top of every workspace: where you are, the platform's
 * promise, and which stage of determine → prove this screen serves.
 */
export const Hero = ({ view }: { view: View }) => {
  const stage = STAGE[view]
  return (
    <section className="zt-hero" aria-label={labelOf(view)}>
      <div className="zt-hero-text">
        <div className="zt-hero-eyebrow">ZoikoTax {groupOf(view)} · {labelOf(view)}</div>
        <h1>Answers for a complex tax world.</h1>
        <p>Telecom tax clarity, across jurisdictions.</p>
      </div>
      <TowerArt />
      <ol className="zt-hero-flow" aria-label="Fiscal pipeline">
        {STAGES.map((s) => (
          <li key={s} className={s === stage ? 'zt-hero-flow--on' : undefined} aria-current={s === stage ? 'step' : undefined}>
            {s}
          </li>
        ))}
      </ol>
    </section>
  )
}

/** A lattice mast against a dusk sky, behind two glass panes. Decorative. */
const TowerArt = () => (
  <svg className="zt-hero-art" viewBox="0 0 520 170" preserveAspectRatio="xMaxYMid slice" aria-hidden="true">
    <defs>
      <linearGradient id="zt-sky" x1="0" y1="0" x2="0" y2="1">
        <stop offset="0" stopColor="#5a4a9c" />
        <stop offset=".45" stopColor="#b77aa0" />
        <stop offset=".75" stopColor="#f0a36f" />
        <stop offset="1" stopColor="#f7d2a8" />
      </linearGradient>
      <linearGradient id="zt-pane" x1="0" y1="0" x2="1" y2="1">
        <stop offset="0" stopColor="#ffffff" stopOpacity=".75" />
        <stop offset="1" stopColor="#ffffff" stopOpacity=".1" />
      </linearGradient>
      <linearGradient id="zt-fade" x1="0" y1="0" x2="1" y2="0">
        <stop offset="0" stopColor="#ffffff" stopOpacity="1" />
        <stop offset=".35" stopColor="#ffffff" stopOpacity="0" />
      </linearGradient>
    </defs>
    <polygon points="160,0 520,0 520,170 60,170" fill="url(#zt-sky)" />
    <polygon points="120,170 230,0 300,0 190,170" fill="url(#zt-pane)" />
    <polygon points="215,170 325,0 360,0 250,170" fill="#ffffff" opacity=".35" />
    <g stroke="#2a1f4a" strokeWidth="1.6" fill="none" opacity=".85">
      <path d="M370 170 L392 22 L414 170" />
      <path d="M376 130 L408 130 M380 100 L404 100 M384 72 L400 72 M388 46 L396 46" />
      <path d="M376 130 L404 100 L380 100 L400 72 L384 72 L396 46" />
      <path d="M408 130 L380 100 M404 100 L384 72 M400 72 L388 46" />
      <path d="M392 22 L392 8" />
      <rect x="380" y="30" width="6" height="14" fill="#2a1f4a" />
      <rect x="398" y="30" width="6" height="14" fill="#2a1f4a" />
      <rect x="379" y="54" width="5" height="11" fill="#2a1f4a" />
      <rect x="400" y="54" width="5" height="11" fill="#2a1f4a" />
    </g>
    <circle cx="392" cy="8" r="2.4" fill="#e0533a" />
    <rect x="0" y="0" width="520" height="170" fill="url(#zt-fade)" />
  </svg>
)
