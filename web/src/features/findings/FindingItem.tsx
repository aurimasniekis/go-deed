import { AnchorLink } from '@/components/links'
import { Group, Stack, Text, Tooltip } from '@mantine/core'
import type { Finding } from '@/api/client'
import { TimeAgo } from '@/components/common'
import { SeverityBadge } from '@/components/StatusBadge'
import { humanize, jsonValue, truncate } from '@/lib/format'

export const FINDING_CLASSES: Record<string, string> = {
  confirmed: 'Confirmed by evidence',
  warning: 'Likely problem; verify',
  stale: 'Based on stale evidence',
  unknown: 'Cannot be determined from the evidence',
  not_applicable: 'Does not apply',
}

/** Evidence key/values rendered as text (never HTML). */
export function EvidenceList({ evidence, max = 4 }: { evidence: Record<string, unknown>; max?: number }) {
  const entries = Object.entries(evidence).filter(([, v]) => v !== null && v !== undefined && v !== '')
  if (entries.length === 0) return null
  return (
    <Group gap={10} wrap="wrap">
      {entries.slice(0, max).map(([k, v]) => (
        <Text key={k} size="xs" c="dimmed">
          {humanize(k)}: <span className="mono">{truncate(jsonValue(v), 60)}</span>
        </Text>
      ))}
      {entries.length > max && (
        <Tooltip
          label={entries
            .slice(max)
            .map(([k, v]) => `${k}: ${jsonValue(v)}`)
            .join('\n')}
          multiline
          maw={360}
        >
          <Text size="xs" c="dimmed">
            +{entries.length - max} more
          </Text>
        </Tooltip>
      )}
    </Group>
  )
}

/** One finding in a compact list: severity (icon + text), title, message, evidence and links. */
export function FindingItem({ f, showEvidence = true }: { f: Finding; showEvidence?: boolean }) {
  return (
    <Stack gap={2} py={6} style={{ borderBottom: '1px solid var(--deed-border)' }} data-testid="finding-item">
      <Group gap="xs" wrap="nowrap" justify="space-between" align="flex-start">
        <Group gap="xs" wrap="nowrap" style={{ minWidth: 0 }}>
          <SeverityBadge severity={f.severity} size="xs" />
          {f.entity_id ? (
            <AnchorLink to="/entities/$id" params={{ id: f.entity_id }} size="sm" fw={500} truncate="end">
              {f.title}
            </AnchorLink>
          ) : (
            <AnchorLink to="/findings" search={{ type: f.type }} size="sm" fw={500} truncate="end">
              {f.title}
            </AnchorLink>
          )}
        </Group>
        <Text size="xs" c="dimmed" style={{ whiteSpace: 'nowrap' }}>
          <TimeAgo value={f.last_seen_at} />
        </Text>
      </Group>
      {f.message && (
        <Text size="xs" c="dimmed" lineClamp={2}>
          {f.message}
        </Text>
      )}
      {showEvidence && <EvidenceList evidence={f.evidence} />}
    </Stack>
  )
}
