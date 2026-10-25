import {
  Badge,
  Button,
  Card,
  Group,
  MultiSelect,
  SegmentedControl,
  Select,
  Stack,
  Table,
  Text,
  Tooltip,
} from '@mantine/core'
import { notifications } from '@mantine/notifications'
import { IconCheck, IconRefresh } from '@tabler/icons-react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { anyProjectAtLeast, isInstallationAdmin, useMe } from '@/api/auth'
import { api, check, errorMessage, unwrap, type Finding } from '@/api/client'
import { keys, useProjects } from '@/api/queries'
import { Page } from '@/components/AppShell'
import { PageHeader, TimeAgo } from '@/components/common'
import { AnchorLink } from '@/components/links'
import { EmptyState, ErrorState, LoadingRows } from '@/components/States'
import { SeverityBadge } from '@/components/StatusBadge'
import { useProjectFilter, useTitle } from '@/lib/appStore'
import { humanize } from '@/lib/format'
import { SEVERITY } from '@/lib/status'
import { compact, decodeList, encodeList, type FindingsSearch } from '@/lib/urlState'
import { EvidenceList, FINDING_CLASSES } from './FindingItem'

export const FINDING_TYPES = [
  'certificate_expiring',
  'certificate_expired',
  'domain_expiring',
  'missing_relationship_target',
  'stale_observation',
  'stale_source',
  'ingestion_failed',
  'integration_unreachable',
  'invalid_schema',
  'missing_owner',
  'declared_observed_drift',
  'source_conflict',
  'duplicate_candidate',
  'expected_connection_unseen',
  'version_update_available',
  'version_end_of_life',
  'backup_stale',
  'backup_restored',
  'events_dead_lettered',
  'breakglass_used',
  'absent_from_source',
]

export function FindingsPage({
  search,
  setSearch,
}: {
  search: FindingsSearch
  setSearch: (s: FindingsSearch) => void
}) {
  useTitle('Findings')
  const qc = useQueryClient()
  const projects = useProjects()
  const globalProjects = useProjectFilter()
  const project = search.project ?? (globalProjects.length === 1 ? globalProjects[0] : undefined)
  const query = compact({
    severity: decodeList(search.severity) as ('critical' | 'info' | 'warning')[],
    class: decodeList(search.class) as ('confirmed' | 'not_applicable' | 'stale' | 'unknown' | 'warning')[],
    type: decodeList(search.type),
    project,
    entity: search.entity,
    state: search.state ?? ('open' as const),
    limit: 500,
  })
  const findings = useQuery({
    queryKey: keys.findings(query),
    queryFn: async () => unwrap(await api.GET('/findings', { params: { query } })),
  })
  const ack = useMutation({
    mutationFn: async (f: Finding) =>
      unwrap(await api.POST('/findings/{id}/acknowledge', { params: { path: { id: f.id } } })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ['findings'] }),
    onError: (e) => notifications.show({ color: 'red', title: 'Could not acknowledge', message: errorMessage(e) }),
  })
  const admin = isInstallationAdmin(useMe().data)
  const evaluate = useMutation({
    mutationFn: async () => {
      check(await api.POST('/findings/evaluate'))
    },
    onSuccess: () => {
      notifications.show({ color: 'green', message: 'Findings and alerts re-evaluated' })
      void qc.invalidateQueries({ queryKey: ['findings'] })
      void qc.invalidateQueries({ queryKey: ['alerts'] })
    },
    onError: (e) =>
      notifications.show({ color: 'red', title: 'Could not evaluate findings', message: errorMessage(e) }),
  })
  const patch = (p: Partial<FindingsSearch>) => setSearch(compact({ ...search, ...p }))
  const canAck = anyProjectAtLeast(projects.data, 'editor')
  const rows = (findings.data ?? []).filter(
    (f) => globalProjects.length === 0 || !f.project_id || globalProjects.includes(f.project_id),
  )
  const projectName = (id?: string) =>
    id ? (projects.data?.find((p) => p.id === id)?.display_name ?? id) : 'installation'

  return (
    <Page wide>
      <PageHeader
        title="Findings"
        description="Problems Deed derives from evidence: expiring certificates, drift, conflicts, stale sources, unresolved relationships and more. Findings are evaluated every two minutes and close by themselves when the evidence clears."
        actions={
          admin && (
            <Button
              variant="default"
              leftSection={<IconRefresh size={14} />}
              loading={evaluate.isPending}
              onClick={() => evaluate.mutate()}
              data-testid="evaluate-findings"
            >
              Evaluate now
            </Button>
          )
        }
      />
      <Group gap="xs" mb="sm" align="flex-end" wrap="wrap">
        <SegmentedControl
          aria-label="State"
          value={search.state ?? 'open'}
          onChange={(v) => patch({ state: v === 'open' ? undefined : (v as FindingsSearch['state']) })}
          data={[
            { value: 'open', label: 'Open' },
            { value: 'closed', label: 'Closed' },
            { value: 'all', label: 'All' },
          ]}
        />
        <MultiSelect
          label="Severity"
          data={Object.entries(SEVERITY).map(([value, m]) => ({ value, label: m.label }))}
          value={decodeList(search.severity)}
          onChange={(v) => patch({ severity: encodeList(v) })}
          clearable
          w={220}
          data-testid="findings-severity"
        />
        <MultiSelect
          label="Class"
          data={Object.entries(FINDING_CLASSES).map(([value, label]) => ({
            value,
            label: humanize(value) + ' — ' + label,
          }))}
          value={decodeList(search.class)}
          onChange={(v) => patch({ class: encodeList(v) })}
          clearable
          w={220}
        />
        <MultiSelect
          label="Type"
          data={FINDING_TYPES.map((t) => ({ value: t, label: humanize(t) }))}
          value={decodeList(search.type)}
          onChange={(v) => patch({ type: encodeList(v) })}
          searchable
          clearable
          w={300}
        />
        <Select
          label="Project"
          data={(projects.data ?? []).map((p) => ({ value: p.id, label: p.display_name || p.name }))}
          value={search.project ?? null}
          onChange={(v) => patch({ project: v ?? undefined })}
          clearable
          w={200}
        />
      </Group>
      {findings.isPending ? (
        <LoadingRows rows={8} />
      ) : findings.isError ? (
        <ErrorState error={findings.error} what="findings" onRetry={() => void findings.refetch()} />
      ) : rows.length === 0 ? (
        <EmptyState title={search.state === 'closed' ? 'No closed findings' : 'No open findings'}>
          {query.type.length || query.severity.length
            ? 'Nothing matches these filters.'
            : 'Findings are evaluated every two minutes from the catalog’s evidence.'}
        </EmptyState>
      ) : (
        <Card p={0}>
          <Table.ScrollContainer minWidth={980}>
            <Table fz="sm" verticalSpacing={6} aria-label="Findings" data-testid="findings-table">
              <Table.Thead>
                <Table.Tr>
                  <Table.Th>Severity</Table.Th>
                  <Table.Th>Finding</Table.Th>
                  <Table.Th>Type / class</Table.Th>
                  <Table.Th>Project</Table.Th>
                  <Table.Th>Seen</Table.Th>
                  <Table.Th>State</Table.Th>
                  <Table.Th />
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {rows.map((f) => (
                  <Table.Tr key={f.id} data-testid="finding-row">
                    <Table.Td>
                      <SeverityBadge severity={f.severity} size="xs" />
                    </Table.Td>
                    <Table.Td maw={460}>
                      {f.entity_id ? (
                        <AnchorLink to="/entities/$id" params={{ id: f.entity_id }} size="sm" fw={500}>
                          {f.title}
                        </AnchorLink>
                      ) : (
                        <Text size="sm" fw={500}>
                          {f.title}
                        </Text>
                      )}
                      {f.message && (
                        <Text size="xs" c="dimmed">
                          {f.message}
                        </Text>
                      )}
                      <EvidenceList evidence={f.evidence} />
                      {f.source_id && (
                        <AnchorLink to="/sources/$id" params={{ id: f.source_id }} size="xs">
                          source
                        </AnchorLink>
                      )}
                    </Table.Td>
                    <Table.Td>
                      <AnchorLink to="/findings" search={{ type: f.type }} size="sm">
                        {humanize(f.type)}
                      </AnchorLink>
                      <Tooltip label={FINDING_CLASSES[f.class] ?? f.class}>
                        <Text size="xs" c="dimmed">
                          {f.class}
                        </Text>
                      </Tooltip>
                    </Table.Td>
                    <Table.Td>{projectName(f.project_id)}</Table.Td>
                    <Table.Td>
                      <Text size="xs">
                        first <TimeAgo value={f.first_seen_at} />
                      </Text>
                      <Text size="xs" c="dimmed">
                        last <TimeAgo value={f.last_seen_at} />
                      </Text>
                    </Table.Td>
                    <Table.Td>
                      <Stack gap={2}>
                        <Badge size="xs" variant="light" color={f.state === 'open' ? 'orange' : 'gray'}>
                          {f.state}
                        </Badge>
                        {f.acknowledged_at && (
                          <Tooltip label={`Acknowledged by ${f.acknowledged_by ?? 'someone'}`}>
                            <Badge size="xs" variant="outline" color="gray" leftSection={<IconCheck size={10} />}>
                              acknowledged
                            </Badge>
                          </Tooltip>
                        )}
                      </Stack>
                    </Table.Td>
                    <Table.Td ta="right">
                      {canAck && f.state === 'open' && !f.acknowledged_at && (
                        <Button
                          size="compact-xs"
                          variant="default"
                          onClick={() => ack.mutate(f)}
                          loading={ack.isPending && ack.variables.id === f.id}
                        >
                          Acknowledge
                        </Button>
                      )}
                    </Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
        </Card>
      )}
    </Page>
  )
}
