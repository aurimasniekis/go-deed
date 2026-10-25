import {
  ActionIcon,
  Alert,
  Badge,
  Button,
  Card,
  ColorInput,
  Group,
  Modal,
  MultiSelect,
  Select,
  SimpleGrid,
  Stack,
  Switch,
  Table,
  Text,
  TextInput,
  Textarea,
  Tooltip,
} from '@mantine/core'
import { IconPencil, IconPlus, IconTrash } from '@tabler/icons-react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { isInstallationAdmin, useMe } from '@/api/auth'
import { api, check, errorMessage, unwrap, type RelType, type Schemas } from '@/api/client'
import { keys, useKinds, useRelTypes } from '@/api/queries'
import { Page } from '@/components/AppShell'
import { ConfirmModal, PageHeader } from '@/components/common'
import { ErrorState, LoadingRows } from '@/components/States'
import { EvidenceBadge } from '@/components/StatusBadge'
import { useTitle } from '@/lib/appStore'

const IMPACT: Record<string, string> = {
  forward: 'from depends on to (a failing target impacts the source)',
  reverse: 'to depends on from (a failing source impacts the target)',
  none: 'no impact propagation',
}

function RelTypeModal({ t, opened, onClose }: { t: RelType | null; opened: boolean; onClose: () => void }) {
  const qc = useQueryClient()
  const kinds = useKinds()
  const [v, setV] = useState<Schemas['RelTypeInput']>(() =>
    t
      ? {
          name: t.name,
          title: t.title,
          inverse_name: t.inverse_name,
          description: t.description,
          color: t.color,
          impact: t.impact,
          default_evidence: t.default_evidence,
          directed: t.directed,
          cardinality: t.cardinality,
          from_kinds: t.from_kinds,
          to_kinds: t.to_kinds,
        }
      : {
          name: '',
          title: '',
          inverse_name: '',
          description: '',
          color: '#868e96',
          impact: 'none',
          default_evidence: 'structural',
          directed: true,
          cardinality: 'many_to_many',
          from_kinds: [],
          to_kinds: [],
        },
  )
  const save = useMutation({
    mutationFn: async () =>
      t
        ? unwrap(
            await api.PUT('/relationship-types/{name}', {
              params: { path: { name: t.name }, header: { 'If-Match': String(t.resource_version) } },
              body: v,
            }),
          )
        : unwrap(await api.POST('/relationship-types', { body: v })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: keys.relTypes() })
      onClose()
    },
  })
  const kindOptions = (kinds.data ?? []).map((k) => ({ value: k.name, label: k.title }))
  return (
    <Modal opened={opened} onClose={onClose} title={t ? `Edit ${t.title}` : 'New relationship type'} size="lg" centered>
      <Stack gap="sm">
        <SimpleGrid cols={2}>
          <TextInput
            label="Name"
            required
            value={v.name ?? ''}
            onChange={(e) => setV({ ...v, name: e.currentTarget.value })}
            disabled={!!t}
            description="lower_snake_case"
          />
          <TextInput
            label="Title"
            required
            value={v.title}
            onChange={(e) => setV({ ...v, title: e.currentTarget.value })}
          />
          <TextInput
            label="Inverse name"
            value={v.inverse_name ?? ''}
            onChange={(e) => setV({ ...v, inverse_name: e.currentTarget.value })}
            description="Reading direction from the target, e.g. hosts"
          />
          <ColorInput
            label="Map colour"
            value={v.color ?? ''}
            onChange={(c) => setV({ ...v, color: c })}
            format="hex"
          />
          <Select
            label="Impact"
            data={Object.entries(IMPACT).map(([value, label]) => ({ value, label: `${value} — ${label}` }))}
            value={v.impact ?? 'none'}
            onChange={(x) => setV({ ...v, impact: x ?? 'none' })}
            allowDeselect={false}
          />
          <Select
            label="Default evidence"
            data={['structural', 'observed']}
            value={v.default_evidence ?? 'structural'}
            onChange={(x) => setV({ ...v, default_evidence: x ?? 'structural' })}
            allowDeselect={false}
          />
          <MultiSelect
            label="From kinds"
            description="Empty = any"
            data={kindOptions}
            value={v.from_kinds ?? []}
            onChange={(x) => setV({ ...v, from_kinds: x })}
            searchable
            clearable
          />
          <MultiSelect
            label="To kinds"
            description="Empty = any"
            data={kindOptions}
            value={v.to_kinds ?? []}
            onChange={(x) => setV({ ...v, to_kinds: x })}
            searchable
            clearable
          />
        </SimpleGrid>
        <Textarea
          label="Description"
          value={v.description ?? ''}
          onChange={(e) => setV({ ...v, description: e.currentTarget.value })}
          autosize
          minRows={2}
        />
        <Switch
          label="Directed"
          checked={v.directed ?? true}
          onChange={(e) => setV({ ...v, directed: e.currentTarget.checked })}
        />
        {save.isError && (
          <Alert color="red" role="alert">
            {errorMessage(save.error)}
          </Alert>
        )}
        <Group justify="flex-end">
          <Button variant="default" onClick={onClose}>
            Cancel
          </Button>
          <Button onClick={() => save.mutate()} loading={save.isPending} disabled={!v.title || !v.name}>
            Save
          </Button>
        </Group>
      </Stack>
    </Modal>
  )
}

export function RelTypesPage() {
  useTitle('Relationship types')
  const qc = useQueryClient()
  const types = useRelTypes()
  const admin = isInstallationAdmin(useMe().data)
  const [edit, setEdit] = useState<RelType | null | 'new'>(null)
  const [del, setDel] = useState<RelType | null>(null)
  const remove = useMutation({
    mutationFn: async (t: RelType) =>
      check(await api.DELETE('/relationship-types/{name}', { params: { path: { name: t.name } } })),
    onSuccess: () => {
      setDel(null)
      void qc.invalidateQueries({ queryKey: keys.relTypes() })
    },
  })
  return (
    <Page>
      <PageHeader
        title="Relationship types"
        description="How entities relate. Impact direction drives impact analysis; the colour is the default edge colour on the map (views can override it)."
        actions={
          admin && (
            <Button leftSection={<IconPlus size={14} />} onClick={() => setEdit('new')}>
              New type
            </Button>
          )
        }
      />
      {types.isPending ? (
        <LoadingRows />
      ) : types.isError ? (
        <ErrorState error={types.error} what="relationship types" />
      ) : (
        <Card p={0}>
          <Table.ScrollContainer minWidth={900}>
            <Table fz="sm" verticalSpacing={6} aria-label="Relationship types">
              <Table.Thead>
                <Table.Tr>
                  <Table.Th>Type</Table.Th>
                  <Table.Th>Impact</Table.Th>
                  <Table.Th>Evidence</Table.Th>
                  <Table.Th>Kinds</Table.Th>
                  <Table.Th />
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {types.data.map((t) => (
                  <Table.Tr key={t.name}>
                    <Table.Td>
                      <Group gap={8} wrap="nowrap">
                        <span
                          style={{
                            width: 22,
                            height: 4,
                            borderRadius: 2,
                            background: t.color || '#868e96',
                            flexShrink: 0,
                          }}
                          aria-hidden
                        />
                        <div>
                          <Text size="sm" fw={500}>
                            {t.title}{' '}
                            {!t.builtin && (
                              <Badge size="xs" variant="outline" component="span">
                                custom
                              </Badge>
                            )}
                          </Text>
                          <Text size="xs" c="dimmed" className="mono">
                            {t.name}
                            {t.inverse_name ? ` ⇄ ${t.inverse_name}` : ''}
                          </Text>
                          {t.description && (
                            <Text size="xs" c="dimmed">
                              {t.description}
                            </Text>
                          )}
                        </div>
                      </Group>
                    </Table.Td>
                    <Table.Td>
                      <Tooltip label={IMPACT[t.impact] ?? t.impact}>
                        <Badge size="sm" variant="light" color={t.impact === 'none' ? 'gray' : 'deed'}>
                          {t.impact}
                        </Badge>
                      </Tooltip>
                    </Table.Td>
                    <Table.Td>
                      <EvidenceBadge cls={t.default_evidence} size="xs" />
                    </Table.Td>
                    <Table.Td>
                      <Text size="xs">
                        {t.from_kinds.length ? t.from_kinds.join(', ') : 'any'} →{' '}
                        {t.to_kinds.length ? t.to_kinds.join(', ') : 'any'}
                      </Text>
                    </Table.Td>
                    <Table.Td ta="right">
                      {admin && !t.builtin && (
                        <Group gap={4} justify="flex-end">
                          <ActionIcon aria-label={`Edit ${t.title}`} onClick={() => setEdit(t)}>
                            <IconPencil size={14} />
                          </ActionIcon>
                          <ActionIcon color="red" aria-label={`Delete ${t.title}`} onClick={() => setDel(t)}>
                            <IconTrash size={14} />
                          </ActionIcon>
                        </Group>
                      )}
                    </Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
        </Card>
      )}
      {edit !== null && <RelTypeModal t={edit === 'new' ? null : edit} opened onClose={() => setEdit(null)} />}
      <ConfirmModal
        opened={del !== null}
        title="Delete relationship type"
        danger
        confirmLabel="Delete"
        pending={remove.isPending}
        onClose={() => setDel(null)}
        onConfirm={() => del && remove.mutate(del)}
      >
        Only unused custom types can be deleted.
        {remove.isError && (
          <Text c="red" size="sm" mt="xs">
            {errorMessage(remove.error)}
          </Text>
        )}
      </ConfirmModal>
    </Page>
  )
}
