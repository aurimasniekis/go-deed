/**
 * A tiny Node client for the Deed API used by the seed script and Playwright: a cookie jar, the CSRF header and JSON
 * helpers. It speaks the same contract as the UI (api/openapi.json).
 */
export class ApiFailure extends Error {
  readonly status: number
  readonly body: unknown
  constructor(status: number, body: unknown, what: string) {
    super(`${what}: HTTP ${status} ${JSON.stringify(body)}`)
    this.status = status
    this.body = body
  }
}

// Node's type stripping runs these files directly: no parameter properties or enums here.
export class DeedClient {
  private cookies = new Map<string, string>()
  csrf = ''
  bearer = ''
  readonly baseURL: string

  constructor(baseURL: string) {
    this.baseURL = baseURL
  }

  /** A client that authenticates with a bearer token (source tokens, API tokens). */
  withBearer(token: string): DeedClient {
    const c = new DeedClient(this.baseURL)
    c.bearer = token
    return c
  }

  cookieHeader(): string {
    return [...this.cookies].map(([k, v]) => `${k}=${v}`).join('; ')
  }

  cookieList(): { name: string; value: string }[] {
    return [...this.cookies].map(([name, value]) => ({ name, value }))
  }

  async request<T = unknown>(
    method: string,
    path: string,
    body?: unknown,
    headers: Record<string, string> = {},
  ): Promise<T> {
    const h: Record<string, string> = { Accept: 'application/json', ...headers }
    if (body !== undefined) h['Content-Type'] = 'application/json'
    if (this.cookies.size) h.Cookie = this.cookieHeader()
    if (this.bearer) h.Authorization = `Bearer ${this.bearer}`
    if (method !== 'GET' && this.csrf) h['X-Deed-CSRF'] = this.csrf
    // Cookie sessions must prove same-origin on unsafe methods, as a browser would.
    if (method !== 'GET' && !this.bearer) h.Origin = new URL(this.baseURL).origin
    const res = await fetch(`${this.baseURL}/api/v1${path}`, {
      method,
      headers: h,
      body: body === undefined ? undefined : JSON.stringify(body),
      redirect: 'manual',
    })
    for (const c of res.headers.getSetCookie()) {
      const [pair] = c.split(';')
      const eq = pair?.indexOf('=') ?? -1
      if (!pair || eq < 0) continue
      const name = pair.slice(0, eq).trim()
      const value = pair.slice(eq + 1).trim()
      if (value === '' || /max-age=0|max-age=-1|expires=thu, 01 jan 1970/i.test(c)) this.cookies.delete(name)
      else this.cookies.set(name, value)
    }
    const text = await res.text()
    let data: unknown = undefined
    if (text) {
      try {
        data = JSON.parse(text)
      } catch {
        data = text
      }
    }
    if (!res.ok) throw new ApiFailure(res.status, data, `${method} ${path}`)
    if (data && typeof data === 'object' && 'csrf_token' in data && typeof data.csrf_token === 'string') {
      this.csrf = data.csrf_token
    }
    return data as T
  }

  get<T = unknown>(path: string) {
    return this.request<T>('GET', path)
  }

  post<T = unknown>(path: string, body?: unknown) {
    return this.request<T>('POST', path, body ?? {})
  }

  put<T = unknown>(path: string, body?: unknown, headers?: Record<string, string>) {
    return this.request<T>('PUT', path, body ?? {}, headers)
  }

  patch<T = unknown>(path: string, body?: unknown) {
    return this.request<T>('PATCH', path, body ?? {})
  }

  del<T = unknown>(path: string) {
    return this.request<T>('DELETE', path)
  }

  /** Refreshes the CSRF token (it changes whenever the session rotates). */
  async me<T = { csrf_token?: string; must_enroll: boolean; mfa_pending: boolean }>(): Promise<T> {
    return this.get<T>('/me')
  }
}
