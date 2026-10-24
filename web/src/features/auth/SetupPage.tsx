import { AnchorLink, ButtonLink } from '@/components/links'
import { Alert, Button, Center, Loader, PasswordInput, Stack, Text, TextInput } from '@mantine/core'
import { useQueryClient } from '@tanstack/react-query'
import { useEffect, useRef, useState } from 'react'
import { fetchMe, meKey } from '@/api/auth'
import { api, errorMessage, unwrap, type Me, type Schemas } from '@/api/client'
import { setCsrfToken } from '@/api/session'
import { AuthLayout } from './AuthLayout'
import { parseSetupFragment, type SetupPurpose } from './sso'
import { TotpEnrollment } from './TotpEnrollment'

const MIN_PASSWORD = 12

const titles: Record<SetupPurpose, { title: string; subtitle: string; submit: string }> = {
  bootstrap: {
    title: 'Create the first owner',
    subtitle: 'This account owns the installation. Owners must use two-factor authentication.',
    submit: 'Create owner account',
  },
  invite: { title: 'Accept your invitation', subtitle: 'Choose your name and a password.', submit: 'Create account' },
  reset: { title: 'Choose a new password', subtitle: 'Your previous password stops working.', submit: 'Set password' },
  recovery: {
    title: 'Break-glass sign-in',
    subtitle:
      'This single-use recovery link signs you in without your second factor. Its use is audited and raises an alert.',
    submit: 'Sign in with recovery link',
  },
}

type Phase =
  | { kind: 'reading' }
  | { kind: 'missing' }
  | { kind: 'invalid'; error: unknown }
  | { kind: 'form'; purpose: SetupPurpose; token: string; info: Schemas['LinkInfo'] }
  | { kind: 'enroll' }
  | { kind: 'reset-done' }

/**
 * Single-use links (`/setup#<purpose>=<token>`): bootstrap (first owner), invite, password reset and break-glass
 * recovery. The token is read from the fragment, removed from the address bar, validated with
 * POST /auth/setup/inspect and consumed by POST /auth/setup/complete.
 */
export function SetupPage({ onDone }: { onDone: () => void }) {
  const qc = useQueryClient()
  // The fragment is read once, before the first render.
  const [link] = useState(() => parseSetupFragment(window.location.hash))
  const [phase, setPhase] = useState<Phase>(() => (link ? { kind: 'reading' } : { kind: 'missing' }))
  const [email, setEmail] = useState('')
  const [name, setName] = useState('')
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const read = useRef(false)

  useEffect(() => {
    if (read.current) return
    read.current = true
    if (!link) return
    // Keep the token out of history, screenshots and bookmarks.
    window.history.replaceState(null, '', window.location.pathname)
    api
      .POST('/auth/setup/inspect', { body: { token: link.token } })
      .then((r) => {
        const info = unwrap(r)
        const purpose =
          (['bootstrap', 'invite', 'reset', 'recovery'] as const).find((p) => p === info.purpose) ?? link.purpose
        setEmail(info.email ?? '')
        setName(info.display_name ?? '')
        setPhase({ kind: 'form', purpose, token: link.token, info })
      })
      .catch((e: unknown) => setPhase({ kind: 'invalid', error: e }))
  }, [link])

  const enter = (me: Me) => {
    setCsrfToken(me.csrf_token)
    if (me.must_enroll) {
      setPhase({ kind: 'enroll' })
      return
    }
    qc.setQueryData(meKey, me)
    onDone()
  }

  if (phase.kind === 'reading') {
    return (
      <AuthLayout title="Opening link">
        <Center py="md">
          <Loader size="sm" aria-label="Checking the link" />
        </Center>
      </AuthLayout>
    )
  }
  if (phase.kind === 'missing' || phase.kind === 'invalid') {
    return (
      <AuthLayout title="This link cannot be used">
        <Alert color="red" role="alert">
          {phase.kind === 'missing'
            ? 'The link is incomplete. Open the full link you received (it contains a # part).'
            : `${errorMessage(phase.error)}. Links are single-use and expire; ask an administrator for a new one.`}
        </Alert>
        <AnchorLink to="/login" size="sm">
          Go to sign-in
        </AnchorLink>
      </AuthLayout>
    )
  }
  if (phase.kind === 'reset-done') {
    return (
      <AuthLayout title="Password changed">
        <Alert color="green">Your new password is set. Sign in with it now.</Alert>
        <ButtonLink to="/login">Sign in</ButtonLink>
      </AuthLayout>
    )
  }
  if (phase.kind === 'enroll') {
    return (
      <AuthLayout title="Set up two-factor authentication" width={460}>
        <TotpEnrollment
          onDone={() => {
            void fetchMe().then((me) => {
              qc.setQueryData(meKey, me)
              onDone()
            })
          }}
        />
      </AuthLayout>
    )
  }

  const { purpose, token, info } = phase
  const t = titles[purpose]
  const needsPassword = purpose !== 'recovery'
  const mismatch = needsPassword && confirm !== '' && confirm !== password
  const tooShort = needsPassword && password !== '' && password.length < MIN_PASSWORD

  return (
    <AuthLayout title={t.title} subtitle={t.subtitle}>
      <form
        aria-label={t.title}
        onSubmit={(e) => {
          e.preventDefault()
          if (mismatch || tooShort) return
          setPending(true)
          setError(null)
          api
            .POST('/auth/setup/complete', {
              body: {
                token,
                ...(purpose === 'bootstrap' ? { email: email.trim() } : {}),
                ...(purpose === 'bootstrap' || purpose === 'invite' ? { display_name: name.trim() } : {}),
                ...(needsPassword ? { password } : {}),
              },
            })
            .then((r) => {
              const me = unwrap(r)
              if (purpose === 'reset') setPhase({ kind: 'reset-done' })
              else enter(me)
            })
            .catch((err: unknown) => setError(err))
            .finally(() => setPending(false))
        }}
      >
        <Stack gap="sm">
          {info.role && (
            <Text size="sm">
              Role: <b>{info.role}</b>
            </Text>
          )}
          {purpose === 'bootstrap' ? (
            <TextInput
              label="Email"
              type="email"
              autoComplete="username"
              required
              value={email}
              onChange={(e) => setEmail(e.currentTarget.value)}
            />
          ) : (
            info.email && <TextInput label="Email" value={info.email} readOnly autoComplete="username" />
          )}
          {(purpose === 'bootstrap' || purpose === 'invite') && (
            <TextInput
              label="Display name"
              autoComplete="name"
              required
              value={name}
              onChange={(e) => setName(e.currentTarget.value)}
            />
          )}
          {needsPassword && (
            <>
              <PasswordInput
                label="New password"
                description={`At least ${MIN_PASSWORD} characters. A passphrase is easiest to remember.`}
                autoComplete="new-password"
                required
                value={password}
                error={tooShort ? `Use at least ${MIN_PASSWORD} characters` : undefined}
                onChange={(e) => setPassword(e.currentTarget.value)}
              />
              <PasswordInput
                label="Confirm password"
                autoComplete="new-password"
                required
                value={confirm}
                error={mismatch ? 'The passwords do not match' : undefined}
                onChange={(e) => setConfirm(e.currentTarget.value)}
              />
            </>
          )}
          {error !== null && (
            <Alert color="red" role="alert">
              {errorMessage(error)}
            </Alert>
          )}
          <Button type="submit" loading={pending} fullWidth color={purpose === 'recovery' ? 'red' : undefined}>
            {t.submit}
          </Button>
        </Stack>
      </form>
    </AuthLayout>
  )
}
