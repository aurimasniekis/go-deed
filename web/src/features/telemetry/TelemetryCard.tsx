import { Anchor, Badge, Card, Group, Stack, Table, Text, Tooltip, useComputedColorScheme } from '@mantine/core'
import { IconExternalLink } from '@tabler/icons-react'
import { LineChart } from 'echarts/charts'
import { GridComponent, LegendComponent } from 'echarts/components'
import * as echarts from 'echarts/core'
import { useMemo } from 'react'
import type { Schemas } from '@/api/client'
import { EChart, type EChartsOption } from '@/components/EChart'
import { MetaBadge } from '@/components/StatusBadge'
import { TimeAgo } from '@/components/common'
import { jsonValue } from '@/lib/format'
import { safeLinkUrl } from '@/lib/links'
import { chartTokens } from '@/theme/chart'

echarts.use([LineChart, GridComponent, LegendComponent])

export type TelemetryResult = Schemas['TelemetryResult']

/** Telemetry result status: always shown with its measurement time (TELEMETRY.md). */
export const TELEMETRY_STATUS: Record<
  string,
  { label: string; tone: 'green' | 'blue' | 'yellow' | 'gray'; icon: string; description: string }
> = {
  measured: {
    label: 'Measured',
    tone: 'green',
    icon: 'circle-check',
    description: 'A fresh value from the system of record.',
  },
  inferred: {
    label: 'Inferred',
    tone: 'blue',
    icon: 'bulb',
    description: 'Derived (counter delta, sampled rollup, or a link built locally).',
  },
  stale: {
    label: 'Stale',
    tone: 'yellow',
    icon: 'clock-x',
    description: 'The latest query failed; showing the last good value.',
  },
  unavailable: {
    label: 'Unavailable',
    tone: 'gray',
    icon: 'help-circle',
    description: 'No value: the integration is down, timed out, returned no data, or the binding does not apply.',
  },
}

export const TELEMETRY_HEALTH: Record<
  string,
  { label: string; tone: 'green' | 'yellow' | 'red'; icon: string; description: string }
> = {
  ok: { label: 'OK', tone: 'green', icon: 'circle-check', description: 'Within thresholds.' },
  warning: { label: 'Warning', tone: 'yellow', icon: 'alert-triangle', description: 'Above the warning threshold.' },
  critical: { label: 'Critical', tone: 'red', icon: 'alert-octagon', description: 'Above the critical threshold.' },
}

const compact = new Intl.NumberFormat('en', { notation: 'compact', maximumFractionDigits: 1 })

export function compactNumber(v: number): string {
  return compact.format(v)
}

export function formatValue(v: number | undefined, unit: string): string {
  if (v === undefined || !Number.isFinite(v)) return '—'
  if (unit === 'ratio' || unit === 'percent_ratio') return `${(v * 100).toFixed(v < 0.01 ? 2 : 1)} %`
  const abs = Math.abs(v)
  const s =
    abs >= 1000
      ? new Intl.NumberFormat('en', { maximumFractionDigits: 0 }).format(v)
      : abs >= 10
        ? v.toFixed(1)
        : v.toFixed(abs >= 1 ? 2 : 3)
  return unit ? `${s} ${unit}` : s
}

function Series({ r }: { r: TelemetryResult }) {
  const scheme = useComputedColorScheme('light')
  const series = useMemo(() => r.data?.series ?? [], [r.data])
  const option = useMemo<EChartsOption>(() => {
    const t = chartTokens(scheme)
    const many = series.length > 1
    return {
      animation: false,
      color: t.categorical,
      textStyle: { fontFamily: t.fontFamily, color: t.text },
      grid: { left: 8, right: 12, top: many ? 28 : 10, bottom: 4, containLabel: true },
      tooltip: {
        trigger: 'axis',
        backgroundColor: t.tooltipBg,
        borderColor: t.tooltipBorder,
        textStyle: { color: t.text, fontSize: 12 },
        valueFormatter: (v: number) => formatValue(v, r.unit),
      },
      ...(many ? { legend: { top: 0, textStyle: { color: t.muted, fontSize: 11 }, type: 'scroll' } } : {}),
      xAxis: {
        type: 'time',
        axisLine: { lineStyle: { color: t.axis } },
        axisLabel: { color: t.muted, fontSize: 11, hideOverlap: true },
        splitNumber: 4,
        splitLine: { show: false },
      },
      yAxis: {
        type: 'value',
        splitNumber: 3,
        axisLabel: {
          color: t.muted,
          fontSize: 11,
          formatter: (v: number) => (r.unit === 'ratio' ? formatValue(v, 'ratio') : compactNumber(v)),
        },
        splitLine: { lineStyle: { color: t.grid } },
      },
      series: series.slice(0, 8).map((s) => ({
        type: 'line',
        name:
          Object.entries(s.labels)
            .map(([k, v]) => `${k}=${v}`)
            .join(', ') || r.title,
        showSymbol: false,
        lineStyle: { width: 2 },
        data: s.points.map((p) => [p.t, p.v]),
      })),
    }
  }, [series, scheme, r.unit, r.title])
  if (series.length === 0)
    return (
      <Text size="sm" c="dimmed">
        No data points in this window.
      </Text>
    )
  return <EChart option={option} height={180} label={`${r.title} over ${r.window}`} />
}

/** One binding's result: status (+ time), value / chart / table / links, telemetry health, errors and notes. */
export function TelemetryCard({ r }: { r: TelemetryResult }) {
  const status = TELEMETRY_STATUS[r.status] ?? TELEMETRY_STATUS.unavailable
  const health = r.health ? TELEMETRY_HEALTH[r.health.state] : undefined
  const d = r.data
  return (
    <Card data-testid="telemetry-card" data-binding={r.binding}>
      <Group justify="space-between" wrap="nowrap" mb={6} align="flex-start">
        <div style={{ minWidth: 0 }}>
          <Text fw={600} size="sm" truncate>
            {r.title}
          </Text>
          <Text size="xs" c="dimmed">
            {r.integration} ({r.adapter}) · {r.window}
            {r.cached ? ' · cached' : ''}
          </Text>
        </div>
        <Stack gap={2} align="flex-end">
          {status && <MetaBadge meta={status} size="xs" />}
          <Text size="xs" c="dimmed">
            {r.measured_at ? (
              <>
                measured <TimeAgo value={r.measured_at} />
              </>
            ) : (
              'not measured'
            )}
          </Text>
        </Stack>
      </Group>
      {r.shape === 'scalar' && (
        <Group gap="sm" align="baseline">
          <Text fz={26} fw={650} c={r.status === 'unavailable' ? 'dimmed' : undefined} data-testid="telemetry-value">
            {formatValue(d?.value, r.unit)}
          </Text>
          {health && r.health && (
            <MetaBadge
              meta={{ ...health, label: `Telemetry health: ${health.label}` }}
              tooltip={r.health.reason || health.description}
              size="sm"
            />
          )}
        </Group>
      )}
      {r.shape === 'timeseries' && <Series r={r} />}
      {r.shape === 'table' && (d?.rows?.length ?? 0) > 0 && (
        <Table fz="xs" verticalSpacing={2} aria-label={r.title}>
          <Table.Thead>
            <Table.Tr>
              {Object.keys(d?.rows?.[0] ?? {}).map((k) => (
                <Table.Th key={k}>{k}</Table.Th>
              ))}
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {d?.rows?.slice(0, 20).map((row, i) => (
              <Table.Tr key={i}>
                {Object.values(row).map((v, j) => (
                  <Table.Td key={j} className="mono">
                    {jsonValue(v)}
                  </Table.Td>
                ))}
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      )}
      {(r.shape === 'link' || (d?.links?.length ?? 0) > 0) && (
        <Stack gap={4}>
          {(d?.links ?? []).map((l) => {
            const url = safeLinkUrl(l.url, true)
            return url ? (
              <Anchor key={l.url} href={url} target="_blank" rel="noopener noreferrer" size="sm">
                {l.title} <IconExternalLink size={12} aria-label="(opens in a new tab)" />
                {l.kind && (
                  <Badge size="xs" variant="outline" ml={6}>
                    {l.kind}
                  </Badge>
                )}
              </Anchor>
            ) : (
              <Text key={l.url} size="sm" c="dimmed">
                {l.title} (blocked URL)
              </Text>
            )
          })}
          {r.shape === 'link' && (d?.links?.length ?? 0) === 0 && (
            <Text size="sm" c="dimmed">
              No links found.
            </Text>
          )}
        </Stack>
      )}
      {r.error && (
        <Tooltip label={r.error} multiline maw={360}>
          <Text size="xs" c={r.status === 'stale' ? 'yellow.8' : 'red'} mt={4} lineClamp={2}>
            {r.error}
          </Text>
        </Tooltip>
      )}
      {d?.note && (
        <Text size="xs" c="dimmed" mt={4}>
          {d.note}
        </Text>
      )}
      {d?.truncated && (
        <Text size="xs" c="dimmed">
          Result truncated.
        </Text>
      )}
    </Card>
  )
}
