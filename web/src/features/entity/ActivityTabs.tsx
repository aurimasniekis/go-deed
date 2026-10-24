import { Alert, Badge, Button, Card, Code, Group, Table, Text } from '@mantine/core'
import { useInfiniteQuery, useQuery } from '@tanstack/react-query'
import { api, unwrap, type Entity } from '@/api/client'
import { keys } from '@/api/queries'
import { TimeAgo } from '@/components/common'
import { AnchorLink } from '@/components/links'
import { EmptyState, ErrorState, LoadingRows } from '@/components/States'
import { humanize, jsonValue } from '@/lib/format'
import { pointerLabel } from '@/lib/provenance'

export function HistoryTab({ e }: { e: Entity }) {
  const id = e.metadata.id
  const hist = useInfiniteQuery({
    queryKey: keys.entityPart(id, 'history'),
    queryFn: async ({ pageParam }) =>
      unwrap(
        await api.GET('/entities/{id}/history', {
          params: { path: { id }, query: { limit: 50, ...(pageParam ? { before: pageParam } : {}) } },
        }),
      ),
    initialPageParam: 0,
    getNextPageParam: (last) => (last.length === 50 ? last[last.length - 1]?.seq : undefined),
  })
  if (hist.isPending) return <LoadingRows />
  if (hist.isError) return <ErrorState error={hist.error} what="history" onRetry={() => void hist.refetch()} />
  const items = hist.data.pages.flat()
  if (items.length === 0) return <EmptyState title="No history">No changes are recorded for this entity.</EmptyState>
  return (
    <Card>
      <Table fz="sm" verticalSpacing={6} aria-label="Change history" data-testid="history-table">
        <Table.Thead>
          <Table.Tr>
            <Table.Th>When</Table.Th>
            <Table.Th>Change</Table.Th>
            <Table.Th>By</Table.Th>
            <Table.Th>Fields</Table.Th>
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {items.map((c) => {
            const changed = Array.isArray(c.summary.changed) ? (c.summary.changed as string[]) : []
            return (
              <Table.Tr key={c.seq}>
                <Table.Td style={{ whiteSpace: 'nowrap' }}>
                  <TimeAgo value={c.at} />
                </Table.Td>
                <Table.Td>
                  <Badge
                    size="sm"
                    variant="light"
                    color={c.action === 'removed' ? 'red' : c.action === 'created' ? 'green' : 'gray'}
                  >
                    {c.action}
                  </Badge>
                </Table.Td>
                <Table.Td>
                  {c.source_id ? (
                    <AnchorLink to="/sources/$id" params={{ id: c.source_id }} size="sm">
                      {c.source ?? c.source_id}
                    </AnchorLink>
                  ) : (
                    <Text size="sm">
                      {c.actor_type ? `${c.actor_type}${c.actor_id ? ` ${c.actor_id.slice(0, 12)}…` : ''}` : '—'}
                    </Text>
                  )}
                </Table.Td>
                <Table.Td>
                  <Group gap={4}>
                    {changed.slice(0, 6).map((p) => (
                      <Code key={p} fz={11}>
                        {pointerLabel(p)}
                      </Code>
                    ))}
                    {changed.length > 6 && <Text size="xs">+{changed.length - 6}</Text>}
                    {changed.length === 0 &&
                      Object.entries(c.summary)
                        .filter(([k]) => !['kind', 'name'].includes(k))
                        .map(([k, v]) => (
                          <Text key={k} size="xs" c="dimmed">
                            {humanize(k)}: {jsonValue(v)}
                          </Text>
                        ))}
                  </Group>
                </Table.Td>
              </Table.Tr>
            )
          })}
        </Table.Tbody>
      </Table>
      {hist.hasNextPage && (
        <Group justify="center" mt="sm">
          <Button variant="default" onClick={() => void hist.fetchNextPage()} loading={hist.isFetchingNextPage}>
            Load older changes
          </Button>
        </Group>
      )}
    </Card>
  )
}

export function AuditTab({ e }: { e: Entity }) {
  const id = e.metadata.id
  const audit = useQuery({
    queryKey: keys.entityPart(id, 'audit'),
    queryFn: async () =>
      unwrap(await api.GET('/entities/{id}/audit', { params: { path: { id }, query: { limit: 100 } } })),
    retry: false,
  })
  if (audit.isPending) return <LoadingRows />
  if (audit.isError)
    return <ErrorState error={audit.error} what="the audit trail of this entity" onRetry={() => void audit.refetch()} />
  if (audit.data.length === 0)
    return <EmptyState title="No audit records">No audited actions target this entity.</EmptyState>
  return (
    <Card>
      <Alert color="gray" variant="light" mb="sm">
        Audit records are tamper-evident (hash-chained). Verify the chain from Settings › Audit log.
      </Alert>
      <Table fz="sm" verticalSpacing={6} aria-label="Audit records">
        <Table.Thead>
          <Table.Tr>
            <Table.Th>When</Table.Th>
            <Table.Th>Action</Table.Th>
            <Table.Th>Outcome</Table.Th>
            <Table.Th>Actor</Table.Th>
            <Table.Th>Details</Table.Th>
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {audit.data.map((r) => (
            <Table.Tr key={r.id}>
              <Table.Td style={{ whiteSpace: 'nowrap' }}>
                <TimeAgo value={r.at / 1000} />
              </Table.Td>
              <Table.Td className="mono" fz="xs">
                {r.action}
              </Table.Td>
              <Table.Td>
                <Badge size="xs" color={r.outcome === 'success' ? 'green' : 'red'} variant="light">
                  {r.outcome}
                </Badge>
              </Table.Td>
              <Table.Td fz="xs">
                {r.actor_type} {r.actor_id}
              </Table.Td>
              <Table.Td fz="xs" className="mono">
                {r.details ? jsonValue(r.details) : ''}
              </Table.Td>
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
    </Card>
  )
}
