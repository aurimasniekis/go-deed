import { describe, expect, it } from 'vitest'
import {
  arrangeComponents,
  connectedComponents,
  foldLeaves,
  GROUP_PADDING,
  LayoutCache,
  leafRings,
  packGroups,
  sectorLayout,
  wrapLayers,
  type Box,
} from './layoutTypes'

const size = { width: 100, height: 40 }

describe('wrapLayers', () => {
  it('leaves short layers alone', () => {
    const positions = { a: { x: 0, y: 0 }, b: { x: 200, y: 0 }, c: { x: 200, y: 60 } }
    const sizes = new Map(Object.keys(positions).map((id) => [id, size]))
    expect(wrapLayers(positions, sizes, 'RIGHT', 5)).toEqual(positions)
  })

  it('wraps a tall layer into columns and shifts later layers', () => {
    const positions: Record<string, { x: number; y: number }> = { root: { x: 0, y: 0 }, tail: { x: 400, y: 0 } }
    for (let i = 0; i < 10; i++) positions[`n${String(i)}`] = { x: 200, y: i * 60 }
    const sizes = new Map(Object.keys(positions).map((id) => [id, size]))
    const out = wrapLayers(positions, sizes, 'RIGHT', 4)
    const xs = new Set(Array.from({ length: 10 }, (_, i) => out[`n${String(i)}`]?.x))
    expect(xs.size).toBe(3) // 10 nodes, 4 per column
    const ys = Array.from({ length: 10 }, (_, i) => out[`n${String(i)}`]?.y ?? 0)
    expect(Math.max(...ys) - Math.min(...ys)).toBeLessThan(4 * 66)
    expect(out.root).toEqual(positions.root)
    // The following layer moves right past the extra columns.
    expect(out.tail?.x).toBeGreaterThan(Math.max(...[...xs].map((x) => x ?? 0)))
  })

  it('wraps vertical layouts into rows', () => {
    const positions: Record<string, { x: number; y: number }> = {}
    for (let i = 0; i < 6; i++) positions[`n${String(i)}`] = { x: i * 120, y: 100 }
    const sizes = new Map(Object.keys(positions).map((id) => [id, size]))
    const out = wrapLayers(positions, sizes, 'DOWN', 3)
    expect(new Set(Object.values(out).map((p) => p.y)).size).toBe(2)
  })
})

const node = (id: string, group?: string) => ({ id, width: 224, height: 66, ...(group ? { group } : {}) })
const link = (source: string, target: string) => ({ id: `${source}>${target}`, source, target })

describe('foldLeaves', () => {
  it('folds degree-1 nodes onto their neighbour', () => {
    const nodes = [node('hub'), node('h1'), node('h2'), node('c1'), node('c2'), node('c3')]
    const edges = [link('h1', 'hub'), link('h2', 'hub'), link('c1', 'h1'), link('c2', 'h1'), link('c3', 'h2')]
    const out = foldLeaves({ nodes, edges, focus: ['hub'], pinned: {} })
    expect(Object.fromEntries(out)).toEqual({ h1: ['c1', 'c2'], h2: ['c3'] })
  })

  it('keeps focus, pinned, cross-group leaves and two-node components in the simulation', () => {
    const nodes = [node('h'), node('x'), node('f'), node('p'), node('g', 'other'), node('a'), node('b')]
    const edges = [link('h', 'x'), link('f', 'h'), link('p', 'h'), link('g', 'h'), link('a', 'b')]
    const out = foldLeaves({ nodes, edges, focus: ['f'], pinned: { p: { x: 0, y: 0 } } })
    expect(Object.fromEntries(out)).toEqual({ h: ['x'] })
  })
})

describe('leafRings', () => {
  const box = { width: 224, height: 66 }
  const overlap = (a: { x: number; y: number }, b: { x: number; y: number }) =>
    Math.abs(a.x - b.x) < box.width && Math.abs(a.y - b.y) < box.height

  it.each([
    [1, 0, 0],
    [12, 0.6, 0],
    [49, 0.6, 2.1],
    [120, 0, 1],
  ])('places %i boxes without overlaps (gap %f, base %f)', (count, gap, base) => {
    const { offsets, radius } = leafRings(count, box, base, gap)
    expect(offsets).toHaveLength(count)
    const all = [{ x: 0, y: 0 }, ...offsets]
    all.forEach((a, i) => all.slice(i + 1).forEach((b, j) => expect(overlap(a, b), `${i}/${i + j + 1}`).toBe(false)))
    for (const o of offsets) expect(Math.hypot(o.x, o.y)).toBeLessThan(radius)
  })

  it('leaves the gap around the base angle free', () => {
    const { offsets } = leafRings(30, box, 0, 0.6)
    for (const o of offsets) expect(Math.abs(Math.atan2(o.y, o.x))).toBeGreaterThanOrEqual(0.3 - 1e-9)
  })
})

describe('sectorLayout', () => {
  it('puts the focus in the centre, each depth on its ring and unreached nodes outside', () => {
    const nodes = [node('a'), node('b'), node('c'), node('d'), node('lone')]
    const edges = [link('a', 'b'), link('a', 'c'), link('c', 'd')]
    const pos = sectorLayout({ nodes, edges, focus: ['a'] }, 100)
    const dist = (id: string) => Math.hypot(pos[id]?.x ?? NaN, pos[id]?.y ?? NaN)
    expect(pos.a).toEqual({ x: 0, y: 0 })
    expect(dist('b')).toBeCloseTo(100)
    expect(dist('d')).toBeCloseTo(200)
    expect(dist('lone')).toBeGreaterThanOrEqual(300)
  })
})

describe('LayoutCache', () => {
  it('evicts the least recently used entry', () => {
    const c = new LayoutCache<number>(2)
    c.set('a', 1)
    c.set('b', 2)
    expect(c.get('a')).toBe(1)
    c.set('c', 3)
    expect(c.get('b')).toBeUndefined()
    expect(c.get('a')).toBe(1)
    expect(c.get('c')).toBe(3)
    expect(c.size).toBe(2)
  })
})

describe('component regions and packed groups', () => {
  const node = (id: string, group?: string) => ({ id, width: 100, height: 40, ...(group ? { group } : {}) })
  const edge = (source: string, target: string) => ({ id: `${source}>${target}`, source, target })

  it('splits the drawing into connected components, largest first; a visual group is connected', () => {
    const comps = connectedComponents({
      nodes: [node('a'), node('b'), node('c'), node('d', 'g'), node('e', 'g'), node('f')],
      edges: [edge('a', 'b'), edge('b', 'c')],
    })
    expect(comps).toEqual([['a', 'b', 'c'], ['d', 'e'], ['f']])
  })

  it('packs a group without internal relationships into a grid inside one block', () => {
    const members = Array.from({ length: 10 }, (_, i) => node(`m${i}`, 'g'))
    const packed = packGroups({
      nodes: [...members, node('x')],
      edges: [edge('x', 'm0')],
      groups: [{ id: 'g', label: 'g' }],
      pinned: {},
    })
    const g = packed.get('g')
    expect(g).toBeDefined()
    const cells = Object.values(g?.cells ?? {})
    expect(cells).toHaveLength(10)
    // Several columns, nothing overlapping, everything below the header and inside the block.
    expect(new Set(cells.map((c) => c.x)).size).toBeGreaterThan(1)
    expect(new Set(cells.map((c) => `${c.x},${c.y}`)).size).toBe(10)
    for (const c of cells) {
      expect(c.y).toBeGreaterThanOrEqual(GROUP_PADDING.top)
      expect(c.x + 100).toBeLessThanOrEqual((g?.width ?? 0) - GROUP_PADDING.side)
      expect(c.y + 40).toBeLessThanOrEqual((g?.height ?? 0) - GROUP_PADDING.bottom)
    }
    // A group with a relationship inside stays a compound node; a pinned member keeps the group unpacked too.
    expect(
      packGroups({ nodes: members, edges: [edge('m0', 'm1')], groups: [{ id: 'g', label: 'g' }], pinned: {} }).size,
    ).toBe(0)
    expect(
      packGroups({ nodes: members, edges: [], groups: [{ id: 'g', label: 'g' }], pinned: { m3: { x: 0, y: 0 } } }).size,
    ).toBe(0)
  })

  it('places components apart, each multi-node component in its own region, and applies pins last', () => {
    type Part = Parameters<typeof arrangeComponents>[1][number]
    const parts: Part[] = [
      { nodes: [node('a'), node('b')], positions: { a: { x: 0, y: 0 }, b: { x: 200, y: 0 } }, groups: {} },
      { nodes: [node('c'), node('d')], positions: { c: { x: 0, y: 0 }, d: { x: 0, y: 100 } }, groups: {} },
      { nodes: [node('e')], positions: { e: { x: 0, y: 0 } }, groups: {} },
    ]
    const r = arrangeComponents(
      { nodes: parts.flatMap((p) => p.nodes), groups: [], pinned: { e: { x: -500, y: -500 } } },
      parts,
    )
    expect(r.regions).toHaveLength(2)
    const [ra, rb] = r.regions ?? []
    const overlap = (p: Box, q: Box) =>
      p.x < q.x + q.width && q.x < p.x + p.width && p.y < q.y + q.height && q.y < p.y + p.height
    expect(ra && rb && overlap(ra, rb)).toBe(false)
    // Every node sits inside its region.
    for (const [id, region] of [
      ['a', ra],
      ['b', ra],
      ['c', rb],
      ['d', rb],
    ] as const) {
      const p = r.positions[id]
      expect(p && region && p.x >= region.x && p.x + 100 <= region.x + region.width).toBe(true)
    }
    expect(r.positions.e).toEqual({ x: -500, y: -500 })
    // One component: no region.
    expect(
      arrangeComponents({ nodes: parts[0]?.nodes ?? [], groups: [], pinned: {} }, parts.slice(0, 1)).regions,
    ).toEqual([])
  })
})
