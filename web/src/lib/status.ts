/**
 * Status vocabularies. Status is never conveyed by colour alone: every state has an icon name and a text label too
 * (DISCOVERY Q-49).
 */

export type Tone = 'green' | 'yellow' | 'red' | 'gray' | 'orange' | 'blue' | 'grape' | 'teal' | 'deed'

export interface StatusMeta {
  label: string
  tone: Tone
  icon: string
  description: string
}

export const HEALTH: Record<string, StatusMeta> = {
  ok: { label: 'Healthy', tone: 'green', icon: 'circle-check', description: 'The latest health report is OK.' },
  warning: { label: 'Warning', tone: 'yellow', icon: 'alert-triangle', description: 'A source reports a warning.' },
  failed: { label: 'Failed', tone: 'red', icon: 'circle-x', description: 'A source reports a failure.' },
  unknown: { label: 'Unknown', tone: 'gray', icon: 'help-circle', description: 'No current health report.' },
}

export const FRESHNESS: Record<string, StatusMeta> = {
  fresh: { label: 'Fresh', tone: 'green', icon: 'clock-check', description: 'Seen within the expected interval.' },
  aging: { label: 'Aging', tone: 'yellow', icon: 'clock-pause', description: 'Not seen for longer than expected.' },
  stale: { label: 'Stale', tone: 'red', icon: 'clock-x', description: 'Not seen for much longer than expected.' },
  unknown: { label: 'Unknown', tone: 'gray', icon: 'clock-question', description: 'Never observed by a source.' },
}

export const EVIDENCE: Record<string, StatusMeta> = {
  declared: {
    label: 'Declared',
    tone: 'deed',
    icon: 'file-check',
    description: 'Declared by a person or Terraform (strongest evidence).',
  },
  structural: {
    label: 'Structural',
    tone: 'teal',
    icon: 'hierarchy',
    description: 'Follows from structure reported by a source (e.g. a container on its host).',
  },
  observed: {
    label: 'Observed',
    tone: 'blue',
    icon: 'eye',
    description: 'Observed at runtime (traffic, subscriptions, connections).',
  },
  inferred: {
    label: 'Inferred',
    tone: 'orange',
    icon: 'bulb',
    description: 'Inferred from indirect evidence; verify before relying on it.',
  },
}

export const SEVERITY: Record<string, StatusMeta> = {
  critical: { label: 'Critical', tone: 'red', icon: 'alert-octagon', description: 'Needs attention now.' },
  warning: { label: 'Warning', tone: 'yellow', icon: 'alert-triangle', description: 'Should be looked at.' },
  info: { label: 'Info', tone: 'blue', icon: 'info-circle', description: 'For awareness.' },
}

export const LAYER: Record<string, StatusMeta & { rank: number }> = {
  override: { label: 'Override', tone: 'grape', icon: 'pencil-bolt', description: 'Manual override.', rank: 100 },
  declared: { label: 'Declared', tone: 'deed', icon: 'file-check', description: 'Declarations.', rank: 80 },
  annotation: { label: 'Annotation', tone: 'teal', icon: 'note', description: 'UI annotations.', rank: 70 },
  imported: {
    label: 'Imported',
    tone: 'blue',
    icon: 'file-import',
    description: 'Imports (state, snapshots).',
    rank: 60,
  },
  discovered: { label: 'Discovered', tone: 'orange', icon: 'radar', description: 'Runtime discovery.', rank: 40 },
  observation: {
    label: 'Observation',
    tone: 'gray',
    icon: 'activity',
    description: 'Time-bounded observations.',
    rank: 0,
  },
  rule: {
    label: 'Label rule',
    tone: 'gray',
    icon: 'tags',
    description: "A project label rule (below every source; a source's own label wins).",
    rank: 5,
  },
}

export const SUPPORT_LEVEL: Record<string, StatusMeta> = {
  modeling: { label: 'Modeling', tone: 'gray', icon: 'pencil', description: 'Can be declared and related by hand.' },
  terraform: {
    label: 'Terraform',
    tone: 'grape',
    icon: 'brand-terraform',
    description: 'Managed with the Terraform provider.',
  },
  runtime: {
    label: 'Runtime',
    tone: 'blue',
    icon: 'radar',
    description: 'Discovered at runtime by an agent or push source.',
  },
  native: { label: 'Native', tone: 'teal', icon: 'plug-connected', description: 'Pulled by a built-in integration.' },
}

const unknownMeta = (value: string): StatusMeta => ({
  label: value ? value.charAt(0).toUpperCase() + value.slice(1) : 'Unknown',
  tone: 'gray',
  icon: 'help-circle',
  description: '',
})

export function healthMeta(state: string | undefined | null): StatusMeta {
  return HEALTH[state ?? 'unknown'] ?? unknownMeta(state ?? '')
}

export function freshnessMeta(state: string | undefined | null): StatusMeta {
  return FRESHNESS[state ?? 'unknown'] ?? unknownMeta(state ?? '')
}

export function evidenceMeta(cls: string | undefined | null): StatusMeta {
  return EVIDENCE[cls ?? ''] ?? unknownMeta(cls ?? '')
}

export function severityMeta(s: string | undefined | null): StatusMeta {
  return SEVERITY[s ?? ''] ?? unknownMeta(s ?? '')
}

export function layerMeta(l: string | undefined | null): StatusMeta & { rank: number } {
  return LAYER[l ?? ''] ?? { ...unknownMeta(l ?? ''), rank: -1 }
}

/** Observed relationships not seen for this long are stale (the graph API's include_stale window). */
export const STALE_RELATION_MS = 7 * 24 * 3600 * 1000

/** Declared and structural relationships never go stale; observed and inferred ones do when not seen recently. */
export function isStaleRelation(evidence: string, lastSeenAt: string | undefined, now = Date.now()): boolean {
  if (evidence === 'declared' || evidence === 'structural' || !lastSeenAt) return false
  const t = Date.parse(lastSeenAt)
  return Number.isFinite(t) && now - t > STALE_RELATION_MS
}

/** Health of an entity: status.health is {state, reason, …} or empty. */
export function entityHealth(status: { health?: Record<string, unknown> } | undefined): {
  state: string
  reason: string
} {
  const h = status?.health ?? {}
  const state = typeof h.state === 'string' ? h.state : 'unknown'
  const reason = typeof h.reason === 'string' ? h.reason : ''
  return { state, reason }
}

/** Version intelligence (GET /versions). */
export const VERSION_STATUS: Record<string, StatusMeta> = {
  eol: {
    label: 'End of life',
    tone: 'red',
    icon: 'alert-octagon',
    description: 'The release cycle is past its end-of-life date.',
  },
  update_available: {
    label: 'Update available',
    tone: 'yellow',
    icon: 'arrow-big-up-lines',
    description: 'A newer release exists than the version in use.',
  },
  current: {
    label: 'Current',
    tone: 'green',
    icon: 'circle-check',
    description: 'The newest known release is in use.',
  },
  unknown: {
    label: 'Unknown',
    tone: 'gray',
    icon: 'help-circle',
    description: 'No release data matched this product or version.',
  },
  not_applicable: {
    label: 'Not applicable',
    tone: 'gray',
    icon: 'minus',
    description: 'No version to compare, or the product is not tracked.',
  },
}
