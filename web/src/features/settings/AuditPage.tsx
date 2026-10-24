import { Alert, Badge, Button, Card, Code, Group, Select, Stack, Table, Text, TextInput, Tooltip } from '@mantine/core'
import { IconShieldCheck } from '@tabler/icons-react'
import { useInfiniteQuery, useMutation } from '@tanstack/react-query'
import { canReadAllAudit, useMe } from '@/api/auth'
import { api, errorMessage, unwrap } from '@/api/client'
import { keys, useProjects } from '@/api/queries'
import { Page } from '@/components/AppShell'
import { PageHeader, TimeAgo } from '@/components/common'
import { AnchorLink } from '@/components/links'
import { EmptyState, ErrorState, LoadingRows } from '@/components/States'
import { useTitle } from '@/lib/appStore'
import { jsonValue, truncate } from '@/lib/format'
import { compact, decodeEnum, decodeString } from '@/lib/urlState'

export interface AuditSearch {
  project?: string
  target_type?: string
  target_id?: string
  actor?: string
  action?: string
  outcome?: 'success' | 'failure' | 'denied'
}

export function validateAuditSearch(s: Record<string, unknown>): AuditSearch {
  return compact({
    project: decodeString(s.project, 64),
    target_type: decodeString(s.target_type, 64),
    target_id: decodeString(s.target_id, 64),
    actor: decodeString(s.actor, 64),
    action: decodeString(s.action, 64),
    outcome: decodeEnum(s.outcome, ['success', 'failure', 'denied'] as const),
  })
}

export function AuditPage({ search, setSearch }: { search: AuditSearch; setSearch: (s: AuditSearch) => void }) {
  useTitle('Audit log')
  const me = useMe().data
  const projects = useProjects()
  const list = useInfiniteQuery({
    queryKey: keys.audit(search),
    queryFn: async ({ pageParam }) =>
      unwrap(
        await api.GET('/audit', {
          params: { query: { ...search, limit: 100, ...(pageParam ? { before: pageParam } : {}) } },
        }),
      ),
    initialPageParam: 0,
    getNextPageParam: (last) => (last.length === 100 ? last[last.length - 1]?.seq : undefined),
  })
  const verify = useMutation({ mutationFn: async () => unwrap(await api.POST('/audit/verify')) })
  const patch = (p: Partial<AuditSearch>) => setSearch(compact({ ...search, ...p }))
  const rows = list.data?.pages.flat() ?? []
  return (
    <Page wide>
      <PageHeader
        title="Audit log"
        description="Security-relevant actions, hash-chained so tampering is detectable. Owners, admins and auditors see every record; project admins see their projects."
        actions={
          canReadAllAudit(me) && (
            <Button
              leftSection={<IconShieldCheck size={14} />}
              variant="default"
              onClick={() => verify.mutate()}
              loading={verify.isPending}
              data-testid="verify-audit"
            >
              Verify chain
            </Button>
          )
        }
      />
      {verify.data && (
        <Alert
          color={verify.data.ok ? 'green' : 'red'}
          title={verify.data.ok ? 'The audit chain is intact' : 'The audit chain is broken'}
          mb="sm"
          data-testid="verify-result"
        >
          <Text size="sm">
            {verify.data.events} events checked.
            {verify.data.head_hash && (
              <>
                {' '}
                Head <Code>{truncate(verify.data.head_hash, 24)}</Code>.
              </>
            )}
            {verify.data.problem && ` ${verify.data.problem}`}
            {verify.data.broken_at !== undefined && ` Broken at sequence ${verify.data.broken_at}.`}
          </Text>
        </Alert>
      )}
      {verify.isError && (
        <Alert color="red" mb="sm">
          {errorMessage(verify.error)}
        </Alert>
      )}
      <Group gap="xs" mb="sm" align="flex-end" wrap="wrap">
        <Select
          label="Project"
          data={(projects.data ?? []).map((p) => ({ value: p.id, label: p.display_name || p.name }))}
          value={search.project ?? null}
          onChange={(v) => patch({ project: v ?? undefined })}
          clearable
          w={200}
        />
        <TextInput
          label="Action prefix"
          placeholder="auth. or entity."
          value={search.action ?? ''}
          onChange={(e) => patch({ action: e.currentTarget.value || undefined })}
          w={180}
        />
        <TextInput
          label="Actor ID"
          value={search.actor ?? ''}
          onChange={(e) => patch({ actor: e.currentTarget.value || undefined })}
          w={220}
        />
        <TextInput
          label="Target type"
          value={search.target_type ?? ''}
          onChange={(e) => patch({ target_type: e.currentTarget.value || undefined })}
          w={140}
        />
        <TextInput
          label="Target ID"
          value={search.target_id ?? ''}
          onChange={(e) => patch({ target_id: e.currentTarget.value || undefined })}
          w={220}
        />
        <Select
          label="Outcome"
          data={['success', 'failure', 'denied']}
          value={search.outcome ?? null}
          onChange={(v) => patch({ outcome: (v ?? undefined) as AuditSearch['outcome'] })}
          clearable
          w={130}
        />
      </Group>
      {list.isPending ? (
        <LoadingRows rows={10} />
      ) : list.isError ? (
        <ErrorState error={list.error} what="the audit log" onRetry={() => void list.refetch()} />
      ) : rows.length === 0 ? (
        <EmptyState title="No audit records match">Try removing a filter.</EmptyState>
      ) : (
        <Card p={0}>
          <Table.ScrollContainer minWidth={1000}>
            <Table fz="sm" verticalSpacing={5} aria-label="Audit records" data-testid="audit-table">
              <Table.Thead>
                <Table.Tr>
                  <Table.Th>#</Table.Th>
                  <Table.Th>When</Table.Th>
                  <Table.Th>Action</Table.Th>
                  <Table.Th>Outcome</Table.Th>
                  <Table.Th>Actor</Table.Th>
                  <Table.Th>Target</Table.Th>
                  <Table.Th>Details</Table.Th>
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {rows.map((r) => (
                  <Table.Tr key={r.id}>
                    <Table.Td className="mono">{r.seq}</Table.Td>
                    <Table.Td style={{ whiteSpace: 'nowrap' }}>
                      <TimeAgo value={r.at / 1000} />
                    </Table.Td>
                    <Table.Td className="mono" fz="xs">
                      {r.action}
                    </Table.Td>
                    <Table.Td>
                      <Badge
                        size="xs"
                        variant="light"
                        color={r.outcome === 'success' ? 'green' : r.outcome === 'denied' ? 'orange' : 'red'}
                      >
                        {r.outcome}
                      </Badge>
                      {r.reason_code && (
                        <Text size="xs" c="dimmed">
                          {r.reason_code}
                        </Text>
                      )}
                    </Table.Td>
                    <Table.Td fz="xs">
                      {r.actor_type} <span className="mono">{r.actor_id}</span>
                      {r.auth_method && (
                        <Text size="xs" c="dimmed">
                          via {r.auth_method}
                        </Text>
                      )}
                      {r.source_ip && (
                        <Text size="xs" c="dimmed">
                          {r.source_ip}
                        </Text>
                      )}
                    </Table.Td>
                    <Table.Td fz="xs">
                      {r.target_type === 'entity' && r.target_id ? (
                        <AnchorLink to="/entities/$id" params={{ id: r.target_id }} size="xs">
                          {r.target_id}
                        </AnchorLink>
                      ) : r.target_type === 'source' && r.target_id ? (
                        <AnchorLink to="/sources/$id" params={{ id: r.target_id }} size="xs">
                          {r.target_id}
                        </AnchorLink>
                      ) : (
                        <>
                          {r.target_type} <span className="mono">{r.target_id}</span>
                        </>
                      )}
                    </Table.Td>
                    <Table.Td fz="xs" maw={320}>
                      {r.details && (
                        <Tooltip label={jsonValue(r.details)} multiline maw={420}>
                          <Text size="xs" className="mono" truncate>
                            {jsonValue(r.details)}
                          </Text>
                        </Tooltip>
                      )}
                      {r.has_sensitive && (
                        <Badge size="xs" variant="outline">
                          redacted
                        </Badge>
                      )}
                    </Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
          {list.hasNextPage && (
            <Stack align="center" p="sm">
              <Button variant="default" onClick={() => void list.fetchNextPage()} loading={list.isFetchingNextPage}>
                Older records
              </Button>
            </Stack>
          )}
        </Card>
      )}
    </Page>
  )
}
