import {
  Anchor,
  Badge,
  Card,
  Group,
  Loader,
  Popover,
  SimpleGrid,
  Stack,
  Table,
  Text,
  Title,
  Tooltip,
} from '@mantine/core'
import { IconArrowLeft, IconArrowRight, IconClockX, IconLinkOff, IconTopologyStar3 } from '@tabler/icons-react'
import { useQuery } from '@tanstack/react-query'
import { ButtonLink } from '@/components/links'
import { useState } from 'react'
import { api, unwrap, type Entity, type Relation } from '@/api/client'
import { keys, useRelTypes } from '@/api/queries'
import { EntityLink, TimeAgo } from '@/components/common'
import { EvidenceBadge, HealthBadge } from '@/components/StatusBadge'
import { EmptyState, ErrorState, LoadingRows } from '@/components/States'
import { EvidenceList } from '@/features/findings/FindingItem'
import { humanize } from '@/lib/format'
import { isStaleRelation } from '@/lib/status'
import { useEntityRelations, useRelationDetail } from './queries'

/** Popover with the assertions behind a relationship (who asserts it, evidence, confidence). */
export function EvidencePopover({ rel }: { rel: Relation }) {
  const [open, setOpen] = useState(false)
  const detail = useRelationDetail(rel.id, open)
  return (
    <Popover opened={open} onChange={setOpen} width={380} withinPortal position="left" shadow="md">
      <Popover.Target>
        <Anchor
          component="button"
          type="button"
          size="xs"
          onClick={() => setOpen((o) => !o)}
          aria-label={`Evidence for ${rel.type}`}
        >
          <EvidenceBadge cls={rel.evidence_class} size="xs" />
        </Anchor>
      </Popover.Target>
      <Popover.Dropdown>
        <Stack gap={6}>
          <Text size="sm" fw={600}>
            Why does this relationship exist?
          </Text>
          {detail.isPending ? (
            <Loader size="xs" />
          ) : detail.isError ? (
            <Text size="sm" c="red">
              Could not load the evidence.
            </Text>
          ) : (detail.data.assertions ?? []).length === 0 ? (
            <Text size="sm" c="dimmed">
              No assertion details available.
            </Text>
          ) : (
            detail.data.assertions?.map((a) => (
              <Stack key={a.id} gap={2} pb={4} style={{ borderBottom: '1px solid var(--deed-border)' }}>
                <Group gap={6}>
                  <Text size="sm">{a.source}</Text>
                  <EvidenceBadge cls={a.evidence_class} size="xs" />
                  {a.inferred && (
                    <Badge size="xs" color="orange" variant="outline">
                      confidence {Math.round(a.confidence * 100)}%
                    </Badge>
                  )}
                </Group>
                <Text size="xs" c="dimmed">
                  {a.layer} · first seen <TimeAgo value={a.first_seen_at} /> · last seen{' '}
                  <TimeAgo value={a.last_seen_at} />
                </Text>
                <EvidenceList evidence={a.evidence} max={6} />
              </Stack>
            ))
          )}
          {rel.inferred && (
            <Text size="xs" c="orange">
              Inferred relationships are hypotheses from indirect evidence. Verify before relying on them.
            </Text>
          )}
        </Stack>
      </Popover.Dropdown>
    </Popover>
  )
}

interface Group_ {
  type: string
  dir: 'out' | 'in'
  items: Relation[]
}

/** Relations in/out grouped by type, with evidence badges; restricted endpoints show as "restricted". */
export function RelationsTab({ e }: { e: Entity }) {
  const id = e.metadata.id
  const rels = useEntityRelations(id)
  const relTypes = useRelTypes()
  const unresolved = useQuery({
    queryKey: keys.unresolved(),
    queryFn: async () => unwrap(await api.GET('/relations/unresolved')),
  })
  if (rels.isPending) return <LoadingRows />
  if (rels.isError) return <ErrorState error={rels.error} what="relationships" onRetry={() => void rels.refetch()} />
  const typeOf = new Map((relTypes.data ?? []).map((t) => [t.name, t]))
  const groups = new Map<string, Group_>()
  for (const r of rels.data.items) {
    const dir = r.from.id === id ? 'out' : 'in'
    const k = `${dir}:${r.type}`
    const g = groups.get(k) ?? { type: r.type, dir, items: [] }
    g.items.push(r)
    groups.set(k, g)
  }
  const out = [...groups.values()].filter((g) => g.dir === 'out').sort((a, b) => a.type.localeCompare(b.type))
  const inc = [...groups.values()].filter((g) => g.dir === 'in').sort((a, b) => a.type.localeCompare(b.type))
  const mine = (unresolved.data ?? []).filter((u) => u.from_entity === id)

  const renderGroup = (g: Group_) => {
    const t = typeOf.get(g.type)
    const verb = g.dir === 'out' ? (t?.title ?? humanize(g.type)) : humanize(t?.inverse_name || `${g.type} (incoming)`)
    return (
      <Stack key={`${g.dir}-${g.type}`} gap={2} mb="sm">
        <Group gap={6}>
          <span
            style={{
              width: 10,
              height: 10,
              borderRadius: 2,
              background: t?.color ?? '#adb5bd',
              display: 'inline-block',
            }}
            aria-hidden
          />
          <Text size="sm" fw={600}>
            {verb}
          </Text>
          <Badge size="xs" variant="outline" color="gray">
            {g.items.length}
          </Badge>
        </Group>
        <Table fz="sm" verticalSpacing={3} withRowBorders={false}>
          <Table.Tbody>
            {g.items.map((r) => {
              const other = g.dir === 'out' ? r.to : r.from
              const stale = isStaleRelation(r.evidence_class, r.last_seen_at)
              return (
                <Table.Tr key={r.id}>
                  <Table.Td>
                    <EntityLink
                      id={other.id}
                      name={other.display_name || other.name}
                      kind={other.kind}
                      redacted={other.redacted}
                    />
                    {other.tombstoned && (
                      <Text span size="xs" c="dimmed" ml={4}>
                        (removed)
                      </Text>
                    )}
                  </Table.Td>
                  <Table.Td w={110}>{!other.redacted && <HealthBadge state={other.health} size="xs" />}</Table.Td>
                  <Table.Td w={120}>
                    <EvidencePopover rel={r} />
                  </Table.Td>
                  <Table.Td w={150}>
                    <Group gap={4} wrap="nowrap">
                      {r.cross_project && (
                        <Tooltip label="Crosses a project bridge">
                          <Badge size="xs" variant="outline" color="grape">
                            bridge
                          </Badge>
                        </Tooltip>
                      )}
                      {stale && (
                        <Tooltip label="Not observed for more than 7 days">
                          <Badge size="xs" color="red" variant="light" leftSection={<IconClockX size={10} />}>
                            stale
                          </Badge>
                        </Tooltip>
                      )}
                    </Group>
                  </Table.Td>
                  <Table.Td w={110} c="dimmed">
                    <TimeAgo value={r.last_seen_at} />
                  </Table.Td>
                </Table.Tr>
              )
            })}
          </Table.Tbody>
        </Table>
      </Stack>
    )
  }

  return (
    <Stack gap="sm">
      <Group justify="space-between">
        <Text size="sm" c="dimmed">
          {rels.data.total} relationships. Evidence: declared and structural are facts; observed comes from runtime;
          inferred is a hypothesis.
        </Text>
        <Group gap="xs">
          <ButtonLink size="xs" variant="default" to="/relations" search={{ entity: id }}>
            Table view
          </ButtonLink>
          <ButtonLink
            size="xs"
            leftSection={<IconTopologyStar3 size={14} />}
            to="/map"
            search={{ focus: id, mode: 'neighborhood', depth: 1 }}
          >
            Open in map
          </ButtonLink>
        </Group>
      </Group>
      {rels.data.items.length === 0 && mine.length === 0 ? (
        <EmptyState title="No relationships">This entity has no declared or observed relationships yet.</EmptyState>
      ) : (
        <SimpleGrid cols={{ base: 1, lg: 2 }} spacing="sm">
          <Card>
            <Group gap={6} mb="xs">
              <IconArrowRight size={16} aria-hidden />
              <Title order={2} fz={14}>
                Outgoing (this entity → others)
              </Title>
            </Group>
            {out.length === 0 ? (
              <Text size="sm" c="dimmed">
                None.
              </Text>
            ) : (
              out.map(renderGroup)
            )}
          </Card>
          <Card>
            <Group gap={6} mb="xs">
              <IconArrowLeft size={16} aria-hidden />
              <Title order={2} fz={14}>
                Incoming (others → this entity)
              </Title>
            </Group>
            {inc.length === 0 ? (
              <Text size="sm" c="dimmed">
                None.
              </Text>
            ) : (
              inc.map(renderGroup)
            )}
          </Card>
        </SimpleGrid>
      )}
      {mine.length > 0 && (
        <Card>
          <Group gap={6} mb="xs">
            <IconLinkOff size={16} aria-hidden />
            <Title order={2} fz={14}>
              Unresolved references
            </Title>
          </Group>
          <Text size="sm" c="dimmed" mb="xs">
            A source reported these relationships, but the target entity does not exist (yet).
          </Text>
          {mine.map((u) => (
            <Text key={u.id} size="sm">
              <span className="mono">{u.from}</span> — {u.type} → <span className="mono">{u.to}</span>{' '}
              <Text span size="xs" c="dimmed">
                first seen <TimeAgo value={u.first_seen_at} />
              </Text>
            </Text>
          ))}
        </Card>
      )}
    </Stack>
  )
}
