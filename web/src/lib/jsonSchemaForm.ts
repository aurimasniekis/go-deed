/**
 * JSON Schema → form model for provider and adapter configuration (`config_schema`). Supports what connector schemas
 * use: strings (with format, pattern, min/max length), numbers and integers (min/max), booleans, enums, arrays of
 * strings, nested objects and defaults. Anything else falls back to a JSON editor for that field. Secret fields are not
 * part of config_schema: they are listed separately (`secret_fields`) and handled as write-only inputs.
 */

export type FieldKind = 'string' | 'number' | 'integer' | 'boolean' | 'enum' | 'string-array' | 'object' | 'json'

export interface FieldSpec {
  key: string
  path: string[]
  kind: FieldKind
  title: string
  description?: string
  required: boolean
  enumValues?: string[]
  default?: unknown
  format?: string
  minimum?: number
  maximum?: number
  minLength?: number
  maxLength?: number
  pattern?: string
  maxItems?: number
  children?: FieldSpec[]
}

interface Schema {
  type?: string | string[]
  title?: string
  description?: string
  properties?: Record<string, Schema>
  required?: string[]
  enum?: unknown[]
  default?: unknown
  format?: string
  minimum?: number
  maximum?: number
  minLength?: number
  maxLength?: number
  pattern?: string
  items?: Schema
  maxItems?: number
  oneOf?: Schema[]
  anyOf?: Schema[]
}

function asSchema(v: unknown): Schema {
  return v && typeof v === 'object' ? v : {}
}

function typeOf(s: Schema): string | undefined {
  if (Array.isArray(s.type)) return s.type.find((t) => t !== 'null')
  return s.type
}

export function titleFromKey(key: string): string {
  const t = key.replace(/[_-]+/g, ' ').replace(/([a-z])([A-Z])/g, '$1 $2')
  return t.charAt(0).toUpperCase() + t.slice(1)
}

export function fieldsFromSchema(schemaIn: unknown, path: string[] = []): FieldSpec[] {
  const schema = asSchema(schemaIn)
  const req = new Set(schema.required ?? [])
  return Object.entries(schema.properties ?? {}).map(([key, raw]) => {
    const s = asSchema(raw)
    const base = {
      key,
      path: [...path, key],
      title: s.title ?? titleFromKey(key),
      required: req.has(key),
      ...(s.description ? { description: s.description } : {}),
      ...(s.default !== undefined ? { default: s.default } : {}),
    }
    if (Array.isArray(s.enum) && s.enum.every((e) => typeof e === 'string')) {
      return { ...base, kind: 'enum' as const, enumValues: s.enum }
    }
    switch (typeOf(s)) {
      case 'string':
        return {
          ...base,
          kind: 'string' as const,
          ...(s.format ? { format: s.format } : {}),
          ...(s.minLength !== undefined ? { minLength: s.minLength } : {}),
          ...(s.maxLength !== undefined ? { maxLength: s.maxLength } : {}),
          ...(s.pattern ? { pattern: s.pattern } : {}),
        }
      case 'integer':
      case 'number':
        return {
          ...base,
          kind: typeOf(s) as 'integer' | 'number',
          ...(s.minimum !== undefined ? { minimum: s.minimum } : {}),
          ...(s.maximum !== undefined ? { maximum: s.maximum } : {}),
        }
      case 'boolean':
        return { ...base, kind: 'boolean' as const }
      case 'array':
        if (typeOf(asSchema(s.items)) === 'string') {
          return {
            ...base,
            kind: 'string-array' as const,
            ...(s.maxItems !== undefined ? { maxItems: s.maxItems } : {}),
          }
        }
        return { ...base, kind: 'json' as const }
      case 'object':
        if (s.properties) return { ...base, kind: 'object' as const, children: fieldsFromSchema(s, [...path, key]) }
        return { ...base, kind: 'json' as const }
      default:
        return { ...base, kind: 'json' as const }
    }
  })
}

export type FormValues = Record<string, unknown>

/** Form values for a schema from an existing config (falling back to defaults). */
export function initialValues(fields: FieldSpec[], config: unknown): FormValues {
  const cfg = config && typeof config === 'object' ? (config as Record<string, unknown>) : {}
  const out: FormValues = {}
  for (const f of fields) {
    const v = cfg[f.key]
    switch (f.kind) {
      case 'object':
        out[f.key] = initialValues(f.children ?? [], v)
        break
      case 'boolean':
        out[f.key] = typeof v === 'boolean' ? v : f.default === true
        break
      case 'string-array':
        out[f.key] = Array.isArray(v)
          ? v.filter((x) => typeof x === 'string')
          : Array.isArray(f.default)
            ? f.default
            : []
        break
      case 'number':
      case 'integer':
        out[f.key] = typeof v === 'number' ? v : typeof f.default === 'number' ? f.default : ''
        break
      case 'json':
        out[f.key] =
          v === undefined
            ? f.default === undefined
              ? ''
              : JSON.stringify(f.default, null, 2)
            : JSON.stringify(v, null, 2)
        break
      default:
        out[f.key] = typeof v === 'string' ? v : typeof f.default === 'string' ? f.default : ''
    }
  }
  return out
}

/** Validation errors keyed by dotted path. */
export function validateValues(fields: FieldSpec[], values: FormValues, prefix = ''): Record<string, string> {
  const errs: Record<string, string> = {}
  for (const f of fields) {
    const key = prefix ? `${prefix}.${f.key}` : f.key
    const v = values[f.key]
    const empty = v === '' || v === undefined || v === null || (Array.isArray(v) && v.length === 0)
    if (f.kind === 'object') {
      Object.assign(errs, validateValues(f.children ?? [], (v ?? {}) as FormValues, key))
      continue
    }
    if (empty) {
      if (f.required && f.kind !== 'boolean') errs[key] = 'Required'
      continue
    }
    if (f.kind === 'string' && typeof v === 'string') {
      if (f.minLength !== undefined && v.length < f.minLength) errs[key] = `At least ${f.minLength} characters`
      else if (f.maxLength !== undefined && v.length > f.maxLength) errs[key] = `At most ${f.maxLength} characters`
      else if (f.format === 'uri' && !/^[a-z][a-z0-9+.-]*:\/\/\S+$/i.test(v)) errs[key] = 'Must be an absolute URL'
      else if (f.pattern) {
        try {
          if (!new RegExp(f.pattern).test(v)) errs[key] = `Must match ${f.pattern}`
        } catch {
          // RE2 patterns the browser cannot compile are checked by the server.
        }
      }
    }
    if ((f.kind === 'number' || f.kind === 'integer') && typeof v === 'number') {
      if (f.kind === 'integer' && !Number.isInteger(v)) errs[key] = 'Must be a whole number'
      else if (f.minimum !== undefined && v < f.minimum) errs[key] = `At least ${f.minimum}`
      else if (f.maximum !== undefined && v > f.maximum) errs[key] = `At most ${f.maximum}`
    }
    if (f.kind === 'string-array' && Array.isArray(v) && f.maxItems !== undefined && v.length > f.maxItems)
      errs[key] = `At most ${f.maxItems} items`
    if (f.kind === 'json' && typeof v === 'string') {
      try {
        JSON.parse(v)
      } catch {
        errs[key] = 'Invalid JSON'
      }
    }
  }
  return errs
}

/** Builds the config object to send: empty optional values are omitted, numbers and JSON parsed. */
export function buildConfig(fields: FieldSpec[], values: FormValues): Record<string, unknown> {
  const out: Record<string, unknown> = {}
  for (const f of fields) {
    const v = values[f.key]
    switch (f.kind) {
      case 'object': {
        const o = buildConfig(f.children ?? [], (v ?? {}) as FormValues)
        if (Object.keys(o).length || f.required) out[f.key] = o
        break
      }
      case 'boolean':
        if (v === true || f.required || f.default !== undefined) out[f.key] = v === true
        break
      case 'string-array':
        if (Array.isArray(v) && v.length) out[f.key] = v
        break
      case 'number':
      case 'integer':
        if (typeof v === 'number') out[f.key] = v
        break
      case 'json':
        if (typeof v === 'string' && v.trim()) out[f.key] = JSON.parse(v) as unknown
        break
      default:
        if (typeof v === 'string' && v !== '') out[f.key] = v
    }
  }
  return out
}
