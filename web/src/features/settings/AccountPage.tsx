import {
  Alert,
  Badge,
  Button,
  Card,
  Group,
  Modal,
  MultiSelect,
  NumberInput,
  PasswordInput,
  Select,
  SimpleGrid,
  Stack,
  Table,
  Text,
  TextInput,
  Title,
} from '@mantine/core'
import { notifications } from '@mantine/notifications'
import { IconKey, IconLogout, IconShieldCheck } from '@tabler/icons-react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { fetchMe, meKey, useMe } from '@/api/auth'
import { api, check, errorMessage, unwrap, type Schemas } from '@/api/client'
import { keys, useProjects } from '@/api/queries'
import { Page } from '@/components/AppShell'
import { ConfirmModal, Field, OneTimeSecret, PageHeader, TimeAgo } from '@/components/common'
import { ErrorState, LoadingRows } from '@/components/States'
import { RecoveryCodes } from '@/features/auth/RecoveryCodes'
import { TotpEnrollment } from '@/features/auth/TotpEnrollment'
import { useTitle } from '@/lib/appStore'

function PasswordCard({ hasPassword }: { hasPassword: boolean }) {
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [confirm, setConfirm] = useState('')
  const save = useMutation({
    mutationFn: async () =>
      check(await api.PUT('/me/password', { body: { ...(hasPassword ? { current } : {}), new: next } })),
    onSuccess: () => {
      setCurrent('')
      setNext('')
      setConfirm('')
      notifications.show({ color: 'green', message: 'Password changed. Other sessions were signed out.' })
    },
  })
  const mismatch = confirm !== '' && confirm !== next
  return (
    <Card>
      <Title order={2} fz={14} mb="xs">
        {hasPassword ? 'Change password' : 'Set a password'}
      </Title>
      <form
        onSubmit={(e) => {
          e.preventDefault()
          if (!mismatch) save.mutate()
        }}
      >
        <Stack gap="xs">
          {hasPassword && (
            <PasswordInput
              label="Current password"
              autoComplete="current-password"
              required
              value={current}
              onChange={(e) => setCurrent(e.currentTarget.value)}
            />
          )}
          <PasswordInput
            label="New password"
            description="At least 12 characters."
            autoComplete="new-password"
            required
            value={next}
            onChange={(e) => setNext(e.currentTarget.value)}
          />
          <PasswordInput
            label="Confirm"
            autoComplete="new-password"
            required
            value={confirm}
            onChange={(e) => setConfirm(e.currentTarget.value)}
            error={mismatch ? 'Does not match' : undefined}
          />
          {save.isError && <Alert color="red">{errorMessage(save.error)}</Alert>}
          <Group justify="flex-end">
            <Button type="submit" loading={save.isPending} disabled={next.length < 12 || mismatch}>
              Save password
            </Button>
          </Group>
        </Stack>
      </form>
    </Card>
  )
}

function MfaCard({ enrolled }: { enrolled: boolean }) {
  const qc = useQueryClient()
  const [enroll, setEnroll] = useState(false)
  const [codes, setCodes] = useState<string[] | null>(null)
  const [disable, setDisable] = useState(false)
  const regen = useMutation({
    mutationFn: async () => unwrap(await api.POST('/me/mfa/recovery-codes')),
    onSuccess: (r) => setCodes(r.recovery_codes),
    onError: (e) => notifications.show({ color: 'red', title: 'Could not regenerate codes', message: errorMessage(e) }),
  })
  const off = useMutation({
    mutationFn: async () => check(await api.DELETE('/me/mfa/totp')),
    onSuccess: async () => {
      setDisable(false)
      qc.setQueryData(meKey, await fetchMe())
    },
  })
  return (
    <Card>
      <Group justify="space-between" mb="xs">
        <Title order={2} fz={14}>
          Two-factor authentication
        </Title>
        <Badge color={enrolled ? 'green' : 'gray'} leftSection={<IconShieldCheck size={12} />}>
          {enrolled ? 'On' : 'Off'}
        </Badge>
      </Group>
      <Text size="sm" c="dimmed" mb="sm">
        An authenticator app (TOTP) protects sign-in and sensitive actions. Recovery codes work once each if you lose
        the app.
      </Text>
      <Group gap="xs">
        <Button variant={enrolled ? 'default' : 'filled'} onClick={() => setEnroll(true)}>
          {enrolled ? 'Replace authenticator' : 'Set up authenticator'}
        </Button>
        {enrolled && (
          <>
            <Button variant="default" onClick={() => regen.mutate()} loading={regen.isPending}>
              New recovery codes
            </Button>
            <Button variant="subtle" color="red" onClick={() => setDisable(true)}>
              Turn off
            </Button>
          </>
        )}
      </Group>
      <Modal opened={enroll} onClose={() => setEnroll(false)} title="Set up an authenticator" centered size="md">
        <TotpEnrollment
          onDone={() => {
            setEnroll(false)
            void fetchMe().then((me) => qc.setQueryData(meKey, me))
          }}
          onCancel={() => setEnroll(false)}
        />
      </Modal>
      <Modal
        opened={codes !== null}
        onClose={() => setCodes(null)}
        title="New recovery codes"
        centered
        closeOnClickOutside={false}
      >
        <Stack gap="sm">
          <Alert color="yellow">Your previous codes no longer work. Store these offline; they are shown once.</Alert>
          {codes && <RecoveryCodes codes={codes} />}
          <Button onClick={() => setCodes(null)}>I have saved them</Button>
        </Stack>
      </Modal>
      <ConfirmModal
        opened={disable}
        title="Turn off two-factor authentication"
        danger
        confirmLabel="Turn off"
        pending={off.isPending}
        onClose={() => setDisable(false)}
        onConfirm={() => off.mutate()}
      >
        Your account will be protected by the password only. Owners and admins cannot turn it off.
        {off.isError && (
          <Text c="red" size="sm">
            {errorMessage(off.error)}
          </Text>
        )}
      </ConfirmModal>
    </Card>
  )
}

function SessionsCard() {
  const qc = useQueryClient()
  const sessions = useQuery({
    queryKey: keys.account('sessions'),
    queryFn: async () => unwrap(await api.GET('/me/sessions')),
  })
  const revoke = useMutation({
    mutationFn: async (id: string) => check(await api.DELETE('/me/sessions/{id}', { params: { path: { id } } })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: keys.account('sessions') }),
  })
  const others = useMutation({
    mutationFn: async () => check(await api.POST('/me/sessions/revoke-others')),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: keys.account('sessions') })
      notifications.show({ color: 'green', message: 'Signed out everywhere else' })
    },
  })
  return (
    <Card>
      <Group justify="space-between" mb="xs">
        <Title order={2} fz={14}>
          Sessions
        </Title>
        <Button
          size="xs"
          variant="default"
          leftSection={<IconLogout size={14} />}
          onClick={() => others.mutate()}
          loading={others.isPending}
        >
          Sign out other sessions
        </Button>
      </Group>
      {sessions.isPending ? (
        <LoadingRows rows={2} />
      ) : sessions.isError ? (
        <ErrorState error={sessions.error} what="sessions" />
      ) : (
        <Table fz="sm" aria-label="Sessions">
          <Table.Tbody>
            {sessions.data.map((s) => (
              <Table.Tr key={s.id}>
                <Table.Td>
                  <Text size="sm">
                    {s.user_agent.slice(0, 80) || 'unknown client'}{' '}
                    {s.current && (
                      <Badge size="xs" component="span">
                        this session
                      </Badge>
                    )}
                  </Text>
                  <Text size="xs" c="dimmed">
                    {s.method}
                    {s.provider ? ` (${s.provider})` : ''} · {s.ip} · last seen <TimeAgo value={s.last_seen_at} />
                  </Text>
                </Table.Td>
                <Table.Td ta="right">
                  {!s.current && (
                    <Button size="compact-xs" variant="subtle" color="red" onClick={() => revoke.mutate(s.id)}>
                      Sign out
                    </Button>
                  )}
                </Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      )}
    </Card>
  )
}

function TokensCard() {
  const qc = useQueryClient()
  const projects = useProjects()
  const tokens = useQuery({
    queryKey: keys.account('tokens'),
    queryFn: async () => unwrap(await api.GET('/me/tokens')),
  })
  const [open, setOpen] = useState(false)
  const [name, setName] = useState('')
  const [days, setDays] = useState(90)
  const [scope, setScope] = useState<string[]>([])
  const [projectRole, setProjectRole] = useState<string>('')
  const [instRole, setInstRole] = useState<string>('')
  const [secret, setSecret] = useState<string | null>(null)
  const [revoke, setRevoke] = useState<Schemas['Token'] | null>(null)
  const me = useMe().data
  const create = useMutation({
    mutationFn: async () =>
      unwrap(
        await api.POST('/me/tokens', {
          body: {
            name: name.trim(),
            expires_in_days: days,
            ...(scope.length ? { projects: scope } : {}),
            ...(projectRole ? { project_role: projectRole as Schemas['CreateTokenInputBody']['project_role'] } : {}),
            ...(instRole
              ? { installation_role: instRole as Schemas['CreateTokenInputBody']['installation_role'] }
              : {}),
          },
        }),
      ),
    onSuccess: (t) => {
      setOpen(false)
      setSecret(t.token)
      setName('')
      void qc.invalidateQueries({ queryKey: keys.account('tokens') })
    },
  })
  const doRevoke = useMutation({
    mutationFn: async (t: Schemas['Token']) =>
      check(await api.DELETE('/me/tokens/{id}', { params: { path: { id: t.id } } })),
    onSuccess: () => {
      setRevoke(null)
      void qc.invalidateQueries({ queryKey: keys.account('tokens') })
    },
  })
  return (
    <Card>
      <Group justify="space-between" mb="xs">
        <Title order={2} fz={14}>
          API tokens
        </Title>
        <Button size="xs" leftSection={<IconKey size={14} />} onClick={() => setOpen(true)} data-testid="new-api-token">
          New token
        </Button>
      </Group>
      <Text size="sm" c="dimmed" mb="xs">
        For the Terraform provider, scripts and CI. A token acts for you with at most your roles, optionally restricted
        to projects and a lower role.
      </Text>
      {tokens.isPending ? (
        <LoadingRows rows={2} />
      ) : tokens.isError ? (
        <ErrorState error={tokens.error} what="tokens" />
      ) : tokens.data.length === 0 ? (
        <Text size="sm" c="dimmed">
          No tokens.
        </Text>
      ) : (
        <Table fz="sm" aria-label="API tokens">
          <Table.Thead>
            <Table.Tr>
              <Table.Th>Name</Table.Th>
              <Table.Th>Scope</Table.Th>
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
                    {t.projects.length
                      ? t.projects.map((p) => projects.data?.find((x) => x.id === p)?.name ?? p).join(', ')
                      : 'all your projects'}
                    {t.project_role ? ` · ≤ ${t.project_role}` : ''}
                    {t.installation_role ? ` · ${t.installation_role}` : ''}
                  </Text>
                </Table.Td>
                <Table.Td>
                  <TimeAgo value={t.last_used_at} />
                </Table.Td>
                <Table.Td>
                  {t.revoked_at ? <Badge color="gray">revoked</Badge> : <TimeAgo value={t.expires_at} />}
                </Table.Td>
                <Table.Td ta="right">
                  {!t.revoked_at && (
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
      <Modal opened={open} onClose={() => setOpen(false)} title="New API token" centered>
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
              value={name}
              onChange={(e) => setName(e.currentTarget.value)}
              placeholder="terraform-ci"
            />
            <NumberInput
              label="Expires in (days)"
              min={1}
              max={365}
              value={days}
              onChange={(v) => setDays(typeof v === 'number' ? v : 90)}
            />
            <MultiSelect
              label="Restrict to projects"
              description="Empty = every project you can access"
              data={(projects.data ?? []).map((p) => ({ value: p.id, label: p.display_name || p.name }))}
              value={scope}
              onChange={setScope}
              clearable
            />
            <Select
              label="Cap the project role"
              data={[
                { value: '', label: 'Your role in each project' },
                { value: 'viewer', label: 'viewer (read-only)' },
                { value: 'editor', label: 'editor' },
                { value: 'admin', label: 'admin' },
              ]}
              value={projectRole}
              onChange={(v) => setProjectRole(v ?? '')}
              allowDeselect={false}
            />
            {me?.installation_role && (
              <Select
                label="Installation role"
                data={[
                  { value: '', label: 'None' },
                  { value: 'auditor', label: 'auditor' },
                  ...(me.installation_role !== 'auditor' ? [{ value: 'admin', label: 'admin' }] : []),
                ]}
                value={instRole}
                onChange={(v) => setInstRole(v ?? '')}
                allowDeselect={false}
              />
            )}
            <Text size="xs" c="dimmed">
              Creating a token needs a recent sign-in; you may be asked to confirm your password.
            </Text>
            {create.isError && <Alert color="red">{errorMessage(create.error)}</Alert>}
            <Group justify="flex-end">
              <Button variant="default" onClick={() => setOpen(false)}>
                Cancel
              </Button>
              <Button type="submit" loading={create.isPending} disabled={!name.trim()}>
                Create token
              </Button>
            </Group>
          </Stack>
        </form>
      </Modal>
      <OneTimeSecret
        opened={secret !== null}
        title="API token created"
        intro="Use it as a bearer token (Authorization: Bearer …)."
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
        Programs using “{revoke?.name}” stop working immediately.
      </ConfirmModal>
    </Card>
  )
}

function IdentitiesCard() {
  const qc = useQueryClient()
  const ids = useQuery({
    queryKey: keys.account('identities'),
    queryFn: async () => unwrap(await api.GET('/me/identities')),
  })
  const unlink = useMutation({
    mutationFn: async (id: string) => check(await api.DELETE('/me/identities/{id}', { params: { path: { id } } })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: keys.account('identities') }),
    onError: (e) => notifications.show({ color: 'red', title: 'Could not unlink', message: errorMessage(e) }),
  })
  return (
    <Card>
      <Title order={2} fz={14} mb="xs">
        Single sign-on identities
      </Title>
      {ids.isPending ? (
        <LoadingRows rows={1} />
      ) : ids.isError ? (
        <ErrorState error={ids.error} what="identities" />
      ) : ids.data.length === 0 ? (
        <Text size="sm" c="dimmed">
          No linked identities.
        </Text>
      ) : (
        ids.data.map((i) => (
          <Group key={i.id} justify="space-between">
            <Text size="sm">
              {i.provider}: {i.email ?? i.subject}
            </Text>
            <Button size="compact-xs" variant="subtle" color="red" onClick={() => unlink.mutate(i.id)}>
              Unlink
            </Button>
          </Group>
        ))
      )}
    </Card>
  )
}

export function AccountPage() {
  useTitle('Account')
  const me = useMe().data
  if (!me) return null
  return (
    <Page>
      <PageHeader title="Account & security" description={`${me.display_name}${me.email ? ` · ${me.email}` : ''}`} />
      <SimpleGrid cols={{ base: 1, lg: 2 }} spacing="sm">
        <Stack gap="sm">
          <Card>
            <Title order={2} fz={14} mb="xs">
              Profile
            </Title>
            <Field label="Name">{me.display_name}</Field>
            <Field label="Email">{me.email ?? '—'}</Field>
            <Field label="Installation role">{me.installation_role || 'none'}</Field>
            <Field label="Signed in">
              {me.method}
              {me.provider ? ` (${me.provider})` : ''} · <TimeAgo value={me.auth_at} />
            </Field>
            <Field label="Session expires">
              <TimeAgo value={me.expires_at} />
            </Field>
          </Card>
          <MfaCard enrolled={me.mfa_enrolled} />
          {me.method !== 'oidc' && <PasswordCard hasPassword={me.has_password} />}
        </Stack>
        <Stack gap="sm">
          <TokensCard />
          <SessionsCard />
          <IdentitiesCard />
        </Stack>
      </SimpleGrid>
    </Page>
  )
}
