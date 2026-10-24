import {
  ActionIcon,
  Alert,
  Button,
  Card,
  Divider,
  Group,
  MultiSelect,
  NumberInput,
  Select,
  SimpleGrid,
  Stack,
  Switch,
  Text,
  TextInput,
  Title,
} from '@mantine/core'
import { IconPlus, IconTrash } from '@tabler/icons-react'
import { useMemo, useState } from 'react'
import { anyProjectAtLeast, projectAtLeast } from '@/api/auth'
import { errorMessage, type Descriptor, type Schemas, type SourceView } from '@/api/client'
import { useKinds, useProjects } from '@/api/queries'
import { SchemaFields, SecretFields } from '@/components/SchemaForm'
import { buildConfig, fieldsFromSchema, initialValues, validateValues, type FormValues } from '@/lib/jsonSchemaForm'
import { duration } from '@/lib/format'
import { LAYER } from '@/lib/status'

export type Policy = Schemas['Policy']

/** Reconciliation policy editor: priority, absent-fact handling, mass-removal guard, TTL, field priorities, kinds. */
export function PolicyEditor({
  value,
  onChange,
  layer,
}: {
  value: Policy
  onChange: (p: Policy) => void
  layer: string
}) {
  const kinds = useKinds()
  const fp = Object.entries(value.field_priority ?? {})
  const setFp = (rows: [string, number][]) => {
    const obj = Object.fromEntries(rows.filter(([k]) => k.trim()))
    const next = { ...value }
    if (Object.keys(obj).length) next.field_priority = obj
    else delete next.field_priority
    onChange(next)
  }
  const num = (k: 'priority' | 'max_removal_percent' | 'default_ttl_seconds', v: string | number) => {
    const { [k]: _drop, ...rest } = value
    onChange(typeof v === 'number' ? { ...rest, [k]: v } : rest)
  }
  return (
    <Stack gap="sm">
      <SimpleGrid cols={{ base: 1, sm: 2 }}>
        <NumberInput
          label="Priority"
          description={`Overrides the layer’s default precedence (${LAYER[layer]?.label ?? layer}: ${LAYER[layer]?.rank ?? '—'}). Higher wins.`}
          value={value.priority ?? ''}
          onChange={(v) => num('priority', v)}
          min={0}
          max={1000}
        />
        <Select
          label="When a complete snapshot no longer contains a fact"
          data={[
            { value: 'tombstone', label: 'Tombstone it (default)' },
            { value: 'orphan', label: 'Keep it, flag as absent' },
            { value: 'keep', label: 'Keep it' },
          ]}
          value={value.on_absent || 'tombstone'}
          onChange={(v) => onChange({ ...value, on_absent: v === 'tombstone' ? undefined : (v ?? undefined) })}
          allowDeselect={false}
        />
        <NumberInput
          label="Mass-removal guard (%)"
          description="A snapshot that would remove more than this share of the source’s facts is refused (installation default when empty)."
          value={value.max_removal_percent ?? ''}
          onChange={(v) => num('max_removal_percent', v)}
          min={0}
          max={100}
          decimalScale={1}
        />
        <NumberInput
          label="Default TTL (seconds)"
          description={
            value.default_ttl_seconds
              ? `Facts without expires_at expire after ${duration(value.default_ttl_seconds)}.`
              : 'Facts without expires_at never expire.'
          }
          value={value.default_ttl_seconds ?? ''}
          onChange={(v) => num('default_ttl_seconds', v)}
          min={0}
        />
      </SimpleGrid>
      <MultiSelect
        label="Allowed kinds"
        description="Restrict what the source may write (empty = any kind)."
        data={(kinds.data ?? []).map((k) => ({ value: k.name, label: k.title }))}
        value={value.allowed_kinds ?? []}
        onChange={(v) => {
          const next = { ...value }
          if (v.length) next.allowed_kinds = v
          else delete next.allowed_kinds
          onChange(next)
        }}
        searchable
        clearable
      />
      <Stack gap={4}>
        <Group justify="space-between">
          <div>
            <Text size="sm" fw={500}>
              Field priorities
            </Text>
            <Text size="xs" c="dimmed">
              Per JSON pointer (exact, or a prefix ending in /), e.g. /spec/ipv4 or /metadata/labels/.
            </Text>
          </div>
          <Button
            size="compact-xs"
            variant="default"
            leftSection={<IconPlus size={12} />}
            onClick={() => setFp([...fp, ['/spec/', 50]])}
          >
            Add
          </Button>
        </Group>
        {fp.map(([k, v], i) => (
          <Group key={i} gap="xs" wrap="nowrap">
            <TextInput
              aria-label="JSON pointer"
              value={k}
              onChange={(e) => {
                const nk = e.currentTarget.value
                setFp(fp.map((r, j) => (j === i ? [nk, r[1]] : r)))
              }}
              style={{ flex: 1 }}
              styles={{ input: { fontFamily: 'var(--deed-mono)' } }}
            />
            <NumberInput
              aria-label="Priority"
              value={v}
              onChange={(nv) => setFp(fp.map((r, j) => (j === i ? [r[0], typeof nv === 'number' ? nv : 0] : r)))}
              w={110}
            />
            <ActionIcon
              color="red"
              aria-label="Remove field priority"
              onClick={() => setFp(fp.filter((_, j) => j !== i))}
            >
              <IconTrash size={14} />
            </ActionIcon>
          </Group>
        ))}
      </Stack>
    </Stack>
  )
}

export interface SourceFormValue {
  body: Schemas['SourceBody']
}

/**
 * Create/edit form rendered from the provider descriptor: identity, schedule, the provider's config_schema (JSON
 * Schema → inputs), write-only secrets and the reconciliation policy.
 */
export function SourceForm({
  providers,
  source,
  initialProvider,
  pending,
  error,
  onSubmit,
  onCancel,
}: {
  providers: Descriptor[]
  source?: SourceView
  initialProvider?: string
  pending: boolean
  error: unknown
  onSubmit: (body: Schemas['SourceBody']) => void
  onCancel: () => void
}) {
  const projects = useProjects()
  const editable = (projects.data ?? []).filter((p) => projectAtLeast(p, 'admin'))
  const [provider, setProvider] = useState<string | null>(source?.provider ?? initialProvider ?? null)
  const desc = providers.find((p) => p.type === provider)
  const fields = useMemo(() => fieldsFromSchema(desc?.config_schema), [desc])
  const [project, setProject] = useState<string | null>(source?.project_id ?? editable[0]?.id ?? null)
  const [name, setName] = useState(source?.name ?? '')
  const [displayName, setDisplayName] = useState(source?.display_name ?? '')
  const [layer, setLayer] = useState<string>(source?.layer ?? desc?.default_layer ?? 'discovered')
  const [enabled, setEnabled] = useState(source?.enabled ?? true)
  const [schedule, setSchedule] = useState<number | ''>(source?.schedule_seconds ?? desc?.default_schedule_seconds ?? 0)
  const [expected, setExpected] = useState<number | ''>(source?.expected_interval_seconds ?? '')
  const [values, setValues] = useState<FormValues>(() => initialValues(fields, source?.config))
  const [secrets, setSecrets] = useState<Record<string, string>>({})
  const [policy, setPolicy] = useState<Policy>(source?.policy ?? desc?.default_policy ?? {})
  const [touched, setTouched] = useState(false)

  const pickProvider = (p: string | null) => {
    setProvider(p)
    const d = providers.find((x) => x.type === p)
    setValues(initialValues(fieldsFromSchema(d?.config_schema), undefined))
    setLayer(d?.default_layer ?? 'discovered')
    setSchedule(d?.capabilities.pull ? d.default_schedule_seconds : 0)
    setPolicy(d?.default_policy ?? {})
    setSecrets({})
  }

  const errors = touched ? validateValues(fields, values) : {}
  const nameOk = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/.test(name)
  const canPull = desc?.capabilities.pull ?? false
  const submit = () => {
    setTouched(true)
    if (!desc || Object.keys(validateValues(fields, values)).length || (!source && (!nameOk || !project))) return
    const filled = Object.fromEntries(Object.entries(secrets).filter(([, v]) => v !== ''))
    onSubmit({
      ...(source ? {} : { provider: desc.type, project: project ?? '', name }),
      display_name: displayName.trim(),
      layer: layer as Schemas['SourceBody']['layer'],
      enabled,
      schedule_seconds: canPull && typeof schedule === 'number' ? schedule : 0,
      ...(typeof expected === 'number' ? { expected_interval_seconds: expected } : {}),
      config: buildConfig(fields, values),
      policy,
      ...(Object.keys(filled).length ? { secrets: filled } : {}),
    })
  }

  if (!source && !anyProjectAtLeast(projects.data, 'admin') && projects.isSuccess) {
    return (
      <Alert color="yellow" title="Project admin required">
        Creating sources needs the admin role in at least one project.
      </Alert>
    )
  }

  return (
    <form
      onSubmit={(e) => {
        e.preventDefault()
        submit()
      }}
      aria-label={source ? 'Edit source' : 'Create source'}
    >
      <Stack gap="md">
        <Card>
          <Title order={2} fz={14} mb="sm">
            Source
          </Title>
          <SimpleGrid cols={{ base: 1, sm: 2 }}>
            <Select
              label="Provider"
              required
              data={providers.filter((p) => p.type !== 'manual').map((p) => ({ value: p.type, label: p.title }))}
              value={provider}
              onChange={pickProvider}
              disabled={!!source}
              data-testid="source-provider"
            />
            <Select
              label="Project"
              required
              data={editable.map((p) => ({ value: p.id, label: p.display_name || p.name }))}
              value={project}
              onChange={setProject}
              disabled={!!source}
            />
            <TextInput
              label="Name"
              required
              description="Lowercase slug, unique in the project."
              value={name}
              onChange={(e) => setName(e.currentTarget.value)}
              disabled={!!source}
              error={touched && !source && !nameOk ? 'Lowercase letters, digits and dashes' : undefined}
              data-testid="source-name"
            />
            <TextInput
              label="Display name"
              value={displayName}
              onChange={(e) => setDisplayName(e.currentTarget.value)}
            />
            <Select
              label="Layer"
              description="Where this source’s facts rank (declared › imported › discovered › observation)."
              data={['declared', 'imported', 'discovered', 'observation'].map((l) => ({
                value: l,
                label: LAYER[l]?.label ?? l,
              }))}
              value={layer}
              onChange={(v) => setLayer(v ?? 'discovered')}
              allowDeselect={false}
            />
            <Switch label="Enabled" checked={enabled} onChange={(e) => setEnabled(e.currentTarget.checked)} mt="lg" />
            <NumberInput
              label="Sync schedule (seconds)"
              description={
                canPull
                  ? schedule
                    ? `Every ${duration(schedule)}`
                    : '0 = manual “Sync now” only'
                  : 'Push source: external programs send data with a token.'
              }
              value={canPull ? schedule : 0}
              onChange={(v) => setSchedule(typeof v === 'number' ? v : '')}
              min={0}
              disabled={!canPull}
            />
            <NumberInput
              label="Expected interval (seconds)"
              description="Freshness turns aging, then stale, when the source is silent longer than this."
              value={expected}
              onChange={(v) => setExpected(typeof v === 'number' ? v : '')}
              min={0}
            />
          </SimpleGrid>
        </Card>
        {desc && (
          <Card>
            <Title order={2} fz={14} mb={2}>
              {desc.title} configuration
            </Title>
            <Text size="xs" c="dimmed" mb="sm">
              {desc.description}
            </Text>
            <SchemaFields fields={fields} values={values} onChange={setValues} errors={errors} />
            {desc.secret_fields.length > 0 && (
              <>
                <Divider my="sm" label="Credentials" labelPosition="left" />
                <SecretFields
                  names={desc.secret_fields}
                  stored={source?.secret_fields_set ?? []}
                  values={secrets}
                  onChange={setSecrets}
                />
              </>
            )}
          </Card>
        )}
        {desc && (
          <Card>
            <Title order={2} fz={14} mb="sm">
              Reconciliation policy
            </Title>
            <PolicyEditor value={policy} onChange={setPolicy} layer={layer} />
          </Card>
        )}
        {error !== null && error !== undefined && (
          <Alert color="red" role="alert" title="Could not save the source">
            {errorMessage(error)}
          </Alert>
        )}
        <Group justify="flex-end">
          <Button variant="default" onClick={onCancel}>
            Cancel
          </Button>
          <Button type="submit" loading={pending} disabled={!desc} data-testid="source-save">
            {source ? 'Save changes' : 'Create source'}
          </Button>
        </Group>
      </Stack>
    </form>
  )
}
