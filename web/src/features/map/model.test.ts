import { describe, expect, it } from 'vitest'
import type { GraphResult } from '@/api/client'
import { radialLayout } from './layoutTypes'
import {
  applyFilters,
  clusterModel,
  enforceBudget,
  mergeResults,
  neighbours,
  nextInDirection,
  NO_FILTERS,
  topologySignature,
} from './model'
import { bundleSources, toMapEdge } from './types'

describe('edge sources', () => {
  const src = (id: string, layer: string, relations?: number) => ({
    id,
    name: id,
    layer,
    project_id: 'p',
    ...(relations ? { relations } : {}),
  })
  const edge = (id: string, sources: ReturnType<typeof src>[], primary?: string, count?: number) =>
    toMapEdge({
      id,
      from: 'a',
      to: 'b',
      type: 'depends_on',
      evidence_class: 'declared',
      inferred: false,
      cross_project: false,
      sources,
      ...(primary ? { primary_source: primary } : {}),
      ...(count ? { count } : {}),
    })

  it('maps the API sources, counting a single relationship once', () => {
    const e = edge('rel_1', [src('api', 'declared')], 'api')
    expect(e.sources).toEqual([{ id: 'api', name: 'api', layer: 'declared', projectId: 'p', relations: 1 }])
    expect(e.primarySource).toBe('api')
  })

  it('bundles the union of sources and keeps a primary only when every part shares it', () => {
    const a = edge('rel_1', [src('api', 'declared'), src('scan', 'discovered')], 'api')
    const b = edge('bundle:x', [src('scan', 'discovered', 3), src('api', 'declared', 2)], 'api', 3)
    const both = bundleSources(a, b)
    expect(both.primarySource).toBe('api')
    expect(both.sources.map((s) => [s.id, s.relations])).toEqual([
      ['api', 3],
      ['scan', 4],
    ])
    const c = edge('rel_2', [src('scan', 'discovered')], 'scan')
    expect(bundleSources(a, c).primarySource).toBeUndefined()
    // A part without visible sources leaves the bundle without a single primary.
    expect(bundleSources(a, edge('rel_3', [])).primarySource).toBeUndefined()
  })

  it('carries sources through client-side group aggregation', () => {
    const r: GraphResult = {
      nodes: [n('a', { depth: 0 }), n('b', { kind: 'database' }), n('c', { kind: 'database' })],
      edges: [
        { ...e('r1', 'a', 'b'), sources: [src('api', 'declared')], primary_source: 'api' },
        { ...e('r2', 'a', 'c'), sources: [src('scan', 'discovered')], primary_source: 'scan' },
      ],
      notes: [],
      truncated: false,
      elapsed_ms: 1,
    }
    const model = mergeResults(r, [], ['a'])
    const grouped = clusterModel(model, 'kind', new Set(['kind:database']), (id) => id)
    const agg = [...grouped.model.edges.values()].find((x) => x.id.startsWith('agg:'))
    expect(agg?.sources.map((s) => s.id)).toEqual(['api', 'scan'])
    expect(agg?.primarySource).toBeUndefined()
  })
})

const n = (id: string, over: Record<string, unknown> = {}) => ({
  id,
  kind: 'service',
  name: id,
  project_id: 'p1',
  health: 'ok',
  freshness: 'fresh',
  declared: true,
  valid: true,
  findings: 0,
  depth: 1,
  ...over,
})
const e = (id: string, from: string, to: string, type = 'depends_on', over: Record<string, unknown> = {}) => ({
  id,
  from,
  to,
  type,
  evidence_class: 'declared',
  inferred: false,
  cross_project: false,
  sources: [] as GraphResult['edges'][number]['sources'],
  ...over,
})
const result = (nodes: ReturnType<typeof n>[], edges: ReturnType<typeof e>[]): GraphResult => ({
  nodes: nodes,
  edges: edges,
  notes: [],
  truncated: false,
  elapsed_ms: 2,
})

describe('graph model', () => {
  const base = result(
    [
      n('a', { depth: 0 }),
      n('b'),
      n('c', { kind: 'database', environment: 'prod' }),
      n('k', { kind: 'cluster', cluster: { key: 'fan|a|runs_on', count: 40, group_by: 'type', value: 'runs_on' } }),
    ],
    [e('r1', 'a', 'b'), e('r2', 'a', 'c'), e('r3', 'a', 'k', 'runs_on')],
  )

  it('merges expansions, replacing expanded clusters and dropping dangling edges', () => {
    const m = mergeResults(
      base,
      [
        {
          key: 'fan|a|runs_on',
          result: result([n('x'), n('y')], [e('r4', 'x', 'a', 'runs_on'), e('r5', 'y', 'a', 'runs_on')]),
        },
      ],
      ['a'],
    )
    expect([...m.nodes.keys()].sort()).toEqual(['a', 'b', 'c', 'x', 'y'])
    expect(m.edges.has('r3')).toBe(false)
    expect(m.edges.has('r4')).toBe(true)
    expect(m.nodes.get('a')?.root).toBe(true)
    expect(m.elapsedMs).toBe(4)
  })

  it('keeps the smallest depth when a node arrives twice', () => {
    const m = mergeResults(result([n('a', { depth: 3 })], []), [
      { key: null, result: result([n('a', { depth: 1 })], []) },
    ])
    expect(m.nodes.get('a')?.depth).toBe(1)
  })

  it('filters nodes but always keeps roots and clusters; edges need both ends', () => {
    const m = mergeResults(base, [], ['a'])
    const f = applyFilters(m, { ...NO_FILTERS, kind: ['database'] })
    expect([...f.nodes.keys()].sort()).toEqual(['a', 'c', 'k'])
    expect([...f.edges.keys()].sort()).toEqual(['r2', 'r3'])
    const t = applyFilters(m, { ...NO_FILTERS, type: ['runs_on'] })
    expect([...t.edges.keys()]).toEqual(['r3'])
  })

  it('hides stale observed edges when asked', () => {
    const m = mergeResults(
      result(
        [n('a'), n('b')],
        [e('r1', 'a', 'b', 'connects_to', { evidence_class: 'observed', last_seen_at: '2020-01-01T00:00:00Z' })],
      ),
      [],
    )
    expect(applyFilters(m, NO_FILTERS).edges.size).toBe(1)
    expect(applyFilters(m, { ...NO_FILTERS, stale: false }).edges.size).toBe(0)
  })

  it('clusters by kind and collapses groups into one node with re-aggregated edges', () => {
    const m = mergeResults(
      result(
        [n('a'), n('b'), n('c', { kind: 'database' }), n('d', { kind: 'database' })],
        [e('r1', 'a', 'c'), e('r2', 'b', 'd'), e('r3', 'a', 'b')],
      ),
      [],
    )
    const open = clusterModel(m, 'kind', new Set())
    expect(open.groups.map((g) => [g.key, g.members.length])).toEqual([
      ['kind:service', 2],
      ['kind:database', 2],
    ])
    expect(open.model.nodes.get('a')?.group).toBe('kind:service')
    const closed = clusterModel(m, 'kind', new Set(['kind:database']))
    expect(closed.model.nodes.has('grp:kind:database')).toBe(true)
    expect(closed.model.nodes.get('grp:kind:database')?.cluster?.count).toBe(2)
    // Two different sources → two aggregated edges into the collapsed group.
    const into = [...closed.model.edges.values()].filter((x) => x.to === 'grp:kind:database')
    expect(into.map((x) => [x.from, x.count]).sort()).toEqual([
      ['a', 1],
      ['b', 1],
    ])
    const both = clusterModel(m, 'kind', new Set(['kind:database', 'kind:service']))
    const bundle = [...both.model.edges.values()].find(
      (x) => x.from === 'grp:kind:service' && x.to === 'grp:kind:database',
    )
    expect(bundle?.count).toBe(2)
  })

  it('groups workloads by the host they run on', () => {
    const m = mergeResults(
      result(
        [n('h', { kind: 'docker_host' }), n('c1', { kind: 'container' }), n('c2', { kind: 'container' }), n('x')],
        [e('r1', 'c1', 'h', 'runs_on'), e('r2', 'c2', 'h', 'runs_on')],
      ),
      [],
    )
    const { groups, model } = clusterModel(m, 'host', new Set())
    expect(groups).toHaveLength(1)
    expect(groups[0]?.members.sort()).toEqual(['c1', 'c2', 'h'])
    expect(model.nodes.get('x')?.group).toBeUndefined()
  })

  it('enforces the render budget closest-to-focus first', () => {
    const nodes = Array.from({ length: 10 }, (_, i) => n(`n${i}`, { depth: 10 - i }))
    const m = mergeResults(result(nodes, [e('r', 'n0', 'n9')]), [], ['n0'])
    const b = enforceBudget(m, 3, 10)
    expect(b.budgetHit).toBe(true)
    expect([...b.nodes.keys()]).toContain('n0')
    expect([...b.nodes.keys()]).toContain('n9')
    expect(b.nodes.size).toBe(3)
  })

  it('changes the topology signature only when the shape changes', () => {
    const m = mergeResults(base, [], ['a'])
    const sig = topologySignature(m, 'layered')
    const patched = { ...m, nodes: new Map([...m.nodes].map(([k, v]) => [k, { ...v, health: 'failed' }])) }
    expect(topologySignature(patched, 'layered')).toBe(sig)
    expect(topologySignature(m, 'force')).not.toBe(sig)
    const fewer = { ...m, edges: new Map([...m.edges].slice(1)) }
    expect(topologySignature(fewer, 'layered')).not.toBe(sig)
  })

  it('moves keyboard selection to the neighbour in the arrow direction', () => {
    const m = mergeResults(base, [], ['a'])
    const pos = new Map([
      ['a', { x: 0, y: 0 }],
      ['b', { x: 200, y: -10 }],
      ['c', { x: 200, y: 150 }],
      ['k', { x: -200, y: 0 }],
    ])
    expect(neighbours(m, 'a').sort()).toEqual(['b', 'c', 'k'])
    expect(nextInDirection(pos, 'a', 'right', neighbours(m, 'a'))).toBe('b')
    expect(nextInDirection(pos, 'a', 'down', neighbours(m, 'a'))).toBe('c')
    expect(nextInDirection(pos, 'a', 'left', neighbours(m, 'a'))).toBe('k')
    expect(nextInDirection(pos, 'a', 'up', [], [])).toBeNull()
  })
})

describe('radial layout', () => {
  it('puts the focus in the centre and neighbours on rings by distance', () => {
    const size = { width: 100, height: 40 }
    const pos = radialLayout({
      nodes: ['a', 'b', 'c', 'd'].map((id) => ({ id, ...size })),
      edges: [
        { id: '1', source: 'a', target: 'b' },
        { id: '2', source: 'b', target: 'c' },
      ],
      focus: ['a'],
    })
    const r = (id: string) => Math.hypot((pos[id]?.x ?? 0) + 50, (pos[id]?.y ?? 0) + 20)
    expect(r('a')).toBeCloseTo(0)
    expect(r('b')).toBeLessThan(r('c'))
    expect(r('d')).toBeGreaterThan(0)
  })
})
