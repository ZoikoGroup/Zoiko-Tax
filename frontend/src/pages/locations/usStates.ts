/**
 * The fifty states and the District of Columbia, each with the tile it takes
 * in the grid map: one equal square per state, placed roughly where the state
 * is, so a small state is as easy to read and click as a large one.
 *
 * The ISO 3166-2 code is what a place of supply names (`US-CA`); the content
 * pack's jurisdiction for it is `jurisdiction:us-ca`.
 */
export interface UsState {
  /** The two-letter postal code. */
  readonly code: string
  readonly name: string
  readonly row: number
  readonly col: number
}

export const US_STATES: readonly UsState[] = [
  { code: 'AK', name: 'Alaska', row: 0, col: 0 },
  { code: 'ME', name: 'Maine', row: 0, col: 11 },
  { code: 'VT', name: 'Vermont', row: 1, col: 10 },
  { code: 'NH', name: 'New Hampshire', row: 1, col: 11 },
  { code: 'WA', name: 'Washington', row: 2, col: 1 },
  { code: 'ID', name: 'Idaho', row: 2, col: 2 },
  { code: 'MT', name: 'Montana', row: 2, col: 3 },
  { code: 'ND', name: 'North Dakota', row: 2, col: 4 },
  { code: 'MN', name: 'Minnesota', row: 2, col: 5 },
  { code: 'IL', name: 'Illinois', row: 2, col: 6 },
  { code: 'WI', name: 'Wisconsin', row: 2, col: 7 },
  { code: 'MI', name: 'Michigan', row: 2, col: 8 },
  { code: 'NY', name: 'New York', row: 2, col: 9 },
  { code: 'RI', name: 'Rhode Island', row: 2, col: 10 },
  { code: 'MA', name: 'Massachusetts', row: 2, col: 11 },
  { code: 'OR', name: 'Oregon', row: 3, col: 1 },
  { code: 'NV', name: 'Nevada', row: 3, col: 2 },
  { code: 'WY', name: 'Wyoming', row: 3, col: 3 },
  { code: 'SD', name: 'South Dakota', row: 3, col: 4 },
  { code: 'IA', name: 'Iowa', row: 3, col: 5 },
  { code: 'IN', name: 'Indiana', row: 3, col: 6 },
  { code: 'OH', name: 'Ohio', row: 3, col: 7 },
  { code: 'PA', name: 'Pennsylvania', row: 3, col: 8 },
  { code: 'NJ', name: 'New Jersey', row: 3, col: 9 },
  { code: 'CT', name: 'Connecticut', row: 3, col: 10 },
  { code: 'CA', name: 'California', row: 4, col: 1 },
  { code: 'UT', name: 'Utah', row: 4, col: 2 },
  { code: 'CO', name: 'Colorado', row: 4, col: 3 },
  { code: 'NE', name: 'Nebraska', row: 4, col: 4 },
  { code: 'MO', name: 'Missouri', row: 4, col: 5 },
  { code: 'KY', name: 'Kentucky', row: 4, col: 6 },
  { code: 'WV', name: 'West Virginia', row: 4, col: 7 },
  { code: 'VA', name: 'Virginia', row: 4, col: 8 },
  { code: 'MD', name: 'Maryland', row: 4, col: 9 },
  { code: 'DE', name: 'Delaware', row: 4, col: 10 },
  { code: 'AZ', name: 'Arizona', row: 5, col: 2 },
  { code: 'NM', name: 'New Mexico', row: 5, col: 3 },
  { code: 'KS', name: 'Kansas', row: 5, col: 4 },
  { code: 'AR', name: 'Arkansas', row: 5, col: 5 },
  { code: 'TN', name: 'Tennessee', row: 5, col: 6 },
  { code: 'NC', name: 'North Carolina', row: 5, col: 7 },
  { code: 'SC', name: 'South Carolina', row: 5, col: 8 },
  { code: 'DC', name: 'District of Columbia', row: 5, col: 9 },
  { code: 'OK', name: 'Oklahoma', row: 6, col: 4 },
  { code: 'LA', name: 'Louisiana', row: 6, col: 5 },
  { code: 'MS', name: 'Mississippi', row: 6, col: 6 },
  { code: 'AL', name: 'Alabama', row: 6, col: 7 },
  { code: 'GA', name: 'Georgia', row: 6, col: 8 },
  { code: 'HI', name: 'Hawaii', row: 7, col: 0 },
  { code: 'TX', name: 'Texas', row: 7, col: 4 },
  { code: 'FL', name: 'Florida', row: 7, col: 9 },
]

/** The content pack's jurisdiction for a state. */
export function stateJurisdiction(code: string): string {
  return `jurisdiction:us-${code.toLowerCase()}`
}
