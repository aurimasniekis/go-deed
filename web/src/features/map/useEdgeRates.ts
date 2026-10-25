import { useQuery } from '@tanstack/react-query'
import { useMemo, useState } from 'react'
import { api, unwrap } from '@/api/client'
import { useTelemetryStream, type EdgeRate } from '@/features/telemetry/stream'
import type { RateInfo } from './edgeStyle'

const BATCH = 200
const MAX_EDGES = 1000
const STREAM_EDGES = 100

function toInfo(r: EdgeRate): RateInfo {
  return {
    status: r.status,
    ...(r.rate !== undefined ? { rate: r.rate } : {}),
    ...(r.unit ? { unit: r.unit } : {}),
    window: r.window,
    basis: r.basis,
    ...(r.note ? { note: r.note } : {}),
    ...(r.measured_at ? { measured_at: r.measured_at } : {}),
  }
}

/**
 * Traffic overlay data: POST /telemetry/edge-rates for the visible relationships (batches of 200, refreshed every
 * minute) plus the telemetry SSE stream for up to 100 of them (30 s ticks). Rates patch edge data only — they are not
 * part of the topology signature, so they never re-run layout.
 */
export function useEdgeRates(relationIds: string[], enabled: boolean, window = '15m') {
  const ids = useMemo(
    () => [...new Set(relationIds.filter((id) => id.startsWith('rel_')))].sort().slice(0, MAX_EDGES),
    [relationIds],
  )
  const key = ids.join(',')
  const [live, setLive] = useState<Map<string, RateInfo>>(new Map())
  const q = useQuery({
    queryKey: ['graph', 'edge-rates', window, key],
    queryFn: async () => {
      const out = new Map<string, RateInfo>()
      for (let i = 0; i < ids.length; i += BATCH) {
        const res = unwrap(
          await api.POST('/telemetry/edge-rates', {
            body: { relation_ids: ids.slice(i, i + BATCH), window: window as '15m' },
          }),
        )
        for (const r of res.rates) out.set(r.relation_id, toInfo(r))
      }
      return out
    },
    enabled: enabled && ids.length > 0,
    refetchInterval: 60_000,
    staleTime: 30_000,
    retry: false,
  })
  const stream = useTelemetryStream({
    enabled: enabled && ids.length > 0,
    relations: ids.slice(0, STREAM_EDGES),
    window,
    interval: 30,
    onEdgeRates: (ev) => {
      setLive((prev) => {
        const next = new Map(prev)
        for (const r of ev.rates) next.set(r.relation_id, toInfo(r))
        return next
      })
    },
  })
  const rates = useMemo(() => {
    const m = new Map(q.data ?? [])
    for (const [k, v] of live) m.set(k, v)
    return m
  }, [q.data, live])
  return {
    rates: enabled ? rates : new Map<string, RateInfo>(),
    error: q.error,
    stream,
    measured: [...rates.values()].filter((r) => r.status === 'measured').length,
  }
}
