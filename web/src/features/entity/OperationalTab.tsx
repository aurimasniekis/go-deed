import { Badge, Card, Group, SegmentedControl, SimpleGrid, Stack, Table, Text, Title, Tooltip } from '@mantine/core'
import { IconActivity } from '@tabler/icons-react'
import { useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import type { Entity } from '@/api/client'
import { keys } from '@/api/queries'
import { EmptyState, ErrorState, LoadingBlock } from '@/components/States'
import { TelemetryCard } from '@/features/telemetry/TelemetryCard'
import { useTelemetryStream } from '@/features/telemetry/stream'
import { jsonValue } from '@/lib/format'
import { flatten, pointerLabel } from '@/lib/provenance'
import { TELEMETRY_WINDOWS, useTelemetry, type TelemetryWindow } from './queries'

const streamLabel: Record<string, { label: string; color: string; tip: string }> = {
  live: { label: 'Live', color: 'green', tip: 'Values refresh every 30 seconds over the telemetry stream.' },
  connecting: { label: 'Connecting', color: 'gray', tip: 'Opening the telemetry stream…' },
  reconnecting: { label: 'Reconnecting', color: 'yellow', tip: 'The telemetry stream dropped; reconnecting.' },
  closed: { label: 'Paused', color: 'gray', tip: 'The telemetry stream closed; values are not refreshing.' },
  idle: { label: 'Paused', color: 'gray', tip: 'Not streaming.' },
}

/**
 * Operational data from telemetry bindings (GET /entities/{id}/telemetry): scalar values, time series, tables and links,
 * each with its status (measured, inferred, stale, unavailable) and measurement time. Telemetry health is kept apart
 * from structural health. Updates arrive over the telemetry stream; no placeholder charts are ever drawn.
 */
export function OperationalTab({ e }: { e: Entity }) {
  const id = e.metadata.id
  const qc = useQueryClient()
  const [window, setWindow] = useState<TelemetryWindow>('1h')
  const tel = useTelemetry(id, window)
  const stream = useTelemetryStream({
    enabled: tel.isSuccess && tel.data.results.length > 0,
    entity: id,
    window,
    interval: 30,
    onTelemetry: (ev) => {
      if (ev.entity_id !== id) return
      qc.setQueryData(keys.entityPart(id, 'telemetry', { window }), {
        entity_id: id,
        window: ev.window,
        results: ev.results,
      })
    },
  })
  const observed = [...flatten(e.status.observed, '/status/observed')]
  const results = tel.data?.results ?? []
  const charts = results.filter((r) => r.role !== 'link')
  const links = results.filter((r) => r.role === 'link')
  const sl = streamLabel[stream] ?? streamLabel.idle
  return (
    <Stack gap="sm">
      <Group justify="space-between">
        <Group gap={6}>
          <IconActivity size={16} aria-hidden />
          <Title order={2} fz={14}>
            Telemetry
          </Title>
          {results.length > 0 && sl && (
            <Tooltip label={sl.tip}>
              <Badge size="sm" variant="light" color={sl.color} data-testid="telemetry-stream">
                {sl.label}
              </Badge>
            </Tooltip>
          )}
        </Group>
        <SegmentedControl
          aria-label="Time window"
          value={window}
          onChange={(v) => setWindow(v as TelemetryWindow)}
          data={[...TELEMETRY_WINDOWS]}
        />
      </Group>
      {tel.isPending ? (
        <SimpleGrid cols={{ base: 1, md: 2, xl: 3 }}>
          <LoadingBlock height={140} />
          <LoadingBlock height={140} />
          <LoadingBlock height={140} />
        </SimpleGrid>
      ) : tel.isError ? (
        <ErrorState error={tel.error} what="telemetry" onRetry={() => void tel.refetch()} />
      ) : results.length === 0 ? (
        <EmptyState title="No telemetry bindings apply">
          Operational charts appear when an admin binds a query (Prometheus, Loki, NATS monitoring, OpenSearch, Grafana
          or Pulse) to the {e.kind} kind in Settings › Telemetry. Deed shows nothing rather than placeholder data.
        </EmptyState>
      ) : (
        <>
          <SimpleGrid cols={{ base: 1, md: 2, xl: 3 }} spacing="sm">
            {charts.map((r) => (
              <TelemetryCard key={r.binding} r={r} />
            ))}
          </SimpleGrid>
          {links.length > 0 && (
            <SimpleGrid cols={{ base: 1, md: 2 }} spacing="sm">
              {links.map((r) => (
                <TelemetryCard key={r.binding} r={r} />
              ))}
            </SimpleGrid>
          )}
          <Text size="xs" c="dimmed">
            Values are bounded summaries queried from your systems and cached for each binding’s freshness period; raw
            samples are never stored.
          </Text>
        </>
      )}
      <Card>
        <Title order={3} fz={13} mb={2}>
          Latest observation summary
        </Title>
        <Text size="xs" c="dimmed" mb="xs">
          Values pushed by observation-layer sources. They expire when the source stops reporting.
        </Text>
        {observed.length === 0 ? (
          <Text size="sm" c="dimmed">
            No observation source reports values for this entity.
          </Text>
        ) : (
          <Table fz="sm" verticalSpacing={4} aria-label="Observed values">
            <Table.Tbody>
              {observed.map(([p, v]) => (
                <Table.Tr key={p}>
                  <Table.Td className="mono" fz="xs">
                    {pointerLabel(p).replace(/^status › observed › /, '')}
                  </Table.Td>
                  <Table.Td className="mono">{jsonValue(v)}</Table.Td>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        )}
      </Card>
    </Stack>
  )
}
