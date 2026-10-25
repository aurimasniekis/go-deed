import { Badge, Checkbox, Group, Text, Tooltip } from '@mantine/core'
import { IconAlertTriangle, IconGitCompare } from '@tabler/icons-react'
import type { ColumnDef } from '@tanstack/react-table'
import type { Entity } from '@/api/client'
import { EntityLink, KindLabel, TimeAgo } from '@/components/common'
import { FreshnessBadge, HealthBadge } from '@/components/StatusBadge'
import { entityHealth } from '@/lib/status'

/** Column ID → server sort field (columns without one are not sortable: the API sorts, not the browser). */
export const SORT_FIELD: Record<string, string> = {
  name: 'name',
  kind: 'kind',
  environment: 'environment',
  health: 'health',
  freshness: 'freshness',
  updated: 'updated_at',
  created: 'created_at',
  lastSeen: 'last_seen_at',
}

export const DEFAULT_COLUMNS = [
  'name',
  'kind',
  'project',
  'environment',
  'owner',
  'health',
  'freshness',
  'findings',
  'updated',
]

export const COLUMN_LABELS: Record<string, string> = {
  name: 'Name',
  kind: 'Kind',
  project: 'Project',
  environment: 'Environment',
  owner: 'Owner',
  health: 'Health',
  freshness: 'Freshness',
  lifecycle: 'Lifecycle',
  version: 'Version',
  labels: 'Labels',
  findings: 'Findings',
  conflicts: 'Conflicts',
  sources: 'Sources',
  declared: 'Declared',
  valid: 'Valid',
  updated: 'Updated',
  created: 'Created',
  lastSeen: 'Last seen',
}

export function totalFindings(e: Entity): number {
  return Object.values(e.status.findings).reduce((s, n) => s + n, 0)
}

export function entityColumns(): ColumnDef<Entity>[] {
  return [
    {
      id: 'select',
      size: 36,
      enableHiding: false,
      header: ({ table }) => (
        <Checkbox
          aria-label="Select all loaded rows"
          checked={table.getIsAllRowsSelected()}
          indeterminate={table.getIsSomeRowsSelected()}
          onChange={table.getToggleAllRowsSelectedHandler()}
        />
      ),
      cell: ({ row }) => (
        <Checkbox
          aria-label={`Select ${row.original.metadata.name}`}
          checked={row.getIsSelected()}
          onChange={row.getToggleSelectedHandler()}
        />
      ),
    },
    {
      id: 'name',
      size: 260,
      header: 'Name',
      cell: ({ row }) => {
        const m = row.original.metadata
        return (
          <div style={{ minWidth: 0 }}>
            <EntityLink id={m.id} name={m.displayName || m.name} kind={row.original.kind} />
            {m.displayName && m.displayName !== m.name && (
              <Text size="xs" c="dimmed" className="mono" truncate>
                {m.name}
              </Text>
            )}
          </div>
        )
      },
    },
    { id: 'kind', size: 150, header: 'Kind', cell: ({ row }) => <KindLabel kind={row.original.kind} /> },
    {
      id: 'project',
      size: 110,
      header: 'Project',
      cell: ({ row }) => <Text size="sm">{row.original.metadata.project}</Text>,
    },
    {
      id: 'environment',
      size: 110,
      header: 'Environment',
      cell: ({ row }) => <Text size="sm">{row.original.metadata.environment || '—'}</Text>,
    },
    {
      id: 'owner',
      size: 130,
      header: 'Owner',
      cell: ({ row }) => <Text size="sm">{row.original.metadata.owner || '—'}</Text>,
    },
    {
      id: 'health',
      size: 110,
      header: 'Health',
      cell: ({ row }) => {
        const h = entityHealth(row.original.status)
        return <HealthBadge state={h.state} reason={h.reason} size="xs" />
      },
    },
    {
      id: 'freshness',
      size: 110,
      header: 'Freshness',
      cell: ({ row }) => <FreshnessBadge state={row.original.status.freshness.state} size="xs" />,
    },
    {
      id: 'lifecycle',
      size: 100,
      header: 'Lifecycle',
      cell: ({ row }) => <Text size="sm">{row.original.metadata.lifecycle || '—'}</Text>,
    },
    {
      id: 'version',
      size: 140,
      header: 'Version',
      cell: ({ row }) => {
        const v = row.original.metadata.version
        if (!v.declared && !v.observed)
          return (
            <Text size="sm" c="dimmed">
              —
            </Text>
          )
        const drift = v.declared && v.observed && v.declared !== v.observed
        return (
          <Tooltip
            label={`declared ${v.declared ?? '—'} · observed ${v.observed ?? '—'}${v.available ? ` · available ${v.available}` : ''}`}
          >
            <Group gap={4} wrap="nowrap">
              <Text size="sm" className="mono">
                {v.observed ?? v.declared}
              </Text>
              {drift && (
                <IconGitCompare
                  size={14}
                  color="var(--mantine-color-orange-6)"
                  aria-label="declared and observed differ"
                />
              )}
            </Group>
          </Tooltip>
        )
      },
    },
    {
      id: 'labels',
      size: 200,
      header: 'Labels',
      cell: ({ row }) => {
        const entries = Object.entries(row.original.metadata.labels)
        return (
          <Group gap={4} wrap="nowrap" style={{ overflow: 'hidden' }}>
            {entries.slice(0, 2).map(([k, v]) => (
              <Badge key={k} size="xs" variant="outline" color="gray" styles={{ label: { textTransform: 'none' } }}>
                {k}={v}
              </Badge>
            ))}
            {entries.length > 2 && (
              <Text size="xs" c="dimmed">
                +{entries.length - 2}
              </Text>
            )}
          </Group>
        )
      },
    },
    {
      id: 'findings',
      size: 90,
      header: 'Findings',
      cell: ({ row }) => {
        const n = totalFindings(row.original)
        return n ? (
          <Group gap={4} wrap="nowrap">
            <IconAlertTriangle size={14} color="var(--mantine-color-orange-6)" aria-hidden />
            <Text size="sm">{n}</Text>
          </Group>
        ) : (
          <Text size="sm" c="dimmed">
            0
          </Text>
        )
      },
    },
    {
      id: 'conflicts',
      size: 90,
      header: 'Conflicts',
      cell: ({ row }) => <Text size="sm">{row.original.status.conflicts}</Text>,
    },
    {
      id: 'sources',
      size: 80,
      header: 'Sources',
      cell: ({ row }) => <Text size="sm">{row.original.status.facets}</Text>,
    },
    {
      id: 'declared',
      size: 100,
      header: 'Declared',
      cell: ({ row }) => <Text size="sm">{row.original.status.declared ? 'Declared' : 'Observed only'}</Text>,
    },
    {
      id: 'valid',
      size: 80,
      header: 'Valid',
      cell: ({ row }) =>
        row.original.status.valid ? (
          <Text size="sm">Valid</Text>
        ) : (
          <Text size="sm" c="red">
            Invalid
          </Text>
        ),
    },
    {
      id: 'updated',
      size: 120,
      header: 'Updated',
      cell: ({ row }) => <TimeAgo value={row.original.metadata.updatedAt} />,
    },
    {
      id: 'created',
      size: 120,
      header: 'Created',
      cell: ({ row }) => <TimeAgo value={row.original.metadata.createdAt} />,
    },
    {
      id: 'lastSeen',
      size: 120,
      header: 'Last seen',
      cell: ({ row }) => <TimeAgo value={row.original.status.freshness.lastSeenAt} />,
    },
  ]
}

/** CSV row for an entity (plain values, not rendered cells). */
export function entityCsvRow(e: Entity, cols: string[]): unknown[] {
  const m = e.metadata
  const h = entityHealth(e.status)
  const values: Record<string, unknown> = {
    name: m.name,
    kind: e.kind,
    project: m.project,
    environment: m.environment ?? '',
    owner: m.owner ?? '',
    health: h.state,
    freshness: e.status.freshness.state,
    lifecycle: m.lifecycle ?? '',
    version: m.version.observed ?? m.version.declared ?? '',
    labels: Object.entries(m.labels)
      .map(([k, v]) => `${k}=${v}`)
      .join(';'),
    findings: totalFindings(e),
    conflicts: e.status.conflicts,
    sources: e.status.facets,
    declared: e.status.declared,
    valid: e.status.valid,
    updated: m.updatedAt,
    created: m.createdAt,
    lastSeen: e.status.freshness.lastSeenAt ?? '',
  }
  return ['id', ...cols].map((c) => (c === 'id' ? m.id : values[c]))
}
