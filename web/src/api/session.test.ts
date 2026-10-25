import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  afterResponse,
  CSRF_HEADER,
  problemHead,
  sessionFetch,
  setCsrfToken,
  setNavigateHandler,
  setReauthHandler,
  setUnauthorizedHandler,
  stepUpUrl,
} from './session'

function problem(status: number, body: Record<string, unknown>): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/problem+json' } })
}

describe('session middleware', () => {
  const fetchMock = vi.fn<(r: Request) => Promise<Response>>()
  beforeEach(() => {
    fetchMock.mockReset()
    vi.stubGlobal('fetch', fetchMock)
    setCsrfToken('csrf-1')
  })
  afterEach(() => {
    vi.unstubAllGlobals()
    setCsrfToken('')
  })

  it('sends the CSRF token on unsafe methods only', async () => {
    fetchMock.mockResolvedValue(new Response('{}', { status: 200 }))
    await sessionFetch('/api/v1/projects', { method: 'POST', body: '{}' })
    await sessionFetch('/api/v1/projects')
    expect(fetchMock.mock.calls[0]?.[0].headers.get(CSRF_HEADER)).toBe('csrf-1')
    expect(fetchMock.mock.calls[1]?.[0].headers.get(CSRF_HEADER)).toBeNull()
  })

  it('reads the problem code and action', async () => {
    const head = await problemHead(problem(403, { code: 'reauth_required', action: 'reauth' }))
    expect(head).toEqual({ code: 'reauth_required', action: 'reauth' })
    expect(await problemHead(new Response('not json', { status: 500 }))).toEqual({ code: '', action: '' })
  })

  it('prompts for re-authentication and retries once with the rotated CSRF token', async () => {
    const unregister = setReauthHandler(() => {
      setCsrfToken('csrf-2')
      return Promise.resolve(true)
    })
    fetchMock
      .mockResolvedValueOnce(problem(403, { code: 'reauth_required', action: 'reauth' }))
      .mockResolvedValueOnce(new Response('{"ok":true}', { status: 201 }))
    const res = await sessionFetch('/api/v1/me/tokens', { method: 'POST', body: '{"name":"x"}' })
    expect(res.status).toBe(201)
    expect(fetchMock).toHaveBeenCalledTimes(2)
    const retry = fetchMock.mock.calls[1]?.[0]
    expect(retry?.headers.get(CSRF_HEADER)).toBe('csrf-2')
    expect(await retry?.text()).toBe('{"name":"x"}')
    unregister()
  })

  it('returns the original response when re-authentication is cancelled', async () => {
    const unregister = setReauthHandler(() => Promise.resolve(false))
    fetchMock.mockResolvedValueOnce(problem(403, { code: 'reauth_required', action: 'reauth' }))
    const res = await sessionFetch('/api/v1/me/tokens', { method: 'POST' })
    expect(res.status).toBe(403)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    unregister()
  })

  it('shares one prompt between concurrent requests', async () => {
    let prompts = 0
    let release: (ok: boolean) => void = () => undefined
    const unregister = setReauthHandler(() => {
      prompts++
      return new Promise<boolean>((r) => (release = r))
    })
    fetchMock.mockImplementation((r: Request) =>
      Promise.resolve(
        r.headers.get(CSRF_HEADER) === 'csrf-3'
          ? new Response('{}', { status: 200 })
          : problem(403, { code: 'reauth_required', action: 'reauth' }),
      ),
    )
    const a = sessionFetch('/api/v1/a', { method: 'POST' })
    const b = sessionFetch('/api/v1/b', { method: 'DELETE' })
    await vi.waitFor(() => expect(prompts).toBe(1))
    setCsrfToken('csrf-3')
    release(true)
    const [ra, rb] = await Promise.all([a, b])
    expect(ra.status).toBe(200)
    expect(rb.status).toBe(200)
    expect(prompts).toBe(1)
    unregister()
  })

  it('navigates to the SSO step-up action instead of prompting', async () => {
    const nav = vi.fn()
    setNavigateHandler(nav)
    const req = new Request('http://localhost/api/v1/me/tokens', { method: 'POST' })
    const res = await afterResponse(
      req.clone(),
      req,
      problem(403, { code: 'reauth_required', action: '/auth/oidc/corp/start?purpose=reauth' }),
      fetchMock,
    )
    expect(res.status).toBe(403)
    expect(nav).toHaveBeenCalledWith(expect.stringContaining('/auth/oidc/corp/start?purpose=reauth&return_to='))
  })

  it('calls the unauthorized handler when the session is gone, but not for auth endpoints', async () => {
    const handler = vi.fn()
    const unregister = setUnauthorizedHandler(handler)
    const req = new Request('http://localhost/api/v1/entities')
    await afterResponse(req.clone(), req, problem(401, { code: 'unauthorized' }), fetchMock)
    expect(handler).toHaveBeenCalledTimes(1)
    const login = new Request('http://localhost/api/v1/auth/login', { method: 'POST' })
    await afterResponse(login.clone(), login, problem(401, { code: 'unauthorized' }), fetchMock)
    expect(handler).toHaveBeenCalledTimes(1)
    unregister()
  })

  it('builds step-up URLs that return to the current page', () => {
    expect(stepUpUrl('/auth/oidc/x/start?purpose=reauth', '/map?focus=a')).toBe(
      '/auth/oidc/x/start?purpose=reauth&return_to=%2Fmap%3Ffocus%3Da',
    )
    expect(stepUpUrl('/auth/oidc/x/start', '/')).toBe('/auth/oidc/x/start?return_to=%2F')
  })
})
