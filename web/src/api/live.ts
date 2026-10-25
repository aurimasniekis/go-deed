/**
 * Live updates over Server-Sent Events (ADR-009): GET /api/v1/stream.
 *
 * Events: `hello` (connected; data {seq, replay_from}), `change` (id = change-log seq; data {object_type, object_id,
 * action, project_id, summary}), `resync` (the gap was too large: refetch everything visible) and `closed`
 * (credentials revoked or expired).
 *
 * Reconnects: while the EventSource is CONNECTING the browser reconnects by itself and sends Last-Event-ID, so the
 * server replays what was missed. When the browser gives up (readyState CLOSED, e.g. a 401/503 response) this module
 * reconnects with capped exponential backoff; a fresh EventSource cannot send Last-Event-ID, so the first `hello` after
 * a manual reconnect counts as a resync.
 */

export type LiveStatus = 'connecting' | 'live' | 'reconnecting' | 'offline' | 'closed'

export interface ChangeMessage {
  seq: number
  /** entity, relation, source, kind, reltype, finding or project. */
  object_type: string
  object_id: string
  action: string
  project_id: string
  source_id?: string
  summary?: Record<string, unknown>
}

/** The subset of EventSource this module uses (tests pass a fake). */
export interface EventSourceLike {
  readonly readyState: number
  onopen: ((this: EventSourceLike, ev: Event) => unknown) | null
  onerror: ((this: EventSourceLike, ev: Event) => unknown) | null
  addEventListener(type: string, listener: (ev: MessageEvent<string>) => void): void
  close(): void
}

export interface LiveOptions {
  url?: string
  create?: (url: string) => EventSourceLike
  onChange: (msg: ChangeMessage) => void
  onResync: (reason: string) => void
  onClosed?: (reason: string) => void
  onStatus?: (status: LiveStatus) => void
  /** Delay before manual reconnect attempt n (1-based). */
  backoff?: (attempt: number) => number
  setTimer?: (fn: () => void, ms: number) => unknown
  clearTimer?: (handle: unknown) => void
}

const CLOSED = 2

export function defaultBackoff(attempt: number): number {
  return Math.min(30_000, 1000 * 2 ** Math.max(0, attempt - 1))
}

export function parseChange(data: string): ChangeMessage | null {
  try {
    const v = JSON.parse(data) as Partial<ChangeMessage>
    if (typeof v.object_type !== 'string' || typeof v.object_id !== 'string' || typeof v.action !== 'string')
      return null
    return {
      seq: typeof v.seq === 'number' ? v.seq : 0,
      object_type: v.object_type,
      object_id: v.object_id,
      action: v.action,
      project_id: typeof v.project_id === 'string' ? v.project_id : '',
      ...(typeof v.source_id === 'string' ? { source_id: v.source_id } : {}),
      summary: v.summary ?? {},
    }
  } catch {
    return null
  }
}

export class LiveStream {
  private es: EventSourceLike | null = null
  private attempts = 0
  private timer: unknown = null
  private stopped = true
  private manualReconnect = false
  private status: LiveStatus = 'connecting'
  lastSeq = 0

  constructor(private readonly opts: LiveOptions) {}

  get currentStatus(): LiveStatus {
    return this.status
  }

  start(): void {
    if (!this.stopped) return
    this.stopped = false
    this.connect()
  }

  stop(): void {
    this.stopped = true
    this.clearTimer()
    this.es?.close()
    this.es = null
  }

  private setStatus(s: LiveStatus) {
    if (this.status === s) return
    this.status = s
    this.opts.onStatus?.(s)
  }

  private clearTimer() {
    if (this.timer !== null) {
      ;(this.opts.clearTimer ?? ((h) => clearTimeout(h as ReturnType<typeof setTimeout>)))(this.timer)
      this.timer = null
    }
  }

  private connect() {
    const create =
      this.opts.create ??
      ((url: string) => new EventSource(url, { withCredentials: true }) as unknown as EventSourceLike)
    const es = create(this.opts.url ?? '/api/v1/stream')
    this.es = es
    this.setStatus(this.manualReconnect ? 'reconnecting' : 'connecting')
    es.addEventListener('hello', () => {
      this.attempts = 0
      this.setStatus('live')
      if (this.manualReconnect) {
        this.manualReconnect = false
        this.opts.onResync('reconnected')
      }
    })
    es.addEventListener('change', (ev) => {
      if (ev.lastEventId) this.lastSeq = Number(ev.lastEventId) || this.lastSeq
      const msg = parseChange(ev.data)
      if (msg) this.opts.onChange(msg)
    })
    es.addEventListener('resync', (ev) => {
      let reason = 'gap'
      try {
        reason = (JSON.parse(ev.data) as { reason?: string }).reason ?? 'gap'
      } catch {
        // keep the default reason
      }
      this.opts.onResync(reason)
    })
    es.addEventListener('closed', (ev) => {
      let reason = 'closed'
      try {
        reason = (JSON.parse(ev.data) as { reason?: string }).reason ?? 'closed'
      } catch {
        // keep the default reason
      }
      es.close()
      this.es = null
      this.setStatus('closed')
      this.opts.onClosed?.(reason)
    })
    es.onerror = () => {
      if (this.stopped || this.es !== es) return
      if (es.readyState === CLOSED) {
        // The browser gave up (HTTP error or network failure): reconnect ourselves with backoff.
        es.close()
        this.es = null
        this.scheduleReconnect()
      } else {
        // The browser retries on its own and resumes with Last-Event-ID.
        this.setStatus('reconnecting')
      }
    }
  }

  private scheduleReconnect() {
    this.attempts += 1
    this.manualReconnect = true
    this.setStatus('offline')
    const delay = (this.opts.backoff ?? defaultBackoff)(this.attempts)
    const set = this.opts.setTimer ?? ((fn: () => void, ms: number) => setTimeout(fn, ms))
    this.clearTimer()
    this.timer = set(() => {
      this.timer = null
      if (!this.stopped) this.connect()
    }, delay)
  }

  /** Reconnects now (after the user signs in again, or from the indicator's retry button). */
  reconnect(): void {
    this.clearTimer()
    this.es?.close()
    this.es = null
    this.stopped = false
    this.manualReconnect = true
    this.connect()
  }
}

/**
 * Query-key prefixes to invalidate for a change. Observation-only entity changes (`observed`) refresh entity data and
 * the dashboard but not graph topology: the map patches node data in place instead of re-running layout.
 */
export function invalidationsFor(msg: ChangeMessage): string[][] {
  switch (msg.object_type) {
    case 'entity':
      return msg.action === 'observed'
        ? [['entities'], ['dashboard']]
        : [['entities'], ['relations'], ['graph'], ['dashboard'], ['findings']]
    case 'relation':
      return [['relations'], ['graph'], ['entities', 'detail'], ['dashboard']]
    case 'source':
      return [['sources'], ['dashboard']]
    case 'finding':
      return [['findings'], ['alerts'], ['dashboard'], ['entities', 'detail']]
    case 'kind':
      return [['kinds'], ['entities']]
    case 'reltype':
      return [['relationship-types'], ['graph']]
    case 'project':
      return [['projects'], ['dashboard']]
    default:
      return [['dashboard']]
  }
}
