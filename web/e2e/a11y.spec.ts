import { expect, test, type Page } from '@playwright/test'
import { entityId, expectAccessible, ready, readState, screenshot, useScheme } from './helpers'

interface View {
  name: string
  path: () => string
  wait: (page: Page) => Promise<void>
}

const shell = async (page: Page) => ready(page)
const mapReady = async (page: Page) => {
  await expect(page.getByTestId('map-canvas')).toHaveAttribute('data-layout-ready', 'true', { timeout: 20_000 })
}

/** Every main view, scanned with axe and captured in both themes. */
const views: View[] = [
  { name: 'dashboard', path: () => '/', wait: shell },
  { name: 'inventory', path: () => '/inventory', wait: shell },
  { name: 'entity-overview', path: () => `/entities/${entityId('service/billing-api')}`, wait: shell },
  { name: 'entity-compare', path: () => `/entities/${entityId('server/srv-hel1-02')}?tab=compare`, wait: shell },
  { name: 'entity-provenance', path: () => `/entities/${entityId('server/srv-hel1-02')}?tab=provenance`, wait: shell },
  { name: 'entity-relations', path: () => `/entities/${entityId('service/billing-api')}?tab=relations`, wait: shell },
  {
    name: 'entity-operational',
    path: () => `/entities/${entityId('service/billing-api')}?tab=operational`,
    wait: shell,
  },
  { name: 'map-overview', path: () => '/map?group=kind', wait: mapReady },
  {
    name: 'map-focus',
    path: () => `/map?focus=${entityId('service/billing-api')}&mode=neighborhood&depth=2`,
    wait: mapReady,
  },
  { name: 'relations', path: () => `/relations?entity=${entityId('service/billing-api')}`, wait: shell },
  { name: 'findings', path: () => '/findings', wait: shell },
  { name: 'alerts', path: () => '/alerts', wait: shell },
  { name: 'versions', path: () => '/versions', wait: shell },
  { name: 'sources', path: () => '/sources', wait: shell },
  { name: 'source-detail', path: () => `/sources/${readState().seed.sources['docker-agent']?.id ?? ''}`, wait: shell },
  { name: 'kinds', path: () => '/kinds', wait: shell },
  { name: 'kind-detail', path: () => '/kinds/backup_job', wait: shell },
  { name: 'relationship-types', path: () => '/relationship-types', wait: shell },
  { name: 'settings-icons', path: () => '/settings/icons', wait: shell },
  { name: 'settings-projects', path: () => '/settings/projects', wait: shell },
  { name: 'settings-project', path: () => '/settings/projects/home', wait: shell },
  { name: 'settings-bridges', path: () => '/settings/bridges', wait: shell },
  { name: 'settings-users', path: () => '/settings/users', wait: shell },
  { name: 'settings-account', path: () => '/settings/account', wait: shell },
  { name: 'settings-audit', path: () => '/settings/audit', wait: shell },
  { name: 'settings-telemetry', path: () => '/settings/telemetry', wait: shell },
  { name: 'settings-backups', path: () => '/settings/backups', wait: shell },
  { name: 'settings-about', path: () => '/settings/about', wait: shell },
]

for (const scheme of ['light', 'dark'] as const) {
  test.describe(`${scheme} theme`, () => {
    for (const v of views) {
      test(`${v.name} is accessible`, async ({ page }) => {
        await page.goto('/settings/about')
        await useScheme(page, scheme)
        await page.goto(v.path())
        await v.wait(page)
        await expect(page.locator('html')).toHaveAttribute('data-mantine-color-scheme', scheme)
        await page.waitForTimeout(400)
        await screenshot(page, `${v.name}-${scheme}`)
        await expectAccessible(page, `${v.name} (${scheme})`)
      })
    }
  })
}

/** States the route scan does not reach: the map coloured by source with an edge's sources open, and the built-in
 * relationship-type colour dialog. */
for (const scheme of ['light', 'dark'] as const) {
  test(`colour by source and the built-in colour dialog are accessible (${scheme})`, async ({ page }) => {
    await page.goto('/settings/about')
    await useScheme(page, scheme)
    await page.evaluate(() => {
      window.localStorage.setItem('deed.ui.map.style', JSON.stringify({ colorMode: 'source' }))
    })
    await page.goto(`/map?focus=${entityId('service/billing-api')}&mode=neighborhood&depth=1`)
    await mapReady(page)
    await page.locator('.react-flow__edge').first().locator('path').first().dispatchEvent('click')
    await expect(page.getByTestId('edge-sources')).toBeVisible()
    await expect(page.getByTestId('map-legend')).toContainText('Colour: the primary source')
    await page.waitForTimeout(400)
    await screenshot(page, `map-source-${scheme}`)
    await expectAccessible(page, `map coloured by source (${scheme})`)

    await page.goto('/relationship-types')
    await ready(page)
    await page.getByRole('button', { name: 'Change colour of Runs on' }).click()
    await expect(page.getByRole('dialog')).toContainText('Built-in default')
    await expectAccessible(page, `built-in colour dialog (${scheme})`)
  })
}

test.describe('signed out', () => {
  test.use({ storageState: { cookies: [], origins: [] } })
  for (const scheme of ['light', 'dark'] as const) {
    test(`login is accessible (${scheme})`, async ({ page }) => {
      await page.emulateMedia({ colorScheme: scheme })
      await page.goto('/login')
      await expect(page.getByRole('heading', { name: 'Sign in to Deed' })).toBeVisible()
      await screenshot(page, `login-${scheme}`)
      await expectAccessible(page, `login (${scheme})`)
    })
  }
})
