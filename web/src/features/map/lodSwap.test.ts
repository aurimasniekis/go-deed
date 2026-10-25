import { describe, expect, it } from 'vitest'
import { ALL_DETAIL, DetailStore, lodStep, NO_DETAIL, settled, type Box, type Drawn } from './lodSwap'

/** A row of 100 × 50 boxes, 200 apart, chained by edges n0 → n1 → …; the view covers the first `inView` of them. */
function row(count: number) {
  const nodes: Box[] = Array.from({ length: count }, (_, i) => ({
    id: `n${String(i)}`,
    x: i * 200,
    y: 0,
    width: 100,
    height: 50,
  }))
  const edges = nodes.slice(1).map((n, i) => ({ id: `e${String(i)}`, source: `n${String(i)}`, target: n.id }))
  return { nodes, edges }
}
const viewOf = (inView: number) => ({ x0: 0, y0: 0, x1: inView * 200 - 150, y1: 50 })
const ids = (s: Drawn['cards']) => (s === 'all' ? 'all' : [...s].sort())

describe('lodStep', () => {
  it('swaps everything at once when the budget allows', () => {
    const { nodes, edges } = row(5)
    const out = lodStep({ drawn: NO_DETAIL, toDetail: true, nodes, edges, view: null, budget: 100 })
    expect(out).toEqual(ALL_DETAIL)
    expect(settled(out, true)).toBe(true)
  })

  it('swaps what is off screen at once and what is on screen within the budget, nearest the centre first', () => {
    const { nodes, edges } = row(20)
    // n0…n4 in view, centred near n2.
    const out = lodStep({ drawn: NO_DETAIL, toDetail: true, nodes, edges, view: viewOf(5), budget: 4 })
    const cards = ids(out.cards)
    expect(cards).toEqual(expect.arrayContaining(['n2', 'n1']))
    // Off-screen nodes are free; at most half the budget goes to visible nodes while edges are pending.
    for (let i = 5; i < 20; i++) expect(cards).toContain(`n${String(i)}`)
    expect(cards.length).toBe(15 + 2)
    // e0…e4 touch the view (e4 through n4); e5… are off screen and free, two visible ones fit the rest of the budget.
    const drawnEdges = ids(out.edges)
    for (let i = 5; i < 19; i++) expect(drawnEdges).toContain(`e${String(i)}`)
    expect(drawnEdges.length).toBe(14 + 2)
  })

  it('reaches the target drawing in a bounded number of steps, in both directions', () => {
    const { nodes, edges } = row(40)
    for (const toDetail of [true, false]) {
      let d: Drawn = toDetail ? NO_DETAIL : ALL_DETAIL
      let steps = 0
      while (!settled(d, toDetail) && steps < 100) {
        d = lodStep({ drawn: d, toDetail, nodes, edges, view: viewOf(10), budget: 6 })
        steps++
      }
      expect(settled(d, toDetail)).toBe(true)
      // 10 visible nodes and 10 visible edges at 6 per step (nodes take 3 while edges wait).
      expect(steps).toBeLessThanOrEqual(5)
    }
  })

  it('treats everything as on screen without a view (React Flow mounts it all)', () => {
    const { nodes, edges } = row(10)
    const out = lodStep({ drawn: ALL_DETAIL, toDetail: false, nodes, edges, view: null, budget: 4 })
    expect(ids(out.cards)).toHaveLength(10 - 2)
    expect(ids(out.edges)).toHaveLength(9 - 2)
  })

  it('settles even when the drawing still lists nodes and edges that are gone', () => {
    const { nodes, edges } = row(3)
    const stale: Drawn = { cards: new Set(['gone', 'n0']), edges: new Set(['old']) }
    const out = lodStep({ drawn: stale, toDetail: false, nodes, edges, view: null, budget: 10 })
    expect(settled(out, false)).toBe(true)
  })

  it('returns the same drawing when nothing is pending', () => {
    const { nodes, edges } = row(3)
    expect(lodStep({ drawn: ALL_DETAIL, toDetail: true, nodes, edges, view: null, budget: 10 })).toBe(ALL_DETAIL)
    expect(lodStep({ drawn: NO_DETAIL, toDetail: false, nodes, edges, view: null, budget: 10 })).toBe(NO_DETAIL)
  })
})

describe('DetailStore', () => {
  it('notifies subscribers of changes only', () => {
    const store = new DetailStore()
    let calls = 0
    const off = store.subscribe(() => calls++)
    store.set(ALL_DETAIL)
    expect(calls).toBe(0)
    store.set(NO_DETAIL)
    expect(calls).toBe(1)
    off()
    store.set(ALL_DETAIL)
    expect(calls).toBe(1)
  })
})
