import { ActionIcon, Group, ScrollArea, Stack, Text, Tooltip } from '@mantine/core'
import { IconX } from '@tabler/icons-react'
import { MetaBadge } from '@/components/StatusBadge'
import { FRESHNESS, HEALTH } from '@/lib/status'
import { STROKE_DASH, type Legend as LegendModel, type StrokeKind } from './edgeStyle'

function StrokeSample({ kind, color = 'currentColor' }: { kind: StrokeKind; color?: string }) {
  const dash = STROKE_DASH[kind]
  return (
    <svg width={36} height={10} aria-hidden style={{ flexShrink: 0 }}>
      <line
        x1={2}
        y1={5}
        x2={34}
        y2={5}
        stroke={color}
        strokeWidth={kind === 'double' ? 4.5 : kind === 'observed' ? 1.4 : 2}
        strokeDasharray={dash}
        strokeLinecap={kind === 'dotted' ? 'round' : 'butt'}
        opacity={kind === 'faded' ? 0.4 : 1}
      />
      {kind === 'double' && <line x1={2} y1={5} x2={34} y2={5} stroke="var(--deed-canvas)" strokeWidth={1.6} />}
    </svg>
  )
}

/** Auto-generated legend: explains every colour, stroke and animation currently on screen. */
export function MapLegend({
  legend,
  onClose,
  showNodes,
  lod = false,
}: {
  legend: LegendModel
  onClose: () => void
  showNodes: boolean
  /** The simplified zoomed-out drawing is on screen (nodes as blocks). */
  lod?: boolean
}) {
  return (
    <div className="deed-panel" style={{ width: 260 }} data-testid="map-legend" role="region" aria-label="Map legend">
      <Group justify="space-between" px="sm" pt={6}>
        <Text size="xs" fw={700} tt="uppercase" c="dimmed" style={{ letterSpacing: '0.04em' }}>
          Legend
        </Text>
        <Tooltip label="Hide legend (l)">
          <ActionIcon size="sm" onClick={onClose} aria-label="Hide legend">
            <IconX size={12} />
          </ActionIcon>
        </Tooltip>
      </Group>
      <ScrollArea.Autosize
        mah={280}
        type="auto"
        px="sm"
        pb="sm"
        viewportProps={{ tabIndex: 0, 'aria-label': 'Legend entries' }}
      >
        <Stack gap={8}>
          {legend.colors.length > 0 && (
            <Stack gap={3}>
              <Text size="xs" fw={600}>
                Edge colour
              </Text>
              {legend.colors.map((c) => (
                <Group key={c.key} gap={6} wrap="nowrap" data-legend-color={c.color} data-legend-key={c.key}>
                  <span
                    style={{ width: 14, height: 4, borderRadius: 2, background: c.color, flexShrink: 0 }}
                    aria-hidden
                  />
                  <Text size="xs" lineClamp={2} title={c.description ? `${c.label} (${c.description})` : c.label}>
                    {c.label}
                    {c.description ? ` (${c.description})` : ''}
                  </Text>
                </Group>
              ))}
            </Stack>
          )}
          {legend.strokes.length > 0 && (
            <Stack gap={3}>
              <Text size="xs" fw={600}>
                Line style (evidence)
              </Text>
              {legend.strokes.map((s) => (
                <Group key={s.kind} gap={6} wrap="nowrap" align="flex-start">
                  <StrokeSample kind={s.kind} />
                  <Text size="xs">
                    <b>{s.label}</b>: {s.description}
                  </Text>
                </Group>
              ))}
            </Stack>
          )}
          {legend.animations.length > 0 && (
            <Stack gap={3}>
              <Text size="xs" fw={600}>
                Animation
              </Text>
              {legend.animations.map((a) => (
                <Text key={a.label} size="xs">
                  <b>{a.label}</b>: {a.description}
                </Text>
              ))}
            </Stack>
          )}
          {showNodes && lod && (
            <Stack gap={3} data-testid="map-legend-lod">
              <Text size="xs" fw={600}>
                Nodes (zoomed out)
              </Text>
              <Text size="xs">
                One block per entity in its kind colour; stacked block: cluster. Thick outline: focus (indigo, set
                apart), failed health (red, double), warning (yellow). Names, freshness and findings show when you zoom
                in.
              </Text>
            </Stack>
          )}
          {showNodes && !lod && (
            <Stack gap={3}>
              <Text size="xs" fw={600}>
                Nodes
              </Text>
              <Text size="xs">Left border: kind colour. Stacked card: cluster (click to expand). Ring: focus.</Text>
              <Group gap={4}>
                {Object.values(HEALTH).map((m) => (
                  <MetaBadge key={m.label} meta={m} size="xs" tooltip={m.description} />
                ))}
              </Group>
              <Group gap={4}>
                {Object.values(FRESHNESS).map((m) => (
                  <MetaBadge key={m.label} meta={m} size="xs" tooltip={m.description} />
                ))}
              </Group>
            </Stack>
          )}
          {legend.notes.map((n) => (
            <Text key={n} size="xs" c="dimmed">
              {n}
            </Text>
          ))}
        </Stack>
      </ScrollArea.Autosize>
    </div>
  )
}
