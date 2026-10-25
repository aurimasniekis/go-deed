/** Formatting helpers (locale-aware, deterministic for tests when `now` is passed). */

const rtf = new Intl.RelativeTimeFormat('en', { numeric: 'auto' })
const nf = new Intl.NumberFormat('en')

const units: [Intl.RelativeTimeFormatUnit, number][] = [
  ['year', 365 * 24 * 3600],
  ['month', 30 * 24 * 3600],
  ['week', 7 * 24 * 3600],
  ['day', 24 * 3600],
  ['hour', 3600],
  ['minute', 60],
  ['second', 1],
]

export function toDate(v: string | number | Date | undefined | null): Date | null {
  if (v === undefined || v === null || v === '') return null
  const d = v instanceof Date ? v : new Date(v)
  if (Number.isNaN(d.getTime()) || d.getFullYear() < 1971) return null
  return d
}

/** "3 minutes ago", "in 2 days"; "never" for missing values. */
export function relativeTime(v: string | number | Date | undefined | null, now: number = Date.now()): string {
  const d = toDate(v)
  if (!d) return 'never'
  const diff = Math.round((d.getTime() - now) / 1000)
  const abs = Math.abs(diff)
  if (abs < 5) return 'just now'
  for (const [unit, secs] of units) {
    if (abs >= secs || unit === 'second') return rtf.format(Math.trunc(diff / secs), unit)
  }
  return rtf.format(diff, 'second')
}

/** 2026-10-24 21:58 (local time). */
export function dateTime(v: string | number | Date | undefined | null): string {
  const d = toDate(v)
  if (!d) return '—'
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

export function isoTitle(v: string | number | Date | undefined | null): string {
  const d = toDate(v)
  return d ? d.toISOString() : ''
}

export function count(n: number | undefined | null): string {
  return nf.format(n ?? 0)
}

export function plural(n: number, one: string, many = `${one}s`): string {
  return `${count(n)} ${n === 1 ? one : many}`
}

/** "1h 5m", "45s", "2d 3h". */
export function duration(seconds: number | undefined | null): string {
  if (seconds === undefined || seconds === null || seconds <= 0) return '—'
  const d = Math.floor(seconds / 86400)
  const h = Math.floor((seconds % 86400) / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  const s = Math.floor(seconds % 60)
  const parts: string[] = []
  if (d) parts.push(`${d}d`)
  if (h) parts.push(`${h}h`)
  if (m && parts.length < 2) parts.push(`${m}m`)
  if (s && parts.length === 0) parts.push(`${s}s`)
  return parts.join(' ') || '0s'
}

/** Human title for snake/kebab identifiers: "nats_subject" → "Nats subject". */
export function humanize(s: string): string {
  const t = s.replace(/[_-]+/g, ' ').trim()
  return t.charAt(0).toUpperCase() + t.slice(1)
}

/** Compact rendering of an arbitrary JSON value for tables and diffs. */
export function jsonValue(v: unknown): string {
  if (v === undefined) return '—'
  if (v === null) return 'null'
  if (typeof v === 'string') return v
  if (typeof v === 'number' || typeof v === 'boolean') return String(v)
  try {
    return JSON.stringify(v)
  } catch {
    return '[unserializable]'
  }
}

export function truncate(s: string, max: number): string {
  return s.length > max ? `${s.slice(0, max - 1)}…` : s
}
