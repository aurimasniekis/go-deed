import { fireEvent, screen } from '@testing-library/react'
import { useState } from 'react'
import { describe, expect, it } from 'vitest'
import { fieldsFromSchema, initialValues, type FormValues } from '@/lib/jsonSchemaForm'
import { renderWithMantine } from '@/test/render'
import { SchemaFields, SecretFields } from './SchemaForm'

const schema = {
  type: 'object',
  required: ['url'],
  properties: {
    url: { type: 'string', format: 'uri', description: 'Base URL' },
    insecure_http: { type: 'boolean', description: 'Allow http://' },
    timeout: { type: 'integer', minimum: 1 },
    level: { type: 'string', enum: ['info', 'debug'] },
    nested: { type: 'object', properties: { tenant: { type: 'string' } } },
  },
}

function Harness({ onValues }: { onValues: (v: FormValues) => void }) {
  const fields = fieldsFromSchema(schema)
  const [values, setValues] = useState<FormValues>(() => initialValues(fields, { url: 'https://prom' }))
  return (
    <SchemaFields
      fields={fields}
      values={values}
      errors={{ url: 'Must be an absolute URL' }}
      onChange={(v) => {
        setValues(v)
        onValues(v)
      }}
    />
  )
}

describe('SchemaFields', () => {
  it('renders an input per property, with labels, descriptions and errors', () => {
    let last: FormValues = {}
    renderWithMantine(<Harness onValues={(v) => (last = v)} />)
    const url = screen.getByLabelText(/Url/)
    expect(url).toHaveValue('https://prom')
    expect(screen.getByText('Base URL')).toBeInTheDocument()
    expect(screen.getByText('Must be an absolute URL')).toBeInTheDocument()
    expect(screen.getByRole('switch', { name: /Insecure http/ })).toBeInTheDocument()
    expect(screen.getByText('Nested')).toBeInTheDocument()
    fireEvent.change(url, { target: { value: 'https://mimir/prometheus' } })
    expect(last.url).toBe('https://mimir/prometheus')
    fireEvent.change(screen.getByLabelText('Tenant'), { target: { value: 'ops' } })
    expect(last.nested).toEqual({ tenant: 'ops' })
  })

  it('says when a provider has no configuration', () => {
    renderWithMantine(<SchemaFields fields={[]} values={{}} errors={{}} onChange={() => undefined} />)
    expect(screen.getByText('This provider has no configuration fields.')).toBeInTheDocument()
  })
})

describe('SecretFields', () => {
  it('never pre-fills secrets and marks stored ones', () => {
    renderWithMantine(
      <SecretFields names={['token', 'password']} stored={['token']} values={{}} onChange={() => undefined} />,
    )
    expect(screen.getByText('stored')).toBeInTheDocument()
    const inputs = screen.getAllByPlaceholderText(/stored|^$/)
    expect(inputs.length).toBeGreaterThan(0)
    for (const el of document.querySelectorAll('input[type="password"]')) expect(el).toHaveValue('')
    expect(screen.getByText(/A value is stored/)).toBeInTheDocument()
    expect(screen.getByText(/Write-only/)).toBeInTheDocument()
  })
})
