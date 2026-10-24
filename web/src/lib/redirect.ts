/**
 * Post-login redirect validation (adapted from Pulse web/src/lib/redirect.ts, Apache-2.0): only same-origin relative
 * paths are accepted, also after normalization, so `/login?return_to=https://evil.example`, `//evil.example` or
 * `/.//evil.example` go to "/".
 */
const AUTH_PAGES = ['/login', '/setup']

// eslint-disable-next-line no-control-regex
const CONTROL_CHARS = /[\u0000-\u001F\u007F]/

// "." and ".." segments, also percent-encoded: URL normalization would resolve "/.//evil.example" to "//evil.example".
const DOT_SEGMENT = /(^|\/)(\.|%2e){1,2}(\/|$)/i

export function safeNextPath(next: unknown, origin: string = window.location.origin): string {
  if (typeof next !== 'string' || next === '' || next.length > 2048) return '/'
  if (!next.startsWith('/') || next.startsWith('//') || next.includes('\\') || CONTROL_CHARS.test(next)) return '/'
  const rawPath = next.split(/[?#]/, 1)[0] ?? ''
  if (DOT_SEGMENT.test(rawPath)) return '/'
  let url: URL
  try {
    url = new URL(next, origin)
  } catch {
    return '/'
  }
  if (url.origin !== origin || url.pathname.startsWith('//')) return '/'
  if (AUTH_PAGES.includes(url.pathname) || url.pathname.startsWith('/api/') || url.pathname.startsWith('/auth/')) {
    return '/'
  }
  return `${url.pathname}${url.search}${url.hash}`
}
