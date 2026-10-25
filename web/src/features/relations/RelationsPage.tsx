import { AnchorLink, ButtonLink } from '@/components/links'
import {
  Badge,
  Button,
  Card,
  Group,
  MultiSelect,
  SegmentedControl,
  Stack,
  Table,
  Text,
  Title,
  Tooltip,
} from '@mantine/core'
import { IconLinkOff, IconTopologyStar3 } from '@tabler/icons-react'
import { keepPreviousData, useInfiniteQuery, useQuery } from '@tanstack/react-query'
import { api, unwrap } from '@/api/client'
import { keys, useRelTypes } from '@/api/queries'
import { Page } from '@/components/AppShell'
import { EntityLink, PageHeader, TimeAgo } from '@/components/common'
import { EntityPicker } from '@/components/EntityPicker'
import { EmptyState, ErrorState, LoadingRows } from '@/components/States'
import { EvidenceBadge } from '@/components/StatusBadge'
import { EvidencePopover } from '@/features/entity/RelationsTab'
import { useEntity } from '@/features/entity/queries'
import { useProjectFilter, useTitle } from '@/lib/appStore'
import { count, humanize } from '@/lib/format'
import { EVIDENCE, isStaleRelation } from '@/lib/status'
import { compact, decodeBool, decodeEnum, decodeList, decodeString, encodeList, type Evidence } from '@/lib/urlState'

export interface RelationsSearch {
  entity?: string
  direction?: 'out' | 'in' | 'both'
  type?: string
  evidence?: string
  unresolved?: boolean
}

export function validateRelationsSearch(s: Record<string, unknown>): RelationsSearch {
  return compact({
    entity: decodeString(s.entity, 64),
    direction: decodeEnum(s.direction, ['out', 'in', 'both'] as const),
    type: encodeList(decodeList(s.type)),
    evidence: encodeList(decodeList(s.evidence)),
    unresolved: decodeBool(s.unresolved),
  })
}

/**
 * The accessible, non-graph route to the map's information: every relationship as a table with its type, direction,
 * evidence class, freshness and bridge status. Linked from the map ("Table") and entity pages.
 */
export function RelationsPage({
  search,
  setSearch,
}: {
  search: RelationsSearch
  setSearch: (s: RelationsSearch) => void
}) {
  useTitle('Relationships')
  const projects = useProjectFilter()
  const relTypes = useRelTypes()
  const focus = useEntity(search.entity ?? '')
  const query = compact({
    entity: search.entity,
    direction: search.entity ? (search.direction ?? 'both') : undefined,
    type: decodeList(search.type),
    evidence: decodeList(search.evidence) as Evidence[],
    project: projects,
    limit: 200,
  })
  const list = useInfiniteQuery({
    queryKey: keys.relations(query),
    queryFn: async ({ pageParam }) =>
      unwrap(
        await api.GET('/relations', { params: { query: { ...query, ...(pageParam ? { cursor: pageParam } : {}) } } }),
      ),
    initialPageParam: '',
    getNextPageParam: (last) => last.next_cursor || undefined,
    placeholderData: keepPreviousData,
  })
  const unresolved = useQuery({
    queryKey: keys.unresolved(),
    queryFn: async () => unwrap(await api.GET('/relations/unresolved')),
    enabled: !!search.unresolved,
  })
  const rows = list.data?.pages.flatMap((p) => p.items) ?? []
  const total = list.data?.pages[0]?.total ?? 0
  const typeTitle = (t: string) => relTypes.data?.find((x) => x.name === t)?.title ?? humanize(t)
  const patch = (p: Partial<RelationsSearch>) => setSearch(compact({ ...search, ...p }))
  const focusName = focus.data ? focus.data.metadata.displayName || focus.data.metadata.name : undefined

  return (
    <Page wide>
      <PageHeader
        title={focusName ? `Relationships of ${focusName}` : 'Relationships'}
        description="The relationships behind the map, as a table: direction, type, evidence and freshness for every edge."
        actions={
          <ButtonLink
            leftSection={<IconTopologyStar3 size={14} />}
            to="/map"
            search={search.entity ? { focus: search.entity, mode: 'neighborhood', depth: 1 } : {}}
          >
            Show on map
          </ButtonLink>
        }
      />
      <Group gap="xs" mb="sm" align="flex-end" wrap="wrap">
        <EntityPicker
          label="Entity"
          value={search.entity ?? null}
          initialLabel={focusName}
          onChange={(v) => patch({ entity: v ?? undefined })}
          w={320}
          data-testid="relations-entity"
        />
        {search.entity && (
          <SegmentedControl
            aria-label="Direction"
            value={search.direction ?? 'both'}
            onChange={(v) => patch({ direction: v === 'both' ? undefined : (v as 'in' | 'out') })}
            data={[
              { value: 'both', label: 'Both' },
              { value: 'out', label: 'Outgoing' },
              { value: 'in', label: 'Incoming' },
            ]}
          />
        )}
        <MultiSelect
          label="Types"
          data={(relTypes.data ?? []).map((t) => ({ value: t.name, label: t.title }))}
          value={decodeList(search.type)}
          onChange={(v) => patch({ type: encodeList(v) })}
          searchable
          clearable
          w={240}
        />
        <MultiSelect
          label="Evidence"
          data={Object.entries(EVIDENCE).map(([value, m]) => ({ value, label: m.label }))}
          value={decodeList(search.evidence)}
          onChange={(v) => patch({ evidence: encodeList(v) })}
          clearable
          w={220}
        />
        <Button
          variant={search.unresolved ? 'light' : 'default'}
          leftSection={<IconLinkOff size={14} />}
          onClick={() => patch({ unresolved: search.unresolved ? undefined : true })}
        >
          Unresolved references
        </Button>
      </Group>

      {search.unresolved && (
        <Card mb="sm">
          <Title order={2} fz={14} mb={4}>
            Unresolved references
          </Title>
          <Text size="sm" c="dimmed" mb="xs">
            Relationships a source reported whose target does not exist (yet). They resolve automatically when the
            target appears.
          </Text>
          {unresolved.isPending ? (
            <LoadingRows rows={2} />
          ) : unresolved.isError ? (
            <ErrorState error={unresolved.error} what="unresolved references" />
          ) : unresolved.data.length === 0 ? (
            <Text size="sm">None — every reported relationship resolves.</Text>
          ) : (
            <Table fz="sm" aria-label="Unresolved references" data-testid="unresolved-table">
              <Table.Thead>
                <Table.Tr>
                  <Table.Th>From</Table.Th>
                  <Table.Th>Type</Table.Th>
                  <Table.Th>Missing target</Table.Th>
                  <Table.Th>Source</Table.Th>
                  <Table.Th>First seen</Table.Th>
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {unresolved.data.map((u) => (
                  <Table.Tr key={u.id}>
                    <Table.Td>
                      {u.from_entity ? (
                        <AnchorLink to="/entities/$id" params={{ id: u.from_entity }} size="sm">
                          {u.from}
                        </AnchorLink>
                      ) : (
                        u.from
                      )}
                    </Table.Td>
                    <Table.Td>{typeTitle(u.type)}</Table.Td>
                    <Table.Td className="mono">{u.to}</Table.Td>
                    <Table.Td>
                      <AnchorLink to="/sources/$id" params={{ id: u.source_id }} size="sm">
                        source
                      </AnchorLink>
                    </Table.Td>
                    <Table.Td>
                      <TimeAgo value={u.first_seen_at} />
                    </Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          )}
        </Card>
      )}

      {list.isPending ? (
        <LoadingRows rows={10} />
      ) : list.isError && !list.data ? (
        <ErrorState error={list.error} what="relationships" onRetry={() => void list.refetch()} />
      ) : rows.length === 0 ? (
        <EmptyState title="No relationships match">Change the entity or filters.</EmptyState>
      ) : (
        <Card p={0}>
          <Text size="sm" c="dimmed" p="sm">
            {count(total)} relationships{rows.length < total ? ` · ${count(rows.length)} loaded` : ''}
          </Text>
          <Table.ScrollContainer minWidth={900}>
            <Table fz="sm" verticalSpacing={6} striped aria-label="Relationships" data-testid="relations-table">
              <Table.Thead>
                <Table.Tr>
                  <Table.Th scope="col">From</Table.Th>
                  <Table.Th scope="col">Relationship</Table.Th>
                  <Table.Th scope="col">To</Table.Th>
                  <Table.Th scope="col">Evidence</Table.Th>
                  <Table.Th scope="col">Status</Table.Th>
                  <Table.Th scope="col">Last seen</Table.Th>
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {rows.map((r) => {
                  const stale = isStaleRelation(r.evidence_class, r.last_seen_at)
                  return (
                    <Table.Tr key={r.id}>
                      <Table.Td>
                        <EntityLink
                          id={r.from.id}
                          name={r.from.display_name || r.from.name}
                          kind={r.from.kind}
                          redacted={r.from.redacted}
                        />
                      </Table.Td>
                      <Table.Td>
                        <Group gap={6} wrap="nowrap">
                          <span
                            style={{
                              width: 10,
                              height: 10,
                              borderRadius: 2,
                              background: relTypes.data?.find((t) => t.name === r.type)?.color ?? '#adb5bd',
                              flexShrink: 0,
                            }}
                            aria-hidden
                          />
                          <Text size="sm">{typeTitle(r.type)} →</Text>
                        </Group>
                      </Table.Td>
                      <Table.Td>
                        <EntityLink
                          id={r.to.id}
                          name={r.to.display_name || r.to.name}
                          kind={r.to.kind}
                          redacted={r.to.redacted}
                        />
                      </Table.Td>
                      <Table.Td>
                        {r.inferred ? <EvidencePopover rel={r} /> : <EvidenceBadge cls={r.evidence_class} size="xs" />}
                      </Table.Td>
                      <Table.Td>
                        <Group gap={4}>
                          {r.cross_project && (
                            <Tooltip label="Crosses a project bridge">
                              <Badge size="xs" variant="outline" color="grape">
                                bridge
                              </Badge>
                            </Tooltip>
                          )}
                          {stale && (
                            <Badge size="xs" color="red" variant="light">
                              stale
                            </Badge>
                          )}
                          {!r.live && (
                            <Badge size="xs" color="gray">
                              removed
                            </Badge>
                          )}
                          {!r.cross_project && !stale && r.live && (
                            <Text size="xs" c="dimmed">
                              current
                            </Text>
                          )}
                        </Group>
                      </Table.Td>
                      <Table.Td>
                        <TimeAgo value={r.last_seen_at} />
                      </Table.Td>
                    </Table.Tr>
                  )
                })}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
          {list.hasNextPage && (
            <Stack align="center" p="sm">
              <Button variant="default" onClick={() => void list.fetchNextPage()} loading={list.isFetchingNextPage}>
                Load more
              </Button>
            </Stack>
          )}
        </Card>
      )}
    </Page>
  )
}
