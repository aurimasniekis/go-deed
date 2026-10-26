import { ButtonLink } from '@/components/links'
import {
  ActionIcon,
  Alert,
  Anchor,
  Badge,
  Box,
  Breadcrumbs,
  Button,
  Group,
  Loader,
  NumberInput,
  SegmentedControl,
  Select,
  Text,
  Tooltip,
  useComputedColorScheme,
} from '@mantine/core'
import { useReducedMotion } from '@mantine/hooks'
import { spotlight } from '@mantine/spotlight'
import {
  IconAdjustmentsHorizontal,
  IconArrowsMaximize,
  IconInfoCircle,
  IconListDetails,
  IconMap2,
  IconMask,
  IconRoute,
  IconSearch,
} from '@tabler/icons-react'
import { useQuery } from '@tanstack/react-query'
import { useSelector } from '@tanstack/react-store'
import {
  Background,
  BackgroundVariant,
  Controls,
  MarkerType,
  MiniMap,
  Panel,
  Position,
  ReactFlow,
  ReactFlowProvider,
  getViewportForBounds,
  useNodesState,
  useReactFlow,
  useStoreApi,
  type Edge,
  type Node,
  type FitViewOptions,
  type ReactFlowProps,
} from '@xyflow/react'
import { startTransition, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { isInstallationAdmin, useMe } from '@/api/auth'
import { api, unwrap, type View } from '@/api/client'
import { keys, useKinds, useProjects, useRelTypes, useSources } from '@/api/queries'
import { onLiveChanges } from '@/api/useLive'
import { EmptyState, ErrorState } from '@/components/States'
import { SavedViewsMenu } from '@/features/views/SavedViews'
import { setMasked, useMasked, useProjectFilter, useTitle } from '@/lib/appStore'
import { alias, maskProject } from '@/lib/mask'
import { humanize, plural } from '@/lib/format'
import { compact, decodeList, encodeList, validateMapSearch, type MapSearch } from '@/lib/urlState'
import { flushSync } from 'react-dom'
import { buildLegend, edgeVisual, sourceColor, sourcePalette } from './edgeStyle'
import { edgeTypes, LodEdges, type DeedFlowEdge } from './edges'
import { MapContextMenu, type ContextMenuActions, type MenuTarget } from './ContextMenu'
import { EdgeInspector, NodeInspector, type InspectorActions } from './Inspector'
import { maskModel } from './mask'
import { layoutCache, runLayout, warmLayout } from './layout'
import type { LayoutResult } from './layoutTypes'
import { MapLegend } from './Legend'
import {
  ALL_DETAIL,
  DetailContext,
  DetailStore,
  lodStep,
  NO_DETAIL,
  settled,
  STEP_BUDGET,
  useDrawnEdges,
} from './lodSwap'
import {
  mapStore,
  pinPosition,
  setPanel,
  setPinned,
  setSelected,
  setStyle,
  toggleCollapsed,
  type MapStyle,
} from './mapStore'
import {
  applyFilters,
  clusterModel,
  enforceBudget,
  groupExpansions,
  hashString,
  neighbours,
  nextInDirection,
  topologySignature,
  type MapFilters,
} from './model'
import { LOD, NODE_SIZE, nodeTypes, type EntityNodeData, type GroupNodeData } from './nodes'
import { OptionsDrawer } from './OptionsDrawer'
import { PathFinder } from './PathFinder'
import { sourceLabel, type MapEdge, type MapNode } from './types'
import { kindAccent } from '@/theme/chart'
import { useEdgeRates } from './useEdgeRates'
import { useMapData } from './useMapData'
import './map.css'

declare global {
  interface Window {
    __deedMap?: {
      nodes: number
      edges: number
      layoutMs: number
      /** The layout came from the in-memory cache (a revisit), not the worker. */
      layoutCached: boolean
      fetchToRenderMs: number
      signature: string
      renders: number
    }
  }
}

const handlePos: Record<string, { source: Position; target: Position }> = {
  RIGHT: { source: Position.Right, target: Position.Left },
  LEFT: { source: Position.Left, target: Position.Right },
  DOWN: { source: Position.Bottom, target: Position.Top },
  UP: { source: Position.Top, target: Position.Bottom },
}

function sizeOf(n: MapNode, compactCards: boolean) {
  if (n.cluster) return NODE_SIZE.cluster
  return compactCards ? NODE_SIZE.compact : NODE_SIZE.detailed
}

interface Patch {
  health: string
  freshness: string
  findings: number
}

const NO_EDGES: Edge[] = []

/**
 * React Flow with the edges the current drawing shows in full (lodSwap.ts): during a level-of-detail swap only this
 * component re-renders per step, with the same props apart from the edge list.
 */
function DetailFlow({ allEdges, ...props }: ReactFlowProps & { allEdges: Edge[] }) {
  const ids = useDrawnEdges()
  const edges = useMemo(
    () => (ids === 'all' ? allEdges : ids.size === 0 ? NO_EDGES : allEdges.filter((e) => ids.has(e.id))),
    [allEdges, ids],
  )
  return <ReactFlow {...props} edges={edges} />
}

/** Runs `fn` once the browser is idle (after 500 ms where there is no requestIdleCallback, as in Safari); returns a cancel. */
function whenIdle(fn: () => void): () => void {
  if (!('requestIdleCallback' in window)) {
    const t = setTimeout(fn, 500)
    return () => {
      clearTimeout(t)
    }
  }
  const id = requestIdleCallback(fn, { timeout: 2000 })
  return () => {
    cancelIdleCallback(id)
  }
}

/** Fit padding that keeps nodes clear of the legend panel (bottom-left) when it is open. */
function fitPadding(): NonNullable<FitViewOptions['padding']> {
  return mapStore.state.legendOpen
    ? { top: '48px', right: '72px', bottom: '48px', left: '290px' }
    : { x: '48px', y: '48px' }
}

/** The asserting sources for an edge's tooltip and accessible name. */
function sourcesText(e: MapEdge, projectName: (id: string) => string): string {
  if (e.sources.length === 0) return ''
  const names = e.sources.map(
    (s) =>
      `${sourceLabel(s)} (${[projectName(s.projectId), ...(s.id === e.primarySource ? ['primary'] : []), ...(e.count > 1 ? [`${s.relations}/${e.count}`] : [])].join(', ')})`,
  )
  return ` · sources: ${names.join(', ')}`
}

function MapInner({ search, setSearch }: { search: MapSearch; setSearch: (s: MapSearch, replace?: boolean) => void }) {
  useTitle('Map')
  const rf = useReactFlow()
  const scheme = useComputedColorScheme('light')
  const reduced = useReducedMotion()
  const me = useMe().data
  const globalProjects = useProjectFilter()
  const relTypes = useRelTypes()
  const kinds = useKinds()
  const projects = useProjects()
  const style = useSelector(mapStore, (s) => s.style)
  const selected = useSelector(mapStore, (s) => s.selected)
  const collapsed = useSelector(mapStore, (s) => s.collapsed)
  const legendOpen = useSelector(mapStore, (s) => s.legendOpen)
  const optionsOpen = useSelector(mapStore, (s) => s.optionsOpen)
  const pathOpen = useSelector(mapStore, (s) => s.pathOpen)
  const [selectedEdge, setSelectedEdge] = useState<string | null>(null)
  const [menu, setMenu] = useState<MenuTarget | null>(null)
  const menuFromKeyAt = useRef(0)
  const [patches, setPatches] = useState<Map<string, Patch>>(new Map())
  const [layout, setLayout] = useState<(LayoutResult & { sig: string; cached?: boolean }) | null>(null)
  const [layoutError, setLayoutError] = useState<{ sig: string; message: string } | null>(null)
  const renders = useRef(0)
  useEffect(warmLayout, [])
  // Zoomed out past the level-of-detail threshold (with hysteresis: enter below LOD.enter, leave above LOD.leave).
  // Subscribing to the store directly re-renders the map only when the threshold is crossed, not on every zoom step.
  const flowStore = useStoreApi()
  const [zoomedOut, setZoomedOut] = useState(false)
  // What is drawn in detail right now; it follows the zoom over a few frames (lodSwap.ts).
  const [detail] = useState(() => new DetailStore())
  // The legend and status bar describe the simplified drawing while any of it is on screen. They follow the swap in a
  // transition, so re-rendering the page never lengthens a swap step.
  const [complete, setComplete] = useState(true)
  useEffect(
    () =>
      detail.subscribe(() => {
        const c = settled(detail.drawn, true)
        startTransition(() => setComplete(c))
      }),
    [detail],
  )
  useEffect(
    () =>
      flowStore.subscribe((s) => {
        const z = s.transform[2]
        // A transition: swapping drawings must not block the wheel or drag event that crossed the threshold.
        startTransition(() => setZoomedOut((prev) => (prev ? z < LOD.leave : z < LOD.enter)))
      }),
    [flowStore],
  )
  // Staleness (7-day window) is evaluated against a clock that ticks every minute, not on every render.
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 60_000)
    return () => clearInterval(t)
  }, [])

  const data = useMapData(search, globalProjects, style.colorMode === 'source')
  const sources = useSources()
  const kindMap = useMemo(() => new Map((kinds.data ?? []).map((k) => [k.name, k])), [kinds.data])
  const typeMap = useMemo(() => new Map((relTypes.data ?? []).map((t) => [t.name, t])), [relTypes.data])
  const masked = useMasked()
  const projectName = useCallback(
    (id: string) => {
      if (masked) return maskProject(id)
      const p = projects.data?.find((x) => x.id === id)
      return p ? p.display_name || p.name : id
    },
    [projects.data, masked],
  )
  const typeTitle = useCallback((t: string) => typeMap.get(t)?.title ?? humanize(t), [typeMap])
  const typeColor = useCallback((t: string) => typeMap.get(t)?.color, [typeMap])

  const filters: MapFilters = useMemo(
    () => ({
      kind: decodeList(search.kind),
      type: decodeList(search.type),
      project: decodeList(search.project),
      environment: decodeList(search.environment),
      owner: decodeList(search.owner),
      health: decodeList(search.health),
      freshness: decodeList(search.freshness),
      evidence: decodeList(search.evidence),
      sourceIds: data.sourceIds,
      stale: search.stale !== false,
    }),
    [search, data.sourceIds],
  )
  const clustering = search.cluster ?? 'none'
  const algorithm = search.layout ?? 'layered'
  const rankdir = search.rankdir ?? 'RIGHT'
  const compactCards = style.nodeMode === 'compact'

  const view = useMemo(() => {
    const filtered = applyFilters(data.model, filters)
    // Mask mode replaces names before clustering, so host and project group labels are aliases too.
    const clustered = clusterModel(masked ? maskModel(filtered) : filtered, clustering, new Set(collapsed), projectName)
    // Members of an expanded cluster stay together in a container (collapsing it undoes the expansion).
    const expanded = groupExpansions(clustered.model)
    const budgeted = enforceBudget(expanded.model)
    const groups = [
      ...clustered.groups,
      ...expanded.groups.map((g) =>
        g.expansion?.startsWith('project|') ? { ...g, label: projectName(g.expansion.slice('project|'.length)) } : g,
      ),
    ].filter((g) => !g.collapsed && g.members.some((m) => budgeted.nodes.has(m)))
    return { model: budgeted, groups }
  }, [data.model, filters, clustering, collapsed, projectName, masked])

  // Source colours are assigned over every source the user can see (oldest first), so a colour does not depend on
  // what happens to be on screen.
  const sourceColors = useMemo(() => {
    const ids = new Set((sources.data ?? []).map((s) => s.id))
    for (const e of view.model.edges.values()) for (const s of e.sources) ids.add(s.id)
    return sourcePalette(ids)
  }, [sources.data, view.model])
  const colorOfSource = useCallback((id: string) => sourceColor(id, sourceColors), [sourceColors])

  const signature = useMemo(
    () =>
      topologySignature(
        view.model,
        `${algorithm}|${rankdir}|${style.nodeMode}|${view.groups.map((g) => g.key).join(',')}`,
      ),
    [view, algorithm, rankdir, style.nodeMode],
  )

  // Layout runs only when the topology signature changes (never for observation-only patches).
  useEffect(() => {
    if (view.model.nodes.size === 0) return
    let cancelled = false
    const nodes = [...view.model.nodes.values()].map((n) => ({
      id: n.id,
      ...sizeOf(n, compactCards),
      ...(n.group ? { group: n.group } : {}),
    }))
    const edges = [...view.model.edges.values()].map((e) => ({ id: e.id, source: e.from, target: e.to }))
    const pinned = Object.fromEntries(Object.entries(mapStore.state.pinned).filter(([id]) => view.model.nodes.has(id)))
    // Revisits (back/forward, re-applied views) reuse the positions computed before.
    const cacheKey = `${search.view ?? ''}|${signature}|${hashString(JSON.stringify(pinned))}`
    const hit = layoutCache.get(cacheKey)
    const pending = hit
      ? Promise.resolve({ ...hit, cached: true })
      : runLayout({
          algorithm,
          direction: rankdir,
          nodes,
          edges,
          groups: view.groups.map((g) => ({ id: g.key, label: g.label })),
          focus: [...view.model.nodes.values()].filter((n) => n.root).map((n) => n.id),
          pinned,
        }).then((r) => {
          layoutCache.set(cacheKey, r)
          return r
        })
    pending
      .then((r) => {
        if (cancelled) return
        try {
          performance.mark('deed-map-layout')
        } catch {
          // diagnostics only
        }
        // Large graphs: jump to the fitted viewport before the first render, so a drawing that will be shown zoomed
        // out mounts as level-of-detail blocks straight away instead of as cards first (the animated fit follows). A
        // new drawing starts complete; only later zooming swaps it in steps.
        let blocks = false
        if (nodes.length >= LOD.minNodes) {
          const { width, height, minZoom } = flowStore.getState()
          let [x0, y0, x1, y1] = [Infinity, Infinity, -Infinity, -Infinity]
          for (const n of nodes) {
            const p = r.positions[n.id] ?? { x: 0, y: 0 }
            ;[x0, y0, x1, y1] = [
              Math.min(x0, p.x),
              Math.min(y0, p.y),
              Math.max(x1, p.x + n.width),
              Math.max(y1, p.y + n.height),
            ]
          }
          if (width > 0 && height > 0 && x1 > x0) {
            const vp = getViewportForBounds(
              { x: x0, y: y0, width: x1 - x0, height: y1 - y0 },
              width,
              height,
              minZoom,
              1.25,
              fitPadding(),
            )
            void rf.setViewport(vp)
            // Urgent, in the same batch as the layout (the store subscription only schedules a transition).
            blocks = vp.zoom < LOD.enter
            setZoomedOut(blocks)
          }
        }
        detail.setPorts(!blocks)
        detail.set(blocks ? NO_DETAIL : ALL_DETAIL)
        setComplete(!blocks)
        setLayout({ ...r, sig: signature })
      })
      .catch((err: unknown) => {
        if (!cancelled) setLayoutError({ sig: signature, message: err instanceof Error ? err.message : String(err) })
      })
    return () => {
      cancelled = true
    }
  }, [signature]) // eslint-disable-line react-hooks/exhaustive-deps

  // Live updates: observation-only entity changes patch health/freshness in place (no refetch, no relayout).
  useEffect(
    () =>
      onLiveChanges((msgs) => {
        const ids = [
          ...new Set(msgs.filter((m) => m.object_type === 'entity' && m.action === 'observed').map((m) => m.object_id)),
        ].filter((id) => view.model.nodes.has(id))
        if (ids.length === 0) return
        void api.GET('/entities', { params: { query: { id: ids.slice(0, 200), limit: 200 } } }).then((r) => {
          if (!r.data) return
          const items = r.data.items
          setPatches((prev) => {
            const next = new Map(prev)
            for (const e of items) {
              const h = e.status.health.state
              next.set(e.metadata.id, {
                health: typeof h === 'string' ? h : 'unknown',
                freshness: e.status.freshness.state,
                findings: Object.values(e.status.findings).reduce((s, n) => s + n, 0),
              })
            }
            return next
          })
        })
      }),
    [view.model.nodes],
  )

  // A selection that is no longer drawn (a cluster that was just expanded) dims nothing.
  const neighbourSet = useMemo(
    () =>
      selected && view.model.nodes.has(selected) ? new Set([selected, ...neighbours(view.model, selected)]) : null,
    [selected, view.model],
  )

  // Traffic overlay: measured edge rates patch edge data only (never part of the topology signature).
  const edgeIds = useMemo(() => [...view.model.edges.keys()], [view.model])
  const traffic = useEdgeRates(edgeIds, style.traffic)

  const big = view.model.nodes.size >= LOD.minNodes
  const lod = zoomedOut && big
  const lodShown = lod || !complete
  // Setting up dragging costs ~15 ms per 2,000 mounted nodes, and dragging a block is never useful: a drawing that
  // opens zoomed out mounts its blocks without it and turns it on once the browser is idle (switching it on or off
  // while zooming would add that cost to a swap step).
  const [dragSig, setDragSig] = useState('')
  useEffect(() => {
    if (layout?.sig !== signature || dragSig === signature) return
    return whenIdle(() => {
      setDragSig(signature)
    })
  }, [layout?.sig, signature, dragSig])
  // Stable (reads the latest search), so group containers are not rebuilt when the URL changes.
  const searchRef = useRef(search)
  searchRef.current = search
  const collapseExpansion = useCallback(
    (key: string) => {
      const cur = searchRef.current
      setSearch(compact({ ...cur, expand: encodeList(decodeList(cur.expand).filter((k) => k !== key)) }), true)
    },
    [setSearch],
  )

  const built = useMemo(() => {
    if (layout?.sig !== signature) return { nodes: [] as Node[], edges: [] as Edge[] }
    const hp =
      algorithm === 'layered'
        ? (handlePos[rankdir] ?? { source: Position.Right, target: Position.Left })
        : { source: Position.Right, target: Position.Left }
    const regionNodes: Node[] = (layout.regions ?? []).map((r, i) => ({
      id: `region:${i}`,
      type: 'region',
      position: { x: r.x, y: r.y },
      data: {},
      style: { width: r.width, height: r.height },
      width: r.width,
      height: r.height,
      selectable: false,
      draggable: false,
      focusable: false,
      zIndex: -2,
    }))
    const groupNodes: Node<GroupNodeData, 'group'>[] = view.groups
      .filter((g) => layout.groups[g.key])
      .map((g) => {
        const b = layout.groups[g.key] as { x: number; y: number; width: number; height: number }
        return {
          id: `group:${g.key}`,
          type: 'group' as const,
          position: { x: b.x, y: b.y },
          data: {
            key: g.key,
            label: g.label,
            count: g.members.length,
            ...(g.expansion ? { onCollapse: () => collapseExpansion(g.expansion ?? '') } : {}),
          },
          style: { width: b.width, height: b.height },
          width: b.width,
          height: b.height,
          selectable: false,
          draggable: false,
          focusable: false,
          zIndex: -1,
          ariaLabel: `Group ${g.label}, ${g.members.length} nodes`,
        }
      })
    const nodes: Node<EntityNodeData>[] = [...view.model.nodes.values()].map((raw) => {
      const p = patches.get(raw.id)
      const n = p ? { ...raw, ...p } : raw
      const k = kindMap.get(n.cluster?.groupBy === 'kind' ? n.cluster.value : n.kind)
      const pos = layout.positions[n.id] ?? { x: 0, y: 0 }
      const size = sizeOf(n, compactCards)
      return {
        id: n.id,
        type: n.cluster ? 'cluster' : 'entity',
        position: pos,
        width: size.width,
        height: size.height,
        selected: n.id === selected,
        data: {
          node: n,
          kindTitle: k?.title ?? humanize(n.cluster?.value ?? n.kind),
          kindIcon: k?.icon ?? 'tabler:cube',
          kindColor: kindAccent(k?.color, k?.group),
          compact: compactCards,
          iconSize: style.iconSize,
          sourcePos: hp.source,
          targetPos: hp.target,
          dimmed: !!neighbourSet && !neighbourSet.has(n.id),
        },
        ariaLabel: `${n.cluster ? 'Cluster' : (k?.title ?? n.kind)} ${n.displayName ?? n.name}, health ${n.health}, freshness ${n.freshness}`,
      }
    })
    const opts = {
      colorMode: style.colorMode,
      typeColors: style.typeColors,
      sourceColors,
      projectName,
      labels: style.labels,
      directionHint: style.directionHint,
      traffic: style.traffic,
      reducedMotion: reduced,
    }
    const edges: DeedFlowEdge[] = [...view.model.edges.values()].map((e) => {
      const v = edgeVisual(e, opts, typeColor, view.model.nodes, now, traffic.rates.get(e.id))
      const from = view.model.nodes.get(e.from)
      const to = view.model.nodes.get(e.to)
      const title = `${from?.displayName ?? from?.name ?? e.from} ${typeTitle(e.type)} ${to?.displayName ?? to?.name ?? e.to} · evidence: ${e.evidence}${sourcesText(e, projectName)}${v.notes.length ? ` · ${v.notes.join(', ')}` : ''}`
      return {
        id: e.id,
        source: e.from,
        target: e.to,
        type: 'deed' as const,
        selected: e.id === selectedEdge,
        markerEnd: { type: MarkerType.ArrowClosed, color: v.color, width: 14, height: 14 },
        data: {
          edge: e,
          visual: v,
          routing: style.routing,
          floating: algorithm !== 'layered',
          label: typeTitle(e.type),
          showLabel: style.labels,
          dimmed:
            !!neighbourSet &&
            !(neighbourSet.has(e.from) && neighbourSet.has(e.to) && (e.from === selected || e.to === selected)),
          title,
        },
        ariaLabel: title,
        zIndex: 0,
      }
    })
    return { nodes: [...regionNodes, ...groupNodes, ...nodes] as Node[], edges: edges }
  }, [
    layout,
    signature,
    view,
    patches,
    kindMap,
    compactCards,
    style,
    reduced,
    selected,
    selectedEdge,
    neighbourSet,
    algorithm,
    rankdir,
    typeColor,
    typeTitle,
    traffic.rates,
    now,
    sourceColors,
    projectName,
    collapseExpansion,
  ])

  const [nodes, setNodes, onNodesChange] = useNodesState<Node>([])
  // A node object without `measured` makes React Flow forget the node's size and connection points and measure it
  // again, and draw none of its edges until then — never, for nodes outside the view, which React Flow does not mount.
  // A node that keeps its size, type and port sides keeps its measurements; one that did not change keeps its object.
  const synced = useRef(new WeakMap<Node, Node>())
  useEffect(() => {
    setNodes((prev) => {
      const before = new Map(prev.map((n) => [n.id, n]))
      return built.nodes.map((n) => {
        const p = before.get(n.id)
        if (!p) return n
        if (synced.current.get(p) === n) return p
        const m = p.measured
        const pd = p.data as Partial<EntityNodeData>
        const nd = n.data as Partial<EntityNodeData>
        if (
          !m ||
          m.width !== n.width ||
          m.height !== n.height ||
          p.type !== n.type ||
          pd.sourcePos !== nd.sourcePos ||
          pd.targetPos !== nd.targetPos
        )
          return n
        const out = { ...n, measured: m }
        synced.current.set(out, n)
        return out
      })
    })
  }, [built.nodes, setNodes])

  const nodesRef = useRef(nodes)
  useLayoutEffect(() => {
    nodesRef.current = nodes
  }, [nodes])

  // Blocks of a drawing that opens zoomed out also mount without their connection points (DetailStore.ports). Once the
  // browser is idle — or before the first swap step towards cards, whichever comes first — they get them and every
  // mounted node is measured again in one batch. Nodes React Flow has not mounted (outside the view) would keep the
  // measurement without connection points, and their edges would never be drawn: without `measured` React Flow mounts
  // them once to measure them again, a few hundred per idle period.
  const portsPending = useRef<string[]>([])
  const ensurePorts = useCallback(() => {
    if (detail.ports) return
    flushSync(() => {
      detail.setPorts(true)
    })
    const s = flowStore.getState()
    const updates: Parameters<typeof s.updateNodeInternals>[0] = new Map()
    s.domNode?.querySelectorAll<HTMLDivElement>('.react-flow__node[data-id]').forEach((el) => {
      const id = el.dataset.id
      if (id) updates.set(id, { id, nodeElement: el, force: true })
    })
    s.updateNodeInternals(updates)
    portsPending.current = nodesRef.current
      .filter((n) => n.type !== 'group' && n.type !== 'region' && !updates.has(n.id))
      .map((n) => n.id)
  }, [detail, flowStore])
  useEffect(() => {
    if (layout?.sig !== signature) return
    let cancel = whenIdle(function next() {
      ensurePorts()
      const batch = new Set(portsPending.current.splice(0, 300))
      if (batch.size === 0) return
      setNodes((prev) =>
        prev.map((n) => {
          if (!batch.has(n.id)) return n
          const { measured: _, ...rest } = n
          return rest
        }),
      )
      cancel = whenIdle(next)
    })
    return () => {
      cancel()
      portsPending.current = []
    }
  }, [layout?.sig, signature, ensurePorts, setNodes])

  // Swap between blocks and cards in steps, one per frame (lodSwap.ts), outside React state: a step updates the detail
  // store, which re-renders only the nodes it swaps, the flow's edge list and LodEdges. Each direction's budget adapts
  // to how long the previous step's frame took, and is kept for the next swap.
  const stepBudget = useRef({ ...STEP_BUDGET })
  useEffect(() => {
    if (!big) {
      detail.set(ALL_DETAIL)
      return
    }
    const toDetail = !lod
    const key = toDetail ? 'toDetail' : 'toBlocks'
    let raf = 0
    let last = 0
    const step = (t: number) => {
      if (settled(detail.drawn, toDetail)) return
      if (toDetail) ensurePorts()
      if (last > 0) {
        const took = t - last
        const b = stepBudget.current[key]
        stepBudget.current[key] = took > 40 ? Math.max(8, b * 0.6) : took < 24 ? Math.min(2000, b * 1.5) : b
      }
      last = t
      const s = flowStore.getState()
      const [tx, ty, z] = s.transform
      const all = nodesRef.current
      detail.set(
        lodStep({
          drawn: detail.drawn,
          toDetail,
          nodes: all
            .filter((n) => n.type !== 'group' && n.type !== 'region')
            .map((n) => ({ id: n.id, x: n.position.x, y: n.position.y, width: n.width ?? 0, height: n.height ?? 0 })),
          edges: built.edges,
          // React Flow mounts only what is in view above 400 nodes (onlyRenderVisibleElements below).
          view:
            all.length > 400 && z > 0
              ? { x0: -tx / z, y0: -ty / z, x1: (s.width - tx) / z, y1: (s.height - ty) / z }
              : null,
          budget: stepBudget.current[key],
        }),
      )
      raf = requestAnimationFrame(step)
    }
    raf = requestAnimationFrame(step)
    return () => {
      cancelAnimationFrame(raf)
    }
  }, [lod, big, built.edges, detail, flowStore, ensurePorts])

  // Fit the view when the topology changes; record render timings for the budget measurements.
  const lastFit = useRef('')
  useEffect(() => {
    if (!layout || built.nodes.length === 0 || lastFit.current === layout.sig) return
    lastFit.current = layout.sig
    requestAnimationFrame(() => {
      void rf.fitView({ padding: fitPadding(), duration: reduced ? 0 : 250, maxZoom: 1.25 })
      requestAnimationFrame(() => {
        renders.current += 1
        let fetchToRender = 0
        try {
          performance.mark('deed-map-rendered')
          const m = performance.measure('deed-map-ttfn', 'deed-map-fetch-start', 'deed-map-rendered')
          fetchToRender = m.duration
        } catch {
          // the fetch mark is missing when data came from cache
        }
        window.__deedMap = {
          nodes: view.model.nodes.size,
          edges: view.model.edges.size,
          layoutMs: layout.cached ? 0 : Math.round(layout.ms),
          layoutCached: !!layout.cached,
          fetchToRenderMs: Math.round(fetchToRender),
          signature: layout.sig,
          renders: renders.current,
        }
      })
    })
  }, [layout, built.nodes.length, rf, reduced, view.model])

  const patch = useCallback(
    (p: Partial<MapSearch>) => setSearch(compact({ ...search, ...p }), true),
    [search, setSearch],
  )

  const go = useCallback(
    (p: Partial<MapSearch>, keepExpand = false) => {
      setSelected(null)
      setSelectedEdge(null)
      setPinned({})
      const trail = decodeList(search.trail)
      const focus = p.focus
      setSearch(
        compact({
          ...search,
          expand: keepExpand ? search.expand : undefined,
          from: undefined,
          to: undefined,
          impact: undefined,
          view: undefined,
          sel: undefined,
          ...p,
          ...(focus && focus !== search.focus
            ? { trail: encodeList([...trail.filter((t) => t !== focus), focus].slice(-10)) }
            : {}),
        }),
      )
    },
    [search, setSearch],
  )

  const expandNode = useCallback(
    (n: MapNode) => {
      if (n.kind === 'group' && n.cluster) {
        toggleCollapsed(n.cluster.key)
        return
      }
      const key = n.cluster ? n.cluster.key : `e:${n.id}`
      const cur = decodeList(search.expand)
      if (cur.includes(key)) return
      patch({ expand: encodeList([...cur, key]) })
    },
    [search.expand, patch],
  )

  const actions: InspectorActions = {
    focus: (id) => go({ focus: id, mode: 'neighborhood' }),
    expand: expandNode,
    impact: (id, dir) => go({ focus: id, mode: 'impact', impact: dir }),
    pathFrom: (id) => {
      setPanel('pathOpen', true)
      setSearch(compact({ ...search, from: id }), true)
    },
    pathTo: (id) => {
      setPanel('pathOpen', true)
      setSearch(compact({ ...search, to: id }), true)
    },
    select: (id) => {
      setSelected(id)
      const n = rf.getNode(id)
      if (n)
        void rf.setCenter(n.position.x + (n.width ?? 0) / 2, n.position.y + (n.height ?? 0) / 2, {
          zoom: rf.getZoom(),
          duration: reduced ? 0 : 200,
        })
    },
  }

  // Selection announcements for screen readers.
  const announce = useMemo(() => {
    const n = selected ? view.model.nodes.get(selected) : undefined
    if (!n || !selected) return ''
    const deg = neighbours(view.model, selected).length
    return `Selected ${n.displayName ?? n.name}, ${kindMap.get(n.kind)?.title ?? n.kind}, health ${n.health}, ${plural(deg, 'neighbour')}.`
  }, [selected, view.model, kindMap])

  /** The context menu from the keyboard: on the selected node, or the canvas menu in the middle of the map. */
  const openMenuFromKeyboard = (container: HTMLElement) => {
    const n = selected ? view.model.nodes.get(selected) : undefined
    const fn = selected ? rf.getInternalNode(selected) : undefined
    if (n && fn) {
      const p = rf.flowToScreenPosition({
        x: fn.internals.positionAbsolute.x + (fn.measured.width ?? 0) / 2,
        y: fn.internals.positionAbsolute.y + (fn.measured.height ?? 0) / 2,
      })
      setMenu({ kind: 'node', node: n, x: p.x, y: p.y })
    } else {
      const r = container.getBoundingClientRect()
      setMenu({ kind: 'pane', x: r.left + r.width / 2, y: r.top + r.height / 2 })
    }
  }

  const onKeyDown = (ev: React.KeyboardEvent) => {
    const t = ev.target as HTMLElement
    if (['INPUT', 'TEXTAREA', 'SELECT'].includes(t.tagName) || t.isContentEditable) return
    if (ev.shiftKey && ev.key === 'F10') {
      ev.preventDefault()
      menuFromKeyAt.current = performance.now()
      openMenuFromKeyboard(ev.currentTarget as HTMLElement)
      return
    }
    const dirs: Record<string, 'left' | 'right' | 'up' | 'down'> = {
      ArrowLeft: 'left',
      ArrowRight: 'right',
      ArrowUp: 'up',
      ArrowDown: 'down',
    }
    const dir = dirs[ev.key]
    if (dir) {
      ev.preventDefault()
      const positions = new Map(
        nodes
          .filter((n) => n.type !== 'group' && n.type !== 'region')
          .map((n) => [n.id, { x: n.position.x + (n.width ?? 0) / 2, y: n.position.y + (n.height ?? 0) / 2 }]),
      )
      if (!selected || !positions.has(selected)) {
        const first = [...view.model.nodes.values()].find((n) => n.root) ?? view.model.nodes.values().next().value
        if (first) actions.select(first.id)
        return
      }
      const next = nextInDirection(positions, selected, dir, neighbours(view.model, selected), [...positions.keys()])
      if (next) actions.select(next)
      return
    }
    switch (ev.key) {
      case 'Enter': {
        const n = selected ? view.model.nodes.get(selected) : undefined
        if (n) {
          ev.preventDefault()
          expandNode(n)
        }
        break
      }
      case 'Escape':
        setSelected(null)
        setSelectedEdge(null)
        setPanel('pathOpen', false)
        break
      case 'f':
        void rf.fitView({ padding: fitPadding(), duration: reduced ? 0 : 250 })
        break
      case 'i':
        if (selected && view.model.nodes.get(selected) && !view.model.nodes.get(selected)?.cluster)
          actions.impact(selected, 'downstream')
        break
      case 'l':
        setPanel('legendOpen', !legendOpen)
        break
      case 'm':
        setMasked(!masked)
        break
      case '+':
      case '=':
        void rf.zoomIn({ duration: reduced ? 0 : 150 })
        break
      case '-':
        void rf.zoomOut({ duration: reduced ? 0 : 150 })
        break
    }
  }

  const legend = useMemo(
    () =>
      buildLegend(
        [...view.model.edges.values()],
        {
          colorMode: style.colorMode,
          typeColors: style.typeColors,
          sourceColors,
          projectName,
          labels: style.labels,
          directionHint: style.directionHint,
          traffic: style.traffic,
          reducedMotion: reduced,
          lod: lodShown,
        },
        typeColor,
        typeTitle,
        view.model.nodes,
        now,
        traffic.rates,
      ),
    [view.model, style, reduced, lodShown, typeColor, typeTitle, traffic.rates, now, sourceColors, projectName],
  )

  // Breadcrumb names for the exploration trail.
  const focusIds = decodeList(search.focus)
  const trail = [...decodeList(search.trail)]
  if (focusIds.length === 1 && focusIds[0] && trail[trail.length - 1] !== focusIds[0]) trail.push(focusIds[0])
  const trailNames = useQuery({
    queryKey: keys.entities({ trail }),
    queryFn: async () => unwrap(await api.GET('/entities', { params: { query: { id: trail, limit: 20 } } })),
    enabled: trail.length > 0,
    staleTime: 60_000,
  })
  const nameOf = (id: string) => {
    if (masked) {
      const v = view.model.nodes.get(id)
      return v ? v.name : alias('entity', id)
    }
    const n = data.model.nodes.get(id)
    if (n) return n.displayName ?? n.name
    const e = trailNames.data?.items.find((x) => x.metadata.id === id)
    return e ? e.metadata.displayName || e.metadata.name : id.slice(0, 10)
  }

  const facets = useQuery({
    queryKey: keys.dashboard(globalProjects),
    queryFn: async () =>
      unwrap(
        await api.GET('/dashboard', { params: { query: globalProjects.length ? { project: globalProjects } : {} } }),
      ),
    staleTime: 60_000,
  })

  const applyView = (v: View | null) => {
    if (!v) {
      patch({ view: undefined })
      return
    }
    const st = (v.state && typeof v.state === 'object' ? v.state : {}) as {
      search?: Record<string, unknown>
      style?: Partial<MapStyle>
    }
    if (st.style) setStyle(st.style)
    const layoutPins = (v.layout && typeof v.layout === 'object' ? v.layout : {}) as Record<
      string,
      { x: number; y: number }
    >
    setPinned(layoutPins)
    setSearch({ ...validateMapSearch(st.search ?? {}), view: v.id })
  }

  const selNode = selected ? view.model.nodes.get(selected) : undefined
  const selEdge = selectedEdge ? view.model.edges.get(selectedEdge) : undefined
  const loading = data.base.isPending || (view.model.nodes.size > 0 && layout?.sig !== signature)
  const mode = data.mode
  const menuActions: ContextMenuActions = {
    ...actions,
    selectEdge: (id) => {
      setSelected(null)
      setSelectedEdge(id)
    },
    unpin: (id) => {
      setPinned(Object.fromEntries(Object.entries(mapStore.state.pinned).filter(([k]) => k !== id)))
    },
    isPinned: (id) => id in mapStore.state.pinned,
    fitView: () => void rf.fitView({ padding: fitPadding(), duration: reduced ? 0 : 250 }),
    overview: () => go({ focus: undefined, mode: undefined, trail: undefined }),
    clearSelection: () => {
      setSelected(null)
      setSelectedEdge(null)
    },
    hasSelection: selected !== null || selectedEdge !== null,
    inOverview: mode === 'overview',
  }

  return (
    <Box
      style={{ display: 'flex', flexDirection: 'column', height: 'calc(100dvh - 52px - var(--deed-banner-h, 0px))' }}
    >
      <Group
        px="md"
        py={8}
        gap="sm"
        justify="space-between"
        wrap="wrap"
        style={{ borderBottom: '1px solid var(--deed-border)', background: 'var(--deed-surface)' }}
      >
        <Group gap="sm" wrap="nowrap" style={{ minWidth: 0 }}>
          <IconMap2 size={18} aria-hidden />
          <Breadcrumbs
            separator="›"
            separatorMargin={6}
            aria-label="Exploration path"
            style={{ flexWrap: 'nowrap', overflow: 'hidden' }}
          >
            <Anchor
              component="button"
              type="button"
              size="sm"
              onClick={() => go({ focus: undefined, mode: undefined, trail: undefined })}
              fw={mode === 'overview' ? 600 : 400}
            >
              Overview
            </Anchor>
            {trail.map((id) => (
              <Anchor
                key={id}
                component="button"
                type="button"
                size="sm"
                truncate
                maw={160}
                fw={search.focus === id ? 600 : 400}
                onClick={() => go({ focus: id, mode: 'neighborhood' })}
              >
                {nameOf(id)}
              </Anchor>
            ))}
          </Breadcrumbs>
          {mode === 'path' && <Badge variant="light">Path</Badge>}
          {mode === 'impact' && (
            <Badge variant="light" color="orange">
              Impact · {search.impact ?? 'downstream'}
            </Badge>
          )}
        </Group>
        <Group gap="xs" wrap="wrap">
          {mode === 'overview' && (
            <Select
              aria-label="Group overview by"
              data={[
                { value: 'kind', label: 'Group by kind' },
                { value: 'project', label: 'Group by project' },
                { value: 'environment', label: 'Group by environment' },
                { value: 'owner', label: 'Group by owner' },
              ]}
              value={search.group ?? 'kind'}
              onChange={(v) => go({ group: (v ?? 'kind') as MapSearch['group'] })}
              w={180}
              allowDeselect={false}
              data-testid="map-group-by"
            />
          )}
          {(mode === 'neighborhood' || mode === 'impact') && (
            <NumberInput
              aria-label="Depth"
              leftSection={
                <Text size="xs" c="dimmed" pl={6}>
                  Depth
                </Text>
              }
              leftSectionWidth={48}
              min={1}
              max={6}
              value={search.depth ?? (mode === 'impact' ? 4 : 2)}
              onChange={(v) => patch({ depth: typeof v === 'number' ? v : undefined, expand: undefined })}
              w={110}
              data-testid="map-depth"
            />
          )}
          {mode === 'neighborhood' && (
            <SegmentedControl
              aria-label="Direction"
              value={search.dir ?? 'both'}
              onChange={(v) => patch({ dir: v === 'both' ? undefined : (v as MapSearch['dir']), expand: undefined })}
              data={[
                { value: 'both', label: 'Both' },
                { value: 'out', label: 'Out' },
                { value: 'in', label: 'In' },
              ]}
            />
          )}
          {mode === 'impact' && (
            <SegmentedControl
              aria-label="Impact direction"
              value={search.impact ?? 'downstream'}
              onChange={(v) => patch({ impact: v as MapSearch['impact'] })}
              data={[
                { value: 'downstream', label: 'Downstream' },
                { value: 'upstream', label: 'Upstream' },
              ]}
            />
          )}
          <Tooltip label="Find an entity to focus (/)">
            <Button variant="default" leftSection={<IconSearch size={14} />} onClick={() => spotlight.open()}>
              Focus…
            </Button>
          </Tooltip>
          <Button
            variant={pathOpen ? 'light' : 'default'}
            leftSection={<IconRoute size={14} />}
            onClick={() => setPanel('pathOpen', !pathOpen)}
            data-testid="path-finder-toggle"
          >
            Path
          </Button>
          <Button
            variant="default"
            leftSection={<IconAdjustmentsHorizontal size={14} />}
            onClick={() => setPanel('optionsOpen', true)}
            data-testid="map-options-button"
          >
            Filters & options
          </Button>
          <SavedViewsMenu
            kind="map"
            activeId={search.view}
            currentState={{ search: { ...search, view: undefined, sel: undefined }, style }}
            currentLayout={() =>
              Object.fromEntries(
                rf
                  .getNodes()
                  .filter((n) => n.type !== 'group' && n.type !== 'region')
                  .map((n) => [n.id, { x: Math.round(n.position.x), y: Math.round(n.position.y) }]),
              )
            }
            onApply={applyView}
          />
          <Tooltip label="Mask mode: replace names, owners and projects with stable aliases for screenshots (m)">
            <Button
              variant={masked ? 'filled' : 'default'}
              color={masked ? 'grape' : undefined}
              leftSection={<IconMask size={14} />}
              aria-pressed={masked}
              onClick={() => setMasked(!masked)}
              data-testid="map-mask"
            >
              {masked ? 'Masked' : 'Mask'}
            </Button>
          </Tooltip>
          <Tooltip label="The same relationships as an accessible table">
            <ButtonLink
              variant="default"
              leftSection={<IconListDetails size={14} />}
              to="/relations"
              search={search.focus ? { entity: decodeList(search.focus)[0] } : {}}
              data-testid="map-table-view"
            >
              Table
            </ButtonLink>
          </Tooltip>
        </Group>
      </Group>
      {mode === 'impact' && (
        <Alert
          color="orange"
          variant="light"
          radius={0}
          py={6}
          icon={<IconInfoCircle size={16} />}
          data-testid="impact-banner"
        >
          <Text size="sm">
            <b>Potential impact, evidence-based.</b>{' '}
            {search.impact === 'upstream' ? 'What this entity depends on' : 'What may be affected if this entity fails'}
            , following the dependency semantics of each relationship type. {Math.max(0, view.model.nodes.size - 1)}{' '}
            entities reached; each is labelled with the weakest evidence on its path. This is not an observed failure.
          </Text>
        </Alert>
      )}
      {(data.model.truncated || ('budgetHit' in view.model && view.model.budgetHit)) && (
        <Alert color="yellow" variant="light" radius={0} py={4}>
          <Text size="sm">
            Showing a bounded part of the graph ({view.model.nodes.size} nodes, {view.model.edges.size} edges; budget
            2,000 / 4,000). Narrow with filters, depth or grouping, or expand clusters one at a time.
          </Text>
        </Alert>
      )}
      {data.model.notes.length > 0 && (
        <Text size="xs" c="dimmed" px="md" py={2}>
          {data.model.notes.join(' · ')}
        </Text>
      )}
      <Box
        style={{ flex: 1, position: 'relative', minHeight: 0 }}
        onKeyDown={onKeyDown}
        onContextMenu={(ev) => {
          // Right-clicks on nodes, edges and the canvas are handled (and prevented) by React Flow's handlers. A
          // context-menu event that gets here comes from the keyboard (the context-menu key, or Shift+F10 where the
          // browser turns it into one) on the focused map.
          if (ev.defaultPrevented) return
          ev.preventDefault()
          // Shift+F10 already opened the menu on keydown; some browsers follow it with this event.
          if (performance.now() - menuFromKeyAt.current < 500) return
          openMenuFromKeyboard(ev.currentTarget)
        }}
      >
        <div aria-live="polite" className="visually-hidden">
          {announce}
        </div>
        {data.base.isError && !data.base.data ? (
          <Box p="md">
            <ErrorState error={data.base.error} what="the map" onRetry={() => void data.base.refetch()} />
          </Box>
        ) : !data.base.isPending && data.model.nodes.size === 0 ? (
          <EmptyState
            title={
              mode === 'path' ? 'No path found' : mode === 'overview' ? 'Nothing to map yet' : 'No relationships here'
            }
            action={
              mode === 'overview' ? (
                <ButtonLink to="/sources">Add a source</ButtonLink>
              ) : (
                <Button variant="default" onClick={() => go({ focus: undefined, mode: undefined })}>
                  Back to overview
                </Button>
              )
            }
          >
            {mode === 'path'
              ? 'No relationship path connects these entities within the maximum depth. Try a larger depth or remove type filters.'
              : mode === 'overview'
                ? 'The catalog has no entities you can read. Declare some or connect a source.'
                : 'The focused entity has no relationships matching the filters.'}
          </EmptyState>
        ) : (
          // The map container takes focus for its keyboard model (arrows, Enter, Esc); role="application" tells screen
          // readers that keys go to the widget.
          <DetailContext.Provider value={detail}>
            <div
              className="deed-map"
              style={{ position: 'absolute', inset: 0 }}
              // eslint-disable-next-line jsx-a11y/no-noninteractive-tabindex
              tabIndex={0}
              role="application"
              aria-roledescription="relationship map"
              aria-label="Relationship map. Arrow keys move between neighbours, Enter expands, Escape clears the selection, f fits the view. The same data is available as a table from the Table button."
              data-testid="map-canvas"
              data-layout-ready={layout?.sig === signature ? 'true' : 'false'}
            >
              <DetailFlow
                nodes={nodes}
                allEdges={built.edges}
                onNodesChange={onNodesChange}
                nodeTypes={nodeTypes}
                edgeTypes={edgeTypes}
                colorMode={scheme}
                minZoom={0.05}
                maxZoom={2}
                onlyRenderVisibleElements={nodes.length > 400}
                // Blocks of a drawing that opens zoomed out mount without drag set-up (see dragSig).
                nodesDraggable={dragSig === signature || !lod}
                disableKeyboardA11y
                deleteKeyCode={null}
                selectionKeyCode={null}
                multiSelectionKeyCode={null}
                nodesConnectable={false}
                elevateEdgesOnSelect
                proOptions={{ hideAttribution: false }}
                onNodeClick={(_, n) => {
                  const mn = view.model.nodes.get(n.id)
                  setSelectedEdge(null)
                  if (!mn) return
                  if (mn.cluster) {
                    setSelected(n.id)
                    expandNode(mn)
                  } else setSelected(n.id)
                }}
                onNodeDoubleClick={(_, n) => {
                  const mn = view.model.nodes.get(n.id)
                  if (mn && !mn.cluster) expandNode(mn)
                }}
                onEdgeClick={(_, e) => {
                  setSelected(null)
                  setSelectedEdge(e.id)
                }}
                onPaneClick={() => {
                  setSelected(null)
                  setSelectedEdge(null)
                }}
                onNodeContextMenu={(ev, n) => {
                  const mn = view.model.nodes.get(n.id)
                  if (!mn) return
                  ev.preventDefault()
                  setMenu({ kind: 'node', node: mn, x: ev.clientX, y: ev.clientY })
                }}
                onEdgeContextMenu={(ev, e) => {
                  const me = view.model.edges.get(e.id)
                  if (!me) return
                  ev.preventDefault()
                  setMenu({
                    kind: 'edge',
                    edge: me,
                    from: view.model.nodes.get(me.from),
                    to: view.model.nodes.get(me.to),
                    x: ev.clientX,
                    y: ev.clientY,
                  })
                }}
                onPaneContextMenu={(ev) => {
                  ev.preventDefault()
                  setMenu({ kind: 'pane', x: ev.clientX, y: ev.clientY })
                }}
                onNodeDragStop={(_, n) => {
                  if (n.type !== 'group' && n.type !== 'region') pinPosition(n.id, n.position)
                }}
              >
                <Background variant={BackgroundVariant.Dots} gap={18} size={1} />
                {big && <LodEdges edges={built.edges as DeedFlowEdge[]} nodes={nodes} />}
                <Controls
                  showInteractive={false}
                  showFitView={false}
                  position="bottom-right"
                  aria-label="Zoom controls"
                >
                  <Tooltip label="Fit view (f)">
                    <button
                      type="button"
                      className="react-flow__controls-button"
                      onClick={() => void rf.fitView({ padding: fitPadding() })}
                      aria-label="Fit view"
                    >
                      <IconArrowsMaximize size={12} />
                    </button>
                  </Tooltip>
                </Controls>
                {style.minimap && (
                  <MiniMap
                    position="bottom-right"
                    style={{ marginRight: 52 }}
                    pannable
                    zoomable
                    ariaLabel="Minimap"
                    nodeColor={(n) =>
                      n.type === 'group' || n.type === 'region'
                        ? 'transparent'
                        : (n.data as EntityNodeData).kindColor || '#adb5bd'
                    }
                    nodeStrokeWidth={2}
                    maskColor={scheme === 'dark' ? 'rgba(0,0,0,0.5)' : 'rgba(240,242,245,0.6)'}
                  />
                )}
                <Panel position="top-left">
                  {pathOpen && (
                    <PathFinder
                      key={`${search.from ?? ''}>${search.to ?? ''}`}
                      search={search}
                      onFind={(from, to, depth, directed) =>
                        go({ mode: 'path', from, to, depth, focus: undefined, ...(directed ? {} : {}) })
                      }
                      onClose={() => setPanel('pathOpen', false)}
                    />
                  )}
                </Panel>
                <Panel position="bottom-left">
                  {legendOpen ? (
                    <MapLegend legend={legend} onClose={() => setPanel('legendOpen', false)} showNodes lod={lodShown} />
                  ) : (
                    <Button size="xs" variant="default" onClick={() => setPanel('legendOpen', true)}>
                      Show legend (l)
                    </Button>
                  )}
                </Panel>
                <Panel position="top-right" style={{ maxHeight: 'calc(100% - 30px)', display: 'flex' }}>
                  {selNode ? (
                    <NodeInspector
                      node={patches.get(selNode.id) ? { ...selNode, ...patches.get(selNode.id) } : selNode}
                      model={view.model}
                      kindTitle={kindMap.get(selNode.kind)?.title ?? humanize(selNode.cluster?.value ?? selNode.kind)}
                      kindIcon={
                        kindMap.get(selNode.cluster?.groupBy === 'kind' ? selNode.cluster.value : selNode.kind)?.icon ??
                        'tabler:cube'
                      }
                      typeTitle={typeTitle}
                      actions={actions}
                      onClose={() => setSelected(null)}
                    />
                  ) : selEdge ? (
                    <EdgeInspector
                      edge={selEdge}
                      model={view.model}
                      typeTitle={typeTitle}
                      sourceColor={colorOfSource}
                      projectName={projectName}
                      onClose={() => setSelectedEdge(null)}
                    />
                  ) : null}
                </Panel>
                <Panel position="top-center">
                  {(loading || data.expanding) && (
                    <Group gap={6} className="deed-panel" px="sm" py={4}>
                      <Loader size="xs" />
                      <Text size="xs">
                        {data.base.isPending ? 'Loading graph…' : data.expanding ? 'Expanding…' : 'Laying out…'}
                      </Text>
                    </Group>
                  )}
                  {layoutError?.sig === signature && (
                    <Alert color="red" py={4}>
                      Layout failed: {layoutError.message}
                    </Alert>
                  )}
                </Panel>
              </DetailFlow>
            </div>
          </DetailContext.Provider>
        )}
      </Box>
      {/* Outside the map container: events from the menu's portal must not reach the map's key and menu handlers. */}
      <MapContextMenu target={menu} onClose={() => setMenu(null)} actions={menuActions} />
      <Group
        px="md"
        py={4}
        gap="md"
        style={{ borderTop: '1px solid var(--deed-border)', background: 'var(--deed-surface)' }}
      >
        <Text size="xs" c="dimmed" data-testid="map-stats">
          {view.model.nodes.size} nodes · {view.model.edges.size} edges
          {layout
            ? ` · layout ${layout.cached ? `cached (${Math.round(layout.ms)} ms when computed)` : `${Math.round(layout.ms)} ms`} (${algorithm})`
            : ''}
          {data.model.elapsedMs ? ` · query ${data.model.elapsedMs} ms` : ''}
          {style.traffic
            ? ` · traffic: ${traffic.measured} measured edge${traffic.measured === 1 ? '' : 's'}${traffic.stream === 'live' ? ' (live)' : ''}`
            : ''}
        </Text>
        {lodShown && (
          <Text size="xs" c="dimmed" data-testid="map-lod-note">
            Simplified drawing while zoomed out — zoom in for names, arrowheads and labels
          </Text>
        )}
        <Text size="xs" c="dimmed">
          ←↑→↓ neighbours · Enter expand · right-click or Shift+F10 actions · Esc clear · f fit · l legend · / search
        </Text>
        {decodeList(search.expand).length > 0 && (
          <Anchor component="button" type="button" size="xs" onClick={() => patch({ expand: undefined })}>
            Collapse {plural(decodeList(search.expand).length, 'expansion')}
          </Anchor>
        )}
        {Object.keys(mapStore.state.pinned).length > 0 && (
          <Anchor component="button" type="button" size="xs" onClick={() => setPinned({})}>
            Reset dragged positions
          </Anchor>
        )}
        {collapsed.length > 0 && (
          <Anchor component="button" type="button" size="xs" onClick={() => collapsed.forEach(toggleCollapsed)}>
            Expand {plural(collapsed.length, 'collapsed group')}
          </Anchor>
        )}
      </Group>
      <OptionsDrawer
        opened={optionsOpen}
        onClose={() => setPanel('optionsOpen', false)}
        search={search}
        patch={patch}
        style={style}
        relTypes={relTypes.data ?? []}
        presentTypes={[...new Set([...view.model.edges.values()].map((e) => e.type))].sort()}
        envOptions={Object.keys(facets.data?.counts.by_environment ?? {}).filter(Boolean)}
        ownerOptions={Object.keys(facets.data?.counts.by_owner ?? {}).filter(Boolean)}
        canEditTypes={isInstallationAdmin(me)}
      />
      {data.expansionErrors.length > 0 && (
        <Box pos="fixed" bottom={60} left="50%" style={{ transform: 'translateX(-50%)' }}>
          <Alert color="red" py={4}>
            {data.expansionErrors.length} expansion(s) failed to load. <ActionIcon component="span" size="xs" />
          </Alert>
        </Box>
      )}
    </Box>
  )
}

export function MapPage(props: { search: MapSearch; setSearch: (s: MapSearch, replace?: boolean) => void }) {
  return (
    <ReactFlowProvider>
      <MapInner {...props} />
    </ReactFlowProvider>
  )
}
