import { Alert, Anchor, Badge, Card, Group, SimpleGrid, Stack, Text, Title, Tooltip } from '@mantine/core'
import { IconExternalLink, IconGitCompare } from '@tabler/icons-react'
import type { ReactNode } from 'react'
import type { Entity, ProjectView } from '@/api/client'
import { Field, Mono, TimeAgo } from '@/components/common'
import { NamedIcon } from '@/components/icons'
import { FreshnessBadge, HealthBadge } from '@/components/StatusBadge'
import { LoadingRows, PartialFailure } from '@/components/States'
import { FindingItem } from '@/features/findings/FindingItem'
import { jsonValue } from '@/lib/format'
import { linkHost, linkIconName, safeLinkUrl } from '@/lib/links'
import { escapeToken, flatten, joinPointer, pointerLabel } from '@/lib/provenance'
import { entityHealth } from '@/lib/status'
import { useEntityFindings } from './queries'
import { Why } from './Why'

function Row({
  label,
  pointer,
  value,
  children,
}: {
  label: string
  pointer: string
  value?: unknown
  children: ReactNode
}) {
  return (
    <Field label={label} why={<Why pointer={pointer} value={value} />}>
      {children}
    </Field>
  )
}

function Empty() {
  return (
    <Text span size="sm" c="dimmed">
      —
    </Text>
  )
}

/** Values of a nested object as pointer rows (spec, observed). */
function ValueRows({ value, base, empty }: { value: Record<string, unknown>; base: string; empty: string }) {
  const flat = [...flatten(value, base)]
  if (flat.length === 0)
    return (
      <Text size="sm" c="dimmed">
        {empty}
      </Text>
    )
  return (
    <Stack gap={0}>
      {flat.map(([p, v]) => (
        <Row key={p} label={pointerLabel(p).replace(/^(spec|status › observed) › /, '')} pointer={p} value={v}>
          <Mono>{jsonValue(v)}</Mono>
        </Row>
      ))}
    </Stack>
  )
}

export function OverviewTab({ e, project }: { e: Entity; project?: ProjectView }) {
  const m = e.metadata
  const h = entityHealth(e.status)
  const v = m.version
  const findings = useEntityFindings(m.id)
  const drift = v.declared && v.observed && v.declared !== v.observed
  return (
    <SimpleGrid cols={{ base: 1, lg: 2 }} spacing="sm">
      <Stack gap="sm">
        <Card>
          <Title order={2} fz={14} mb="xs">
            Metadata
          </Title>
          <Row label="Name" pointer="/metadata/name">
            <Mono>{m.name}</Mono>
          </Row>
          <Row label="Display name" pointer="/metadata/displayName" value={m.displayName}>
            {m.displayName ?? <Empty />}
          </Row>
          <Row label="Description" pointer="/metadata/description" value={m.description}>
            {m.description ? (
              <Text size="sm" style={{ whiteSpace: 'pre-wrap' }}>
                {m.description}
              </Text>
            ) : (
              <Empty />
            )}
          </Row>
          <Row label="Project" pointer="/metadata/project">
            {project?.display_name || m.project}
            {project?.synthetic && (
              <Badge size="xs" ml={6} color="yellow" variant="light">
                synthetic
              </Badge>
            )}
          </Row>
          <Row label="Environment" pointer="/metadata/environment" value={m.environment}>
            {m.environment || <Empty />}
          </Row>
          <Row label="Owner" pointer="/metadata/owner" value={m.owner}>
            {m.owner || (
              <Text span size="sm" c="orange">
                No owner
              </Text>
            )}
          </Row>
          <Row label="Lifecycle" pointer="/metadata/lifecycle" value={m.lifecycle}>
            {m.lifecycle || <Empty />}
          </Row>
          {m.tags.length > 0 && (
            <Row label="Tags" pointer="/metadata/tags" value={m.tags}>
              <Group gap={4}>
                {m.tags.map((t) => (
                  <Badge key={t} size="sm" variant="outline" color="gray" styles={{ label: { textTransform: 'none' } }}>
                    {t}
                  </Badge>
                ))}
              </Group>
            </Row>
          )}
          {(m.aliases?.length ?? 0) > 0 && (
            <Field label="Aliases">
              <Mono>{m.aliases?.join(', ')}</Mono>
            </Field>
          )}
          <Field label="ID">
            <Mono>{m.id}</Mono>
          </Field>
          <Field label="Created / updated">
            <TimeAgo value={m.createdAt} /> / <TimeAgo value={m.updatedAt} />
          </Field>
        </Card>

        <Card>
          <Title order={2} fz={14} mb="xs">
            Labels
          </Title>
          {Object.keys(m.labels).length === 0 ? (
            <Text size="sm" c="dimmed">
              No labels.
            </Text>
          ) : (
            Object.entries(m.labels).map(([k, val]) => (
              <Row key={k} label={k} pointer={joinPointer('/metadata/labels', k)} value={val}>
                <Mono>{val}</Mono>
              </Row>
            ))
          )}
          {Object.keys(m.annotations).length > 0 && (
            <>
              <Title order={3} fz={13} mt="sm" mb={4}>
                Annotations
              </Title>
              {Object.entries(m.annotations).map(([k, val]) => (
                <Row key={k} label={k} pointer={`/metadata/annotations/${escapeToken(k)}`} value={val}>
                  <Text size="sm" style={{ wordBreak: 'break-word' }}>
                    {val}
                  </Text>
                </Row>
              ))}
            </>
          )}
        </Card>

        <Card>
          <Group justify="space-between" mb="xs">
            <Title order={2} fz={14}>
              Links
            </Title>
            <Why pointer="/metadata/links" value={m.links} />
          </Group>
          {m.links.length === 0 ? (
            <Text size="sm" c="dimmed">
              No links. Editors can add repository, dashboard, runbook and docs links as annotations.
            </Text>
          ) : (
            <Stack gap={6}>
              {m.links.map((l) => {
                const url = safeLinkUrl(l.url, project?.allow_http_links ?? false)
                return (
                  <Group key={`${l.type}-${l.url}`} gap="xs" wrap="nowrap">
                    <NamedIcon name={linkIconName(l.type)} size={16} />
                    {url ? (
                      <Anchor href={url} target="_blank" rel="noopener noreferrer" size="sm">
                        {l.title || l.type} <IconExternalLink size={12} aria-label="(opens in a new tab)" />
                      </Anchor>
                    ) : (
                      <Tooltip label="This link is not https and is not shown as a clickable link.">
                        <Text size="sm" c="dimmed">
                          {l.title || l.type} (blocked URL)
                        </Text>
                      </Tooltip>
                    )}
                    <Text size="xs" c="dimmed">
                      {l.type} · {url ? linkHost(url) : ''}
                    </Text>
                  </Group>
                )
              })}
            </Stack>
          )}
        </Card>
      </Stack>

      <Stack gap="sm">
        <Card>
          <Title order={2} fz={14} mb="xs">
            Status
          </Title>
          <Row label="Health" pointer="/status/health" value={e.status.health}>
            <Group gap="xs">
              <HealthBadge state={h.state} reason={h.reason} />
              {h.reason && <Text size="sm">{h.reason}</Text>}
            </Group>
          </Row>
          <Field label="Freshness">
            <Group gap="xs">
              <FreshnessBadge state={e.status.freshness.state} />
              <Text size="sm" c="dimmed">
                last seen <TimeAgo value={e.status.freshness.lastSeenAt} />
              </Text>
            </Group>
          </Field>
          <Field label="Declaration">
            {e.status.declared ? 'Declared by a person, Terraform or the API' : 'Observed only (no declaration)'}
          </Field>
          <Field label="Schema">
            {e.status.valid ? (
              <Text size="sm">
                Valid against {e.kind} {e.status.schemaVersion}
              </Text>
            ) : (
              <Stack gap={2}>
                <Text size="sm" c="red">
                  Invalid against {e.kind} {e.status.schemaVersion}
                </Text>
                {e.status.validation.map((f) => (
                  <Text key={f.field} size="xs">
                    <Mono>{f.field}</Mono>: {f.message}
                  </Text>
                ))}
              </Stack>
            )}
          </Field>
          <Field label="Sources">
            {e.status.facets} contributing · {e.status.conflicts} conflicts
          </Field>
        </Card>

        <Card>
          <Group justify="space-between" mb="xs">
            <Title order={2} fz={14}>
              Version
            </Title>
            {drift && (
              <Badge
                color="orange"
                leftSection={<IconGitCompare size={12} />}
                styles={{ label: { textTransform: 'none' } }}
              >
                Declared and observed differ
              </Badge>
            )}
          </Group>
          <Row label="Declared" pointer="/metadata/version/declared" value={v.declared}>
            {v.declared ? <Mono>{v.declared}</Mono> : <Empty />}
          </Row>
          <Row label="Observed" pointer="/metadata/version/observed" value={v.observed}>
            {v.observed ? <Mono>{v.observed}</Mono> : <Empty />}
          </Row>
          <Row label="Available" pointer="/metadata/version/available" value={v.available}>
            {v.available ? (
              <Mono>{v.available}</Mono>
            ) : (
              <Text span size="sm" c="dimmed">
                No version check reported
              </Text>
            )}
          </Row>
          {(v.product || v.channel || v.policy) && (
            <Field label="Product / channel / policy">
              <Mono>{[v.product, v.channel, v.policy].map((x) => x ?? '—').join(' / ')}</Mono>
            </Field>
          )}
        </Card>

        <Card>
          <Title order={2} fz={14} mb="xs">
            Open findings
          </Title>
          {findings.isPending ? (
            <LoadingRows rows={2} />
          ) : findings.isError ? (
            <PartialFailure what="Findings" error={findings.error} onRetry={() => void findings.refetch()} />
          ) : findings.data.length === 0 ? (
            <Text size="sm" c="dimmed">
              No open findings for this entity.
            </Text>
          ) : (
            findings.data.map((f) => <FindingItem key={f.id} f={f} />)
          )}
        </Card>

        <Card>
          <Title order={2} fz={14} mb="xs">
            Spec
          </Title>
          <ValueRows value={e.spec} base="/spec" empty="No spec values." />
        </Card>

        {Object.keys(e.status.observed).length > 0 && (
          <Card>
            <Title order={2} fz={14} mb={2}>
              Latest observations
            </Title>
            <Text size="xs" c="dimmed" mb="xs">
              Last values reported by observation sources (time-bounded summaries, not live metrics).
            </Text>
            <ValueRows value={e.status.observed} base="/status/observed" empty="" />
          </Card>
        )}
        {!e.status.valid && (
          <Alert color="red" title="This entity fails its kind schema">
            Sources are not blocked by invalid data; the entity is flagged and an invalid_schema finding stays open
            until the effective spec validates.
          </Alert>
        )}
      </Stack>
    </SimpleGrid>
  )
}
