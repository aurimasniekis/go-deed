import {
  ActionIcon,
  Badge,
  Button,
  Card,
  Group,
  Modal,
  Stack,
  Table,
  TagsInput,
  Text,
  Textarea,
  TextInput,
  Title,
} from '@mantine/core'
import { notifications } from '@mantine/notifications'
import { IconPencil, IconPlus, IconTrash } from '@tabler/icons-react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { api, check, errorMessage, unwrap, type Schemas } from '@/api/client'
import { ConfirmModal, Mono, TimeAgo } from '@/components/common'
import { ErrorState, LoadingRows } from '@/components/States'

type Rule = Schemas['LabelRule']

/** `key=value` lines ↔ a label map. */
function toLines(m: Record<string, string> | null | undefined): string {
  return Object.entries(m ?? {})
    .map(([k, v]) => `${k}=${v}`)
    .join('\n')
}
function fromLines(s: string): Record<string, string> {
  const out: Record<string, string> = {}
  for (const line of s.split('\n')) {
    const t = line.trim()
    if (!t) continue
    const i = t.indexOf('=')
    if (i > 0) out[t.slice(0, i).trim()] = t.slice(i + 1).trim()
  }
  return out
}

function RuleModal({ project, rule, onClose }: { project: string; rule: Rule | null; onClose: () => void }) {
  const qc = useQueryClient()
  const [name, setName] = useState(rule?.name ?? '')
  const [description, setDescription] = useState(rule?.description ?? '')
  const [kinds, setKinds] = useState<string[]>(rule?.match.kinds ?? [])
  const [sources, setSources] = useState<string[]>(rule?.match.sources ?? [])
  const [matchLabels, setMatchLabels] = useState(toLines(rule?.match.labels))
  const [labels, setLabels] = useState(toLines(rule?.labels))
  const save = useMutation({
    mutationFn: async () => {
      const body = {
        name,
        description,
        match: { kinds, sources, labels: fromLines(matchLabels) },
        labels: fromLines(labels),
      }
      return rule
        ? unwrap(
            await api.PATCH('/projects/{project}/label-rules/{rule}', {
              params: { path: { project, rule: rule.id }, header: { 'If-Match': String(rule.resource_version) } },
              body,
            }),
          )
        : unwrap(await api.POST('/projects/{project}/label-rules', { params: { path: { project } }, body }))
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['projects', project, 'label-rules'] })
      void qc.invalidateQueries({ queryKey: ['entities'] })
      void qc.invalidateQueries({ queryKey: ['graph'] })
      onClose()
    },
  })
  return (
    <Modal opened onClose={onClose} title={rule ? `Label rule ${rule.name}` : 'New label rule'} size="lg">
      <Stack gap="sm">
        <TextInput label="Name" value={name} onChange={(e) => setName(e.currentTarget.value)} required data-autofocus />
        <TextInput label="Description" value={description} onChange={(e) => setDescription(e.currentTarget.value)} />
        <Text size="sm" fw={600}>
          Applies to entities that match all of
        </Text>
        <TagsInput
          label="Kinds"
          description="Globs allowed (dns_*); any may match. Empty: every kind."
          value={kinds}
          onChange={setKinds}
          clearable
        />
        <TagsInput
          label="Sources"
          description="Names of sources that describe the entity; globs allowed (tf-cloudflare-*). Empty: any source."
          value={sources}
          onChange={setSources}
          clearable
        />
        <Textarea
          label="Labels the sources set"
          description="One key=value per line; values may be globs (terraform/type=cloudflare_*). All must match."
          value={matchLabels}
          onChange={(e) => setMatchLabels(e.currentTarget.value)}
          autosize
          minRows={2}
          styles={{ input: { fontFamily: 'var(--deed-mono)', fontSize: 12 } }}
        />
        <Textarea
          label="Labels to add"
          description="One key=value per line. A source's own label for the same key wins; the rule never keeps an entity alive."
          value={labels}
          onChange={(e) => setLabels(e.currentTarget.value)}
          autosize
          minRows={2}
          required
          styles={{ input: { fontFamily: 'var(--deed-mono)', fontSize: 12 } }}
          data-testid="label-rule-labels"
        />
        {save.isError && (
          <Text c="red" size="sm">
            {errorMessage(save.error)}
          </Text>
        )}
        <Group justify="flex-end">
          <Button variant="default" onClick={onClose}>
            Cancel
          </Button>
          <Button onClick={() => save.mutate()} loading={save.isPending} data-testid="label-rule-save">
            {rule ? 'Save and re-apply' : 'Create and apply'}
          </Button>
        </Group>
      </Stack>
    </Modal>
  )
}

function matchText(m: Rule['match']): string {
  const parts = [
    m.kinds?.length ? `kind ${m.kinds.join(' | ')}` : '',
    m.sources?.length ? `source ${m.sources.join(' | ')}` : '',
    ...Object.entries(m.labels ?? {}).map(([k, v]) => `${k}=${v}`),
  ].filter(Boolean)
  return parts.length ? parts.join(' · ') : 'every entity'
}

/**
 * A project's label rules (docs/LABEL_RULES.md): labels added below every source, by kind, describing source or
 * existing labels. Viewers see them; project admins change them.
 */
export function LabelRulesSection({ project, admin }: { project: string; admin: boolean }) {
  const qc = useQueryClient()
  const rules = useQuery({
    queryKey: ['projects', project, 'label-rules'],
    queryFn: async () => unwrap(await api.GET('/projects/{project}/label-rules', { params: { path: { project } } })),
  })
  const [editing, setEditing] = useState<Rule | 'new' | null>(null)
  const [deleting, setDeleting] = useState<Rule | null>(null)
  const remove = useMutation({
    mutationFn: async (r: Rule) =>
      check(await api.DELETE('/projects/{project}/label-rules/{rule}', { params: { path: { project, rule: r.id } } })),
    onSuccess: () => {
      setDeleting(null)
      void qc.invalidateQueries({ queryKey: ['projects', project, 'label-rules'] })
      void qc.invalidateQueries({ queryKey: ['entities'] })
      void qc.invalidateQueries({ queryKey: ['graph'] })
    },
    onError: (e) => notifications.show({ color: 'red', title: 'Could not delete the rule', message: errorMessage(e) }),
  })
  return (
    <>
      <Group justify="space-between" mt="lg" mb="xs">
        <div>
          <Title order={2} fz={16}>
            Label rules
          </Title>
          <Text size="xs" c="dimmed">
            Add labels (such as group=github) to entities other sources own, below every source: their own labels win,
            and a rule never keeps an entity alive.
          </Text>
        </div>
        {admin && (
          <Button
            size="xs"
            leftSection={<IconPlus size={14} />}
            onClick={() => setEditing('new')}
            data-testid="label-rule-new"
          >
            New rule
          </Button>
        )}
      </Group>
      <Card p={0}>
        {rules.isPending ? (
          <LoadingRows />
        ) : rules.isError ? (
          <ErrorState error={rules.error} what="label rules" />
        ) : rules.data.length === 0 ? (
          <Text size="sm" c="dimmed" p="md">
            No label rules.
          </Text>
        ) : (
          <Table fz="sm" aria-label="Label rules">
            <Table.Thead>
              <Table.Tr>
                <Table.Th>Name</Table.Th>
                <Table.Th>Applies to</Table.Th>
                <Table.Th>Adds</Table.Th>
                <Table.Th>Updated</Table.Th>
                {admin && <Table.Th aria-label="Actions" />}
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {rules.data.map((r) => (
                <Table.Tr key={r.id}>
                  <Table.Td>
                    <Mono>{r.name}</Mono>
                    {r.description && (
                      <Text size="xs" c="dimmed">
                        {r.description}
                      </Text>
                    )}
                  </Table.Td>
                  <Table.Td>
                    <Text size="xs">{matchText(r.match)}</Text>
                  </Table.Td>
                  <Table.Td>
                    <Group gap={4}>
                      {Object.entries(r.labels).map(([k, v]) => (
                        <Badge key={k} variant="light" size="sm" tt="none">
                          {k}={v}
                        </Badge>
                      ))}
                    </Group>
                  </Table.Td>
                  <Table.Td>
                    <TimeAgo value={r.updated_at} />
                  </Table.Td>
                  {admin && (
                    <Table.Td>
                      <Group gap={4} justify="flex-end" wrap="nowrap">
                        <ActionIcon variant="subtle" aria-label={`Edit ${r.name}`} onClick={() => setEditing(r)}>
                          <IconPencil size={14} />
                        </ActionIcon>
                        <ActionIcon
                          variant="subtle"
                          color="red"
                          aria-label={`Delete ${r.name}`}
                          onClick={() => setDeleting(r)}
                        >
                          <IconTrash size={14} />
                        </ActionIcon>
                      </Group>
                    </Table.Td>
                  )}
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        )}
      </Card>
      {editing && (
        <RuleModal project={project} rule={editing === 'new' ? null : editing} onClose={() => setEditing(null)} />
      )}
      <ConfirmModal
        opened={!!deleting}
        title="Delete label rule"
        danger
        confirmLabel="Delete rule"
        pending={remove.isPending}
        onClose={() => setDeleting(null)}
        onConfirm={() => deleting && remove.mutate(deleting)}
      >
        <Text size="sm">
          The labels {deleting?.name} added go away from the project&apos;s entities (unless a source or another rule
          sets them).
        </Text>
      </ConfirmModal>
    </>
  )
}
