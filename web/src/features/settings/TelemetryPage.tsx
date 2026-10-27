import {
  ActionIcon,
  Alert,
  Badge,
  Button,
  Card,
  Code,
  Divider,
  Group,
  Modal,
  MultiSelect,
  NumberInput,
  Select,
  SimpleGrid,
  Stack,
  Switch,
  Table,
  Tabs,
  Text,
  TextInput,
  Textarea,
  Title,
} from '@mantine/core'
import { notifications } from '@mantine/notifications'
import { IconPencil, IconPlugConnected, IconPlus, IconTrash } from '@tabler/icons-react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useMemo, useState } from 'react'
import { isInstallationAdmin, useMe } from '@/api/auth'
import { api, check, errorMessage, unwrap, type Schemas } from '@/api/client'
import { useKinds, useRelTypes } from '@/api/queries'
import { Page } from '@/components/AppShell'
import { ConfirmModal, PageHeader, TimeAgo } from '@/components/common'
import { EntityPicker } from '@/components/EntityPicker'
import { SchemaFields, SecretFields } from '@/components/SchemaForm'
import { EmptyState, ErrorState, LoadingRows } from '@/components/States'
import { TelemetryCard } from '@/features/telemetry/TelemetryCard'
import { TELEMETRY_WINDOWS } from '@/features/entity/queries'
import { useTitle } from '@/lib/appStore'
import { buildConfig, fieldsFromSchema, initialValues, validateValues, type FormValues } from '@/lib/jsonSchemaForm'

type Integration = Schemas['Integration']
type Binding = Schemas['Binding']
type Adapter = Schemas['AdapterInfo']

const tkeys = {
  adapters: ['telemetry', 'adapters'] as const,
  integrations: ['telemetry', 'integrations'] as const,
  bindings: ['telemetry', 'bindings'] as const,
}

function useAdapters() {
  return useQuery({
    queryKey: tkeys.adapters,
    queryFn: async () => unwrap(await api.GET('/telemetry/adapters')),
    staleTime: 10 * 60_000,
  })
}

function IntegrationModal({
  integ,
  adapters,
  onClose,
}: {
  integ: Integration | null
  adapters: Adapter[]
  onClose: () => void
}) {
  const qc = useQueryClient()
  const [adapter, setAdapter] = useState<string | null>(integ?.adapter ?? null)
  const desc = adapters.find((a) => a.type === adapter)
  const fields = useMemo(() => fieldsFromSchema(desc?.config_schema), [desc])
  const [name, setName] = useState(integ?.name ?? '')
  const [enabled, setEnabled] = useState(integ?.enabled ?? true)
  const [values, setValues] = useState<FormValues>(() =>
    initialValues(fieldsFromSchema(adapters.find((a) => a.type === integ?.adapter)?.config_schema), integ?.config),
  )
  const [secrets, setSecrets] = useState<Record<string, string>>({})
  const [touched, setTouched] = useState(false)
  const errors = touched ? validateValues(fields, values) : {}
  const save = useMutation({
    mutationFn: async () => {
      const filled = Object.fromEntries(Object.entries(secrets).filter(([, v]) => v !== ''))
      const body = {
        name,
        adapter: adapter ?? '',
        enabled,
        config: buildConfig(fields, values),
        ...(Object.keys(filled).length ? { secrets: filled } : {}),
      }
      return integ
        ? unwrap(
            await api.PATCH('/integrations/{id}', {
              params: { path: { id: integ.id }, header: { 'If-Match': String(integ.resource_version) } },
              body,
            }),
          )
        : unwrap(await api.POST('/integrations', { body }))
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: tkeys.integrations })
      onClose()
    },
  })
  return (
    <Modal
      opened
      onClose={onClose}
      title={integ ? `Edit integration ${integ.name}` : 'New telemetry integration'}
      size="lg"
      centered
    >
      <form
        onSubmit={(e) => {
          e.preventDefault()
          setTouched(true)
          if (Object.keys(validateValues(fields, values)).length === 0) save.mutate()
        }}
      >
        <Stack gap="sm">
          <SimpleGrid cols={2}>
            <Select
              label="Adapter"
              required
              data={adapters.map((a) => ({ value: a.type, label: a.title }))}
              value={adapter}
              onChange={(v) => {
                setAdapter(v)
                setValues(initialValues(fieldsFromSchema(adapters.find((a) => a.type === v)?.config_schema), undefined))
              }}
              disabled={!!integ}
              data-testid="integration-adapter"
            />
            <TextInput
              label="Name"
              required
              value={name}
              onChange={(e) => setName(e.currentTarget.value)}
              disabled={!!integ}
              data-testid="integration-name"
            />
          </SimpleGrid>
          {desc && (
            <>
              <Text size="xs" c="dimmed">
                {desc.description}
              </Text>
              <SchemaFields fields={fields} values={values} onChange={setValues} errors={errors} />
              {desc.secret_fields.length > 0 && (
                <>
                  <Divider label="Credentials (write-only)" labelPosition="left" />
                  <SecretFields
                    names={desc.secret_fields}
                    stored={integ?.has_secrets ? desc.secret_fields : []}
                    values={secrets}
                    onChange={setSecrets}
                  />
                  {integ?.has_secrets && (
                    <Text size="xs" c="dimmed">
                      Credentials are stored. Deed does not report which fields are set; leave the inputs empty to keep
                      them.
                    </Text>
                  )}
                </>
              )}
              <Text size="xs">
                <b>Validated:</b> {desc.verified}
              </Text>
            </>
          )}
          <Switch label="Enabled" checked={enabled} onChange={(e) => setEnabled(e.currentTarget.checked)} />
          {save.isError && <Alert color="red">{errorMessage(save.error)}</Alert>}
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" loading={save.isPending} disabled={!adapter || !name}>
              Save integration
            </Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  )
}

function IntegrationsTab({ admin }: { admin: boolean }) {
  const qc = useQueryClient()
  const adapters = useAdapters()
  const list = useQuery({ queryKey: tkeys.integrations, queryFn: async () => unwrap(await api.GET('/integrations')) })
  const [edit, setEdit] = useState<Integration | null | 'new'>(null)
  const [del, setDel] = useState<Integration | null>(null)
  const [testResult, setTestResult] = useState<Record<string, { ok: boolean; message: string }>>({})
  const test = useMutation({
    mutationFn: async (i: Integration) => ({
      id: i.id,
      r: unwrap(await api.POST('/integrations/{id}/test', { params: { path: { id: i.id } } })),
    }),
    onSuccess: ({ id, r }) => {
      setTestResult((s) => ({ ...s, [id]: r }))
      void qc.invalidateQueries({ queryKey: tkeys.integrations })
    },
    onError: (e) => notifications.show({ color: 'red', title: 'Test failed', message: errorMessage(e) }),
  })
  const remove = useMutation({
    mutationFn: async (i: Integration) =>
      check(await api.DELETE('/integrations/{id}', { params: { path: { id: i.id } } })),
    onSuccess: () => {
      setDel(null)
      void qc.invalidateQueries({ queryKey: tkeys.integrations })
    },
  })
  return (
    <Stack gap="sm">
      {admin && (
        <Group>
          <Button
            leftSection={<IconPlus size={14} />}
            onClick={() => setEdit('new')}
            disabled={!adapters.data}
            data-testid="new-integration"
          >
            New integration
          </Button>
        </Group>
      )}
      {list.isPending ? (
        <LoadingRows />
      ) : list.isError ? (
        <ErrorState error={list.error} what="integrations" />
      ) : list.data.length === 0 ? (
        <EmptyState title="No integrations">
          Connect Prometheus, Loki, NATS monitoring, OpenSearch or Grafana to show operational data next to the catalog.
        </EmptyState>
      ) : (
        <Card p={0}>
          <Table fz="sm" verticalSpacing={6} aria-label="Integrations">
            <Table.Thead>
              <Table.Tr>
                <Table.Th>Integration</Table.Th>
                <Table.Th>Connectivity</Table.Th>
                <Table.Th>Configuration</Table.Th>
                <Table.Th />
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {list.data.map((i) => {
                const tr = testResult[i.id]
                return (
                  <Table.Tr key={i.id}>
                    <Table.Td>
                      <Text size="sm" fw={500}>
                        {i.name}{' '}
                        {!i.enabled && (
                          <Badge size="xs" color="gray" component="span">
                            disabled
                          </Badge>
                        )}
                      </Text>
                      <Text size="xs" c="dimmed">
                        {adapters.data?.find((a) => a.type === i.adapter)?.title ?? i.adapter}
                        {i.has_secrets ? ' · credentials stored' : ''}
                      </Text>
                    </Table.Td>
                    <Table.Td>
                      {i.last_test_at ? (
                        <>
                          <Badge size="xs" variant="light" color={i.last_test_ok ? 'green' : 'red'}>
                            {i.last_test_ok ? 'reachable' : 'failing'}
                          </Badge>
                          <Text size="xs" c="dimmed">
                            tested <TimeAgo value={i.last_test_at} />
                          </Text>
                        </>
                      ) : (
                        <Text size="xs" c="dimmed">
                          not tested
                        </Text>
                      )}
                      {(tr?.message ?? i.last_error) && (
                        <Text size="xs" c={tr?.ok ? 'dimmed' : 'red'} maw={280}>
                          {tr?.message ?? i.last_error}
                        </Text>
                      )}
                    </Table.Td>
                    <Table.Td>
                      <Code fz={11}>{JSON.stringify(i.config)}</Code>
                    </Table.Td>
                    <Table.Td ta="right">
                      <Group gap={4} justify="flex-end" wrap="nowrap">
                        <Button
                          size="compact-xs"
                          variant="default"
                          leftSection={<IconPlugConnected size={12} />}
                          onClick={() => test.mutate(i)}
                          loading={test.isPending && test.variables.id === i.id}
                        >
                          Test
                        </Button>
                        {admin && (
                          <>
                            <ActionIcon aria-label={`Edit ${i.name}`} onClick={() => setEdit(i)}>
                              <IconPencil size={14} />
                            </ActionIcon>
                            <ActionIcon color="red" aria-label={`Delete ${i.name}`} onClick={() => setDel(i)}>
                              <IconTrash size={14} />
                            </ActionIcon>
                          </>
                        )}
                      </Group>
                    </Table.Td>
                  </Table.Tr>
                )
              })}
            </Table.Tbody>
          </Table>
        </Card>
      )}
      {edit !== null && adapters.data && (
        <IntegrationModal integ={edit === 'new' ? null : edit} adapters={adapters.data} onClose={() => setEdit(null)} />
      )}
      <ConfirmModal
        opened={del !== null}
        title="Delete integration"
        danger
        confirmLabel="Delete"
        pending={remove.isPending}
        onClose={() => setDel(null)}
        onConfirm={() => del && remove.mutate(del)}
      >
        Bindings that use “{del?.name}” stop returning values.
        {remove.isError && (
          <Text c="red" size="sm">
            {errorMessage(remove.error)}
          </Text>
        )}
      </ConfirmModal>
    </Stack>
  )
}

type Param = Schemas['BindingParam']

function BindingModal({
  b,
  integrations,
  onClose,
}: {
  b: Binding | null
  integrations: Integration[]
  onClose: () => void
}) {
  const qc = useQueryClient()
  const kinds = useKinds()
  const relTypes = useRelTypes()
  const adapters = useAdapters()
  const [v, setV] = useState<Schemas['BindingInput']>(() =>
    b
      ? {
          name: b.name,
          title: b.title,
          integration: b.integration_id,
          kinds: b.kinds,
          query: b.query ?? '',
          params: b.params,
          shape: b.shape as Schemas['BindingInput']['shape'],
          unit: b.unit,
          role: b.role as Schemas['BindingInput']['role'],
          relationship_type: b.relationship_type ?? '',
          thresholds: b.thresholds,
          freshness_seconds: b.freshness_seconds,
          guard: b.guard ?? '',
          when_empty: b.when_empty != null ? { value: b.when_empty } : {},
          on_map: b.on_map,
        }
      : {
          name: '',
          title: '',
          integration: integrations[0]?.id ?? '',
          kinds: [],
          query: '',
          params: [{ name: 'name', type: 'label', from: '/metadata/name' }],
          shape: 'scalar',
          unit: '',
          role: 'chart',
          freshness_seconds: 60,
          guard: '',
          when_empty: {},
          on_map: false,
        },
  )
  const integ = integrations.find((i) => i.id === v.integration || i.name === v.integration)
  const adapter = adapters.data?.find((a) => a.type === integ?.adapter)
  const params = v.params ?? []
  const setParam = (i: number, p: Partial<Param>) =>
    setV({ ...v, params: params.map((x, j) => (j === i ? { ...x, ...p } : x)) })
  const onMapAllowed = (v.shape ?? 'scalar') === 'scalar' && (v.role === 'chart' || v.role === 'health')
  const save = useMutation({
    mutationFn: async () => {
      // A binding switched to a shape or role the map can't show drops on_map instead of failing validation.
      const body = { ...v, on_map: onMapAllowed && !!v.on_map }
      return b
        ? unwrap(await api.PATCH('/bindings/{id}', { params: { path: { id: b.id } }, body }))
        : unwrap(await api.POST('/bindings', { body }))
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: tkeys.bindings })
      onClose()
    },
  })
  const edge = v.role === 'edge_rate'
  return (
    <Modal opened onClose={onClose} title={b ? `Edit binding ${b.name}` : 'New binding'} size="xl" centered>
      <Stack gap="sm">
        <SimpleGrid cols={{ base: 1, sm: 3 }}>
          <TextInput
            label="Name"
            required
            value={v.name ?? ''}
            onChange={(e) => setV({ ...v, name: e.currentTarget.value })}
            disabled={!!b}
          />
          <TextInput
            label="Title"
            required
            value={v.title ?? ''}
            onChange={(e) => setV({ ...v, title: e.currentTarget.value })}
          />
          <Select
            label="Integration"
            required
            data={integrations.map((i) => ({ value: i.id, label: `${i.name} (${i.adapter})` }))}
            value={integ?.id ?? null}
            onChange={(x) => setV({ ...v, integration: x ?? '' })}
          />
          <Select
            label="Role"
            data={[
              { value: 'chart', label: 'Chart' },
              { value: 'health', label: 'Health (thresholds)' },
              { value: 'edge_rate', label: 'Edge rate (traffic overlay)' },
              { value: 'link', label: 'Link' },
            ]}
            value={v.role ?? 'chart'}
            onChange={(x) => setV({ ...v, role: (x ?? 'chart') as Schemas['BindingInput']['role'] })}
            allowDeselect={false}
          />
          <Select
            label="Shape"
            data={adapter?.shapes ?? ['scalar', 'timeseries', 'table', 'link']}
            value={v.shape ?? 'scalar'}
            onChange={(x) => setV({ ...v, shape: (x ?? 'scalar') as Schemas['BindingInput']['shape'] })}
            allowDeselect={false}
          />
          <TextInput
            label="Unit"
            value={v.unit ?? ''}
            onChange={(e) => setV({ ...v, unit: e.currentTarget.value })}
            placeholder="req/s, ms, ratio…"
          />
          {edge ? (
            <Select
              label="Relationship type"
              required
              data={(relTypes.data ?? []).map((t) => ({ value: t.name, label: t.title }))}
              value={v.relationship_type || null}
              onChange={(x) => setV({ ...v, relationship_type: x ?? '' })}
              searchable
            />
          ) : (
            <MultiSelect
              label="Kinds"
              description="Empty = every kind"
              data={(kinds.data ?? []).map((k) => ({ value: k.name, label: k.title }))}
              value={v.kinds ?? []}
              onChange={(x) => setV({ ...v, kinds: x })}
              searchable
              clearable
            />
          )}
          <NumberInput
            label="Freshness (seconds)"
            description="How long a result is cached"
            value={v.freshness_seconds ?? 60}
            onChange={(x) => setV({ ...v, freshness_seconds: typeof x === 'number' ? x : 60 })}
            min={5}
          />
        </SimpleGrid>
        <Textarea
          label="Query template"
          description={adapter?.query_help ?? 'Use {{param}} placeholders and the built-ins {{window}} and {{step}}.'}
          value={v.query ?? ''}
          onChange={(e) => setV({ ...v, query: e.currentTarget.value })}
          autosize
          minRows={3}
          styles={{ input: { fontFamily: 'var(--deed-mono)', fontSize: 12 } }}
          spellCheck={false}
        />
        <SimpleGrid cols={{ base: 1, sm: 2 }}>
          <Textarea
            label="Guard query (optional)"
            description='The binding applies to an entity only when this query returns data, e.g. up{instance="{{instance}}"}. Without data the entity shows “not applicable”.'
            value={v.guard ?? ''}
            onChange={(e) => setV({ ...v, guard: e.currentTarget.value })}
            autosize
            minRows={1}
            styles={{ input: { fontFamily: 'var(--deed-mono)', fontSize: 12 } }}
            spellCheck={false}
            data-testid="binding-guard"
          />
          <NumberInput
            label="Value when empty (optional)"
            description="Scalar bindings: use this when the query returns nothing (after the guard passed), e.g. 0 for an error rate. Leave empty for “no data”."
            value={v.when_empty?.value ?? ''}
            onChange={(x) => setV({ ...v, when_empty: typeof x === 'number' ? { value: x } : {} })}
            disabled={(v.shape ?? 'scalar') !== 'scalar'}
            data-testid="binding-when-empty"
          />
        </SimpleGrid>
        <Switch
          label="Show on the map"
          description="Draw the value under the entity's node card (scalar chart or health bindings; up to six per entity)."
          checked={!!v.on_map}
          onChange={(e) => setV({ ...v, on_map: e.currentTarget.checked })}
          disabled={!onMapAllowed}
          data-testid="binding-on-map"
        />
        <Stack gap={4}>
          <Group justify="space-between">
            <div>
              <Text size="sm" fw={500}>
                Parameters
              </Text>
              <Text size="xs" c="dimmed">
                Filled from the entity by JSON pointer
                {edge ? ' (from:/… and to:/… for the relationship’s endpoints)' : ''}; values are type-checked and
                escaped before they reach the query.
              </Text>
            </div>
            <Button
              size="compact-xs"
              variant="default"
              leftSection={<IconPlus size={12} />}
              onClick={() => setV({ ...v, params: [...params, { name: '', type: 'label', from: '' }] })}
            >
              Add
            </Button>
          </Group>
          {params.map((p, i) => (
            <Group key={i} gap="xs" wrap="nowrap">
              <TextInput
                aria-label="Parameter name"
                placeholder="name"
                value={p.name}
                onChange={(e) => setParam(i, { name: e.currentTarget.value })}
                w={140}
              />
              <Select
                aria-label="Parameter type"
                data={['label', 'string', 'int', 'duration', 'subject']}
                value={p.type}
                onChange={(x) => setParam(i, { type: (x ?? 'label') as Param['type'] })}
                w={120}
                allowDeselect={false}
              />
              <TextInput
                aria-label="From pointer"
                placeholder={edge ? 'from:/metadata/name' : '/metadata/name'}
                value={p.from ?? ''}
                onChange={(e) => setParam(i, { from: e.currentTarget.value })}
                style={{ flex: 1 }}
                styles={{ input: { fontFamily: 'var(--deed-mono)' } }}
              />
              <TextInput
                aria-label="Default"
                placeholder="default"
                value={p.default ?? ''}
                onChange={(e) => setParam(i, { default: e.currentTarget.value })}
                w={120}
              />
              <ActionIcon
                color="red"
                aria-label="Remove parameter"
                onClick={() => setV({ ...v, params: params.filter((_, j) => j !== i) })}
              >
                <IconTrash size={14} />
              </ActionIcon>
            </Group>
          ))}
        </Stack>
        {v.role === 'health' && (
          <SimpleGrid cols={3}>
            <NumberInput
              label="Warning threshold"
              value={v.thresholds?.warning ?? ''}
              onChange={(x) =>
                setV({ ...v, thresholds: { ...v.thresholds, warning: typeof x === 'number' ? x : undefined } })
              }
              decimalScale={6}
            />
            <NumberInput
              label="Critical threshold"
              value={v.thresholds?.critical ?? ''}
              onChange={(x) =>
                setV({ ...v, thresholds: { ...v.thresholds, critical: typeof x === 'number' ? x : undefined } })
              }
              decimalScale={6}
            />
            <Select
              label="Bad when"
              data={[
                { value: 'above', label: 'above' },
                { value: 'below', label: 'below' },
              ]}
              value={v.thresholds?.direction || 'above'}
              onChange={(x) =>
                setV({ ...v, thresholds: { ...v.thresholds, direction: (x ?? 'above') as 'above' | 'below' } })
              }
              allowDeselect={false}
            />
          </SimpleGrid>
        )}
        {save.isError && <Alert color="red">{errorMessage(save.error)}</Alert>}
        <Group justify="flex-end">
          <Button variant="default" onClick={onClose}>
            Cancel
          </Button>
          <Button
            onClick={() => save.mutate()}
            loading={save.isPending}
            disabled={!v.name || !v.title || !v.integration}
          >
            Save binding
          </Button>
        </Group>
      </Stack>
    </Modal>
  )
}

function PreviewModal({ b, onClose }: { b: Binding; onClose: () => void }) {
  const [entity, setEntity] = useState<string | null>(null)
  const [relation, setRelation] = useState('')
  const [window, setWindow] = useState<string>('1h')
  const edge = b.role === 'edge_rate'
  const preview = useMutation({
    mutationFn: async () =>
      unwrap(
        await api.POST('/bindings/{id}/preview', {
          params: { path: { id: b.id } },
          body: {
            entity: edge ? relation.trim() : (entity ?? ''),
            window: window as Schemas['PreviewBindingInputBody']['window'],
          },
        }),
      ),
  })
  return (
    <Modal opened onClose={onClose} title={`Preview ${b.title}`} size="lg" centered>
      <Stack gap="sm">
        <Group align="flex-end" gap="xs">
          {edge ? (
            <TextInput
              label="Relationship ID"
              placeholder="rel_…"
              value={relation}
              onChange={(e) => setRelation(e.currentTarget.value)}
              w={300}
            />
          ) : (
            <EntityPicker
              label="Entity"
              value={entity}
              onChange={(id) => setEntity(id)}
              w={320}
              comboboxProps={{ withinPortal: true }}
            />
          )}
          <Select
            label="Window"
            data={[...TELEMETRY_WINDOWS]}
            value={window}
            onChange={(x) => setWindow(x ?? '1h')}
            w={90}
            allowDeselect={false}
          />
          <Button
            onClick={() => preview.mutate()}
            loading={preview.isPending}
            disabled={edge ? !relation.trim() : !entity}
          >
            Run preview
          </Button>
        </Group>
        {preview.isError && <Alert color="red">{errorMessage(preview.error)}</Alert>}
        {preview.data && <TelemetryCard r={preview.data} />}
      </Stack>
    </Modal>
  )
}

function BindingsTab({ admin }: { admin: boolean }) {
  const qc = useQueryClient()
  const integrations = useQuery({
    queryKey: tkeys.integrations,
    queryFn: async () => unwrap(await api.GET('/integrations')),
  })
  const list = useQuery({ queryKey: tkeys.bindings, queryFn: async () => unwrap(await api.GET('/bindings')) })
  const [edit, setEdit] = useState<Binding | null | 'new'>(null)
  const [preview, setPreview] = useState<Binding | null>(null)
  const [del, setDel] = useState<Binding | null>(null)
  const remove = useMutation({
    mutationFn: async (b: Binding) => check(await api.DELETE('/bindings/{id}', { params: { path: { id: b.id } } })),
    onSuccess: () => {
      setDel(null)
      void qc.invalidateQueries({ queryKey: tkeys.bindings })
    },
  })
  const integName = (id: string) => integrations.data?.find((i) => i.id === id)?.name ?? id
  return (
    <Stack gap="sm">
      {admin && (
        <Group>
          <Button
            leftSection={<IconPlus size={14} />}
            onClick={() => setEdit('new')}
            disabled={!integrations.data?.length}
            data-testid="new-binding"
          >
            New binding
          </Button>
          {integrations.data?.length === 0 && (
            <Text size="sm" c="dimmed">
              Create an integration first.
            </Text>
          )}
        </Group>
      )}
      {list.isPending ? (
        <LoadingRows />
      ) : list.isError ? (
        <ErrorState error={list.error} what="bindings" />
      ) : list.data.length === 0 ? (
        <EmptyState title="No bindings">
          A binding is a named query template that applies to kinds (or, for traffic overlays, to a relationship type).
        </EmptyState>
      ) : (
        <Card p={0}>
          <Table.ScrollContainer minWidth={900}>
            <Table fz="sm" verticalSpacing={6} aria-label="Bindings">
              <Table.Thead>
                <Table.Tr>
                  <Table.Th>Binding</Table.Th>
                  <Table.Th>Applies to</Table.Th>
                  <Table.Th>Integration</Table.Th>
                  <Table.Th>Query</Table.Th>
                  <Table.Th />
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {list.data.map((b) => (
                  <Table.Tr key={b.id}>
                    <Table.Td>
                      <Text size="sm" fw={500}>
                        {b.title}
                      </Text>
                      <Text size="xs" c="dimmed">
                        <span className="mono">{b.name}</span> · {b.role} · {b.shape}
                        {b.unit ? ` · ${b.unit}` : ''}
                      </Text>
                    </Table.Td>
                    <Table.Td>
                      <Text size="xs">
                        {b.role === 'edge_rate'
                          ? `${b.relationship_type ?? ''} relationships`
                          : b.kinds.length
                            ? b.kinds.join(', ')
                            : 'every kind'}
                      </Text>
                    </Table.Td>
                    <Table.Td>{integName(b.integration_id)}</Table.Td>
                    <Table.Td maw={360}>
                      {b.query ? (
                        <Code fz={11} style={{ wordBreak: 'break-all' }}>
                          {b.query}
                        </Code>
                      ) : (
                        <Text size="xs" c="dimmed">
                          hidden (admins only)
                        </Text>
                      )}
                    </Table.Td>
                    <Table.Td ta="right">
                      <Group gap={4} justify="flex-end" wrap="nowrap">
                        <Button size="compact-xs" variant="default" onClick={() => setPreview(b)}>
                          Preview
                        </Button>
                        {admin && (
                          <>
                            <ActionIcon aria-label={`Edit ${b.name}`} onClick={() => setEdit(b)}>
                              <IconPencil size={14} />
                            </ActionIcon>
                            <ActionIcon color="red" aria-label={`Delete ${b.name}`} onClick={() => setDel(b)}>
                              <IconTrash size={14} />
                            </ActionIcon>
                          </>
                        )}
                      </Group>
                    </Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
        </Card>
      )}
      {edit !== null && integrations.data && (
        <BindingModal b={edit === 'new' ? null : edit} integrations={integrations.data} onClose={() => setEdit(null)} />
      )}
      {preview && <PreviewModal b={preview} onClose={() => setPreview(null)} />}
      <ConfirmModal
        opened={del !== null}
        title="Delete binding"
        danger
        confirmLabel="Delete"
        pending={remove.isPending}
        onClose={() => setDel(null)}
        onConfirm={() => del && remove.mutate(del)}
      >
        Entities stop showing “{del?.title}”.
      </ConfirmModal>
    </Stack>
  )
}

export function TelemetryPage() {
  useTitle('Telemetry')
  const admin = isInstallationAdmin(useMe().data)
  return (
    <Page>
      <PageHeader
        title="Telemetry"
        description="Integrations connect Deed to the systems you already run; bindings are named query templates that show bounded summaries on entities and rates on map edges. Raw samples are never stored."
      />
      <Tabs defaultValue="integrations">
        <Tabs.List mb="md">
          <Tabs.Tab value="integrations">Integrations</Tabs.Tab>
          <Tabs.Tab value="bindings">Bindings</Tabs.Tab>
        </Tabs.List>
        <Tabs.Panel value="integrations">
          <IntegrationsTab admin={admin} />
        </Tabs.Panel>
        <Tabs.Panel value="bindings">
          <BindingsTab admin={admin} />
        </Tabs.Panel>
      </Tabs>
      <Title order={2} fz={14} mt="lg" mb="xs">
        Status vocabulary
      </Title>
      <Text size="sm" c="dimmed">
        Every value shows its status — measured, inferred, stale or unavailable — and when it was measured. Telemetry
        health (from health bindings) is shown separately from an entity’s structural health.
      </Text>
    </Page>
  )
}
