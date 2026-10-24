import { Box, useComputedColorScheme } from '@mantine/core'
import { BarChart, PieChart } from 'echarts/charts'
import { AriaComponent, GridComponent, TooltipComponent } from 'echarts/components'
import * as echarts from 'echarts/core'
import { SVGRenderer } from 'echarts/renderers'
import { useEffect, useRef } from 'react'

echarts.use([BarChart, PieChart, GridComponent, TooltipComponent, AriaComponent, SVGRenderer])

export type EChartsOption = echarts.EChartsCoreOption

/**
 * Thin ECharts wrapper: SVG renderer (no eval, no canvas fingerprinting; inline style attributes only, which the CSP
 * allows), resizes with its box, replaces the option on change and forwards clicks on data items.
 */
export function EChart({
  option,
  height = 240,
  label,
  onItemClick,
  testId,
}: {
  option: EChartsOption
  height?: number
  label: string
  onItemClick?: (name: string, dataIndex: number) => void
  testId?: string
}) {
  const el = useRef<HTMLDivElement>(null)
  const chart = useRef<echarts.ECharts | null>(null)
  const click = useRef(onItemClick)
  const scheme = useComputedColorScheme('light')

  useEffect(() => {
    click.current = onItemClick
  }, [onItemClick])

  useEffect(() => {
    if (!el.current) return
    const c = echarts.init(el.current, undefined, { renderer: 'svg' })
    chart.current = c
    c.on('click', (p) => {
      const params = p as { name?: string; dataIndex?: number }
      if (params.name !== undefined) click.current?.(params.name, params.dataIndex ?? 0)
    })
    const ro = new ResizeObserver(() => c.resize())
    ro.observe(el.current)
    return () => {
      ro.disconnect()
      c.dispose()
      chart.current = null
    }
  }, [])

  useEffect(() => {
    chart.current?.setOption({ aria: { enabled: true, label: { description: label } }, ...option }, { notMerge: true })
  }, [option, label, scheme])

  return <Box ref={el} h={height} w="100%" data-testid={testId} role="img" aria-label={label} />
}
