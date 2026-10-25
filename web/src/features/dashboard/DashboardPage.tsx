import { AnchorLink } from '@/components/links'
import { Badge, Card, Group, SimpleGrid, Stack, Table, Text, Title, Tooltip } from '@mantine/core'
import {
  IconAlertTriangle,
  IconBell,
  IconBinaryTree2,
  IconCertificate,
  IconGitCompare,
  IconLinkOff,
  IconPackages,
  IconPlugConnected,
  IconTag,
} from '@tabler/icons-react'
import { useQuery } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { useEffect, useMemo, type ReactNode } from 'react'
import { isInstallationAdmin, useMe } from '@/api/auth'
import { api, unwrap, type ChangeEvent, type Dashboard, type Finding } from '@/api/client'
import { keys, useProjects } from '@/api/queries'
import { Page } from '@/components/AppShell'
import { PageHeader, TimeAgo, useKindMap, KindIcon } from '@/components/common'
import { ErrorState, LoadingBlock, PartialFailure, StaleNotice } from '@/components/States'
import { FreshnessBadge, MetaBadge, SeverityBadge } from '@/components/StatusBadge'
import { setSyntheticData, useProjectFilter, useTitle } from '@/lib/appStore'
import { count, humanize, jsonValue, plural } from '@/lib/format'
import { FRESHNESS, freshnessMeta, healthMeta, HEALTH, SEVERITY } from '@/lib/status'
import { topN } from '@/theme/chart'
import { FindingItem } from '@/features/findings/FindingItem'
import { ChartCard, CountBars, CountTable, StatusDonut, TileIcon } from './charts'

function StatTile({
  label,
  value,
  icon,
  to,
  search,
  hint,
  testId,
}: {
  label: string
  value: ReactNode
  icon: ReactNode
  to: string
  search?: Record<string, unknown>
  hint?: ReactNode
  testId?: string
}) {
  return (
    <Card padding="sm" data-testid={testId}>
      <Group justify="space-between" wrap="nowrap" align="flex-start">
        <Stack gap={0}>
          <Text size="xs" c="dimmed" fw={500}>
            {label}
          </Text>
          <AnchorLink to={to} search={search} underline="hover" c="var(--mantine-color-text)">
            <Text fz={24} fw={650} lh={1.2} className="tile-value">
              {value}
            </Text>
          </AnchorLink>
          {hint && (
            <Text size="xs" c="dimmed" mt={2}>
              {hint}
            </Text>
          )}
        </Stack>
        <TileIcon>{icon}</TileIcon>
      </Group>
    </Card>
  )
}

function Section({
  title,
  icon,
  count: n,
  link,
  children,
  testId,
}: {
  title: string
  icon: ReactNode
  count?: number
  link?: ReactNode
  children: ReactNode
  testId?: string
}) {
  return (
    <Card data-testid={testId}>
      <Group justify="space-between" mb={4}>
        <Group gap={6}>
          {icon}
          <Title order={2} fz={14}>
            {title}
          </Title>
          {n !== undefined && (
            <Badge size="xs" variant="outline" color="gray">
              {n}
            </Badge>
          )}
        </Group>
        {link}
      </Group>
      {children}
    </Card>
  )
}

function FindingSection({ items, empty, limit = 6 }: { items: Finding[]; empty: string; limit?: number }) {
  if (items.length === 0) {
    return (
      <Text size="sm" c="dimmed" py="sm">
        {empty}
      </Text>
    )
  }
  return (
    <Stack gap={0}>
      {items.slice(0, limit).map((f) => (
        <FindingItem key={f.id} f={f} />
      ))}
    </Stack>
  )
}

const changeVerb: Record<string, string> = {
  created: 'created',
  updated: 'updated',
  removed: 'removed',
  deleted: 'deleted',
  observed: 'observed',
  renamed: 'renamed',
  merged: 'merged',
  stale: 'went stale',
}

function RecentChanges({ changes }: { changes: ChangeEvent[] }) {
  if (changes.length === 0)
    return (
      <Text size="sm" c="dimmed">
        No changes yet.
      </Text>
    )
  return (
    <Table fz="sm" verticalSpacing={4} aria-label="Recent changes">
      <Table.Tbody>
        {changes.slice(0, 12).map((c) => {
          const name = typeof c.summary.name === 'string' ? c.summary.name : c.object_id
          const kind = typeof c.summary.kind === 'string' ? c.summary.kind : ''
          return (
            <Table.Tr key={c.seq}>
              <Table.Td w={22}>{kind && <KindIcon kind={kind} size={14} />}</Table.Td>
              <Table.Td>
                {c.object_type === 'entity' && c.action !== 'removed' ? (
                  <AnchorLink to="/entities/$id" params={{ id: c.object_id }} size="sm">
                    {name}
                  </AnchorLink>
                ) : c.object_type === 'source' ? (
                  <AnchorLink to="/sources/$id" params={{ id: c.object_id }} size="sm">
                    {name}
                  </AnchorLink>
                ) : (
                  <Text span size="sm">
                    {name}
                  </Text>
                )}{' '}
                <Text span size="sm" c="dimmed">
                  {humanize(c.object_type)} {changeVerb[c.action] ?? c.action}
                  {c.source ? ` by ${c.source}` : ''}
                </Text>
              </Table.Td>
              <Table.Td ta="right" c="dimmed" style={{ whiteSpace: 'nowrap' }}>
                <TimeAgo value={c.at} />
              </Table.Td>
            </Table.Tr>
          )
        })}
      </Table.Tbody>
    </Table>
  )
}

export function DashboardPage() {
  useTitle('Dashboard')
  const projectFilter = useProjectFilter()
  const me = useMe().data
  const projects = useProjects()
  const kindMap = useKindMap()
  const navigate = useNavigate()
  const q = useQuery({
    queryKey: keys.dashboard(projectFilter),
    queryFn: async () =>
      unwrap(
        await api.GET('/dashboard', { params: { query: projectFilter.length ? { project: projectFilter } : {} } }),
      ),
    refetchInterval: 60_000,
  })
  const d = q.data
  useEffect(() => {
    if (d) setSyntheticData(d.synthetic_data)
  }, [d])

  const projectName = useMemo(() => {
    const m = new Map((projects.data ?? []).map((p) => [p.id, p.display_name || p.name]))
    return (id: string) => m.get(id) ?? id
  }, [projects.data])

  if (q.isPending) {
    return (
      <Page>
        <PageHeader title="Dashboard" description="Loading the catalog summary…" />
        <SimpleGrid cols={{ base: 2, md: 3, xl: 6 }} mb="md">
          {Array.from({ length: 6 }, (_, i) => (
            <LoadingBlock key={i} height={78} />
          ))}
        </SimpleGrid>
        <SimpleGrid cols={{ base: 1, md: 3 }}>
          <LoadingBlock height={260} />
          <LoadingBlock height={260} />
          <LoadingBlock height={260} />
        </SimpleGrid>
      </Page>
    )
  }
  if (!d) {
    return (
      <Page>
        <PageHeader title="Dashboard" />
        <ErrorState error={q.error} what="the dashboard" onRetry={() => void q.refetch()} />
      </Page>
    )
  }
  return (
    <DashboardBody
      d={d}
      stale={q.isError}
      updatedAt={q.dataUpdatedAt}
      refetch={() => void q.refetch()}
      projectName={projectName}
      kindTitle={(k) => kindMap.get(k)?.title ?? k}
      admin={isInstallationAdmin(me)}
      navigate={(s) => void navigate({ to: '/inventory', search: s })}
      scoped={projectFilter.length}
    />
  )
}

function DashboardBody({
  d,
  stale,
  updatedAt,
  refetch,
  projectName,
  kindTitle,
  admin,
  navigate,
  scoped,
}: {
  d: Dashboard
  stale: boolean
  updatedAt: number
  refetch: () => void
  projectName: (id: string) => string
  kindTitle: (k: string) => string
  admin: boolean
  navigate: (s: Record<string, unknown>) => void
  scoped: number
}) {
  const c = d.counts
  const findingTotal = Object.values(d.finding_counts).reduce((s, n) => s + n, 0)
  const firing = d.alerts.filter((a) => a.state === 'firing')
  const staleSources = d.sources.filter((s) => s.freshness === 'stale' || s.freshness === 'aging' || s.last_error)
  const byKind = topN(c.by_kind, 12, kindTitle)
  const byEnv = topN(c.by_environment, 8, (k) => k || '(none)')
  const byOwner = topN(c.by_owner, 8, (k) => k || '(no owner)')
  const extraEntries = Object.entries(d.extra)

  return (
    <Page>
      <PageHeader
        title="Dashboard"
        description={
          <>
            {scoped ? `Scoped to ${plural(scoped, 'project')} by the project filter. ` : 'Every project you can read. '}
            Updated <TimeAgo value={d.generated_at} />.
          </>
        }
      />
      {stale && <StaleNotice updatedAt={updatedAt} onRetry={refetch} />}
      <SimpleGrid cols={{ base: 2, sm: 3, xl: 6 }} mb="md" spacing="sm">
        <StatTile
          label="Entities"
          value={count(c.entities)}
          icon={<IconPackages size={18} />}
          to="/inventory"
          testId="tile-entities"
          hint={`${count(c.undeclared)} observed only · ${count(c.custom)} custom kind`}
        />
        <StatTile
          label="Relationships"
          value={count(c.relations)}
          icon={<IconBinaryTree2 size={18} />}
          to="/relations"
          hint="declared and observed"
        />
        <StatTile
          label="Open findings"
          value={count(findingTotal)}
          icon={<IconAlertTriangle size={18} />}
          to="/findings"
          testId="tile-findings"
          hint={
            <Group gap={6} component="span">
              {Object.keys(SEVERITY).map((s) =>
                d.finding_counts[s] ? (
                  <span key={s}>
                    {count(d.finding_counts[s])} {SEVERITY[s]?.label.toLowerCase()}
                  </span>
                ) : null,
              )}
              {findingTotal === 0 && 'none open'}
            </Group>
          }
        />
        <StatTile
          label="Firing alerts"
          value={count(firing.length)}
          icon={<IconBell size={18} />}
          to="/alerts"
          hint={`${count(d.alerts.length - firing.length)} pending`}
        />
        <StatTile
          label="Sources needing attention"
          value={`${staleSources.length} / ${d.sources.length}`}
          icon={<IconPlugConnected size={18} />}
          to="/sources"
          hint="stale, aging or failing"
        />
        <StatTile
          label="Invalid entities"
          value={count(c.invalid)}
          icon={<IconTag size={18} />}
          to="/inventory"
          search={{ valid: false }}
          hint="spec fails its kind schema"
        />
      </SimpleGrid>

      <SimpleGrid cols={{ base: 1, md: 2, xl: 3 }} spacing="sm" mb="sm">
        <ChartCard
          title="Inventory by kind"
          description="Click a bar to open the filtered inventory."
          empty={byKind.length === 0}
          chart={<CountBars data={byKind} label="Entities by kind" onSelect={(x) => navigate({ kind: x.key })} />}
          table={
            <CountTable
              data={byKind}
              label="Entities by kind"
              linkFor={(x) => (x.key === '__other__' ? null : { kind: x.key })}
            />
          }
        />
        <ChartCard
          title="By environment"
          empty={byEnv.length === 0}
          chart={
            <CountBars
              data={byEnv}
              label="Entities by environment"
              onSelect={(x) => x.key && navigate({ environment: x.key })}
            />
          }
          table={
            <CountTable
              data={byEnv}
              label="Entities by environment"
              linkFor={(x) => (x.key && x.key !== '__other__' ? { environment: x.key } : null)}
            />
          }
        />
        <ChartCard
          title="By owner"
          empty={byOwner.length === 0}
          chart={
            <CountBars data={byOwner} label="Entities by owner" onSelect={(x) => x.key && navigate({ owner: x.key })} />
          }
          table={
            <CountTable
              data={byOwner}
              label="Entities by owner"
              linkFor={(x) => (x.key && x.key !== '__other__' ? { owner: x.key } : null)}
            />
          }
        />
      </SimpleGrid>
      <SimpleGrid cols={{ base: 1, md: 2 }} spacing="sm" mb="sm">
        <Card>
          <Title order={2} fz={14} mb="xs">
            Health
          </Title>
          <StatusDonut
            counts={c.by_health}
            metaOf={healthMeta}
            order={Object.keys(HEALTH)}
            label="Entities by health"
            linkFor={(k) => ({ health: k })}
          />
        </Card>
        <Card>
          <Title order={2} fz={14} mb="xs">
            Freshness
          </Title>
          <StatusDonut
            counts={c.by_freshness}
            metaOf={freshnessMeta}
            order={Object.keys(FRESHNESS)}
            label="Entities by freshness"
            linkFor={(k) => ({ freshness: k })}
          />
        </Card>
      </SimpleGrid>

      <SimpleGrid cols={{ base: 1, lg: 2 }} spacing="sm" mb="sm">
        <Section
          title="Source freshness & sync health"
          icon={<IconPlugConnected size={16} />}
          count={d.sources.length}
          link={
            <AnchorLink to="/sources" size="xs">
              All sources
            </AnchorLink>
          }
          testId="dash-sources"
        >
          {d.sources.length === 0 ? (
            <Text size="sm" c="dimmed" py="sm">
              No sources yet. <AnchorLink to="/sources">Add a source</AnchorLink> to import or discover entities.
            </Text>
          ) : (
            <Table fz="sm" verticalSpacing={4} aria-label="Source freshness">
              <Table.Thead>
                <Table.Tr>
                  <Table.Th>Source</Table.Th>
                  <Table.Th>Freshness</Table.Th>
                  <Table.Th>Last success</Table.Th>
                  <Table.Th>Last error</Table.Th>
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {d.sources.map((s) => (
                  <Table.Tr key={s.id}>
                    <Table.Td>
                      <AnchorLink to="/sources/$id" params={{ id: s.id }} size="sm">
                        {s.display_name || s.name}
                      </AnchorLink>
                      <Text size="xs" c="dimmed">
                        {s.provider} · {projectName(s.project_id)}
                        {!s.enabled && ' · disabled'}
                      </Text>
                    </Table.Td>
                    <Table.Td>
                      <FreshnessBadge state={s.freshness} size="xs" />
                    </Table.Td>
                    <Table.Td>
                      <TimeAgo value={s.last_success_at} />
                    </Table.Td>
                    <Table.Td maw={220}>
                      {s.last_error ? (
                        <Tooltip label={s.last_error} multiline maw={360}>
                          <Text size="xs" c="red" truncate>
                            {s.last_error}
                          </Text>
                        </Tooltip>
                      ) : (
                        <Text size="xs" c="dimmed">
                          —
                        </Text>
                      )}
                    </Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          )}
        </Section>
        <Section title="Recent changes" icon={<IconGitCompare size={16} />} count={d.recent_changes.length}>
          <RecentChanges changes={d.recent_changes} />
        </Section>
      </SimpleGrid>

      <SimpleGrid cols={{ base: 1, md: 2, xl: 3 }} spacing="sm" mb="sm">
        <Section
          title="Expiring certificates & domains"
          icon={<IconCertificate size={16} />}
          count={d.expiring.length}
          testId="dash-expiring"
          link={
            <AnchorLink
              to="/findings"
              search={{ type: 'certificate_expiring,certificate_expired,domain_expiring' }}
              size="xs"
            >
              All
            </AnchorLink>
          }
        >
          <FindingSection items={d.expiring} empty="Nothing expires in the warning window." />
        </Section>
        <Section
          title="Broken or unresolved relationships"
          icon={<IconLinkOff size={16} />}
          count={d.unresolved.length}
          testId="dash-unresolved"
          link={
            <AnchorLink to="/relations" search={{ unresolved: true }} size="xs">
              Unresolved references
            </AnchorLink>
          }
        >
          <FindingSection items={d.unresolved} empty="Every relationship resolves to an entity." />
        </Section>
        <Section
          title="Outdated versions"
          icon={<IconTag size={16} />}
          count={d.versions.length}
          link={
            <AnchorLink to="/findings" search={{ type: 'version_update_available,version_end_of_life' }} size="xs">
              All
            </AnchorLink>
          }
        >
          <FindingSection items={d.versions} empty="No version checks report updates or end-of-life releases." />
        </Section>
        <Section
          title="Drift & conflicts"
          icon={<IconGitCompare size={16} />}
          count={d.drift.length}
          link={
            <AnchorLink to="/findings" search={{ type: 'declared_observed_drift,source_conflict' }} size="xs">
              All
            </AnchorLink>
          }
        >
          <FindingSection items={d.drift} empty="Declared and observed values agree." />
        </Section>
        <Section
          title="Alerts"
          icon={<IconBell size={16} />}
          count={d.alerts.length}
          link={
            <AnchorLink to="/alerts" size="xs">
              Alerts
            </AnchorLink>
          }
        >
          {d.alerts.length === 0 ? (
            <Text size="sm" c="dimmed" py="sm">
              No alerts are firing or pending.
            </Text>
          ) : (
            <Stack gap={4}>
              {d.alerts.slice(0, 6).map((a) => (
                <Group key={a.id} gap="xs" wrap="nowrap" justify="space-between">
                  <Group gap="xs" wrap="nowrap" style={{ minWidth: 0 }}>
                    <SeverityBadge severity={a.severity} size="xs" />
                    <Text size="sm" truncate>
                      {a.title}
                    </Text>
                  </Group>
                  <Badge size="xs" variant="outline" color={a.state === 'firing' ? 'red' : 'gray'}>
                    {a.state}
                  </Badge>
                </Group>
              ))}
            </Stack>
          )}
        </Section>
        <Section title="Findings by type" icon={<IconAlertTriangle size={16} />} count={findingTotal}>
          {Object.keys(d.findings_by_type).length === 0 ? (
            <Text size="sm" c="dimmed" py="sm">
              No open findings.
            </Text>
          ) : (
            <Table fz="sm" verticalSpacing={3} aria-label="Open findings by type">
              <Table.Tbody>
                {Object.entries(d.findings_by_type)
                  .sort((a, b) => b[1] - a[1])
                  .map(([t, n]) => (
                    <Table.Tr key={t}>
                      <Table.Td>
                        <AnchorLink to="/findings" search={{ type: t }} size="sm">
                          {humanize(t)}
                        </AnchorLink>
                      </Table.Td>
                      <Table.Td ta="right" className="mono">
                        {count(n)}
                      </Table.Td>
                    </Table.Tr>
                  ))}
              </Table.Tbody>
            </Table>
          )}
        </Section>
      </SimpleGrid>

      {(admin && d.outbox) || extraEntries.length > 0 ? (
        <SimpleGrid cols={{ base: 1, md: 2 }} spacing="sm">
          {admin && d.outbox && (
            <Section title="Event outbox" icon={<IconPlugConnected size={16} />}>
              <Group gap="lg">
                <MetaBadge
                  meta={{
                    label: `${count(d.outbox.pending)} pending`,
                    tone: d.outbox.pending > 100 ? 'yellow' : 'gray',
                    icon: 'clock-pause',
                    description: 'Events waiting to be published to Pulse.',
                  }}
                />
                <MetaBadge
                  meta={{
                    label: `${count(d.outbox.dead)} dead-lettered`,
                    tone: d.outbox.dead > 0 ? 'red' : 'gray',
                    icon: 'circle-x',
                    description: 'Events that exhausted their retries.',
                  }}
                />
                <Text size="sm">{count(d.outbox.published_24h)} published in 24 h</Text>
                {d.outbox.oldest_pending && (
                  <Text size="sm" c="dimmed">
                    oldest pending <TimeAgo value={d.outbox.oldest_pending} />
                  </Text>
                )}
              </Group>
              {d.outbox.last_error && (
                <Text size="xs" c="red" mt="xs">
                  Last error: {d.outbox.last_error}
                </Text>
              )}
            </Section>
          )}
          {extraEntries.length > 0 && (
            <Section title="Operational summaries" icon={<IconPackages size={16} />}>
              {extraEntries.map(([k, v]) =>
                v && typeof v === 'object' ? (
                  <Stack key={k} gap={2} mb="xs">
                    <Text size="sm" fw={600}>
                      {humanize(k)}
                    </Text>
                    {Object.entries(v as Record<string, unknown>).map(([kk, vv]) => (
                      <Text key={kk} size="xs" c="dimmed">
                        {humanize(kk)}: <span className="mono">{jsonValue(vv)}</span>
                      </Text>
                    ))}
                  </Stack>
                ) : (
                  <PartialFailure key={k} what={humanize(k)} error={String(v)} />
                ),
              )}
            </Section>
          )}
        </SimpleGrid>
      ) : null}
    </Page>
  )
}
