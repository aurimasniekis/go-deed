import { createContext, useContext, useSyncExternalStore } from 'react'

/**
 * Progressive level-of-detail swap (see LOD in nodes.tsx). Crossing the zoom threshold used to swap every block for a
 * card and mount every edge in view in one React commit — one frame of ~100 ms at 500 nodes and ~500 ms at 2,000. The
 * map now moves between the two drawings in steps, one per frame: each step swaps what is off screen (free: React Flow
 * does not mount it) plus a budget of what is on screen, nearest the centre of the view first, nodes before edges.
 * Which nodes and edges are detailed lives in a small external store (DetailStore) that the node components, the
 * flow's edge list and LodEdges subscribe to, so a step re-renders only what it swaps — not the map page or React
 * Flow's node list. While a swap is in progress the remaining edges are drawn by LodEdges and the legend keeps saying
 * the drawing is simplified. lodStep is pure, so it is unit-tested; MapPage schedules the steps and adapts the budget
 * to the frame time.
 */

/** Which nodes are drawn as cards and which edges as full edges ('all': every one). */
export interface Drawn {
  cards: ReadonlySet<string> | 'all'
  edges: ReadonlySet<string> | 'all'
}

const NONE: ReadonlySet<string> = new Set()
export const ALL_DETAIL: Drawn = { cards: 'all', edges: 'all' }
export const NO_DETAIL: Drawn = { cards: NONE, edges: NONE }

/**
 * Budget (nodes plus edges swapped on screen per step) the first swap in each direction starts with; MapPage adapts
 * it to the frame time. Cards and edges cost far more to mount than to unmount.
 */
export const STEP_BUDGET = { toDetail: 60, toBlocks: 150 }

const has = (s: ReadonlySet<string> | 'all', id: string) => s === 'all' || s.has(id)

/** Whether the drawing is complete: everything detailed (toDetail) or nothing. */
export function settled(d: Drawn, toDetail: boolean): boolean {
  return toDetail
    ? d.cards === 'all' && d.edges === 'all'
    : d.cards !== 'all' && d.cards.size === 0 && d.edges !== 'all' && d.edges.size === 0
}

export interface Box {
  id: string
  x: number
  y: number
  width: number
  height: number
}

/** The visible area in flow coordinates; null when everything is mounted (React Flow renders all elements). */
export interface View {
  x0: number
  y0: number
  x1: number
  y1: number
}

/**
 * One step towards the target drawing: every pending node and edge that is not on screen, and up to `budget` that are
 * (nodes first, at most half the budget while edges are pending too; then edges), nearest the view centre first.
 */
export function lodStep(input: {
  drawn: Drawn
  toDetail: boolean
  nodes: Box[]
  edges: { id: string; source: string; target: string }[]
  view: View | null
  budget: number
}): Drawn {
  const { drawn, toDetail, nodes, edges, view } = input
  const budget = Math.max(1, Math.floor(input.budget))
  const cx = view ? (view.x0 + view.x1) / 2 : 0
  const cy = view ? (view.y0 + view.y1) / 2 : 0
  const byId = new Map(nodes.map((n) => [n.id, n]))
  const overlaps = (x0: number, y0: number, x1: number, y1: number) =>
    !view || (x1 >= view.x0 && x0 <= view.x1 && y1 >= view.y0 && y0 <= view.y1)

  const pendingNodes = nodes.filter((n) => has(drawn.cards, n.id) !== toDetail)
  const pendingEdges = edges.filter((e) => has(drawn.edges, e.id) !== toDetail)

  const visibleNodes: { id: string; d: number }[] = []
  const flipNodes: string[] = []
  for (const n of pendingNodes) {
    if (overlaps(n.x, n.y, n.x + n.width, n.y + n.height))
      visibleNodes.push({ id: n.id, d: Math.hypot(n.x + n.width / 2 - cx, n.y + n.height / 2 - cy) })
    else flipNodes.push(n.id)
  }
  const visibleEdges: { id: string; d: number }[] = []
  const flipEdges: string[] = []
  for (const e of pendingEdges) {
    const a = byId.get(e.source)
    const b = byId.get(e.target)
    if (!a || !b) {
      flipEdges.push(e.id)
      continue
    }
    // React Flow's rule: an edge is rendered when the box around both of its nodes meets the view.
    const x0 = Math.min(a.x, b.x)
    const y0 = Math.min(a.y, b.y)
    const x1 = Math.max(a.x + a.width, b.x + b.width)
    const y1 = Math.max(a.y + a.height, b.y + b.height)
    if (overlaps(x0, y0, x1, y1)) {
      const mx = (a.x + a.width / 2 + b.x + b.width / 2) / 2
      const my = (a.y + a.height / 2 + b.y + b.height / 2) / 2
      visibleEdges.push({ id: e.id, d: Math.hypot(mx - cx, my - cy) })
    } else flipEdges.push(e.id)
  }
  const nearest = (a: { id: string; d: number }, b: { id: string; d: number }) => a.d - b.d || (a.id < b.id ? -1 : 1)
  visibleNodes.sort(nearest)
  visibleEdges.sort(nearest)
  const nodeBudget = visibleEdges.length > 0 ? Math.ceil(budget / 2) : budget
  const takenNodes = visibleNodes.slice(0, nodeBudget)
  const takenEdges = visibleEdges.slice(0, budget - takenNodes.length)
  for (const v of takenNodes) flipNodes.push(v.id)
  for (const v of takenEdges) flipEdges.push(v.id)

  // Done once nothing is pending ('all' or empty, which also drops ids of nodes and edges no longer on the map).
  const apply = (cur: ReadonlySet<string> | 'all', flips: string[], all: () => string[], pending: number) => {
    if (flips.length === pending) return toDetail ? 'all' : NONE
    if (flips.length === 0) return cur
    const next = new Set(cur === 'all' ? all() : cur)
    for (const id of flips) {
      if (toDetail) next.add(id)
      else next.delete(id)
    }
    return next
  }
  const cards = apply(drawn.cards, flipNodes, () => nodes.map((n) => n.id), pendingNodes.length)
  const drawnEdges = apply(drawn.edges, flipEdges, () => edges.map((e) => e.id), pendingEdges.length)
  return cards === drawn.cards && drawnEdges === drawn.edges ? drawn : { cards, edges: drawnEdges }
}

/** The current drawing, for components that render part of it (see useCard, useDrawnEdges). */
export class DetailStore {
  drawn: Drawn = ALL_DETAIL
  /**
   * Whether blocks render their connection points (Ports in nodes.tsx). Measuring them costs ~20 ms when 2,000 blocks
   * mount at once, so a drawing that opens zoomed out mounts its blocks without them and MapPage adds and measures them
   * once the browser is idle (or before the first swap step, whichever comes first).
   */
  ports = true
  private readonly listeners = new Set<() => void>()

  subscribe = (fn: () => void) => {
    this.listeners.add(fn)
    return () => {
      this.listeners.delete(fn)
    }
  }

  set(d: Drawn) {
    if (d === this.drawn) return
    this.drawn = d
    this.notify()
  }

  setPorts(on: boolean) {
    if (on === this.ports) return
    this.ports = on
    this.notify()
  }

  private notify() {
    for (const fn of this.listeners) fn()
  }
}

export const DetailContext = createContext<DetailStore | null>(null)

const noSubscribe = () => () => undefined

/** Whether the node is drawn as a card (true without a DetailContext). */
export function useCard(id: string): boolean {
  const store = useContext(DetailContext)
  return useSyncExternalStore(store?.subscribe ?? noSubscribe, () => (store ? has(store.drawn.cards, id) : true))
}

/** The edges drawn as full edges ('all' without a DetailContext). */
export function useDrawnEdges(): Drawn['edges'] {
  const store = useContext(DetailContext)
  return useSyncExternalStore(store?.subscribe ?? noSubscribe, () => (store ? store.drawn.edges : 'all'))
}

/** Whether blocks render their connection points (DetailStore.ports; true without a DetailContext). */
export function usePorts(): boolean {
  const store = useContext(DetailContext)
  return useSyncExternalStore(store?.subscribe ?? noSubscribe, () => store?.ports ?? true)
}
