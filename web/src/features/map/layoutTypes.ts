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

export interface LayoutResult {
  id: number
  /** Absolute top-left positions. */
  positions: Record<string, { x: number; y: number }>
  /** Group containers (absolute). */
  groups: Record<string, { x: number; y: number; width: number; height: number }>
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
