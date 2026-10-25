import { expect, test } from '@playwright/test'
import { entityId, ready } from './helpers'

test('dashboard → finding → inventory → entity → focused map → path', async ({ page }) => {
  await page.goto('/')
  await ready(page)
  await expect(page.getByRole('heading', { name: 'Dashboard', level: 1 })).toBeVisible()
  await expect(page.getByTestId('synthetic-banner')).toContainText('Synthetic demo data')
  await expect(page.getByTestId('tile-entities')).toContainText(/\d+/)

  // A finding on the dashboard links to the findings list for its type.
  await page.getByTestId('tile-findings').getByRole('link').click()
  await expect(page).toHaveURL(/\/findings/)
  await expect(page.getByTestId('finding-row').first()).toBeVisible()
  const certRow = page.getByTestId('finding-row').filter({ hasText: 'Certificate expires' }).first()
  await certRow.getByRole('link', { name: 'Certificate expiring' }).click()
  await expect(page).toHaveURL(/type=certificate_expiring/)

  // Inventory, filtered through the URL (shareable state).
  await page.goto('/inventory?kind=service&sort=name')
  await ready(page)
  await expect(page.getByTestId('inventory-count')).toContainText('entities')
  await page.getByTestId('inventory-search').fill('billing')
  await expect(page).toHaveURL(/q=billing/)
  const row = page.getByTestId('inventory-row').filter({ hasText: 'Billing API' })
  await expect(row).toHaveCount(1)
  await row.getByRole('link', { name: 'Billing API' }).click()

  // Entity detail.
  await expect(page.getByTestId('entity-title')).toHaveText('Billing API')
  await expect(page.getByText('Declared and observed differ')).toBeVisible()
  await page.getByRole('tab', { name: 'Relations' }).click()
  await expect(page.getByText('Outgoing (this entity → others)')).toBeVisible()
  await page.getByRole('tab', { name: 'Operational' }).click()
  await expect(page.getByTestId('telemetry-card').first()).toBeVisible()
  await expect(page.getByTestId('telemetry-card').first()).toContainText(/Measured|Inferred|Stale|Unavailable/)

  // Focused map.
  await page.getByTestId('open-in-map').click()
  await expect(page).toHaveURL(/\/map\?.*focus=/)
  await expect(page.getByTestId('map-canvas')).toHaveAttribute('data-layout-ready', 'true', { timeout: 20_000 })
  await expect(page.locator('[data-entity-id]').first()).toBeVisible()
  await expect(page.getByLabel('Exploration path').getByText('Billing API')).toBeVisible()

  // Path finder between two seeded entities.
  await page.goto(`/map?mode=path&from=${entityId('service/api-gateway')}&to=${entityId('database/postgres-main')}`)
  await expect(page.getByTestId('map-canvas')).toHaveAttribute('data-layout-ready', 'true', { timeout: 20_000 })
  await expect(page.locator(`[data-entity-id="${entityId('service/api-gateway')}"]`)).toBeVisible()
  await expect(page.locator(`[data-entity-id="${entityId('database/postgres-main')}"]`)).toBeVisible()
  await expect(page.getByText('Path', { exact: true }).first()).toBeVisible()
})

test('impact traversal is labelled as potential, evidence-based impact', async ({ page }) => {
  await page.goto(`/map?mode=impact&impact=downstream&focus=${entityId('database/postgres-main')}`)
  await expect(page.getByTestId('impact-banner')).toContainText('Potential impact, evidence-based')
  await expect(page.getByTestId('map-canvas')).toHaveAttribute('data-layout-ready', 'true', { timeout: 20_000 })
})

test('inventory bulk edit writes UI annotations with per-row results', async ({ page }) => {
  await page.goto('/inventory?kind=certificate')
  await ready(page)
  await page.getByRole('checkbox', { name: 'Select all loaded rows' }).check()
  await expect(page.getByTestId('bulk-bar')).toContainText('3 selected')
  await page.getByRole('button', { name: 'Bulk edit' }).click()
  const labels = page.getByRole('textbox', { name: 'Labels (key=value)' })
  await labels.fill('renewal=auto')
  await labels.press('Enter')
  await page.getByTestId('bulk-apply').click()
  await page.getByTestId('bulk-confirm').click()
  await expect(page.getByText('3 updated, 0 failed')).toBeVisible()
})

test('relations table is the accessible equivalent of the map', async ({ page }) => {
  await page.goto(`/relations?entity=${entityId('service/billing-api')}`)
  await ready(page)
  await expect(page.getByTestId('relations-table')).toBeVisible()
  await expect(page.getByTestId('relations-table').getByRole('row')).not.toHaveCount(0)
})
