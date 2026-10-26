import { safeNextPath } from '@/lib/redirect'

/**
 * Browser navigation into the OIDC flow (GET /auth/oidc/{slug}/start, outside /api): a full page load, because the
 * provider round trip sets and checks a binding cookie. Errors come back as /login?error=<code>.
 */
export function oidcStartUrl(slug: string, returnTo?: string, purpose?: 'link'): string {
  const params = new URLSearchParams()
  if (purpose) params.set('purpose', purpose)
  if (returnTo !== undefined) params.set('return_to', safeNextPath(returnTo))
  const qs = params.toString()
  return `/auth/oidc/${encodeURIComponent(slug)}/start${qs ? `?${qs}` : ''}`
}

/** Messages for /login?error=<code> (stable codes from the server, never internals). */
export function loginErrorMessage(code: string): string {
  switch (code) {
    case 'forbidden':
      return 'Single sign-on was refused for this account. Ask an administrator to check your account or the provider mappings.'
    case 'not_found':
      return 'That sign-in provider is not available.'
    case 'unauthorized':
    case 'validation_failed':
      return 'The sign-in attempt expired or was already used. Start again.'
    case 'precondition_failed':
      return 'Single sign-on could not continue. Start again.'
    case 'rate_limited':
      return 'Too many sign-in attempts. Wait a minute and try again.'
    case 'unavailable':
      return 'The identity provider is unavailable. Try again later, or sign in another way.'
    case 'conflict':
    case 'already_exists':
      return 'This identity is already linked to another account, or its email belongs to another user. Ask an administrator.'
    case 'reauth_required':
      return 'This needs a recent sign-in. Sign in again to continue.'
    case 'mfa_required':
      return 'This needs your second factor. Sign in with your password and authenticator.'
    default:
      return 'Single sign-on failed. Try again, or contact an administrator.'
  }
}

/** Messages for /settings/account?sso_error=<code>, where a failed identity link returns the signed-in user. */
export function linkErrorMessage(code: string): string {
  switch (code) {
    case 'conflict':
    case 'already_exists':
      return 'This identity is already linked to a Deed account. Unlink it there first, or ask an administrator.'
    case 'forbidden':
      return "The identity provider refused, or the identity does not meet this installation's sign-in rules (tenant, verified email or sign-in method)."
    default:
      return loginErrorMessage(code)
  }
}

export type SetupPurpose = 'bootstrap' | 'invite' | 'reset' | 'recovery'

const PURPOSES: SetupPurpose[] = ['bootstrap', 'invite', 'reset', 'recovery']

/**
 * Reads a single-use link token from the URL fragment (`/setup#bootstrap=deed_setup_…`). Fragments never reach the
 * server or the Referer header; the page removes it from the address bar once read.
 */
export function parseSetupFragment(hash: string): { purpose: SetupPurpose; token: string } | null {
  const params = new URLSearchParams(hash.startsWith('#') ? hash.slice(1) : hash)
  for (const purpose of PURPOSES) {
    const token = params.get(purpose)
    if (token && /^[A-Za-z0-9_\-.]{8,128}$/.test(token)) return { purpose, token }
  }
  return null
}
