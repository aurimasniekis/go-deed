import {
  Badge,
  Fieldset,
  Group,
  NumberInput,
  PasswordInput,
  Select,
  Stack,
  Switch,
  TagsInput,
  Text,
  TextInput,
  Textarea,
} from '@mantine/core'
import type { FieldSpec, FormValues } from '@/lib/jsonSchemaForm'

/** Renders a form for fields derived from a JSON Schema (see lib/jsonSchemaForm.ts). */
export function SchemaFields({
  fields,
  values,
  onChange,
  errors,
  prefix = '',
}: {
  fields: FieldSpec[]
  values: FormValues
  onChange: (v: FormValues) => void
  errors: Record<string, string>
  prefix?: string
}) {
  if (fields.length === 0) {
    return (
      <Text size="sm" c="dimmed">
        This provider has no configuration fields.
      </Text>
    )
  }
  const set = (k: string, v: unknown) => onChange({ ...values, [k]: v })
  return (
    <Stack gap="sm">
      {fields.map((f) => {
        const key = prefix ? `${prefix}.${f.key}` : f.key
        const err = errors[key]
        const common = {
          label: f.title,
          description: f.description,
          required: f.required,
          error: err,
          'data-field': key,
        }
        switch (f.kind) {
          case 'object':
            return (
              <Fieldset key={f.key} legend={f.title} variant="filled">
                {f.description && (
                  <Text size="xs" c="dimmed" mb="xs">
                    {f.description}
                  </Text>
                )}
                <SchemaFields
                  fields={f.children ?? []}
                  values={(values[f.key] ?? {}) as FormValues}
                  onChange={(v) => set(f.key, v)}
                  errors={errors}
                  prefix={key}
                />
              </Fieldset>
            )
          case 'boolean':
            return (
              <Switch
                key={f.key}
                label={f.title}
                description={f.description}
                checked={values[f.key] === true}
                onChange={(e) => set(f.key, e.currentTarget.checked)}
                data-field={key}
              />
            )
          case 'enum':
            return (
              <Select
                key={f.key}
                {...common}
                data={f.enumValues ?? []}
                value={typeof values[f.key] === 'string' && values[f.key] !== '' ? (values[f.key] as string) : null}
                onChange={(v) => set(f.key, v ?? '')}
                clearable={!f.required}
              />
            )
          case 'number':
          case 'integer':
            return (
              <NumberInput
                key={f.key}
                {...common}
                value={typeof values[f.key] === 'number' ? (values[f.key] as number) : ''}
                onChange={(v) => set(f.key, typeof v === 'number' ? v : '')}
                min={f.minimum}
                max={f.maximum}
                allowDecimal={f.kind === 'number'}
              />
            )
          case 'string-array':
            return (
              <TagsInput
                key={f.key}
                {...common}
                value={Array.isArray(values[f.key]) ? (values[f.key] as string[]) : []}
                onChange={(v) => set(f.key, v)}
                maxTags={f.maxItems}
                splitChars={[',', ' ']}
                clearable
              />
            )
          case 'json':
            return (
              <Textarea
                key={f.key}
                {...common}
                autosize
                minRows={3}
                value={typeof values[f.key] === 'string' ? (values[f.key] as string) : ''}
                onChange={(e) => set(f.key, e.currentTarget.value)}
                styles={{ input: { fontFamily: 'var(--deed-mono)', fontSize: 12 } }}
              />
            )
          default:
            return (
              <TextInput
                key={f.key}
                {...common}
                type={f.format === 'uri' ? 'url' : 'text'}
                value={typeof values[f.key] === 'string' ? (values[f.key] as string) : ''}
                onChange={(e) => set(f.key, e.currentTarget.value)}
                maxLength={f.maxLength}
                placeholder={f.format === 'uri' ? 'https://…' : undefined}
              />
            )
        }
      })}
    </Stack>
  )
}

/**
 * Write-only secret inputs: never pre-filled; a stored secret shows "stored" and is kept unless replaced; "Clear" sends
 * an empty value for that key (the API's {} clears all).
 */
export function SecretFields({
  names,
  stored,
  values,
  onChange,
}: {
  names: string[]
  stored: string[]
  values: Record<string, string>
  onChange: (v: Record<string, string>) => void
}) {
  if (names.length === 0) return null
  return (
    <Stack gap="sm">
      {names.map((n) => {
        const isStored = stored.includes(n)
        return (
          <PasswordInput
            key={n}
            label={
              <Group gap={6} component="span">
                {n.replace(/_/g, ' ')}
                {isStored && (
                  <Badge size="xs" color="green" variant="light">
                    stored
                  </Badge>
                )}
              </Group>
            }
            description={
              isStored
                ? 'A value is stored. Leave empty to keep it; type to replace it.'
                : 'Write-only: the value is never shown again.'
            }
            placeholder={isStored ? '•••••••• (stored)' : ''}
            autoComplete="new-password"
            value={values[n] ?? ''}
            onChange={(e) => onChange({ ...values, [n]: e.currentTarget.value })}
            data-field={`secret.${n}`}
          />
        )
      })}
    </Stack>
  )
}
