export type LayoutAlgorithm = 'layered' | 'force' | 'radial'
export type LayoutDirection = 'RIGHT' | 'DOWN' | 'LEFT' | 'UP'

export interface LayoutRequest {
  id: number
  algorithm: LayoutAlgorithm
  direction: LayoutDirection
  nodes: { id: string; width: number; height: number; group?: string }[]
  edges: { id: string; source: string; target: string }[]
  groups: { id: string; label: string }[]
  /** Focus nodes (radial centre, force anchors). */
  focus: string[]
  /** Fixed positions (manual drags / saved layouts) the layout must respect. */
  pinned: Record<string, { x: number; y: number }>
}

export interface Box {
  x: number
  y: number
  width: number
  height: number
}

export interface LayoutResult {
  id: number
  /** Absolute top-left positions. */
  positions: Record<string, { x: number; y: number }>
  /** Group containers (absolute). */
  groups: Record<string, Box>
  /** Backgrounds of the separately laid out connected components (layered layouts with more than one). */
  regions?: Box[]
  ms: number
}

export const GROUP_PADDING = { top: 34, side: 16, bottom: 16 }

/** Bounding boxes of groups around their members (force and radial layouts). */
export function groupBounds(
  req: Pick<LayoutRequest, 'nodes' | 'groups'>,
  positions: Record<string, { x: number; y: number }>,
): LayoutResult['groups'] {
  const out: LayoutResult['groups'] = {}
  for (const g of req.groups) {
    let minX = Infinity
    let minY = Infinity
    let maxX = -Infinity
    let maxY = -Infinity
    for (const n of req.nodes) {
      if (n.group !== g.id) continue
      const p = positions[n.id]
      if (!p) continue
      minX = Math.min(minX, p.x)
      minY = Math.min(minY, p.y)
      maxX = Math.max(maxX, p.x + n.width)
      maxY = Math.max(maxY, p.y + n.height)
    }
    if (minX === Infinity) continue
    out[g.id] = {
      x: minX - GROUP_PADDING.side,
      y: minY - GROUP_PADDING.top,
      width: maxX - minX + GROUP_PADDING.side * 2,
      height: maxY - minY + GROUP_PADDING.top + GROUP_PADDING.bottom,
    }
  }
  return out
}

/**
 * Radial layout around the focus: breadth-first rings (depth = ring), nodes ordered by their parent's angle so related
 * nodes stay close. Pure, so it runs in the worker and in tests.
 */
export function radialLayout(
  req: Pick<LayoutRequest, 'nodes' | 'edges' | 'focus'>,
): Record<string, { x: number; y: number }> {
  const adj = new Map<string, string[]>()
  for (const n of req.nodes) adj.set(n.id, [])
  for (const e of req.edges) {
    adj.get(e.source)?.push(e.target)
    adj.get(e.target)?.push(e.source)
  }
  const size = new Map(req.nodes.map((n) => [n.id, n]))
  const depth = new Map<string, number>()
  const parent = new Map<string, string>()
  const roots = req.focus.filter((f) => size.has(f))
  if (roots.length === 0 && req.nodes[0]) roots.push(req.nodes[0].id)
  const queue = [...roots]
  for (const r of roots) depth.set(r, 0)
  for (;;) {
    const cur = queue.shift()
    if (cur === undefined) break
    for (const nb of adj.get(cur) ?? []) {
      if (depth.has(nb)) continue
      depth.set(nb, (depth.get(cur) ?? 0) + 1)
      parent.set(nb, cur)
      queue.push(nb)
    }
  }
  // Disconnected nodes go to an outer ring.
  const maxDepth = Math.max(0, ...depth.values())
  for (const n of req.nodes) if (!depth.has(n.id)) depth.set(n.id, maxDepth + 1)
  const rings = new Map<number, string[]>()
  for (const [id, d] of depth) rings.set(d, [...(rings.get(d) ?? []), id])
  const angle = new Map<string, number>()
  const pos: Record<string, { x: number; y: number }> = {}
  const ringGap = 240
  for (const d of [...rings.keys()].sort((a, b) => a - b)) {
    const ids = rings.get(d) ?? []
    ids.sort(
      (a, b) => (angle.get(parent.get(a) ?? '') ?? 0) - (angle.get(parent.get(b) ?? '') ?? 0) || a.localeCompare(b),
    )
    if (d === 0) {
      ids.forEach((id, i) => {
        const n = size.get(id)
        const a = (2 * Math.PI * i) / Math.max(1, ids.length)
        const r = ids.length > 1 ? 120 : 0
        angle.set(id, a)
        pos[id] = { x: Math.cos(a) * r - (n?.width ?? 0) / 2, y: Math.sin(a) * r - (n?.height ?? 0) / 2 }
      })
      continue
    }
    // Ring radius grows with the number of nodes so cards do not overlap.
    const radius = Math.max(d * ringGap, (ids.length * 230) / (2 * Math.PI))
    ids.forEach((id, i) => {
      const n = size.get(id)
      const a = (2 * Math.PI * i) / ids.length - Math.PI / 2
      angle.set(id, a)
      pos[id] = { x: Math.cos(a) * radius - (n?.width ?? 0) / 2, y: Math.sin(a) * radius - (n?.height ?? 0) / 2 }
    })
  }
  return pos
}

/**
 * Wraps very tall layers of a layered layout into several columns (rows for vertical directions). A hub with hundreds
 * of neighbours puts them all in one layer, which would be a single column thousands of pixels long; wrapping keeps
 * the drawing compact enough to fit on screen. Later layers shift to make room. Pure, so it is unit-tested.
 */
export function wrapLayers(
  positions: Record<string, { x: number; y: number }>,
  sizes: Map<string, { width: number; height: number }>,
  direction: LayoutDirection,
  maxPerLayer: number,
  gap = 26,
): Record<string, { x: number; y: number }> {
  const horizontal = direction === 'RIGHT' || direction === 'LEFT'
  const main = (p: { x: number; y: number }) => (horizontal ? p.x : p.y)
  const layers = new Map<number, string[]>()
  for (const [id, p] of Object.entries(positions)) {
    const k = Math.round(main(p))
    layers.set(k, [...(layers.get(k) ?? []), id])
  }
  const ordered = [...layers.keys()].sort((a, b) => (direction === 'LEFT' || direction === 'UP' ? b - a : a - b))
  const out: Record<string, { x: number; y: number }> = { ...positions }
  let shift = 0
  for (const k of ordered) {
    const ids = layers.get(k) ?? []
    const sign = direction === 'LEFT' || direction === 'UP' ? -1 : 1
    if (ids.length <= maxPerLayer) {
      for (const id of ids) {
        const p = positions[id]
        if (p) out[id] = horizontal ? { x: p.x + sign * shift, y: p.y } : { x: p.x, y: p.y + sign * shift }
      }
      continue
    }
    ids.sort((a, b) => {
      const pa = positions[a]
      const pb = positions[b]
      return horizontal ? (pa?.y ?? 0) - (pb?.y ?? 0) : (pa?.x ?? 0) - (pb?.x ?? 0)
    })
    const first = positions[ids[0] ?? '']
    const startCross = first ? (horizontal ? first.y : first.x) : 0
    let mainStep = 0
    let crossStep = 0
    for (const id of ids) {
      const sz = sizes.get(id)
      mainStep = Math.max(mainStep, (horizontal ? sz?.width : sz?.height) ?? 0)
      crossStep = Math.max(crossStep, (horizontal ? sz?.height : sz?.width) ?? 0)
    }
    mainStep += gap * 2
    crossStep += gap
    const chunks = Math.ceil(ids.length / maxPerLayer)
    // Centre the wrapped block on the layer's original centre along the cross axis.
    const blockCross = Math.min(ids.length, maxPerLayer) * crossStep
    const origCenter = startCross + (ids.length * crossStep) / 2
    const crossStart = origCenter - blockCross / 2
    ids.forEach((id, i) => {
      const col = Math.floor(i / maxPerLayer)
      const row = i % maxPerLayer
      const m = k + sign * (shift + col * mainStep)
      const c = crossStart + row * crossStep
      out[id] = horizontal ? { x: m, y: c } : { x: c, y: m }
    })
    shift += (chunks - 1) * mainStep
  }
  return out
}

/**
 * Common end of both layered layouts (ELK in layout.ts, fastLayered in the worker): wraps very tall layers into columns
 * (ungrouped layouts only) and lets pinned positions win, growing groups around them.
 */
/**
 * Connected components of the drawing (relationships, and membership of the same visual group), largest first, in
 * first-appearance order among equals. Pure, so it is unit-tested.
 */
export function connectedComponents(req: Pick<LayoutRequest, 'nodes' | 'edges'>): string[][] {
  const parent = new Map(req.nodes.map((n) => [n.id, n.id]))
  const find = (x: string): string => {
    let r = x
    while (parent.get(r) !== r) r = parent.get(r) ?? r
    let c = x
    while (c !== r) {
      const next = parent.get(c) ?? r
      parent.set(c, r)
      c = next
    }
    return r
  }
  const union = (a: string, b: string) => {
    if (!parent.has(a) || !parent.has(b)) return
    const ra = find(a)
    const rb = find(b)
    if (ra !== rb) parent.set(rb, ra)
  }
  for (const e of req.edges) union(e.source, e.target)
  const firstOfGroup = new Map<string, string>()
  for (const n of req.nodes) {
    if (!n.group) continue
    const f = firstOfGroup.get(n.group)
    if (f) union(f, n.id)
    else firstOfGroup.set(n.group, n.id)
  }
  const comps = new Map<string, string[]>()
  for (const n of req.nodes) {
    const r = find(n.id)
    const c = comps.get(r)
    if (c) c.push(n.id)
    else comps.set(r, [n.id])
  }
  return [...comps.values()].sort((a, b) => b.length - a.length)
}

/** The part of a layout request that covers the given nodes (no pins: pins are applied to the whole drawing). */
export function subRequest(req: LayoutRequest, ids: string[]): LayoutRequest {
  const set = new Set(ids)
  const nodes = req.nodes.filter((n) => set.has(n.id))
  const groups = new Set(nodes.map((n) => n.group).filter(Boolean))
  return {
    ...req,
    nodes,
    edges: req.edges.filter((e) => set.has(e.source) && set.has(e.target)),
    groups: req.groups.filter((g) => groups.has(g.id)),
    focus: req.focus.filter((f) => set.has(f)),
    pinned: {},
  }
}

/** Space between connected components, and the padding of their background regions. */
export const REGION_GAP = 100
export const REGION_PAD = 24

/**
 * Places separately laid out components side by side in rows (largest first, rows about 1.6 times as wide as the
 * drawing is tall), each with its own background region when it has two nodes or more, then applies the pins.
 */
export function arrangeComponents(
  req: Pick<LayoutRequest, 'nodes' | 'groups' | 'pinned'>,
  parts: { nodes: LayoutRequest['nodes']; positions: LayoutResult['positions']; groups: LayoutResult['groups'] }[],
): Pick<LayoutResult, 'positions' | 'groups' | 'regions'> {
  const boxes = parts.map((p) => {
    let [x0, y0, x1, y1] = [Infinity, Infinity, -Infinity, -Infinity]
    for (const n of p.nodes) {
      const q = p.positions[n.id] ?? { x: 0, y: 0 }
      ;[x0, y0, x1, y1] = [
        Math.min(x0, q.x),
        Math.min(y0, q.y),
        Math.max(x1, q.x + n.width),
        Math.max(y1, q.y + n.height),
      ]
    }
    for (const g of Object.values(p.groups)) {
      ;[x0, y0, x1, y1] = [
        Math.min(x0, g.x),
        Math.min(y0, g.y),
        Math.max(x1, g.x + g.width),
        Math.max(y1, g.y + g.height),
      ]
    }
    if (x0 === Infinity) [x0, y0, x1, y1] = [0, 0, 0, 0]
    return { x0, y0, width: x1 - x0 + 2 * REGION_PAD, height: y1 - y0 + 2 * REGION_PAD }
  })
  const area = boxes.reduce((a, b) => a + (b.width + REGION_GAP) * (b.height + REGION_GAP), 0)
  const rowWidth = Math.max(...boxes.map((b) => b.width), Math.sqrt(area * 1.6))
  const positions: LayoutResult['positions'] = {}
  const groups: LayoutResult['groups'] = {}
  const regions: Box[] = []
  let [x, y, rowHeight] = [0, 0, 0]
  boxes.forEach((b, i) => {
    const p = parts[i]
    if (!p) return
    if (x > 0 && x + b.width > rowWidth) {
      ;[x, y, rowHeight] = [0, y + rowHeight + REGION_GAP, 0]
    }
    const dx = x - b.x0 + REGION_PAD
    const dy = y - b.y0 + REGION_PAD
    for (const n of p.nodes) {
      const q = p.positions[n.id] ?? { x: 0, y: 0 }
      positions[n.id] = { x: q.x + dx, y: q.y + dy }
    }
    for (const [id, g] of Object.entries(p.groups)) groups[id] = { ...g, x: g.x + dx, y: g.y + dy }
    if (parts.length > 1 && p.nodes.length > 1) regions.push({ x, y, width: b.width, height: b.height })
    x += b.width + REGION_GAP
    rowHeight = Math.max(rowHeight, b.height)
  })
  const merged = { ...positions, ...req.pinned }
  const hasPins = Object.keys(req.pinned).length > 0
  return { positions: merged, groups: hasPins ? groupBounds(req, merged) : groups, regions }
}

/** Space between the cards of a packed group (x, y). */
export const PACK_GAP = { x: 16, y: 14 }

/** A group laid out as one block: its members in a grid (cells relative to the block's top-left corner). */
export interface PackedGroup {
  width: number
  height: number
  cells: Record<string, { x: number; y: number }>
}

/**
 * Groups whose members have no relationships among themselves (an expanded kind, a project of unrelated hosts) would
 * be one tall column in a layered layout. Such a group (two members at least, none pinned) is instead laid out as a
 * single block of a roughly 1.6:1 grid, with the members placed in rows inside it. Pure, so it is unit-tested.
 */
export function packGroups(
  req: Pick<LayoutRequest, 'nodes' | 'edges' | 'groups' | 'pinned'>,
): Map<string, PackedGroup> {
  const groupOf = new Map<string, string>()
  for (const n of req.nodes) if (n.group) groupOf.set(n.id, n.group)
  const linked = new Set<string>()
  for (const e of req.edges) {
    const g = groupOf.get(e.source)
    if (g && e.source !== e.target && g === groupOf.get(e.target)) linked.add(g)
  }
  const out = new Map<string, PackedGroup>()
  for (const g of req.groups) {
    if (linked.has(g.id)) continue
    const members = req.nodes.filter((n) => n.group === g.id)
    if (members.length < 2 || members.some((m) => req.pinned[m.id])) continue
    const cw = Math.max(...members.map((m) => m.width))
    const ch = Math.max(...members.map((m) => m.height))
    const cols = Math.max(
      1,
      Math.min(members.length, Math.round(Math.sqrt((1.6 * members.length * (ch + PACK_GAP.y)) / (cw + PACK_GAP.x)))),
    )
    const rows = Math.ceil(members.length / cols)
    const cells: PackedGroup['cells'] = {}
    members.forEach((m, i) => {
      cells[m.id] = {
        x: GROUP_PADDING.side + (i % cols) * (cw + PACK_GAP.x),
        y: GROUP_PADDING.top + Math.floor(i / cols) * (ch + PACK_GAP.y),
      }
    })
    out.set(g.id, {
      width: GROUP_PADDING.side * 2 + cols * cw + (cols - 1) * PACK_GAP.x,
      height: GROUP_PADDING.top + GROUP_PADDING.bottom + rows * ch + (rows - 1) * PACK_GAP.y,
      cells,
    })
  }
  return out
}

export function finishLayered(
  req: Pick<LayoutRequest, 'nodes' | 'groups' | 'direction' | 'pinned'>,
  positions: Record<string, { x: number; y: number }>,
  groups: LayoutResult['groups'],
): Pick<LayoutResult, 'positions' | 'groups'> {
  // Hubs with hundreds of neighbours make one enormous layer; wrap it into columns.
  const maxPerLayer = Math.max(30, Math.ceil(Math.sqrt(req.nodes.length) * 1.2))
  const wrapped =
    req.groups.length === 0 && req.nodes.length > maxPerLayer
      ? wrapLayers(positions, new Map(req.nodes.map((n) => [n.id, n])), req.direction, maxPerLayer)
      : positions
  const merged = { ...positions, ...wrapped, ...req.pinned }
  const hasPins = Object.keys(req.pinned).length > 0
  return { positions: merged, groups: hasPins ? groupBounds(req, merged) : groups }
}

/**
 * Layouts of more nodes than this take the cheaper paths: ELK polyline instead of orthogonal routing (layout.ts), and
 * leaf folding, warm start and scaled ticks for force layouts (layout.worker.ts). Typical neighbourhoods stay below it
 * and are laid out exactly as before.
 */
export const LARGE_LAYOUT = 150

/** Space kept between folded leaf cards on a ring (x, y). */
const LEAF_GAP = { x: 24, y: 16 }

/**
 * Leaf folding for large force layouts: a node with exactly one neighbour (a container on its host, a host's lone
 * volume) is taken out of the simulation and later placed on rings around that neighbour, which then simulates as one
 * larger disc. The parent must keep another neighbour (two-node components stay simulated), share the leaf's visual
 * group, and the leaf must not be pinned or a focus node. Returns parent → leaves in input order. Pure, so it is
 * unit-tested.
 */
export function foldLeaves(req: Pick<LayoutRequest, 'nodes' | 'edges' | 'focus' | 'pinned'>): Map<string, string[]> {
  const nodes = new Map(req.nodes.map((n) => [n.id, n]))
  const nbs = new Map<string, Set<string>>()
  for (const e of req.edges) {
    if (e.source === e.target || !nodes.has(e.source) || !nodes.has(e.target)) continue
    for (const [a, b] of [
      [e.source, e.target],
      [e.target, e.source],
    ] as const) {
      const s = nbs.get(a) ?? new Set<string>()
      s.add(b)
      nbs.set(a, s)
    }
  }
  const keep = new Set(req.focus)
  const out = new Map<string, string[]>()
  for (const n of req.nodes) {
    const own = nbs.get(n.id)
    if (own?.size !== 1 || keep.has(n.id) || req.pinned[n.id]) continue
    const parent = [...own][0] ?? ''
    if ((nbs.get(parent)?.size ?? 0) < 2 || nodes.get(parent)?.group !== n.group) continue
    out.set(parent, [...(out.get(parent) ?? []), n.id])
  }
  return out
}

/**
 * Centre offsets for `count` boxes of `box` size on concentric rings around a parent box of the same size, starting at
 * angle `base` and leaving a `gap` (radians, centred on `base`) free for the parent's other edges. Each next box is the
 * first one clear of the previous (axis-aligned, with LEAF_GAP), rings are far enough apart that boxes on different
 * rings never overlap, and the last box on a full ring also clears the first. Returns the offsets and the disc radius.
 */
export function leafRings(
  count: number,
  box: { width: number; height: number },
  base = 0,
  gap = 0,
): { offsets: { x: number; y: number }[]; radius: number } {
  const w = box.width + LEAF_GAP.x
  const h = box.height + LEAF_GAP.y
  const ring = Math.hypot(w, h)
  const offsets: { x: number; y: number }[] = []
  let r = 0
  while (offsets.length < count) {
    r += ring
    const at = (t: number) => ({ x: Math.cos(base + t) * r, y: Math.sin(base + t) * r })
    const clear = (a: { x: number; y: number }, b: { x: number; y: number }) =>
      Math.abs(a.x - b.x) >= w || Math.abs(a.y - b.y) >= h
    const step = Math.min(0.02, 20 / r)
    const start = gap / 2
    const end = 2 * Math.PI - gap / 2
    const first = at(start)
    let t = start
    let prev: { x: number; y: number } | null = null
    while (t <= end && offsets.length < count) {
      const p = at(t)
      if (prev && gap === 0 && !clear(p, first)) break
      offsets.push(p)
      prev = p
      let u = t + step
      while (u <= end && !clear(p, at(u))) u += step
      t = u
    }
  }
  return { offsets, radius: r + ring / 2 }
}

/**
 * Warm start for force layouts: a breadth-first tree from the focus (or the first node) where every subtree gets an
 * angular sector proportional to its size and each depth its own ring, so connected nodes start close together and the
 * simulation needs far fewer ticks to untangle. Nodes the tree does not reach start on an outer spiral. Centres, not
 * top-left corners. Pure, so it is unit-tested.
 */
export function sectorLayout(
  req: Pick<LayoutRequest, 'nodes' | 'edges' | 'focus'>,
  ringGap = 400,
): Record<string, { x: number; y: number }> {
  const adj = new Map<string, string[]>(req.nodes.map((n) => [n.id, []]))
  for (const e of req.edges) {
    adj.get(e.source)?.push(e.target)
    adj.get(e.target)?.push(e.source)
  }
  const root = req.focus.find((f) => adj.has(f)) ?? req.nodes[0]?.id
  const pos: Record<string, { x: number; y: number }> = {}
  if (root === undefined) return pos
  const depth = new Map([[root, 0]])
  const children = new Map<string, string[]>()
  const order = [root]
  // Breadth-first: `order` grows while it is iterated.
  for (const cur of order) {
    for (const nb of adj.get(cur) ?? []) {
      if (depth.has(nb)) continue
      depth.set(nb, (depth.get(cur) ?? 0) + 1)
      children.set(cur, [...(children.get(cur) ?? []), nb])
      order.push(nb)
    }
  }
  const size = new Map<string, number>()
  for (let i = order.length - 1; i >= 0; i--) {
    const id = order[i] ?? ''
    size.set(id, 1 + (children.get(id) ?? []).reduce((s, c) => s + (size.get(c) ?? 1), 0))
  }
  const sector = new Map([[root, [0, 2 * Math.PI] as const]])
  pos[root] = { x: 0, y: 0 }
  let maxDepth = 0
  for (const id of order) {
    const [a0, a1] = sector.get(id) ?? [0, 2 * Math.PI]
    const kids = children.get(id) ?? []
    const total = Math.max(1, (size.get(id) ?? 1) - 1)
    let a = a0
    for (const c of kids) {
      const span = ((a1 - a0) * (size.get(c) ?? 1)) / total
      sector.set(c, [a, a + span])
      const d = depth.get(c) ?? 1
      maxDepth = Math.max(maxDepth, d)
      pos[c] = { x: Math.cos(a + span / 2) * d * ringGap, y: Math.sin(a + span / 2) * d * ringGap }
      a += span
    }
  }
  let k = 0
  for (const n of req.nodes) {
    if (pos[n.id]) continue
    const a = k * 2.39996
    const r = (maxDepth + 1) * ringGap + 40 * Math.sqrt(k++)
    pos[n.id] = { x: Math.cos(a) * r, y: Math.sin(a) * r }
  }
  return pos
}

/**
 * In-memory layout cache, least recently used first out: a revisit of the same graph (same view, layout options,
 * topology and pins) reuses the positions instead of running the layout again.
 */
export class LayoutCache<T> {
  private readonly entries = new Map<string, T>()

  constructor(private readonly capacity = 24) {}

  get(key: string): T | undefined {
    const v = this.entries.get(key)
    if (v === undefined) return undefined
    this.entries.delete(key)
    this.entries.set(key, v)
    return v
  }

  set(key: string, value: T) {
    this.entries.delete(key)
    this.entries.set(key, value)
    while (this.entries.size > this.capacity) {
      const oldest = this.entries.keys().next().value
      if (oldest === undefined) break
      this.entries.delete(oldest)
    }
  }

  get size() {
    return this.entries.size
  }
}
