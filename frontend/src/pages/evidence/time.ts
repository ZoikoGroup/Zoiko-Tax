/**
 * Timestamps as the API takes them: RFC 3339 UTC with exactly six fractional
 * digits and a literal Z (the Timestamp schema). Inputs here are read as UTC.
 */

/** "2026-10-08T00:00" or "2026-10-08T00:00:30" (datetime-local, read as UTC) → API Timestamp. */
export function fromLocalInput(value: string): string | null {
  if (/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$/.test(value)) {
    return `${value}:00.000000Z`
  }
  if (/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}$/.test(value)) {
    return `${value}.000000Z`
  }
  return null
}

/** "2026-10-08" (a date input) → midnight UTC that day as an API Timestamp. */
export function fromDateInput(value: string): string | null {
  return /^\d{4}-\d{2}-\d{2}$/.test(value) ? `${value}T00:00:00.000000Z` : null
}

/** An API Timestamp → the value a datetime-local input shows, minutes precision. */
export function toLocalInput(ts: string): string {
  return ts.slice(0, 16)
}

/** Midnight UTC of the day `offset` days from today, as a datetime-local value. */
export function utcMidnight(offset: number): string {
  const now = new Date()
  const day = new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate() + offset))
  return day.toISOString().slice(0, 16)
}

/** Non-empty trimmed lines of a textarea. */
export function lines(text: string): string[] {
  return text
    .split('\n')
    .map((l) => l.trim())
    .filter((l) => l !== '')
}
