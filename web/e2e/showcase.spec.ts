import { expect, test, type Page } from '@playwright/test'
import { entityId, ready, screenshot, useScheme } from './helpers'

/**
 * Feature screenshots for the README, the documentation site and docs/UI.md, in both themes: map states the route scan
 * in a11y.spec.ts does not reach (metrics in cards, show / hide, quick actions, grouping, mask mode) and label rules.
 * Accessibility of the same states is covered by map.spec.ts.
 */

const mapReady = async (page: Page) => {
  await expect(page.getByTestId('map-canvas')).toHaveAttribute('data-layout-ready', 'true', { timeout: 20_000 })
}

async function open(page: Page, scheme: 'light' | 'dark', path: string) {
  await page.goto('/settings/about')
  await useScheme(page, scheme)
  await page.goto(path)
}

/** Lets the fit-view animation settle before the capture. */
const settle = (page: Page) => page.waitForTimeout(600)

for (const scheme of ['light', 'dark'] as const) {
  test.describe(`${scheme} theme`, () => {
    test('map: metrics inside node cards', async ({ page }) => {
      const gateway = entityId('service/api-gateway')
      await open(page, scheme, `/map?focus=${gateway}&mode=neighborhood&depth=1`)
      await mapReady(page)
      await expect(page.locator(`.deed-node[data-entity-id="${gateway}"] [data-testid="node-metrics"]`)).toContainText(
        'req/s',
        { timeout: 20_000 },
      )
      await page.getByRole('button', { name: 'Hide legend' }).click()
      await page.getByTestId('map-canvas').focus()
      await page.keyboard.press('f')
      await settle(page)
      await screenshot(page, `map-metrics-${scheme}`)
    })

    test('map: show / hide panel', async ({ page }) => {
      await open(
        page,
        scheme,
        `/map?focus=${entityId('service/billing-api')}&mode=neighborhood&depth=1&hideType=owned_by`,
      )
      await mapReady(page)
      await page.getByRole('button', { name: 'Hide legend' }).click()
      await page.getByTestId('map-canvas').focus()
      await page.keyboard.press('v')
      await expect(page.getByTestId('map-visibility')).toBeVisible()
      await settle(page)
      await screenshot(page, `map-show-hide-${scheme}`)
    })

    test('map: right-click quick actions', async ({ page }) => {
      const billing = entityId('service/billing-api')
      await open(page, scheme, `/map?focus=${billing}&mode=neighborhood&depth=1`)
      await mapReady(page)
      await page.getByRole('button', { name: 'Hide legend' }).click()
      await settle(page)
      await page.locator(`[data-entity-id="${billing}"]`).click({ button: 'right' })
      await expect(page.getByTestId('map-context-menu')).toContainText('Billing API')
      await screenshot(page, `map-quick-actions-${scheme}`)
    })

    test('map: overview grouped by a label', async ({ page }) => {
      await open(page, scheme, '/map?mode=overview&group=label:tier')
      await mapReady(page)
      await page.getByRole('button', { name: 'Hide legend' }).click()
      for (const value of ['core', 'compute']) {
        await page.locator(`[data-cluster-key="label:tier|${value}"]`).click()
        await mapReady(page)
      }
      await settle(page)
      await screenshot(page, `map-grouped-${scheme}`)
    })

    test('map: mask mode', async ({ page }) => {
      const billing = entityId('service/billing-api')
      await open(page, scheme, `/map?focus=${billing}&mode=neighborhood&depth=1`)
      await mapReady(page)
      await page.getByTestId('map-mask').click()
      await page.locator(`[data-entity-id="${billing}"]`).click()
      await expect(page.getByText('Details are hidden in mask mode.')).toBeVisible()
      await settle(page)
      await screenshot(page, `map-mask-${scheme}`)
    })
  })
}

test('project page: label rules', async ({ page }) => {
  await page.goto('/settings/projects/home')
  await ready(page)
  await page.getByTestId('label-rule-new').click()
  const modal = page.getByRole('dialog')
  await modal.getByLabel('Name').fill('dns')
  const kinds = modal.getByRole('textbox', { name: 'Kinds' })
  await kinds.fill('dns_*')
  await kinds.press('Enter')
  await modal.getByTestId('label-rule-labels').fill('group=dns')
  await modal.getByTestId('label-rule-save').click()
  await expect(modal).toBeHidden()
  const table = page.getByRole('table', { name: 'Label rules' })
  await expect(table).toContainText('group=dns')

  for (const scheme of ['light', 'dark'] as const) {
    await useScheme(page, scheme)
    await page.reload()
    await ready(page)
    await table.scrollIntoViewIfNeeded()
    await page.waitForTimeout(400)
    await screenshot(page, `project-label-rules-${scheme}`)
  }

  await page.getByRole('button', { name: 'Delete dns' }).click()
  await page.getByRole('dialog').getByRole('button', { name: 'Delete rule' }).click()
  await expect(page.getByText('No label rules.')).toBeVisible()
})
