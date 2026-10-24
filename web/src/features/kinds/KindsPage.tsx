import {
  Alert,
  Badge,
  Button,
  Card,
  Code,
  ColorInput,
  Group,
  Modal,
  SegmentedControl,
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
import { IconPlus, IconTrash, IconUpload } from '@tabler/icons-react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { useMemo, useState } from 'react'
import { isInstallationAdmin, useMe } from '@/api/auth'
import { api, check, errorMessage, unwrap, type KindView } from '@/api/client'
import { keys, useKinds } from '@/api/queries'
import { Page } from '@/components/AppShell'
import { ConfirmModal, Field, JsonBlock, Mono, PageHeader, TimeAgo } from '@/components/common'
import { RefIcon } from '@/components/icons'
import { AnchorLink, ButtonLink } from '@/components/links'
import { EmptyState, ErrorState, LoadingRows } from '@/components/States'
import { SupportBadge } from '@/components/StatusBadge'
import { useTitle } from '@/lib/appStore'
import { count } from '@/lib/format'
import { parseJson, SchemaEditor, SCHEMA_TEMPLATE } from './SchemaEditor'

export function SupportLevels({ k }: { k: KindView }) {
  const s = k.support
  return (
    <Group gap={4}>
      {s.modeling && <SupportBadge level="modeling" />}
      {s.terraform.length > 0 && <SupportBadge level="terraform" detail={s.terraform.join(', ')} />}
      {s.runtime.length > 0 && <SupportBadge level="runtime" detail={s.runtime.join(', ')} />}
      {s.native.length > 0 && <SupportBadge level="native" detail={s.native.join(', ')} />}
    </Group>
  )
}

export function KindsPage() {
  useTitle('Kinds')
  const kinds = useKinds()
  const admin = isInstallationAdmin(useMe().data)
  const [filter, setFilter] = useState<'all' | 'builtin' | 'custom'>('all')
  const counts = useQuery({
    queryKey: ['dashboard', 'kinds-counts'],
    queryFn: async () => unwrap(await api.GET('/dashboard')).counts.by_kind,
    staleTime: 60_000,
  })
  const groups = useMemo(() => {
    const list = (kinds.data ?? []).filter((k) =>
      filter === 'all' ? true : filter === 'builtin' ? k.builtin : !k.builtin,
    )
    const m = new Map<string, KindView[]>()
    for (const k of list) m.set(k.group || 'other', [...(m.get(k.group || 'other') ?? []), k])
    return [...m.entries()].sort((a, b) => a[0].localeCompare(b[0]))
  }, [kinds.data, filter])
  return (
    <Page>
      <PageHeader
        title="Kinds"
        description="Entity types: built-in kinds ship with Deed; custom kinds have versioned JSON Schemas. Support levels say how each kind gets into the catalog."
        actions={
          admin && (
            <ButtonLink to="/kinds/new" leftSection={<IconPlus size={14} />} data-testid="new-kind">
              New custom kind
            </ButtonLink>
          )
        }
      />
      <SegmentedControl
        mb="sm"
        value={filter}
        onChange={(v) => setFilter(v as typeof filter)}
        data={[
          { value: 'all', label: 'All' },
          { value: 'builtin', label: 'Built-in' },
          { value: 'custom', label: 'Custom' },
        ]}
      />
      {kinds.isPending ? (
        <LoadingRows />
      ) : kinds.isError ? (
        <ErrorState error={kinds.error} what="kinds" />
      ) : groups.length === 0 ? (
        <EmptyState title="No custom kinds yet">
          Model anything Deed does not know with a custom kind and a JSON Schema.
        </EmptyState>
      ) : (
        <Stack gap="md">
          {groups.map(([group, list]) => (
            <Card key={group} p={0}>
              <Text fw={600} size="sm" p="sm" tt="capitalize">
                {group}
              </Text>
              <Table fz="sm" verticalSpacing={6} aria-label={`${group} kinds`}>
                <Table.Thead>
                  <Table.Tr>
                    <Table.Th>Kind</Table.Th>
                    <Table.Th>Version</Table.Th>
                    <Table.Th>Support</Table.Th>
                    <Table.Th ta="right">Entities</Table.Th>
                  </Table.Tr>
                </Table.Thead>
                <Table.Tbody>
                  {list.map((k) => (
                    <Table.Tr key={k.name}>
                      <Table.Td>
                        <Group gap={8} wrap="nowrap">
                          <span
                            style={{
                              width: 4,
                              height: 24,
                              background: k.color || 'var(--mantine-color-gray-4)',
                              borderRadius: 2,
                            }}
                            aria-hidden
                          />
                          <RefIcon iconRef={k.icon} size={18} />
                          <div>
                            <AnchorLink to="/kinds/$name" params={{ name: k.name }} size="sm" fw={500}>
                              {k.title}
                            </AnchorLink>
                            <Text size="xs" c="dimmed" className="mono">
                              {k.name}
                              {!k.builtin && ' · custom'}
                              {k.deprecated && ' · deprecated'}
                            </Text>
                          </div>
                        </Group>
                      </Table.Td>
                      <Table.Td>
                        <Mono>{k.storage_version}</Mono>
                        {k.versions.length > 1 && (
                          <Text size="xs" c="dimmed">
                            {k.versions.length} versions
                          </Text>
                        )}
                      </Table.Td>
                      <Table.Td>
                        <SupportLevels k={k} />
                      </Table.Td>
                      <Table.Td ta="right">
                        <AnchorLink to="/inventory" search={{ kind: k.name }} size="sm">
                          {count(counts.data?.[k.name] ?? 0)}
                        </AnchorLink>
                      </Table.Td>
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            </Card>
          ))}
        </Stack>
      )}
    </Page>
  )
}

interface KindMeta {
  name: string
  title: string
  plural: string
  group: string
  icon: string
  color: string
  description: string
  identityKeys: string
  deprecated: boolean
}

function KindMetaFields({ v, onChange, isNew }: { v: KindMeta; onChange: (v: KindMeta) => void; isNew: boolean }) {
  const nameOk = /^([a-z0-9]([a-z0-9.-]{0,61}[a-z0-9])?\/)?[a-z][a-z0-9_]{0,62}$/.test(v.name)
  return (
    <SimpleGrid cols={{ base: 1, sm: 2 }}>
      <TextInput
        label="Name"
        required
        description="lower_snake_case, optionally prefixed with a domain (example.com/thing)"
        value={v.name}
        onChange={(e) => onChange({ ...v, name: e.currentTarget.value })}
        disabled={!isNew}
        error={isNew && v.name && !nameOk ? 'Invalid kind name' : undefined}
        data-testid="kind-name"
      />
      <TextInput
        label="Title"
        required
        value={v.title}
        onChange={(e) => onChange({ ...v, title: e.currentTarget.value })}
        data-testid="kind-title"
      />
      <TextInput label="Plural" value={v.plural} onChange={(e) => onChange({ ...v, plural: e.currentTarget.value })} />
      <TextInput
        label="Group"
        value={v.group}
        onChange={(e) => onChange({ ...v, group: e.currentTarget.value })}
        placeholder="infra, software, ops…"
      />
      <TextInput
        label="Icon"
        description="pack:name — tabler:server, simple-icons:docker…"
        value={v.icon}
        onChange={(e) => onChange({ ...v, icon: e.currentTarget.value })}
        leftSection={<RefIcon iconRef={v.icon || 'tabler:cube'} size={14} />}
      />
      <ColorInput label="Colour" value={v.color} onChange={(c) => onChange({ ...v, color: c })} format="hex" />
      <Textarea
        label="Description"
        value={v.description}
        onChange={(e) => onChange({ ...v, description: e.currentTarget.value })}
        autosize
        minRows={2}
      />
      <Textarea
        label="Identity keys"
        description="One key per line; a key is one or more JSON pointers separated by commas (e.g. /spec/hostname). Used to merge facts from different sources."
        value={v.identityKeys}
        onChange={(e) => onChange({ ...v, identityKeys: e.currentTarget.value })}
        autosize
        minRows={2}
        styles={{ input: { fontFamily: 'var(--deed-mono)', fontSize: 12 } }}
      />
    </SimpleGrid>
  )
}

function identityKeys(text: string): string[][] {
  return text
    .split('\n')
    .map((l) =>
      l
        .split(',')
        .map((p) => p.trim())
        .filter(Boolean),
    )
    .filter((k) => k.length > 0)
}

export function NewKindPage() {
  useTitle('New kind')
  const qc = useQueryClient()
  const navigate = useNavigate()
  const [meta, setMeta] = useState<KindMeta>({
    name: '',
    title: '',
    plural: '',
    group: 'custom',
    icon: 'tabler:puzzle',
    color: '#7048e8',
    description: '',
    identityKeys: '',
    deprecated: false,
  })
  const [schema, setSchema] = useState(SCHEMA_TEMPLATE)
  const [sample, setSample] = useState('{\n  "endpoint": "https://example.com"\n}')
  const parsed = parseJson(schema)
  const create = useMutation({
    mutationFn: async () =>
      unwrap(
        await api.POST('/kinds', {
          body: {
            name: meta.name,
            title: meta.title,
            plural: meta.plural || undefined,
            group: meta.group || undefined,
            icon: meta.icon || undefined,
            color: meta.color || undefined,
            description: meta.description || undefined,
            ...(identityKeys(meta.identityKeys).length ? { identity_keys: identityKeys(meta.identityKeys) } : {}),
            schema: parsed.value,
          },
        }),
      ),
    onSuccess: (k) => {
      void qc.invalidateQueries({ queryKey: keys.kinds() })
      notifications.show({ color: 'green', message: `Kind ${k.name} created` })
      void navigate({ to: '/kinds/$name', params: { name: k.name } })
    },
  })
  return (
    <Page>
      <PageHeader
        title="New custom kind"
        description="Custom kinds are versioned: v1 starts with this schema; later versions must stay compatible or migrate explicitly."
      />
      <Stack gap="md">
        <Card>
          <KindMetaFields v={meta} onChange={setMeta} isNew />
        </Card>
        <Card>
          <SchemaEditor
            kindName={meta.name}
            schema={schema}
            onSchema={setSchema}
            sample={sample}
            onSample={setSample}
          />
        </Card>
        {create.isError && (
          <Alert color="red" role="alert">
            {errorMessage(create.error)}
          </Alert>
        )}
        <Group justify="flex-end">
          <ButtonLink to="/kinds" variant="default">
            Cancel
          </ButtonLink>
          <Button
            onClick={() => create.mutate()}
            loading={create.isPending}
            disabled={!meta.name || !meta.title || !!parsed.error}
            data-testid="create-kind"
          >
            Create kind
          </Button>
        </Group>
      </Stack>
    </Page>
  )
}

function PublishVersionModal({ k, opened, onClose }: { k: KindView; opened: boolean; onClose: () => void }) {
  const qc = useQueryClient()
  const current = k.versions.find((v) => v.version === k.storage_version)
  const [schema, setSchema] = useState(JSON.stringify(current?.schema ?? {}, null, 2))
  const [sample, setSample] = useState('')
  const [breaking, setBreaking] = useState(false)
  const [migration, setMigration] = useState<'revalidate' | 'mark_invalid'>('revalidate')
  const parsed = parseJson(schema)
  const publish = useMutation({
    mutationFn: async () =>
      unwrap(
        await api.POST('/kinds/{name}/versions', {
          params: { path: { name: k.name }, header: { 'If-Match': String(k.resource_version) } },
          body: { schema: parsed.value, breaking, ...(breaking ? { migration } : {}) },
        }),
      ),
    onSuccess: (r) => {
      void qc.invalidateQueries({ queryKey: keys.kinds() })
      notifications.show({
        color: r.invalid ? 'yellow' : 'green',
        title: `Published ${k.name} ${r.version}`,
        message: `${r.revalidated} entities revalidated${r.invalid ? `, ${r.invalid} now invalid` : ''}.`,
      })
      onClose()
    },
  })
  return (
    <Modal opened={opened} onClose={onClose} title={`Publish a new version of ${k.title}`} size="90%" centered>
      <Stack gap="sm">
        <SchemaEditor kindName={k.name} schema={schema} onSchema={setSchema} sample={sample} onSample={setSample} />
        <Switch
          label="Breaking change"
          description="Needed when the dry run reports compatibility problems."
          checked={breaking}
          onChange={(e) => setBreaking(e.currentTarget.checked)}
        />
        {breaking && (
          <Select
            label="Migration of existing entities"
            data={[
              { value: 'revalidate', label: 'Revalidate — refuse to publish if any entity fails' },
              { value: 'mark_invalid', label: 'Mark invalid — publish; failing entities are flagged (never dropped)' },
            ]}
            value={migration}
            onChange={(v) => setMigration((v ?? 'revalidate') as typeof migration)}
            allowDeselect={false}
          />
        )}
        {publish.isError && (
          <Alert color="red" role="alert">
            {errorMessage(publish.error)}
          </Alert>
        )}
        <Group justify="flex-end">
          <Button variant="default" onClick={onClose}>
            Cancel
          </Button>
          <Button
            leftSection={<IconUpload size={14} />}
            onClick={() => publish.mutate()}
            loading={publish.isPending}
            disabled={!!parsed.error}
          >
            Publish version
          </Button>
        </Group>
      </Stack>
    </Modal>
  )
}

export function KindDetailPage({ name }: { name: string }) {
  const qc = useQueryClient()
  const navigate = useNavigate()
  const admin = isInstallationAdmin(useMe().data)
  const q = useQuery({
    queryKey: keys.kind(name),
    queryFn: async () => unwrap(await api.GET('/kinds/{name}', { params: { path: { name } } })),
  })
  const k = q.data
  useTitle(k?.title ?? 'Kind')
  const [publishOpen, setPublishOpen] = useState(false)
  const [editOpen, setEditOpen] = useState(false)
  const [delOpen, setDelOpen] = useState(false)
  const [meta, setMeta] = useState<KindMeta | null>(null)
  const update = useMutation({
    mutationFn: async (m: KindMeta) =>
      unwrap(
        await api.PATCH('/kinds/{name}', {
          params: { path: { name }, header: { 'If-Match': String(k?.resource_version ?? '') } },
          body: {
            title: m.title,
            plural: m.plural,
            group: m.group,
            icon: m.icon,
            color: m.color,
            description: m.description,
            deprecated: m.deprecated,
            identity_keys: identityKeys(m.identityKeys),
          },
        }),
      ),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: keys.kinds() })
      setEditOpen(false)
    },
  })
  const remove = useMutation({
    mutationFn: async () => check(await api.DELETE('/kinds/{name}', { params: { path: { name } } })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: keys.kinds() })
      void navigate({ to: '/kinds' })
    },
    onError: (e) => notifications.show({ color: 'red', title: 'Could not delete', message: errorMessage(e) }),
  })
  if (q.isPending)
    return (
      <Page>
        <LoadingRows />
      </Page>
    )
  if (!k)
    return (
      <Page>
        <ErrorState error={q.error} what="this kind" />
      </Page>
    )
  const versions = [...k.versions].sort((a, b) => b.created_at.localeCompare(a.created_at))
  return (
    <Page>
      <PageHeader
        title={
          <Group gap="sm">
            <RefIcon iconRef={k.icon} size={24} />
            {k.title}
          </Group>
        }
        description={k.description}
        actions={
          <>
            <ButtonLink to="/inventory" search={{ kind: k.name }} variant="default">
              Entities
            </ButtonLink>
            {admin && !k.builtin && (
              <>
                <Button
                  variant="default"
                  onClick={() => {
                    setMeta({
                      name: k.name,
                      title: k.title,
                      plural: k.plural,
                      group: k.group,
                      icon: k.icon,
                      color: k.color,
                      description: k.description,
                      deprecated: k.deprecated,
                      identityKeys: k.identity_keys.map((x) => x.join(', ')).join('\n'),
                    })
                    setEditOpen(true)
                  }}
                >
                  Edit
                </Button>
                <Button onClick={() => setPublishOpen(true)} leftSection={<IconUpload size={14} />}>
                  Publish new version
                </Button>
                <Button
                  color="red"
                  variant="subtle"
                  leftSection={<IconTrash size={14} />}
                  onClick={() => setDelOpen(true)}
                >
                  Delete
                </Button>
              </>
            )}
          </>
        }
      />
      <SimpleGrid cols={{ base: 1, md: 2 }} mb="md">
        <Card>
          <Field label="Name">
            <Mono>{k.name}</Mono>
          </Field>
          <Field label="Group">{k.group}</Field>
          <Field label="Origin">{k.builtin ? 'Built-in (versioned by Deed releases)' : 'Custom'}</Field>
          <Field label="Storage version">
            <Mono>{k.storage_version}</Mono>
          </Field>
          <Field label="Identity keys">
            {k.identity_keys.length
              ? k.identity_keys.map((x) => (
                  <div key={x.join()}>
                    <Mono>{x.join(' + ')}</Mono>
                  </div>
                ))
              : '—'}
          </Field>
          <Field label="Updated">
            <TimeAgo value={k.updated_at} />
          </Field>
        </Card>
        <Card>
          <Title order={2} fz={14} mb="xs">
            How entities of this kind get into Deed
          </Title>
          <SupportLevels k={k} />
          <Stack gap={4} mt="sm">
            {k.support.terraform.length > 0 && <Text size="xs">Terraform: {k.support.terraform.join(', ')}</Text>}
            {k.support.runtime.length > 0 && <Text size="xs">Runtime: {k.support.runtime.join(', ')}</Text>}
            {k.support.native.length > 0 && <Text size="xs">Native integrations: {k.support.native.join(', ')}</Text>}
            {k.support.modeling && <Text size="xs">Can always be declared through the API and the UI.</Text>}
          </Stack>
        </Card>
      </SimpleGrid>
      <Title order={2} fz={16} mb="xs">
        Schema versions
      </Title>
      <Tabs defaultValue={versions[0]?.version}>
        <Tabs.List>
          {versions.map((v) => (
            <Tabs.Tab
              key={v.version}
              value={v.version}
              rightSection={
                v.version === k.storage_version ? (
                  <Badge size="xs">storage</Badge>
                ) : v.breaking ? (
                  <Badge size="xs" color="orange">
                    breaking
                  </Badge>
                ) : null
              }
            >
              {v.version}
            </Tabs.Tab>
          ))}
        </Tabs.List>
        {versions.map((v) => (
          <Tabs.Panel key={v.version} value={v.version} pt="sm">
            <Text size="xs" c="dimmed" mb="xs">
              Published <TimeAgo value={v.created_at} />
              {v.breaking ? ` · breaking · migration ${v.migration ?? '—'}` : ''}
            </Text>
            <JsonBlock value={v.schema} maxHeight={480} />
          </Tabs.Panel>
        ))}
      </Tabs>
      {publishOpen && <PublishVersionModal k={k} opened onClose={() => setPublishOpen(false)} />}
      <Modal opened={editOpen} onClose={() => setEditOpen(false)} title={`Edit ${k.title}`} size="xl" centered>
        {meta && (
          <Stack gap="sm">
            <KindMetaFields v={meta} onChange={setMeta} isNew={false} />
            <Switch
              label="Deprecated"
              checked={meta.deprecated}
              onChange={(e) => setMeta({ ...meta, deprecated: e.currentTarget.checked })}
            />
            {update.isError && <Alert color="red">{errorMessage(update.error)}</Alert>}
            <Group justify="flex-end">
              <Button variant="default" onClick={() => setEditOpen(false)}>
                Cancel
              </Button>
              <Button onClick={() => update.mutate(meta)} loading={update.isPending}>
                Save
              </Button>
            </Group>
          </Stack>
        )}
      </Modal>
      <ConfirmModal
        opened={delOpen}
        title="Delete kind"
        danger
        confirmLabel="Delete kind"
        typeToConfirm={k.name}
        pending={remove.isPending}
        onClose={() => setDelOpen(false)}
        onConfirm={() => remove.mutate()}
      >
        Only unused custom kinds can be deleted. <Code>{k.name}</Code>
      </ConfirmModal>
    </Page>
  )
}
