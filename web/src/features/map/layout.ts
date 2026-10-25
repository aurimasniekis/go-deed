import ELK, { type ElkExtendedEdge, type ElkNode } from 'elkjs/lib/elk-api'
import elkWorkerUrl from 'elkjs/lib/elk-worker.min.js?worker&url'
import { FAST_LAYERED_MIN } from './layered'
import {
  finishLayered,
  GROUP_PADDING,
  LARGE_LAYOUT,
  LayoutCache,
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

async function layered(req: LayoutRequest): Promise<LayoutResult> {
  const t0 = performance.now()
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
  const top: ElkNode[] = []
  for (const n of req.nodes) {
    const child: ElkNode = { id: n.id, width: n.width, height: n.height }
    const g = n.group ? groupNodes.get(n.group) : undefined
    if (g) g.children?.push(child)
    else top.push(child)
  }
  const edges: ElkExtendedEdge[] = req.edges.map((e) => ({ id: e.id, sources: [e.source], targets: [e.target] }))
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
      if (groupNodes.has(c.id)) {
        groups[c.id] = { x, y, width: c.width ?? 0, height: c.height ?? 0 }
        walk(c, x, y)
      } else {
        positions[c.id] = { x, y }
      }
    }
  }
  walk(out, 0, 0)
  return { id: req.id, ...finishLayered(req, positions, groups), ms: performance.now() - t0 }
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
