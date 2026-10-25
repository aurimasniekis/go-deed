import { Alert, Anchor, Button, Stack, Text, TextInput } from '@mantine/core'
import { useState } from 'react'
import { errorMessage } from '@/api/client'

/** One code field (TOTP, or a recovery code) with a submit button; used by sign-in and re-authentication. */
export function CodeForm({
  label,
  intro,
  onSubmit,
  onBack,
  backLabel = 'Use a different account',
}: {
  label: string
  intro: string
  onSubmit: (code: string) => Promise<void>
  onBack?: () => void
  backLabel?: string
}) {
  const [code, setCode] = useState('')
  const [recovery, setRecovery] = useState(false)
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<unknown>(null)
  return (
    <form
      aria-label={label}
      onSubmit={(e) => {
        e.preventDefault()
        setPending(true)
        setError(null)
        onSubmit(code.trim())
          .catch((err: unknown) => setError(err))
          .finally(() => setPending(false))
      }}
    >
      <Stack gap="sm">
        <Text size="sm">
          {recovery
            ? 'Enter one of the recovery codes you saved when you set up two-factor authentication. Each code works once.'
            : intro}
        </Text>
        <TextInput
          label={recovery ? 'Recovery code' : 'Authentication code'}
          autoComplete="one-time-code"
          inputMode={recovery ? 'text' : 'numeric'}
          spellCheck={false}
          maxLength={32}
          required
          data-autofocus
          value={code}
          onChange={(e) => setCode(e.currentTarget.value)}
        />
        {error !== null && (
          <Alert color="red" role="alert">
            {errorMessage(error)}
          </Alert>
        )}
        <Button type="submit" loading={pending} fullWidth>
          Verify
        </Button>
        <Anchor
          component="button"
          type="button"
          size="sm"
          onClick={() => {
            setRecovery((r) => !r)
            setCode('')
          }}
        >
          {recovery ? 'Use my authenticator app instead' : 'Use a recovery code instead'}
        </Anchor>
        {onBack && (
          <Button variant="subtle" onClick={onBack} disabled={pending} fullWidth>
            {backLabel}
          </Button>
        )}
      </Stack>
    </form>
  )
}
