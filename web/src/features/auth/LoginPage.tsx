import { Alert, Button, Center, Code, Divider, Loader, PasswordInput, Stack, Text, TextInput } from '@mantine/core'
import { IconLogin2 } from '@tabler/icons-react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect, useState } from 'react'
import { fetchMe, meKey, useSystemInfo } from '@/api/auth'
import { api, errorMessage, unwrap, type Me, type SystemInfo } from '@/api/client'
import { setCsrfToken } from '@/api/session'
import { AuthLayout } from './AuthLayout'
import { CodeForm } from './CodeForm'
import { loginErrorMessage, oidcStartUrl } from './sso'
import { TotpEnrollment } from './TotpEnrollment'

/**
 * Sign-in (ADR-005). Renders from GET /system/info: a password form, SSO buttons, or both. POST /auth/login returns the
 * Me body: `mfa_pending` moves to the code step (POST /auth/mfa, TOTP or recovery code), `must_enroll` to TOTP
 * enrollment, which upgrades the session when confirmed. An existing limited session (after a reload) resumes at the
 * right step. SSO errors arrive as /login?error=<code>.
 */
type Step = 'credentials' | 'mfa' | 'enroll'

export function resumeStep(me: Me | null | undefined): Step | 'done' | null {
  if (!me) return null
  if (me.must_enroll) return 'enroll'
  if (me.mfa_pending) return 'mfa'
  return 'done'
}

export function nextStep(me: Me): Step | 'done' {
  return me.must_enroll ? 'enroll' : me.mfa_pending ? 'mfa' : 'done'
}

function LocalLoginForm({ onResult }: { onResult: (me: Me) => void }) {
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<unknown>(null)
  return (
    <form
      aria-label="Sign in with email and password"
      onSubmit={(e) => {
        e.preventDefault()
        setPending(true)
        setError(null)
        api
          .POST('/auth/login', { body: { email: email.trim(), password } })
          .then((r) => {
            const me = unwrap(r)
            setCsrfToken(me.csrf_token)
            onResult(me)
          })
          .catch((err: unknown) => setError(err))
          .finally(() => setPending(false))
      }}
    >
      <Stack gap="sm">
        <TextInput
          label="Email"
          type="email"
          autoComplete="username"
          autoCapitalize="none"
          spellCheck={false}
          required
          value={email}
          onChange={(e) => setEmail(e.currentTarget.value)}
        />
        <PasswordInput
          label="Password"
          autoComplete="current-password"
          required
          value={password}
          onChange={(e) => setPassword(e.currentTarget.value)}
        />
        {error !== null && (
          <Alert color="red" role="alert">
            {errorMessage(error)}
          </Alert>
        )}
        <Button type="submit" loading={pending} fullWidth leftSection={<IconLogin2 size={14} />}>
          Sign in
        </Button>
      </Stack>
    </form>
  )
}

export function LoginForms({
  info,
  returnTo,
  onSignedIn,
  initialStep = 'credentials',
  error,
}: {
  info: SystemInfo
  returnTo: string
  onSignedIn: () => void
  initialStep?: Step
  error?: string
}) {
  const qc = useQueryClient()
  const [step, setStep] = useState<Step>(initialStep)
  const back = () => {
    void api.POST('/auth/logout').catch(() => undefined)
    setCsrfToken('')
    qc.setQueryData(meKey, null)
    setStep('credentials')
  }
  const finish = async () => {
    // The session rotated: refresh /me (new CSRF token) before entering the app.
    const me = await fetchMe()
    qc.setQueryData(meKey, me)
    onSignedIn()
  }

  if (step === 'mfa') {
    return (
      <CodeForm
        label="Two-factor authentication"
        intro="Enter the 6-digit code from your authenticator app."
        onSubmit={async (code) => {
          const me = unwrap(await api.POST('/auth/mfa', { body: { code } }))
          setCsrfToken(me.csrf_token)
          qc.setQueryData(meKey, me)
          onSignedIn()
        }}
        onBack={back}
      />
    )
  }
  if (step === 'enroll') {
    return (
      <Stack gap="sm">
        <Alert color="blue" title="Set up two-factor authentication">
          Your role requires an authenticator app before you can continue.
        </Alert>
        <TotpEnrollment onDone={() => void finish()} onCancel={back} />
      </Stack>
    )
  }

  const providers = info.sso_providers
  return (
    <Stack gap="sm">
      {error && (
        <Alert color="red" role="alert">
          {loginErrorMessage(error)}
        </Alert>
      )}
      {info.setup_required && (
        <Alert color="blue" title="Deed is not set up yet">
          Run <Code>deed admin bootstrap</Code> on the server and open the setup link it prints.
        </Alert>
      )}
      {info.local_auth ? (
        <LocalLoginForm
          onResult={(me) => {
            const s = nextStep(me)
            if (s === 'done') {
              qc.setQueryData(meKey, me)
              onSignedIn()
            } else setStep(s)
          }}
        />
      ) : (
        <Text size="sm" c="dimmed">
          Password sign-in is disabled. Use single sign-on.
        </Text>
      )}
      {info.local_auth && providers.length > 0 && <Divider label="or" labelPosition="center" />}
      {providers.length > 0 && (
        <Stack gap="xs" aria-label="Single sign-on providers">
          {providers.map((p) => (
            <Button key={p.slug} variant="default" component="a" href={oidcStartUrl(p.slug, returnTo)} fullWidth>
              Sign in with {p.display_name}
            </Button>
          ))}
        </Stack>
      )}
      {!info.local_auth && providers.length === 0 && (
        <Alert color="yellow" title="No sign-in method is enabled">
          An administrator can restore access with <Code>deed admin recover</Code> on the server.
        </Alert>
      )}
    </Stack>
  )
}

export function LoginPage({
  returnTo,
  error,
  onSignedIn,
}: {
  returnTo: string
  error?: string
  onSignedIn: () => void
}) {
  const info = useSystemInfo()
  // An existing session (limited after a reload, or full) decides the first step.
  const session = useQuery({ queryKey: ['login-session'], queryFn: fetchMe, retry: false, staleTime: 0, gcTime: 0 })
  const resume = session.isSuccess ? resumeStep(session.data) : null

  useEffect(() => {
    if (resume === 'done') onSignedIn()
  }, [resume, onSignedIn])

  return (
    <AuthLayout title="Sign in to Deed">
      {info.isPending || session.isPending || resume === 'done' ? (
        <Center py="md">
          <Loader size="sm" aria-label="Loading" />
        </Center>
      ) : info.isError ? (
        <Alert color="red" title="Sign-in options unavailable" role="alert">
          {errorMessage(info.error)}
        </Alert>
      ) : (
        <LoginForms
          info={info.data}
          returnTo={returnTo}
          onSignedIn={onSignedIn}
          error={error}
          {...(resume ? { initialStep: resume } : {})}
        />
      )}
    </AuthLayout>
  )
}
