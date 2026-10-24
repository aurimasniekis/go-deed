import type { Middleware } from 'openapi-fetch'

/**
 * Browser session plumbing (ADR-005). The session cookie is HttpOnly; what the page holds is the session's CSRF token
 * (from GET /me), sent as X-Deed-CSRF on every state-changing request. Tokens are never stored in localStorage.
 *
 * - `reauth_required` / `mfa_required` (401 or 403): the problem's `action` says what to do. "reauth" runs the
 *   registered re-authentication prompt (password + optional TOTP → POST /auth/reauth); the session rotates (new CSRF
 *   token from the returned Me) and the original request is retried exactly once. Concurrent requests share one
 *   prompt. Any other action is an SSO step-up URL: the browser navigates there and returns afterwards.
 * - `unauthorized` (401) outside /auth/*: the session is gone; the unauthorized handler sends the browser to /login.
 */

export const CSRF_HEADER = 'X-Deed-CSRF'
export const API_BASE = '/api/v1'

export type ReauthCode = 'reauth_required' | 'mfa_required'
export type ReauthHandler = (code: ReauthCode) => Promise<boolean>

const AUTH_PREFIX = `${API_BASE}/auth/`

let csrf = ''
let reauthHandler: ReauthHandler | null = null
let unauthorizedHandler: () => void = defaultUnauthorized
let navigateHandler: (url: string) => void = (url) => {
  window.location.assign(url)
}
let inflight: Promise<boolean> | null = null

export function setCsrfToken(token: string | undefined | null) {
  csrf = token ?? ''
}

export function csrfToken(): string {
  return csrf
}

/** Registers the re-authentication prompt; returns an unregister function. */
export function setReauthHandler(h: ReauthHandler): () => void {
  reauthHandler = h
  return () => {
    if (reauthHandler === h) reauthHandler = null
  }
}

export function setUnauthorizedHandler(h: () => void): () => void {
  unauthorizedHandler = h
  return () => {
    if (unauthorizedHandler === h) unauthorizedHandler = defaultUnauthorized
  }
}

/** Test hook: where SSO step-up navigations go. */
export function setNavigateHandler(h: (url: string) => void) {
  navigateHandler = h
}

/** Paths that render without a session (the sign-in pages). */
export const PUBLIC_PATHS = ['/login', '/setup']

export function isPublicPath(path: string): boolean {
  return PUBLIC_PATHS.includes(path)
}

export function loginUrl(returnTo: string): string {
  return returnTo && returnTo !== '/' ? `/login?return_to=${encodeURIComponent(returnTo)}` : '/login'
}

function defaultUnauthorized() {
  const here = `${window.location.pathname}${window.location.search}`
  if (!isPublicPath(window.location.pathname)) window.location.assign(loginUrl(here))
}

const mutating = (method: string) => !['GET', 'HEAD', 'OPTIONS'].includes(method.toUpperCase())

interface ProblemHead {
  code: string
  action: string
}

/** The `code` and `action` of a problem response ("" when absent or not JSON). */
export async function problemHead(res: Response): Promise<ProblemHead> {
  try {
    const body = (await res.clone().json()) as { code?: unknown; action?: unknown }
    return {
      code: typeof body.code === 'string' ? body.code : '',
      action: typeof body.action === 'string' ? body.action : '',
    }
  } catch {
    return { code: '', action: '' }
  }
}

/** Builds the step-up URL for an SSO re-authentication action, returning to the current page. */
export function stepUpUrl(action: string, here = `${window.location.pathname}${window.location.search}`): string {
  const sep = action.includes('?') ? '&' : '?'
  return `${action}${sep}return_to=${encodeURIComponent(here)}`
}

function prompt(code: ReauthCode): Promise<boolean> {
  if (!reauthHandler) return Promise.resolve(false)
  inflight ??= reauthHandler(code).finally(() => {
    inflight = null
  })
  return inflight
}

function prepare(request: Request): Request {
  if (mutating(request.method) && csrf) request.headers.set(CSRF_HEADER, csrf)
  return request
}

/** Handles a 401/403 for request (original is an unconsumed clone made before sending). */
export async function afterResponse(
  original: Request | undefined,
  request: Request,
  response: Response,
  doFetch: (r: Request) => Promise<Response>,
): Promise<Response> {
  if (response.status !== 401 && response.status !== 403) return response
  const path = new URL(request.url, window.location.origin).pathname
  const { code, action } = await problemHead(response)
  if ((code === 'reauth_required' || code === 'mfa_required') && original && !path.startsWith(AUTH_PREFIX)) {
    if (action && action !== 'reauth' && action.startsWith('/')) {
      // SSO session: re-authenticate at the identity provider, then come back here.
      navigateHandler(stepUpUrl(action))
      return response
    }
    if (!(await prompt(code))) return response
    const retry = original.clone()
    if (mutating(retry.method)) retry.headers.set(CSRF_HEADER, csrf)
    return doFetch(retry)
  }
  if (
    response.status === 401 &&
    code === 'unauthorized' &&
    !path.startsWith(AUTH_PREFIX) &&
    path !== `${API_BASE}/me`
  ) {
    unauthorizedHandler()
  }
  return response
}

const originals = new WeakMap<Request, Request>()

/** openapi-fetch middleware for the API client. */
export const sessionMiddleware: Middleware = {
  onRequest({ request }) {
    prepare(request)
    originals.set(request, request.clone())
    return request
  },
  async onResponse({ request, response }) {
    return afterResponse(originals.get(request), request, response, (r) => fetch(r))
  },
}

/** fetch with the same session handling, for requests outside the typed client. */
export async function sessionFetch(input: string, init: RequestInit = {}): Promise<Response> {
  const request = prepare(new Request(new URL(input, window.location.origin), { credentials: 'same-origin', ...init }))
  const original = request.clone()
  const response = await fetch(request)
  return afterResponse(original, request, response, (r) => fetch(r))
}
