import type { LayoutDirection, LayoutRequest } from './layoutTypes'

/**
 * Ungrouped layered layouts of more nodes than this use fastLayered (in layout.worker.ts) instead of ELK. Up to it ELK
 * runs with its full settings (network-simplex layering, Brandes–Köpf placement); above it the map already asked ELK
 * for its cheapest settings (simple placement, thoroughness 1), which still took ~350 ms at the 2,000-node cap in a
 * freshly started worker — most of the first frame's budget. fastLayered does the same steps in ~20–40 ms.
 */
export const FAST_LAYERED_MIN = 600

/** Spacing as in the ELK settings (layout.ts): between nodes in a layer, between layers, between components. */
const NODE_GAP = 26
const LAYER_GAP = 90
const COMPONENT_GAP = 60
/** Space a long edge's bend point takes in a layer (ELK's edge–node and edge–edge spacing). */
const EDGE_GAP = 10
/** Barycentre sweeps (each one down or up the layers); the ordering with the fewest crossings is kept. */
const SWEEPS = 8
/** Layering balance passes (see layering()). */
const BALANCE_PASSES = 4
/** Width/height the component packing aims for (ELK's default aspect ratio). */
const ASPECT = 1.6

type Pos = Record<string, { x: number; y: number }>

interface Graph {
  /** Node count (real nodes first, then bend points of long edges). */
  n: number
  real: number
  layer: number[]
  /** Neighbours in the previous / next layer (edges always span exactly one layer after splitting). */
  up: number[][]
  down: number[][]
}

/**
 * A Sugiyama-style layered layout for large graphs, pure so it runs in the worker and in tests:
 *
 * 1. connected components are laid out separately and packed in rows (ELK's separateConnectedComponents);
 * 2. cycles are broken by reversing depth-first back edges;
 * 3. layering is longest-path, then balanced: a node with more outgoing than incoming edges moves as close to its
 *    successors as it can, one with more incoming moves close to its predecessors — on tree-like neighbourhoods that is
 *    what network-simplex layering gives (every edge spans one layer);
 * 4. edges spanning several layers get bend points (dummy nodes) so ordering keeps room for them;
 * 5. nodes are ordered within layers by barycentre sweeps, keeping the sweep with the fewest crossings;
 * 6. layers are stacked along the direction and each layer is centred on the cross axis (ELK's SIMPLE node
 *    placement, which the map already used at this size); nodes of a layer share their main-axis coordinate.
 *
 * Returns top-left positions. Deterministic: the same input gives the same output.
 */
export function fastLayered(req: Pick<LayoutRequest, 'nodes' | 'edges' | 'direction'>): Pos {
  const n = req.nodes.length
  const index = new Map(req.nodes.map((nd, i) => [nd.id, i]))
  const horizontal = req.direction === 'RIGHT' || req.direction === 'LEFT'
  const mainSize = req.nodes.map((nd) => (horizontal ? nd.width : nd.height))
  const crossSize = req.nodes.map((nd) => (horizontal ? nd.height : nd.width))

  // Unique directed edges between distinct known nodes.
  const seen = new Set<number>()
  const edges: [number, number][] = []
  for (const e of req.edges) {
    const u = index.get(e.source)
    const v = index.get(e.target)
    if (u === undefined || v === undefined || u === v) continue
    const key = u * n + v
    if (seen.has(key)) continue
    seen.add(key)
    edges.push([u, v])
  }

  // Connected components, numbered by their first node in input order.
  const parent = Array.from({ length: n }, (_, i) => i)
  const find = (x: number): number => {
    let r = x
    while ((parent[r] ?? r) !== r) r = parent[r] ?? r
    while ((parent[x] ?? x) !== r) {
      const next = parent[x] ?? x
      parent[x] = r
      x = next
    }
    return r
  }
  for (const [u, v] of edges) {
    const a = find(u)
    const b = find(v)
    if (a !== b) parent[Math.max(a, b)] = Math.min(a, b)
  }
  const compOf = new Map<number, number>()
  const comps: number[][] = []
  for (let i = 0; i < n; i++) {
    const r = find(i)
    let c = compOf.get(r)
    if (c === undefined) {
      c = comps.length
      compOf.set(r, c)
      comps.push([])
    }
    comps[c]?.push(i)
  }
  const compEdges: [number, number][][] = comps.map(() => [])
  for (const e of edges) compEdges[compOf.get(find(e[0])) ?? 0]?.push(e)

  // Lay out each component in local coordinates, then pack.
  const boxes = comps.map((members, c) => {
    const local = layoutComponent(members, compEdges[c] ?? [], mainSize, crossSize, req.direction)
    let minX = Infinity
    let minY = Infinity
    let maxX = -Infinity
    let maxY = -Infinity
    for (const [i, p] of local) {
      const nd = req.nodes[i]
      if (!nd) continue
      minX = Math.min(minX, p.x)
      minY = Math.min(minY, p.y)
      maxX = Math.max(maxX, p.x + nd.width)
      maxY = Math.max(maxY, p.y + nd.height)
    }
    return { local, minX, minY, width: maxX - minX, height: maxY - minY, first: members[0] ?? 0 }
  })
  boxes.sort((a, b) => b.width * b.height - a.width * a.height || a.first - b.first)
  const area = boxes.reduce((s, b) => s + (b.width + COMPONENT_GAP) * (b.height + COMPONENT_GAP), 0)
  const rowLimit = Math.max(Math.sqrt(area * ASPECT), ...boxes.map((b) => b.width))
  const out: Pos = {}
  let x = 0
  let y = 0
  let rowHeight = 0
  for (const b of boxes) {
    if (x > 0 && x + b.width > rowLimit) {
      x = 0
      y += rowHeight + COMPONENT_GAP
      rowHeight = 0
    }
    for (const [i, p] of b.local) {
      const id = req.nodes[i]?.id
      if (id !== undefined) out[id] = { x: x + p.x - b.minX, y: y + p.y - b.minY }
    }
    x += b.width + COMPONENT_GAP
    rowHeight = Math.max(rowHeight, b.height)
  }
  return out
}

/** One connected component: positions by node index, in the requested direction. */
function layoutComponent(
  members: number[],
  edges: [number, number][],
  mainSize: number[],
  crossSize: number[],
  direction: LayoutDirection,
): Map<number, { x: number; y: number }> {
  const local = new Map(members.map((g, i) => [g, i]))
  const m = members.length
  const dag = breakCycles(
    m,
    edges.map(([u, v]) => [local.get(u) ?? 0, local.get(v) ?? 0]),
  )
  const layerOf = layering(m, dag)
  const g = splitLongEdges(m, dag, layerOf)
  const order = orderLayers(g)

  // Main axis: layers one after another; cross axis: each layer stacked and centred on 0.
  const layers = order.length
  const thick = order.map((ids) => Math.max(0, ...ids.filter((v) => v < m).map((v) => mainSize[members[v] ?? 0] ?? 0)))
  const start: number[] = []
  let at = 0
  for (let l = 0; l < layers; l++) {
    start.push(at)
    at += (thick[l] ?? 0) + LAYER_GAP
  }
  const out = new Map<number, { x: number; y: number }>()
  const sign = direction === 'LEFT' || direction === 'UP' ? -1 : 1
  const horizontal = direction === 'RIGHT' || direction === 'LEFT'
  order.forEach((ids, l) => {
    const cross: number[] = []
    let c = 0
    let prevReal: boolean | null = null
    for (const v of ids) {
      const isReal = v < m
      if (prevReal !== null) c += prevReal && isReal ? NODE_GAP : EDGE_GAP
      cross.push(c)
      c += isReal ? (crossSize[members[v] ?? 0] ?? 0) : 0
      prevReal = isReal
    }
    const shift = -c / 2
    const mainAt = sign > 0 ? (start[l] ?? 0) : -((start[l] ?? 0) + (thick[l] ?? 0))
    ids.forEach((v, k) => {
      if (v >= m) return
      const crossAt = (cross[k] ?? 0) + shift
      out.set(members[v] ?? 0, horizontal ? { x: mainAt, y: crossAt } : { x: crossAt, y: mainAt })
    })
  })
  return out
}

/** Reverses the back edges of an iterative depth-first search (roots and successors in index order). */
export function breakCycles(n: number, edges: [number, number][]): [number, number][] {
  const succ: number[][] = Array.from({ length: n }, () => [])
  const pred: number[][] = Array.from({ length: n }, () => [])
  for (const [u, v] of edges) {
    succ[u]?.push(v)
    pred[v]?.push(u)
  }
  const outdeg = succ.map((s) => s.length)
  const indeg = pred.map((p) => p.length)
  const removed = new Uint8Array(n)
  const rank = new Int32Array(n)
  let left = 0
  let right = n - 1
  const sinks: number[] = []
  const sources: number[] = []
  for (let v = n - 1; v >= 0; v--) {
    if (outdeg[v] === 0) sinks.push(v)
    else if (indeg[v] === 0) sources.push(v)
  }
  const remove = (v: number) => {
    removed[v] = 1
    for (const w of succ[v] ?? []) {
      if (removed[w]) continue
      indeg[w] = (indeg[w] ?? 0) - 1
      if (indeg[w] === 0) sources.push(w)
    }
    for (const u of pred[v] ?? []) {
      if (removed[u]) continue
      outdeg[u] = (outdeg[u] ?? 0) - 1
      if (outdeg[u] === 0) sinks.push(u)
    }
  }
  for (let done = 0; done < n; done++) {
    let v = -1
    while (v < 0 && sinks.length > 0) {
      const s = sinks.pop() ?? 0
      if (!removed[s]) {
        v = s
        rank[v] = right--
      }
    }
    while (v < 0 && sources.length > 0) {
      const s = sources.pop() ?? 0
      if (!removed[s]) {
        v = s
        rank[v] = left++
      }
    }
    if (v < 0) {
      // Every remaining node is on a cycle: take the one with the most surplus of outgoing edges.
      let best = -Infinity
      for (let w = 0; w < n; w++) {
        if (removed[w]) continue
        const d = (outdeg[w] ?? 0) - (indeg[w] ?? 0)
        if (d > best) {
          best = d
          v = w
        }
      }
      rank[v] = left++
    }
    remove(v)
  }
  return edges.map(([u, v]) => ((rank[u] ?? 0) > (rank[v] ?? 0) ? [v, u] : [u, v]))
}

/**
 * Layer per node of a DAG: longest path from the sources, then balance passes that move each node with unequal in- and
 * out-degree towards the side with more edges, as far as its neighbours allow (never lengthening the total edge span).
 * The smallest layer is 0.
 */
export function layering(n: number, dag: [number, number][]): number[] {
  const succ: number[][] = Array.from({ length: n }, () => [])
  const pred: number[][] = Array.from({ length: n }, () => [])
  for (const [u, v] of dag) {
    succ[u]?.push(v)
    pred[v]?.push(u)
  }
  // Kahn's topological order, sources in index order.
  const indeg = pred.map((p) => p.length)
  const topo: number[] = []
  for (let i = 0; i < n; i++) if (indeg[i] === 0) topo.push(i)
  // `topo` grows while it is iterated.
  for (const v of topo) {
    for (const w of succ[v] ?? []) {
      indeg[w] = (indeg[w] ?? 0) - 1
      if (indeg[w] === 0) topo.push(w)
    }
  }
  const layer = new Array<number>(n).fill(0)
  for (const v of topo) for (const w of succ[v] ?? []) layer[w] = Math.max(layer[w] ?? 0, (layer[v] ?? 0) + 1)
  for (let pass = 0; pass < BALANCE_PASSES; pass++) {
    let moved = false
    for (let h = topo.length - 1; h >= 0; h--) {
      const v = topo[h] ?? 0
      const s = succ[v] ?? []
      if (s.length === 0 || s.length <= (pred[v]?.length ?? 0)) continue
      const to = Math.min(...s.map((w) => layer[w] ?? 0)) - 1
      if (to > (layer[v] ?? 0)) {
        layer[v] = to
        moved = true
      }
    }
    for (const v of topo) {
      const p = pred[v] ?? []
      if (p.length === 0 || p.length <= (succ[v]?.length ?? 0)) continue
      const to = Math.max(...p.map((u) => layer[u] ?? 0)) + 1
      if (to < (layer[v] ?? 0)) {
        layer[v] = to
        moved = true
      }
    }
    if (!moved) break
  }
  let min = Infinity
  for (const l of layer) min = Math.min(min, l)
  return min === Infinity ? layer : layer.map((l) => l - min)
}

/** Proper layered graph: every edge spanning k > 1 layers becomes a chain through k − 1 bend points. */
function splitLongEdges(n: number, dag: [number, number][], layerOf: number[]): Graph {
  const layer = [...layerOf]
  const up: number[][] = Array.from({ length: n }, () => [])
  const down: number[][] = Array.from({ length: n }, () => [])
  const link = (a: number, b: number) => {
    down[a]?.push(b)
    up[b]?.push(a)
  }
  for (const [u, v] of dag) {
    let prev = u
    for (let l = (layer[u] ?? 0) + 1; l < (layer[v] ?? 0); l++) {
      const d = layer.length
      layer.push(l)
      up.push([])
      down.push([])
      link(prev, d)
      prev = d
    }
    link(prev, v)
  }
  return { n: layer.length, real: n, layer, up, down }
}

/** Orders every layer by barycentre sweeps (down, up, …) and returns the ordering with the fewest crossings. */
function orderLayers(g: Graph): number[][] {
  let layers = 1
  for (const l of g.layer) layers = Math.max(layers, l + 1)
  // Initial order: first visit of a depth-first walk over the undirected graph, so neighbours start out together.
  const order: number[][] = Array.from({ length: layers }, () => [])
  const visited = new Uint8Array(g.n)
  for (let root = 0; root < g.n; root++) {
    if (visited[root]) continue
    const stack = [root]
    while (stack.length > 0) {
      const v = stack.pop() ?? 0
      if (visited[v]) continue
      visited[v] = 1
      order[g.layer[v] ?? 0]?.push(v)
      const nb = [...(g.up[v] ?? []), ...(g.down[v] ?? [])]
      for (let i = nb.length - 1; i >= 0; i--) if (!visited[nb[i] ?? 0]) stack.push(nb[i] ?? 0)
    }
  }
  const pos = new Float64Array(g.n)
  const index = () => {
    for (const ids of order) ids.forEach((v, k) => (pos[v] = k))
  }
  index()
  let best = order.map((ids) => [...ids])
  let bestCrossings = crossings(order, g.down, pos)
  const bary = new Float64Array(g.n)
  for (let sweep = 0; sweep < SWEEPS && bestCrossings > 0; sweep++) {
    const downward = sweep % 2 === 0
    for (let k = 1; k < layers; k++) {
      const l = downward ? k : layers - 1 - k
      const ids = order[l] ?? []
      // A node without neighbours on the fixed side stays right after the node it follows now.
      let last = -1
      for (const v of ids) {
        const nb = (downward ? g.up[v] : g.down[v]) ?? []
        if (nb.length === 0) {
          bary[v] = last
          continue
        }
        let s = 0
        for (const w of nb) s += pos[w] ?? 0
        last = s / nb.length
        bary[v] = last
      }
      ids.sort((a, b) => (bary[a] ?? 0) - (bary[b] ?? 0) || (pos[a] ?? 0) - (pos[b] ?? 0))
      ids.forEach((v, i) => (pos[v] = i))
    }
    const c = crossings(order, g.down, pos)
    if (c < bestCrossings) {
      bestCrossings = c
      best = order.map((ids) => [...ids])
    }
  }
  return best
}

/** Edge crossings between consecutive layers (sorting edges by upper end, then counting inversions of lower ends). */
export function crossings(order: number[][], down: number[][], pos: ArrayLike<number>): number {
  let total = 0
  for (let l = 0; l + 1 < order.length; l++) {
    const width = order[l + 1]?.length ?? 0
    if (width < 2) continue
    const ends: number[] = []
    for (const v of order[l] ?? []) {
      const targets = (down[v] ?? []).map((w) => pos[w] ?? 0).sort((a, b) => a - b)
      for (const t of targets) ends.push(t)
    }
    // Fenwick tree over lower positions: for each edge, count earlier edges ending further along.
    const tree = new Int32Array(width + 1)
    let seenEdges = 0
    for (const t of ends) {
      let le = 0
      for (let i = t + 1; i > 0; i -= i & -i) le += tree[i] ?? 0
      total += seenEdges - le
      for (let i = t + 1; i <= width; i += i & -i) tree[i] = (tree[i] ?? 0) + 1
      seenEdges++
    }
  }
  return total
}
