import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useCallback } from 'react'
import { LoginPage } from '@/features/auth/LoginPage'
import { safeNextPath } from '@/lib/redirect'

interface LoginSearch {
  return_to?: string
  error?: string
}

export const Route = createFileRoute('/login')({
  validateSearch: (s: Record<string, unknown>): LoginSearch => ({
    ...(typeof s.return_to === 'string' ? { return_to: s.return_to } : {}),
    ...(typeof s.error === 'string' ? { error: s.error.slice(0, 64) } : {}),
  }),
  component: Login,
})

function Login() {
  const { return_to, error } = Route.useSearch()
  const navigate = useNavigate()
  const target = safeNextPath(return_to)
  const onSignedIn = useCallback(() => {
    void navigate({ href: target, replace: true })
  }, [navigate, target])
  return <LoginPage returnTo={target} error={error} onSignedIn={onSignedIn} />
}
