/**
 * A tiny Prometheus HTTP API (v1 query / query_range) for development and e2e: deterministic values derived from the
 * query text, so telemetry bindings and traffic overlays exercise Deed's real Prometheus adapter end to end without a
 * real Prometheus. Values are clearly synthetic (the seeded projects are marked synthetic).
 *
 *   node e2e/mock-prometheus.ts --port 39090
 */
import { createServer, type IncomingMessage, type Server } from 'node:http'

function hash(s: string): number {
  let h = 2166136261
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i)
    h = Math.imul(h, 16777619)
  }
  return (h >>> 0) / 4294967295
}

/** A smooth, deterministic signal per query: base level from the hash plus a slow wave. */
function valueAt(query: string, t: number): number {
  const base = 5 + hash(query) * 120
  const wave = Math.sin(t / 600 + hash(`${query}!`) * 6) * base * 0.25
  const v = base + wave
  if (/error|5xx|5\.\.|fail/i.test(query)) return Math.max(0, (v / 100) * 0.04)
  if (/latency|duration|p95|seconds/i.test(query)) return Math.max(1, v * 0.9)
  return Math.max(0, v)
}

async function params(req: IncomingMessage): Promise<URLSearchParams> {
  const url = new URL(req.url ?? '/', 'http://localhost')
  const p = new URLSearchParams(url.search)
  if (req.method === 'POST') {
    const chunks: Buffer[] = []
    for await (const c of req) chunks.push(c as Buffer)
    for (const [k, v] of new URLSearchParams(Buffer.concat(chunks).toString('utf8'))) p.set(k, v)
  }
  return p
}

export function startMockPrometheus(port: number): Promise<Server> {
  const srv = createServer((req, res) => {
    void (async () => {
      const url = new URL(req.url ?? '/', 'http://localhost')
      const p = await params(req)
      const q = p.get('query') ?? ''
      res.setHeader('Content-Type', 'application/json')
      if (url.pathname.endsWith('/api/v1/query')) {
        const t = Number(p.get('time') ?? Date.now() / 1000)
        res.end(
          JSON.stringify({
            status: 'success',
            data: { resultType: 'vector', result: [{ metric: {}, value: [t, valueAt(q, t).toFixed(4)] }] },
          }),
        )
        return
      }
      if (url.pathname.endsWith('/api/v1/query_range')) {
        const start = Number(p.get('start') ?? Date.now() / 1000 - 3600)
        const end = Number(p.get('end') ?? Date.now() / 1000)
        const step = Math.max(15, Number.parseFloat(p.get('step') ?? '60') || 60)
        const values: [number, string][] = []
        for (let t = start; t <= end && values.length < 2000; t += step) values.push([t, valueAt(q, t).toFixed(4)])
        res.end(
          JSON.stringify({
            status: 'success',
            data: { resultType: 'matrix', result: [{ metric: { instance: 'mock' }, values }] },
          }),
        )
        return
      }
      if (url.pathname.endsWith('/api/v1/status/buildinfo') || url.pathname.endsWith('/-/healthy')) {
        res.end(JSON.stringify({ status: 'success', data: { version: 'mock' } }))
        return
      }
      res.statusCode = 404
      res.end(JSON.stringify({ status: 'error', error: 'not found' }))
    })()
  })
  return new Promise((resolve) => srv.listen(port, '127.0.0.1', () => resolve(srv)))
}

if (process.argv[1]?.endsWith('mock-prometheus.ts')) {
  const i = process.argv.indexOf('--port')
  const port = Number(i >= 0 ? process.argv[i + 1] : '39090')
  void startMockPrometheus(port).then(() => console.log(`mock prometheus on http://127.0.0.1:${port}`))
}
