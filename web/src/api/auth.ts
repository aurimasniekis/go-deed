import { queryOptions, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, ApiError, type Me, type ProjectView } from './client'
import { setCsrfToken } from './session'

export const meKey = ['me'] as const

/** GET /me; keeps the CSRF token in sync. Resolves to null without a session. */
export async function fetchMe(): Promise<Me | null> {
  const res = await api.GET('/me')
  if (res.response.status === 401) {
    setCsrfToken('')
    return null
  }
  if (res.error !== undefined) throw new ApiError(res.error, res.response.status)
  setCsrfToken(res.data.csrf_token)
  return res.data
}

export const meQuery = queryOptions({
  queryKey: meKey,
  queryFn: fetchMe,
  retry: false,
  staleTime: 30_000,
  refetchInterval: 120_000,
})

export function useMe() {
  return useQuery(meQuery)
}

/** Stores a freshly returned Me (after login, MFA, re-authentication) without another round trip. */
export function useSetMe() {
  const qc = useQueryClient()
  return (me: Me | null) => {
    setCsrfToken(me?.csrf_token)
    qc.setQueryData(meKey, me)
  }
}

export const systemInfoQuery = queryOptions({
  queryKey: ['system-info'],
  queryFn: async () => {
    const res = await api.GET('/system/info')
    if (res.error !== undefined) throw new ApiError(res.error, res.response.status)
    return res.data
  },
  staleTime: 60_000,
  retry: false,
})

export function useSystemInfo() {
  return useQuery(systemInfoQuery)
}

/** A session that finished sign-in (not limited by a pending second factor or enrollment). */
export function isFullSession(me: Me | null | undefined): me is Me {
  return !!me && !me.mfa_pending && !me.must_enroll
}

export type InstallationRole = 'owner' | 'admin' | 'auditor'
export type ProjectRole = 'viewer' | 'editor' | 'admin'

const projectLevel: Record<string, number> = { viewer: 1, editor: 2, admin: 3 }

/** Owners and admins administer the installation (users, kinds, bridges, every project). */
export function isInstallationAdmin(me: Me | null | undefined): boolean {
  return isFullSession(me) && (me.installation_role === 'owner' || me.installation_role === 'admin')
}

/** Owners, admins and auditors read every audit record. */
export function canReadAllAudit(me: Me | null | undefined): boolean {
  return isInstallationAdmin(me) || (isFullSession(me) && me.installation_role === 'auditor')
}

/** Whether the effective project role (from GET /projects) reaches need. The server enforces it; this only hides what would fail. */
export function projectAtLeast(project: Pick<ProjectView, 'role'> | undefined, need: ProjectRole): boolean {
  return !!project && (projectLevel[project.role] ?? 0) >= (projectLevel[need] ?? 99)
}

/** Whether any readable project grants at least need. */
export function anyProjectAtLeast(projects: Pick<ProjectView, 'role'>[] | undefined, need: ProjectRole): boolean {
  return (projects ?? []).some((p) => projectAtLeast(p, need))
}

export const installationRoleInfo: Record<InstallationRole, { color: string; description: string }> = {
  owner: { color: 'red', description: 'Everything, including other owners and break-glass recovery' },
  admin: { color: 'deed', description: 'Users, kinds, bridges and every project (admin role)' },
  auditor: { color: 'grape', description: 'Reads every project and the full audit log' },
}

export const projectRoleInfo: Record<ProjectRole, { color: string; description: string }> = {
  admin: { color: 'deed', description: 'Members, sources, settings and everything editors can do' },
  editor: { color: 'teal', description: 'Declares and annotates entities and relationships' },
  viewer: { color: 'gray', description: 'Reads the project' },
}
