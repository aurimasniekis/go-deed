#!/usr/bin/env node
// Adapted from Porch web/scripts/check-dist.mjs (Apache-2.0).
// Post-build checks for the embedded web UI (internal/webui/dist), run by `pnpm build` / `make web`.
// Fails when:
//   - index.html contains inline scripts, inline event handlers or javascript: URLs (the CSP is
//     `script-src 'self' 'nonce-…'`; only the server-injected nonce may authorise scripts);
//   - development-only code (TanStack Query / Router devtools) leaked into the bundle;
//   - CSS inlines non-image data: URIs (fonts/scripts as data: are blocked; img-src allows data:);
//   - a script uses eval-like constructs the CSP blocks (no 'unsafe-eval');
//   - the go:embed placeholder README.txt is missing.
import { existsSync, readdirSync, readFileSync, statSync } from 'node:fs'
import { join } from 'node:path'

const dist = new URL('../internal/webui/dist/', import.meta.url).pathname
const failures = []

if (!existsSync(join(dist, 'index.html'))) {
  console.error(`check-dist: ${dist}index.html not found — run the web build first`)
  process.exit(1)
}
if (!existsSync(join(dist, 'README.txt'))) {
  failures.push('README.txt (go:embed placeholder) is missing; web/public/README.txt must be copied into dist')
}

const html = readFileSync(join(dist, 'index.html'), 'utf8')
for (const match of html.matchAll(/<script\b([^>]*)>([\s\S]*?)<\/script>/gi)) {
  const attrs = match[1] ?? ''
  const body = (match[2] ?? '').trim()
  if (!/\bsrc\s*=/.test(attrs) || body !== '') {
    failures.push(`index.html: inline <script> is not allowed by the CSP: ${match[0].slice(0, 120)}`)
  }
}
if (/<style\b/i.test(html)) failures.push('index.html: inline <style> element (needs the CSP nonce)')
if (/\son[a-z]+\s*=/i.test(html)) failures.push('index.html: inline event handler attribute found')
if (/javascript:/i.test(html)) failures.push('index.html: javascript: URL found')
if (/https?:\/\/(?!www\.w3\.org)/i.test(html)) failures.push('index.html: external URL (fonts and scripts must be same-origin)')

function walk(dir) {
  return readdirSync(dir).flatMap((name) => {
    const p = join(dir, name)
    return statSync(p).isDirectory() ? walk(p) : [p]
  })
}

// Strings that only exist in development-only modules.
const forbidden = [
  ['tsqd-', 'TanStack Query devtools (CSS class prefix)'],
  ['ReactQueryDevtools', 'TanStack Query devtools'],
  ['tsrd-font-size', 'TanStack Router devtools (CSS)'],
  ['TanStackRouterDevtools', 'TanStack Router devtools'],
]
const assets = walk(dist).filter((p) => p.endsWith('.js') || p.endsWith('.css'))
for (const file of assets) {
  const text = readFileSync(file, 'utf8')
  for (const [needle, what] of forbidden) {
    if (text.includes(needle)) failures.push(`${file.slice(dist.length)}: contains ${what} (${JSON.stringify(needle)})`)
  }
  if (file.endsWith('.js') && /\bnew Function\s*\(|\beval\s*\(/.test(text)) {
    failures.push(`${file.slice(dist.length)}: eval/new Function would be blocked by the CSP (no 'unsafe-eval')`)
  }
  if (file.endsWith('.css') && /url\(\s*["']?data:(?!image\/)/i.test(text)) {
    failures.push(`${file.slice(dist.length)}: non-image data: URI in CSS is blocked by the CSP`)
  }
}

if (failures.length > 0) {
  console.error('check-dist: FAILED')
  for (const f of failures) console.error(`  - ${f}`)
  process.exit(1)
}
console.log(`check-dist: OK (${assets.length} assets; no inline scripts, no devtools, no eval)`)
