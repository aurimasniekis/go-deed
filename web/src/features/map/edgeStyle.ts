/**
 * Edge styling (ADR-011c). Colour = relationship type (palette from /relationship-types, overridable per view); alternative
 * colour modes: health (worst endpoint), freshness (last seen), evidence and source (the primary asserting source, from a
 * stable per-source palette). Stroke encodes evidence: solid for
 * declared/structural (observed: solid, thinner), dashed for inferred, dotted for stale, double for a cross-project
 * bridge, faded for unknown. Traffic overlays (POST /telemetry/edge-rates and the telemetry stream): width is the
 * log-scaled measured rate and the dash flow animates — only for `measured` rates; `inferred` rates are labelled with
 * their note and never animated; `unavailable` rates change nothing. The legend is generated from what is on screen.
 */
import { evidenceMeta, freshnessMeta, healthMeta, isStaleRelation } from '@/lib/status'
import { sourceLabel, type MapEdge, type MapNode } from './types'

export type ColorMode = 'type' | 'health' | 'freshness' | 'evidence' | 'source'
export type Routing = 'bezier' | 'smoothstep' | 'straight'
export type StrokeKind = 'solid' | 'observed' | 'dashed' | 'dotted' | 'double' | 'faded'

export interface EdgeStyleOptions {
  colorMode: ColorMode
  /** Type → colour overrides (user edits), falling back to the relationship type's colour. */
  typeColors: Record<string, string>
  /** Source ID → colour (see sourcePalette); sources missing from it fall back to their hash slot. */
  sourceColors?: Map<string, string>
  /** Project names for source legend entries (every project has its own system sources, e.g. "API declarations"). */
  projectName?: (id: string) => string
  labels: boolean
  directionHint: boolean
  /** Traffic overlay switched on. */
  traffic: boolean
  reducedMotion: boolean
  /** Level-of-detail drawing (zoomed out): straight lines without arrowheads, labels or animation. Legend only. */
  lod?: boolean
}

/** A traffic measurement for one relationship (subset of the API's EdgeRate). */
export interface RateInfo {
  status: string
  rate?: number
  unit?: string
  window: string
  basis: string
  note?: string
  measured_at?: string
}

export interface EdgeVisual {
  color: string
  stroke: StrokeKind
  width: number
  dash?: string
  opacity: number
  /** Direction hint (marching dashes, not traffic). */
  animated: boolean
  /** Traffic flow animation (measured rates only) and its period in seconds (faster = higher rate). */
  traffic: boolean
  flowSeconds: number
  /** Rate text for the mid-edge label ("12.4 req/s", "~3.1 msg/s inferred"). */
  rateLabel?: string
  rateStatus?: 'measured' | 'inferred' | 'stale'
  /** Short reason list for tooltips ("inferred", "stale", "cross-project", rate notes). */
  notes: string[]
  /** Legend category of the colour. */
  colorKey: string
  colorLabel: string
}

export const FALLBACK_TYPE_COLOR = '#868e96'

export const STATUS_COLORS: Record<string, string> = {
  ok: '#2f9e44',
  fresh: '#2f9e44',
  warning: '#e8a400',
  aging: '#e8a400',
  failed: '#e03131',
  stale: '#e03131',
  unknown: '#868e96',
}

export const EVIDENCE_COLORS: Record<string, string> = {
  declared: '#4263eb',
  structural: '#0ca678',
  observed: '#1c7ed6',
  inferred: '#e8590c',
  unknown: '#868e96',
}

/**
 * Source colours: 12 hues at two lightness steps (OKLCH L 0.60 and 0.55), each at least 3:1 against the canvas in both
 * themes (WCAG 1.4.11; asserted in edgeStyle.test.ts). Status colours are not reused. Ordered so neighbouring slots
 * are far apart on the hue wheel (150° steps, alternating lightness): a source moved to the next free slot does not
 * end up with a look-alike of the colour it collided with.
 */
export const SOURCE_PALETTE = [
  '#c5547c',
  '#138282',
  '#25984d',
  '#ac4785',
  '#8f68cb',
  '#478416',
  '#9c7c0b',
  '#6b61c4',
  '#0b89c8',
  '#986603',
  '#ca5747',
  '#127d9d',
  '#149483',
  '#b84455',
  '#b05baa',
  '#038660',
  '#768b09',
  '#9252ad',
  '#5e78d9',
  '#7c7406',
  '#bb6802',
  '#2971c6',
  '#1590a1',
  '#b64e10',
] as const

/** Bundled relationships whose primary sources differ. */
export const MIXED_SOURCE_COLOR = '#8a7f72'

/** FNV-1a: a stable slot for a source ID. */
function hashSlot(id: string): number {
  let h = 0x811c9dc5
  for (let i = 0; i < id.length; i++) {
    h ^= id.charCodeAt(i)
    h = Math.imul(h, 0x01000193)
  }
  return (h >>> 0) % SOURCE_PALETTE.length
}

/**
 * Assigns palette colours to sources: each takes its hash slot, or the next free one when an older source (IDs sort by
 * creation time) holds it. Colours stay put across reloads, and a new source never recolours an existing one; only
 * beyond 24 sources do colours repeat (the legend and tooltips still name each source).
 */
export function sourcePalette(ids: Iterable<string>): Map<string, string> {
  const out = new Map<string, string>()
  const taken = new Set<number>()
  for (const id of [...new Set(ids)].sort()) {
    let slot = hashSlot(id)
    if (taken.size < SOURCE_PALETTE.length) {
      while (taken.has(slot)) slot = (slot + 1) % SOURCE_PALETTE.length
    }
    taken.add(slot)
    out.set(id, SOURCE_PALETTE[slot] ?? FALLBACK_TYPE_COLOR)
  }
  return out
}

export function sourceColor(id: string, palette?: Map<string, string>): string {
  return palette?.get(id) ?? SOURCE_PALETTE[hashSlot(id)] ?? FALLBACK_TYPE_COLOR
}

const LAYER_LABEL: Record<string, string> = {
  declared: 'declared',
  imported: 'imported',
  discovered: 'discovered',
  observation: 'observation',
  annotation: 'annotation',
  override: 'override',
}

const healthRank: Record<string, number> = { failed: 3, warning: 2, ok: 1, unknown: 0 }

function edgeFreshness(e: MapEdge, now: number): string {
  if (e.evidence === 'declared' || e.evidence === 'structural') return 'fresh'
  if (!e.lastSeenAt) return 'unknown'
  const age = now - Date.parse(e.lastSeenAt)
  if (!Number.isFinite(age)) return 'unknown'
  if (age > 7 * 86_400_000) return 'stale'
  if (age > 86_400_000) return 'aging'
  return 'fresh'
}

export function strokeOf(e: MapEdge, now = Date.now()): StrokeKind {
  if (e.crossProject) return 'double'
  if (isStaleRelation(e.evidence, e.lastSeenAt, now)) return 'dotted'
  if (e.inferred || e.evidence === 'inferred') return 'dashed'
  if (e.evidence === 'declared' || e.evidence === 'structural') return 'solid'
  if (e.evidence === 'observed') return 'observed'
  return 'faded'
}

export const STROKE_DASH: Record<StrokeKind, string | undefined> = {
  solid: undefined,
  observed: undefined,
  dashed: '7 5',
  dotted: '1.5 4.5',
  double: undefined,
  faded: undefined,
}

export const STROKE_LABEL: Record<StrokeKind, { label: string; description: string }> = {
  solid: { label: 'Solid', description: 'Declared or structural relationship (a fact).' },
  observed: { label: 'Thin solid', description: 'Observed at runtime and seen recently.' },
  dashed: {
    label: 'Dashed',
    description: 'Inferred from indirect evidence; hover or open the inspector for the evidence.',
  },
  dotted: { label: 'Dotted', description: 'Stale: an observed relationship not seen for more than 7 days.' },
  double: { label: 'Double', description: 'Crosses projects through a bridge.' },
  faded: { label: 'Faded', description: 'Unknown evidence class.' },
}

export function formatRate(rate: number, unit = ''): string {
  const abs = Math.abs(rate)
  const s = abs >= 100 ? rate.toFixed(0) : abs >= 10 ? rate.toFixed(1) : rate.toFixed(2)
  return unit ? `${s} ${unit}` : s
}

/** Log-scaled stroke width for a measured rate (1 → ~2 px, 1,000 → ~6.5 px). */
export function rateWidth(rate: number): number {
  return Math.min(8, 1.5 + Math.log10(Math.max(0, rate) + 1) * 1.7)
}

/** Flow period: faster for higher rates, clamped to stay readable (0.6–4 s). */
export function flowSeconds(rate: number): number {
  return Math.max(0.6, Math.min(4, 4 / (1 + Math.log10(Math.max(0, rate) + 1))))
}

export function edgeVisual(
  e: MapEdge,
  opts: EdgeStyleOptions,
  typeColor: (type: string) => string | undefined,
  nodes: Map<string, MapNode>,
  now = Date.now(),
  rate?: RateInfo,
): EdgeVisual {
  const stroke = strokeOf(e, now)
  let color = FALLBACK_TYPE_COLOR
  let colorKey = e.type
  let colorLabel = e.type
  switch (opts.colorMode) {
    case 'type':
      color = opts.typeColors[e.type] ?? typeColor(e.type) ?? FALLBACK_TYPE_COLOR
      break
    case 'health': {
      const a = nodes.get(e.from)?.health ?? 'unknown'
      const b = nodes.get(e.to)?.health ?? 'unknown'
      const worst = (healthRank[a] ?? 0) >= (healthRank[b] ?? 0) ? a : b
      color = STATUS_COLORS[worst] ?? FALLBACK_TYPE_COLOR
      colorKey = `health:${worst}`
      colorLabel = `Worst endpoint health: ${healthMeta(worst).label}`
      break
    }
    case 'freshness': {
      const f = edgeFreshness(e, now)
      color = STATUS_COLORS[f] ?? FALLBACK_TYPE_COLOR
      colorKey = `freshness:${f}`
      colorLabel = `Last seen: ${freshnessMeta(f).label}`
      break
    }
    case 'evidence':
      color = EVIDENCE_COLORS[e.evidence] ?? FALLBACK_TYPE_COLOR
      colorKey = `evidence:${e.evidence}`
      colorLabel = `Evidence: ${evidenceMeta(e.evidence).label}`
      break
    case 'source': {
      const primary = e.primarySource ? e.sources.find((s) => s.id === e.primarySource) : undefined
      if (primary) {
        color = sourceColor(primary.id, opts.sourceColors)
        colorKey = `source:${primary.id}`
        colorLabel = sourceLabel(primary)
      } else if (e.sources.length > 0) {
        color = MIXED_SOURCE_COLOR
        colorKey = 'source:mixed'
        colorLabel = 'Several sources (bundled relationships with different primary sources)'
      } else {
        colorKey = 'source:none'
        colorLabel = 'No source you can see'
      }
      break
    }
  }
  const notes: string[] = []
  if (stroke === 'dashed') notes.push('inferred')
  if (stroke === 'dotted') notes.push('stale (not seen for 7+ days)')
  if (e.crossProject) notes.push('cross-project bridge')
  if (e.count > 1) notes.push(`${e.count} relationships bundled`)
  let width = stroke === 'observed' ? 1.4 : stroke === 'double' ? 4.5 : 2 + Math.min(3, Math.log2(e.count))
  let traffic = false
  let flow = 0
  let rateLabel: string | undefined
  let rateStatus: EdgeVisual['rateStatus']
  if (
    opts.traffic &&
    rate?.rate !== undefined &&
    (rate.status === 'measured' || rate.status === 'inferred' || rate.status === 'stale')
  ) {
    rateStatus = rate.status
    if (rate.status === 'measured') {
      width = Math.max(width, rateWidth(rate.rate))
      traffic = !opts.reducedMotion && rate.rate > 0
      flow = flowSeconds(rate.rate)
      rateLabel = formatRate(rate.rate, rate.unit)
      notes.push(`${rateLabel} measured (sampled, ${rate.window} window)`)
    } else {
      rateLabel = `~${formatRate(rate.rate, rate.unit)} ${rate.status}`
      notes.push(`${rateLabel}${rate.note ? `: ${rate.note}` : ''}`)
    }
  }
  const animated = opts.directionHint && !opts.reducedMotion && stroke !== 'dotted' && !traffic
  return {
    color,
    stroke,
    width,
    ...(STROKE_DASH[stroke] ? { dash: STROKE_DASH[stroke] } : {}),
    opacity: stroke === 'faded' ? 0.4 : 1,
    animated,
    traffic,
    flowSeconds: flow,
    ...(rateLabel ? { rateLabel } : {}),
    ...(rateStatus ? { rateStatus } : {}),
    notes,
    colorKey,
    colorLabel,
  }
}

export interface LegendColor {
  key: string
  label: string
  color: string
  description?: string
}

export interface Legend {
  colors: LegendColor[]
  strokes: { kind: StrokeKind; label: string; description: string }[]
  animations: { label: string; description: string }[]
  notes: string[]
}

/**
 * Builds the legend from what is on screen: every colour, stroke and animation in use is explained, nothing else.
 */
export function buildLegend(
  edges: MapEdge[],
  opts: EdgeStyleOptions,
  typeColor: (type: string) => string | undefined,
  typeTitle: (type: string) => string,
  nodes: Map<string, MapNode>,
  now = Date.now(),
  rates = new Map<string, RateInfo>(),
): Legend {
  const colors = new Map<string, LegendColor>()
  const strokes = new Set<StrokeKind>()
  let animated = false
  let traffic = false
  let measuredStatic = false
  let inferred = false
  let bundled = false
  const windows = new Set<string>()
  for (const e of edges) {
    const rate = rates.get(e.id)
    const v = edgeVisual(e, opts, typeColor, nodes, now, rate)
    strokes.add(v.stroke)
    animated ||= v.animated
    traffic ||= v.traffic
    if (v.rateStatus === 'measured') {
      windows.add(rate?.window ?? '')
      if (!v.traffic) measuredStatic = true
    }
    inferred ||= v.rateStatus === 'inferred' || v.rateStatus === 'stale'
    bundled ||= e.count > 1
    if (!colors.has(v.colorKey)) {
      const primary =
        opts.colorMode === 'source' && e.primarySource ? e.sources.find((s) => s.id === e.primarySource) : undefined
      colors.set(v.colorKey, {
        key: v.colorKey,
        label: opts.colorMode === 'type' ? typeTitle(e.type) : v.colorLabel,
        color: v.color,
        ...(opts.colorMode === 'type' && opts.typeColors[e.type] ? { description: 'custom colour for this view' } : {}),
        ...(primary
          ? {
              description: `${LAYER_LABEL[primary.layer] ?? primary.layer} layer, ${opts.projectName?.(primary.projectId) ?? primary.projectId}`,
            }
          : {}),
      })
    }
  }
  const order: StrokeKind[] = ['solid', 'observed', 'dashed', 'dotted', 'double', 'faded']
  const strokeList = order.filter((s) => strokes.has(s)).map((kind) => ({ kind, ...STROKE_LABEL[kind] }))
  // Neutral source entries (several / none) go last.
  const rank = (c: LegendColor) => (c.key === 'source:mixed' ? 1 : c.key === 'source:none' ? 2 : 0)
  const colorList = [...colors.values()].sort((a, b) => rank(a) - rank(b) || a.label.localeCompare(b.label))
  const sourceNote =
    opts.colorMode === 'source' && edges.length > 0
      ? 'Colour: the primary source (highest-precedence layer, then the oldest claim). Hover or select a relationship for every source asserting it.'
      : undefined
  if (opts.lod) {
    // The simplified drawing keeps colours, strokes and rate widths, and drops everything else: say so.
    const notes = [
      'Zoomed out: relationships are straight lines without arrowheads, labels or animation; zoom in to see them.',
    ]
    if (measuredStatic || traffic) notes.push('Wider lines carry a measured rate.')
    if (bundled) notes.push('Thicker lines bundle several relationships.')
    if (sourceNote) notes.push(sourceNote)
    return { colors: colorList, strokes: strokeList, animations: [], notes }
  }
  const animations: Legend['animations'] = []
  const win = [...windows].filter(Boolean).join(', ')
  if (traffic) {
    animations.push({
      label: 'Moving light dots (traffic)',
      description: `Measured rate, sampled over a ${win || '15m'} window; faster flow = higher rate. Line width is the log-scaled rate.`,
    })
  }
  if (animated) {
    animations.push({
      label: 'Marching dashes (direction hint)',
      description: 'Shows the direction of each relationship. Not traffic.',
    })
  }
  const notes: string[] = ['Arrowheads point from the source of a relationship to its target.']
  if (sourceNote) notes.push(sourceNote)
  if (measuredStatic)
    notes.push('Wider lines with a rate label carry a measured rate; animation is off (reduced motion).')
  if (inferred)
    notes.push(
      '“~ … inferred” labels are derived rates (e.g. a subject’s publish rate for a subscriber) and are never animated.',
    )
  if (bundled) notes.push('Thicker lines bundle several relationships (count in the tooltip).')
  if (opts.reducedMotion && (opts.directionHint || opts.traffic))
    notes.push('Animations are off because your system asks for reduced motion.')
  return { colors: colorList, strokes: strokeList, animations, notes }
}
