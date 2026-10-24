import { AnchorLink } from '@/components/links'
import { Alert, Badge, Card, Group, SegmentedControl, Stack, Table, Text, TextInput } from '@mantine/core'
import { IconSearch } from '@tabler/icons-react'
import { useEffect, useMemo, useRef, useState } from 'react'
import type { Entity } from '@/api/client'
import { useSources } from '@/api/queries'
import { Mono, TimeAgo } from '@/components/common'
import { MetaBadge, FreshnessBadge, LayerBadge } from '@/components/StatusBadge'
import { EmptyState, ErrorState, LoadingRows } from '@/components/States'
import { jsonValue, truncate } from '@/lib/format'
import { compareFacets, pointerLabel, provenanceRows, type CompareState } from '@/lib/provenance'
import { useConflicts, useFacets, useProvenance } from './queries'

const compareMeta: Record<
  CompareState,
  { label: string; tone: 'red' | 'blue' | 'gray' | 'green'; icon: string; description: string }
> = {
  differs: {
    label: 'Differs',
    tone: 'red',
    icon: 'alert-triangle',
    description: 'An observed value differs from the declared one.',
  },
  'observed-only': {
    label: 'Observed only',
    tone: 'blue',
    icon: 'eye',
    description: 'Only runtime or import sources report this field.',
  },
  'declared-only': {
    label: 'Declared only',
    tone: 'gray',
    icon: 'file-check',
    description: 'Declared, not (yet) observed by any source.',
  },
  match: {
    label: 'Matches',
    tone: 'green',
    icon: 'circle-check',
    description: 'Every observed value equals a declared value.',
  },
}

function Value({ v }: { v: unknown }) {
  return (
    <Text span className="mono" size="xs" style={{ wordBreak: 'break-all' }}>
      {truncate(jsonValue(v), 160)}
    </Text>
  )
}

/** Declared (declared/override/annotation layers) vs observed (imported/discovered/observation) per field. */
export function CompareTab({ e }: { e: Entity }) {
  const facets = useFacets(e.metadata.id)
  const [filter, setFilter] = useState<'all' | 'differs'>('all')
  const rows = useMemo(() => (facets.data ? compareFacets(facets.data, e) : []), [facets.data, e])
  if (facets.isPending) return <LoadingRows />
  if (facets.isError) return <ErrorState error={facets.error} what="facets" onRetry={() => void facets.refetch()} />
  const shown = filter === 'differs' ? rows.filter((r) => r.state === 'differs') : rows
  const differs = rows.filter((r) => r.state === 'differs').length
  return (
    <Card>
      <Group justify="space-between" mb="sm">
        <Text size="sm" c="dimmed">
          Declared facts (Terraform, API, UI annotations, overrides) next to what imports, discovery and observations
          report. {differs > 0 ? `${differs} field${differs === 1 ? '' : 's'} differ.` : 'No field differs.'}
        </Text>
        <SegmentedControl
          value={filter}
          onChange={(v) => setFilter(v as 'all' | 'differs')}
          data={[
            { value: 'all', label: `All (${rows.length})` },
            { value: 'differs', label: `Differs (${differs})` },
          ]}
        />
      </Group>
      {shown.length === 0 ? (
        <EmptyState title={filter === 'differs' ? 'No differences' : 'No facts yet'}>
          {filter === 'differs'
            ? 'Every observed value matches the declaration.'
            : 'No source contributes fields to this entity.'}
        </EmptyState>
      ) : (
        <Table.ScrollContainer minWidth={760}>
          <Table
            fz="sm"
            verticalSpacing={6}
            striped
            aria-label="Declared versus observed values"
            data-testid="compare-table"
          >
            <Table.Thead>
              <Table.Tr>
                <Table.Th>Field</Table.Th>
                <Table.Th>Declared</Table.Th>
                <Table.Th>Observed / imported</Table.Th>
                <Table.Th>Effective</Table.Th>
                <Table.Th>State</Table.Th>
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {shown.map((r) => (
                <Table.Tr key={r.pointer}>
                  <Table.Td>
                    <AnchorLink
                      to="/entities/$id"
                      params={{ id: e.metadata.id }}
                      search={{ tab: 'provenance', ptr: r.pointer }}
                      size="sm"
                      className="mono"
                    >
                      {pointerLabel(r.pointer)}
                    </AnchorLink>
                  </Table.Td>
                  <Table.Td>
                    {r.declared.length === 0 ? (
                      <Text size="xs" c="dimmed">
                        —
                      </Text>
                    ) : (
                      r.declared.map((d) => (
                        <div key={d.sourceId}>
                          <Value v={d.value} />{' '}
                          <Text span size="xs" c="dimmed">
                            ({d.source}, {d.layer})
                          </Text>
                        </div>
                      ))
                    )}
                  </Table.Td>
                  <Table.Td>
                    {r.observed.length === 0 ? (
                      <Text size="xs" c="dimmed">
                        —
                      </Text>
                    ) : (
                      r.observed.map((o) => (
                        <div key={o.sourceId}>
                          <Value v={o.value} />{' '}
                          <Text span size="xs" c="dimmed">
                            ({o.source}, {o.layer})
                          </Text>
                        </div>
                      ))
                    )}
                  </Table.Td>
                  <Table.Td>
                    <Value v={r.effective} />
                  </Table.Td>
                  <Table.Td>
                    <MetaBadge meta={compareMeta[r.state]} size="xs" />
                  </Table.Td>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        </Table.ScrollContainer>
      )}
    </Card>
  )
}

/** "Why does this value exist?" for every field: winner, layer, revision, time and losing candidates. */
export function ProvenanceTab({ e, highlight }: { e: Entity; highlight?: string }) {
  const prov = useProvenance(e.metadata.id)
  const [q, setQ] = useState('')
  const rows = useMemo(() => (prov.data ? provenanceRows(prov.data, e) : []), [prov.data, e])
  const hl = useRef<HTMLTableRowElement>(null)
  useEffect(() => {
    hl.current?.scrollIntoView({ block: 'center' })
  }, [highlight, rows.length])
  if (prov.isPending) return <LoadingRows />
  if (prov.isError) return <ErrorState error={prov.error} what="provenance" onRetry={() => void prov.refetch()} />
  const shown = q
    ? rows.filter(
        (r) => r.pointer.toLowerCase().includes(q.toLowerCase()) || r.source.toLowerCase().includes(q.toLowerCase()),
      )
    : rows
  return (
    <Card>
      <Group justify="space-between" mb="sm" wrap="wrap">
        <Text size="sm" c="dimmed" maw={680}>
          For every field: the source whose value won, its layer and revision, and the candidates that lost. Precedence:
          override › declared › UI annotation › imported › discovered (per-source policy may reorder); ties go to the
          earliest claim.
        </Text>
        <TextInput
          leftSection={<IconSearch size={14} />}
          placeholder="Filter fields or sources"
          value={q}
          onChange={(ev) => setQ(ev.currentTarget.value)}
          aria-label="Filter provenance"
          w={240}
        />
      </Group>
      <Table.ScrollContainer minWidth={800}>
        <Table fz="sm" verticalSpacing={6} aria-label="Provenance per field" data-testid="provenance-table">
          <Table.Thead>
            <Table.Tr>
              <Table.Th>Field</Table.Th>
              <Table.Th>Value</Table.Th>
              <Table.Th>Winning source</Table.Th>
              <Table.Th>Layer</Table.Th>
              <Table.Th>Revision</Table.Th>
              <Table.Th>Reported</Table.Th>
              <Table.Th>Losing candidates</Table.Th>
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {shown.map((r) => {
              const active = highlight === r.pointer
              return (
                <Table.Tr
                  key={r.pointer}
                  ref={active ? hl : undefined}
                  style={active ? { outline: '2px solid var(--deed-focus)', outlineOffset: -2 } : undefined}
                  data-pointer={r.pointer}
                >
                  <Table.Td className="mono" fz="xs">
                    {pointerLabel(r.pointer)}
                  </Table.Td>
                  <Table.Td>
                    <Value v={r.value} />
                  </Table.Td>
                  <Table.Td>{r.source}</Table.Td>
                  <Table.Td>
                    <LayerBadge layer={r.layer} size="xs" />
                  </Table.Td>
                  <Table.Td className="mono">{r.revision}</Table.Td>
                  <Table.Td>
                    <TimeAgo value={r.observed_at} />
                  </Table.Td>
                  <Table.Td>
                    {r.losing.length === 0 ? (
                      <Text size="xs" c="dimmed">
                        none
                      </Text>
                    ) : (
                      <Stack gap={2}>
                        {r.losing.map((c) => (
                          <Group key={c.source} gap={4} wrap="nowrap">
                            <Text size="xs">{c.source}</Text>
                            <LayerBadge layer={c.layer} size="xs" />
                            <Value v={c.value} />
                            {c.sameValue && (
                              <Text size="xs" c="dimmed">
                                (agrees)
                              </Text>
                            )}
                          </Group>
                        ))}
                      </Stack>
                    )}
                  </Table.Td>
                </Table.Tr>
              )
            })}
          </Table.Tbody>
        </Table>
      </Table.ScrollContainer>
    </Card>
  )
}

export function ConflictsTab({ e }: { e: Entity }) {
  const conflicts = useConflicts(e.metadata.id)
  if (conflicts.isPending) return <LoadingRows />
  if (conflicts.isError)
    return <ErrorState error={conflicts.error} what="conflicts" onRetry={() => void conflicts.refetch()} />
  if (conflicts.data.length === 0) {
    return (
      <EmptyState title="No conflicts">
        Conflicts appear when two sources in the same layer report different values for one field. The higher-precedence
        value wins; the conflict stays visible here and as a finding until they agree.
      </EmptyState>
    )
  }
  return (
    <Card>
      <Alert color="orange" variant="light" mb="sm">
        Sources in the same layer disagree. The winner is chosen deterministically (priority, then the earliest claim);
        fix the losing source or set a field priority in its policy.
      </Alert>
      <Table fz="sm" verticalSpacing={6} aria-label="Conflicts">
        <Table.Thead>
          <Table.Tr>
            <Table.Th>Field</Table.Th>
            <Table.Th>Layer</Table.Th>
            <Table.Th>Winner</Table.Th>
            <Table.Th>Loser</Table.Th>
            <Table.Th>Since</Table.Th>
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {conflicts.data.map((c) => (
            <Table.Tr key={`${c.pointer}-${c.loser_source_id}`}>
              <Table.Td>
                <AnchorLink
                  to="/entities/$id"
                  params={{ id: e.metadata.id }}
                  search={{ tab: 'provenance', ptr: c.pointer }}
                  className="mono"
                  size="xs"
                >
                  {pointerLabel(c.pointer)}
                </AnchorLink>
              </Table.Td>
              <Table.Td>
                <LayerBadge layer={c.layer} size="xs" />
              </Table.Td>
              <Table.Td>
                {c.winner_source}: <Value v={c.winner_value} />
              </Table.Td>
              <Table.Td>
                {c.loser_source}: <Value v={c.loser_value} />
              </Table.Td>
              <Table.Td>
                <TimeAgo value={c.first_seen_at} />
              </Table.Td>
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
    </Card>
  )
}

/** Which sources contribute, their layer, freshness and external IDs. */
export function SourcesTab({ e }: { e: Entity }) {
  const facets = useFacets(e.metadata.id)
  const sources = useSources()
  if (facets.isPending) return <LoadingRows />
  if (facets.isError) return <ErrorState error={facets.error} what="sources" onRetry={() => void facets.refetch()} />
  const byId = new Map((sources.data ?? []).map((s) => [s.id, s]))
  const ext = e.metadata.externalIds ?? []
  return (
    <Stack gap="sm">
      <Card>
        <Table.ScrollContainer minWidth={760}>
          <Table fz="sm" verticalSpacing={6} aria-label="Contributing sources" data-testid="sources-table">
            <Table.Thead>
              <Table.Tr>
                <Table.Th>Source</Table.Th>
                <Table.Th>Layer</Table.Th>
                <Table.Th>Source freshness</Table.Th>
                <Table.Th>Fact reported</Table.Th>
                <Table.Th>Expires</Table.Th>
                <Table.Th>Revision</Table.Th>
                <Table.Th>State</Table.Th>
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {facets.data.map((f) => {
                const s = byId.get(f.source_id)
                return (
                  <Table.Tr key={f.id}>
                    <Table.Td>
                      <AnchorLink to="/sources/$id" params={{ id: f.source_id }} size="sm">
                        {s?.display_name || f.source}
                      </AnchorLink>
                      <Text size="xs" c="dimmed">
                        {f.provider}
                        {f.external_id ? ` · ${f.external_id}` : ''}
                      </Text>
                    </Table.Td>
                    <Table.Td>
                      <LayerBadge layer={f.layer} size="xs" />
                    </Table.Td>
                    <Table.Td>
                      {s ? (
                        <FreshnessBadge state={s.freshness} size="xs" />
                      ) : (
                        <Text size="xs" c="dimmed">
                          system
                        </Text>
                      )}
                    </Table.Td>
                    <Table.Td>
                      <TimeAgo value={f.observed_at} />
                    </Table.Td>
                    <Table.Td>
                      {f.expires_at ? (
                        <TimeAgo value={f.expires_at} />
                      ) : (
                        <Text size="xs" c="dimmed">
                          never
                        </Text>
                      )}
                    </Table.Td>
                    <Table.Td className="mono">
                      r{f.revision}
                      {f.generation ? ` · g${f.generation}` : ''}
                    </Table.Td>
                    <Table.Td>
                      <Group gap={4}>
                        {f.live ? (
                          <Badge size="xs" color="green" variant="light">
                            live
                          </Badge>
                        ) : (
                          <Badge size="xs" color="gray">
                            tombstoned
                          </Badge>
                        )}
                        {f.absent_from_last_snapshot && (
                          <Badge size="xs" color="orange" variant="light">
                            absent from last snapshot
                          </Badge>
                        )}
                      </Group>
                    </Table.Td>
                  </Table.Tr>
                )
              })}
            </Table.Tbody>
          </Table>
        </Table.ScrollContainer>
      </Card>
      <Card>
        <Text fw={600} size="sm" mb="xs">
          External IDs
        </Text>
        {ext.length === 0 ? (
          <Text size="sm" c="dimmed">
            No source reported an external ID.
          </Text>
        ) : (
          ext.map((x) => (
            <Group key={`${x.sourceId}-${x.id}`} gap="xs">
              <Text size="sm">{x.source}</Text>
              <Mono>{x.id}</Mono>
            </Group>
          ))
        )}
      </Card>
    </Stack>
  )
}
