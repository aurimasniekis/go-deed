import createClient from 'openapi-fetch'
import type { components, paths } from './schema.gen'
import { API_BASE, sessionMiddleware } from './session'

/** Typed API client; types are generated from api/openapi.json (`pnpm gen:api`). */
export const api = createClient<paths>({ baseUrl: API_BASE, credentials: 'same-origin' })
api.use(sessionMiddleware)

export type Schemas = components['schemas']
export type Problem = Schemas['Problem']
export type Me = Schemas['Me']
export type SystemInfo = Schemas['SystemInfo']
export type Entity = Schemas['Entity']
export type EntityPage = Schemas['EntityPage']
export type Relation = Schemas['Relation']
export type RelType = Schemas['RelType']
export type KindView = Schemas['KindView']
export type ProjectView = Schemas['ProjectView']
export type Finding = Schemas['Finding']
export type Alert = Schemas['Alert']
export type Rule = Schemas['Rule']
export type Silence = Schemas['Silence']
export type SourceView = Schemas['SourceView']
export type Descriptor = Schemas['Descriptor']
export type Run = Schemas['Run']
export type Facet = Schemas['Facet']
export type Provenance = Schemas['Provenance']
export type ConflictRow = Schemas['ConflictRow']
export type ChangeEvent = Schemas['ChangeEvent']
export type AuditRecord = Schemas['Record']
export type View = Schemas['View']
export type GraphResult = Schemas['Result']
export type GraphNode = Schemas['Node']
export type GraphEdge = Schemas['Edge']
export type ImpactResult = Schemas['ImpactResult']
export type ImpactNode = Schemas['ImpactNode']
export type Dashboard = Schemas['Dashboard']
export type User = Schemas['User']
export type Token = Schemas['Token']
export type Bridge = Schemas['Bridge']
export type Declaration = Schemas['Declaration']

/** An API error with its HTTP status, stable code, field problems and optional re-authentication action. */
export class ApiError extends Error {
  readonly status: number
  readonly code: string
  readonly action: string
  readonly fields: { field: string; message: string }[]

  constructor(p: Partial<Problem>, status: number) {
    const fields = p.fields?.map((f) => `${f.field}: ${f.message}`).join('; ')
    const extra = p.errors
      ?.map((e) => [e.location, e.message].filter(Boolean).join(': '))
      .filter(Boolean)
      .join('; ')
    super([p.detail ?? p.title ?? `HTTP ${status}`, fields ?? extra].filter(Boolean).join(' — '))
    this.name = 'ApiError'
    this.status = status
    this.code = p.code ?? ''
    this.action = p.action ?? ''
    this.fields = p.fields ?? []
  }

  get forbidden(): boolean {
    return this.status === 403
  }

  get notFound(): boolean {
    return this.status === 404
  }
}

function problemOf(e: unknown): Partial<Problem> {
  return typeof e === 'object' && e !== null ? e : {}
}

/** Unwraps an openapi-fetch result or throws an ApiError. */
export function unwrap<T>(res: { data?: T; error?: unknown; response: Response }): T {
  if (res.error !== undefined || res.data === undefined) throw new ApiError(problemOf(res.error), res.response.status)
  return res.data
}

/** Throws an ApiError for a failed result without a body (204 endpoints). */
export function check(res: { error?: unknown; response: Response }): void {
  if (res.error !== undefined || !res.response.ok) throw new ApiError(problemOf(res.error), res.response.status)
}

/** A readable message for any thrown value. */
export function errorMessage(e: unknown): string {
  if (e instanceof Error) return e.message
  return typeof e === 'string' ? e : 'Something went wrong'
}

export function isApiError(e: unknown): e is ApiError {
  return e instanceof ApiError
}
