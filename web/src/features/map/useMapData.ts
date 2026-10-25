import { useQueries, useQuery } from '@tanstack/react-query'
import { useMemo } from 'react'
import { api, unwrap, type GraphEdge, type GraphResult, type ImpactResult } from '@/api/client'
import { keys } from '@/api/queries'
import { decodeList, type Evidence, type MapSearch } from '@/lib/urlState'
import { mergeResults, MAX_NODES, type Expansion } from './model'
import { bundleSources, toMapEdge } from './types'

/** Adds one more relationship to a client-side bundle, keeping the union of the asserting sources. */
function addToBundle(cur: GraphEdge, e: GraphEdge): GraphEdge {
  const { sources, primarySource } = bundleSources(toMapEdge(cur), toMapEdge(e))
  return {
    ...cur,
    count: (cur.count ?? 1) + 1,
    sources: sources.map((s) => ({
      id: s.id,
      name: s.name,
      ...(s.displayName ? { display_name: s.displayName } : {}),
      layer: s.layer,
      project_id: s.projectId,
      relations: s.relations,
    })),
    primary_source: primarySource,
  }
}

export type MapMode = 'overview' | 'neighborhood' | 'path' | 'impact'

const GROUP_FILTERS = ['kind', 'environment', 'owner', 'project'] as const

/**
 * Expands an overview group (`kind|service`, `environment|prod`, …). /graph/expand only accepts fan-cluster keys, so
 * the members come from GET /entities and their relationships from a depth-1 neighbourhood; relationships to entities
 * outside the group are re-pointed at that entity's (still collapsed) overview group, bundled with counts.
 */
export async function expandOverviewGroup(
  key: string,
  projects: string[],
  types: string[],
  evidence: Evidence[],
): Promise<GraphResult> {
  const [groupBy = '', value = ''] = key.split('|', 2)
  const empty: GraphResult = { nodes: [], edges: [], notes: [], truncated: false, elapsed_ms: 0 }
  if (!(GROUP_FILTERS as readonly string[]).includes(groupBy)) return { ...empty, notes: [`cannot expand ${key}`] }
  if (value === '') return { ...empty, notes: [`entities without a ${groupBy} cannot be listed by filter`] }
  const filter =
    groupBy === 'kind'
      ? { kind: [value] }
      : groupBy === 'environment'
        ? { environment: [value] }
        : groupBy === 'owner'
          ? { owner: value }
          : { project: [value] }
  const page = unwrap(
    await api.GET('/entities', {
      params: {
        query: { ...filter, limit: 500, ...(projects.length && groupBy !== 'project' ? { project: projects } : {}) },
      },
    }),
  )
  const members = new Set(page.items.map((e) => e.metadata.id))
  if (members.size === 0) return empty
  const hood = unwrap(
    await api.GET('/graph/neighborhood', {
      params: {
        query: {
          root: [...members].slice(0, 300),
          depth: 1,
          direction: 'both',
          max_nodes: 2000,
          include_stale: true,
          ...(types.length ? { type: types } : {}),
          ...(evidence.length ? { evidence } : {}),
        },
      },
    }),
  )
  const groupOf = (n: GraphResult['nodes'][number]): string => {
    switch (groupBy) {
      case 'kind':
        return n.kind
      case 'environment':
        return n.environment ?? ''
      case 'owner':
        return n.owner ?? ''
      default:
        return n.project_id
    }
  }
  const outside = new Map(
    hood.nodes.filter((n) => !members.has(n.id)).map((n) => [n.id, `group:${groupBy}:${groupOf(n)}`]),
  )
  const edges = new Map<string, GraphResult['edges'][number]>()
  for (const e of hood.edges) {
    // Direct edges stay (they render when the other end is loaded, e.g. its group is expanded too) …
    edges.set(e.id, e)
    // … and an aggregated edge points at the other end's collapsed group.
    const from = outside.get(e.from) ?? e.from
    const to = outside.get(e.to) ?? e.to
    if (from === e.from && to === e.to) continue
    const id = `agg:${from}>${to}:${e.type}`
    const cur = edges.get(id)
    edges.set(id, cur ? addToBundle(cur, e) : { ...e, id, from, to, count: 1 })
  }
  return {
    nodes: hood.nodes.filter((n) => members.has(n.id)).map((n) => ({ ...n, depth: 1 })),
    edges: [...edges.values()],
    notes:
      page.items.length < page.total
        ? [`${groupBy} ${value}: showing ${page.items.length} of ${page.total} entities`]
        : [],
    truncated: page.items.length < page.total || hood.truncated,
    elapsed_ms: hood.elapsed_ms,
  }
}

export function modeOf(s: MapSearch): MapMode {
  if (s.mode === 'path' && s.from && s.to) return 'path'
  if (s.mode === 'impact' && s.focus) return 'impact'
  if (s.focus) return 'neighborhood'
  return 'overview'
}

function mark(name: string) {
  try {
    performance.mark(name)
  } catch {
    // performance marks are diagnostics only
  }
}

/**
 * Loads what the map shows: the base query for the mode (overview groups, a neighbourhood, a path, an impact
 * traversal) plus on-demand expansions from the URL (`expand`: cluster keys, or `e:<id>` entity neighbourhoods), and
 * entity IDs for a source filter. Server caps (depth, max_nodes) keep every answer within the render budget.
 * withSources asks the overview for its bundles' asserting sources (colour-by-source mode); other answers always
 * carry them.
 */
export function useMapData(s: MapSearch, globalProjects: string[], withSources = false) {
  const mode = modeOf(s)
  const focus = decodeList(s.focus)
  const types = decodeList(s.type)
  const kinds = decodeList(s.kind)
  const evidence = decodeList(s.evidence) as Evidence[]
  const projects = decodeList(s.project)
  const scopeProjects = projects.length ? projects : globalProjects
  // A plain object: the query key hashes it by value.
  const params = (() => {
    switch (mode) {
      case 'overview':
        return {
          group_by: s.group ?? 'kind',
          ...(scopeProjects.length ? { project: scopeProjects } : {}),
          ...(withSources ? { sources: true } : {}),
        }
      case 'neighborhood':
        return {
          root: focus,
          depth: s.depth ?? 2,
          direction: s.dir ?? 'both',
          max_nodes: MAX_NODES,
          include_stale: true,
          ...(types.length ? { type: types } : {}),
          ...(kinds.length ? { kind: kinds } : {}),
          ...(evidence.length ? { evidence } : {}),
        }
      case 'path':
        return { from: s.from ?? '', to: s.to ?? '', max_depth: s.depth ?? 6, ...(types.length ? { type: types } : {}) }
      case 'impact':
        return { entity: focus[0] ?? '', direction: s.impact ?? 'downstream', depth: s.depth ?? 4 }
    }
  })()

  const base = useQuery({
    queryKey: keys.graph(mode, params),
    queryFn: async (): Promise<GraphResult | ImpactResult> => {
      mark('deed-map-fetch-start')
      let r: GraphResult | ImpactResult
      switch (mode) {
        case 'overview':
          r = unwrap(await api.GET('/graph/overview', { params: { query: params as { group_by: 'kind' } } }))
          break
        case 'neighborhood':
          r = unwrap(await api.GET('/graph/neighborhood', { params: { query: params as { root: string[] } } }))
          break
        case 'path':
          r = unwrap(await api.GET('/graph/path', { params: { query: params as { from: string; to: string } } }))
          break
        case 'impact':
          r = unwrap(await api.GET('/graph/impact', { params: { query: params as { entity: string } } }))
          break
      }
      mark('deed-map-data')
      return r
    },
    staleTime: 30_000,
  })

  const expandList = decodeList(s.expand)
  const expansions = useQueries({
    queries: expandList.map((k) => ({
      queryKey: keys.graph('expand', { k, types, kinds, evidence }),
      queryFn: async (): Promise<Expansion> => {
        if (k.startsWith('e:')) {
          const result = unwrap(
            await api.GET('/graph/neighborhood', {
              params: {
                query: {
                  root: [k.slice(2)],
                  depth: 1,
                  direction: 'both',
                  max_nodes: 300,
                  include_stale: true,
                  ...(types.length ? { type: types } : {}),
                  ...(kinds.length ? { kind: kinds } : {}),
                  ...(evidence.length ? { evidence } : {}),
                },
              },
            }),
          )
          return { key: null, result }
        }
        if (k.startsWith('fan|')) {
          return {
            key: k,
            result: unwrap(await api.GET('/graph/expand', { params: { query: { key: k, limit: 500 } } })),
          }
        }
        return { key: k, result: await expandOverviewGroup(k, scopeProjects, types, evidence) }
      },
      staleTime: 30_000,
    })),
  })

  const sourceIds = decodeList(s.source)
  const sources = useQuery({
    queryKey: keys.entities({ mapSources: sourceIds }),
    queryFn: async () => {
      const ids = new Set<string>()
      for (const src of sourceIds) {
        const page = unwrap(await api.GET('/entities', { params: { query: { source: src, limit: 500 } } }))
        for (const e of page.items) ids.add(e.metadata.id)
      }
      return ids
    },
    enabled: sourceIds.length > 0,
  })

  const expansionData = expansions.map((q) => q.data).filter((x): x is Expansion => !!x)
  const expansionKey = expansions.map((q) => q.dataUpdatedAt).join()
  const focusKey = s.focus ?? ''
  const model = useMemo(
    () =>
      mergeResults(
        base.data ?? null,
        expansionData,
        mode === 'path' ? [s.from ?? '', s.to ?? ''] : decodeList(focusKey),
      ),
    // expansionData is rebuilt every render; expansionKey (data timestamps) tracks its content.
    [base.data, expansionKey, mode, s.from, s.to, focusKey], // eslint-disable-line react-hooks/exhaustive-deps
  )
  return {
    mode,
    focus,
    base,
    model,
    expanding: expansions.some((q) => q.isFetching),
    expansionErrors: expansions.filter((q) => q.isError).map((q) => q.error),
    sourceIds: sourceIds.length ? (sources.data ?? new Set<string>()) : null,
  }
}
