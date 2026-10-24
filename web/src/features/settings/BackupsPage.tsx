import { Alert, Badge, Button, Card, Code, Group, SimpleGrid, Stack, Table, Text, Title, Tooltip } from '@mantine/core'
import { notifications } from '@mantine/notifications'
import { IconArchive, IconPlayerPlay } from '@tabler/icons-react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { isInstallationAdmin, useMe } from '@/api/auth'
import { api, ApiError, errorMessage, unwrap } from '@/api/client'
import { Page } from '@/components/AppShell'
import { CopyIcon, Field, PageHeader, TimeAgo } from '@/components/common'
import { EmptyState, ErrorState, LoadingRows } from '@/components/States'
import { useTitle } from '@/lib/appStore'
import { dateTime, duration } from '@/lib/format'

function bytes(n: number | undefined): string {
  if (!n) return '—'
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB']
  let i = 0
  let v = n
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(i ? 1 : 0)} ${units[i]}`
}

/**
 * Encrypted backups (ADR-010): status, run history, stored backups and the verification key. Restores are offline
 * and CLI-only, so this page documents them instead of offering a button. Feature-detected: servers without the
 * backup API show how backups are configured.
 */
export function BackupsPage() {
  useTitle('Backups')
  const qc = useQueryClient()
  const admin = isInstallationAdmin(useMe().data)
  const q = useQuery({
    queryKey: ['backups'],
    queryFn: async () => {
      const res = await api.GET('/backups', { params: { query: { remote: true, limit: 50 } } })
      if (res.response.status === 404) return null
      return unwrap(res)
    },
    refetchInterval: (query) => (query.state.data?.running ? 3000 : 60_000),
    retry: false,
  })
  const run = useMutation({
    mutationFn: async () => unwrap(await api.POST('/backups')),
    onSuccess: () => {
      notifications.show({ color: 'blue', message: 'Backup started' })
      void qc.invalidateQueries({ queryKey: ['backups'] })
    },
    onError: (e) =>
      notifications.show({
        color: 'red',
        title: 'Could not start a backup',
        message: e instanceof ApiError && e.status === 409 ? 'A backup is already running.' : errorMessage(e),
      }),
  })
  const d = q.data
  return (
    <Page>
      <PageHeader
        title="Backups"
        description="Encrypted (age), signed backups of the database to object storage. Restores run offline with the CLI."
        actions={
          admin &&
          d?.configured && (
            <Button
              leftSection={<IconPlayerPlay size={14} />}
              onClick={() => run.mutate()}
              loading={run.isPending || d.running}
              data-testid="run-backup"
            >
              {d.running ? 'Backup running…' : 'Back up now'}
            </Button>
          )
        }
      />
      {q.isPending ? (
        <LoadingRows />
      ) : q.isError ? (
        <ErrorState error={q.error} what="backups" onRetry={() => void q.refetch()} />
      ) : d === null || d === undefined ? (
        <EmptyState title="Backups are configured on the server" icon={<IconArchive size={22} />}>
          Backups are configured on the server; status will appear here when this server exposes the backup API.
        </EmptyState>
      ) : (
        <Stack gap="sm">
          {!d.configured && (
            <Alert color="yellow" title="Backups are not configured">
              Configure <Code>backup:</Code> in deed.yaml (object storage, age recipients, schedule). See
              docs/BACKUP.md.
            </Alert>
          )}
          <SimpleGrid cols={{ base: 1, md: 2 }}>
            <Card>
              <Title order={2} fz={14} mb="xs">
                Status
              </Title>
              <Field label="Configured">{d.configured ? 'yes' : 'no'}</Field>
              <Field label="Scheduled">{d.scheduled ? 'yes' : 'no (manual only)'}</Field>
              <Field label="Running">{d.running ? 'yes' : 'no'}</Field>
              {d.verify_key && (
                <Field label="Verification key">
                  <Stack gap={2}>
                    <Group gap={4} wrap="nowrap">
                      <Code style={{ wordBreak: 'break-all' }} data-testid="verify-key">
                        {d.verify_key}
                      </Code>
                      <CopyIcon value={d.verify_key} label="Copy verification key" />
                    </Group>
                    <Text size="xs" c="dimmed">
                      Store this with your age identity; restores need it.
                    </Text>
                  </Stack>
                </Field>
              )}
            </Card>
            <Card>
              <Title order={2} fz={14} mb="xs">
                Restoring
              </Title>
              <Text size="sm">
                Restore is offline and CLI-only: stop Deed, verify, then restore with the age identity and this
                verification key. Every session is revoked afterwards and a <Code>backup_restored</Code> finding opens.
              </Text>
              <Code block mt="xs" fz={11}>
                {`deed backup verify -config deed.yaml -identity id.txt \\\n  -verify-key deed-backup-ed25519:… -id latest\ndeed restore -config deed.yaml -identity id.txt \\\n  -verify-key deed-backup-ed25519:… -id latest -yes`}
              </Code>
              <Text size="xs" c="dimmed" mt="xs">
                Full runbook and failure modes: docs/BACKUP.md in the Deed repository.
              </Text>
            </Card>
          </SimpleGrid>
          <Card p={0}>
            <Title order={2} fz={14} p="sm">
              Runs on this server
            </Title>
            {d.runs.length === 0 ? (
              <Text size="sm" c="dimmed" px="sm" pb="sm">
                No backups have run yet.
              </Text>
            ) : (
              <Table fz="sm" aria-label="Backup runs" data-testid="backup-runs">
                <Table.Thead>
                  <Table.Tr>
                    <Table.Th>Started</Table.Th>
                    <Table.Th>Result</Table.Th>
                    <Table.Th>Trigger</Table.Th>
                    <Table.Th>Size</Table.Th>
                    <Table.Th>Object</Table.Th>
                  </Table.Tr>
                </Table.Thead>
                <Table.Tbody>
                  {d.runs.map((r) => (
                    <Table.Tr key={r.id}>
                      <Table.Td>
                        {dateTime(r.started_at)}
                        {r.finished_at && (
                          <Text size="xs" c="dimmed">
                            took {duration((Date.parse(r.finished_at) - Date.parse(r.started_at)) / 1000)}
                          </Text>
                        )}
                      </Table.Td>
                      <Table.Td>
                        <Badge
                          variant="light"
                          color={r.result === 'ok' ? 'green' : r.result === 'running' ? 'blue' : 'red'}
                        >
                          {r.result}
                        </Badge>
                        {r.error && (
                          <Text size="xs" c="red">
                            {r.error}
                          </Text>
                        )}
                      </Table.Td>
                      <Table.Td>{r.trigger}</Table.Td>
                      <Table.Td>{bytes(r.size_bytes)}</Table.Td>
                      <Table.Td className="mono" fz="xs">
                        {r.object_key}
                        {r.schema_version ? ` · schema ${r.schema_version}` : ''}
                      </Table.Td>
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            )}
          </Card>
          <Card p={0}>
            <Title order={2} fz={14} p="sm">
              Stored backups
            </Title>
            {d.remote_error && (
              <Alert color="orange" mx="sm" mb="sm" title="Could not list the bucket">
                {d.remote_error}
              </Alert>
            )}
            {(d.remote ?? []).length === 0 ? (
              <Text size="sm" c="dimmed" px="sm" pb="sm">
                {d.remote_error ? '' : 'No stored backups found.'}
              </Text>
            ) : (
              <Table fz="sm" aria-label="Stored backups">
                <Table.Thead>
                  <Table.Tr>
                    <Table.Th>Created</Table.Th>
                    <Table.Th>Signature</Table.Th>
                    <Table.Th>Complete</Table.Th>
                    <Table.Th>Size</Table.Th>
                    <Table.Th>Version</Table.Th>
                  </Table.Tr>
                </Table.Thead>
                <Table.Tbody>
                  {(d.remote ?? []).map((b) => (
                    <Table.Tr key={b.id}>
                      <Table.Td>
                        <TimeAgo value={b.created} />
                        <Text size="xs" c="dimmed" className="mono">
                          {b.id}
                        </Text>
                      </Table.Td>
                      <Table.Td>
                        {b.verified ? (
                          <Badge color="green" variant="light">
                            signed by this installation
                          </Badge>
                        ) : (
                          <Tooltip label="The manifest was not signed by this installation's backup key. Do not restore it unless you know where it came from.">
                            <Badge color="red" variant="light">
                              not verified
                            </Badge>
                          </Tooltip>
                        )}
                      </Table.Td>
                      <Table.Td>{b.complete ? 'yes' : <Badge color="orange">incomplete</Badge>}</Table.Td>
                      <Table.Td>{bytes(b.bytes)}</Table.Td>
                      <Table.Td fz="xs">
                        {b.manifest ? `${b.manifest.app_version} · schema ${b.manifest.schema_version}` : '—'}
                      </Table.Td>
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            )}
          </Card>
        </Stack>
      )}
    </Page>
  )
}
