import type { View } from '../components/layout/Sidebar'
import type { SessionInfo } from '../platform/api'

/** What every workspace page receives from the shell. */
export interface PageProps {
  readonly session: SessionInfo
  /** The tax period the header selects, YYYY-MM. */
  readonly period: string
  /** Opens another workspace, e.g. from an action to its obligation. */
  readonly onNavigate: (view: View) => void
}
