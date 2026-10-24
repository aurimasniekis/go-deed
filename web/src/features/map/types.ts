import type { GraphEdge, GraphNode } from '@/api/client'

/** A node of the merged client-side graph (entity, server cluster, or a visual group/collapsed group). */
export interface MapNode {
  id: string
  kind: string
  name: string
  displayName?: string
  projectId: string
  health: string
  freshness: string
  environment?: string
  owner?: string
  icon?: string
  labels?: Record<string, string>
  declared: boolean
  valid: boolean
  findings: number
  depth: number
  redacted?: boolean
  /** Server-side cluster (overview group or high-degree fan): expandable with /graph/expand?key=. */
  cluster?: { key: string; count: number; groupBy: string; value: string }
  /** Impact traversal: relationship types along the path and the weakest evidence. */
  via?: string[]
  impactEvidence?: string
  /** Visual group this node is drawn in (client-side clustering). */
  group?: string
  /** True for the roots of a neighborhood / the focus of impact or path queries. */
  root?: boolean
}

export interface MapEdge {
  id: string
  from: string
  to: string
  type: string
  evidence: string
  inferred: boolean
  crossProject: boolean
  lastSeenAt?: string
  /** Number of relationships bundled into this edge (aggregated or collapsed). */
  count: number
}

export interface GraphModel {
  nodes: Map<string, MapNode>
  edges: Map<string, MapEdge>
  notes: string[]
  truncated: boolean
  elapsedMs: number
}

export function emptyModel(): GraphModel {
  return { nodes: new Map(), edges: new Map(), notes: [], truncated: false, elapsedMs: 0 }
}

export function toMapNode(n: GraphNode & { via?: string[]; evidence?: string }, rootIds: Set<string>): MapNode {
  return {
    id: n.id,
    kind: n.kind,
    name: n.name,
    ...(n.display_name ? { displayName: n.display_name } : {}),
    projectId: n.project_id,
    health: n.health || 'unknown',
    freshness: n.freshness || 'unknown',
    ...(n.environment ? { environment: n.environment } : {}),
    ...(n.owner ? { owner: n.owner } : {}),
    ...(n.icon ? { icon: n.icon } : {}),
    ...(n.labels ? { labels: n.labels } : {}),
    declared: n.declared,
    valid: n.valid,
    findings: n.findings,
    depth: n.depth,
    ...(n.redacted ? { redacted: true } : {}),
    ...(n.cluster
      ? { cluster: { key: n.cluster.key, count: n.cluster.count, groupBy: n.cluster.group_by, value: n.cluster.value } }
      : {}),
    ...(n.via ? { via: n.via } : {}),
    ...(n.evidence ? { impactEvidence: n.evidence } : {}),
    ...(rootIds.has(n.id) ? { root: true } : {}),
  }
}

export function toMapEdge(e: GraphEdge): MapEdge {
  return {
    id: e.id,
    from: e.from,
    to: e.to,
    type: e.type,
    evidence: e.evidence_class || 'unknown',
    inferred: e.inferred,
    crossProject: e.cross_project,
    ...(e.last_seen_at ? { lastSeenAt: e.last_seen_at } : {}),
    count: e.count ?? 1,
  }
}
