/**
 * User-defined links (DISCOVERY Q-51): only https: URLs (http: when the project allows it), at most 2,048 characters,
 * rendered as plain anchors with rel="noopener noreferrer" — never as HTML.
 */
export function safeLinkUrl(raw: unknown, allowHttp = false): string | null {
  if (typeof raw !== 'string' || raw.length === 0 || raw.length > 2048) return null
  let u: URL
  try {
    u = new URL(raw)
  } catch {
    return null
  }
  if (u.protocol === 'https:' || (allowHttp && u.protocol === 'http:')) {
    if (u.username || u.password) return null
    return u.toString()
  }
  return null
}

/** Link type → Tabler icon name (see components/icons.tsx). */
export const linkTypeIcons: Record<string, string> = {
  repository: 'brand-git',
  repo: 'brand-git',
  docs: 'book',
  documentation: 'book',
  runbook: 'notebook',
  dashboard: 'chart-line',
  grafana: 'chart-line',
  console: 'terminal-2',
  admin: 'settings',
  website: 'world-www',
  homepage: 'world-www',
  issue: 'bug',
  issues: 'bug',
  tracker: 'list-check',
  ci: 'player-play',
  pipeline: 'player-play',
  alerting: 'bell',
  logs: 'file-text',
}

export function linkIconName(type: string | undefined): string {
  return linkTypeIcons[(type ?? '').toLowerCase()] ?? 'link'
}

/** Host shown next to a link so users can see where it goes before clicking. */
export function linkHost(url: string): string {
  try {
    return new URL(url).host
  } catch {
    return ''
  }
}
