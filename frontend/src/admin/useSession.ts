import { useCallback, useEffect, useState } from 'react'

import { ApiError, api, type SessionInfo } from '../platform/api'

/**
 * Who the viewer is, according to the server.
 *
 * ADR-0019 C8 forbids fiscal or personal data in browser storage, and this hook
 * is where that constraint stops being a rule and becomes the design: there is
 * nothing to store. The session token is an httpOnly cookie the browser holds
 * and JavaScript cannot read, so the only way to know who you are is to ask —
 * which is what this does on mount, and again after any sign-in or sign-out.
 *
 * The consequence worth knowing: a page reload always costs one round trip
 * before the shell can render. That is the correct trade. Caching identity in
 * localStorage would mean a disabled account still rendering its own admin
 * screens until something happened to refresh it.
 */

/** 'checking' is the initial read, and is the only state that may render as a
 *  wait. 'anonymous' and 'signed-in' are answers, not stages. */
export type SessionState = 'checking' | 'anonymous' | 'signed-in' | 'unreachable'

export interface UseSession {
  readonly state: SessionState
  readonly session: SessionInfo | null
  readonly detail: string
  readonly refresh: () => Promise<void>
  readonly signIn: (tenant: string, email: string, password: string) => Promise<void>
  readonly signOut: () => Promise<void>
}

export function useSession(): UseSession {
  const [state, setState] = useState<SessionState>('checking')
  const [session, setSession] = useState<SessionInfo | null>(null)
  const [detail, setDetail] = useState('')

  const refresh = useCallback(async () => {
    try {
      const info = await api.session()
      setSession(info)
      setDetail('')
      setState('signed-in')
    } catch (error) {
      setSession(null)
      if (error instanceof ApiError && error.isUnauthenticated) {
        // Not an error condition. Nobody is signed in, which is a perfectly
        // ordinary state for a sign-in screen to be in.
        setDetail('')
        setState('anonymous')
        return
      }
      // C3: an unreachable cell renders as unreachable, not as a sign-in form
      // that will fail for reasons the user cannot see.
      setDetail(error instanceof Error ? error.message : 'no response')
      setState('unreachable')
    }
  }, [])

  useEffect(() => { void refresh() }, [refresh])

  const signIn = useCallback(async (tenant: string, email: string, password: string) => {
    const info = await api.signIn(tenant, email, password)
    setSession(info)
    setDetail('')
    setState('signed-in')
  }, [])

  const signOut = useCallback(async () => {
    try {
      await api.signOut()
    } finally {
      // Whether or not the call succeeded, this browser is done with the
      // session. If the revoke failed the cookie is still cleared server-side
      // on the next rejected request, and leaving the user on an admin screen
      // they have asked to leave is the worse outcome.
      setSession(null)
      setState('anonymous')
    }
  }, [])

  return { state, session, detail, refresh, signIn, signOut }
}
