import {
  ActionIcon,
  Alert,
  Badge,
  Button,
  Card,
  Group,
  Modal,
  Select,
  SimpleGrid,
  Stack,
  Switch,
  Table,
  Text,
  TextInput,
  Textarea,
  Title,
} from '@mantine/core'
import { notifications } from '@mantine/notifications'
import { IconPlus, IconTrash } from '@tabler/icons-react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { useState } from 'react'
import {
  canReadAllAudit,
  isInstallationAdmin,
  projectAtLeast,
  projectRoleInfo,
  useMe,
  type ProjectRole,
} from '@/api/auth'
import { api, check, errorMessage, unwrap, type ProjectView } from '@/api/client'
import { keys, useProjects } from '@/api/queries'
import { Page } from '@/components/AppShell'
import { ConfirmModal, Field, Mono, PageHeader, TimeAgo } from '@/components/common'
import { AnchorLink } from '@/components/links'
import { ErrorState, LoadingRows } from '@/components/States'
import { useTitle } from '@/lib/appStore'

function ProjectModal({ p, opened, onClose }: { p: ProjectView | null; opened: boolean; onClose: () => void }) {
  const qc = useQueryClient()
  const [name, setName] = useState(p?.name ?? '')
  const [displayName, setDisplayName] = useState(p?.display_name ?? '')
  const [description, setDescription] = useState(p?.description ?? '')
  const [allowHttp, setAllowHttp] = useState(p?.allow_http_links ?? false)
  const [synthetic, setSynthetic] = useState(p?.synthetic ?? false)
  const save = useMutation({
    mutationFn: async () => {
      const body = { display_name: displayName, description, allow_http_links: allowHttp, synthetic }
      return p
        ? unwrap(
            await api.PATCH('/projects/{project}', {
              params: { path: { project: p.id }, header: { 'If-Match': String(p.resource_version) } },
              body,
            }),
          )
        : unwrap(await api.POST('/projects', { body: { ...body, name } }))
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: keys.projects() })
      onClose()
    },
  })
  const nameOk = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/.test(name)
  return (
    <Modal opened={opened} onClose={onClose} title={p ? `Edit ${p.display_name || p.name}` : 'New project'} centered>
      <form
        onSubmit={(e) => {
          e.preventDefault()
          save.mutate()
        }}
      >
        <Stack gap="sm">
          <TextInput
            label="Name"
            required
            value={name}
            onChange={(e) => setName(e.currentTarget.value)}
            disabled={!!p}
            description="Lowercase slug; used in references and Terraform."
            error={!p && name && !nameOk ? 'Lowercase letters, digits and dashes' : undefined}
            data-testid="project-name"
          />
          <TextInput label="Display name" value={displayName} onChange={(e) => setDisplayName(e.currentTarget.value)} />
          <Textarea
            label="Description"
            value={description}
            onChange={(e) => setDescription(e.currentTarget.value)}
            autosize
            minRows={2}
          />
          <Switch
            label="Allow http:// links"
            description="Links must be https unless the project allows plain http."
            checked={allowHttp}
            onChange={(e) => setAllowHttp(e.currentTarget.checked)}
          />
          <Switch
            label="Synthetic demo data"
            description="Marks every view that includes this project with a “Synthetic demo data” banner."
            checked={synthetic}
            onChange={(e) => setSynthetic(e.currentTarget.checked)}
          />
          {save.isError && <Alert color="red">{errorMessage(save.error)}</Alert>}
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" loading={save.isPending} disabled={!p && !nameOk}>
              {p ? 'Save' : 'Create project'}
            </Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  )
}

export function ProjectsPage() {
  useTitle('Projects')
  const projects = useProjects()
  const admin = isInstallationAdmin(useMe().data)
  const [edit, setEdit] = useState<ProjectView | null | 'new'>(null)
  return (
    <Page>
      <PageHeader
        title="Projects"
        description="Projects scope access: members get viewer, editor or admin roles. Installation owners and admins act as admins everywhere; auditors read everything."
        actions={
          admin && (
            <Button leftSection={<IconPlus size={14} />} onClick={() => setEdit('new')} data-testid="new-project">
              New project
            </Button>
          )
        }
      />
      {projects.isPending ? (
        <LoadingRows />
      ) : projects.isError ? (
        <ErrorState error={projects.error} what="projects" />
      ) : (
        <Card p={0}>
          <Table fz="sm" verticalSpacing={6} aria-label="Projects">
            <Table.Thead>
              <Table.Tr>
                <Table.Th>Project</Table.Th>
                <Table.Th>Your role</Table.Th>
                <Table.Th>Created</Table.Th>
                <Table.Th />
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {projects.data.map((p) => (
                <Table.Tr key={p.id}>
                  <Table.Td>
                    <AnchorLink to="/settings/projects/$project" params={{ project: p.name }} size="sm" fw={500}>
                      {p.display_name || p.name}
                    </AnchorLink>
                    {p.synthetic && (
                      <Badge size="xs" color="yellow" variant="light" ml={6}>
                        synthetic
                      </Badge>
                    )}
                    <Text size="xs" c="dimmed">
                      <span className="mono">{p.name}</span>
                      {p.description ? ` · ${p.description}` : ''}
                    </Text>
                  </Table.Td>
                  <Table.Td>
                    <Badge
                      variant="light"
                      color={(projectRoleInfo as Partial<Record<string, { color: string }>>)[p.role]?.color ?? 'gray'}
                    >
                      {p.role}
                    </Badge>
                  </Table.Td>
                  <Table.Td>
                    <TimeAgo value={p.created_at} />
                  </Table.Td>
                  <Table.Td ta="right">
                    {projectAtLeast(p, 'admin') && (
                      <Button size="compact-xs" variant="default" onClick={() => setEdit(p)}>
                        Edit
                      </Button>
                    )}
                  </Table.Td>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        </Card>
      )}
      {edit !== null && <ProjectModal p={edit === 'new' ? null : edit} opened onClose={() => setEdit(null)} />}
    </Page>
  )
}

export function ProjectDetailPage({ project }: { project: string }) {
  const qc = useQueryClient()
  const navigate = useNavigate()
  const me = useMe().data
  const q = useQuery({
    queryKey: ['projects', project, 'detail'],
    queryFn: async () => unwrap(await api.GET('/projects/{project}', { params: { path: { project } } })),
  })
  const p = q.data
  useTitle(p ? p.display_name || p.name : 'Project')
  const members = useQuery({
    queryKey: keys.members(project),
    queryFn: async () => unwrap(await api.GET('/projects/{project}/members', { params: { path: { project } } })),
    enabled: !!p,
  })
  const users = useQuery({
    queryKey: keys.users(),
    queryFn: async () => unwrap(await api.GET('/users')),
    enabled: canReadAllAudit(me),
  })
  const [userId, setUserId] = useState<string | null>(null)
  const [role, setRole] = useState<ProjectRole>('viewer')
  const [del, setDel] = useState(false)
  const [force, setForce] = useState(false)
  const setMember = useMutation({
    mutationFn: async ({ user, r }: { user: string; r: ProjectRole }) =>
      check(
        await api.PUT('/projects/{project}/members/{user}', { params: { path: { project, user } }, body: { role: r } }),
      ),
    onSuccess: () => {
      setUserId(null)
      void qc.invalidateQueries({ queryKey: keys.members(project) })
    },
    onError: (e) =>
      notifications.show({ color: 'red', title: 'Could not change the membership', message: errorMessage(e) }),
  })
  const removeMember = useMutation({
    mutationFn: async (user: string) =>
      check(await api.DELETE('/projects/{project}/members/{user}', { params: { path: { project, user } } })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: keys.members(project) }),
    onError: (e) =>
      notifications.show({ color: 'red', title: 'Could not remove the member', message: errorMessage(e) }),
  })
  const remove = useMutation({
    mutationFn: async () =>
      check(await api.DELETE('/projects/{project}', { params: { path: { project }, query: { force } } })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: keys.projects() })
      void navigate({ to: '/settings/projects' })
    },
  })
  if (q.isPending)
    return (
      <Page>
        <LoadingRows />
      </Page>
    )
  if (!p)
    return (
      <Page>
        <ErrorState error={q.error} what="this project" />
      </Page>
    )
  const admin = projectAtLeast(p, 'admin')
  const roleOptions = Object.entries(projectRoleInfo).map(([value, info]) => ({
    value,
    label: `${value} — ${info.description}`,
  }))
  return (
    <Page>
      <PageHeader
        title={p.display_name || p.name}
        description={p.description}
        actions={
          admin &&
          isInstallationAdmin(me) && (
            <Button color="red" variant="subtle" leftSection={<IconTrash size={14} />} onClick={() => setDel(true)}>
              Delete project
            </Button>
          )
        }
      />
      <SimpleGrid cols={{ base: 1, md: 2 }} mb="md">
        <Card>
          <Field label="Name">
            <Mono>{p.name}</Mono>
          </Field>
          <Field label="ID">
            <Mono>{p.id}</Mono>
          </Field>
          <Field label="Your role">{p.role}</Field>
          <Field label="http links">{p.allow_http_links ? 'allowed' : 'https only'}</Field>
          <Field label="Synthetic">{p.synthetic ? 'yes — demo data' : 'no'}</Field>
        </Card>
        <Card>
          <Title order={2} fz={14} mb="xs">
            Shortcuts
          </Title>
          <Stack gap={4}>
            <AnchorLink to="/inventory" search={{ project: p.name }} size="sm">
              Inventory of this project
            </AnchorLink>
            <AnchorLink to="/map" search={{ group: 'kind', project: p.id }} size="sm">
              Map of this project
            </AnchorLink>
            <AnchorLink to="/settings/audit" search={{ project: p.id }} size="sm">
              Audit records
            </AnchorLink>
          </Stack>
        </Card>
      </SimpleGrid>
      <Title order={2} fz={16} mb="xs">
        Members
      </Title>
      {admin && (
        <Card mb="sm">
          <Group align="flex-end" gap="xs">
            {users.data ? (
              <Select
                label="User"
                searchable
                data={users.data
                  .filter((u) => u.status === 'active')
                  .map((u) => ({ value: u.id, label: `${u.display_name}${u.email ? ` (${u.email})` : ''}` }))}
                value={userId}
                onChange={setUserId}
                w={320}
              />
            ) : (
              <TextInput
                label="User ID"
                value={userId ?? ''}
                onChange={(e) => setUserId(e.currentTarget.value || null)}
                w={320}
                description="Ask an installation admin for the user ID."
              />
            )}
            <Select
              label="Role"
              data={roleOptions}
              value={role}
              onChange={(v) => setRole((v ?? 'viewer') as ProjectRole)}
              w={360}
              allowDeselect={false}
            />
            <Button
              disabled={!userId}
              loading={setMember.isPending}
              onClick={() => userId && setMember.mutate({ user: userId, r: role })}
            >
              Add or update
            </Button>
          </Group>
        </Card>
      )}
      {members.isPending ? (
        <LoadingRows rows={3} />
      ) : members.isError ? (
        <ErrorState error={members.error} what="members" />
      ) : (
        <Card p={0}>
          <Table fz="sm" aria-label="Members">
            <Table.Thead>
              <Table.Tr>
                <Table.Th>Member</Table.Th>
                <Table.Th>Role</Table.Th>
                <Table.Th>Granted by</Table.Th>
                <Table.Th />
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {members.data.length === 0 && (
                <Table.Tr>
                  <Table.Td colSpan={4}>
                    <Text size="sm" c="dimmed">
                      No explicit members. Installation owners, admins and auditors have implicit access.
                    </Text>
                  </Table.Td>
                </Table.Tr>
              )}
              {members.data.map((m) => (
                <Table.Tr key={m.user_id}>
                  <Table.Td>
                    {m.display_name}
                    <Text size="xs" c="dimmed">
                      {m.email}
                    </Text>
                  </Table.Td>
                  <Table.Td>
                    {admin && m.role_source === 'explicit' ? (
                      <Select
                        aria-label={`Role of ${m.display_name}`}
                        data={Object.keys(projectRoleInfo)}
                        value={m.role}
                        onChange={(v) => v && setMember.mutate({ user: m.user_id, r: v as ProjectRole })}
                        w={130}
                        allowDeselect={false}
                      />
                    ) : (
                      <Badge variant="light">{m.role}</Badge>
                    )}
                  </Table.Td>
                  <Table.Td>{m.role_source}</Table.Td>
                  <Table.Td ta="right">
                    {admin && m.role_source === 'explicit' && (
                      <ActionIcon
                        color="red"
                        aria-label={`Remove ${m.display_name}`}
                        onClick={() => removeMember.mutate(m.user_id)}
                      >
                        <IconTrash size={14} />
                      </ActionIcon>
                    )}
                  </Table.Td>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        </Card>
      )}
      <ConfirmModal
        opened={del}
        title="Delete project"
        danger
        confirmLabel="Delete project"
        typeToConfirm={p.name}
        pending={remove.isPending}
        onClose={() => setDel(false)}
        onConfirm={() => remove.mutate()}
      >
        <Stack gap="xs">
          <Text size="sm">Deleting a project removes its sources, memberships and views.</Text>
          <Switch
            label="Delete even if it still has entities"
            checked={force}
            onChange={(e) => setForce(e.currentTarget.checked)}
          />
          {remove.isError && (
            <Text c="red" size="sm">
              {errorMessage(remove.error)}
            </Text>
          )}
        </Stack>
      </ConfirmModal>
    </Page>
  )
}
