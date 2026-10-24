import {
  Alert,
  Badge,
  Button,
  Card,
  Code,
  Group,
  Loader,
  SimpleGrid,
  Stack,
  Table,
  Tabs,
  Text,
  TextInput,
  NumberInput,
  Title,
  Tooltip,
} from '@mantine/core'
import { notifications } from '@mantine/notifications'
import { IconEye, IconPlayerPlay, IconPlugConnected, IconTrash } from '@tabler/icons-react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { useState } from 'react'
import { projectAtLeast } from '@/api/auth'
import { api, check, errorMessage, unwrap, type Schemas, type SourceView } from '@/api/client'
import { keys, useProjects, useProviders } from '@/api/queries'
import { Page } from '@/components/AppShell'
import { ConfirmModal, Field, JsonBlock, Mono, OneTimeSecret, PageHeader, TimeAgo } from '@/components/common'
import { AnchorLink } from '@/components/links'
import { EmptyState, ErrorState, LoadingRows } from '@/components/States'
import { FreshnessBadge, LayerBadge } from '@/components/StatusBadge'
import { useTitle } from '@/lib/appStore'
import { count, dateTime, duration, humanize, jsonValue } from '@/lib/format'
import { ProviderDetails } from './SourcesPage'
import { SourceForm } from './SourceForm'

type RunOutput = Schemas['RunOutputBody']

function DiagnosticsList({ items }: { items: Schemas['Diagnostic'][] }) {
  if (items.length === 0)
    return (
      <Text size="sm" c="dimmed">
        No diagnostics.
      </Text>
    )
  return (
    <Stack gap={4}>
      {items.map((d, i) => (
        <Group key={i} gap="xs" wrap="nowrap" align="flex-start">
          <Badge
            size="xs"
            color={d.level === 'error' ? 'red' : d.level === 'warning' ? 'yellow' : 'blue'}
            variant="light"
            miw={60}
          >
            {d.level}
          </Badge>
          <Text size="sm">
            {d.message}{' '}
            <Text span size="xs" c="dimmed" className="mono">
              {d.code}
              {d.count ? ` ×${d.count}` : ''}
            </Text>
          </Text>
        </Group>
      ))}
    </Stack>
  )
}

const summaryKeys: (keyof Schemas['Summary'])[] = [
  'created',
  'updated',
  'unchanged',
  'removed',
  'deleted',
  'stale',
  'unresolved',
  'not_found',
  'orphaned',
  'errors',
]

/** Preview / sync outcome: counts, per-item errors and (for previews) the changes that would be applied. */
export function RunOutcome({ out, preview }: { out: RunOutput; preview: boolean }) {
  const r = out.result
  return (
    <Stack gap="sm" data-testid={preview ? 'preview-result' : 'sync-result'}>
      <Group gap="xs">
        <Badge color={out.run.result === 'success' ? 'green' : out.run.result === 'partial' ? 'yellow' : 'red'}>
          {out.run.result}
        </Badge>
        <Text size="sm">
          {preview ? 'Dry run — nothing was changed.' : 'Applied.'} Started <TimeAgo value={out.run.started_at} />
          {out.run.error ? ` · ${out.run.error}` : ''}
        </Text>
      </Group>
      {r && (
        <>
          <SimpleGrid cols={{ base: 3, sm: 5 }} spacing="xs">
            {summaryKeys.map((k) => (
              <Card key={k} padding="xs">
                <Text size="xs" c="dimmed">
                  {humanize(k)}
                </Text>
                <Text fw={600} c={k === 'errors' && r.summary[k] ? 'red' : undefined}>
                  {count(r.summary[k])}
                </Text>
              </Card>
            ))}
          </SimpleGrid>
          {r.results.length > 0 && (
            <>
              <Title order={3} fz={13}>
                Items with errors or warnings
              </Title>
              <Table fz="sm" aria-label="Item results">
                <Table.Thead>
                  <Table.Tr>
                    <Table.Th>#</Table.Th>
                    <Table.Th>Type</Table.Th>
                    <Table.Th>Outcome</Table.Th>
                    <Table.Th>Detail</Table.Th>
                  </Table.Tr>
                </Table.Thead>
                <Table.Tbody>
                  {r.results.map((it) => (
                    <Table.Tr key={`${it.type}-${it.index}`}>
                      <Table.Td className="mono">{it.index}</Table.Td>
                      <Table.Td>{it.type}</Table.Td>
                      <Table.Td>
                        <Badge size="xs" color={it.error ? 'red' : 'gray'} variant="light">
                          {it.action}
                        </Badge>
                      </Table.Td>
                      <Table.Td>
                        {it.error ? (
                          <>
                            <Text size="sm" c="red">
                              {it.error.message}
                            </Text>
                            {it.error.fields?.map((f) => (
                              <Text key={f.field} size="xs">
                                <Mono>{f.field}</Mono>: {f.message}
                              </Text>
                            ))}
                          </>
                        ) : (
                          <Text size="sm">{it.detail}</Text>
                        )}
                      </Table.Td>
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            </>
          )}
          {preview && (r.changes?.length ?? 0) > 0 && (
            <>
              <Title order={3} fz={13}>
                Changes ({r.changes?.length}
                {(r.changes?.length ?? 0) >= 500 ? ', first 500' : ''})
              </Title>
              <Table fz="sm" aria-label="Changes">
                <Table.Tbody>
                  {r.changes?.slice(0, 100).map((c) => (
                    <Table.Tr key={`${c.type}-${c.id}`}>
                      <Table.Td>
                        <Badge size="xs" variant="light">
                          {c.action}
                        </Badge>
                      </Table.Td>
                      <Table.Td>{c.type}</Table.Td>
                      <Table.Td>{c.label}</Table.Td>
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            </>
          )}
          {r.removed.length > 0 && (
            <Text size="sm">
              {preview ? 'Would remove' : 'Removed'}:{' '}
              {r.removed
                .slice(0, 20)
                .map((x) => `${x.kind ?? x.type}/${x.name ?? x.id}`)
                .join(', ')}
              {r.removed.length > 20 ? ` and ${r.removed.length - 20} more` : ''}
            </Text>
          )}
        </>
      )}
      {out.run.diagnostics.length > 0 && <DiagnosticsList items={out.run.diagnostics} />}
    </Stack>
  )
}

function TokensTab({ s, admin }: { s: SourceView; admin: boolean }) {
  const qc = useQueryClient()
  const tokens = useQuery({
    queryKey: keys.sourcePart(s.id, 'tokens'),
    queryFn: async () => unwrap(await api.GET('/sources/{id}/tokens', { params: { path: { id: s.id } } })),
  })
  const [name, setName] = useState('')
  const [days, setDays] = useState<number>(90)
  const [created, setCreated] = useState<string | null>(null)
  const [revoke, setRevoke] = useState<Schemas['Token'] | null>(null)
  const create = useMutation({
    mutationFn: async () =>
      unwrap(
        await api.POST('/sources/{id}/tokens', {
          params: { path: { id: s.id } },
          body: { name: name.trim(), expires_in_days: days },
        }),
      ),
    onSuccess: (t) => {
      setCreated(t.token)
      setName('')
      void qc.invalidateQueries({ queryKey: keys.sourcePart(s.id, 'tokens') })
    },
  })
  const doRevoke = useMutation({
    mutationFn: async (t: Schemas['Token']) =>
      check(await api.DELETE('/sources/{id}/tokens/{token}', { params: { path: { id: s.id, token: t.id } } })),
    onSuccess: () => {
      setRevoke(null)
      void qc.invalidateQueries({ queryKey: keys.sourcePart(s.id, 'tokens') })
    },
  })
  return (
    <Stack gap="sm">
      {!s.push && (
        <Alert color="gray" variant="light">
          This provider pulls on a schedule; source tokens are for push sources (agents, CI jobs, services).
        </Alert>
      )}
      {admin && s.push && (
        <Card>
          <form
            onSubmit={(e) => {
              e.preventDefault()
              create.mutate()
            }}
          >
            <Group align="flex-end" gap="xs">
              <TextInput
                label="Token name"
                required
                value={name}
                onChange={(e) => setName(e.currentTarget.value)}
                placeholder="docker-01 agent"
                w={260}
                data-testid="token-name"
              />
              <NumberInput
                label="Expires in (days)"
                value={days}
                onChange={(v) => setDays(typeof v === 'number' ? v : 90)}
                min={1}
                max={365}
                w={150}
              />
              <Button type="submit" loading={create.isPending} disabled={!name.trim()} data-testid="create-token">
                Create token
              </Button>
            </Group>
            {create.isError && (
              <Text size="sm" c="red" mt="xs">
                {errorMessage(create.error)}
              </Text>
            )}
          </form>
        </Card>
      )}
      {tokens.isPending ? (
        <LoadingRows rows={3} />
      ) : tokens.isError ? (
        <ErrorState error={tokens.error} what="tokens" />
      ) : tokens.data.length === 0 ? (
        <Text size="sm" c="dimmed">
          No tokens.
        </Text>
      ) : (
        <Card p={0}>
          <Table fz="sm" aria-label="Source tokens">
            <Table.Thead>
              <Table.Tr>
                <Table.Th>Name</Table.Th>
                <Table.Th>Created</Table.Th>
                <Table.Th>Last used</Table.Th>
                <Table.Th>Expires</Table.Th>
                <Table.Th />
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {tokens.data.map((t) => (
                <Table.Tr key={t.id}>
                  <Table.Td>{t.name}</Table.Td>
                  <Table.Td>
                    <TimeAgo value={t.created_at} />
                  </Table.Td>
                  <Table.Td>
                    <TimeAgo value={t.last_used_at} />
                  </Table.Td>
                  <Table.Td>
                    {t.revoked_at ? <Badge color="gray">revoked</Badge> : <TimeAgo value={t.expires_at} />}
                  </Table.Td>
                  <Table.Td ta="right">
                    {admin && !t.revoked_at && (
                      <Button size="compact-xs" color="red" variant="subtle" onClick={() => setRevoke(t)}>
                        Revoke
                      </Button>
                    )}
                  </Table.Td>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        </Card>
      )}
      <OneTimeSecret
        opened={created !== null}
        title="Source token created"
        intro="Configure your agent or job with this token (Authorization: Bearer …). It can only call this source’s ingest endpoints."
        secret={created ?? ''}
        onClose={() => setCreated(null)}
      />
      <ConfirmModal
        opened={revoke !== null}
        title="Revoke token"
        danger
        confirmLabel="Revoke"
        pending={doRevoke.isPending}
        onClose={() => setRevoke(null)}
        onConfirm={() => revoke && doRevoke.mutate(revoke)}
      >
        Programs using “{revoke?.name}” stop working immediately; in-flight snapshots fail at their next batch.
      </ConfirmModal>
    </Stack>
  )
}

function RunsTab({ id }: { id: string }) {
  const runs = useQuery({
    queryKey: keys.sourcePart(id, 'runs'),
    queryFn: async () =>
      unwrap(await api.GET('/sources/{id}/runs', { params: { path: { id }, query: { limit: 50 } } })),
  })
  if (runs.isPending) return <LoadingRows />
  if (runs.isError) return <ErrorState error={runs.error} what="runs" />
  if (runs.data.length === 0)
    return (
      <EmptyState title="No runs yet">
        Runs appear after a scheduled sync, “Sync now”, a preview or a pushed snapshot.
      </EmptyState>
    )
  return (
    <Stack gap="xs">
      {runs.data.map((r) => (
        <Card key={r.id} padding="sm">
          <Group justify="space-between">
            <Group gap="xs">
              <Badge
                color={
                  r.result === 'success'
                    ? 'green'
                    : r.result === 'partial'
                      ? 'yellow'
                      : r.result === 'running'
                        ? 'blue'
                        : 'red'
                }
                variant="light"
              >
                {r.result}
              </Badge>
              <Text size="sm">{r.trigger}</Text>
              <Text size="xs" c="dimmed">
                {dateTime(r.started_at)}
                {r.finished_at ? ` · ${duration((Date.parse(r.finished_at) - Date.parse(r.started_at)) / 1000)}` : ''}
              </Text>
            </Group>
          </Group>
          {r.error && (
            <Text size="sm" c="red" mt={4}>
              {r.error}
            </Text>
          )}
          {r.diagnostics.length > 0 && (
            <Stack mt={6}>
              <DiagnosticsList items={r.diagnostics} />
            </Stack>
          )}
          {r.stats !== null &&
            r.stats !== undefined &&
            typeof r.stats === 'object' &&
            Object.keys(r.stats).length > 0 && (
              <Text size="xs" c="dimmed" mt={4} className="mono">
                {Object.entries(r.stats as Record<string, unknown>)
                  .map(([k, v]) => `${k}: ${jsonValue(v)}`)
                  .join(' · ')}
              </Text>
            )}
        </Card>
      ))}
    </Stack>
  )
}

export function SourceDetailPage({ id }: { id: string }) {
  const qc = useQueryClient()
  const navigate = useNavigate()
  const projects = useProjects()
  const providers = useProviders()
  const q = useQuery({
    queryKey: keys.source(id),
    queryFn: async () => unwrap(await api.GET('/sources/{id}', { params: { path: { id } } })),
  })
  const s = q.data
  useTitle(s ? s.display_name || s.name : 'Source')
  const [tab, setTab] = useState<string | null>('overview')
  const [diag, setDiag] = useState<Schemas['DiagnosticsOutputBody'] | null>(null)
  const [outcome, setOutcome] = useState<{ out: RunOutput; preview: boolean } | null>(null)
  const [del, setDel] = useState(false)
  const invalidate = () => {
    void qc.invalidateQueries({ queryKey: ['sources'] })
    void qc.invalidateQueries({ queryKey: ['dashboard'] })
  }
  const test = useMutation({
    mutationFn: async () => unwrap(await api.POST('/sources/{id}/test', { params: { path: { id } } })),
    onSuccess: setDiag,
    onError: (e) => notifications.show({ color: 'red', title: 'Test failed', message: errorMessage(e) }),
  })
  const preview = useMutation({
    mutationFn: async () => unwrap(await api.POST('/sources/{id}/preview', { params: { path: { id } } })),
    onSuccess: (out) => setOutcome({ out, preview: true }),
    onError: (e) => notifications.show({ color: 'red', title: 'Preview failed', message: errorMessage(e) }),
  })
  const sync = useMutation({
    mutationFn: async () => unwrap(await api.POST('/sources/{id}/sync', { params: { path: { id } } })),
    onSuccess: (out) => {
      setOutcome({ out, preview: false })
      invalidate()
    },
    onError: (e) => notifications.show({ color: 'red', title: 'Sync failed', message: errorMessage(e) }),
  })
  const update = useMutation({
    mutationFn: async (body: Schemas['SourceBody']) =>
      unwrap(
        await api.PATCH('/sources/{id}', {
          params: { path: { id }, header: { 'If-Match': String(s?.resource_version ?? '') } },
          body,
        }),
      ),
    onSuccess: () => {
      invalidate()
      notifications.show({ color: 'green', message: 'Source saved' })
      setTab('overview')
    },
  })
  const remove = useMutation({
    mutationFn: async () => check(await api.DELETE('/sources/{id}', { params: { path: { id } } })),
    onSuccess: () => {
      invalidate()
      void navigate({ to: '/sources' })
    },
    onError: (e) => notifications.show({ color: 'red', title: 'Could not delete', message: errorMessage(e) }),
  })

  if (q.isPending)
    return (
      <Page>
        <LoadingRows rows={8} />
      </Page>
    )
  if (!s)
    return (
      <Page>
        <ErrorState error={q.error} what="this source" onRetry={() => void q.refetch()} />
      </Page>
    )
  const project = projects.data?.find((p) => p.id === s.project_id)
  const admin = projectAtLeast(project, 'admin') && !s.system
  const desc = providers.data?.find((p) => p.type === s.provider)

  return (
    <Page>
      <PageHeader
        title={s.display_name || s.name}
        description={
          <>
            {s.provider_title || s.provider} source in {project?.display_name ?? s.project_id}
            {s.system ? ' · system source (managed by Deed)' : ''}
          </>
        }
        actions={
          <>
            <Button
              variant="default"
              leftSection={<IconPlugConnected size={14} />}
              onClick={() => test.mutate()}
              loading={test.isPending}
              data-testid="source-test"
            >
              Test connection
            </Button>
            {s.pull && (
              <>
                <Button
                  variant="default"
                  leftSection={<IconEye size={14} />}
                  onClick={() => preview.mutate()}
                  loading={preview.isPending}
                  data-testid="source-preview"
                >
                  Preview
                </Button>
                {admin && (
                  <Button
                    leftSection={<IconPlayerPlay size={14} />}
                    onClick={() => sync.mutate()}
                    loading={sync.isPending || s.running}
                    data-testid="source-sync"
                  >
                    Sync now
                  </Button>
                )}
              </>
            )}
            {!s.pull && (
              <Tooltip label="Push sources are written by external programs; preview and sync apply to pull providers.">
                <Button
                  variant="default"
                  leftSection={<IconEye size={14} />}
                  onClick={() => preview.mutate()}
                  loading={preview.isPending}
                  data-testid="source-preview"
                >
                  Preview
                </Button>
              </Tooltip>
            )}
            {admin && (
              <Button color="red" variant="subtle" leftSection={<IconTrash size={14} />} onClick={() => setDel(true)}>
                Delete
              </Button>
            )}
          </>
        }
      />
      {diag && (
        <Alert
          color={diag.ok ? 'green' : 'red'}
          title={diag.ok ? 'Connection test passed' : 'Connection test failed'}
          mb="sm"
          withCloseButton
          onClose={() => setDiag(null)}
          data-testid="test-result"
        >
          <DiagnosticsList items={diag.diagnostics} />
        </Alert>
      )}
      {outcome && (
        <Card mb="sm">
          <Group justify="space-between" mb="xs">
            <Title order={2} fz={14}>
              {outcome.preview ? 'Preview (dry run)' : 'Sync result'}
            </Title>
            <Button size="compact-xs" variant="subtle" onClick={() => setOutcome(null)}>
              Dismiss
            </Button>
          </Group>
          <RunOutcome out={outcome.out} preview={outcome.preview} />
        </Card>
      )}
      <Tabs value={tab} onChange={setTab}>
        <Tabs.List mb="md">
          <Tabs.Tab value="overview">Overview</Tabs.Tab>
          {admin && <Tabs.Tab value="configure">Configure</Tabs.Tab>}
          <Tabs.Tab value="runs">Runs</Tabs.Tab>
          <Tabs.Tab value="tokens">Tokens</Tabs.Tab>
          <Tabs.Tab value="provider">Provider</Tabs.Tab>
        </Tabs.List>
        <Tabs.Panel value="overview">
          <SimpleGrid cols={{ base: 1, md: 2 }}>
            <Card>
              <Title order={2} fz={14} mb="xs">
                Status
              </Title>
              <Field label="Freshness">
                <FreshnessBadge state={s.freshness} />
              </Field>
              <Field label="Last success">
                <TimeAgo value={s.last_success_at} />
              </Field>
              <Field label="Last run">
                <TimeAgo value={s.last_run_at} />
              </Field>
              <Field label="Next run">{s.next_run_at ? <TimeAgo value={s.next_run_at} /> : '—'}</Field>
              <Field label="Last heartbeat">
                <TimeAgo value={s.last_heartbeat_at} />
              </Field>
              <Field label="Last error">
                {s.last_error ? (
                  <Text size="sm" c="red">
                    {s.last_error}
                  </Text>
                ) : (
                  '—'
                )}
              </Field>
              <Field label="Generation">{s.last_generation}</Field>
              <Field label="State">
                {s.enabled ? 'Enabled' : 'Disabled'}
                {s.running ? ' · running now' : ''}
              </Field>
            </Card>
            <Card>
              <Title order={2} fz={14} mb="xs">
                Settings
              </Title>
              <Field label="Name">
                <Mono>{s.name}</Mono>
              </Field>
              <Field label="Layer">
                <LayerBadge layer={s.layer} />
              </Field>
              <Field label="Schedule">
                {s.pull ? (s.schedule_seconds ? `every ${duration(s.schedule_seconds)}` : 'manual') : 'push (token)'}
              </Field>
              <Field label="Expected interval">{duration(s.expected_interval_seconds)}</Field>
              <Field label="Credentials">{s.has_secrets ? `stored: ${s.secret_fields_set.join(', ')}` : 'none'}</Field>
              <Field label="Policy">
                <JsonBlock value={s.policy} maxHeight={160} />
              </Field>
              <Field label="Configuration">
                <JsonBlock value={s.config} maxHeight={200} />
              </Field>
              <AnchorLink to="/inventory" search={{ source: s.id }} size="sm" mt="xs">
                Entities with facts from this source
              </AnchorLink>
            </Card>
          </SimpleGrid>
        </Tabs.Panel>
        {admin && (
          <Tabs.Panel value="configure">
            {providers.isPending ? (
              <Loader size="sm" />
            ) : (
              <SourceForm
                providers={providers.data ?? []}
                source={s}
                pending={update.isPending}
                error={update.error}
                onSubmit={(b) => update.mutate(b)}
                onCancel={() => setTab('overview')}
              />
            )}
          </Tabs.Panel>
        )}
        <Tabs.Panel value="runs">
          <RunsTab id={s.id} />
        </Tabs.Panel>
        <Tabs.Panel value="tokens">
          <TokensTab s={s} admin={admin} />
        </Tabs.Panel>
        <Tabs.Panel value="provider">
          <Card>{desc ? <ProviderDetails d={desc} /> : <Text size="sm">Unknown provider {s.provider}.</Text>}</Card>
        </Tabs.Panel>
      </Tabs>
      <ConfirmModal
        opened={del}
        title="Delete source"
        danger
        confirmLabel="Delete source"
        typeToConfirm={s.name}
        pending={remove.isPending}
        onClose={() => setDel(false)}
        onConfirm={() => remove.mutate()}
      >
        Entities keep the facts of other sources; entities left without any facts are tombstoned. The source’s tokens
        are revoked. <Code>{s.name}</Code>
      </ConfirmModal>
    </Page>
  )
}
