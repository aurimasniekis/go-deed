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
import { groupBounds, radialLayout, type LayoutRequest, type LayoutResult } from './layoutTypes'

interface SimNode extends SimulationNodeDatum {
  id: string
  w: number
  h: number
  group?: string
}

function forceLayout(req: LayoutRequest): Record<string, { x: number; y: number }> {
  const groups = [...new Set(req.nodes.map((n) => n.group).filter((g): g is string => !!g))]
  const centers = new Map(
    groups.map((g, i) => {
      const a = (2 * Math.PI * i) / Math.max(1, groups.length)
      const r = groups.length > 1 ? 160 * Math.sqrt(req.nodes.length) : 0
      return [g, { x: Math.cos(a) * r, y: Math.sin(a) * r }]
    }),
  )
  const nodes: SimNode[] = req.nodes.map((n, i) => {
    const pin = req.pinned[n.id]
    const a = i * 2.39996
    const r = 12 * Math.sqrt(i)
    return {
      id: n.id,
      w: n.width,
      h: n.height,
      ...(n.group ? { group: n.group } : {}),
      x: pin ? pin.x + n.width / 2 : Math.cos(a) * r,
      y: pin ? pin.y + n.height / 2 : Math.sin(a) * r,
      ...(pin ? { fx: pin.x + n.width / 2, fy: pin.y + n.height / 2 } : {}),
    }
  })
  const links = req.edges.map((e) => ({ source: e.source, target: e.target }))
  const sim = forceSimulation(nodes)
    .force(
      'link',
      forceLink<SimNode, { source: string; target: string }>(links)
        .id((d) => d.id)
        .distance(150)
        .strength(0.4),
    )
    .force('charge', forceManyBody().strength(-520).distanceMax(900))
    .force(
      'collide',
      forceCollide<SimNode>((d) => Math.hypot(d.w, d.h) / 2 + 12),
    )
    .force(
      'x',
      forceX<SimNode>((d) => (d.group ? (centers.get(d.group)?.x ?? 0) : 0)).strength((d) => (d.group ? 0.12 : 0.03)),
    )
    .force(
      'y',
      forceY<SimNode>((d) => (d.group ? (centers.get(d.group)?.y ?? 0) : 0)).strength((d) => (d.group ? 0.12 : 0.03)),
    )
    .stop()
  const ticks = Math.min(400, Math.max(120, Math.ceil(Math.log(sim.alphaMin()) / Math.log(1 - sim.alphaDecay()))))
  for (let i = 0; i < ticks; i++) sim.tick()
  const out: Record<string, { x: number; y: number }> = {}
  for (const n of nodes) out[n.id] = { x: (n.x ?? 0) - n.w / 2, y: (n.y ?? 0) - n.h / 2 }
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
