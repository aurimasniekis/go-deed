import { expect, test } from '@playwright/test'
import { entityId } from './helpers'

test('map: filter, group, inspect, keyboard and provenance', async ({ page }) => {
  const billing = entityId('service/billing-api')
  await page.goto(`/map?focus=${billing}&mode=neighborhood&depth=2`)
  const canvas = page.getByTestId('map-canvas')
  await expect(canvas).toHaveAttribute('data-layout-ready', 'true', { timeout: 20_000 })
  const before = await page.locator('[data-entity-id]').count()
  expect(before).toBeGreaterThan(5)

  // Legend explains what is on screen.
  await expect(page.getByTestId('map-legend')).toContainText('Line style (evidence)')

  // Filter by kind through the options drawer (URL state).
  await page.getByTestId('map-options-button').click()
  await page.getByTestId('map-filter-kind').click()
  await page.getByRole('option', { name: 'Database', exact: true }).click()
  await page.keyboard.press('Escape')
  await expect(page).toHaveURL(/kind=database/)
  await expect(canvas).toHaveAttribute('data-layout-ready', 'true')
  await expect.poll(async () => page.locator('[data-entity-id]').count()).toBeLessThan(before)

  // Group the loaded nodes by kind.
  await page.getByTestId('map-cluster-select').click()
  await page.getByRole('option', { name: 'Kind' }).click()
  await expect(page).toHaveURL(/cluster=kind/)
  await page.getByRole('button', { name: 'Close' }).first().click()
  await expect(page.getByTestId('map-group').first()).toBeVisible()

  // Select the focus node, inspect it and follow "why" to provenance.
  await page.goto(`/map?focus=${billing}&mode=neighborhood&depth=1`)
  await expect(canvas).toHaveAttribute('data-layout-ready', 'true', { timeout: 20_000 })
  await page.locator(`[data-entity-id="${billing}"]`).click()
  await expect(page.getByTestId('map-inspector')).toBeVisible()
  await expect(page.getByTestId('inspector-title')).toHaveText('Billing API')

  // Keyboard: arrows move the selection to a neighbour, Escape clears it.
  await canvas.focus()
  await page.keyboard.press('ArrowRight')
  await expect(page.getByTestId('inspector-title')).not.toHaveText('Billing API')
  await page.keyboard.press('Escape')
  await expect(page.getByTestId('map-inspector')).toHaveCount(0)

  await page.locator(`[data-entity-id="${billing}"]`).click()
  await page.getByTestId('inspector-why').click()
  await expect(page).toHaveURL(/tab=provenance/)
  await expect(page.getByTestId('provenance-table')).toBeVisible()
  await expect(page.getByTestId('provenance-table')).toContainText('metadata › owner')
})

test('map overview groups expand on click', async ({ page }) => {
  await page.goto('/map?group=kind')
  await expect(page.getByTestId('map-canvas')).toHaveAttribute('data-layout-ready', 'true', { timeout: 20_000 })
  const cluster = page.locator('[data-cluster-key="kind|service"]')
  await expect(cluster).toBeVisible()
  await cluster.click()
  await expect(page).toHaveURL(/expand=kind%7Cservice|expand=kind\|service/)
  await expect(page.getByTestId('map-canvas')).toHaveAttribute('data-layout-ready', 'true', { timeout: 20_000 })
  await expect(page.locator(`[data-entity-id="${entityId('service/billing-api')}"]`)).toBeVisible()
})

test('traffic overlay shows measured rates only on measured edges', async ({ page }) => {
  await page.goto(`/map?focus=${entityId('service/api-gateway')}&mode=neighborhood&depth=1`)
  await expect(page.getByTestId('map-canvas')).toHaveAttribute('data-layout-ready', 'true', { timeout: 20_000 })
  await expect(page.getByTestId('map-stats')).toContainText(/traffic: [1-9]\d* measured edge/, { timeout: 20_000 })
  await expect(page.locator('.deed-edge-label[data-rate="measured"]').first()).toContainText('req/s')
})
