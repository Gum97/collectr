import { useQuery } from '@tanstack/react-query'
import { api, RequestFailed } from './api'

export interface Me {
  user_id: string
  tenant_id: string
  kind: string
  capabilities: string[]
  email?: string
  name?: string
  org_role?: string
  org_name?: string
  mfa_enabled?: boolean
  mfa_required?: boolean
  recovery_codes_left?: number
}

/** useMe resolves the current session.
 *
 * A 401 is a valid answer, not an error: it means "signed out", and treating it
 * as a failure would put a retry loop on the login screen. */
export function useMe() {
  return useQuery({
    queryKey: ['me'],
    queryFn: async (): Promise<Me | null> => {
      try {
        return await api.get<Me>('/api/v1/auth/me')
      } catch (err) {
        if (err instanceof RequestFailed && err.status === 401) return null
        throw err
      }
    },
    staleTime: 60_000,
    retry: false,
  })
}

export function can(me: Me | null | undefined, capability: string): boolean {
  return Boolean(me?.capabilities.includes(capability))
}

/** Whether the account is signed in but held to enrolling a second factor.
 *
 * Roles that reach personal data across the whole organisation hold no
 * capabilities until they enrol, so every screen would otherwise render as a
 * permission error with no explanation. */
export function mfaGated(me: Me | null | undefined): boolean {
  return Boolean(me?.mfa_required && !me?.mfa_enabled)
}
