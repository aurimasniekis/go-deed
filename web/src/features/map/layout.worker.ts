/**
 * Force and radial layouts off the main thread (the layered layout runs in ELK's own worker, see layout.ts). Runs only
 * when the graph's topology changes.
 */
import {
  forceCollide,
  forceLink,
  forceManyBody,
  forceSimulation,
  forceX,
  forceY,
  type SimulationNodeDatum,
} from 'd3-force'
import {
  LARGE_LAYOUT,
  foldLeaves,
  groupBounds,
  leafRings,
  radialLayout,
  sectorLayout,
  type LayoutRequest,
  type LayoutResult,
} from './layoutTypes'

interface SimNode extends SimulationNodeDatum {
  id: string
  w: number
  h: number
  /** Collision radius: the card's half diagonal, or the disc of leaves folded around it. */
  r: number
  group?: string
}

/** d3 replaces link endpoints (ids) with the node objects once the simulation initialises. */
const idOf = (end: unknown) => (typeof end === 'string' ? end : (end as SimNode).id)

/** Angular gap (radians) kept free in a leaf disc towards the parent's other neighbours. */
const LEAF_WEDGE = 0.6

/**
 * Force-directed layout (d3-force). Small graphs (≤ LARGE_LAYOUT nodes) simulate every node for 300 ticks from a
 * phyllotaxis start. Larger graphs, where that took over a second at the 2,000-node cap, are made cheaper without
 * becoming a hairball:
 *
 * - leaf folding: degree-1 nodes (foldLeaves) leave the simulation and are placed on rings around their neighbour
 *   afterwards, which simulates as one disc — in typical neighbourhoods (hosts and their containers) that removes most
 *   nodes and every leaf-on-leaf overlap;
 * - the remaining skeleton starts from a sector-radial tree (sectorLayout) instead of a spiral, so far fewer ticks
 *   untangle it: 300 up to 400 skeleton nodes, 160 up to 1,000, 100 beyond, with a coarser Barnes–Hut approximation
 *   (theta 1.2) above 400;
 * - a short collision-only pass at the end pushes apart discs the link forces still hold together.
 */
function forceLayout(req: LayoutRequest): Record<string, { x: number; y: number }> {
  const large = req.nodes.length > LARGE_LAYOUT
  const folded = large ? foldLeaves(req) : new Map<string, string[]>()
  const leaves = new Set([...folded.values()].flat())
  const byId = new Map(req.nodes.map((n) => [n.id, n]))
  const coreNodes = req.nodes.filter((n) => !leaves.has(n.id))
  const coreEdges = req.edges.filter((e) => !leaves.has(e.source) && !leaves.has(e.target))
  const hasCoreNeighbour = new Set(coreEdges.filter((e) => e.source !== e.target).flatMap((e) => [e.source, e.target]))
  const leafBox = (ids: string[]) => ({
    width: Math.max(...ids.map((id) => byId.get(id)?.width ?? 0)),
    height: Math.max(...ids.map((id) => byId.get(id)?.height ?? 0)),
  })
  // Disc radius for each parent: the worst case over a few start angles, since the real one is known only afterwards.
  const disc = new Map<string, number>()
  for (const [p, ids] of folded) {
    const gap = hasCoreNeighbour.has(p) ? LEAF_WEDGE : 0
    const box = leafBox(ids)
    disc.set(p, Math.max(...[0, 0.25, 0.5, 0.75].map((b) => leafRings(ids.length, box, b * Math.PI, gap).radius)))
  }

  const groups = [...new Set(req.nodes.map((n) => n.group).filter((g): g is string => !!g))]
  const centers = new Map(
    groups.map((g, i) => {
      const a = (2 * Math.PI * i) / Math.max(1, groups.length)
      const r = groups.length > 1 ? 160 * Math.sqrt(req.nodes.length) : 0
      return [g, { x: Math.cos(a) * r, y: Math.sin(a) * r }]
    }),
  )
  const warm = large ? sectorLayout({ nodes: coreNodes, edges: coreEdges, focus: req.focus }) : null
  const nodes: SimNode[] = coreNodes.map((n, i) => {
    const pin = req.pinned[n.id]
    const a = i * 2.39996
    const r = 12 * Math.sqrt(i)
    const start = warm?.[n.id] ?? { x: Math.cos(a) * r, y: Math.sin(a) * r }
    return {
      id: n.id,
      w: n.width,
      h: n.height,
      r: disc.get(n.id) ?? Math.hypot(n.width, n.height) / 2 + 12,
      ...(n.group ? { group: n.group } : {}),
      x: pin ? pin.x + n.width / 2 : start.x,
      y: pin ? pin.y + n.height / 2 : start.y,
      ...(pin ? { fx: pin.x + n.width / 2, fy: pin.y + n.height / 2 } : {}),
    }
  })
  const m = nodes.length
  const links = coreEdges.map((e) => ({ source: e.source, target: e.target }))
  const charge = forceManyBody<SimNode>().distanceMax(900)
  if (large) charge.strength((d) => -520 * Math.max(1, d.r / 130)).theta(m > 400 ? 1.2 : 0.9)
  else charge.strength(-520)
  const collide = forceCollide<SimNode>((d) => d.r).iterations(large ? 2 : 1)
  const sim = forceSimulation(nodes)
    .force(
      'link',
      forceLink<SimNode, { source: string; target: string }>(links)
        .id((d) => d.id)
        .distance((l) => 150 + (disc.get(idOf(l.source)) ?? 0) + (disc.get(idOf(l.target)) ?? 0))
        .strength(0.4),
    )
    .force('charge', charge)
    .force('collide', collide)
    .force(
      'x',
      forceX<SimNode>((d) => (d.group ? (centers.get(d.group)?.x ?? 0) : 0)).strength((d) => (d.group ? 0.12 : 0.03)),
    )
    .force(
      'y',
      forceY<SimNode>((d) => (d.group ? (centers.get(d.group)?.y ?? 0) : 0)).strength((d) => (d.group ? 0.12 : 0.03)),
    )
    .stop()
  const ticks = !large ? 300 : m > 1000 ? 100 : m > 400 ? 160 : 300
  for (let i = 0; i < ticks; i++) sim.tick()
  if (large) {
    sim.force('link', null).force('charge', null).force('x', null).force('y', null)
    for (let i = 0; i < 30; i++) sim.tick()
  }

  const out: Record<string, { x: number; y: number }> = {}
  const centre = new Map<string, SimNode>()
  for (const n of nodes) {
    centre.set(n.id, n)
    out[n.id] = { x: (n.x ?? 0) - n.w / 2, y: (n.y ?? 0) - n.h / 2 }
  }
  // Leaves on rings around their parent, the free wedge facing the parent's other neighbours.
  const coreAdj = new Map<string, string[]>()
  for (const e of coreEdges) {
    if (e.source === e.target) continue
    coreAdj.set(e.source, [...(coreAdj.get(e.source) ?? []), e.target])
    coreAdj.set(e.target, [...(coreAdj.get(e.target) ?? []), e.source])
  }
  for (const [p, ids] of folded) {
    const c = centre.get(p)
    if (!c) continue
    const cx = c.x ?? 0
    const cy = c.y ?? 0
    let ax = 0
    let ay = 0
    for (const o of coreAdj.get(p) ?? []) {
      ax += (centre.get(o)?.x ?? cx) - cx
      ay += (centre.get(o)?.y ?? cy) - cy
    }
    const others = (coreAdj.get(p) ?? []).length > 0
    const { offsets } = leafRings(ids.length, leafBox(ids), others ? Math.atan2(ay, ax) : 0, others ? LEAF_WEDGE : 0)
    ids.forEach((id, i) => {
      const n = byId.get(id)
      const o = offsets[i] ?? { x: 0, y: 0 }
      out[id] = { x: cx + o.x - (n?.width ?? 0) / 2, y: cy + o.y - (n?.height ?? 0) / 2 }
    })
  }
  return out
}

interface WorkerScope {
  onmessage: ((ev: MessageEvent<LayoutRequest>) => void) | null
  postMessage: (msg: LayoutResult) => void
}

const scope = self as unknown as WorkerScope

scope.onmessage = (ev) => {
  const req = ev.data
  const t0 = performance.now()
  let positions = req.algorithm === 'radial' ? radialLayout(req) : forceLayout(req)
  positions = { ...positions, ...req.pinned }
  scope.postMessage({ id: req.id, positions, groups: groupBounds(req, positions), ms: performance.now() - t0 })
}
