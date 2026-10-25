import {
  BaseEdge,
  EdgeLabelRenderer,
  getBezierPath,
  getSmoothStepPath,
  getStraightPath,
  Position,
  useInternalNode,
  ViewportPortal,
  type Edge,
  type EdgeProps,
  type InternalNode,
  type Node,
} from '@xyflow/react'
import { memo, useMemo, type CSSProperties } from 'react'
import type { EdgeVisual, Routing } from './edgeStyle'
import { useDrawnEdges } from './lodSwap'
import type { MapEdge } from './types'

export interface DeedEdgeData extends Record<string, unknown> {
  edge: MapEdge
  visual: EdgeVisual
  routing: Routing
  floating: boolean
  label: string
  showLabel: boolean
  dimmed: boolean
  title: string
}

export type DeedFlowEdge = Edge<DeedEdgeData, 'deed'>

function center(n: InternalNode) {
  const w = n.measured.width ?? 0
  const h = n.measured.height ?? 0
  return { x: n.internals.positionAbsolute.x + w / 2, y: n.internals.positionAbsolute.y + h / 2, w, h }
}

/** Where the line from a node's centre towards another point leaves the node's rectangle, and on which side. */
function boundaryPoint(n: InternalNode, toward: { x: number; y: number }): { x: number; y: number; pos: Position } {
  const c = center(n)
  const dx = toward.x - c.x
  const dy = toward.y - c.y
  if (dx === 0 && dy === 0) return { x: c.x, y: c.y, pos: Position.Right }
  const sx = c.w / 2 / Math.abs(dx || 1e-9)
  const sy = c.h / 2 / Math.abs(dy || 1e-9)
  const s = Math.min(sx, sy)
  const pos = sx < sy ? (dx > 0 ? Position.Right : Position.Left) : dy > 0 ? Position.Bottom : Position.Top
  return { x: c.x + dx * s, y: c.y + dy * s, pos }
}

/**
 * The map's edge: colour by relationship type (or health/freshness/evidence), stroke by evidence, arrowheads for
 * direction, optional mid-edge type labels, bezier/smoothstep/straight routing. Force and radial layouts use floating
 * endpoints (the line leaves each card where it points); layered layouts use the direction's handles.
 */
export const DeedEdge = memo(function DeedEdge(props: EdgeProps<DeedFlowEdge>) {
  const { source, target, data, markerEnd, selected } = props
  const s = useInternalNode(source)
  const t = useInternalNode(target)
  if (!data) return null
  let sx = props.sourceX
  let sy = props.sourceY
  let tx = props.targetX
  let ty = props.targetY
  let sp = props.sourcePosition
  let tp = props.targetPosition
  if (data.floating && s && t) {
    const a = boundaryPoint(s, center(t))
    const b = boundaryPoint(t, center(s))
    ;[sx, sy, sp, tx, ty, tp] = [a.x, a.y, a.pos, b.x, b.y, b.pos]
  }
  const args = { sourceX: sx, sourceY: sy, targetX: tx, targetY: ty, sourcePosition: sp, targetPosition: tp }
  const [path, lx, ly] =
    data.routing === 'straight'
      ? getStraightPath(args)
      : data.routing === 'smoothstep'
        ? getSmoothStepPath({ ...args, borderRadius: 8 })
        : getBezierPath(args)
  const v = data.visual
  const opacity = data.dimmed ? 0.15 : v.opacity
  return (
    <>
      <g className={v.animated ? 'deed-edge-animated' : undefined} style={{ opacity }}>
        <title>{data.title}</title>
        <BaseEdge
          id={props.id}
          path={path}
          markerEnd={markerEnd}
          interactionWidth={14}
          style={{
            stroke: v.color,
            strokeWidth: selected ? v.width + 1.5 : v.width,
            ...(v.dash ? { strokeDasharray: v.dash } : {}),
            strokeLinecap: v.stroke === 'dotted' ? 'round' : 'butt',
          }}
        />
        {v.stroke === 'double' && (
          // A thin canvas-coloured core turns the wide stroke into a double line (cross-project bridge).
          <path
            d={path}
            fill="none"
            style={{ stroke: 'var(--deed-canvas)', strokeWidth: 1.6, pointerEvents: 'none' }}
          />
        )}
        {v.traffic && (
          // Traffic flow: light dots travel along the (unchanged) evidence stroke; speed follows the measured rate.
          <path
            d={path}
            fill="none"
            className="deed-flow"
            style={
              {
                strokeWidth: Math.max(2, v.width * 0.55),
                '--deed-flow': `${v.flowSeconds.toFixed(2)}s`,
              } as CSSProperties
            }
          />
        )}
      </g>
      {(data.showLabel || v.rateLabel) && !data.dimmed && (
        <EdgeLabelRenderer>
          <div
            className="deed-edge-label nodrag nopan"
            data-rate={v.rateStatus}
            style={{ transform: `translate(-50%, -50%) translate(${lx}px, ${ly}px)` }}
          >
            {data.showLabel && `${data.label}${data.edge.count > 1 ? ` ×${data.edge.count}` : ''}`}
            {data.showLabel && v.rateLabel && ' · '}
            {v.rateLabel}
          </div>
        </EdgeLabelRenderer>
      )}
    </>
  )
})

export const edgeTypes = { deed: DeedEdge }

interface LodStroke {
  color: string
  width: number
  dash?: string
  opacity: number
  double: boolean
  d: string[]
}

/**
 * Level-of-detail edges (see LOD in nodes.tsx): every relationship not drawn as a full edge right now (lodSwap.ts) as a
 * straight centre-to-centre line, batched into one path per colour, stroke and opacity inside a single SVG in the
 * viewport — no per-edge components, markers, labels, animation or hit areas, so panning costs no React work. Colour
 * and evidence stroke stay as in the full drawing (the legend still applies); strokes keep their screen width at any
 * zoom. Drawn under the nodes.
 */
export const LodEdges = memo(function LodEdges({ edges: all, nodes }: { edges: DeedFlowEdge[]; nodes: Node[] }) {
  const drawn = useDrawnEdges()
  const edges = useMemo(() => (drawn === 'all' ? [] : all.filter((e) => !drawn.has(e.id))), [all, drawn])
  const strokes = useMemo(() => {
    const centre = new Map<string, string>()
    for (const n of nodes) {
      if (n.type === 'group') continue
      centre.set(
        n.id,
        `${Math.round(n.position.x + (n.width ?? 0) / 2)} ${Math.round(n.position.y + (n.height ?? 0) / 2)}`,
      )
    }
    const out = new Map<string, LodStroke>()
    for (const e of edges) {
      const v = e.data?.visual
      const a = centre.get(e.source)
      const b = centre.get(e.target)
      if (!v || !a || !b) continue
      const opacity = e.data?.dimmed ? 0.15 : v.opacity
      const width = e.selected ? v.width + 1.5 : v.width
      const double = v.stroke === 'double'
      const key = `${v.color}|${String(width)}|${v.dash ?? ''}|${String(opacity)}|${String(double)}`
      let s = out.get(key)
      if (!s) {
        s = { color: v.color, width, opacity, double, d: [], ...(v.dash ? { dash: v.dash } : {}) }
        out.set(key, s)
      }
      s.d.push(`M${a}L${b}`)
    }
    return [...out.entries()].map(([key, s]) => ({ key, ...s, path: s.d.join('') }))
  }, [edges, nodes])
  if (strokes.length === 0) return null
  return (
    <ViewportPortal>
      <svg className="deed-lod-edges" aria-hidden data-testid="map-lod-edges">
        {strokes.map((s) => (
          <g key={s.key} opacity={s.opacity}>
            <path
              d={s.path}
              fill="none"
              stroke={s.color}
              strokeWidth={s.width}
              strokeDasharray={s.dash}
              vectorEffect="non-scaling-stroke"
            />
            {s.double && (
              <path
                d={s.path}
                fill="none"
                stroke="var(--deed-canvas)"
                strokeWidth={1.6}
                vectorEffect="non-scaling-stroke"
              />
            )}
          </g>
        ))}
      </svg>
    </ViewportPortal>
  )
})
