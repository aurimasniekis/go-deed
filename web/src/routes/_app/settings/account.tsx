import { createFileRoute } from '@tanstack/react-router'
import { AccountPage } from '@/features/settings/AccountPage'

export interface AccountSearch {
  /** Set by the server when linking a single sign-on identity failed. */
  sso_error?: string
}

export const Route = createFileRoute('/_app/settings/account')({
  validateSearch: (s: Record<string, unknown>): AccountSearch =>
    typeof s.sso_error === 'string' ? { sso_error: s.sso_error.slice(0, 64) } : {},
  component: AccountPage,
})
