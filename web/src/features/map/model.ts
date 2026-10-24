import type { GraphResult, ImpactResult } from '@/api/client'
import { isStaleRelation } from '@/lib/status'
import { emptyModel, toMapEdge, toMapNode, type GraphModel, type MapEdge, type MapNode } from './types'

/** Render budgets (ADR-011): never more than this on screen at once. */
export const MAX_NODES = 2000
export const MAX_EDGES = 4000

export interface Expansion {
  /** Cluster key expanded with /graph/expand, or null for an entity neighbourhood expansion. */
  key: string | null
  result: GraphResult
}

/**
 * Merges the base query result with on-demand expansions. A cluster node whose key was expanded is replaced by its
 * members; edges whose endpoints are not loaded are dropped.
 */
export function mergeResults(
  base: GraphResult | ImpactResult | null,
  expansions: Expansion[],
  rootIds: string[] = [],
): GraphModel {
  const model = emptyModel()
  if (!base) return model
  const roots = new Set(rootIds)
  const expandedKeys = new Set(expansions.map((x) => x.key).filter((k): k is string => k !== null))
  const add = (r: GraphResult | ImpactResult) => {
    if ('root' in r) model.nodes.set(r.root.id, toMapNode(r.root, new Set([...roots, r.root.id])))
    for (const n of r.nodes) {
      if (n.cluster && expandedKeys.has(n.cluster.key)) continue
      const existing = model.nodes.get(n.id)
      const mapped = toMapNode(n, roots)
      // Keep the smallest depth (closest to the focus) when a node arrives twice.
      model.nodes.set(
        n.id,
        existing
          ? { ...mapped, depth: Math.min(existing.depth, mapped.depth), root: existing.root ?? mapped.root }
          : mapped,
      )
    }
    for (const e of r.edges) model.edges.set(e.id, toMapEdge(e))
    model.notes.push(...r.notes)
    model.truncated ||= r.truncated
    model.elapsedMs += r.elapsed_ms
  }
  add(base)
  for (const x of expansions) add(x.result)
  for (const [id, e] of model.edges) {
    if (!model.nodes.has(e.from) || !model.nodes.has(e.to)) model.edges.delete(id)
  }
  model.notes = [...new Set(model.notes)]
  return model
}

export interface MapFilters {
  kind: string[]
  type: string[]
  project: string[]
  environment: string[]
  owner: string[]
  health: string[]
  freshness: string[]
  evidence: string[]
  /** Entity IDs a source contributes to (source filter), or null when not filtering by source. */
  sourceIds: Set<string> | null
  /** Show observed relationships not seen for 7 days (dotted). */
  stale: boolean
}

export const NO_FILTERS: MapFilters = {
  kind: [],
  type: [],
  project: [],
  environment: [],
  owner: [],
  health: [],
  freshness: [],
  evidence: [],
  sourceIds: null,
  stale: true,
}

function nodeMatches(n: MapNode, f: MapFilters): boolean {
  if (n.root || n.cluster) return true
  if (f.kind.length && !f.kind.includes(n.kind)) return false
  if (f.project.length && !f.project.includes(n.projectId)) return false
  if (f.environment.length && !f.environment.includes(n.environment ?? '')) return false
  if (f.owner.length && !f.owner.includes(n.owner ?? '')) return false
  if (f.health.length && !f.health.includes(n.health)) return false
  if (f.freshness.length && !f.freshness.includes(n.freshness)) return false
  if (f.sourceIds && !f.sourceIds.has(n.id)) return false
  return true
}

export function edgeIsStale(e: MapEdge, now = Date.now()): boolean {
  return isStaleRelation(e.evidence, e.lastSeenAt, now)
}

/** Client-side show/hide filters. Roots and clusters always stay; edges need both endpoints. */
export function applyFilters(model: GraphModel, f: MapFilters, now = Date.now()): GraphModel {
  const nodes = new Map([...model.nodes].filter(([, n]) => nodeMatches(n, f)))
  const edges = new Map(
    [...model.edges].filter(([, e]) => {
      if (!nodes.has(e.from) || !nodes.has(e.to)) return false
      if (f.type.length && !f.type.includes(e.type)) return false
      if (f.evidence.length && !f.evidence.includes(e.evidence)) return false
      if (!f.stale && edgeIsStale(e, now)) return false
      return true
    }),
  )
  return { ...model, nodes, edges }
}

export type Clustering = 'none' | 'project' | 'environment' | 'kind' | 'host'

export interface VisualGroup {
  key: string
  label: string
  members: string[]
  collapsed: boolean
}

const HOST_TYPES = new Set(['runs_on', 'hosted_on'])

/** The group key of every node for a clustering mode (host = what it runs on, transitively one level). */
export function groupKeys(
  model: GraphModel,
  mode: Clustering,
  projectName: (id: string) => string = (id) => id,
): Map<string, { key: string; label: string }> {
  const out = new Map<string, { key: string; label: string }>()
  if (mode === 'none') return out
  const hostOf = new Map<string, string>()
  const hosts = new Set<string>()
  if (mode === 'host') {
    for (const e of model.edges.values()) {
      if (HOST_TYPES.has(e.type) && !hostOf.has(e.from) && model.nodes.has(e.to)) {
        hostOf.set(e.from, e.to)
        hosts.add(e.to)
      }
    }
  }
  for (const n of model.nodes.values()) {
    if (n.cluster) continue
    let key = ''
    let label = ''
    switch (mode) {
      case 'project':
        key = n.projectId
        label = projectName(n.projectId)
        break
      case 'environment':
        key = n.environment ?? ''
        label = n.environment || 'No environment'
        break
      case 'kind':
        key = n.kind
        label = n.kind
        break
      case 'host': {
        // A host groups the workloads that run on it, and itself when it runs on nothing loaded. Groups are flat: a VM
        // with containers anchors their group while it sits in its hypervisor's group.
        const anchor = hostOf.get(n.id) ?? (hosts.has(n.id) ? n.id : '')
        if (!anchor) continue
        const h = model.nodes.get(anchor)
        key = anchor
        label = h ? `on ${h.displayName ?? h.name}` : anchor
        break
      }
    }
    out.set(n.id, { key: `${mode}:${key}`, label })
  }
  return out
}

/**
 * Visual clustering with collapsible groups: every group of two or more nodes is drawn as a container; a collapsed group
 * becomes one summary node and its edges are re-aggregated with counts.
 */
export function clusterModel(
  model: GraphModel,
  mode: Clustering,
  collapsed: Set<string>,
  projectName?: (id: string) => string,
): { model: GraphModel; groups: VisualGroup[] } {
  const keys = groupKeys(model, mode, projectName)
  const groups = new Map<string, VisualGroup>()
  for (const [id, g] of keys) {
    const vg = groups.get(g.key) ?? { key: g.key, label: g.label, members: [], collapsed: collapsed.has(g.key) }
    vg.members.push(id)
    groups.set(g.key, vg)
  }
  // Single-member groups are not worth a container.
  for (const [k, g] of groups) if (g.members.length < 2) groups.delete(k)
  const nodes = new Map<string, MapNode>()
  const replace = new Map<string, string>()
  for (const n of model.nodes.values()) {
    const g = keys.get(n.id)
    const vg = g ? groups.get(g.key) : undefined
    if (vg?.collapsed) {
      const sid = `grp:${vg.key}`
      replace.set(n.id, sid)
      const cur = nodes.get(sid)
      const worst = worstHealth(cur?.health, n.health)
      nodes.set(sid, {
        id: sid,
        kind: 'group',
        name: vg.label,
        projectId: n.projectId,
        health: worst,
        freshness: cur ? cur.freshness : n.freshness,
        declared: false,
        valid: true,
        findings: (cur?.findings ?? 0) + n.findings,
        depth: Math.min(cur?.depth ?? n.depth, n.depth),
        cluster: { key: vg.key, count: vg.members.length, groupBy: mode, value: vg.label },
      })
    } else {
      nodes.set(n.id, vg ? { ...n, group: vg.key } : n)
    }
  }
  const edges = new Map<string, MapEdge>()
  for (const e of model.edges.values()) {
    const from = replace.get(e.from) ?? e.from
    const to = replace.get(e.to) ?? e.to
    if (from === to) continue
    if (from === e.from && to === e.to) {
      edges.set(e.id, e)
      continue
    }
    const id = `agg:${from}>${to}:${e.type}`
    const cur = edges.get(id)
    edges.set(
      id,
      cur
        ? { ...cur, count: cur.count + e.count, evidence: weakerEvidence(cur.evidence, e.evidence) }
        : { ...e, id, from, to },
    )
  }
  return { model: { ...model, nodes, edges }, groups: [...groups.values()] }
}

const healthRank: Record<string, number> = { failed: 3, warning: 2, ok: 1, unknown: 0 }
function worstHealth(a: string | undefined, b: string): string {
  if (!a) return b
  return (healthRank[a] ?? 0) >= (healthRank[b] ?? 0) ? a : b
}

const evidenceRank: Record<string, number> = { declared: 4, structural: 3, observed: 2, inferred: 1 }
export function weakerEvidence(a: string, b: string): string {
  return (evidenceRank[a] ?? 0) <= (evidenceRank[b] ?? 0) ? a : b
}

/** Keeps at most MAX_NODES nodes (closest to the focus first) and MAX_EDGES edges. */
export function enforceBudget(
  model: GraphModel,
  maxNodes = MAX_NODES,
  maxEdges = MAX_EDGES,
): GraphModel & { budgetHit: boolean } {
  let budgetHit = false
  let nodes = model.nodes
  if (nodes.size > maxNodes) {
    budgetHit = true
    const keep = [...nodes.values()]
      .sort((a, b) => Number(!!b.root) - Number(!!a.root) || a.depth - b.depth)
      .slice(0, maxNodes)
    nodes = new Map(keep.map((n) => [n.id, n]))
  }
  let edges = new Map([...model.edges].filter(([, e]) => nodes.has(e.from) && nodes.has(e.to)))
  if (edges.size > maxEdges) {
    budgetHit = true
    edges = new Map([...edges].slice(0, maxEdges))
  }
  return { ...model, nodes, edges, budgetHit }
}

/**
 * Identity of the graph's shape: node IDs (with their visual group), edge endpoints and the layout options. Layout runs
 * only when this changes — observation-only updates (health, freshness) patch node data in place.
 */
export function topologySignature(model: GraphModel, extra = ''): string {
  const n = [...model.nodes.values()].map((x) => `${x.id}@${x.group ?? ''}`).sort()
  const e = [...model.edges.values()].map((x) => `${x.from}>${x.to}`).sort()
  let h = 2166136261
  const s = `${extra}|${n.join(',')}|${e.join(',')}`
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i)
    h = Math.imul(h, 16777619)
  }
  return `${model.nodes.size}:${model.edges.size}:${(h >>> 0).toString(36)}`
}

/** Neighbours of a node in the visible graph (both directions). */
export function neighbours(model: GraphModel, id: string): string[] {
  const out = new Set<string>()
  for (const e of model.edges.values()) {
    if (e.from === id) out.add(e.to)
    if (e.to === id) out.add(e.from)
  }
  return [...out]
}

export type Dir = 'left' | 'right' | 'up' | 'down'

/**
 * Keyboard navigation: the neighbour that best matches an arrow direction (smallest angle off the axis, then the
 * nearest). Falls back to any visible node in that direction when no neighbour lies that way.
 */
export function nextInDirection(
  positions: Map<string, { x: number; y: number }>,
  from: string,
  dir: Dir,
  candidates: string[],
  all: string[] = [],
): string | null {
  const p = positions.get(from)
  if (!p) return null
  const v = { left: [-1, 0], right: [1, 0], up: [0, -1], down: [0, 1] }[dir] as [number, number]
  const pick = (ids: string[]) => {
    let best: string | null = null
    let bestScore = Infinity
    for (const id of ids) {
      if (id === from) continue
      const q = positions.get(id)
      if (!q) continue
      const dx = q.x - p.x
      const dy = q.y - p.y
      const dist = Math.hypot(dx, dy)
      if (dist === 0) continue
      const cos = (dx * v[0] + dy * v[1]) / dist
      if (cos < 0.35) continue
      const score = dist * (2 - cos)
      if (score < bestScore) {
        bestScore = score
        best = id
      }
    }
    return best
  }
  return pick(candidates) ?? pick(all)
}
