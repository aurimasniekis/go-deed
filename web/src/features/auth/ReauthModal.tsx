import { Alert, Button, Center, Group, Loader, Modal, PasswordInput, Stack, Text, TextInput } from '@mantine/core'
import { useQueryClient } from '@tanstack/react-query'
import { useEffect, useRef, useState, type ReactNode } from 'react'
import { fetchMe, meKey } from '@/api/auth'
import { api, errorMessage, unwrap, type Me } from '@/api/client'
import { loginUrl, setCsrfToken, setReauthHandler, type ReauthCode } from '@/api/session'

/**
 * "Confirm it's you" (recent authentication, ADR-005). Registered as the session layer's re-authentication handler:
 * when a request fails with reauth_required (action "reauth"), the request waits for this prompt and is retried once
 * after success (the session rotates, so the CSRF token is refreshed from the returned Me). Cancelling fails the
 * original request. SSO sessions get an action URL instead and never reach this modal.
 */
type Mode = { kind: 'loading' } | { kind: 'error'; error: unknown } | { kind: 'limited' } | { kind: 'password'; me: Me }

const here = () => `${window.location.pathname}${window.location.search}`

export function ReauthModal({
  navigate = (url: string) => window.location.assign(url),
}: {
  navigate?: (url: string) => void
}) {
  const qc = useQueryClient()
  const [open, setOpen] = useState(false)
  const [mode, setMode] = useState<Mode>({ kind: 'loading' })
  const [reason, setReason] = useState<ReauthCode>('reauth_required')
  const [password, setPassword] = useState('')
  const [code, setCode] = useState('')
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const resolver = useRef<((ok: boolean) => void) | null>(null)

  useEffect(
    () =>
      setReauthHandler(
        (cause) =>
          new Promise<boolean>((resolve) => {
            resolver.current = resolve
            setReason(cause)
            setError(null)
            setPassword('')
            setCode('')
            setMode({ kind: 'loading' })
            setOpen(true)
            fetchMe()
              .then((me) => {
                if (!me || me.mfa_pending || me.must_enroll) setMode({ kind: 'limited' })
                else setMode({ kind: 'password', me })
              })
              .catch((e: unknown) => setMode({ kind: 'error', error: e }))
          }),
      ),
    [],
  )

  const finish = (ok: boolean) => {
    resolver.current?.(ok)
    resolver.current = null
    setOpen(false)
  }

  const cancel = () => {
    if (!pending) finish(false)
  }

  let body: ReactNode
  switch (mode.kind) {
    case 'loading':
      body = (
        <Center py="md">
          <Loader size="sm" aria-label="Loading" />
        </Center>
      )
      break
    case 'error':
      body = (
        <Stack gap="sm">
          <Alert color="red">{errorMessage(mode.error)}</Alert>
          <Group justify="flex-end">
            <Button variant="default" onClick={cancel}>
              Close
            </Button>
          </Group>
        </Stack>
      )
      break
    case 'limited':
      body = (
        <Stack gap="sm">
          <Text size="sm">Your sign-in is not complete. Finish two-factor authentication to continue.</Text>
          <Group justify="flex-end">
            <Button variant="default" onClick={cancel}>
              Cancel
            </Button>
            <Button
              onClick={() => {
                finish(false)
                navigate(loginUrl(here()))
              }}
            >
              Go to sign-in
            </Button>
          </Group>
        </Stack>
      )
      break
    case 'password': {
      const needsCode = mode.me.mfa_enrolled
      body = (
        <form
          aria-label="Re-authenticate"
          onSubmit={(e) => {
            e.preventDefault()
            setPending(true)
            setError(null)
            api
              .POST('/auth/reauth', { body: { password, ...(needsCode ? { code: code.trim() } : {}) } })
              .then((r) => {
                const me = unwrap(r)
                setCsrfToken(me.csrf_token)
                qc.setQueryData(meKey, me)
                finish(true)
              })
              .catch((err: unknown) => setError(err))
              .finally(() => setPending(false))
          }}
        >
          <Stack gap="sm">
            <Text size="sm">
              {reason === 'mfa_required'
                ? 'This action needs a recent second-factor check. Enter your password and authenticator code.'
                : 'This action needs a recent sign-in. Enter your password to continue.'}
            </Text>
            <PasswordInput
              label="Password"
              autoComplete="current-password"
              required
              data-autofocus
              value={password}
              onChange={(e) => setPassword(e.currentTarget.value)}
            />
            {needsCode && (
              <TextInput
                label="Authenticator or recovery code"
                autoComplete="one-time-code"
                maxLength={32}
                required
                value={code}
                onChange={(e) => setCode(e.currentTarget.value)}
              />
            )}
            {error !== null && (
              <Alert color="red" role="alert">
                {errorMessage(error)}
              </Alert>
            )}
            <Group justify="flex-end">
              <Button variant="default" onClick={cancel} disabled={pending}>
                Cancel
              </Button>
              <Button type="submit" loading={pending}>
                Verify
              </Button>
            </Group>
          </Stack>
        </form>
      )
      break
    }
  }

  return (
    <Modal
      opened={open}
      onClose={cancel}
      title="Confirm it’s you"
      closeOnClickOutside={false}
      withCloseButton={!pending}
      zIndex={400}
      centered
    >
      {body}
    </Modal>
  )
}
