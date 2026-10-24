import {
  ActionIcon,
  Alert as MAlert,
  Badge,
  Button,
  Card,
  Group,
  Modal,
  MultiSelect,
  NumberInput,
  SegmentedControl,
  Select,
  Stack,
  Switch,
  Table,
  Tabs,
  Text,
  TextInput,
  Textarea,
  Tooltip,
} from '@mantine/core'
import { notifications } from '@mantine/notifications'
import { IconPencil, IconPlus, IconTrash } from '@tabler/icons-react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { isInstallationAdmin, useMe } from '@/api/auth'
import { api, check, errorMessage, unwrap, type Rule, type Schemas } from '@/api/client'
import { keys, useProjects } from '@/api/queries'
import { Page } from '@/components/AppShell'
import { ConfirmModal, PageHeader, TimeAgo } from '@/components/common'
import { EmptyState, ErrorState, LoadingRows } from '@/components/States'
import { MetaBadge, SeverityBadge } from '@/components/StatusBadge'
import { FINDING_TYPES } from '@/features/findings/FindingsPage'
import { EvidenceList } from '@/features/findings/FindingItem'
import { useTitle } from '@/lib/appStore'
import { duration, humanize } from '@/lib/format'

const STATE_META: Record<
  string,
  { label: string; tone: 'red' | 'yellow' | 'green' | 'gray'; icon: string; description: string }
> = {
  firing: {
    label: 'Firing',
    tone: 'red',
    icon: 'alert-octagon',
    description: 'The condition holds and notifications are sent.',
  },
  pending: {
    label: 'Pending',
    tone: 'yellow',
    icon: 'clock-pause',
    description: 'Waiting for the rule’s “for” duration.',
  },
  resolved: { label: 'Resolved', tone: 'green', icon: 'circle-check', description: 'The finding closed.' },
}

const DELIVERY_META: Record<
  string,
  { label: string; tone: 'green' | 'yellow' | 'red' | 'gray'; icon: string; description: string }
> = {
  published: {
    label: 'Delivered',
    tone: 'green',
    icon: 'circle-check',
    description: 'The notification event was published.',
  },
  pending: { label: 'Queued', tone: 'gray', icon: 'clock-pause', description: 'Waiting in the outbox.' },
  retrying: {
    label: 'Retrying',
    tone: 'yellow',
    icon: 'alert-triangle',
    description: 'Publishing failed; retrying with backoff.',
  },
  dead: {
    label: 'Failed',
    tone: 'red',
    icon: 'circle-x',
    description: 'Publishing exhausted its retries (dead-lettered).',
  },
}

function AlertsTab() {
  const [state, setState] = useState<string>('active')
  const q = useQuery({
    queryKey: keys.alerts({ state }),
    queryFn: async () =>
      unwrap(
        await api.GET('/alerts', {
          params: {
            query: {
              state: (state === 'active' ? ['firing', 'pending'] : state === 'all' ? [] : [state]) as (
                'firing' | 'pending' | 'resolved' | 'suppressed'
              )[],
              limit: 200,
            },
          },
        }),
      ),
    refetchInterval: 30_000,
  })
  return (
    <Stack gap="sm">
      <SegmentedControl
        value={state}
        onChange={setState}
        data={[
          { value: 'active', label: 'Firing & pending' },
          { value: 'firing', label: 'Firing' },
          { value: 'resolved', label: 'Resolved' },
          { value: 'all', label: 'All' },
        ]}
        w="fit-content"
      />
      {q.isPending ? (
        <LoadingRows />
      ) : q.isError ? (
        <ErrorState error={q.error} what="alerts" onRetry={() => void q.refetch()} />
      ) : q.data.length === 0 ? (
        <EmptyState title="No alerts">Alerts come from rules that match findings; none match right now.</EmptyState>
      ) : (
        <Card p={0}>
          <Table.ScrollContainer minWidth={900}>
            <Table fz="sm" verticalSpacing={6} aria-label="Alerts" data-testid="alerts-table">
              <Table.Thead>
                <Table.Tr>
                  <Table.Th>State</Table.Th>
                  <Table.Th>Alert</Table.Th>
                  <Table.Th>Rule</Table.Th>
                  <Table.Th>Since</Table.Th>
                  <Table.Th>Delivery</Table.Th>
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {q.data.map((a) => (
                  <Table.Tr key={a.id}>
                    <Table.Td>
                      <MetaBadge
                        meta={
                          STATE_META[a.state] ?? { label: a.state, tone: 'gray', icon: 'help-circle', description: '' }
                        }
                        size="xs"
                      />
                      {a.silenced_by && (
                        <Badge size="xs" variant="outline" color="gray" ml={4}>
                          silenced
                        </Badge>
                      )}
                    </Table.Td>
                    <Table.Td>
                      <Group gap={6}>
                        <SeverityBadge severity={a.severity} size="xs" />
                        <Text size="sm" fw={500}>
                          {a.title}
                        </Text>
                      </Group>
                      <EvidenceList evidence={a.evidence} />
                    </Table.Td>
                    <Table.Td>{a.rule}</Table.Td>
                    <Table.Td>
                      <TimeAgo value={a.fired_at ?? a.started_at} />
                      {a.resolved_at && (
                        <Text size="xs" c="dimmed">
                          resolved <TimeAgo value={a.resolved_at} />
                        </Text>
                      )}
                    </Table.Td>
                    <Table.Td>
                      {a.delivery ? (
                        <MetaBadge
                          meta={
                            DELIVERY_META[a.delivery] ?? {
                              label: a.delivery,
                              tone: 'gray',
                              icon: 'help-circle',
                              description: '',
                            }
                          }
                          size="xs"
                        />
                      ) : (
                        <Text size="xs" c="dimmed">
                          not notified
                        </Text>
                      )}
                      <Text size="xs" c="dimmed">
                        {a.notifications} notification{a.notifications === 1 ? '' : 's'}
                      </Text>
                    </Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
        </Card>
      )}
    </Stack>
  )
}

type RuleInput = Schemas['RuleInput']

function MatcherFields({ value, onChange }: { value: Schemas['Matcher']; onChange: (m: Schemas['Matcher']) => void }) {
  const projects = useProjects()
  return (
    <Stack gap="xs">
      <MultiSelect
        label="Finding types"
        description="Empty = every type"
        data={FINDING_TYPES.map((t) => ({ value: t, label: humanize(t) }))}
        value={value.types ?? []}
        onChange={(v) => onChange({ ...value, types: v })}
        searchable
        clearable
      />
      <Select
        label="Minimum severity"
        data={[
          { value: '', label: 'Any' },
          { value: 'info', label: 'Info' },
          { value: 'warning', label: 'Warning' },
          { value: 'critical', label: 'Critical' },
        ]}
        value={value.min_severity ?? ''}
        onChange={(v) => onChange({ ...value, min_severity: (v ?? '') as Schemas['Matcher']['min_severity'] })}
        allowDeselect={false}
      />
      <MultiSelect
        label="Classes"
        data={['confirmed', 'warning', 'stale', 'unknown', 'not_applicable']}
        value={value.classes ?? []}
        onChange={(v) => onChange({ ...value, classes: v })}
        clearable
      />
      <MultiSelect
        label="Projects"
        description="Empty = every project"
        data={(projects.data ?? []).map((p) => ({ value: p.id, label: p.display_name || p.name }))}
        value={value.projects ?? []}
        onChange={(v) => onChange({ ...value, projects: v })}
        clearable
      />
    </Stack>
  )
}

function RuleModal({ rule, opened, onClose }: { rule: Rule | null; opened: boolean; onClose: () => void }) {
  const qc = useQueryClient()
  const [v, setV] = useState<RuleInput>(() =>
    rule
      ? {
          name: rule.name,
          description: rule.description,
          enabled: rule.enabled,
          for_seconds: rule.for_seconds,
          cooldown_seconds: rule.cooldown_seconds,
          matcher: rule.matcher,
          severity: (rule.severity ?? '') as RuleInput['severity'],
        }
      : { name: '', description: '', enabled: true, for_seconds: 0, cooldown_seconds: 3600, matcher: {}, severity: '' },
  )
  const save = useMutation({
    mutationFn: async () =>
      rule
        ? unwrap(
            await api.PUT('/alert-rules/{id}', {
              params: { path: { id: rule.id }, header: { 'If-Match': String(rule.resource_version) } },
              body: v,
            }),
          )
        : unwrap(await api.POST('/alert-rules', { body: v })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: keys.alertRules() })
      onClose()
    },
  })
  return (
    <Modal
      opened={opened}
      onClose={onClose}
      title={rule ? `Edit rule ${rule.name}` : 'New alert rule'}
      size="lg"
      centered
    >
      <form
        onSubmit={(e) => {
          e.preventDefault()
          save.mutate()
        }}
      >
        <Stack gap="sm">
          {rule?.builtin && (
            <MAlert color="blue" variant="light">
              Built-in rule: you can enable, disable and tune it.
            </MAlert>
          )}
          <TextInput
            label="Name"
            required
            value={v.name ?? ''}
            onChange={(e) => setV({ ...v, name: e.currentTarget.value })}
            disabled={rule?.builtin}
          />
          <Textarea
            label="Description"
            value={v.description ?? ''}
            onChange={(e) => setV({ ...v, description: e.currentTarget.value })}
            autosize
            minRows={2}
          />
          <MatcherFields value={v.matcher} onChange={(m) => setV({ ...v, matcher: m })} />
          <Group grow>
            <NumberInput
              label="Fire after (seconds)"
              description="The finding must stay open this long."
              value={v.for_seconds ?? 0}
              onChange={(n) => setV({ ...v, for_seconds: typeof n === 'number' ? n : 0 })}
              min={0}
            />
            <NumberInput
              label="Re-notify cooldown (seconds)"
              value={v.cooldown_seconds ?? 0}
              onChange={(n) => setV({ ...v, cooldown_seconds: typeof n === 'number' ? n : 0 })}
              min={0}
            />
          </Group>
          <Select
            label="Override severity"
            data={[
              { value: '', label: 'Keep the finding’s severity' },
              { value: 'info', label: 'Info' },
              { value: 'warning', label: 'Warning' },
              { value: 'critical', label: 'Critical' },
            ]}
            value={v.severity ?? ''}
            onChange={(s) => setV({ ...v, severity: (s ?? '') as RuleInput['severity'] })}
            allowDeselect={false}
          />
          <Switch
            label="Enabled"
            checked={v.enabled ?? true}
            onChange={(e) => setV({ ...v, enabled: e.currentTarget.checked })}
          />
          {save.isError && (
            <MAlert color="red" role="alert">
              {errorMessage(save.error)}
            </MAlert>
          )}
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" loading={save.isPending}>
              Save rule
            </Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  )
}

function RulesTab({ admin }: { admin: boolean }) {
  const qc = useQueryClient()
  const rules = useQuery({ queryKey: keys.alertRules(), queryFn: async () => unwrap(await api.GET('/alert-rules')) })
  const [edit, setEdit] = useState<Rule | null | 'new'>(null)
  const [del, setDel] = useState<Rule | null>(null)
  const toggle = useMutation({
    mutationFn: async (r: Rule) =>
      unwrap(
        await api.PUT('/alert-rules/{id}', {
          params: { path: { id: r.id }, header: { 'If-Match': String(r.resource_version) } },
          body: {
            name: r.name,
            description: r.description,
            enabled: !r.enabled,
            for_seconds: r.for_seconds,
            cooldown_seconds: r.cooldown_seconds,
            matcher: r.matcher,
            severity: (r.severity ?? '') as RuleInput['severity'],
          },
        }),
      ),
    onSuccess: () => void qc.invalidateQueries({ queryKey: keys.alertRules() }),
    onError: (e) => notifications.show({ color: 'red', title: 'Could not update the rule', message: errorMessage(e) }),
  })
  const remove = useMutation({
    mutationFn: async (r: Rule) => check(await api.DELETE('/alert-rules/{id}', { params: { path: { id: r.id } } })),
    onSuccess: () => {
      setDel(null)
      void qc.invalidateQueries({ queryKey: keys.alertRules() })
    },
  })
  if (rules.isPending) return <LoadingRows />
  if (rules.isError) return <ErrorState error={rules.error} what="alert rules" />
  return (
    <Stack gap="sm">
      {admin && (
        <Group>
          <Button leftSection={<IconPlus size={14} />} onClick={() => setEdit('new')}>
            New rule
          </Button>
        </Group>
      )}
      <Card p={0}>
        <Table fz="sm" verticalSpacing={6} aria-label="Alert rules">
          <Table.Thead>
            <Table.Tr>
              <Table.Th>Enabled</Table.Th>
              <Table.Th>Rule</Table.Th>
              <Table.Th>Matches</Table.Th>
              <Table.Th>Timing</Table.Th>
              <Table.Th />
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {rules.data.map((r) => (
              <Table.Tr key={r.id}>
                <Table.Td>
                  <Switch
                    checked={r.enabled}
                    onChange={() => toggle.mutate(r)}
                    disabled={!admin}
                    aria-label={`${r.enabled ? 'Disable' : 'Enable'} rule ${r.name}`}
                  />
                </Table.Td>
                <Table.Td>
                  <Text size="sm" fw={500}>
                    {r.name}{' '}
                    {r.builtin && (
                      <Badge size="xs" variant="outline">
                        built-in
                      </Badge>
                    )}
                  </Text>
                  <Text size="xs" c="dimmed">
                    {r.description}
                  </Text>
                </Table.Td>
                <Table.Td>
                  <Text size="xs">
                    {r.matcher.types?.length ? r.matcher.types.map(humanize).join(', ') : 'any type'}
                    {r.matcher.min_severity ? ` · ≥ ${r.matcher.min_severity}` : ''}
                    {r.matcher.classes?.length ? ` · ${r.matcher.classes.join(', ')}` : ''}
                    {r.matcher.projects?.length ? ` · ${r.matcher.projects.length} projects` : ''}
                  </Text>
                </Table.Td>
                <Table.Td>
                  <Text size="xs">
                    fire after {r.for_seconds ? duration(r.for_seconds) : 'immediately'} · cooldown{' '}
                    {duration(r.cooldown_seconds)}
                  </Text>
                </Table.Td>
                <Table.Td ta="right">
                  {admin && (
                    <Group gap={4} justify="flex-end">
                      <ActionIcon aria-label={`Edit rule ${r.name}`} onClick={() => setEdit(r)}>
                        <IconPencil size={14} />
                      </ActionIcon>
                      {!r.builtin && (
                        <ActionIcon color="red" aria-label={`Delete rule ${r.name}`} onClick={() => setDel(r)}>
                          <IconTrash size={14} />
                        </ActionIcon>
                      )}
                    </Group>
                  )}
                </Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      </Card>
      {edit !== null && <RuleModal rule={edit === 'new' ? null : edit} opened onClose={() => setEdit(null)} />}
      <ConfirmModal
        opened={del !== null}
        title="Delete rule"
        danger
        confirmLabel="Delete"
        pending={remove.isPending}
        onClose={() => setDel(null)}
        onConfirm={() => del && remove.mutate(del)}
      >
        Delete the rule “{del?.name}”? Its open alerts resolve.
      </ConfirmModal>
    </Stack>
  )
}

function SilencesTab({ admin }: { admin: boolean }) {
  const qc = useQueryClient()
  const silences = useQuery({ queryKey: keys.silences(), queryFn: async () => unwrap(await api.GET('/silences')) })
  const [open, setOpen] = useState(false)
  const [comment, setComment] = useState('')
  const [hours, setHours] = useState(4)
  const [matcher, setMatcher] = useState<Schemas['Matcher']>({})
  const create = useMutation({
    mutationFn: async () => unwrap(await api.POST('/silences', { body: { comment, hours, matcher } })),
    onSuccess: () => {
      setOpen(false)
      void qc.invalidateQueries({ queryKey: keys.silences() })
    },
  })
  const remove = useMutation({
    mutationFn: async (id: string) => check(await api.DELETE('/silences/{id}', { params: { path: { id } } })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: keys.silences() }),
  })
  return (
    <Stack gap="sm">
      {admin && (
        <Group>
          <Button leftSection={<IconPlus size={14} />} onClick={() => setOpen(true)}>
            New silence
          </Button>
        </Group>
      )}
      {silences.isPending ? (
        <LoadingRows />
      ) : silences.isError ? (
        <ErrorState error={silences.error} what="silences" />
      ) : silences.data.length === 0 ? (
        <EmptyState title="No silences">
          Silences mute notifications for matching alerts for a while; alerts still appear.
        </EmptyState>
      ) : (
        <Card p={0}>
          <Table fz="sm" aria-label="Silences">
            <Table.Thead>
              <Table.Tr>
                <Table.Th>Comment</Table.Th>
                <Table.Th>Matches</Table.Th>
                <Table.Th>Until</Table.Th>
                <Table.Th>By</Table.Th>
                <Table.Th />
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {silences.data.map((s) => (
                <Table.Tr key={s.id}>
                  <Table.Td>{s.comment}</Table.Td>
                  <Table.Td>
                    <Text size="xs">
                      {s.matcher.types?.map(humanize).join(', ') || 'any type'}
                      {s.matcher.min_severity ? ` · ≥ ${s.matcher.min_severity}` : ''}
                    </Text>
                  </Table.Td>
                  <Table.Td>
                    <TimeAgo value={s.ends_at} />
                  </Table.Td>
                  <Table.Td>{s.created_by}</Table.Td>
                  <Table.Td ta="right">
                    {admin && (
                      <Tooltip label="End the silence now">
                        <ActionIcon color="red" aria-label="Delete silence" onClick={() => remove.mutate(s.id)}>
                          <IconTrash size={14} />
                        </ActionIcon>
                      </Tooltip>
                    )}
                  </Table.Td>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        </Card>
      )}
      <Modal opened={open} onClose={() => setOpen(false)} title="New silence" centered>
        <form
          onSubmit={(e) => {
            e.preventDefault()
            create.mutate()
          }}
        >
          <Stack gap="sm">
            <TextInput
              label="Comment"
              required
              value={comment}
              onChange={(e) => setComment(e.currentTarget.value)}
              placeholder="Planned maintenance of nas-01"
            />
            <NumberInput
              label="Duration (hours)"
              min={1}
              max={720}
              value={hours}
              onChange={(v) => setHours(typeof v === 'number' ? v : 4)}
            />
            <MatcherFields value={matcher} onChange={setMatcher} />
            {create.isError && <MAlert color="red">{errorMessage(create.error)}</MAlert>}
            <Group justify="flex-end">
              <Button variant="default" onClick={() => setOpen(false)}>
                Cancel
              </Button>
              <Button type="submit" loading={create.isPending} disabled={!comment.trim()}>
                Create silence
              </Button>
            </Group>
          </Stack>
        </form>
      </Modal>
    </Stack>
  )
}

export function AlertsPage() {
  useTitle('Alerts')
  const me = useMe().data
  const admin = isInstallationAdmin(me)
  return (
    <Page wide>
      <PageHeader
        title="Alerts"
        description="Rules turn findings into alerts; firing alerts are published as events (Pulse) through the outbox. Silences mute notifications, not alerts."
      />
      <Tabs defaultValue="alerts">
        <Tabs.List mb="md">
          <Tabs.Tab value="alerts">Alerts</Tabs.Tab>
          <Tabs.Tab value="rules">Rules</Tabs.Tab>
          <Tabs.Tab value="silences">Silences</Tabs.Tab>
        </Tabs.List>
        <Tabs.Panel value="alerts">
          <AlertsTab />
        </Tabs.Panel>
        <Tabs.Panel value="rules">
          <RulesTab admin={admin} />
        </Tabs.Panel>
        <Tabs.Panel value="silences">
          <SilencesTab admin={admin} />
        </Tabs.Panel>
      </Tabs>
    </Page>
  )
}
