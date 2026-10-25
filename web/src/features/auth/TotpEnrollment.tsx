import { Alert, Button, Center, Code, Group, Loader, Stack, Text, TextInput } from '@mantine/core'
import { useEffect, useRef, useState } from 'react'
import { api, errorMessage, unwrap, type Schemas } from '@/api/client'
import { CopyIcon } from '@/components/common'
import { QRCode } from '@/components/QRCode'
import { RecoveryCodes } from './RecoveryCodes'

/**
 * TOTP enrollment: POST /me/mfa/totp returns a secret and an otpauth:// URI (the QR code is rendered locally with uqr),
 * the user confirms a code with POST /me/mfa/totp/confirm, which returns recovery codes shown exactly once.
 *
 * A limited session that must enroll is upgraded to a full session by the confirmation (the cookie rotates; callers
 * refetch /me for the new CSRF token). Replacing an existing factor needs a recent sign-in (the re-authentication
 * prompt handles it).
 */
export function TotpEnrollment({ onDone, onCancel }: { onDone: () => void; onCancel?: () => void }) {
  const [enrollment, setEnrollment] = useState<Schemas['TOTPEnrollment'] | null>(null)
  const [codes, setCodes] = useState<string[] | null>(null)
  const [code, setCode] = useState('')
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const started = useRef(false)

  const begin = async () => {
    setPending(true)
    setError(null)
    try {
      setEnrollment(unwrap(await api.POST('/me/mfa/totp')))
    } catch (e) {
      setError(e)
    } finally {
      setPending(false)
    }
  }

  useEffect(() => {
    // Start once (StrictMode mounts twice in development).
    if (!started.current) {
      started.current = true
      void begin()
    }
  }, [])

  const confirm = async () => {
    setPending(true)
    setError(null)
    try {
      const res = unwrap(await api.POST('/me/mfa/totp/confirm', { body: { code: code.trim() } }))
      setCodes(res.recovery_codes)
    } catch (e) {
      setError(e)
    } finally {
      setPending(false)
    }
  }

  if (codes) {
    return (
      <Stack gap="sm">
        <Alert color="green" title="Two-factor authentication is on">
          Save these recovery codes somewhere safe and offline. Each code signs you in once if you lose your
          authenticator. They are shown only now.
        </Alert>
        <RecoveryCodes codes={codes} />
        <Button onClick={onDone} fullWidth>
          I have saved my recovery codes
        </Button>
      </Stack>
    )
  }

  return (
    <Stack gap="sm">
      {enrollment === null ? (
        error !== null ? (
          <Alert color="red" title="Could not start enrollment" role="alert">
            <Stack gap="xs">
              <Text size="sm">{errorMessage(error)}</Text>
              <Group>
                <Button size="xs" onClick={() => void begin()}>
                  Retry
                </Button>
                {onCancel && (
                  <Button size="xs" variant="default" onClick={onCancel}>
                    Cancel
                  </Button>
                )}
              </Group>
            </Stack>
          </Alert>
        ) : (
          <Center py="md">
            <Loader size="sm" aria-label="Starting enrollment" />
          </Center>
        )
      ) : (
        <form
          aria-label="Confirm authenticator"
          onSubmit={(e) => {
            e.preventDefault()
            void confirm()
          }}
        >
          <Stack gap="sm">
            <Text size="sm">
              Scan this QR code with an authenticator app (1Password, Bitwarden, Google Authenticator, …), then enter
              the 6-digit code it shows.
            </Text>
            <Center>
              <QRCode value={enrollment.otpauth_uri} label="QR code for your authenticator app" />
            </Center>
            <div>
              <Text size="xs" c="dimmed">
                Can’t scan? Enter this key manually (time-based, SHA-1, 6 digits, 30 s):
              </Text>
              <Group gap={4} wrap="nowrap">
                <Code style={{ wordBreak: 'break-all', fontSize: 12 }} data-testid="totp-secret">
                  {enrollment.secret}
                </Code>
                <CopyIcon value={enrollment.secret} label="Copy key" />
              </Group>
            </div>
            <TextInput
              label="Code from your app"
              autoComplete="one-time-code"
              inputMode="numeric"
              maxLength={6}
              required
              value={code}
              onChange={(e) => setCode(e.currentTarget.value)}
            />
            {error !== null && (
              <Alert color="red" role="alert">
                {errorMessage(error)}
              </Alert>
            )}
            <Group justify="flex-end">
              {onCancel && (
                <Button variant="default" onClick={onCancel} disabled={pending}>
                  Cancel
                </Button>
              )}
              <Button type="submit" loading={pending}>
                Turn on two-factor authentication
              </Button>
            </Group>
          </Stack>
        </form>
      )}
    </Stack>
  )
}
