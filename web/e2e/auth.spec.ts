import { expect, test } from '@playwright/test'
import { TotpClock } from './totp.ts'
import { readState, ready, writeState } from './helpers'

test.describe('sign-in', () => {
  test.use({ storageState: { cookies: [], origins: [] } })

  test('signs in with password and a TOTP code, then lands where it started', async ({ page }) => {
    const state = readState()
    await page.goto('/inventory?kind=server')
    await expect(page).toHaveURL(/\/login\?return_to=/)
    await expect(page.getByRole('heading', { name: 'Sign in to Deed' })).toBeVisible()
    await page.getByRole('textbox', { name: 'Email' }).fill(state.email)
    await page.locator('input[type="password"]').fill('wrong password, definitely')
    await page.getByRole('button', { name: 'Sign in' }).click()
    await expect(page.getByRole('alert')).toBeVisible()
    await page.locator('input[type="password"]').fill(state.password)
    await page.getByRole('button', { name: 'Sign in' }).click()

    // Second factor: the recovery-code option exists; use the authenticator.
    await expect(page.getByRole('form', { name: 'Two-factor authentication' })).toBeVisible()
    await page.getByRole('button', { name: 'Use a recovery code instead' }).click()
    await expect(page.getByLabel('Recovery code')).toBeVisible()
    await page.getByRole('button', { name: 'Use my authenticator app instead' }).click()
    const clock = new TotpClock(state.totpSecret, state.lastStep)
    const code = await clock.next()
    state.lastStep = clock.last
    writeState(state)
    await page.getByLabel('Authentication code').fill(code)
    await page.getByRole('button', { name: 'Verify' }).click()

    await expect(page).toHaveURL(/\/inventory\?kind=server/)
    await ready(page)
    await expect(page.getByTestId('inventory-row').first()).toBeVisible()

    // Sign out ends the session.
    await page.getByTestId('user-menu').click()
    await page.getByRole('menuitem', { name: 'Sign out' }).click()
    await expect(page).toHaveURL(/\/login/)
  })

  test('explains incomplete or invalid setup links', async ({ page }) => {
    await page.goto('/setup')
    await expect(page.getByText('The link is incomplete')).toBeVisible()
    await page.goto('/login')
    await page.goto('/setup#invite=deed_setup_doesnotexist123')
    await expect(page.getByRole('alert')).toContainText(/invalid|expired|used/i)
    // The token is removed from the address bar once read.
    expect(page.url()).not.toContain('deed_setup_')
  })

  test('maps SSO errors on the login page', async ({ page }) => {
    await page.goto('/login?error=forbidden')
    await expect(page.getByRole('alert')).toContainText('Single sign-on was refused')
  })
})
