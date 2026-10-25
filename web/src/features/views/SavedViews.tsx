import {
  ActionIcon,
  Badge,
  Button,
  Group,
  Menu,
  Modal,
  Select,
  Stack,
  Switch,
  Text,
  TextInput,
  Textarea,
  Tooltip,
} from '@mantine/core'
import { notifications } from '@mantine/notifications'
import { IconBookmark, IconBookmarkPlus, IconDeviceFloppy, IconShare, IconTrash, IconUsers } from '@tabler/icons-react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { projectAtLeast } from '@/api/auth'
import { api, check, errorMessage, unwrap, type View } from '@/api/client'
import { keys, useProjects } from '@/api/queries'
import { ConfirmModal } from '@/components/common'

type ViewKind = 'inventory' | 'map' | 'dashboard'

export function useViews(kind: ViewKind) {
  return useQuery({
    queryKey: keys.views(kind),
    queryFn: async () => unwrap(await api.GET('/views', { params: { query: { kind } } })),
    staleTime: 30_000,
  })
}

/**
 * Saved views (server-side, stable IDs): list, apply, save the current state as a new view, update the active view,
 * share it with a project, delete. `state` is client-defined (filters, columns, map options); map views also save
 * `layout` (node positions).
 */
export function SavedViewsMenu({
  kind,
  activeId,
  currentState,
  currentLayout,
  onApply,
}: {
  kind: ViewKind
  activeId?: string
  currentState: Record<string, unknown>
  currentLayout?: () => Record<string, { x: number; y: number }>
  onApply: (view: View | null) => void
}) {
  const qc = useQueryClient()
  const views = useViews(kind)
  const projects = useProjects()
  const [saveOpen, setSaveOpen] = useState(false)
  const [deleteView, setDeleteView] = useState<View | null>(null)
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [shared, setShared] = useState(false)
  const [project, setProject] = useState<string | null>(null)
  const active = views.data?.find((v) => v.id === activeId)

  const invalidate = () => qc.invalidateQueries({ queryKey: ['views', kind] })

  const create = useMutation({
    mutationFn: async () =>
      unwrap(
        await api.POST('/views', {
          body: {
            kind,
            name: name.trim(),
            description: description.trim(),
            shared,
            ...(project ? { project } : {}),
            state: currentState,
            ...(currentLayout ? { layout: currentLayout() } : {}),
          },
        }),
      ),
    onSuccess: (v) => {
      void invalidate()
      setSaveOpen(false)
      notifications.show({ color: 'green', message: `Saved view “${v.name}”` })
      onApply(v)
    },
  })

  const update = useMutation({
    mutationFn: async (v: View) =>
      unwrap(
        await api.PUT('/views/{id}', {
          params: { path: { id: v.id }, header: { 'If-Match': String(v.resource_version) } },
          body: {
            kind,
            name: v.name,
            description: v.description,
            shared: v.shared,
            ...(v.project_id ? { project: v.project_id } : {}),
            state: currentState,
            ...(currentLayout ? { layout: currentLayout() } : {}),
          },
        }),
      ),
    onSuccess: (v) => {
      void invalidate()
      notifications.show({ color: 'green', message: `Updated view “${v.name}”` })
    },
    onError: (e) => notifications.show({ color: 'red', title: 'Could not update the view', message: errorMessage(e) }),
  })

  const remove = useMutation({
    mutationFn: async (v: View) => check(await api.DELETE('/views/{id}', { params: { path: { id: v.id } } })),
    onSuccess: () => {
      void invalidate()
      setDeleteView(null)
      if (deleteView?.id === activeId) onApply(null)
    },
    onError: (e) => notifications.show({ color: 'red', title: 'Could not delete the view', message: errorMessage(e) }),
  })

  const shareable = (projects.data ?? []).filter((p) => projectAtLeast(p, 'viewer'))

  return (
    <>
      <Group gap={4} wrap="nowrap">
        <Menu position="bottom-end" width={300} withinPortal>
          <Menu.Target>
            <Button variant="default" leftSection={<IconBookmark size={14} />} data-testid="views-menu">
              {active ? active.name : 'Saved views'}
            </Button>
          </Menu.Target>
          <Menu.Dropdown>
            <Menu.Label>Saved {kind} views</Menu.Label>
            {views.isPending && <Menu.Item disabled>Loading…</Menu.Item>}
            {views.isError && <Menu.Item disabled>Could not load views</Menu.Item>}
            {views.data?.length === 0 && <Menu.Item disabled>No saved views yet</Menu.Item>}
            {views.data?.map((v) => (
              <Menu.Item
                key={v.id}
                onClick={() => onApply(v)}
                rightSection={
                  <Group gap={4} wrap="nowrap">
                    {v.shared && (
                      <Tooltip label="Shared with the project">
                        <IconUsers size={12} aria-label="shared" />
                      </Tooltip>
                    )}
                    {v.editable && (
                      <ActionIcon
                        size="xs"
                        color="red"
                        aria-label={`Delete view ${v.name}`}
                        onClick={(e) => {
                          e.stopPropagation()
                          setDeleteView(v)
                        }}
                      >
                        <IconTrash size={12} />
                      </ActionIcon>
                    )}
                  </Group>
                }
              >
                <Text size="sm" fw={v.id === activeId ? 600 : 400}>
                  {v.name}
                </Text>
                {v.description && (
                  <Text size="xs" c="dimmed" lineClamp={1}>
                    {v.description}
                  </Text>
                )}
              </Menu.Item>
            ))}
            {active && (
              <>
                <Menu.Divider />
                <Menu.Item onClick={() => onApply(null)}>Clear active view</Menu.Item>
              </>
            )}
          </Menu.Dropdown>
        </Menu>
        {active?.editable && (
          <Tooltip label={`Save current state into “${active.name}”`}>
            <ActionIcon
              variant="default"
              size="input-xs"
              aria-label="Update saved view"
              loading={update.isPending}
              onClick={() => update.mutate(active)}
            >
              <IconDeviceFloppy size={14} />
            </ActionIcon>
          </Tooltip>
        )}
        <Tooltip label="Save as a new view">
          <ActionIcon
            variant="default"
            size="input-xs"
            aria-label="Save as a new view"
            data-testid="save-view"
            onClick={() => {
              setName('')
              setDescription('')
              setShared(false)
              setProject(null)
              create.reset()
              setSaveOpen(true)
            }}
          >
            <IconBookmarkPlus size={14} />
          </ActionIcon>
        </Tooltip>
      </Group>
      <Modal opened={saveOpen} onClose={() => setSaveOpen(false)} title={`Save ${kind} view`} centered>
        <form
          onSubmit={(e) => {
            e.preventDefault()
            create.mutate()
          }}
        >
          <Stack gap="sm">
            <TextInput
              label="Name"
              required
              maxLength={120}
              value={name}
              onChange={(e) => setName(e.currentTarget.value)}
              data-autofocus
            />
            <Textarea
              label="Description"
              autosize
              minRows={2}
              maxLength={1000}
              value={description}
              onChange={(e) => setDescription(e.currentTarget.value)}
            />
            <Switch
              label="Share with a project"
              description="Members of the project can open (not change) the view."
              checked={shared}
              onChange={(e) => setShared(e.currentTarget.checked)}
            />
            {shared && (
              <Select
                label="Project"
                required
                data={shareable.map((p) => ({ value: p.id, label: p.display_name || p.name }))}
                value={project}
                onChange={setProject}
                leftSection={<IconShare size={14} />}
              />
            )}
            <Text size="xs" c="dimmed">
              Saves: {Object.keys(currentState).length ? Object.keys(currentState).join(', ') : 'default state'}
              {currentLayout ? ' and node positions' : ''}.
            </Text>
            {create.isError && (
              <Text size="sm" c="red" role="alert">
                {errorMessage(create.error)}
              </Text>
            )}
            <Group justify="flex-end">
              <Button variant="default" onClick={() => setSaveOpen(false)}>
                Cancel
              </Button>
              <Button type="submit" loading={create.isPending} disabled={!name.trim() || (shared && !project)}>
                Save view
              </Button>
            </Group>
          </Stack>
        </form>
      </Modal>
      <ConfirmModal
        opened={deleteView !== null}
        title="Delete saved view"
        danger
        confirmLabel="Delete"
        pending={remove.isPending}
        onClose={() => setDeleteView(null)}
        onConfirm={() => deleteView && remove.mutate(deleteView)}
      >
        Delete “{deleteView?.name}”? {deleteView?.shared && <Badge size="xs">shared</Badge>} Others who use it lose
        access too.
      </ConfirmModal>
    </>
  )
}
