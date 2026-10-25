import { useQueryClient } from '@tanstack/react-query'
import { Store, useSelector } from '@tanstack/react-store'
import { useEffect } from 'react'
import { meKey } from './auth'
import { invalidationsFor, LiveStream, type ChangeMessage, type LiveStatus } from './live'

/** Connection state shown by the live indicator. */
export const liveStore = new Store<{ status: LiveStatus; lastChangeAt: number; changes: number }>({
  status: 'connecting',
  lastChangeAt: 0,
  changes: 0,
})

type ChangeListener = (msgs: ChangeMessage[]) => void
type ResyncListener = () => void

const changeListeners = new Set<ChangeListener>()
const resyncListeners = new Set<ResyncListener>()
let stream: LiveStream | null = null

/** Subscribes to batches of change events (the map patches node data in place with them). */
export function onLiveChanges(fn: ChangeListener): () => void {
  changeListeners.add(fn)
  return () => changeListeners.delete(fn)
}

export function onLiveResync(fn: ResyncListener): () => void {
  resyncListeners.add(fn)
  return () => resyncListeners.delete(fn)
}

export function reconnectLive() {
  stream?.reconnect()
}

export function useLiveStatus(): LiveStatus {
  return useSelector(liveStore, (s) => s.status)
}

/**
 * Owns the app's single SSE connection while a full session is active. Change events are coalesced for 300 ms and then
 * invalidate the affected query areas once; `resync` refetches every active query.
 */
export function useLiveConnection(enabled: boolean) {
  const qc = useQueryClient()
  useEffect(() => {
    if (!enabled) return
    let pending: ChangeMessage[] = []
    let flushTimer: ReturnType<typeof setTimeout> | null = null
    const flush = () => {
      flushTimer = null
      const batch = pending
      pending = []
      const seen = new Set<string>()
      for (const msg of batch) {
        for (const key of invalidationsFor(msg)) {
          const k = JSON.stringify(key)
          if (seen.has(k)) continue
          seen.add(k)
          void qc.invalidateQueries({ queryKey: key })
        }
      }
      for (const fn of changeListeners) fn(batch)
    }
    const s = new LiveStream({
      onChange: (msg) => {
        pending.push(msg)
        liveStore.setState((st) => ({ ...st, lastChangeAt: Date.now(), changes: st.changes + 1 }))
        flushTimer ??= setTimeout(flush, 300)
      },
      onResync: () => {
        void qc.invalidateQueries({ predicate: (q) => q.queryKey[0] !== 'me' })
        for (const fn of resyncListeners) fn()
      },
      onClosed: () => {
        // Credentials were revoked or expired: re-check the session (the app shell redirects to sign-in).
        void qc.invalidateQueries({ queryKey: meKey })
      },
      onStatus: (status) => liveStore.setState((st) => ({ ...st, status })),
    })
    stream = s
    s.start()
    return () => {
      if (flushTimer) clearTimeout(flushTimer)
      s.stop()
      if (stream === s) stream = null
    }
  }, [enabled, qc])
}
