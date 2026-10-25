import { Badge, Tooltip, type BadgeProps } from '@mantine/core'
import {
  evidenceMeta,
  freshnessMeta,
  healthMeta,
  layerMeta,
  severityMeta,
  SUPPORT_LEVEL,
  type StatusMeta,
} from '@/lib/status'
import { NamedIcon } from './icons'

/** A status badge: icon + text + colour (never colour alone). */
export function MetaBadge({
  meta,
  prefix,
  tooltip,
  size = 'sm',
  variant = 'light',
  ...rest
}: { meta: StatusMeta; prefix?: string; tooltip?: string } & Omit<BadgeProps, 'children'>) {
  const badge = (
    <Badge
      color={meta.tone}
      size={size}
      variant={variant}
      leftSection={<NamedIcon name={meta.icon} size={12} />}
      styles={{ label: { textTransform: 'none', fontWeight: 600 } }}
      {...rest}
    >
      {prefix ? `${prefix}: ${meta.label}` : meta.label}
    </Badge>
  )
  const tip = tooltip ?? meta.description
  return tip ? (
    <Tooltip label={tip} multiline maw={280}>
      {badge}
    </Tooltip>
  ) : (
    badge
  )
}

export function HealthBadge({
  state,
  reason,
  ...rest
}: { state: string | undefined; reason?: string; prefix?: string } & Omit<BadgeProps, 'children'>) {
  const m = healthMeta(state)
  return <MetaBadge meta={m} tooltip={reason ? `${m.label}: ${reason}` : m.description} {...rest} />
}

export function FreshnessBadge({ state, ...rest }: { state: string | undefined } & Omit<BadgeProps, 'children'>) {
  return <MetaBadge meta={freshnessMeta(state)} {...rest} />
}

export function EvidenceBadge({ cls, ...rest }: { cls: string | undefined } & Omit<BadgeProps, 'children'>) {
  return <MetaBadge meta={evidenceMeta(cls)} {...rest} />
}

export function SeverityBadge({ severity, ...rest }: { severity: string | undefined } & Omit<BadgeProps, 'children'>) {
  return <MetaBadge meta={severityMeta(severity)} {...rest} />
}

export function LayerBadge({ layer, ...rest }: { layer: string | undefined } & Omit<BadgeProps, 'children'>) {
  return <MetaBadge meta={layerMeta(layer)} {...rest} />
}

export function SupportBadge({ level, detail }: { level: string; detail?: string }) {
  const m = SUPPORT_LEVEL[level] ?? { label: level, tone: 'gray' as const, icon: 'help-circle', description: '' }
  return <MetaBadge meta={m} tooltip={detail ? `${m.description} ${detail}` : m.description} variant="outline" />
}
