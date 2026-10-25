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
  useUpdateNodeInternals,
  type Edge,
  type Node,
  type FitViewOptions,
} from '@xyflow/react'
import { startTransition, useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { isInstallationAdmin, useMe } from '@/api/auth'
import { api, unwrap, type View } from '@/api/client'
import { keys, useKinds, useProjects, useRelTypes, useSources } from '@/api/queries'
import { onLiveChanges } from '@/api/useLive'
import { EmptyState, ErrorState } from '@/components/States'
import { SavedViewsMenu } from '@/features/views/SavedViews'
import { useProjectFilter, useTitle } from '@/lib/appStore'
import { humanize, plural } from '@/lib/format'
import { compact, decodeList, encodeList, validateMapSearch, type MapSearch } from '@/lib/urlState'
import { buildLegend, edgeVisual, sourceColor, sourcePalette } from './edgeStyle'
import { edgeTypes, LodEdges, type DeedFlowEdge } from './edges'
import { EdgeInspector, NodeInspector, type InspectorActions } from './Inspector'
import { layoutCache, runLayout, warmLayout } from './layout'
import type { LayoutResult } from './layoutTypes'
import { MapLegend } from './Legend'
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
  const [patches, setPatches] = useState<Map<string, Patch>>(new Map())
  const [layout, setLayout] = useState<(LayoutResult & { sig: string; cached?: boolean }) | null>(null)
  const [layoutError, setLayoutError] = useState<{ sig: string; message: string } | null>(null)
  const renders = useRef(0)
  useEffect(warmLayout, [])
  // Zoomed out past the level-of-detail threshold (with hysteresis: enter below LOD.enter, leave above LOD.leave).
  // Subscribing to the store directly re-renders the map only when the threshold is crossed, not on every zoom step.
  const flowStore = useStoreApi()
  const [zoomedOut, setZoomedOut] = useState(false)
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
  const projectName = useCallback(
    (id: string) => {
      const p = projects.data?.find((x) => x.id === id)
      return p ? p.display_name || p.name : id
    },
    [projects.data],
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
    const clustered = clusterModel(filtered, clustering, new Set(collapsed), projectName)
    const budgeted = enforceBudget(clustered.model)
    const groups = clustered.groups.filter((g) => !g.collapsed && g.members.some((m) => budgeted.nodes.has(m)))
    return { model: budgeted, groups }
  }, [data.model, filters, clustering, collapsed, projectName])

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
        // out mounts as level-of-detail blocks straight away instead of as cards first (the animated fit follows).
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
            setZoomedOut(vp.zoom < LOD.enter)
          }
        }
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

  const neighbourSet = useMemo(
    () => (selected ? new Set([selected, ...neighbours(view.model, selected)]) : null),
    [selected, view.model],
  )

  // Traffic overlay: measured edge rates patch edge data only (never part of the topology signature).
  const edgeIds = useMemo(() => [...view.model.edges.keys()], [view.model])
  const traffic = useEdgeRates(edgeIds, style.traffic)

  const lod = zoomedOut && view.model.nodes.size >= LOD.minNodes

  const built = useMemo(() => {
    if (layout?.sig !== signature) return { nodes: [] as Node[], edges: [] as Edge[] }
    const hp =
      algorithm === 'layered'
        ? (handlePos[rankdir] ?? { source: Position.Right, target: Position.Left })
        : { source: Position.Right, target: Position.Left }
    const groupNodes: Node<GroupNodeData, 'group'>[] = view.groups
      .filter((g) => layout.groups[g.key])
      .map((g) => {
        const b = layout.groups[g.key] as { x: number; y: number; width: number; height: number }
        return {
          id: `group:${g.key}`,
          type: 'group' as const,
          position: { x: b.x, y: b.y },
          data: { key: g.key, label: g.label, count: g.members.length },
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
          lod: false,
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
    return { nodes: [...groupNodes, ...nodes] as Node[], edges: edges }
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
  ])

  // Level of detail only swaps how nodes draw (crossing the threshold must not rebuild every node and edge).
  const shownNodes = useMemo(
    () =>
      lod
        ? built.nodes.map((n) =>
            n.type === 'group'
              ? n
              : // Dragging a block in the zoomed-out overview is never useful, and skips the drag set-up per node.
                { ...n, draggable: false, data: { ...(n.data as EntityNodeData), lod: true } },
          )
        : built.nodes,
    [built.nodes, lod],
  )
  const [nodes, setNodes, onNodesChange] = useNodesState<Node>([])
  useEffect(() => {
    setNodes(shownNodes)
  }, [shownNodes, setNodes])

  // Blocks have no handles, so nodes that stay mounted when cards come back are re-measured, all in one batch, once
  // the cards are on screen (newly mounted nodes are measured by React Flow itself).
  const updateNodeInternals = useUpdateNodeInternals()
  const remeasure = useRef(false)
  useEffect(() => {
    if (lod) remeasure.current = true
  }, [lod])
  useEffect(() => {
    const first = nodes.find((n) => n.type !== 'group')
    if (!remeasure.current || !first || (first.data as EntityNodeData).lod) return
    remeasure.current = false
    const mounted = [...document.querySelectorAll('.deed-map .react-flow__node[data-id]')].map(
      (el) => el.getAttribute('data-id') ?? '',
    )
    updateNodeInternals(mounted)
  }, [nodes, updateNodeInternals])

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

  const onKeyDown = (ev: React.KeyboardEvent) => {
    const t = ev.target as HTMLElement
    if (['INPUT', 'TEXTAREA', 'SELECT'].includes(t.tagName) || t.isContentEditable) return
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
          .filter((n) => n.type !== 'group')
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
          lod,
        },
        typeColor,
        typeTitle,
        view.model.nodes,
        now,
        traffic.rates,
      ),
    [view.model, style, reduced, lod, typeColor, typeTitle, traffic.rates, now, sourceColors, projectName],
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
                  .filter((n) => n.type !== 'group')
                  .map((n) => [n.id, { x: Math.round(n.position.x), y: Math.round(n.position.y) }]),
              )
            }
            onApply={applyView}
          />
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
      <Box style={{ flex: 1, position: 'relative', minHeight: 0 }} onKeyDown={onKeyDown}>
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
            <ReactFlow
              nodes={nodes}
              edges={lod ? NO_EDGES : built.edges}
              onNodesChange={onNodesChange}
              nodeTypes={nodeTypes}
              edgeTypes={edgeTypes}
              colorMode={scheme}
              minZoom={0.05}
              maxZoom={2}
              onlyRenderVisibleElements={nodes.length > 400}
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
              onNodeDragStop={(_, n) => {
                if (n.type !== 'group') pinPosition(n.id, n.position)
              }}
            >
              <Background variant={BackgroundVariant.Dots} gap={18} size={1} />
              {lod && <LodEdges edges={built.edges as DeedFlowEdge[]} nodes={nodes} />}
              <Controls showInteractive={false} showFitView={false} position="bottom-right" aria-label="Zoom controls">
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
                    n.type === 'group' ? 'transparent' : (n.data as EntityNodeData).kindColor || '#adb5bd'
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
                  <MapLegend legend={legend} onClose={() => setPanel('legendOpen', false)} showNodes lod={lod} />
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
            </ReactFlow>
          </div>
        )}
      </Box>
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
        {lod && (
          <Text size="xs" c="dimmed" data-testid="map-lod-note">
            Simplified drawing while zoomed out — zoom in for names, arrowheads and labels
          </Text>
        )}
        <Text size="xs" c="dimmed">
          ←↑→↓ neighbours · Enter expand · Esc clear · f fit · l legend · / search
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
