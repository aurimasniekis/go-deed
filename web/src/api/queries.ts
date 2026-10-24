import { queryOptions, useQuery } from '@tanstack/react-query'
import { api, unwrap } from './client'

/**
 * Query keys, grouped by the live-update area that invalidates them (src/api/live.ts). The first element is the area,
 * so `invalidateQueries({ queryKey: ['entities'] })` refreshes every entity list and detail.
 */
export const keys = {
  dashboard: (projects: string[]) => ['dashboard', projects] as const,
  entities: (params: object) => ['entities', 'list', params] as const,
  entity: (id: string) => ['entities', 'detail', id] as const,
  entityPart: (id: string, part: string, extra?: object) => ['entities', 'detail', id, part, extra ?? {}] as const,
  relations: (params: object) => ['relations', 'list', params] as const,
  unresolved: () => ['relations', 'unresolved'] as const,
  graph: (op: string, params: object) => ['graph', op, params] as const,
  projects: () => ['projects'] as const,
  members: (project: string) => ['projects', project, 'members'] as const,
  kinds: () => ['kinds'] as const,
  kind: (name: string) => ['kinds', name] as const,
  relTypes: () => ['relationship-types'] as const,
  providers: () => ['providers'] as const,
  sources: (project?: string) => ['sources', 'list', project ?? ''] as const,
  source: (id: string) => ['sources', 'detail', id] as const,
  sourcePart: (id: string, part: string) => ['sources', 'detail', id, part] as const,
  findings: (params: object) => ['findings', params] as const,
  alerts: (params: object) => ['alerts', params] as const,
  alertRules: () => ['alert-rules'] as const,
  silences: () => ['silences'] as const,
  views: (kind: string, project?: string) => ['views', kind, project ?? ''] as const,
  users: () => ['users'] as const,
  bridges: () => ['bridges'] as const,
  audit: (params: object) => ['audit', params] as const,
  account: (part: string) => ['account', part] as const,
}

export const projectsQuery = queryOptions({
  queryKey: keys.projects(),
  queryFn: async () => unwrap(await api.GET('/projects')),
  staleTime: 60_000,
})

export function useProjects() {
  return useQuery(projectsQuery)
}

export const kindsQuery = queryOptions({
  queryKey: keys.kinds(),
  queryFn: async () => unwrap(await api.GET('/kinds')),
  staleTime: 5 * 60_000,
})

export function useKinds() {
  return useQuery(kindsQuery)
}

export const relTypesQuery = queryOptions({
  queryKey: keys.relTypes(),
  queryFn: async () => unwrap(await api.GET('/relationship-types')),
  staleTime: 5 * 60_000,
})

export function useRelTypes() {
  return useQuery(relTypesQuery)
}

export const providersQuery = queryOptions({
  queryKey: keys.providers(),
  queryFn: async () => unwrap(await api.GET('/providers')),
  staleTime: 10 * 60_000,
})

export function useProviders() {
  return useQuery(providersQuery)
}

export function sourcesQuery(project?: string) {
  return queryOptions({
    queryKey: keys.sources(project),
    queryFn: async () => unwrap(await api.GET('/sources', { params: { query: project ? { project } : {} } })),
    staleTime: 15_000,
  })
}

export function useSources(project?: string) {
  return useQuery(sourcesQuery(project))
}
