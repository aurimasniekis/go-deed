import {
  ActionIcon,
  Alert,
  Button,
  Card,
  Group,
  MultiSelect,
  Popover,
  Stack,
  Table,
  Text,
  TextInput,
  UnstyledButton,
} from '@mantine/core'
import { notifications } from '@mantine/notifications'
import { IconInfoCircle, IconRefresh, IconSearch, IconVersions } from '@tabler/icons-react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { isInstallationAdmin, useMe } from '@/api/auth'
import { api, errorMessage, unwrap, type Schemas } from '@/api/client'
import { useProjects } from '@/api/queries'
import { Page } from '@/components/AppShell'
import { EntityLink, JsonBlock, Mono, PageHeader, TimeAgo } from '@/components/common'
import { EmptyState, ErrorState, LoadingRows } from '@/components/States'
import { MetaBadge } from '@/components/StatusBadge'
import { useProjectFilter, useTitle } from '@/lib/appStore'
import { dateTime } from '@/lib/format'
import { VERSION_STATUS } from '@/lib/status'
import { compact, decodeList, encodeList, type VersionsSearch } from '@/lib/urlState'

export type VersionCheck = Schemas['Check']

const ORDER = ['eol', 'update_available', 'current', 'unknown', 'not_applicable'] as const

function statusMeta(s: string) {
  return VERSION_STATUS[s] ?? { label: s, tone: 'gray' as const, icon: 'help-circle', description: '' }
}

const SOURCE_LABEL: Record<string, string> = {
  catalog_release: 'repository release in the catalog',
  eol_api: 'end-of-life API',
}

/**
 * Version intelligence: for every entity with a declared or observed version, the newest known release and
 * end-of-life status, with the evidence used. Results also open version findings.
 */
export function VersionsPage({
  search,
  setSearch,
}: {
  search: VersionsSearch
  setSearch: (s: VersionsSearch) => void
}) {
  useTitle('Versions')
  const qc = useQueryClient()
  const admin = isInstallationAdmin(useMe().data)
  const projects = useProjects()
  const globalProjects = useProjectFilter()
  // GET /versions without ?project= returns nothing for principals that can read every project, so ask per project
  // and merge (a handful of requests; projects are few).
  const scope = globalProjects.length ? globalProjects : (projects.data ?? []).map((p) => p.id)
  const q = useQuery({
    queryKey: ['versions', { scope }],
    enabled: projects.isSuccess,
    queryFn: async () => {
      const pages = await Promise.all(
        (scope.length ? scope : [undefined]).map(async (project) => {
          const res = await api.GET('/versions', { params: { query: compact({ project, limit: 1000 }) } })
          if (res.response.status === 404) return null
          return unwrap(res)
        }),
      )
      const first = pages[0]
      if (!first) return null
      return { ...first, items: pages.flatMap((p) => p?.items ?? []) }
    },
  })
  const recheck = useMutation({
    mutationFn: async () => unwrap(await api.POST('/versions/check')),
    onSuccess: (r) => {
      notifications.show({ color: 'green', message: `Checked ${String(r.checked)} entities` })
      void qc.invalidateQueries({ queryKey: ['versions'] })
      void qc.invalidateQueries({ queryKey: ['findings'] })
    },
    onError: (e) =>
      notifications.show({ color: 'red', title: 'Could not re-check versions', message: errorMessage(e) }),
  })
  const statuses = decodeList(search.status)
  const patch = (p: Partial<VersionsSearch>) => setSearch(compact({ ...search, ...p }))
  const all = (q.data?.items ?? []).filter((c) => globalProjects.length === 0 || globalProjects.includes(c.project_id))
  const needle = (search.q ?? '').toLowerCase()
  const rows = all.filter(
    (c) =>
      (statuses.length === 0 || statuses.includes(c.status)) &&
      (!needle || `${c.name} ${c.kind} ${c.product ?? ''}`.toLowerCase().includes(needle)),
  )
  const counts = new Map<string, number>()
  for (const c of all) counts.set(c.status, (counts.get(c.status) ?? 0) + 1)
  const projectName = (id: string) => projects.data?.find((p) => p.id === id)?.display_name ?? id

  return (
    <Page wide>
      <PageHeader
        title="Versions"
        description="Newest known release and end-of-life status for every entity with a declared or observed version. Outdated and end-of-life versions also open findings."
        actions={
          admin &&
          q.data?.enabled && (
            <Button
              variant="default"
              leftSection={<IconRefresh size={14} />}
              loading={recheck.isPending}
              onClick={() => recheck.mutate()}
              data-testid="recheck-versions"
            >
              Re-check now
            </Button>
          )
        }
      />
      {q.isPending ? (
        <LoadingRows />
      ) : q.isError ? (
        <ErrorState error={q.error} what="version checks" onRetry={() => void q.refetch()} />
      ) : q.data === null ? (
        <EmptyState title="Version checks are not available" icon={<IconVersions size={22} />}>
          This server does not expose version intelligence.
        </EmptyState>
      ) : (
        <Stack gap="sm">
          {!q.data.enabled && (
            <Alert color="yellow" title="Version checks are disabled">
              Enable <Mono>versions:</Mono> in deed.yaml to compare versions with catalog releases and end-of-life data.
            </Alert>
          )}
          {q.data.enabled && !q.data.eol_source && (
            <Alert color="blue" variant="light" title="Comparing with catalog releases only">
              No end-of-life source is configured (<Mono>versions.eol</Mono>), so only entities deployed from a
              repository with releases in the catalog are checked, and end-of-life dates are not known.
            </Alert>
          )}
          <Group gap="xs" wrap="wrap" role="group" aria-label="Status summary">
            {ORDER.map((s) => {
              const n = counts.get(s) ?? 0
              const active = statuses.includes(s)
              return (
                <UnstyledButton
                  key={s}
                  onClick={() => patch({ status: encodeList(active ? statuses.filter((x) => x !== s) : [s]) })}
                  aria-pressed={active}
                  data-testid={`version-count-${s}`}
                >
                  <Card
                    p="xs"
                    style={{
                      borderColor: active ? 'var(--mantine-primary-color-filled)' : undefined,
                      minWidth: 140,
                    }}
                  >
                    <Text size="xl" fw={700} lh={1.1}>
                      {n}
                    </Text>
                    <MetaBadge meta={statusMeta(s)} tooltip="" variant="transparent" px={0} />
                  </Card>
                </UnstyledButton>
              )
            })}
          </Group>
          <Group gap="xs" align="flex-end" wrap="wrap">
            <TextInput
              label="Search"
              placeholder="Name, kind or product"
              leftSection={<IconSearch size={14} />}
              value={search.q ?? ''}
              onChange={(e) => patch({ q: e.currentTarget.value || undefined })}
              w={260}
            />
            <MultiSelect
              label="Status"
              data={ORDER.map((value) => ({ value, label: statusMeta(value).label }))}
              value={statuses}
              onChange={(v) => patch({ status: encodeList(v) })}
              clearable
              w={300}
              data-testid="versions-status"
            />
            <Text size="sm" c="dimmed" ml="auto">
              {rows.length} of {all.length}
            </Text>
          </Group>
          {rows.length === 0 ? (
            <EmptyState title={all.length ? 'No versions match the filters' : 'No versions checked yet'}>
              {all.length
                ? 'Clear the status or search filters to see every checked entity.'
                : 'Entities with metadata.version (declared by a person or reported by a source) appear here after the next check.'}
            </EmptyState>
          ) : (
            <Card p={0}>
              <Table.ScrollContainer minWidth={960}>
                <Table fz="sm" verticalSpacing={6} aria-label="Version checks" data-testid="versions-table">
                  <Table.Thead>
                    <Table.Tr>
                      <Table.Th>Entity</Table.Th>
                      <Table.Th>Project</Table.Th>
                      <Table.Th>Product</Table.Th>
                      <Table.Th>In use</Table.Th>
                      <Table.Th>Latest</Table.Th>
                      <Table.Th>Status</Table.Th>
                      <Table.Th>End of life</Table.Th>
                      <Table.Th>Checked</Table.Th>
                    </Table.Tr>
                  </Table.Thead>
                  <Table.Tbody>
                    {rows.map((c) => (
                      <Table.Tr key={c.entity_id}>
                        <Table.Td maw={260}>
                          <EntityLink id={c.entity_id} name={c.name} kind={c.kind} />
                        </Table.Td>
                        <Table.Td>{projectName(c.project_id)}</Table.Td>
                        <Table.Td>
                          {c.product ? <Mono>{c.product}</Mono> : '—'}
                          {c.cycle && (
                            <Text size="xs" c="dimmed">
                              cycle {c.cycle}
                            </Text>
                          )}
                        </Table.Td>
                        <Table.Td>
                          <InUse declared={c.declared} observed={c.observed} />
                        </Table.Td>
                        <Table.Td>
                          {c.latest ? <Mono>{c.latest}</Mono> : '—'}
                          {c.latest_source && (
                            <Text size="xs" c="dimmed">
                              {SOURCE_LABEL[c.latest_source] ?? c.latest_source}
                            </Text>
                          )}
                        </Table.Td>
                        <Table.Td>
                          <Group gap={4} wrap="nowrap">
                            <MetaBadge meta={statusMeta(c.status)} />
                            <Popover width={380} position="bottom-end" withArrow shadow="md">
                              <Popover.Target>
                                <ActionIcon
                                  variant="subtle"
                                  color="gray"
                                  size="sm"
                                  aria-label={`Evidence for ${c.name}`}
                                >
                                  <IconInfoCircle size={14} />
                                </ActionIcon>
                              </Popover.Target>
                              <Popover.Dropdown>
                                <Text size="xs" fw={600} mb={4}>
                                  Evidence
                                </Text>
                                <JsonBlock value={c.evidence} maxHeight={240} />
                              </Popover.Dropdown>
                            </Popover>
                          </Group>
                        </Table.Td>
                        <Table.Td>
                          {c.eol_date ? dateTime(c.eol_date).split(',')[0] : '—'}
                          {c.support_until && (
                            <Text size="xs" c="dimmed">
                              support until {dateTime(c.support_until).split(',')[0]}
                            </Text>
                          )}
                        </Table.Td>
                        <Table.Td>
                          <TimeAgo value={c.checked_at} />
                        </Table.Td>
                      </Table.Tr>
                    ))}
                  </Table.Tbody>
                </Table>
              </Table.ScrollContainer>
            </Card>
          )}
        </Stack>
      )}
    </Page>
  )
}

function InUse({ declared, observed }: { declared?: string; observed?: string }) {
  if (!declared && !observed) return <>—</>
  if (declared && observed && declared !== observed) {
    return (
      <Stack gap={0}>
        <Text size="sm">
          <Mono>{observed}</Mono> observed
        </Text>
        <Text size="xs" c="dimmed">
          <Mono>{declared}</Mono> declared
        </Text>
      </Stack>
    )
  }
  return (
    <Text size="sm">
      <Mono>{observed ?? declared ?? ''}</Mono>{' '}
      <Text span size="xs" c="dimmed">
        {observed ? 'observed' : 'declared'}
      </Text>
    </Text>
  )
}
