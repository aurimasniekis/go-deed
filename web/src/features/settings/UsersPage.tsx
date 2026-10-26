import {
  Alert,
  Badge,
  Button,
  Card,
  Group,
  Menu,
  Modal,
  NumberInput,
  Select,
  Stack,
  Table,
  Text,
  TextInput,
} from '@mantine/core'
import { notifications } from '@mantine/notifications'
import { IconChevronDown, IconMailPlus } from '@tabler/icons-react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { installationRoleInfo, isInstallationAdmin, useMe, type InstallationRole } from '@/api/auth'
import { api, check, errorMessage, unwrap, type User } from '@/api/client'
import { keys } from '@/api/queries'
import { Page } from '@/components/AppShell'
import { ConfirmModal, OneTimeSecret, PageHeader, TimeAgo } from '@/components/common'
import { ErrorState, LoadingRows } from '@/components/States'
import { useTitle } from '@/lib/appStore'
import { ServiceAccountsCard } from './ServiceAccounts'

type Role = InstallationRole | ''

export function UsersPage() {
  useTitle('Users')
  const qc = useQueryClient()
  const me = useMe().data
  const admin = isInstallationAdmin(me)
  const owner = me?.installation_role === 'owner'
  const users = useQuery({ queryKey: keys.users(), queryFn: async () => unwrap(await api.GET('/users')) })
  const [inviteOpen, setInviteOpen] = useState(false)
  const [email, setEmail] = useState('')
  const [role, setRole] = useState<Role>('')
  const [hours, setHours] = useState(72)
  const [link, setLink] = useState<{ title: string; url: string } | null>(null)
  const [del, setDel] = useState<User | null>(null)
  const invalidate = () => void qc.invalidateQueries({ queryKey: keys.users() })
  const invite = useMutation({
    mutationFn: async () =>
      unwrap(
        await api.POST('/users/invites', {
          body: { email: email.trim(), expires_in_hours: hours, installation_role: role },
        }),
      ),
    onSuccess: (r) => {
      setInviteOpen(false)
      setEmail('')
      setLink({ title: 'Invitation link', url: r.setup_link })
      invalidate()
    },
  })
  const update = useMutation({
    mutationFn: async ({ u, body }: { u: User; body: { installation_role?: Role; status?: 'active' | 'disabled' } }) =>
      unwrap(await api.PATCH('/users/{id}', { params: { path: { id: u.id } }, body })),
    onSuccess: invalidate,
    onError: (e) => notifications.show({ color: 'red', title: 'Could not update the user', message: errorMessage(e) }),
  })
  const reset = useMutation({
    mutationFn: async (u: User) => unwrap(await api.POST('/users/{id}/reset-link', { params: { path: { id: u.id } } })),
    onSuccess: (r) => setLink({ title: 'Password reset link', url: r.setup_link }),
    onError: (e) =>
      notifications.show({ color: 'red', title: 'Could not issue a reset link', message: errorMessage(e) }),
  })
  const remove = useMutation({
    mutationFn: async (u: User) => check(await api.DELETE('/users/{id}', { params: { path: { id: u.id } } })),
    onSuccess: () => {
      setDel(null)
      invalidate()
    },
  })
  const roleOptions = [
    { value: '', label: 'No installation role (project memberships only)' },
    ...Object.entries(installationRoleInfo)
      .filter(([r]) => owner || r !== 'owner')
      .map(([value, info]) => ({ value, label: `${value} — ${info.description}` })),
  ]
  return (
    <Page>
      <PageHeader
        title="Users"
        description="People who can sign in. Invite with a single-use link; owners and admins must use two-factor authentication."
        actions={
          admin && (
            <Button
              leftSection={<IconMailPlus size={14} />}
              onClick={() => setInviteOpen(true)}
              data-testid="invite-user"
            >
              Invite user
            </Button>
          )
        }
      />
      {users.isPending ? (
        <LoadingRows />
      ) : users.isError ? (
        <ErrorState error={users.error} what="users" />
      ) : (
        <Card p={0}>
          <Table.ScrollContainer minWidth={860}>
            <Table fz="sm" verticalSpacing={6} aria-label="Users">
              <Table.Thead>
                <Table.Tr>
                  <Table.Th>User</Table.Th>
                  <Table.Th>Installation role</Table.Th>
                  <Table.Th>Sign-in</Table.Th>
                  <Table.Th>Last sign-in</Table.Th>
                  <Table.Th>Status</Table.Th>
                  <Table.Th />
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {users.data.map((u) => (
                  <Table.Tr key={u.id}>
                    <Table.Td>
                      <Text size="sm" fw={500}>
                        {u.display_name}{' '}
                        {u.id === me?.user_id && (
                          <Badge size="xs" component="span">
                            you
                          </Badge>
                        )}
                      </Text>
                      <Text size="xs" c="dimmed">
                        {u.email}
                      </Text>
                    </Table.Td>
                    <Table.Td>
                      {admin && u.id !== me?.user_id && u.role_source !== 'sso' ? (
                        <Select
                          aria-label={`Installation role of ${u.display_name}`}
                          data={roleOptions.map((o) => ({ value: o.value, label: o.value || 'none' }))}
                          value={u.installation_role ?? ''}
                          onChange={(v) => update.mutate({ u, body: { installation_role: (v ?? '') as Role } })}
                          w={140}
                          allowDeselect={false}
                          disabled={u.installation_role === 'owner' && !owner}
                        />
                      ) : (
                        <Badge
                          variant="light"
                          color={
                            (installationRoleInfo as Partial<Record<string, { color: string }>>)[
                              u.installation_role ?? ''
                            ]?.color ?? 'gray'
                          }
                        >
                          {u.installation_role || 'none'}
                        </Badge>
                      )}
                      {u.role_source === 'sso' && (
                        <Text size="xs" c="dimmed">
                          managed by SSO group mapping
                        </Text>
                      )}
                    </Table.Td>
                    <Table.Td>
                      <Text size="xs">
                        {u.has_password ? 'password' : 'SSO only'} · {u.mfa_enrolled ? '2FA on' : '2FA off'}
                      </Text>
                    </Table.Td>
                    <Table.Td>
                      <TimeAgo value={u.last_login_at} />
                    </Table.Td>
                    <Table.Td>
                      <Badge variant="light" color={u.status === 'active' ? 'green' : 'gray'}>
                        {u.status}
                      </Badge>
                    </Table.Td>
                    <Table.Td ta="right">
                      {admin && u.id !== me?.user_id && (
                        <Menu position="bottom-end" withinPortal>
                          <Menu.Target>
                            <Button size="compact-xs" variant="default" rightSection={<IconChevronDown size={12} />}>
                              Actions
                            </Button>
                          </Menu.Target>
                          <Menu.Dropdown>
                            <Menu.Item onClick={() => reset.mutate(u)} disabled={!u.has_password && !u.email}>
                              Issue password reset link
                            </Menu.Item>
                            <Menu.Item
                              onClick={() =>
                                update.mutate({ u, body: { status: u.status === 'active' ? 'disabled' : 'active' } })
                              }
                            >
                              {u.status === 'active' ? 'Disable (revokes sessions and tokens)' : 'Enable'}
                            </Menu.Item>
                            <Menu.Divider />
                            <Menu.Item color="red" onClick={() => setDel(u)}>
                              Delete user
                            </Menu.Item>
                          </Menu.Dropdown>
                        </Menu>
                      )}
                    </Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
        </Card>
      )}
      <ServiceAccountsCard admin={admin} />
      <Modal opened={inviteOpen} onClose={() => setInviteOpen(false)} title="Invite a user" centered>
        <form
          onSubmit={(e) => {
            e.preventDefault()
            invite.mutate()
          }}
        >
          <Stack gap="sm">
            <TextInput
              label="Email"
              type="email"
              required
              value={email}
              onChange={(e) => setEmail(e.currentTarget.value)}
            />
            <Select
              label="Installation role"
              data={roleOptions}
              value={role}
              onChange={(v) => setRole((v ?? '') as Role)}
              allowDeselect={false}
            />
            <NumberInput
              label="Link valid for (hours)"
              min={1}
              max={720}
              value={hours}
              onChange={(v) => setHours(typeof v === 'number' ? v : 72)}
            />
            <Text size="xs" c="dimmed">
              Grant project roles after the user accepts, from the project’s page.
            </Text>
            {invite.isError && <Alert color="red">{errorMessage(invite.error)}</Alert>}
            <Group justify="flex-end">
              <Button variant="default" onClick={() => setInviteOpen(false)}>
                Cancel
              </Button>
              <Button type="submit" loading={invite.isPending}>
                Create invitation link
              </Button>
            </Group>
          </Stack>
        </form>
      </Modal>
      <OneTimeSecret
        opened={link !== null}
        title={link?.title ?? ''}
        intro="Send this single-use link to the person over a trusted channel. Deed does not email it."
        secret={link?.url ?? ''}
        onClose={() => setLink(null)}
      />
      <ConfirmModal
        opened={del !== null}
        title="Delete user"
        danger
        confirmLabel="Delete"
        typeToConfirm={del?.email ?? del?.display_name}
        pending={remove.isPending}
        onClose={() => setDel(null)}
        onConfirm={() => del && remove.mutate(del)}
      >
        Deleting removes the account, memberships, sessions and tokens. Audit records keep the user ID.
        {remove.isError && (
          <Text c="red" size="sm">
            {errorMessage(remove.error)}
          </Text>
        )}
      </ConfirmModal>
    </Page>
  )
}
