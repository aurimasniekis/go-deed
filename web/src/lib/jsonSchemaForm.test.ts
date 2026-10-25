import { describe, expect, it } from 'vitest'
import { buildConfig, fieldsFromSchema, initialValues, titleFromKey, validateValues } from './jsonSchemaForm'

// The GitHub provider's config_schema, abridged.
const schema = {
  type: 'object',
  required: ['api_url'],
  properties: {
    api_url: { type: 'string', format: 'uri', default: 'https://api.github.com', description: 'REST API base' },
    owners: { type: 'array', maxItems: 2, items: { type: 'string' } },
    include_forks: { type: 'boolean' },
    deployments: { type: 'boolean', default: true },
    releases: { type: 'integer', minimum: 0, maximum: 100, default: 10 },
    mode: { type: 'string', enum: ['fast', 'full'] },
    tls: { type: 'object', properties: { ca: { type: 'string' }, verify: { type: 'boolean' } }, required: ['ca'] },
    extra: { type: 'array', items: { type: 'object' } },
  },
}

describe('JSON Schema form model', () => {
  const fields = fieldsFromSchema(schema)

  it('maps schema types to field kinds', () => {
    expect(fields.map((f) => [f.key, f.kind])).toEqual([
      ['api_url', 'string'],
      ['owners', 'string-array'],
      ['include_forks', 'boolean'],
      ['deployments', 'boolean'],
      ['releases', 'integer'],
      ['mode', 'enum'],
      ['tls', 'object'],
      ['extra', 'json'],
    ])
    expect(fields[0]).toMatchObject({ required: true, format: 'uri', title: 'Api url', description: 'REST API base' })
    expect(fields[6]?.children?.map((c) => c.key)).toEqual(['ca', 'verify'])
    expect(titleFromKey('maxItemsPerRepo')).toBe('Max Items Per Repo')
  })

  it('fills initial values from config, falling back to defaults', () => {
    const v = initialValues(fields, { owners: ['acme'], tls: { ca: 'pem' } })
    expect(v).toMatchObject({
      api_url: 'https://api.github.com',
      owners: ['acme'],
      include_forks: false,
      deployments: true,
      releases: 10,
      mode: '',
      tls: { ca: 'pem', verify: false },
      extra: '',
    })
  })

  it('validates required, formats, bounds, nested objects and JSON', () => {
    const v = initialValues(fields, {})
    v.api_url = 'not a url'
    v.releases = 500
    v.owners = ['a', 'b', 'c']
    v.extra = '{bad'
    const errs = validateValues(fields, v)
    expect(errs).toMatchObject({
      api_url: 'Must be an absolute URL',
      releases: 'At most 100',
      owners: 'At most 2 items',
      'tls.ca': 'Required',
      extra: 'Invalid JSON',
    })
  })

  it('builds a config without empty optional values', () => {
    const v = initialValues(fields, {})
    v.mode = 'full'
    v.tls = { ca: 'pem', verify: false }
    v.extra = '[{"a":1}]'
    expect(buildConfig(fields, v)).toEqual({
      api_url: 'https://api.github.com',
      deployments: true,
      releases: 10,
      mode: 'full',
      tls: { ca: 'pem' },
      extra: [{ a: 1 }],
    })
  })

  it('handles schemas without properties', () => {
    expect(fieldsFromSchema({ type: 'object', additionalProperties: false })).toEqual([])
    expect(fieldsFromSchema(null)).toEqual([])
  })
})
