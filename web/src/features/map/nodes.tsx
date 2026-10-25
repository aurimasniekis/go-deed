import { ActionIcon, Group, Text, Tooltip } from '@mantine/core'
import { IconAlertTriangle, IconChevronDown, IconLock, IconStack2 } from '@tabler/icons-react'
import { Handle, Position, type Node, type NodeProps } from '@xyflow/react'
import { memo } from 'react'
import { NamedIcon, RefIcon } from '@/components/icons'
import { freshnessMeta, healthMeta } from '@/lib/status'
import { toggleCollapsed } from './mapStore'
import type { MapNode } from './types'

export interface EntityNodeData extends Record<string, unknown> {
  node: MapNode
  kindTitle: string
  kindIcon: string
  kindColor: string
  compact: boolean
  iconSize: number
  sourcePos: Position
  targetPos: Position
  dimmed: boolean
}

export interface GroupNodeData extends Record<string, unknown> {
  key: string
  label: string
  count: number
}

export type EntityFlowNode = Node<EntityNodeData, 'entity'>
export type ClusterFlowNode = Node<EntityNodeData, 'cluster'>
export type GroupFlowNode = Node<GroupNodeData, 'group'>
export type LodFlowNode = Node<EntityNodeData, 'lod'>

export const NODE_SIZE = {
  detailed: { width: 224, height: 66 },
  compact: { width: 176, height: 38 },
  cluster: { width: 210, height: 58 },
}

/**
 * Level of detail (docs/UI.md § Performance): a map with at least `minNodes` nodes, zoomed out below `enter`, draws each
 * entity or cluster as a plain block in its kind colour (LodNode) and its relationships as straight lines in one layer
 * (LodEdges) — card text is unreadable that far out, and thousands of cards and edge components are what made panning
 * slow. Zooming back in above `leave` restores cards and edges; the gap between the two avoids flapping.
 */
export const LOD = { minNodes: 200, enter: 0.4, leave: 0.45 }

const toneVar: Record<string, string> = {
  green: 'var(--mantine-color-green-filled)',
  yellow: 'var(--mantine-color-yellow-filled)',
  red: 'var(--mantine-color-red-filled)',
  gray: 'var(--mantine-color-gray-6)',
}

/** Health/freshness as icon + short text (never colour alone). */
function StatusChip({ meta, prefix }: { meta: { label: string; tone: string; icon: string }; prefix: string }) {
  return (
    <Group
      gap={2}
      wrap="nowrap"
      style={{ color: toneVar[meta.tone] ?? toneVar.gray }}
      aria-label={`${prefix}: ${meta.label}`}
    >
      <NamedIcon name={meta.icon} size={12} />
      <Text span fz={10} fw={600} c="var(--mantine-color-text)">
        {meta.label}
      </Text>
    </Group>
  )
}

function Handles({ sourcePos, targetPos }: { sourcePos: Position; targetPos: Position }) {
  return (
    <>
      <Handle type="target" position={targetPos} isConnectable={false} style={{ opacity: 0, width: 6, height: 6 }} />
      <Handle type="source" position={sourcePos} isConnectable={false} style={{ opacity: 0, width: 6, height: 6 }} />
    </>
  )
}

/** Entity card: kind icon and colour, name, kind, health and freshness badges, findings count. */
export const EntityNode = memo(function EntityNode({ data, selected }: NodeProps<EntityFlowNode>) {
  const n = data.node
  const h = healthMeta(n.health)
  const f = freshnessMeta(n.freshness)
  const size = data.compact ? NODE_SIZE.compact : NODE_SIZE.detailed
  return (
    <div
      className="deed-node"
      data-selected={selected || undefined}
      data-root={n.root || undefined}
      data-dimmed={data.dimmed || undefined}
      data-health={n.health}
      style={{ width: size.width, height: size.height, borderLeftColor: data.kindColor }}
      data-testid="map-node"
      data-entity-id={n.id}
    >
      <Handles sourcePos={data.sourcePos} targetPos={data.targetPos} />
      <Group gap={8} wrap="nowrap" h="100%" px={8}>
        {n.redacted ? (
          <IconLock size={data.iconSize} aria-hidden />
        ) : (
          <RefIcon iconRef={n.icon ?? data.kindIcon} size={data.iconSize} />
        )}
        <div style={{ minWidth: 0, flex: 1 }}>
          <Text fz={12} fw={600} truncate lh={1.25} title={n.displayName ?? n.name}>
            {n.redacted ? 'restricted' : (n.displayName ?? n.name)}
          </Text>
          {!data.compact && (
            <>
              <Text fz={10} c="dimmed" truncate lh={1.3}>
                {data.kindTitle}
                {n.environment ? ` · ${n.environment}` : ''}
              </Text>
              <Group gap={8} wrap="nowrap" mt={2}>
                <StatusChip meta={h} prefix="Health" />
                <StatusChip meta={f} prefix="Freshness" />
                {n.findings > 0 && (
                  <Group gap={2} wrap="nowrap" aria-label={`${n.findings} open findings`}>
                    <IconAlertTriangle size={12} color="var(--mantine-color-orange-filled)" aria-hidden />
                    <Text span fz={10} fw={600}>
                      {n.findings}
                    </Text>
                  </Group>
                )}
              </Group>
            </>
          )}
        </div>
        {data.compact && (
          <span
            className="deed-node-dot"
            style={{ background: toneVar[h.tone] }}
            aria-hidden
            title={`Health: ${h.label}`}
          />
        )}
      </Group>
    </div>
  )
})

/** Server cluster (overview group or high-degree fan) or a collapsed visual group: expands on click/Enter. */
export const ClusterNode = memo(function ClusterNode({ data, selected }: NodeProps<ClusterFlowNode>) {
  const n = data.node
  const h = healthMeta(n.health)
  const c = n.cluster
  const isGroup = n.kind === 'group'
  return (
    <div
      className="deed-node deed-cluster"
      data-selected={selected || undefined}
      data-dimmed={data.dimmed || undefined}
      style={{ width: NODE_SIZE.cluster.width, height: NODE_SIZE.cluster.height, borderLeftColor: data.kindColor }}
      data-testid="map-cluster"
      data-cluster-key={c?.key}
    >
      <Handles sourcePos={data.sourcePos} targetPos={data.targetPos} />
      <Group gap={8} wrap="nowrap" h="100%" px={10}>
        <div className="deed-cluster-stack" aria-hidden>
          {isGroup ? <IconStack2 size={data.iconSize} /> : <RefIcon iconRef={data.kindIcon} size={data.iconSize} />}
        </div>
        <div style={{ minWidth: 0, flex: 1 }}>
          <Text fz={12} fw={600} truncate>
            {isGroup ? n.name : data.kindTitle}
          </Text>
          <Text fz={10} c="dimmed" truncate>
            {c?.count ?? 0} {(c?.count ?? 0) === 1 ? 'entity' : 'entities'}
            {isGroup ? ' collapsed' : c?.groupBy && c.groupBy !== 'kind' ? ` · ${c.groupBy}` : ''} · expand
          </Text>
          <StatusChip meta={h} prefix="Worst health" />
        </div>
      </Group>
    </div>
  )
})

/** Visual group container (client-side clustering) with a collapse toggle. */
export const GroupNode = memo(function GroupNode({ data }: NodeProps<GroupFlowNode>) {
  return (
    <div className="deed-group" data-testid="map-group">
      <Group gap={4} px={8} py={4} wrap="nowrap" className="deed-group-header">
        <Tooltip label="Collapse this group into one node">
          <ActionIcon
            size="xs"
            className="nodrag"
            aria-label={`Collapse group ${data.label}`}
            onClick={(e) => {
              e.stopPropagation()
              toggleCollapsed(data.key)
            }}
          >
            <IconChevronDown size={12} />
          </ActionIcon>
        </Tooltip>
        <Text fz={11} fw={600} truncate>
          {data.label}
        </Text>
        <Text fz={10} c="dimmed">
          {data.count}
        </Text>
      </Group>
    </div>
  )
})

/**
 * Level-of-detail node: one block in the kind colour, an outline for failed or warning health, a stack for clusters.
 * No text, icons or handles (edges are drawn by LodEdges while it is shown); the accessible name stays on the node
 * wrapper (ariaLabel) and the title gives the name on hover.
 */
export const LodNode = memo(function LodNode({ data, selected }: NodeProps<LodFlowNode>) {
  const n = data.node
  const size = n.cluster ? NODE_SIZE.cluster : data.compact ? NODE_SIZE.compact : NODE_SIZE.detailed
  return (
    <div
      className="deed-node-lod"
      data-selected={selected || undefined}
      data-root={n.root || undefined}
      data-dimmed={data.dimmed || undefined}
      data-health={n.health}
      data-cluster={n.cluster ? true : undefined}
      style={{ width: size.width, height: size.height, background: data.kindColor }}
      title={`${n.cluster ? data.kindTitle : n.redacted ? 'restricted' : (n.displayName ?? n.name)} · health ${n.health}`}
      {...(n.cluster
        ? { 'data-testid': 'map-cluster', 'data-cluster-key': n.cluster.key }
        : { 'data-testid': 'map-node', 'data-entity-id': n.id })}
    />
  )
})

export const nodeTypes = { entity: EntityNode, cluster: ClusterNode, group: GroupNode, lod: LodNode }
