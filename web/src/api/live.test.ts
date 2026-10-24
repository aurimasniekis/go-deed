import { describe, expect, it, vi } from 'vitest'
import {
  defaultBackoff,
  invalidationsFor,
  LiveStream,
  parseChange,
  type EventSourceLike,
  type LiveStatus,
} from './live'

class FakeEventSource implements EventSourceLike {
  static instances: FakeEventSource[] = []
  readyState = 0
  onopen: EventSourceLike['onopen'] = null
  onerror: EventSourceLike['onerror'] = null
  closed = false
  private listeners = new Map<string, ((ev: MessageEvent<string>) => void)[]>()
  constructor(readonly url: string) {
    FakeEventSource.instances.push(this)
  }
  addEventListener(type: string, fn: (ev: MessageEvent<string>) => void) {
    this.listeners.set(type, [...(this.listeners.get(type) ?? []), fn])
  }
  close() {
    this.closed = true
    this.readyState = 2
  }
  emit(type: string, data: unknown, lastEventId = '') {
    this.readyState = 1
    for (const fn of this.listeners.get(type) ?? [])
      fn(new MessageEvent(type, { data: JSON.stringify(data), lastEventId }))
  }
  fail(readyState: number) {
    this.readyState = readyState
    this.onerror?.call(this, new Event('error'))
  }
}

function latest(): FakeEventSource {
  const es = FakeEventSource.instances[FakeEventSource.instances.length - 1]
  if (!es) throw new Error('no EventSource created')
  return es
}

function setup() {
  FakeEventSource.instances = []
  const statuses: LiveStatus[] = []
  const timers: { fn: () => void; ms: number }[] = []
  const onChange = vi.fn()
  const onResync = vi.fn()
  const onClosed = vi.fn()
  const s = new LiveStream({
    create: (url) => new FakeEventSource(url),
    onChange,
    onResync,
    onClosed,
    onStatus: (st) => statuses.push(st),
    setTimer: (fn, ms) => {
      timers.push({ fn, ms })
      return timers.length
    },
    clearTimer: () => undefined,
  })
  return { s, statuses, timers, onChange, onResync, onClosed, es: () => latest() }
}

describe('LiveStream (SSE)', () => {
  it('goes live on hello and forwards change events with their sequence', () => {
    const t = setup()
    t.s.start()
    expect(t.es().url).toBe('/api/v1/stream')
    t.es().emit('hello', { seq: 10, replay_from: 10 })
    expect(t.s.currentStatus).toBe('live')
    t.es().emit(
      'change',
      { seq: 11, object_type: 'entity', object_id: 'ent_1', action: 'updated', project_id: 'p', summary: {} },
      '11',
    )
    expect(t.onChange).toHaveBeenCalledWith(expect.objectContaining({ object_id: 'ent_1', action: 'updated' }))
    expect(t.s.lastSeq).toBe(11)
  })

  it('refetches on resync and stops on closed', () => {
    const t = setup()
    t.s.start()
    t.es().emit('resync', { reason: 'gap', seq: 99 })
    expect(t.onResync).toHaveBeenCalledWith('gap')
    t.es().emit('closed', { reason: 'credentials revoked or expired' })
    expect(t.onClosed).toHaveBeenCalledWith('credentials revoked or expired')
    expect(t.s.currentStatus).toBe('closed')
    expect(t.es().closed).toBe(true)
  })

  it('lets the browser reconnect (with Last-Event-ID) while CONNECTING', () => {
    const t = setup()
    t.s.start()
    t.es().emit('hello', { seq: 1 })
    t.es().fail(0)
    expect(t.s.currentStatus).toBe('reconnecting')
    expect(FakeEventSource.instances).toHaveLength(1)
    expect(t.timers).toHaveLength(0)
  })

  it('reconnects with backoff when the browser gives up, and treats the next hello as a resync', () => {
    const t = setup()
    t.s.start()
    t.es().emit('hello', { seq: 1 })
    t.es().fail(2)
    expect(t.s.currentStatus).toBe('offline')
    expect(t.timers[0]?.ms).toBe(1000)
    t.timers[0]?.fn()
    expect(FakeEventSource.instances).toHaveLength(2)
    expect(t.s.currentStatus).toBe('reconnecting')
    t.es().fail(2)
    expect(t.timers[1]?.ms).toBe(2000)
    t.timers[1]?.fn()
    t.es().emit('hello', { seq: 5 })
    expect(t.s.currentStatus).toBe('live')
    expect(t.onResync).toHaveBeenCalledWith('reconnected')
  })

  it('does not reconnect after stop', () => {
    const t = setup()
    t.s.start()
    const first = t.es()
    t.s.stop()
    first.fail(2)
    expect(t.timers).toHaveLength(0)
    expect(first.closed).toBe(true)
  })

  it('caps the backoff at 30 s', () => {
    expect(defaultBackoff(1)).toBe(1000)
    expect(defaultBackoff(3)).toBe(4000)
    expect(defaultBackoff(10)).toBe(30_000)
  })

  it('parses change payloads defensively', () => {
    expect(parseChange('not json')).toBeNull()
    expect(parseChange('{"object_type":1}')).toBeNull()
    expect(parseChange('{"object_type":"relation","object_id":"rel_1","action":"created"}')).toMatchObject({
      seq: 0,
      project_id: '',
      summary: {},
    })
  })

  it('maps changes to query invalidations; observations never invalidate graph topology', () => {
    const observed = invalidationsFor({
      seq: 1,
      object_type: 'entity',
      object_id: 'e',
      action: 'observed',
      project_id: 'p',
    })
    expect(observed).toEqual([['entities'], ['dashboard']])
    expect(observed.some((k) => k[0] === 'graph')).toBe(false)
    const created = invalidationsFor({
      seq: 2,
      object_type: 'entity',
      object_id: 'e',
      action: 'created',
      project_id: 'p',
    })
    expect(created).toContainEqual(['graph'])
    expect(
      invalidationsFor({ seq: 3, object_type: 'relation', object_id: 'r', action: 'created', project_id: 'p' }),
    ).toContainEqual(['graph'])
    expect(
      invalidationsFor({ seq: 4, object_type: 'source', object_id: 's', action: 'updated', project_id: 'p' }),
    ).toContainEqual(['sources'])
    expect(
      invalidationsFor({ seq: 5, object_type: 'finding', object_id: 'f', action: 'opened', project_id: 'p' }),
    ).toContainEqual(['findings'])
  })
})
