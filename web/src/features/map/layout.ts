import ELK, { type ElkExtendedEdge, type ElkNode } from 'elkjs/lib/elk-api'
import elkWorkerUrl from 'elkjs/lib/elk-worker.min.js?worker&url'
import { FAST_LAYERED_MIN } from './layered'
import {
  finishLayered,
  GROUP_PADDING,
  LARGE_LAYOUT,
  arrangeComponents,
  connectedComponents,
  LayoutCache,
  packGroups,
  subRequest,
  type LayoutRequest,
  type LayoutResult,
} from './layoutTypes'

/**
 * Layout off the main thread. Layered layouts use ELK (elkjs) in ELK's own Web Worker (elk-worker.min.js, a separate
 * same-origin asset, so the CSP's script-src 'self' allows it); force, radial and large ungrouped layered layouts
 * (fastLayered, above FAST_LAYERED_MIN nodes) use our layout.worker.ts.
 */
let elk: InstanceType<typeof ELK> | null = null
let worker: Worker | null = null
let seq = 0
const pending = new Map<number, (r: LayoutResult) => void>()

function getElk() {
  elk ??= new ELK({ workerFactory: () => new Worker(elkWorkerUrl, { type: 'module' }) })
  return elk
}

function getWorker() {
  if (!worker) {
    worker = new Worker(new URL('./layout.worker.ts', import.meta.url), { type: 'module' })
    worker.onmessage = (ev: MessageEvent<LayoutResult>) => {
      const done = pending.get(ev.data.id)
      pending.delete(ev.data.id)
      done?.(ev.data)
    }
  }
  return worker
}

/**
 * Layered layout: every connected component is laid out on its own and the components are then placed apart, each in
 * its own region, instead of interleaving unrelated chains in one set of layers.
 */
async function layered(req: LayoutRequest): Promise<LayoutResult> {
  const t0 = performance.now()
  const parts = await Promise.all(
    connectedComponents(req).map(async (ids) => {
      const sub = subRequest(req, ids)
      const only = sub.nodes.length === 1 ? sub.nodes[0] : undefined
      if (only) return { nodes: sub.nodes, positions: { [only.id]: { x: 0, y: 0 } }, groups: {} }
      const r = await layeredComponent(sub)
      return { nodes: sub.nodes, ...finishLayered(sub, r.positions, r.groups) }
    }),
  )
  return { id: req.id, ...arrangeComponents(req, parts), ms: performance.now() - t0 }
}

async function layeredComponent(req: LayoutRequest): Promise<Pick<LayoutResult, 'positions' | 'groups'>> {
  const big = req.nodes.length > FAST_LAYERED_MIN
  const groupNodes = new Map<string, ElkNode>(
    req.groups.map((g) => [
      g.id,
      {
        id: g.id,
        children: [],
        layoutOptions: {
          'elk.padding': `[top=${GROUP_PADDING.top},left=${GROUP_PADDING.side},bottom=${GROUP_PADDING.bottom},right=${GROUP_PADDING.side}]`,
        },
      },
    ]),
  )
  // Groups without internal relationships are one block each (a grid of their members), not a compound node.
  const packed = packGroups(req)
  const blockOf = new Map<string, string>()
  const top: ElkNode[] = []
  for (const [id, p] of packed) {
    groupNodes.delete(id)
    top.push({ id, width: p.width, height: p.height })
    for (const m of Object.keys(p.cells)) blockOf.set(m, id)
  }
  for (const n of req.nodes) {
    if (blockOf.has(n.id)) continue
    const child: ElkNode = { id: n.id, width: n.width, height: n.height }
    const g = n.group ? groupNodes.get(n.group) : undefined
    if (g) g.children?.push(child)
    else top.push(child)
  }
  const edges: ElkExtendedEdge[] = []
  for (const e of req.edges) {
    const source = blockOf.get(e.source) ?? e.source
    const target = blockOf.get(e.target) ?? e.target
    if (source !== target) edges.push({ id: e.id, sources: [source], targets: [target] })
  }
  const graph: ElkNode = {
    id: 'root',
    layoutOptions: {
      'elk.algorithm': 'layered',
      'elk.direction': req.direction,
      'elk.hierarchyHandling': 'INCLUDE_CHILDREN',
      'elk.spacing.nodeNode': '26',
      'elk.layered.spacing.nodeNodeBetweenLayers': '90',
      'elk.spacing.componentComponent': '60',
      'elk.layered.nodePlacement.strategy': big ? 'SIMPLE' : 'BRANDES_KOEPF',
      'elk.layered.thoroughness': big ? '1' : '7',
      'elk.separateConnectedComponents': 'true',
      // The map draws its own edges, so ELK's routes are discarded; orthogonal routing (the default) was ~80 % of the
      // time for 2,000 nodes. Polyline routing is 3–5× cheaper and leaves positions alike apart from the gaps between
      // layers, which no longer grow with the number of routed edges. Small graphs keep the default.
      ...(req.nodes.length > LARGE_LAYOUT ? { 'elk.edgeRouting': 'POLYLINE' } : {}),
    },
    children: [...top, ...[...groupNodes.values()].filter((g) => (g.children?.length ?? 0) > 0)],
    edges,
  }
  const out = await getElk().layout(graph)
  const positions: LayoutResult['positions'] = {}
  const groups: LayoutResult['groups'] = {}
  const walk = (n: ElkNode, ox: number, oy: number) => {
    for (const c of n.children ?? []) {
      const x = ox + (c.x ?? 0)
      const y = oy + (c.y ?? 0)
      const block = packed.get(c.id)
      if (block) {
        groups[c.id] = { x, y, width: block.width, height: block.height }
        for (const [m, cell] of Object.entries(block.cells)) positions[m] = { x: x + cell.x, y: y + cell.y }
      } else if (groupNodes.has(c.id)) {
        groups[c.id] = { x, y, width: c.width ?? 0, height: c.height ?? 0 }
        walk(c, x, y)
      } else {
        positions[c.id] = { x, y }
      }
    }
  }
  walk(out, 0, 0)
  return { positions, groups }
}

/**
 * Finished layouts by key (MapPage: saved view, layout options and topology signature, pinned positions), kept for the
 * page's lifetime so going back to a graph seen before skips the worker entirely.
 */
export const layoutCache = new LayoutCache<LayoutResult>(24)

/**
 * Starts the layout workers ahead of the first layout (the map calls it on mount, while its data loads): loading and
 * compiling ELK's worker script, which is most of a cold layered layout's time on small graphs, then overlaps the fetch.
 */
export function warmLayout() {
  if (elk) return
  void getElk()
    .layout({ id: 'warm', children: [{ id: 'a', width: 1, height: 1 }], edges: [] })
    .catch(() => undefined)
  getWorker()
}

export function runLayout(req: Omit<LayoutRequest, 'id'>): Promise<LayoutResult> {
  const id = ++seq
  if (req.algorithm === 'layered' && (req.groups.length > 0 || req.nodes.length <= FAST_LAYERED_MIN))
    return layered({ ...req, id })
  return new Promise((resolve) => {
    pending.set(id, resolve)
    getWorker().postMessage({ ...req, id })
  })
}
