import { defineConfig, devices } from '@playwright/test'
import { E2E_BASE_URL, STORAGE_STATE } from './e2e/paths'

/**
 * End-to-end tests against the real backend: global-setup.ts starts `deed serve` with a fresh data directory, consumes
 * the bootstrap link (owner + TOTP), seeds the catalog through the API (e2e/seed.ts) and saves a signed-in session.
 * Build the UI and the binary first (`make web-e2e`), or point DEED_E2E_BINARY at a binary that embeds the current UI.
 */
export default defineConfig({
  testDir: './e2e',
  globalSetup: './e2e/global-setup.ts',
  timeout: 60_000,
  expect: { timeout: 10_000 },
  fullyParallel: false,
  workers: 1,
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? [['list'], ['html', { open: 'never' }]] : 'list',
  use: {
    baseURL: E2E_BASE_URL,
    storageState: STORAGE_STATE,
    trace: 'retain-on-failure',
    viewport: { width: 1440, height: 900 },
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'], viewport: { width: 1440, height: 900 } } }],
})
