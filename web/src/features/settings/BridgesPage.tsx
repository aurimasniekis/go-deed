import {
  ActionIcon,
  Alert,
  Button,
  Card,
  Group,
  Modal,
  MultiSelect,
  Select,
  Stack,
  Table,
  Text,
  TextInput,
} from '@mantine/core'
import { IconArrowRight, IconPlus, IconTrash } from '@tabler/icons-react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { anyProjectAtLeast, projectAtLeast } from '@/api/auth'
import { api, check, errorMessage, unwrap, type Bridge } from '@/api/client'
import { keys, useProjects, useRelTypes } from '@/api/queries'
import { Page } from '@/components/AppShell'
import { ConfirmModal, PageHeader, TimeAgo } from '@/components/common'
import { EmptyState, ErrorState, LoadingRows } from '@/components/States'
import { useTitle } from '@/lib/appStore'

export function BridgesPage() {
  useTitle('Bridges')
  const qc = useQueryClient()
  const projects = useProjects()
  const relTypes = useRelTypes()
  const bridges = useQuery({ queryKey: keys.bridges(), queryFn: async () => unwrap(await api.GET('/bridges')) })
  const [open, setOpen] = useState(false)
  const [from, setFrom] = useState<string | null>(null)
  const [to, setTo] = useState<string | null>(null)
  const [types, setTypes] = useState<string[]>([])
  const [description, setDescription] = useState('')
  const [del, setDel] = useState<Bridge | null>(null)
  const admins = (projects.data ?? []).filter((p) => projectAtLeast(p, 'admin'))
  const name = (id: string) => projects.data?.find((p) => p.id === id || p.name === id)?.display_name ?? id
  const create = useMutation({
    mutationFn: async () =>
      unwrap(
        await api.POST('/bridges', {
          body: { from_project: from ?? '', to_project: to ?? '', allowed_types: types, description },
        }),
      ),
    onSuccess: () => {
      setOpen(false)
      void qc.invalidateQueries({ queryKey: keys.bridges() })
    },
  })
  const remove = useMutation({
    mutationFn: async (b: Bridge) => check(await api.DELETE('/bridges/{id}', { params: { path: { id: b.id } } })),
    onSuccess: () => {
      setDel(null)
      void qc.invalidateQueries({ queryKey: keys.bridges() })
    },
  })
  return (
    <Page>
      <PageHeader
        title="Project bridges"
        description="Relationships may cross projects only through a bridge (admins of both projects declare it). On the map, bridged relationships are drawn as double lines."
        actions={
          anyProjectAtLeast(projects.data, 'admin') && (
            <Button leftSection={<IconPlus size={14} />} onClick={() => setOpen(true)}>
              New bridge
            </Button>
          )
        }
      />
      {bridges.isPending ? (
        <LoadingRows />
      ) : bridges.isError ? (
        <ErrorState error={bridges.error} what="bridges" />
      ) : bridges.data.length === 0 ? (
        <EmptyState title="No bridges">
          Projects are isolated until a bridge allows relationships between them.
        </EmptyState>
      ) : (
        <Card p={0}>
          <Table fz="sm" verticalSpacing={6} aria-label="Bridges">
            <Table.Thead>
              <Table.Tr>
                <Table.Th>From → to</Table.Th>
                <Table.Th>Allowed relationship types</Table.Th>
                <Table.Th>Description</Table.Th>
                <Table.Th>Created</Table.Th>
                <Table.Th />
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {bridges.data.map((b) => (
                <Table.Tr key={b.id}>
                  <Table.Td>
                    <Group gap={6} wrap="nowrap">
                      {name(b.from_project)} <IconArrowRight size={14} aria-label="to" /> {name(b.to_project)}
                    </Group>
                  </Table.Td>
                  <Table.Td>{b.allowed_types.length ? b.allowed_types.join(', ') : 'any'}</Table.Td>
                  <Table.Td>{b.description}</Table.Td>
                  <Table.Td>
                    <TimeAgo value={b.created_at} />
                  </Table.Td>
                  <Table.Td ta="right">
                    <ActionIcon color="red" aria-label="Delete bridge" onClick={() => setDel(b)}>
                      <IconTrash size={14} />
                    </ActionIcon>
                  </Table.Td>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        </Card>
      )}
      <Modal opened={open} onClose={() => setOpen(false)} title="New bridge" centered>
        <Stack gap="sm">
          <Select
            label="From project"
            required
            data={admins.map((p) => ({ value: p.id, label: p.display_name || p.name }))}
            value={from}
            onChange={setFrom}
          />
          <Select
            label="To project"
            required
            data={admins.filter((p) => p.id !== from).map((p) => ({ value: p.id, label: p.display_name || p.name }))}
            value={to}
            onChange={setTo}
          />
          <MultiSelect
            label="Allowed relationship types"
            description="Empty = any type"
            data={(relTypes.data ?? []).map((t) => ({ value: t.name, label: t.title }))}
            value={types}
            onChange={setTypes}
            searchable
            clearable
          />
          <TextInput label="Description" value={description} onChange={(e) => setDescription(e.currentTarget.value)} />
          {create.isError && <Alert color="red">{errorMessage(create.error)}</Alert>}
          <Group justify="flex-end">
            <Button variant="default" onClick={() => setOpen(false)}>
              Cancel
            </Button>
            <Button loading={create.isPending} disabled={!from || !to} onClick={() => create.mutate()}>
              Create bridge
            </Button>
          </Group>
        </Stack>
      </Modal>
      <ConfirmModal
        opened={del !== null}
        title="Delete bridge"
        danger
        confirmLabel="Delete"
        pending={remove.isPending}
        onClose={() => setDel(null)}
        onConfirm={() => del && remove.mutate(del)}
      >
        <Text size="sm">
          Relationships that relied on it stay but are flagged by findings until removed or re-bridged.
        </Text>
        {remove.isError && (
          <Text c="red" size="sm">
            {errorMessage(remove.error)}
          </Text>
        )}
      </ConfirmModal>
    </Page>
  )
}
