import { expect, test } from '@playwright/test'
import { entityId, readState, ready } from './helpers'

test('sources: create a GitHub source, test the connection and preview the import', async ({ page }) => {
  const { mocks } = readState()
  await page.goto('/sources/new?provider=github')
  await ready(page)
  await page.getByTestId('source-name').fill('github-mock')
  await page.locator('[data-field="api_url"]').fill(mocks)
  await page.locator('[data-field="owners"]').fill('example-org')
  await page.locator('[data-field="owners"]').press('Enter')
  await page.locator('[data-field="allow_cidrs"]').fill('127.0.0.0/8')
  await page.locator('[data-field="allow_cidrs"]').press('Enter')
  await page.getByRole('switch', { name: 'Insecure http' }).check()
  await page.getByTestId('source-save').click()
  await expect(page).toHaveURL(/\/sources\/src_/)

  await page.getByTestId('source-test').click()
  await expect(page.getByTestId('test-result')).toContainText('Connection test passed')
  await expect(page.getByTestId('test-result')).toContainText('repositories reachable')

  await page.getByTestId('source-preview').click()
  await expect(page.getByTestId('preview-result')).toContainText('Dry run')
  await expect(page.getByTestId('preview-result')).toContainText('Created')
  await expect(page.getByTestId('preview-result')).toContainText('repository/')
})

test('sources: push source tokens are shown once', async ({ page }) => {
  await page.goto('/sources/new?provider=push')
  await ready(page)
  await page.getByTestId('source-name').fill('ci-jobs')
  await page.getByTestId('source-save').click()
  await expect(page).toHaveURL(/\/sources\/src_/)
  await page.getByRole('tab', { name: 'Tokens' }).click()
  await page.getByTestId('token-name').fill('ci runner')
  await page.getByTestId('create-token').click()
  await expect(page.getByTestId('one-time-secret')).toContainText('deed_st_')
  await page.getByRole('button', { name: 'I have stored it' }).click()
  await expect(page.getByRole('cell', { name: 'ci runner' })).toBeVisible()
})

test('kinds: create a custom kind with a schema dry run', async ({ page }) => {
  await page.goto('/kinds/new')
  await ready(page)
  await page.getByTestId('kind-name').fill('feature_flag')
  await page.getByTestId('kind-title').fill('Feature flag')
  await page.getByTestId('schema-editor').fill(
    JSON.stringify({
      type: 'object',
      properties: { enabled: { type: 'boolean' }, rollout: { type: 'integer', minimum: 0, maximum: 100 } },
      required: ['enabled'],
    }),
  )
  await page.getByLabel('Sample spec (optional)').fill('{"enabled": true, "rollout": 250}')
  await page.getByTestId('schema-check').click()
  await expect(page.getByTestId('schema-check-result')).toContainText('The sample fails validation')
  await page.getByLabel('Sample spec (optional)').fill('{"enabled": true, "rollout": 25}')
  await page.getByTestId('schema-check').click()
  await expect(page.getByTestId('schema-check-result')).toContainText('The sample spec validates')
  await page.getByTestId('create-kind').click()
  await expect(page).toHaveURL(/\/kinds\/feature_flag/)
  await expect(page.getByRole('heading', { name: 'Feature flag' })).toBeVisible()
  await page.goto('/kinds')
  await expect(page.getByRole('link', { name: 'Feature flag' })).toBeVisible()
})

test('relationship types: change and reset the colour of a built-in type', async ({ page }) => {
  const legendEntry = (p: typeof page) =>
    p.getByTestId('map-legend').locator('[data-legend-key="monitored_by"]', { hasText: 'Monitored by' })
  const map = `/map?focus=${entityId('service/billing-api')}&mode=neighborhood&depth=1`
  await page.goto(map)
  await expect(page.getByTestId('map-canvas')).toHaveAttribute('data-layout-ready', 'true', { timeout: 20_000 })
  const before = await legendEntry(page).getAttribute('data-legend-color')

  await page.goto('/relationship-types')
  await ready(page)
  const row = page.getByRole('row', { name: /Monitored by/ })
  await page.getByRole('button', { name: 'Change colour of Monitored by' }).click()
  const dialog = page.getByRole('dialog')
  await expect(dialog).toContainText(`Built-in default: ${before ?? ''}`)
  await page.getByTestId('reltype-color-input').fill('#123abc')
  await dialog.getByRole('button', { name: 'Save' }).click()
  await expect(dialog).toBeHidden()
  await expect(row).toContainText('colour changed')

  // The map's default edge colour (and its legend) follow.
  await page.goto(map)
  await expect(page.getByTestId('map-canvas')).toHaveAttribute('data-layout-ready', 'true', { timeout: 20_000 })
  await expect(legendEntry(page)).toHaveAttribute('data-legend-color', '#123abc')

  // Reset restores the built-in default.
  await page.goto('/relationship-types')
  await ready(page)
  await page.getByRole('button', { name: 'Change colour of Monitored by' }).click()
  await page.getByRole('dialog').getByRole('button', { name: 'Reset to default' }).click()
  await expect(page.getByRole('dialog')).toBeHidden()
  await expect(row).not.toContainText('colour changed')
  await page.goto(map)
  await expect(page.getByTestId('map-canvas')).toHaveAttribute('data-layout-ready', 'true', { timeout: 20_000 })
  await expect(legendEntry(page)).toHaveAttribute('data-legend-color', before ?? '')
})

test('audit log verifies the hash chain', async ({ page }) => {
  await page.goto('/settings/audit')
  await ready(page)
  await expect(page.getByTestId('audit-table')).toBeVisible()
  await page.getByTestId('verify-audit').click()
  await expect(page.getByTestId('verify-result')).toContainText('The audit chain is intact')
})

test('saved views: save the inventory state and re-open it', async ({ page }) => {
  await page.goto('/inventory?kind=container&health=failed,warning')
  await ready(page)
  await page.getByTestId('save-view').click()
  await page.getByRole('dialog').getByRole('textbox', { name: 'Name' }).fill('Unhealthy containers')
  await page.getByRole('button', { name: 'Save view' }).click()
  await expect(page.getByTestId('views-menu')).toHaveText(/Unhealthy containers/)
  await page.goto('/inventory')
  await page.getByTestId('views-menu').click()
  await page.getByRole('menuitem', { name: /Unhealthy containers/ }).click()
  await expect(page).toHaveURL(/health=failed%2Cwarning|health=failed,warning/)
})

test('versions: re-check shows the newer catalog release', async ({ page }) => {
  await page.goto('/versions')
  await ready(page)
  await page.getByTestId('recheck-versions').click()
  await expect(page.getByText(/Checked \d+ entities/)).toBeVisible()
  const row = page.getByTestId('versions-table').getByRole('row', { name: /billing-api/ })
  await expect(row).toContainText('Update available')
  await expect(row).toContainText('v2.5.0')
  await page.getByTestId('version-count-update_available').click()
  await expect(page).toHaveURL(/status=update_available/)
  await expect(page.getByTestId('versions-table').getByRole('row')).toHaveCount(2)
})

test('icons: upload a monochrome SVG and use its reference', async ({ page }) => {
  await page.goto('/settings/icons')
  await ready(page)
  await page.getByTestId('upload-icon').click()
  await page.getByTestId('icon-name').fill('e2e-rack')
  await page
    .getByTestId('icon-svg')
    .fill(
      '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><rect x="4" y="3" width="16" height="18" fill="currentColor"/></svg>',
    )
  await page.getByTestId('icon-save').click()
  const card = page.getByTestId('icon-e2e-rack')
  await expect(card).toContainText('custom:e2e-rack')
  await expect(card).toContainText('tinted')
})

test('findings: owners can re-evaluate now', async ({ page }) => {
  await page.goto('/findings')
  await ready(page)
  await page.getByTestId('evaluate-findings').click()
  await expect(page.getByText('Findings and alerts re-evaluated')).toBeVisible()
})
