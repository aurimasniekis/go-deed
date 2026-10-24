import { Accordion, Badge, Card, Group, SimpleGrid, Stack, Table, Text, Title, Tooltip } from '@mantine/core'
import { IconPlus } from '@tabler/icons-react'
import { anyProjectAtLeast } from '@/api/auth'
import type { Descriptor } from '@/api/client'
import { useProjects, useProviders, useSources } from '@/api/queries'
import { Page } from '@/components/AppShell'
import { PageHeader, TimeAgo } from '@/components/common'
import { AnchorLink, ButtonLink } from '@/components/links'
import { EmptyState, ErrorState, LoadingRows, PartialFailure } from '@/components/States'
import { FreshnessBadge, LayerBadge, SupportBadge } from '@/components/StatusBadge'
import { useProjectFilter, useTitle } from '@/lib/appStore'
import { duration } from '@/lib/format'

export function CapabilityBadges({ d }: { d: Descriptor }) {
  const caps = Object.entries(d.capabilities).filter(([, v]) => v)
  return (
    <Group gap={4}>
      {caps.map(([k]) => (
        <Badge key={k} size="xs" variant="outline" color="gray">
          {k}
        </Badge>
      ))}
    </Group>
  )
}

export function ProviderDetails({ d }: { d: Descriptor }) {
  return (
    <Stack gap="xs">
      <Text size="sm">{d.description}</Text>
      <Group gap="xs">
        <Text size="xs" c="dimmed">
          Capabilities:
        </Text>
        <CapabilityBadges d={d} />
        <Text size="xs" c="dimmed">
          Default layer:
        </Text>
        <LayerBadge layer={d.default_layer} size="xs" />
        {d.default_schedule_seconds > 0 && (
          <Text size="xs" c="dimmed">
            default schedule every {duration(d.default_schedule_seconds)}
          </Text>
        )}
      </Group>
      {d.kinds.length > 0 && (
        <Table fz="xs" verticalSpacing={2} withRowBorders={false} aria-label={`Kinds supported by ${d.title}`}>
          <Table.Tbody>
            {d.kinds.map((k) => (
              <Table.Tr key={k.kind}>
                <Table.Td className="mono">{k.kind}</Table.Td>
                <Table.Td>
                  <SupportBadge level={k.level} />
                </Table.Td>
                <Table.Td c="dimmed">{k.note}</Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      )}
      {d.relationship_types.length > 0 && (
        <Text size="xs" c="dimmed">
          Relationships: {d.relationship_types.join(', ')}
        </Text>
      )}
      {d.secret_fields.length > 0 && (
        <Text size="xs" c="dimmed">
          Credentials (write-only): {d.secret_fields.join(', ')}
        </Text>
      )}
      <Text size="xs">
        <b>How it was validated:</b> {d.verified}
      </Text>
      {d.docs && (
        <Text size="xs" c="dimmed">
          Documentation: <span className="mono">{d.docs}</span>
        </Text>
      )}
    </Stack>
  )
}

export function SourcesPage() {
  useTitle('Sources')
  const projectFilter = useProjectFilter()
  const projects = useProjects()
  const sources = useSources()
  const providers = useProviders()
  const projectName = (id: string) => projects.data?.find((p) => p.id === id)?.display_name ?? id
  const rows = (sources.data ?? []).filter((s) => projectFilter.length === 0 || projectFilter.includes(s.project_id))
  const canCreate = anyProjectAtLeast(projects.data, 'admin')
  return (
    <Page>
      <PageHeader
        title="Sources & integrations"
        description="Where facts come from: declarations, imports, runtime discovery and observations. Each source has a provider, a schedule, credentials and a reconciliation policy."
        actions={
          canCreate && (
            <ButtonLink to="/sources/new" leftSection={<IconPlus size={14} />} data-testid="new-source">
              New source
            </ButtonLink>
          )
        }
      />
      {sources.isPending ? (
        <LoadingRows />
      ) : sources.isError ? (
        <ErrorState error={sources.error} what="sources" onRetry={() => void sources.refetch()} />
      ) : rows.length === 0 ? (
        <EmptyState
          title="No sources"
          action={canCreate ? <ButtonLink to="/sources/new">Add a source</ButtonLink> : undefined}
        >
          Sources import, discover and observe entities. Declarations from Terraform and the API appear here
          automatically.
        </EmptyState>
      ) : (
        <Card p={0} mb="md">
          <Table.ScrollContainer minWidth={960}>
            <Table fz="sm" verticalSpacing={6} highlightOnHover aria-label="Sources" data-testid="sources-list">
              <Table.Thead>
                <Table.Tr>
                  <Table.Th>Source</Table.Th>
                  <Table.Th>Provider</Table.Th>
                  <Table.Th>Layer</Table.Th>
                  <Table.Th>Freshness</Table.Th>
                  <Table.Th>Last success</Table.Th>
                  <Table.Th>Schedule</Table.Th>
                  <Table.Th>Last error</Table.Th>
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {rows.map((s) => (
                  <Table.Tr key={s.id}>
                    <Table.Td>
                      <AnchorLink to="/sources/$id" params={{ id: s.id }} size="sm" fw={500}>
                        {s.display_name || s.name}
                      </AnchorLink>
                      <Text size="xs" c="dimmed">
                        {s.name} · {projectName(s.project_id)}
                        {s.system ? ' · system' : ''}
                        {!s.enabled ? ' · disabled' : ''}
                        {s.running ? ' · running' : ''}
                      </Text>
                    </Table.Td>
                    <Table.Td>{s.provider_title || s.provider}</Table.Td>
                    <Table.Td>
                      <LayerBadge layer={s.layer} size="xs" />
                    </Table.Td>
                    <Table.Td>
                      <FreshnessBadge state={s.freshness} size="xs" />
                    </Table.Td>
                    <Table.Td>
                      <TimeAgo value={s.last_success_at} />
                    </Table.Td>
                    <Table.Td>
                      {s.pull && s.schedule_seconds
                        ? `every ${duration(s.schedule_seconds)}`
                        : s.push
                          ? 'push'
                          : 'manual'}
                    </Table.Td>
                    <Table.Td maw={260}>
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
          </Table.ScrollContainer>
        </Card>
      )}
      <Title order={2} fz={16} mb="xs">
        Available providers
      </Title>
      {providers.isError ? (
        <PartialFailure what="Providers" error={providers.error} onRetry={() => void providers.refetch()} />
      ) : (
        <SimpleGrid cols={{ base: 1 }}>
          <Accordion variant="separated" multiple>
            {(providers.data ?? []).map((d) => (
              <Accordion.Item key={d.type} value={d.type}>
                <Accordion.Control>
                  <Group justify="space-between" wrap="nowrap" pr="sm">
                    <div>
                      <Text fw={600} size="sm">
                        {d.title}
                      </Text>
                      <Text size="xs" c="dimmed" lineClamp={1}>
                        {d.description}
                      </Text>
                    </div>
                    <CapabilityBadges d={d} />
                  </Group>
                </Accordion.Control>
                <Accordion.Panel>
                  <ProviderDetails d={d} />
                  {canCreate && d.type !== 'manual' && (
                    <ButtonLink to="/sources/new" search={{ provider: d.type }} size="xs" mt="sm" variant="default">
                      Create a {d.title} source
                    </ButtonLink>
                  )}
                </Accordion.Panel>
              </Accordion.Item>
            ))}
          </Accordion>
        </SimpleGrid>
      )}
    </Page>
  )
}
