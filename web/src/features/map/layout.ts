import ELK, { type ElkExtendedEdge, type ElkNode } from 'elkjs/lib/elk-api'
import elkWorkerUrl from 'elkjs/lib/elk-worker.min.js?worker&url'
import { GROUP_PADDING, groupBounds, wrapLayers, type LayoutRequest, type LayoutResult } from './layoutTypes'

/**
 * Layout off the main thread. Layered layouts use ELK (elkjs) in ELK's own Web Worker (elk-worker.min.js, a separate
 * same-origin asset, so the CSP's script-src 'self' allows it); force and radial layouts use our layout.worker.ts.
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
  const big = req.nodes.length > 600
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
  // Hubs with hundreds of neighbours make one enormous layer; wrap it into columns (ungrouped layouts only).
  const maxPerLayer = Math.max(30, Math.ceil(Math.sqrt(req.nodes.length) * 1.2))
  const wrapped =
    req.groups.length === 0 && req.nodes.length > maxPerLayer
      ? wrapLayers(positions, new Map(req.nodes.map((n) => [n.id, n])), req.direction, maxPerLayer)
      : positions
  Object.assign(positions, wrapped)
  // Pinned (dragged or saved) positions win; their groups grow around them.
  const merged = { ...positions, ...req.pinned }
  const hasPins = Object.keys(req.pinned).length > 0
  return {
    id: req.id,
    positions: merged,
    groups: hasPins ? groupBounds(req, merged) : groups,
    ms: performance.now() - t0,
  }
}

export function runLayout(req: Omit<LayoutRequest, 'id'>): Promise<LayoutResult> {
  const id = ++seq
  if (req.algorithm === 'layered') return layered({ ...req, id })
  return new Promise((resolve) => {
    pending.set(id, resolve)
    getWorker().postMessage({ ...req, id })
  })
}
