/**
 * URL state codecs (DISCOVERY Q-48: every view encodes its state in the URL). Lists travel as comma-separated strings
 * (`?kind=server,vm`), booleans as true/false, numbers as numbers, so links stay short and readable. TanStack Router
 * JSON-parses values first, so decoders accept already-parsed values too.
 */
import type { operations } from '@/api/schema.gen'

export function decodeList(v: unknown): string[] {
  if (Array.isArray(v)) return v.filter((x): x is string => typeof x === 'string' && x !== '')
  if (typeof v === 'number') return [String(v)]
  if (typeof v !== 'string' || v === '') return []
  return v
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean)
}

/** Encodes a list for the URL; empty lists disappear from the URL. */
export function encodeList(list: readonly string[] | undefined): string | undefined {
  const clean = (list ?? []).map((s) => s.trim()).filter(Boolean)
  return clean.length ? clean.join(',') : undefined
}

export function decodeString(v: unknown, max = 512): string | undefined {
  if (typeof v === 'number') return String(v)
  if (typeof v !== 'string' || v === '') return undefined
  return v.slice(0, max)
}

export function decodeBool(v: unknown): boolean | undefined {
  if (v === true || v === 'true') return true
  if (v === false || v === 'false') return false
  return undefined
}

export function decodeInt(v: unknown, min: number, max: number): number | undefined {
  const n = typeof v === 'number' ? v : typeof v === 'string' ? Number(v) : NaN
  if (!Number.isFinite(n)) return undefined
  return Math.min(max, Math.max(min, Math.trunc(n)))
}

export function decodeEnum<T extends string>(v: unknown, allowed: readonly T[]): T | undefined {
  return typeof v === 'string' && (allowed as readonly string[]).includes(v) ? (v as T) : undefined
}

/** Drops undefined/empty values so URLs only carry what differs from the defaults. */
export function compact<T extends object>(o: T): Partial<T> {
  const out: Record<string, unknown> = {}
  for (const [k, v] of Object.entries(o)) {
    if (v === undefined || v === '' || (Array.isArray(v) && v.length === 0)) continue
    out[k] = v
  }
  return out as Partial<T>
}

// ---------------------------------------------------------------------------------------------------------------------
// Inventory

export const INVENTORY_SORTS = [
  'name',
  'kind',
  'updated_at',
  'created_at',
  'health',
  'freshness',
  'environment',
  'display_name',
  'last_seen_at',
] as const

export interface InventorySearch {
  q?: string
  kind?: string
  project?: string
  environment?: string
  owner?: string
  health?: string
  freshness?: string
  lifecycle?: string
  label?: string
  source?: string
  declared?: boolean
  valid?: boolean
  sort?: string
  cursor?: string
  cols?: string
  view?: string
  limit?: number
}

export function validateInventorySearch(s: Record<string, unknown>): InventorySearch {
  const sort = decodeString(s.sort, 32)
  const sortOk = sort && (INVENTORY_SORTS as readonly string[]).includes(sort.replace(/^-/, ''))
  return compact({
    q: decodeString(s.q, 256),
    kind: encodeList(decodeList(s.kind)),
    project: encodeList(decodeList(s.project)),
    environment: encodeList(decodeList(s.environment)),
    owner: decodeString(s.owner, 64),
    health: encodeList(decodeList(s.health)),
    freshness: encodeList(decodeList(s.freshness)),
    lifecycle: encodeList(decodeList(s.lifecycle)),
    label: decodeString(s.label, 512),
    source: decodeString(s.source, 64),
    declared: decodeBool(s.declared),
    valid: decodeBool(s.valid),
    sort: sortOk ? sort : undefined,
    cursor: decodeString(s.cursor, 512),
    cols: encodeList(decodeList(s.cols)),
    view: decodeString(s.view, 64),
    limit: decodeInt(s.limit, 10, 500),
  })
}

/** Labels travel as `key=value;key2=value2` (values may contain commas). */
export function decodeLabels(v: string | undefined): string[] {
  return (v ?? '')
    .split(';')
    .map((s) => s.trim())
    .filter((s) => /^[^=]+=.*$/.test(s))
}

export function encodeLabels(list: string[]): string | undefined {
  const clean = list.map((s) => s.trim()).filter((s) => /^[^=]+=.*$/.test(s))
  return clean.length ? clean.join(';') : undefined
}

export type EntityQuery = NonNullable<operations['list-entities']['parameters']['query']>
export type Evidence = 'declared' | 'structural' | 'observed' | 'inferred'

/** GET /entities query parameters for an inventory URL state plus the global project filter. */
export function inventoryQuery(s: InventorySearch, globalProjects: string[] = []): EntityQuery {
  const projects = decodeList(s.project)
  return compact({
    q: s.q,
    kind: decodeList(s.kind),
    project: projects.length ? projects : globalProjects,
    environment: decodeList(s.environment),
    owner: s.owner,
    health: decodeList(s.health) as EntityQuery['health'],
    freshness: decodeList(s.freshness) as EntityQuery['freshness'],
    lifecycle: decodeList(s.lifecycle),
    label: decodeLabels(s.label),
    source: s.source,
    declared: s.declared === undefined ? undefined : s.declared ? ('true' as const) : ('false' as const),
    valid: s.valid === undefined ? undefined : s.valid ? ('true' as const) : ('false' as const),
    sort: s.sort,
    cursor: s.cursor,
    limit: s.limit ?? 100,
  })
}

// ---------------------------------------------------------------------------------------------------------------------
// Map

export const MAP_GROUPS = ['project', 'kind', 'environment', 'owner'] as const
/** A kind name (`location`, `example.com/rack_row`) as it may appear in a group or cluster parameter. */
const KIND_NAME = /^[a-z0-9][a-z0-9._/-]{0,127}$/
/** A label key (`prefix/name`), as Deed validates it. */
export const LABEL_KEY = /^([a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?\/)?[a-zA-Z0-9]([a-zA-Z0-9._-]{0,61}[a-zA-Z0-9])?$/
export const MAP_CLUSTERING = ['none', 'project', 'environment', 'kind', 'host'] as const
export const MAP_LAYOUTS = ['layered', 'force', 'radial'] as const
export const MAP_DIRECTIONS = ['RIGHT', 'DOWN', 'LEFT', 'UP'] as const
export const MAP_MODES = ['overview', 'neighborhood', 'path', 'impact'] as const

export interface MapSearch {
  /** Root entities of the neighborhood (comma-separated IDs). */
  focus?: string
  mode?: (typeof MAP_MODES)[number]
  depth?: number
  dir?: 'out' | 'in' | 'both'
  /** Overview grouping. */
  /** Overview grouping: a MAP_GROUPS value or the name of a kind with a map grouping (location, region, label, …). */
  group?: string
  /** Visual clustering of the loaded nodes. */
  /** Client-side clustering: a MAP_CLUSTERING value or `by:<kind>` for a kind with a map grouping. */
  cluster?: string
  layout?: (typeof MAP_LAYOUTS)[number]
  rankdir?: (typeof MAP_DIRECTIONS)[number]
  /** Path finder endpoints. */
  from?: string
  to?: string
  /** Impact direction. */
  impact?: 'downstream' | 'upstream'
  // Filters
  kind?: string
  type?: string
  project?: string
  source?: string
  environment?: string
  owner?: string
  health?: string
  freshness?: string
  evidence?: string
  stale?: boolean
  /** Kinds and relationship types switched off (legend, visibility panel). */
  hideKind?: string
  hideType?: string
  /** Label values switched off, as `key:value` (show / hide panel). */
  hideLabel?: string
  /** Expanded cluster keys. */
  expand?: string
  /** Breadcrumb trail of focus IDs. */
  trail?: string
  sel?: string
  view?: string
}

export function validateMapSearch(s: Record<string, unknown>): MapSearch {
  const dir = decodeEnum(s.dir, ['out', 'in', 'both'] as const)
  return compact({
    focus: encodeList(decodeList(s.focus).slice(0, 20)),
    mode: decodeEnum(s.mode, MAP_MODES),
    depth: decodeInt(s.depth, 1, 6),
    dir,
    group:
      decodeEnum(s.group, MAP_GROUPS) ??
      (typeof s.group === 'string' &&
      (KIND_NAME.test(s.group) || (s.group.startsWith('label:') && LABEL_KEY.test(s.group.slice(6))))
        ? s.group
        : undefined),
    cluster:
      decodeEnum(s.cluster, MAP_CLUSTERING) ??
      (typeof s.cluster === 'string' && s.cluster.startsWith('by:') && KIND_NAME.test(s.cluster.slice(3))
        ? s.cluster
        : undefined),
    layout: decodeEnum(s.layout, MAP_LAYOUTS),
    rankdir: decodeEnum(s.rankdir, MAP_DIRECTIONS),
    from: decodeString(s.from, 64),
    to: decodeString(s.to, 64),
    impact: decodeEnum(s.impact, ['downstream', 'upstream'] as const),
    kind: encodeList(decodeList(s.kind)),
    type: encodeList(decodeList(s.type)),
    project: encodeList(decodeList(s.project)),
    source: encodeList(decodeList(s.source)),
    environment: encodeList(decodeList(s.environment)),
    owner: encodeList(decodeList(s.owner)),
    health: encodeList(decodeList(s.health)),
    freshness: encodeList(decodeList(s.freshness)),
    evidence: encodeList(decodeList(s.evidence)),
    stale: decodeBool(s.stale),
    hideKind: encodeList(decodeList(s.hideKind).slice(0, 200)),
    hideType: encodeList(decodeList(s.hideType).slice(0, 200)),
    hideLabel: encodeList(decodeList(s.hideLabel).slice(0, 200)),
    expand: encodeList(decodeList(s.expand).slice(0, 50)),
    trail: encodeList(decodeList(s.trail).slice(-12)),
    sel: decodeString(s.sel, 128),
    view: decodeString(s.view, 64),
  })
}

// ---------------------------------------------------------------------------------------------------------------------
// Findings

export interface FindingsSearch {
  severity?: string
  class?: string
  type?: string
  project?: string
  entity?: string
  state?: 'open' | 'closed' | 'all'
}

export function validateFindingsSearch(s: Record<string, unknown>): FindingsSearch {
  return compact({
    severity: encodeList(decodeList(s.severity)),
    class: encodeList(decodeList(s.class)),
    type: encodeList(decodeList(s.type)),
    project: decodeString(s.project, 64),
    entity: decodeString(s.entity, 64),
    state: decodeEnum(s.state, ['open', 'closed', 'all'] as const),
  })
}

// ---------------------------------------------------------------------------------------------------------------------
// Versions

export interface VersionsSearch {
  status?: string
  q?: string
}

export function validateVersionsSearch(s: Record<string, unknown>): VersionsSearch {
  return compact({ status: encodeList(decodeList(s.status)), q: decodeString(s.q, 200) })
}
