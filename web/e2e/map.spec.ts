import { expect, test } from '@playwright/test'
import { entityId, expectAccessible, readState } from './helpers'

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

test('map: colour relationships by their primary source', async ({ page }) => {
  await page.goto(`/map?focus=${entityId('service/billing-api')}&mode=neighborhood&depth=1`)
  const canvas = page.getByTestId('map-canvas')
  await expect(canvas).toHaveAttribute('data-layout-ready', 'true', { timeout: 20_000 })
  await page.getByTestId('map-options-button').click()
  await page.getByTestId('map-color-mode').click()
  await page.getByRole('option', { name: 'Source (primary asserting source)' }).click()
  await expect(page.getByTestId('map-options')).toContainText('primary source')
  await page.getByRole('button', { name: 'Close' }).first().click()

  // The legend names each source colour on screen and how it is chosen.
  const legend = page.getByTestId('map-legend')
  const api = legend.locator('[data-legend-key^="source:src_"]', {
    hasText: 'API declarations (declared layer, Home infrastructure)',
  })
  await expect(api).toBeVisible()
  await expect(api).toHaveCount(1)
  await expect(legend).toContainText('Colour: the primary source')
  const color = await api.getAttribute('data-legend-color')
  expect(color).toMatch(/^#[0-9a-f]{6}$/)

  // Every relationship names its sources in its accessible name and in the inspector.
  const edge = page.locator('.react-flow__edge[aria-label*="Depends on"]').first()
  await expect(edge).toHaveAttribute('aria-label', /sources: API declarations \(Home infrastructure, primary\)/)
  await edge.locator('path').first().dispatchEvent('click')
  await expect(page.getByTestId('edge-sources')).toContainText('API declarations')
  await expect(page.getByTestId('edge-sources')).toContainText('primary')

  // The colour belongs to the source: the same after a reload.
  await page.reload()
  await expect(canvas).toHaveAttribute('data-layout-ready', 'true', { timeout: 20_000 })
  await expect(
    page.getByTestId('map-legend').locator('[data-legend-key^="source:src_"]', {
      hasText: 'API declarations (declared layer, Home infrastructure)',
    }),
  ).toHaveAttribute('data-legend-color', color ?? '')
})

test('map overview groups expand on click', async ({ page }) => {
  await page.goto('/map?group=kind')
  await expect(page.getByTestId('map-canvas')).toHaveAttribute('data-layout-ready', 'true', { timeout: 20_000 })
  const cluster = page.locator('[data-cluster-key="kind|service"]')
  await expect(cluster).toBeVisible()
  await cluster.click()
  await expect(page).toHaveURL(/expand=kind%7Cservice|expand=kind\|service/)
  await expect(page.getByTestId('map-canvas')).toHaveAttribute('data-layout-ready', 'true', { timeout: 20_000 })
  // Every member of the group appears (repeated `root` parameters used to keep only the first), and nothing is dimmed:
  // the clicked cluster is gone, so it no longer drives the neighbour highlight.
  const services = Object.keys(readState().seed.entities).filter((k) => k.startsWith('service/'))
  expect(services.length).toBeGreaterThan(1)
  for (const key of services) await expect(page.locator(`[data-entity-id="${entityId(key)}"]`)).toBeVisible()
  await expect(page.locator('.react-flow__node [data-dimmed]')).toHaveCount(0)
})

test('map options can be switched without breaking the page', async ({ page }) => {
  await page.goto(`/map?focus=${entityId('service/billing-api')}&mode=neighborhood&depth=2`)
  await expect(page.getByTestId('map-canvas')).toHaveAttribute('data-layout-ready', 'true', { timeout: 20_000 })
  await page.getByTestId('map-options-button').click()
  const drawer = page.getByTestId('map-options')
  const ok = async (what: string) => {
    await page.waitForTimeout(300)
    expect(await page.getByText('Something went wrong on this page').count(), what).toBe(0)
  }
  // Turning the traffic overlay off used to loop renders (React error #185).
  await page.getByTestId('traffic-toggle').click()
  await ok('traffic off')
  await page.getByTestId('traffic-toggle').click()
  await ok('traffic on')
  for (const label of ['Mid-edge type labels', 'Direction hint (marching dashes — not traffic)']) {
    await drawer.getByLabel(label).click()
    await ok(label)
    await drawer.getByLabel(label).click()
  }
  for (const option of ['Smooth step', 'Straight', 'Bezier', 'Force', 'Radial', 'Layered']) {
    await drawer.getByText(option, { exact: true }).click()
    await ok(option)
  }
})

test('traffic overlay shows measured rates only on measured edges', async ({ page }) => {
  await page.goto(`/map?focus=${entityId('service/api-gateway')}&mode=neighborhood&depth=1`)
  await expect(page.getByTestId('map-canvas')).toHaveAttribute('data-layout-ready', 'true', { timeout: 20_000 })
  await expect(page.getByTestId('map-stats')).toContainText(/traffic: [1-9]\d* measured edge/, { timeout: 20_000 })
  await expect(page.locator('.deed-edge-label[data-rate="measured"]').first()).toContainText('req/s')
})

test('map context menu: right-click quick actions', async ({ page }) => {
  const billing = entityId('service/billing-api')
  await page.goto(`/map?focus=${billing}&mode=neighborhood&depth=1`)
  await expect(page.getByTestId('map-canvas')).toHaveAttribute('data-layout-ready', 'true', { timeout: 20_000 })
  const menu = page.getByTestId('map-context-menu')

  // An entity: the menu names it and offers its quick actions.
  await page.locator(`[data-entity-id="${billing}"]`).click({ button: 'right' })
  await expect(menu).toBeVisible()
  await expect(menu).toContainText('Billing API')
  for (const item of ['Open details', 'Focus map on this', 'Expand neighbours', 'Path from here…', 'Copy ID']) {
    await expect(menu.getByRole('menuitem', { name: item })).toBeVisible()
  }
  await expectAccessible(page, 'map context menu')
  await page.keyboard.press('Escape')
  await expect(menu).toBeHidden()

  // The canvas.
  await page
    .getByTestId('map-canvas')
    .locator('.react-flow__pane')
    .click({ button: 'right', position: { x: 30, y: 30 } })
  await expect(menu.getByRole('menuitem', { name: 'Fit view' })).toBeVisible()
  // Clicking away closes it: on the canvas (whose pan handler swallows mousedown) and on a node.
  await page
    .getByTestId('map-canvas')
    .locator('.react-flow__pane')
    .click({ position: { x: 60, y: 200 } })
  await expect(menu).toBeHidden()
  await page.locator(`[data-entity-id="${billing}"]`).click({ button: 'right' })
  await expect(menu).toBeVisible()
  // A node the open menu does not cover.
  const mb = await menu.boundingBox()
  const others = page.locator(`[data-entity-id]:not([data-entity-id="${billing}"])`)
  let target = -1
  for (let i = 0; i < (await others.count()) && target < 0; i++) {
    const b = await others.nth(i).boundingBox()
    if (b && mb && (b.x + b.width < mb.x || b.x > mb.x + mb.width || b.y + b.height < mb.y || b.y > mb.y + mb.height))
      target = i
  }
  expect(target).toBeGreaterThanOrEqual(0)
  await others.nth(target).click()
  await expect(menu).toBeHidden()

  // From the keyboard on the selected node, then "Open details" goes to the entity page.
  await page.locator(`[data-entity-id="${billing}"]`).click()
  await page.getByTestId('map-canvas').focus()
  await page.keyboard.press('Shift+F10')
  await expect(menu).toContainText('Billing API')
  await menu.getByRole('menuitem', { name: 'Open details' }).click()
  await expect(page).toHaveURL(new RegExp(`/entities/${billing}`))
})

test('map: a large overview group expands, with its members drawn together; a group of one is the entity', async ({
  page,
}) => {
  await page.goto('/map?group=kind')
  await expect(page.getByTestId('map-canvas')).toHaveAttribute('data-layout-ready', 'true', { timeout: 20_000 })
  // 60 members: more than one /graph/neighborhood request may carry (50 roots), which used to fail with a 422.
  const created = await page.evaluate(async () => {
    const me = (await (await fetch('/api/v1/me')).json()) as { csrf_token: string }
    const post = (path: string, body: unknown) =>
      fetch(`/api/v1${path}`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'X-Deed-CSRF': me.csrf_token },
        body: JSON.stringify(body),
      })
    await post('/projects', { name: 'e2e-bulk', synthetic: true })
    let ok = 0
    for (let i = 0; i < 60; i++) {
      const r = await post('/entities', {
        project: 'e2e-bulk',
        kind: 'nats_consumer',
        name: `bulk-${i}`,
        source: 'api',
      })
      if (r.ok) ok++
    }
    // One entity of a kind nothing else uses: shown as itself, not as a group of one.
    await post('/entities', { project: 'e2e-bulk', kind: 'ha_integration', name: 'only-site', source: 'api' })
    return ok
  })
  expect(created).toBe(60)
  await page.reload()
  await expect(page.getByTestId('map-canvas')).toHaveAttribute('data-layout-ready', 'true', { timeout: 20_000 })
  await expect(page.locator('[data-cluster-key="kind|ha_integration"]')).toHaveCount(0)
  await expect(page.locator('.react-flow__node').filter({ hasText: 'only-site' })).toBeVisible()

  const problems: string[] = []
  page.on('response', (r) => {
    if (r.url().includes('/api/v1/graph/') && r.status() >= 400) problems.push(`${r.status()} ${r.url()}`)
  })
  await page.locator('[data-cluster-key="kind|nats_consumer"]').click()
  await expect(page.getByTestId('map-canvas')).toHaveAttribute('data-layout-ready', 'true', { timeout: 30_000 })
  const group = page.getByTestId('map-group').filter({ hasText: 'nats_consumer' })
  await expect(group).toBeVisible()
  await expect(group).toContainText('60')
  expect(problems).toEqual([])
  // Collapsing the container undoes the expansion.
  await group.getByRole('button', { name: /Collapse group/ }).click()
  await expect(page).not.toHaveURL(/expand=/)
  await expect(page.locator('[data-cluster-key="kind|nats_consumer"]')).toBeVisible()
})

test('map: mask mode hides names for screenshots', async ({ page }) => {
  const billing = entityId('service/billing-api')
  await page.goto(`/map?focus=${billing}&mode=neighborhood&depth=2`)
  const canvas = page.getByTestId('map-canvas')
  await expect(canvas).toHaveAttribute('data-layout-ready', 'true', { timeout: 20_000 })
  await expect(canvas).toContainText('Billing API')
  await page.getByTestId('map-mask').click()
  await expect(page.getByTestId('map-mask')).toHaveAttribute('aria-pressed', 'true')
  await expect(canvas.locator(`[data-entity-id="${billing}"]`)).toContainText(/service-[0-9a-z]{4}/)
  // Nothing identifying anywhere on the page: entity names, project names, owners.
  await page.locator(`[data-entity-id="${billing}"]`).click()
  await expect(page.getByTestId('inspector-title')).toHaveText(/service-[0-9a-z]{4}/)
  await expect(page.getByText('Details are hidden in mask mode.')).toBeVisible()
  const text = await page.locator('body').innerText()
  for (const secret of ['Billing API', 'billing-api', 'Home infrastructure', 'payments'])
    expect(text).not.toContain(secret)
  await expectAccessible(page, 'map in mask mode')
  if (process.env.DEED_E2E_SCREENSHOTS) await page.screenshot({ path: `${process.env.DEED_E2E_SCREENSHOTS}/mask.png` })
  // The shortcut turns it off again (and it is remembered per browser).
  await canvas.focus()
  await page.keyboard.press('m')
  await expect(canvas).toContainText('Billing API')
})
