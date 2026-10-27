import { Store } from '@tanstack/react-store'
import { readPref, writePref } from '@/lib/prefs'
import type { ColorMode, Routing } from './edgeStyle'

/**
 * Map view state (TanStack Store, ADR-011): selection, collapsed groups, manual positions and the options-drawer styles.
 * What defines *which* graph is shown (focus, mode, filters, grouping, layout) lives in the URL; this store holds how it
 * is drawn and what the user is doing with it. Style options are remembered per browser and saved with map views.
 */
export interface MapStyle {
  colorMode: ColorMode
  routing: Routing
  labels: boolean
  directionHint: boolean
  /** Traffic overlay (measured edge rates). */
  traffic: boolean
  /** Values of on-map telemetry bindings (CPU, memory, …) under detailed node cards. */
  metrics: boolean
  nodeMode: 'compact' | 'detailed'
  iconSize: number
  minimap: boolean
  typeColors: Record<string, string>
}

export interface MapState {
  style: MapStyle
  selected: string | null
  collapsed: string[]
  /** Positions the user dragged nodes to (or restored from a saved layout). */
  pinned: Record<string, { x: number; y: number }>
  legendOpen: boolean
  visibilityOpen: boolean
  optionsOpen: boolean
  pathOpen: boolean
}

export const DEFAULT_STYLE: MapStyle = {
  colorMode: 'type',
  routing: 'bezier',
  labels: false,
  directionHint: false,
  traffic: true,
  metrics: true,
  nodeMode: 'detailed',
  iconSize: 18,
  minimap: true,
  typeColors: {},
}

function loadStyle(): MapStyle {
  const s = readPref<Partial<MapStyle>>('map.style', {})
  return { ...DEFAULT_STYLE, ...s, typeColors: { ...(s.typeColors ?? {}) } }
}

export const mapStore = new Store<MapState>({
  style: loadStyle(),
  selected: null,
  collapsed: [],
  pinned: {},
  legendOpen: true,
  visibilityOpen: false,
  optionsOpen: false,
  pathOpen: false,
})

export function setStyle(patch: Partial<MapStyle>) {
  mapStore.setState((s) => {
    const style = { ...s.style, ...patch }
    writePref('map.style', style)
    return { ...s, style }
  })
}

export function setSelected(id: string | null) {
  if (mapStore.state.selected !== id) mapStore.setState((s) => ({ ...s, selected: id }))
}

export function toggleCollapsed(key: string) {
  mapStore.setState((s) => ({
    ...s,
    collapsed: s.collapsed.includes(key) ? s.collapsed.filter((k) => k !== key) : [...s.collapsed, key],
  }))
}

export function pinPosition(id: string, pos: { x: number; y: number }) {
  mapStore.setState((s) => ({ ...s, pinned: { ...s.pinned, [id]: pos } }))
}

export function setPinned(pinned: Record<string, { x: number; y: number }>) {
  mapStore.setState((s) => ({ ...s, pinned }))
}

export function setPanel(panel: 'legendOpen' | 'visibilityOpen' | 'optionsOpen' | 'pathOpen', open: boolean) {
  mapStore.setState((s) => ({ ...s, [panel]: open }))
}
