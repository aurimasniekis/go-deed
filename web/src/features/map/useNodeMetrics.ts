import { useQuery } from '@tanstack/react-query'
import { useMemo } from 'react'
import { api, unwrap, type Schemas } from '@/api/client'

export type MapMetric = Schemas['MapMetric']

const BATCH = 200
const MAX_ENTITIES = 600

/** Returned while node metrics are off: one shared map, so the map's memoized nodes stay stable. */
const NO_METRICS = new Map<string, MapMetric[]>()

/**
 * Node-card metrics: POST /telemetry/map-metrics for the entities on screen (batches of 200, refreshed every minute).
 * Values come from bindings marked on_map (CPU, memory, disk, request rates …); entities without any are absent.
 * Like traffic rates they patch node data only and never re-run layout.
 */
export function useNodeMetrics(entityIds: string[], enabled: boolean, window = '15m') {
  const ids = useMemo(
    () => [...new Set(entityIds.filter((id) => id.startsWith('ent_')))].sort().slice(0, MAX_ENTITIES),
    [entityIds],
  )
  const q = useQuery({
    queryKey: ['graph', 'map-metrics', window, ids.join(',')],
    queryFn: async () => {
      const out = new Map<string, MapMetric[]>()
      for (let i = 0; i < ids.length; i += BATCH) {
        const res = unwrap(
          await api.POST('/telemetry/map-metrics', {
            body: { entity_ids: ids.slice(i, i + BATCH), window: window as '15m' },
          }),
        )
        for (const [id, list] of Object.entries(res.entities)) out.set(id, list)
      }
      return out
    },
    enabled: enabled && ids.length > 0,
    refetchInterval: 60_000,
    staleTime: 30_000,
    retry: false,
  })
  return enabled ? (q.data ?? NO_METRICS) : NO_METRICS
}

const BYTES = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB']

/** A compact rendering of a metric value with its unit: `23%`, `1.2 GiB`, `0.4 req/s`, `12 ms`. */
export function formatMetric(value: number, unit = ''): string {
  const u = unit.trim().toLowerCase()
  const num = (v: number) => {
    const a = Math.abs(v)
    if (a !== 0 && a < 0.01) return v.toExponential(0)
    return a >= 100 ? v.toFixed(0) : a >= 10 ? v.toFixed(1).replace(/\.0$/, '') : v.toFixed(2).replace(/\.?0+$/, '')
  }
  if (u === 'percent' || u === '%') return `${num(value)}%`
  if (u === 'ratio') return `${num(value * 100)}%`
  if (u === 'bytes' || u === 'b') {
    let v = value
    let i = 0
    while (Math.abs(v) >= 1024 && i < BYTES.length - 1) {
      v /= 1024
      i++
    }
    return `${num(v)} ${BYTES[i] ?? 'B'}`
  }
  return unit ? `${num(value)} ${unit}` : num(value)
}
