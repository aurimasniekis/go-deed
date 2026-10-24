/**
 * Provenance and declared-vs-observed logic (ADR-003). Facets hold each source's fields (`{metadata, spec}` or, for
 * observations, `{observed, health}`); the effective entity is a field-level merge. These pure functions flatten facets
 * to JSON pointers and line the values up per field so the UI can show who said what.
 */

/** RFC 6901 escaping of one reference token. */
export function escapeToken(t: string): string {
  return t.replace(/~/g, '~0').replace(/\//g, '~1')
}

export function unescapeToken(t: string): string {
  return t.replace(/~1/g, '/').replace(/~0/g, '~')
}

export function joinPointer(base: string, token: string): string {
  return `${base}/${escapeToken(token)}`
}

/** Reads a JSON pointer from a value (undefined when absent). */
export function getPointer(root: unknown, pointer: string): unknown {
  if (pointer === '' || pointer === '/') return root
  let cur: unknown = root
  for (const raw of pointer.split('/').slice(1)) {
    const t = unescapeToken(raw)
    if (Array.isArray(cur)) {
      const i = Number(t)
      if (!Number.isInteger(i)) return undefined
      cur = cur[i]
    } else if (cur && typeof cur === 'object') {
      cur = (cur as Record<string, unknown>)[t]
    } else {
      return undefined
    }
    if (cur === undefined) return undefined
  }
  return cur
}

/**
 * Flattens an object to pointer → leaf value, as the merge does: objects recurse; arrays and scalars are leaves. Links
 * are a keyed collection (merged per entry), so `links` stays one leaf here for readability.
 */
export function flatten(value: unknown, base = '', out = new Map<string, unknown>()): Map<string, unknown> {
  if (value && typeof value === 'object' && !Array.isArray(value)) {
    const entries = Object.entries(value as Record<string, unknown>)
    if (entries.length === 0 && base) out.set(base, {})
    for (const [k, v] of entries) flatten(v, joinPointer(base, k), out)
    return out
  }
  if (base) out.set(base, value)
  return out
}

export interface FacetLike {
  id: string
  source: string
  source_id: string
  layer: string
  provider?: string
  fields: Record<string, unknown>
  observed_at: string
  live: boolean
  revision: number
}

/** Pointer space of a facet, aligned with the entity: observation facets write under /status. */
export function facetPointers(f: FacetLike): Map<string, unknown> {
  const out = new Map<string, unknown>()
  const fields = f.fields
  if (fields.metadata !== undefined) flatten(fields.metadata, '/metadata', out)
  if (fields.spec !== undefined) flatten(fields.spec, '/spec', out)
  if (fields.observed !== undefined) flatten(fields.observed, '/status/observed', out)
  if (fields.health !== undefined) out.set('/status/health', fields.health)
  return out
}

export const DECLARED_LAYERS = new Set(['declared', 'override', 'annotation'])
export const OBSERVED_LAYERS = new Set(['imported', 'discovered', 'observation'])

export interface SourceValue {
  source: string
  sourceId: string
  layer: string
  value: unknown
}

export type CompareState = 'match' | 'differs' | 'declared-only' | 'observed-only'

export interface CompareRow {
  pointer: string
  declared: SourceValue[]
  observed: SourceValue[]
  effective: unknown
  state: CompareState
}

export function sameValue(a: unknown, b: unknown): boolean {
  if (a === b) return true
  try {
    return JSON.stringify(canonical(a)) === JSON.stringify(canonical(b))
  } catch {
    return false
  }
}

function canonical(v: unknown): unknown {
  if (Array.isArray(v)) return v.map(canonical)
  if (v && typeof v === 'object') {
    return Object.fromEntries(
      Object.keys(v as Record<string, unknown>)
        .sort()
        .map((k) => [k, canonical((v as Record<string, unknown>)[k])]),
    )
  }
  return v
}

/**
 * Lines up declared facets (declared, override, annotation) against observed facets (imported, discovered,
 * observation) per pointer. Rows where the two sides disagree come first, then single-sided rows, then matches.
 */
export function compareFacets(facets: FacetLike[], effective: unknown): CompareRow[] {
  const rows = new Map<string, CompareRow>()
  for (const f of facets) {
    if (!f.live) continue
    const side = DECLARED_LAYERS.has(f.layer) ? 'declared' : OBSERVED_LAYERS.has(f.layer) ? 'observed' : null
    if (!side) continue
    for (const [pointer, value] of facetPointers(f)) {
      let row = rows.get(pointer)
      if (!row) {
        row = { pointer, declared: [], observed: [], effective: getPointer(effective, pointer), state: 'match' }
        rows.set(pointer, row)
      }
      row[side].push({ source: f.source, sourceId: f.source_id, layer: f.layer, value })
    }
  }
  for (const row of rows.values()) {
    if (row.declared.length === 0) row.state = 'observed-only'
    else if (row.observed.length === 0) row.state = 'declared-only'
    else
      row.state = row.observed.every((o) => row.declared.some((d) => sameValue(d.value, o.value))) ? 'match' : 'differs'
  }
  const order: Record<CompareState, number> = { differs: 0, 'observed-only': 1, 'declared-only': 2, match: 3 }
  return [...rows.values()].sort((a, b) => order[a.state] - order[b.state] || a.pointer.localeCompare(b.pointer))
}

export interface ProvenanceEntry {
  source_id: string
  source: string
  layer: string
  revision: number
  observed_at: string
  facet_id: string
  candidates?: { source_id: string; source: string; layer: string; value: unknown }[]
}

export interface ProvenanceRow extends ProvenanceEntry {
  pointer: string
  value: unknown
  /** Candidates whose value differs from the winner (they lost). */
  losing: { source: string; layer: string; value: unknown; sameValue: boolean }[]
}

/** Provenance map (pointer → winner) joined with effective values; sorted by pointer. */
export function provenanceRows(prov: Record<string, ProvenanceEntry>, effective: unknown): ProvenanceRow[] {
  return Object.entries(prov)
    .map(([pointer, p]) => {
      const value = getPointer(effective, pointer)
      return {
        ...p,
        pointer,
        value,
        losing: (p.candidates ?? []).map((c) => ({
          source: c.source,
          layer: c.layer,
          value: c.value,
          sameValue: sameValue(c.value, value),
        })),
      }
    })
    .sort((a, b) => a.pointer.localeCompare(b.pointer))
}

/** The provenance entry that explains a pointer: exact match, else the nearest ancestor or the first descendant. */
export function provenanceFor(
  prov: Record<string, ProvenanceEntry> | undefined,
  pointer: string,
): { pointer: string; entry: ProvenanceEntry } | null {
  if (!prov) return null
  const exact = prov[pointer]
  if (exact) return { pointer, entry: exact }
  const parts = pointer.split('/')
  for (let i = parts.length - 1; i > 1; i--) {
    const p = parts.slice(0, i).join('/')
    const e = prov[p]
    if (e) return { pointer: p, entry: e }
  }
  const child = Object.keys(prov)
    .filter((k) => k.startsWith(`${pointer}/`))
    .sort()[0]
  const entry = child ? prov[child] : undefined
  return child && entry ? { pointer: child, entry } : null
}

/** Human label for a pointer: "/spec/open_ports" → "spec › open_ports". */
export function pointerLabel(pointer: string): string {
  return pointer.split('/').slice(1).map(unescapeToken).join(' › ')
}
