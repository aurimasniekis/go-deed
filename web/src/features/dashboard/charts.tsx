import { AnchorLink } from '@/components/links'
import {
  ActionIcon,
  Card,
  Group,
  SegmentedControl,
  Table,
  Text,
  Title,
  Tooltip,
  useComputedColorScheme,
} from '@mantine/core'
import { IconChartBar, IconTable } from '@tabler/icons-react'
import { useMemo, useState, type ReactNode } from 'react'
import { EChart, type EChartsOption } from '@/components/EChart'
import { MetaBadge } from '@/components/StatusBadge'
import { count } from '@/lib/format'
import type { StatusMeta } from '@/lib/status'
import { chartTokens, type BarDatum } from '@/theme/chart'
import type { InventorySearch } from '@/lib/urlState'

/** A card with a title, a chart and an equivalent table (charts are never the only way to read the numbers). */
export function ChartCard({
  title,
  description,
  chart,
  table,
  empty,
}: {
  title: string
  description?: string
  chart: ReactNode
  table: ReactNode
  empty?: boolean
}) {
  const [mode, setMode] = useState<'chart' | 'table'>('chart')
  return (
    <Card>
      <Group justify="space-between" mb="xs" wrap="nowrap">
        <div>
          <Title order={2} fz={14}>
            {title}
          </Title>
          {description && (
            <Text size="xs" c="dimmed">
              {description}
            </Text>
          )}
        </div>
        {!empty && (
          <SegmentedControl
            size="xs"
            value={mode}
            onChange={(v) => setMode(v as 'chart' | 'table')}
            aria-label={`${title}: chart or table`}
            data={[
              {
                value: 'chart',
                label: (
                  <Tooltip label="Chart">
                    <IconChartBar size={14} aria-label="Chart" />
                  </Tooltip>
                ),
              },
              {
                value: 'table',
                label: (
                  <Tooltip label="Table">
                    <IconTable size={14} aria-label="Table" />
                  </Tooltip>
                ),
              },
            ]}
          />
        )}
      </Group>
      {empty ? (
        <Text size="sm" c="dimmed" py="lg" ta="center">
          No data yet.
        </Text>
      ) : mode === 'chart' ? (
        chart
      ) : (
        table
      )}
    </Card>
  )
}

/** Single-series horizontal bars (magnitude): one hue, value labels, click → filtered inventory. */
export function CountBars({
  data,
  label,
  onSelect,
}: {
  data: BarDatum[]
  label: string
  onSelect?: (d: BarDatum) => void
}) {
  const scheme = useComputedColorScheme('light')
  const option = useMemo<EChartsOption>(() => {
    const t = chartTokens(scheme)
    const rows = [...data].reverse()
    return {
      animation: false,
      textStyle: { fontFamily: t.fontFamily, color: t.text },
      grid: { left: 8, right: 40, top: 4, bottom: 4, containLabel: true },
      tooltip: {
        trigger: 'item',
        backgroundColor: t.tooltipBg,
        borderColor: t.tooltipBorder,
        textStyle: { color: t.text, fontSize: 12 },
        formatter: (p: { name: string; value: number }) => `${p.name}: <b>${count(p.value)}</b>`,
      },
      xAxis: { type: 'value', show: false },
      yAxis: {
        type: 'category',
        data: rows.map((d) => d.label),
        axisTick: { show: false },
        axisLine: { show: false },
        axisLabel: { color: t.muted, fontSize: 12, width: 130, overflow: 'truncate' },
      },
      series: [
        {
          type: 'bar',
          data: rows.map((d) => ({
            value: d.value,
            name: d.label,
            itemStyle: { color: d.key === '__other__' ? t.barMuted : t.bar },
          })),
          barMaxWidth: 14,
          itemStyle: { borderRadius: [0, 4, 4, 0] },
          label: {
            show: true,
            position: 'right',
            color: t.text,
            fontSize: 11,
            formatter: (p: { value: number }) => count(p.value),
          },
          cursor: onSelect ? 'pointer' : 'default',
        },
      ],
    }
  }, [data, scheme, onSelect])
  return (
    <EChart
      option={option}
      label={`${label}: ${data.map((d) => `${d.label} ${d.value}`).join(', ')}`}
      height={Math.max(120, data.length * 24 + 16)}
      onItemClick={(name) => {
        const d = data.find((x) => x.label === name)
        if (d && d.key !== '__other__') onSelect?.(d)
      }}
    />
  )
}

/** Status distribution donut: reserved status colours plus an icon + text legend (never colour alone). */
export function StatusDonut({
  counts,
  metaOf,
  order,
  label,
  linkFor,
}: {
  counts: Record<string, number>
  metaOf: (k: string) => StatusMeta
  order: string[]
  label: string
  linkFor: (k: string) => InventorySearch
}) {
  const scheme = useComputedColorScheme('light')
  const keys = useMemo(
    () => [
      ...order.filter((k) => (counts[k] ?? 0) > 0),
      ...Object.keys(counts).filter((k) => !order.includes(k) && (counts[k] ?? 0) > 0),
    ],
    [order, counts],
  )
  const total = keys.reduce((s, k) => s + (counts[k] ?? 0), 0)
  const option = useMemo<EChartsOption>(() => {
    const t = chartTokens(scheme)
    return {
      animation: false,
      tooltip: {
        trigger: 'item',
        backgroundColor: t.tooltipBg,
        borderColor: t.tooltipBorder,
        textStyle: { color: t.text, fontSize: 12 },
        formatter: (p: { name: string; value: number; percent: number }) =>
          `${p.name}: <b>${count(p.value)}</b> (${p.percent}%)`,
      },
      series: [
        {
          type: 'pie',
          radius: ['58%', '82%'],
          padAngle: 1.5,
          itemStyle: { borderColor: t.surface, borderWidth: 2, borderRadius: 3 },
          label: { show: false },
          data: keys.map((k) => ({
            name: metaOf(k).label,
            value: counts[k] ?? 0,
            itemStyle: { color: (t.status as Partial<Record<string, string>>)[k] ?? t.status.unknown },
          })),
        },
      ],
    }
  }, [keys, counts, metaOf, scheme])
  return (
    <Group wrap="nowrap" align="center" gap="md">
      <div style={{ width: 140, flexShrink: 0, position: 'relative' }}>
        <EChart
          option={option}
          height={140}
          label={`${label}: ${keys.map((k) => `${metaOf(k).label} ${counts[k]}`).join(', ')}`}
        />
        <Text
          ta="center"
          fw={600}
          size="lg"
          style={{
            position: 'absolute',
            inset: 0,
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'center',
            pointerEvents: 'none',
          }}
        >
          {count(total)}
        </Text>
      </div>
      <Table withRowBorders={false} verticalSpacing={2} fz="sm" aria-label={label}>
        <Table.Tbody>
          {keys.map((k) => (
            <Table.Tr key={k}>
              <Table.Td>
                <AnchorLink to="/inventory" search={linkFor(k)} underline="never">
                  <MetaBadge meta={metaOf(k)} tooltip="" />
                </AnchorLink>
              </Table.Td>
              <Table.Td ta="right" className="mono">
                {count(counts[k] ?? 0)}
              </Table.Td>
              <Table.Td ta="right" c="dimmed" w={48}>
                {total ? Math.round(((counts[k] ?? 0) / total) * 100) : 0}%
              </Table.Td>
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
    </Group>
  )
}

/** Table equivalent of CountBars with links into the filtered inventory. */
export function CountTable({
  data,
  linkFor,
  label,
}: {
  data: BarDatum[]
  linkFor: (d: BarDatum) => InventorySearch | null
  label: string
}) {
  return (
    <Table fz="sm" verticalSpacing={4} aria-label={label}>
      <Table.Tbody>
        {data.map((d) => {
          const s = linkFor(d)
          return (
            <Table.Tr key={d.key}>
              <Table.Td>
                {s ? (
                  <AnchorLink to="/inventory" search={s} size="sm">
                    {d.label}
                  </AnchorLink>
                ) : (
                  d.label
                )}
              </Table.Td>
              <Table.Td ta="right" className="mono">
                {count(d.value)}
              </Table.Td>
            </Table.Tr>
          )
        })}
      </Table.Tbody>
    </Table>
  )
}

export function TileIcon({ children }: { children: ReactNode }) {
  return (
    <ActionIcon variant="light" color="gray" size="lg" component="span" aria-hidden tabIndex={-1}>
      {children}
    </ActionIcon>
  )
}
