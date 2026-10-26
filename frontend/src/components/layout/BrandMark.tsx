/** The ring-and-nodes mark from the ZoikoTax wordmark, drawn for a dark tile. */
export const BrandMark = ({ size = 34 }: { size?: number }) => (
  <svg viewBox="0 0 40 40" width={size} height={size} fill="none" stroke="currentColor" strokeWidth="2.2" aria-hidden="true">
    <circle cx="20" cy="20" r="14" />
    <path d="M20 9v7M20 24v7M9 20h7M24 20h7" />
    <circle cx="20" cy="20" r="2.6" fill="currentColor" stroke="none" />
    <circle cx="20" cy="9" r="1.8" fill="currentColor" stroke="none" />
    <circle cx="20" cy="31" r="1.8" fill="currentColor" stroke="none" />
    <circle cx="9" cy="20" r="1.8" fill="currentColor" stroke="none" />
    <circle cx="31" cy="20" r="1.8" fill="currentColor" stroke="none" />
  </svg>
)
