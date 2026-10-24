import { useEffect, useRef, useState } from 'react'
import type { Schemas } from '@/api/client'

export type EdgeRate = Schemas['EdgeRate']

export interface TelemetryEvent {
  entity_id: string
  window: string
  results: Schemas['TelemetryResult'][]
  at: string
}

export interface EdgeRatesEvent {
  window: string
  rates: EdgeRate[]
  at: string
}

export type TelemetryStreamStatus = 'idle' | 'connecting' | 'live' | 'reconnecting' | 'closed'

/** Builds the telemetry stream URL (GET /api/v1/stream/telemetry). */
export function telemetryStreamUrl(opts: {
  entity?: string
  relations?: string[]
  window?: string
  interval?: number
}): string {
  const p = new URLSearchParams()
  if (opts.entity) p.set('entity', opts.entity)
  for (const r of opts.relations ?? []) p.append('relation', r)
  if (opts.window) p.set('window', opts.window)
  p.set('interval', String(Math.min(300, Math.max(15, opts.interval ?? 30))))
  return `/api/v1/stream/telemetry?${p.toString()}`
}

/**
 * The telemetry SSE stream: separate from the topology stream, so metric ticks never re-run graph layout. The first
 * snapshot arrives immediately, then every `interval` seconds; the browser reconnects on its own.
 */
export function useTelemetryStream(opts: {
  enabled: boolean
  entity?: string
  relations?: string[]
  window?: string
  interval?: number
  onTelemetry?: (e: TelemetryEvent) => void
  onEdgeRates?: (e: EdgeRatesEvent) => void
}): TelemetryStreamStatus {
  const [state, setState] = useState<{ url: string | null; status: TelemetryStreamStatus }>({
    url: null,
    status: 'idle',
  })
  const handlers = useRef({ onTelemetry: opts.onTelemetry, onEdgeRates: opts.onEdgeRates })
  useEffect(() => {
    handlers.current = { onTelemetry: opts.onTelemetry, onEdgeRates: opts.onEdgeRates }
  })
  const url = opts.enabled && (opts.entity || (opts.relations?.length ?? 0) > 0) ? telemetryStreamUrl(opts) : null
  useEffect(() => {
    if (!url) return
    const es = new EventSource(url, { withCredentials: true })
    const set = (status: TelemetryStreamStatus) => setState({ url, status })
    const parse = (ev: Event): unknown => {
      try {
        return JSON.parse((ev as MessageEvent<string>).data)
      } catch {
        return null
      }
    }
    es.addEventListener('telemetry', (ev) => {
      set('live')
      const d = parse(ev) as TelemetryEvent | null
      if (d) handlers.current.onTelemetry?.(d)
    })
    es.addEventListener('edge_rates', (ev) => {
      set('live')
      const d = parse(ev) as EdgeRatesEvent | null
      if (d) handlers.current.onEdgeRates?.(d)
    })
    es.addEventListener('closed', () => {
      es.close()
      set('closed')
    })
    es.onerror = () => set(es.readyState === EventSource.CLOSED ? 'closed' : 'reconnecting')
    return () => es.close()
  }, [url])
  const status: TelemetryStreamStatus = !url ? 'idle' : state.url === url ? state.status : 'connecting'
  return status
}
