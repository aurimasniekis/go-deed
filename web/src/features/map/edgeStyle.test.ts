import { describe, expect, it } from 'vitest'
import {
  buildLegend,
  edgeVisual,
  flowSeconds,
  formatRate,
  rateWidth,
  strokeOf,
  type EdgeStyleOptions,
  type RateInfo,
} from './edgeStyle'
import type { MapEdge, MapNode } from './types'

const NOW = Date.parse('2026-10-25T00:00:00Z')
const days = (n: number) => new Date(NOW - n * 86_400_000).toISOString()

const edge = (over: Partial<MapEdge>): MapEdge => ({
  id: 'rel_1',
  from: 'a',
  to: 'b',
  type: 'depends_on',
  evidence: 'declared',
  inferred: false,
  crossProject: false,
  count: 1,
  ...over,
})

const node = (id: string, health: string): MapNode => ({
  id,
  kind: 'service',
  name: id,
  projectId: 'p',
  health,
  freshness: 'fresh',
  declared: true,
  valid: true,
  findings: 0,
  depth: 0,
})

const nodes = new Map([
  ['a', node('a', 'ok')],
  ['b', node('b', 'failed')],
])
const opts: EdgeStyleOptions = {
  colorMode: 'type',
  typeColors: {},
  labels: false,
  directionHint: false,
  traffic: true,
  reducedMotion: false,
}
const typeColor = (t: string) => ({ depends_on: '#fa5252', runs_on: '#4c6ef5' })[t]

describe('edge strokes encode evidence', () => {
  it('maps evidence, staleness and bridges to strokes', () => {
    expect(strokeOf(edge({ evidence: 'declared' }), NOW)).toBe('solid')
    expect(strokeOf(edge({ evidence: 'structural' }), NOW)).toBe('solid')
    expect(strokeOf(edge({ evidence: 'observed', lastSeenAt: days(1) }), NOW)).toBe('observed')
    expect(strokeOf(edge({ evidence: 'inferred', inferred: true, lastSeenAt: days(1) }), NOW)).toBe('dashed')
    expect(strokeOf(edge({ evidence: 'observed', lastSeenAt: days(9) }), NOW)).toBe('dotted')
    // Declared facts never go stale.
    expect(strokeOf(edge({ evidence: 'declared', lastSeenAt: days(90) }), NOW)).toBe('solid')
    expect(strokeOf(edge({ crossProject: true }), NOW)).toBe('double')
    expect(strokeOf(edge({ evidence: 'mystery' }), NOW)).toBe('faded')
  })

  it('colours by type, honouring per-view overrides, and by worst endpoint health', () => {
    expect(edgeVisual(edge({}), opts, typeColor, nodes, NOW).color).toBe('#fa5252')
    expect(edgeVisual(edge({}), { ...opts, typeColors: { depends_on: '#000000' } }, typeColor, nodes, NOW).color).toBe(
      '#000000',
    )
    expect(edgeVisual(edge({ type: 'unknown' }), opts, typeColor, nodes, NOW).color).toBe('#868e96')
    const health = edgeVisual(edge({}), { ...opts, colorMode: 'health' }, typeColor, nodes, NOW)
    expect(health.colorKey).toBe('health:failed')
    expect(health.colorLabel).toContain('Failed')
  })

  it('dashes inferred edges, dots stale ones and fades unknown evidence', () => {
    expect(edgeVisual(edge({ evidence: 'inferred', inferred: true }), opts, typeColor, nodes, NOW).dash).toBe('7 5')
    expect(edgeVisual(edge({ evidence: 'observed', lastSeenAt: days(8) }), opts, typeColor, nodes, NOW).dash).toBe(
      '1.5 4.5',
    )
    expect(edgeVisual(edge({ evidence: 'x' }), opts, typeColor, nodes, NOW).opacity).toBe(0.4)
  })

  it('animates the direction hint only when enabled and motion is allowed', () => {
    expect(edgeVisual(edge({}), opts, typeColor, nodes, NOW).animated).toBe(false)
    expect(edgeVisual(edge({}), { ...opts, directionHint: true }, typeColor, nodes, NOW).animated).toBe(true)
    expect(
      edgeVisual(edge({}), { ...opts, directionHint: true, reducedMotion: true }, typeColor, nodes, NOW).animated,
    ).toBe(false)
  })
})

describe('traffic overlay', () => {
  const measured: RateInfo = { status: 'measured', rate: 120, unit: 'req/s', window: '15m', basis: 'binding:x' }
  const inferred: RateInfo = {
    status: 'inferred',
    rate: 3,
    unit: 'msg/s',
    window: '15m',
    basis: 'pulse_tail',
    note: 'publish rate of the subject',
  }
  const unavailable: RateInfo = { status: 'unavailable', window: '15m', basis: 'none', note: 'no measurement source' }

  it('animates and widens only measured rates', () => {
    const v = edgeVisual(edge({}), opts, typeColor, nodes, NOW, measured)
    expect(v.traffic).toBe(true)
    expect(v.width).toBeCloseTo(rateWidth(120))
    expect(v.rateLabel).toBe('120 req/s')
    expect(v.notes.join(' ')).toContain('sampled, 15m window')
  })

  it('labels inferred rates with their note and never animates them', () => {
    const v = edgeVisual(edge({}), opts, typeColor, nodes, NOW, inferred)
    expect(v.traffic).toBe(false)
    expect(v.rateLabel).toBe('~3.00 msg/s inferred')
    expect(v.notes.join(' ')).toContain('publish rate of the subject')
  })

  it('ignores unavailable rates, the overlay switch and reduced motion', () => {
    const base = edgeVisual(edge({}), opts, typeColor, nodes, NOW)
    expect(edgeVisual(edge({}), opts, typeColor, nodes, NOW, unavailable)).toEqual(base)
    expect(edgeVisual(edge({}), { ...opts, traffic: false }, typeColor, nodes, NOW, measured).traffic).toBe(false)
    const reduced = edgeVisual(edge({}), { ...opts, reducedMotion: true }, typeColor, nodes, NOW, measured)
    expect(reduced.traffic).toBe(false)
    expect(reduced.rateLabel).toBe('120 req/s')
  })

  it('scales width and speed logarithmically within bounds', () => {
    expect(rateWidth(0)).toBe(1.5)
    expect(rateWidth(1e9)).toBe(8)
    expect(flowSeconds(0)).toBe(4)
    expect(flowSeconds(1e6)).toBeGreaterThanOrEqual(0.6)
    expect(flowSeconds(1000)).toBeLessThan(flowSeconds(10))
    expect(formatRate(1234.5, 'req/s')).toBe('1235 req/s')
    expect(formatRate(0.123)).toBe('0.12')
  })
})

describe('legend generation', () => {
  it('explains exactly the colours, strokes and animations on screen', () => {
    const edges = [
      edge({ id: 'rel_1', type: 'depends_on' }),
      edge({ id: 'rel_2', type: 'runs_on', evidence: 'inferred', inferred: true }),
      edge({ id: 'rel_3', type: 'runs_on', evidence: 'observed', lastSeenAt: days(10) }),
    ]
    const legend = buildLegend(edges, opts, typeColor, (t) => (t === 'runs_on' ? 'Runs on' : 'Depends on'), nodes, NOW)
    expect(legend.colors.map((c) => c.label)).toEqual(['Depends on', 'Runs on'])
    expect(legend.strokes.map((s) => s.kind)).toEqual(['solid', 'dashed', 'dotted'])
    expect(legend.animations).toEqual([])
    expect(legend.notes[0]).toContain('Arrowheads')
  })

  it('adds traffic and direction-hint animations only when they are visible', () => {
    const rates = new Map<string, RateInfo>([['rel_1', { status: 'measured', rate: 10, window: '15m', basis: 'b' }]])
    const legend = buildLegend(
      [edge({}), edge({ id: 'rel_9' })],
      { ...opts, directionHint: true },
      typeColor,
      (t) => t,
      nodes,
      NOW,
      rates,
    )
    expect(legend.animations.map((a) => a.label)).toEqual([
      'Moving light dots (traffic)',
      'Marching dashes (direction hint)',
    ])
    expect(legend.animations[0]?.description).toContain('15m window')
    const reduced = buildLegend(
      [edge({})],
      { ...opts, directionHint: true, reducedMotion: true },
      typeColor,
      (t) => t,
      nodes,
      NOW,
      rates,
    )
    expect(reduced.animations).toEqual([])
    expect(reduced.notes.join(' ')).toContain('reduced motion')
  })

  it('says what the zoomed-out drawing leaves out and keeps colours and strokes', () => {
    const rates = new Map<string, RateInfo>([['rel_1', { status: 'measured', rate: 10, window: '15m', basis: 'b' }]])
    const edges = [edge({}), edge({ id: 'rel_2', evidence: 'inferred', inferred: true })]
    const full = buildLegend(edges, { ...opts, directionHint: true }, typeColor, (t) => t, nodes, NOW, rates)
    const lod = buildLegend(edges, { ...opts, directionHint: true, lod: true }, typeColor, (t) => t, nodes, NOW, rates)
    expect(lod.colors).toEqual(full.colors)
    expect(lod.strokes).toEqual(full.strokes)
    expect(lod.animations).toEqual([])
    expect(lod.notes[0]).toContain('without arrowheads, labels or animation')
    expect(lod.notes.join(' ')).toContain('measured rate')
    expect(lod.notes.join(' ')).not.toContain('Arrowheads point')
  })

  it('describes custom colours and health colour mode', () => {
    const custom = buildLegend(
      [edge({})],
      { ...opts, typeColors: { depends_on: '#123456' } },
      typeColor,
      () => 'Depends on',
      nodes,
      NOW,
    )
    expect(custom.colors[0]).toMatchObject({ color: '#123456', description: 'custom colour for this view' })
    const health = buildLegend([edge({})], { ...opts, colorMode: 'health' }, typeColor, () => 'x', nodes, NOW)
    expect(health.colors[0]?.label).toContain('Worst endpoint health')
  })
})
