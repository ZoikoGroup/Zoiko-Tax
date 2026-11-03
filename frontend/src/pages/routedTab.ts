import { useEffect, useState } from 'react'

import type { Route } from '../components/layout/Sidebar'

/**
 * A page's current tab, as the menu sees it. The page opens on the tab a
 * navigation named, re-applies it when the menu sends it there again (a new
 * route seq), and reports a tab the user picks so the menu can follow.
 */
export function useRoutedTab<T extends string>(
  tabs: readonly T[],
  initial: T,
  route: Route | undefined,
  onTabChange: ((tab: string) => void) | undefined,
): [T, (tab: T) => void] {
  const isTab = (t: string | undefined): t is T => t !== undefined && (tabs as readonly string[]).includes(t)
  const [tab, setTab] = useState<T>(() => (isTab(route?.tab) ? route.tab : initial))

  useEffect(() => {
    const wanted = route?.tab
    if (isTab(wanted)) {
      setTab(wanted)
    } else {
      // Opened without a tab: tell the menu which one is showing.
      onTabChange?.(tab)
    }
    // Only a new navigation re-applies the route's tab.
  }, [route?.seq])

  const select = (t: T) => {
    setTab(t)
    onTabChange?.(t)
  }
  return [tab, select]
}
