import { queryOptions, useQuery } from '@tanstack/react-query'
import { api, unwrap } from '@/api/client'
import { keys } from '@/api/queries'
import type { ProvenanceEntry } from '@/lib/provenance'

export const entityQuery = (id: string) =>
  queryOptions({
    queryKey: keys.entity(id),
    queryFn: async () => unwrap(await api.GET('/entities/{id}', { params: { path: { id } } })),
  })

export function useEntity(id: string) {
  return useQuery({ ...entityQuery(id), enabled: id !== '' })
}

export function useFacets(id: string) {
  return useQuery({
    queryKey: keys.entityPart(id, 'facets'),
    queryFn: async () => unwrap(await api.GET('/entities/{id}/facets', { params: { path: { id } } })),
  })
}

export function useProvenance(id: string) {
  return useQuery({
    queryKey: keys.entityPart(id, 'provenance'),
    queryFn: async () =>
      unwrap(await api.GET('/entities/{id}/provenance', { params: { path: { id } } })) as unknown as Record<
        string,
        ProvenanceEntry
      >,
  })
}

export function useConflicts(id: string) {
  return useQuery({
    queryKey: keys.entityPart(id, 'conflicts'),
    queryFn: async () => unwrap(await api.GET('/entities/{id}/conflicts', { params: { path: { id } } })),
  })
}

export function useEntityFindings(id: string) {
  return useQuery({
    queryKey: ['findings', { entity: id }],
    queryFn: async () =>
      unwrap(await api.GET('/findings', { params: { query: { entity: id, state: 'open', limit: 100 } } })),
  })
}

export function useEntityRelations(id: string) {
  return useQuery({
    queryKey: keys.relations({ entity: id, limit: 500 }),
    queryFn: async () =>
      unwrap(await api.GET('/relations', { params: { query: { entity: id, limit: 500, direction: 'both' } } })),
  })
}

export function useRelationDetail(id: string, enabled: boolean) {
  return useQuery({
    queryKey: ['relations', 'detail', id],
    queryFn: async () => unwrap(await api.GET('/relations/{id}', { params: { path: { id } } })),
    enabled,
  })
}

/** The declaration of one source (404 → null: the source has not declared this entity). */
export function useDeclaration(id: string, source: string, enabled = true) {
  return useQuery({
    queryKey: keys.entityPart(id, 'declaration', { source }),
    queryFn: async () => {
      const res = await api.GET('/entities/{id}/declarations/{source}', { params: { path: { id, source } } })
      if (res.response.status === 404) return null
      return unwrap(res)
    },
    enabled,
    retry: false,
  })
}

export const TELEMETRY_WINDOWS = ['15m', '1h', '6h', '24h', '7d'] as const
export type TelemetryWindow = (typeof TELEMETRY_WINDOWS)[number]

/** Telemetry results of every binding that applies to the entity (an empty list = no bindings apply). */
export function useTelemetry(id: string, window: TelemetryWindow) {
  return useQuery({
    queryKey: keys.entityPart(id, 'telemetry', { window }),
    queryFn: async () =>
      unwrap(await api.GET('/entities/{id}/telemetry', { params: { path: { id }, query: { window } } })),
    staleTime: 30_000,
  })
}
