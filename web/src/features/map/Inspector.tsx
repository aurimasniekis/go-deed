import { AnchorLink } from '@/components/links'
import {
  ActionIcon,
  Anchor,
  Badge,
  Button,
  Divider,
  Group,
  Loader,
  ScrollArea,
  SimpleGrid,
  Stack,
  Text,
  Title,
  Tooltip,
} from '@mantine/core'
import {
  IconArrowDownRight,
  IconArrowUpLeft,
  IconExternalLink,
  IconFocus2,
  IconHelpHexagon,
  IconPlus,
  IconRoute,
  IconTable,
  IconX,
} from '@tabler/icons-react'
import { EntityLink, TimeAgo } from '@/components/common'
import { RefIcon } from '@/components/icons'
import { EvidenceBadge, FreshnessBadge, HealthBadge } from '@/components/StatusBadge'
import { EvidenceList } from '@/features/findings/FindingItem'
import { useEntity, useRelationDetail } from '@/features/entity/queries'
import { humanize } from '@/lib/format'
import { evidenceMeta } from '@/lib/status'
import { sourceLabel, type GraphModel, type MapEdge, type MapNode } from './types'

export interface InspectorActions {
  focus: (id: string) => void
  expand: (n: MapNode) => void
  impact: (id: string, dir: 'downstream' | 'upstream') => void
  pathFrom: (id: string) => void
  pathTo: (id: string) => void
  select: (id: string) => void
}

function Shell({
  title,
  onClose,
  children,
}: {
  title: React.ReactNode
  onClose: () => void
  children: React.ReactNode
}) {
  return (
    <div
      className="deed-panel"
      style={{ width: 340, maxHeight: '100%', display: 'flex', flexDirection: 'column' }}
      data-testid="map-inspector"
      role="region"
      aria-label="Selection inspector"
    >
      <Group
        justify="space-between"
        px="sm"
        py={6}
        wrap="nowrap"
        style={{ borderBottom: '1px solid var(--deed-border)' }}
      >
        <div style={{ minWidth: 0 }}>{title}</div>
        <Tooltip label="Close (Esc)">
          <ActionIcon onClick={onClose} aria-label="Close inspector">
            <IconX size={14} />
          </ActionIcon>
        </Tooltip>
      </Group>
      <ScrollArea
        type="auto"
        style={{ flex: 1 }}
        px="sm"
        py="xs"
        viewportProps={{ tabIndex: 0, 'aria-label': 'Inspector details' }}
      >
        {children}
      </ScrollArea>
    </div>
  )
}

/** Selected entity: summary, visible relations, "why" (provenance) and exploration actions. */
export function NodeInspector({
  node,
  model,
  kindTitle,
  kindIcon,
  typeTitle,
  actions,
  onClose,
}: {
  node: MapNode
  model: GraphModel
  kindTitle: string
  kindIcon: string
  typeTitle: (t: string) => string
  actions: InspectorActions
  onClose: () => void
}) {
  const isCluster = !!node.cluster
  const entity = useEntity(isCluster || node.redacted ? '' : node.id)
  const out = [...model.edges.values()].filter((e) => e.from === node.id)
  const inc = [...model.edges.values()].filter((e) => e.to === node.id)
  const rel = (e: MapEdge, dir: 'out' | 'in') => {
    const other = model.nodes.get(dir === 'out' ? e.to : e.from)
    if (!other) return null
    return (
      <Group key={e.id} gap={6} wrap="nowrap" justify="space-between">
        <Group gap={4} wrap="nowrap" style={{ minWidth: 0 }}>
          <Text size="xs" c="dimmed" style={{ whiteSpace: 'nowrap' }}>
            {dir === 'out' ? '→' : '←'} {typeTitle(e.type)}
          </Text>
          <Anchor component="button" type="button" size="xs" truncate onClick={() => actions.select(other.id)}>
            {other.redacted ? 'restricted' : (other.displayName ?? other.name)}
          </Anchor>
        </Group>
        <Tooltip label={evidenceMeta(e.evidence).description}>
          <Text size="xs" c="dimmed">
            {evidenceMeta(e.evidence).label}
          </Text>
        </Tooltip>
      </Group>
    )
  }

  if (isCluster) {
    return (
      <Shell
        onClose={onClose}
        title={
          <Text fw={600} size="sm" truncate>
            {node.kind === 'group' ? node.name : kindTitle} · {node.cluster?.count}
          </Text>
        }
      >
        <Stack gap="xs">
          <Text size="sm">
            {node.kind === 'group'
              ? `A collapsed group of ${node.cluster?.count} nodes. Expand it to see the members.`
              : `A cluster of ${node.cluster?.count} entities (${node.cluster?.groupBy}: ${node.cluster?.value}). The server aggregates large groups; expanding loads the members.`}
          </Text>
          <HealthBadge state={node.health} prefix="Worst health" />
          <Button
            leftSection={<IconPlus size={14} />}
            onClick={() => actions.expand(node)}
            data-testid="inspector-expand"
          >
            Expand
          </Button>
        </Stack>
      </Shell>
    )
  }

  const e = entity.data
  return (
    <Shell
      onClose={onClose}
      title={
        <Group gap={6} wrap="nowrap">
          <RefIcon iconRef={node.icon ?? kindIcon} size={18} />
          <div style={{ minWidth: 0 }}>
            <Text fw={600} size="sm" truncate data-testid="inspector-title">
              {node.redacted ? 'Restricted entity' : (node.displayName ?? node.name)}
            </Text>
            <Text size="xs" c="dimmed" truncate>
              {kindTitle}
              {e ? ` · ${e.metadata.project}` : ''}
            </Text>
          </div>
        </Group>
      }
    >
      {node.redacted ? (
        <Text size="sm">
          This entity belongs to a project you cannot read. Its relationships are shown, its details are not.
        </Text>
      ) : (
        <Stack gap="xs">
          <Group gap={4}>
            <HealthBadge state={node.health} size="xs" />
            <FreshnessBadge state={node.freshness} size="xs" />
            {!node.declared && (
              <Badge size="xs" variant="light" color="blue">
                observed only
              </Badge>
            )}
            {node.findings > 0 && (
              <Badge size="xs" color="orange" variant="light">
                {node.findings} findings
              </Badge>
            )}
          </Group>
          {node.via && (
            <Text size="xs">
              Reached via {node.via.map(typeTitle).join(' → ')} (weakest evidence:{' '}
              {evidenceMeta(node.impactEvidence).label})
            </Text>
          )}
          {entity.isPending ? (
            <Loader size="xs" />
          ) : e ? (
            <Stack gap={2}>
              {e.metadata.description && (
                <Text size="xs" lineClamp={3}>
                  {e.metadata.description}
                </Text>
              )}
              <Text size="xs" c="dimmed">
                Owner: {e.metadata.owner || '—'} · Environment: {e.metadata.environment || '—'}
              </Text>
              <Text size="xs" c="dimmed">
                Updated <TimeAgo value={e.metadata.updatedAt} /> · {e.status.facets} sources · {e.status.conflicts}{' '}
                conflicts
              </Text>
            </Stack>
          ) : null}
          <SimpleGrid cols={2} spacing={6}>
            <Button
              size="xs"
              variant="default"
              leftSection={<IconFocus2 size={14} />}
              onClick={() => actions.focus(node.id)}
            >
              Focus here
            </Button>
            <Button
              size="xs"
              variant="default"
              leftSection={<IconPlus size={14} />}
              onClick={() => actions.expand(node)}
              data-testid="inspector-expand"
            >
              Expand
            </Button>
            <Button
              size="xs"
              variant="default"
              leftSection={<IconArrowDownRight size={14} />}
              onClick={() => actions.impact(node.id, 'downstream')}
            >
              Downstream
            </Button>
            <Button
              size="xs"
              variant="default"
              leftSection={<IconArrowUpLeft size={14} />}
              onClick={() => actions.impact(node.id, 'upstream')}
            >
              Upstream
            </Button>
            <Button
              size="xs"
              variant="default"
              leftSection={<IconRoute size={14} />}
              onClick={() => actions.pathFrom(node.id)}
            >
              Path from
            </Button>
            <Button
              size="xs"
              variant="default"
              leftSection={<IconRoute size={14} />}
              onClick={() => actions.pathTo(node.id)}
            >
              Path to
            </Button>
          </SimpleGrid>
          <Group gap="xs">
            <AnchorLink to="/entities/$id" params={{ id: node.id }} size="xs">
              <IconExternalLink size={12} /> Details
            </AnchorLink>
            <AnchorLink
              to="/entities/$id"
              params={{ id: node.id }}
              search={{ tab: 'provenance' }}
              size="xs"
              data-testid="inspector-why"
            >
              <IconHelpHexagon size={12} /> Why (provenance)
            </AnchorLink>
            <AnchorLink to="/relations" search={{ entity: node.id }} size="xs">
              <IconTable size={12} /> Relations table
            </AnchorLink>
          </Group>
          <Divider />
          <Title order={4} fz={12}>
            Visible relationships ({out.length + inc.length})
          </Title>
          <Stack gap={3}>
            {out.slice(0, 12).map((x) => rel(x, 'out'))}
            {inc.slice(0, 12).map((x) => rel(x, 'in'))}
            {out.length + inc.length > 24 && (
              <Text size="xs" c="dimmed">
                … and {out.length + inc.length - 24} more in the relations table.
              </Text>
            )}
          </Stack>
        </Stack>
      )}
    </Shell>
  )
}

/** Selected relationship: endpoints, evidence class, the sources asserting it and the assertions behind it. */
export function EdgeInspector({
  edge,
  model,
  typeTitle,
  sourceColor,
  projectName,
  onClose,
}: {
  edge: MapEdge
  model: GraphModel
  typeTitle: (t: string) => string
  /** The source's colour in the colour-by-source mode. */
  sourceColor: (id: string) => string
  projectName: (id: string) => string
  onClose: () => void
}) {
  const real = !edge.id.startsWith('agg:') && edge.count <= 1
  const detail = useRelationDetail(edge.id, real)
  const from = model.nodes.get(edge.from)
  const to = model.nodes.get(edge.to)
  return (
    <Shell
      onClose={onClose}
      title={
        <Text fw={600} size="sm">
          {typeTitle(edge.type)}
        </Text>
      }
    >
      <Stack gap="xs">
        <Text size="sm">
          {from ? (
            <EntityLink id={from.id} name={from.displayName ?? from.name} kind={from.kind} redacted={from.redacted} />
          ) : (
            edge.from
          )}
        </Text>
        <Text size="xs" c="dimmed">
          {humanize(edge.type)} →
        </Text>
        <Text size="sm">
          {to ? (
            <EntityLink id={to.id} name={to.displayName ?? to.name} kind={to.kind} redacted={to.redacted} />
          ) : (
            edge.to
          )}
        </Text>
        <Group gap={4}>
          <EvidenceBadge cls={edge.evidence} size="xs" />
          {edge.crossProject && (
            <Badge size="xs" variant="outline" color="grape">
              cross-project
            </Badge>
          )}
          {edge.count > 1 && (
            <Badge size="xs" variant="outline">
              {edge.count} bundled
            </Badge>
          )}
        </Group>
        {edge.lastSeenAt && (
          <Text size="xs" c="dimmed">
            Last seen <TimeAgo value={edge.lastSeenAt} />
          </Text>
        )}
        <Divider label="Asserted by" labelPosition="left" />
        {edge.sources.length === 0 ? (
          <Text size="xs" c="dimmed">
            No source you can see.
          </Text>
        ) : (
          <Stack gap={4} data-testid="edge-sources" role="list" aria-label="Sources asserting this relationship">
            {edge.sources.map((s) => (
              <Group key={s.id} gap={6} wrap="nowrap" align="flex-start" role="listitem">
                <span
                  style={{
                    width: 14,
                    height: 4,
                    marginTop: 7,
                    borderRadius: 2,
                    background: sourceColor(s.id),
                    flexShrink: 0,
                  }}
                  aria-hidden
                />
                <div>
                  <Text size="xs" fw={s.id === edge.primarySource ? 600 : 400}>
                    {sourceLabel(s)}
                    {s.id === edge.primarySource ? ' (primary)' : ''}
                  </Text>
                  <Text size="xs" c="dimmed">
                    {[
                      projectName(s.projectId),
                      `${s.layer} layer`,
                      ...(edge.count > 1 ? [`${s.relations} of ${edge.count}`] : []),
                    ].join(' · ')}
                  </Text>
                </div>
              </Group>
            ))}
            {edge.count > 1 && !edge.primarySource && (
              <Text size="xs" c="dimmed">
                The bundled relationships have different primary sources.
              </Text>
            )}
          </Stack>
        )}
        {real && (
          <>
            <Divider label="Evidence" labelPosition="left" />
            {detail.isPending ? (
              <Loader size="xs" />
            ) : detail.isError ? (
              <Text size="xs" c="red">
                Could not load the relationship.
              </Text>
            ) : (
              (detail.data.assertions ?? []).map((a) => (
                <Stack key={a.id} gap={2}>
                  <Group gap={4}>
                    <Text size="xs" fw={600}>
                      {a.source}
                    </Text>
                    <EvidenceBadge cls={a.evidence_class} size="xs" />
                    {a.inferred && <Text size="xs">confidence {Math.round(a.confidence * 100)}%</Text>}
                  </Group>
                  <EvidenceList evidence={a.evidence} max={6} />
                </Stack>
              ))
            )}
          </>
        )}
      </Stack>
    </Shell>
  )
}
