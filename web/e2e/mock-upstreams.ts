/**
 * Mock upstream systems for development and e2e, on one local port:
 *
 * - a Prometheus HTTP API (v1 query / query_range) with deterministic values derived from the query text, so telemetry
 *   bindings and traffic overlays exercise Deed's real Prometheus adapter end to end;
 * - a minimal GitHub REST API (rate limit, one organisation with two repositories, a release, a pull request and an
 *   issue), so the GitHub source provider can be tested, previewed and synced without the network.
 *
 * Values are clearly synthetic (the seeded projects are marked synthetic).
 *
 *   node e2e/mock-upstreams.ts --port 39090
 */
import { createServer, type IncomingMessage, type Server, type ServerResponse } from 'node:http'

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

function json(res: ServerResponse, body: unknown, status = 200) {
  res.statusCode = status
  res.setHeader('Content-Type', 'application/json')
  res.end(JSON.stringify(body))
}

function prometheus(path: string, p: URLSearchParams, res: ServerResponse): boolean {
  const q = p.get('query') ?? ''
  if (path.endsWith('/api/v1/query')) {
    const t = Number(p.get('time') ?? Date.now() / 1000)
    json(res, {
      status: 'success',
      data: { resultType: 'vector', result: [{ metric: {}, value: [t, valueAt(q, t).toFixed(4)] }] },
    })
    return true
  }
  if (path.endsWith('/api/v1/query_range')) {
    const start = Number(p.get('start') ?? Date.now() / 1000 - 3600)
    const end = Number(p.get('end') ?? Date.now() / 1000)
    const step = Math.max(15, Number.parseFloat(p.get('step') ?? '60') || 60)
    const values: [number, string][] = []
    for (let t = start; t <= end && values.length < 2000; t += step) values.push([t, valueAt(q, t).toFixed(4)])
    json(res, { status: 'success', data: { resultType: 'matrix', result: [{ metric: { instance: 'mock' }, values }] } })
    return true
  }
  if (path.endsWith('/api/v1/status/buildinfo') || path.endsWith('/-/healthy')) {
    json(res, { status: 'success', data: { version: 'mock' } })
    return true
  }
  return false
}

const ORG = 'example-org'
const now = () => new Date().toISOString()
const owner = { login: ORG, id: 1, type: 'Organization', html_url: `https://github.com/${ORG}` }
const repos = ['deploy-tools', 'status-page'].map((name, i) => ({
  id: 100 + i,
  name,
  full_name: `${ORG}/${name}`,
  html_url: `https://github.com/${ORG}/${name}`,
  description: `Synthetic repository ${name}`,
  default_branch: 'main',
  visibility: 'public',
  private: false,
  archived: false,
  disabled: false,
  fork: false,
  language: i === 0 ? 'Go' : 'TypeScript',
  topics: ['deed-demo'],
  pushed_at: now(),
  open_issues_count: 1,
  stargazers_count: 3 + i,
  owner,
}))

function github(path: string, res: ServerResponse): boolean {
  res.setHeader('X-RateLimit-Limit', '5000')
  res.setHeader('X-RateLimit-Remaining', '4990')
  res.setHeader('X-RateLimit-Reset', String(Math.floor(Date.now() / 1000) + 3600))
  if (path === '/rate_limit') {
    json(res, { resources: { core: { limit: 5000, remaining: 4990, reset: Math.floor(Date.now() / 1000) + 3600 } } })
    return true
  }
  if (path === `/orgs/${ORG}/repos` || path === `/users/${ORG}/repos`) {
    json(res, repos)
    return true
  }
  const m = /^\/repos\/([^/]+)\/([^/]+)(\/(releases|pulls|issues|deployments))?$/.exec(path)
  if (m) {
    const repo = repos.find((r) => r.full_name === `${m[1]}/${m[2]}`)
    if (!repo) {
      json(res, { message: 'Not Found' }, 404)
      return true
    }
    switch (m[4]) {
      case undefined:
        json(res, repo)
        return true
      case 'releases':
        json(res, [
          {
            id: 1,
            tag_name: 'v1.2.0',
            name: 'v1.2.0',
            html_url: `${repo.html_url}/releases/v1.2.0`,
            draft: false,
            prerelease: false,
            published_at: now(),
            created_at: now(),
          },
        ])
        return true
      case 'pulls':
      case 'issues':
        json(res, [
          {
            id: m[4] === 'pulls' ? 11 : 12,
            number: m[4] === 'pulls' ? 7 : 8,
            title: m[4] === 'pulls' ? 'Bump dependencies' : 'Status page shows stale data',
            html_url: `${repo.html_url}/${m[4] === 'pulls' ? 'pull' : 'issues'}/${m[4] === 'pulls' ? 7 : 8}`,
            state: 'open',
            user: { login: 'octo', id: 2 },
            labels: [],
            body: '',
            created_at: now(),
            updated_at: now(),
            ...(m[4] === 'issues' ? {} : { draft: false, merged_at: null }),
          },
        ])
        return true
      case 'deployments':
        json(res, [])
        return true
    }
  }
  return false
}

export function startMockUpstreams(port: number): Promise<Server> {
  const srv = createServer((req, res) => {
    void (async () => {
      const url = new URL(req.url ?? '/', 'http://localhost')
      const p = await params(req)
      if (prometheus(url.pathname, p, res)) return
      if (github(url.pathname, res)) return
      json(res, { status: 'error', error: 'not found', message: 'Not Found' }, 404)
    })()
  })
  return new Promise((resolve) => srv.listen(port, '127.0.0.1', () => resolve(srv)))
}

if (process.argv[1]?.endsWith('mock-upstreams.ts')) {
  const i = process.argv.indexOf('--port')
  const port = Number(i >= 0 ? process.argv[i + 1] : '39090')
  void startMockUpstreams(port).then(() =>
    console.log(`mock upstreams (Prometheus, GitHub) on http://127.0.0.1:${port}`),
  )
}
