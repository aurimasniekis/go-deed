/**
 * Chart tokens (dataviz reference palette, validated for both surfaces): one sequential blue for single-series
 * magnitude bars, a fixed categorical order for identity, and the reserved status colours — status always ships with
 * an icon and a label as well.
 */
export interface ChartTokens {
  bar: string
  barMuted: string
  categorical: string[]
  status: Record<'ok' | 'warning' | 'failed' | 'unknown' | 'fresh' | 'aging' | 'stale', string>
  text: string
  muted: string
  grid: string
  axis: string
  surface: string
  tooltipBg: string
  tooltipBorder: string
  fontFamily: string
}

const font = "'Inter Variable', Inter, system-ui, sans-serif"

export const lightTokens: ChartTokens = {
  bar: '#2a78d6',
  barMuted: '#9ec5f4',
  categorical: ['#2a78d6', '#eb6834', '#1baf7a', '#eda100', '#e87ba4', '#008300', '#6250d6', '#e34948'],
  status: {
    ok: '#2f9e44',
    fresh: '#2f9e44',
    warning: '#e8a400',
    aging: '#e8a400',
    failed: '#e03131',
    stale: '#e03131',
    unknown: '#adb5bd',
  },
  text: '#212529',
  muted: '#495057',
  grid: '#e9ecef',
  axis: '#ced4da',
  surface: '#ffffff',
  tooltipBg: '#ffffff',
  tooltipBorder: '#dee2e6',
  fontFamily: font,
}

export const darkTokens: ChartTokens = {
  bar: '#3987e5',
  barMuted: '#184f95',
  categorical: ['#3987e5', '#d95926', '#199e70', '#c98500', '#d55181', '#008300', '#9085e9', '#e66767'],
  status: {
    ok: '#40c057',
    fresh: '#40c057',
    warning: '#fab005',
    aging: '#fab005',
    failed: '#ff6b6b',
    stale: '#ff6b6b',
    unknown: '#868e96',
  },
  text: '#e9ecef',
  muted: '#adb5bd',
  grid: '#373a40',
  axis: '#5c5f66',
  surface: '#242424',
  tooltipBg: '#2c2e33',
  tooltipBorder: '#495057',
  fontFamily: font,
}

export function chartTokens(scheme: 'light' | 'dark'): ChartTokens {
  return scheme === 'dark' ? darkTokens : lightTokens
}

export interface BarDatum {
  key: string
  label: string
  value: number
}

/** Sorts descending and folds everything past `max` into "Other" (never a generated extra colour). */
export function topN(counts: Record<string, number>, max = 10, labelOf: (k: string) => string = (k) => k): BarDatum[] {
  const rows = Object.entries(counts)
    .map(([key, value]) => ({ key, label: labelOf(key), value }))
    .sort((a, b) => b.value - a.value || a.label.localeCompare(b.label))
  if (rows.length <= max) return rows
  const head = rows.slice(0, max - 1)
  const rest = rows.slice(max - 1).reduce((s, r) => s + r.value, 0)
  return [...head, { key: '__other__', label: `Other (${rows.length - max + 1})`, value: rest }]
}
