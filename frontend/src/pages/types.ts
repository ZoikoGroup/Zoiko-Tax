import type { NavTarget, Route, View } from '../components/layout/Sidebar'
import type { SessionInfo } from '../platform/api'

/** What every workspace page receives from the shell. */
export interface PageProps {
  readonly session: SessionInfo
  /** The tax period the header selects, YYYY-MM. */
  readonly period: string
  /** Opens another workspace, e.g. from an action to its obligation. */
  readonly onNavigate: (view: View, options?: Omit<NavTarget, 'view'> & { readonly draft?: string }) => void
  /** Where the shell is: a page with tabs opens the one it was sent to. */
  readonly route?: Route
  /** A page with tabs reports a tab change, so the menu follows it. */
  readonly onTabChange?: (tab: string) => void
}
