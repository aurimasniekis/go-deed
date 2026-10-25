import { describe, expect, it } from 'vitest'
import { breakCycles, crossings, fastLayered, layering } from './layered'
import type { LayoutDirection } from './layoutTypes'

const node = (id: string, width = 224, height = 66) => ({ id, width, height })
const link = (source: string, target: string) => ({ id: `${source}>${target}`, source, target })

/** The scale project's shape (e2e/seed.ts seedLarge): hosts on a core network, containers on hosts, cross links. */
function scaleGraph(hosts: number, perHost: number) {
  const nodes = [node('core')]
  const edges = []
  for (let h = 0; h < hosts; h++) {
    nodes.push(node(`h${String(h)}`))
    edges.push(link(`h${String(h)}`, 'core'))
    for (let i = 0; i < perHost; i++) {
      nodes.push(node(`h${String(h)}c${String(i)}`))
      edges.push(link(`h${String(h)}c${String(i)}`, `h${String(h)}`))
      if (i > 0 && i % 3 === 0)
        edges.push(link(`h${String(h)}c${String(i)}`, `h${String((h + 1) % hosts)}c${String(i - 1)}`))
    }
  }
  return { nodes, edges }
}

/** Deterministic shuffle (so input order cannot do the ordering's work). */
function shuffled<T>(xs: T[], seed = 7): T[] {
  const out = [...xs]
  let s = seed
  for (let i = out.length - 1; i > 0; i--) {
    s = (s * 1103515245 + 12345) % 2 ** 31
    const j = s % (i + 1)
    ;[out[i], out[j]] = [out[j] as T, out[i] as T]
  }
  return out
}

function overlaps(
  nodes: { id: string; width: number; height: number }[],
  pos: Record<string, { x: number; y: number }>,
): string[] {
  const boxes = nodes.map((n) => ({ ...n, ...(pos[n.id] ?? { x: NaN, y: NaN }) }))
  // Sweep along x so the check stays fast on a few thousand boxes.
  boxes.sort((a, b) => a.x - b.x)
  const hits: string[] = []
  for (let i = 0; i < boxes.length; i++) {
    const a = boxes[i]
    if (!a) continue
    for (let j = i + 1; j < boxes.length; j++) {
      const b = boxes[j]
      if (!b || b.x >= a.x + a.width) break
      if (b.y < a.y + a.height && a.y < b.y + b.height) hits.push(`${a.id}/${b.id}`)
    }
  }
  return hits
}

describe('fastLayered', () => {
  it.each<LayoutDirection>(['RIGHT', 'DOWN', 'LEFT', 'UP'])('points every edge along the direction (%s)', (dir) => {
    const g = scaleGraph(6, 12)
    const pos = fastLayered({ ...g, direction: dir })
    expect(Object.keys(pos)).toHaveLength(g.nodes.length)
    const main = (id: string) => {
      const p = pos[id] ?? { x: NaN, y: NaN }
      return dir === 'RIGHT' ? p.x : dir === 'LEFT' ? -p.x : dir === 'DOWN' ? p.y : -p.y
    }
    for (const e of g.edges) expect(main(e.target), e.id).toBeGreaterThan(main(e.source))
  })

  it('gives every node of a layer the same main-axis coordinate and keeps layers apart', () => {
    const nodes = [node('a', 224), node('b', 176), node('c', 210), node('d')]
    const pos = fastLayered({ nodes, edges: [link('a', 'b'), link('a', 'c'), link('b', 'd')], direction: 'RIGHT' })
    expect(pos.b?.x).toBe(pos.c?.x)
    expect(pos.b?.x).toBeGreaterThanOrEqual((pos.a?.x ?? 0) + 224 + 90)
    expect(pos.d?.x).toBeGreaterThanOrEqual((pos.b?.x ?? 0) + 210 + 90)
  })

  it('places a few thousand nodes without overlaps', () => {
    const g = scaleGraph(40, 49)
    const sized = g.nodes.map((n, i) => (i % 5 === 0 ? { ...n, width: 210, height: 58 } : n))
    for (const direction of ['RIGHT', 'DOWN'] as const) {
      const pos = fastLayered({ nodes: sized, edges: g.edges, direction })
      expect(overlaps(sized, pos)).toEqual([])
    }
  })

  it('separates connected components', () => {
    const nodes = ['a', 'b', 'c', 'x', 'y', 'lone'].map((id) => node(id))
    const pos = fastLayered({ nodes, edges: [link('a', 'b'), link('b', 'c'), link('x', 'y')], direction: 'RIGHT' })
    expect(overlaps(nodes, pos)).toEqual([])
    const comps = [['a', 'b', 'c'], ['x', 'y'], ['lone']].map((ids) => {
      const xs = ids.map((id) => pos[id]?.x ?? NaN)
      const ys = ids.map((id) => pos[id]?.y ?? NaN)
      return { x0: Math.min(...xs), x1: Math.max(...xs) + 224, y0: Math.min(...ys), y1: Math.max(...ys) + 66 }
    })
    comps.forEach((a, i) => {
      comps.slice(i + 1).forEach((b) => {
        const apart = a.x1 + 60 <= b.x0 || b.x1 + 60 <= a.x0 || a.y1 + 60 <= b.y0 || b.y1 + 60 <= a.y0
        expect(apart).toBe(true)
      })
    })
  })

  it('lays out a tree without crossings whatever the input order', () => {
    const nodes = [node('r')]
    const edges = []
    for (let i = 0; i < 6; i++) {
      nodes.push(node(`m${String(i)}`))
      edges.push(link('r', `m${String(i)}`))
      for (let j = 0; j < 5; j++) {
        nodes.push(node(`m${String(i)}l${String(j)}`))
        edges.push(link(`m${String(i)}`, `m${String(i)}l${String(j)}`))
      }
    }
    const pos = fastLayered({ nodes: shuffled(nodes), edges: shuffled(edges), direction: 'RIGHT' })
    // Every pair of edges between the same two layers keeps its order on both ends.
    const y = (id: string) => pos[id]?.y ?? NaN
    const between = edges.filter((e) => e.source !== 'r')
    for (const a of between)
      for (const b of between) {
        if (a.source === b.source) continue
        expect(Math.sign(y(a.source) - y(b.source))).toBe(Math.sign(y(a.target) - y(b.target)))
      }
  })

  it('is deterministic', () => {
    const g = scaleGraph(10, 20)
    const a = fastLayered({ ...g, direction: 'RIGHT' })
    const b = fastLayered({ ...g, direction: 'RIGHT' })
    expect(b).toEqual(a)
  })

  it('handles cycles, self loops, duplicates and unknown endpoints', () => {
    const nodes = ['a', 'b', 'c'].map((id) => node(id))
    const edges = [link('a', 'b'), link('b', 'c'), link('c', 'a'), link('a', 'a'), link('a', 'b'), link('a', 'zz')]
    const pos = fastLayered({ nodes, edges, direction: 'RIGHT' })
    expect(Object.keys(pos).sort()).toEqual(['a', 'b', 'c'])
    expect(overlaps(nodes, pos)).toEqual([])
    expect(fastLayered({ nodes: [], edges: [], direction: 'DOWN' })).toEqual({})
  })
})

describe('layered steps', () => {
  it('breaks cycles into a DAG', () => {
    const dag = breakCycles(4, [
      [0, 1],
      [1, 2],
      [2, 0],
      [2, 3],
      [3, 1],
    ])
    const layer = layering(4, dag)
    for (const [u, v] of dag) expect(layer[v] ?? 0).toBeGreaterThan(layer[u] ?? 0)
  })

  it('balances layers so tree edges span one layer', () => {
    // 0 → 2, 1 → 2 → 3 and 4 → 3: longest path puts 4 in layer 0, two layers before its successor.
    const layer = layering(5, [
      [0, 2],
      [1, 2],
      [2, 3],
      [4, 3],
    ])
    expect(layer).toEqual([0, 0, 1, 2, 1])
  })

  it('counts crossings between consecutive layers', () => {
    const order = [
      [0, 1],
      [2, 3],
    ]
    const pos = [0, 1, 0, 1]
    expect(crossings(order, [[3], [2], [], []], pos)).toBe(1)
    expect(crossings(order, [[2], [3], [], []], pos)).toBe(0)
  })
})
