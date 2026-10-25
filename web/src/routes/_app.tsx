import { createFileRoute, redirect } from '@tanstack/react-router'
import { isFullSession, meQuery } from '@/api/auth'
import { AppShellLayout } from '@/components/AppShell'
import { loginUrl } from '@/api/session'

/**
 * Every signed-in page. A missing session goes to /login (returning here afterwards); a limited session (second factor
 * pending or enrollment required) also goes to /login, which resumes at the right step.
 */
export const Route = createFileRoute('/_app')({
  beforeLoad: async ({ context, location }) => {
    const me = await context.queryClient.query(meQuery)
    if (!isFullSession(me)) {
      throw redirect({ href: loginUrl(location.href), replace: true })
    }
    return { me }
  },
  component: AppShellLayout,
})
