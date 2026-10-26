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
  Title,
} from '@mantine/core'
import { notifications } from '@mantine/notifications'
import { IconChevronDown, IconKey, IconRobot } from '@tabler/icons-react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { installationRoleInfo } from '@/api/auth'
import { api, check, errorMessage, unwrap, type Token, type User } from '@/api/client'
import { keys } from '@/api/queries'
import { ConfirmModal, OneTimeSecret, TimeAgo } from '@/components/common'
import { ErrorState, LoadingRows } from '@/components/States'

type ServiceRole = '' | 'admin' | 'auditor'

const roleOptions = [
  { value: '', label: 'none — project memberships only' },
  { value: 'auditor', label: `auditor — ${installationRoleInfo.auditor.description}` },
  { value: 'admin', label: `admin — ${installationRoleInfo.admin.description}` },
]

function RoleBadge({ role }: { role?: string }) {
  const info = (installationRoleInfo as Partial<Record<string, { color: string }>>)[role ?? '']
  return (
    <Badge variant="light" color={info?.color ?? 'gray'}>
      {role || 'none'}
    </Badge>
  )
}

/**
 * Service accounts: non-human users for automation (Terraform, CI). They cannot sign in; they use API tokens, bounded
 * by their installation role and project memberships (granted on each project's page).
 */
export function ServiceAccountsCard({ admin }: { admin: boolean }) {
  const qc = useQueryClient()
  const list = useQuery({
    queryKey: keys.serviceAccounts(),
    queryFn: async () => unwrap(await api.GET('/service-accounts')),
  })
  const [createOpen, setCreateOpen] = useState(false)
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [role, setRole] = useState<ServiceRole>('')
  const [tokensFor, setTokensFor] = useState<User | null>(null)
  const [del, setDel] = useState<User | null>(null)
  const invalidate = () => void qc.invalidateQueries({ queryKey: keys.serviceAccounts() })
  const create = useMutation({
    mutationFn: async () =>
      unwrap(
        await api.POST('/service-accounts', {
          body: {
            name: name.trim(),
            installation_role: role,
            ...(description.trim() ? { description: description.trim() } : {}),
          },
        }),
      ),
    onSuccess: (sa) => {
      setCreateOpen(false)
      setName('')
      setDescription('')
      invalidate()
      setTokensFor(sa)
    },
  })
  const update = useMutation({
    mutationFn: async ({
      sa,
      body,
    }: {
      sa: User
      body: { installation_role?: ServiceRole; status?: 'active' | 'disabled' }
    }) => unwrap(await api.PATCH('/service-accounts/{id}', { params: { path: { id: sa.id } }, body })),
    onSuccess: invalidate,
    onError: (e) =>
      notifications.show({ color: 'red', title: 'Could not update the service account', message: errorMessage(e) }),
  })
  const remove = useMutation({
    mutationFn: async (sa: User) =>
      check(await api.DELETE('/service-accounts/{id}', { params: { path: { id: sa.id } } })),
    onSuccess: () => {
      setDel(null)
      invalidate()
    },
  })
  return (
    <Card p={0} mt="sm" data-testid="service-accounts">
      <Group justify="space-between" p="sm" pb={0}>
        <div>
          <Title order={2} fz={14}>
            Service accounts
          </Title>
          <Text size="sm" c="dimmed">
            For automation such as Terraform. They cannot sign in: they use API tokens, with at most their installation
            role and project memberships. Grant project roles on each project’s page.
          </Text>
        </div>
        {admin && (
          <Button
            size="xs"
            leftSection={<IconRobot size={14} />}
            onClick={() => setCreateOpen(true)}
            data-testid="new-service-account"
          >
            New service account
          </Button>
        )}
      </Group>
      {list.isPending ? (
        <LoadingRows rows={2} />
      ) : list.isError ? (
        <ErrorState error={list.error} what="service accounts" />
      ) : list.data.length === 0 ? (
        <Text size="sm" c="dimmed" p="sm">
          No service accounts. The host command <code>deed admin token -service-account NAME -create</code> also creates
          one with its first token.
        </Text>
      ) : (
        <Table.ScrollContainer minWidth={760}>
          <Table fz="sm" verticalSpacing={6} aria-label="Service accounts" mt="xs">
            <Table.Thead>
              <Table.Tr>
                <Table.Th>Name</Table.Th>
                <Table.Th>Installation role</Table.Th>
                <Table.Th>Created</Table.Th>
                <Table.Th>Status</Table.Th>
                <Table.Th />
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {list.data.map((sa) => (
                <Table.Tr key={sa.id}>
                  <Table.Td>
                    <Text size="sm" fw={500} ff="monospace">
                      {sa.name}
                    </Text>
                    {sa.description && (
                      <Text size="xs" c="dimmed">
                        {sa.description}
                      </Text>
                    )}
                  </Table.Td>
                  <Table.Td>
                    {admin ? (
                      <Select
                        aria-label={`Installation role of ${sa.name ?? sa.id}`}
                        data={roleOptions.map((o) => ({ value: o.value, label: o.value || 'none' }))}
                        value={sa.installation_role ?? ''}
                        onChange={(v) => update.mutate({ sa, body: { installation_role: (v ?? '') as ServiceRole } })}
                        w={140}
                        allowDeselect={false}
                      />
                    ) : (
                      <RoleBadge role={sa.installation_role} />
                    )}
                  </Table.Td>
                  <Table.Td>
                    <TimeAgo value={sa.created_at} />
                  </Table.Td>
                  <Table.Td>
                    <Badge variant="light" color={sa.status === 'active' ? 'green' : 'gray'}>
                      {sa.status}
                    </Badge>
                  </Table.Td>
                  <Table.Td ta="right">
                    <Group gap={6} justify="flex-end" wrap="nowrap">
                      <Button
                        size="compact-xs"
                        variant="default"
                        leftSection={<IconKey size={12} />}
                        onClick={() => setTokensFor(sa)}
                      >
                        Tokens
                      </Button>
                      {admin && (
                        <Menu position="bottom-end" withinPortal>
                          <Menu.Target>
                            <Button size="compact-xs" variant="default" rightSection={<IconChevronDown size={12} />}>
                              Actions
                            </Button>
                          </Menu.Target>
                          <Menu.Dropdown>
                            <Menu.Item
                              onClick={() =>
                                update.mutate({ sa, body: { status: sa.status === 'active' ? 'disabled' : 'active' } })
                              }
                            >
                              {sa.status === 'active' ? 'Disable (revokes its tokens)' : 'Enable'}
                            </Menu.Item>
                            <Menu.Divider />
                            <Menu.Item color="red" onClick={() => setDel(sa)}>
                              Delete service account
                            </Menu.Item>
                          </Menu.Dropdown>
                        </Menu>
                      )}
                    </Group>
                  </Table.Td>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        </Table.ScrollContainer>
      )}
      <Modal opened={createOpen} onClose={() => setCreateOpen(false)} title="New service account" centered>
        <form
          onSubmit={(e) => {
            e.preventDefault()
            create.mutate()
          }}
        >
          <Stack gap="sm">
            <TextInput
              label="Name"
              description="Lowercase letters, digits, '-' and '_'"
              required
              value={name}
              onChange={(e) => setName(e.currentTarget.value)}
              placeholder="terraform"
              data-testid="service-account-name"
            />
            <TextInput
              label="Description"
              value={description}
              onChange={(e) => setDescription(e.currentTarget.value)}
            />
            <Select
              label="Installation role"
              data={roleOptions}
              value={role}
              onChange={(v) => setRole((v ?? '') as ServiceRole)}
              allowDeselect={false}
            />
            {create.isError && <Alert color="red">{errorMessage(create.error)}</Alert>}
            <Group justify="flex-end">
              <Button variant="default" onClick={() => setCreateOpen(false)}>
                Cancel
              </Button>
              <Button type="submit" loading={create.isPending} disabled={!name.trim()}>
                Create
              </Button>
            </Group>
          </Stack>
        </form>
      </Modal>
      {tokensFor && <TokensModal sa={tokensFor} admin={admin} onClose={() => setTokensFor(null)} />}
      <ConfirmModal
        opened={del !== null}
        title="Delete service account"
        danger
        confirmLabel="Delete"
        typeToConfirm={del?.name}
        pending={remove.isPending}
        onClose={() => setDel(null)}
        onConfirm={() => del && remove.mutate(del)}
      >
        Deleting removes the service account, its memberships and tokens; automation using them stops working.
        {remove.isError && (
          <Text c="red" size="sm">
            {errorMessage(remove.error)}
          </Text>
        )}
      </ConfirmModal>
    </Card>
  )
}

function TokensModal({ sa, admin, onClose }: { sa: User; admin: boolean; onClose: () => void }) {
  const qc = useQueryClient()
  const id = sa.id
  const tokens = useQuery({
    queryKey: keys.serviceAccountTokens(id),
    queryFn: async () => unwrap(await api.GET('/service-accounts/{id}/tokens', { params: { path: { id } } })),
  })
  const [name, setName] = useState('')
  const [days, setDays] = useState(90)
  const [secret, setSecret] = useState<string | null>(null)
  const [revoke, setRevoke] = useState<Token | null>(null)
  const invalidate = () => void qc.invalidateQueries({ queryKey: keys.serviceAccountTokens(id) })
  const create = useMutation({
    mutationFn: async () =>
      unwrap(
        await api.POST('/service-accounts/{id}/tokens', {
          params: { path: { id } },
          body: { name: name.trim(), expires_in_days: days },
        }),
      ),
    onSuccess: (t) => {
      setName('')
      setSecret(t.token)
      invalidate()
    },
  })
  const doRevoke = useMutation({
    mutationFn: async (t: Token) =>
      check(await api.DELETE('/service-accounts/{id}/tokens/{token}', { params: { path: { id, token: t.id } } })),
    onSuccess: () => {
      setRevoke(null)
      invalidate()
    },
  })
  return (
    <>
      <Modal opened onClose={onClose} title={`Tokens of ${sa.name ?? sa.id}`} size="lg" centered>
        <Stack gap="sm">
          {tokens.isPending ? (
            <LoadingRows rows={2} />
          ) : tokens.isError ? (
            <ErrorState error={tokens.error} what="tokens" />
          ) : tokens.data.length === 0 ? (
            <Text size="sm" c="dimmed">
              No tokens yet.
            </Text>
          ) : (
            <Table fz="sm" aria-label={`Tokens of ${sa.name ?? sa.id}`}>
              <Table.Thead>
                <Table.Tr>
                  <Table.Th>Name</Table.Th>
                  <Table.Th>Role</Table.Th>
                  <Table.Th>Last used</Table.Th>
                  <Table.Th>Expires</Table.Th>
                  <Table.Th />
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {tokens.data.map((t) => (
                  <Table.Tr key={t.id}>
                    <Table.Td>{t.name}</Table.Td>
                    <Table.Td>
                      <Text size="xs">
                        {t.installation_role || 'projects only'}
                        {t.project_role ? ` · ≤ ${t.project_role}` : ''}
                      </Text>
                    </Table.Td>
                    <Table.Td>
                      <TimeAgo value={t.last_used_at} />
                    </Table.Td>
                    <Table.Td>
                      {t.revoked_at ? <Badge color="gray">revoked</Badge> : <TimeAgo value={t.expires_at} />}
                    </Table.Td>
                    <Table.Td ta="right">
                      {admin && !t.revoked_at && (
                        <Button size="compact-xs" variant="subtle" color="red" onClick={() => setRevoke(t)}>
                          Revoke
                        </Button>
                      )}
                    </Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          )}
          {admin && sa.status === 'active' && (
            <form
              onSubmit={(e) => {
                e.preventDefault()
                create.mutate()
              }}
            >
              <Group align="flex-end" gap="xs">
                <TextInput
                  label="New token"
                  placeholder="terraform-ci"
                  required
                  value={name}
                  onChange={(e) => setName(e.currentTarget.value)}
                  style={{ flex: 1 }}
                  data-testid="service-token-name"
                />
                <NumberInput
                  label="Days"
                  min={1}
                  max={366}
                  value={days}
                  onChange={(v) => setDays(typeof v === 'number' ? v : 90)}
                  w={90}
                />
                <Button
                  type="submit"
                  loading={create.isPending}
                  disabled={!name.trim()}
                  data-testid="service-token-create"
                >
                  Create token
                </Button>
              </Group>
              <Text size="xs" c="dimmed" mt={4}>
                The token has the service account’s installation role ({sa.installation_role || 'none'}). Creating one
                needs a recent sign-in.
              </Text>
              {create.isError && (
                <Alert color="red" mt="xs">
                  {errorMessage(create.error)}
                </Alert>
              )}
            </form>
          )}
        </Stack>
      </Modal>
      <OneTimeSecret
        opened={secret !== null}
        title="Service account token created"
        intro={`Store it in your secret manager; it acts as ${sa.name ?? sa.id} (Authorization: Bearer …).`}
        secret={secret ?? ''}
        onClose={() => setSecret(null)}
      />
      <ConfirmModal
        opened={revoke !== null}
        title="Revoke token"
        danger
        confirmLabel="Revoke"
        pending={doRevoke.isPending}
        onClose={() => setRevoke(null)}
        onConfirm={() => revoke && doRevoke.mutate(revoke)}
      >
        Automation using “{revoke?.name}” stops working immediately.
      </ConfirmModal>
    </>
  )
}
