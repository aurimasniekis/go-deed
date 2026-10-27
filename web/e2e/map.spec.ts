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

test('node metrics: on-map bindings inside detailed cards', async ({ page }) => {
  const gateway = entityId('service/api-gateway')
  await page.goto(`/map?focus=${gateway}&mode=neighborhood&depth=1`)
  await expect(page.getByTestId('map-canvas')).toHaveAttribute('data-layout-ready', 'true', { timeout: 20_000 })
  const card = page.locator(`.deed-node[data-entity-id="${gateway}"]`)
  const strip = card.locator('[data-testid="node-metrics"]')
  await expect(strip).toContainText('req/s', { timeout: 20_000 })
  // Inside the card, which the layout made taller for its metric row.
  await expect.poll(async () => (await card.boundingBox())?.height ?? 0).toBeGreaterThan(80)
  const cardBox = await card.boundingBox()
  const stripBox = await strip.boundingBox()
  expect(cardBox && stripBox && stripBox.y + stripBox.height <= cardBox.y + cardBox.height + 1).toBe(true)
  await expect(strip).toHaveAttribute('aria-label', /Requests per second [\d.]+ req\/s/)
  await expect(strip).toHaveAttribute('aria-label', /Error ratio [\d.]+%/)
  // The options drawer switches them off.
  await page.getByTestId('map-options-button').click()
  await page.getByTestId('metrics-toggle').click()
  await expect(strip).toHaveCount(0)
  await page.getByTestId('metrics-toggle').click()
  await expect(strip).toContainText('req/s')
})

test('legend: clicking a relationship type hides its edges, clicking again shows them', async ({ page }) => {
  await page.goto(`/map?focus=${entityId('service/api-gateway')}&mode=neighborhood&depth=1`)
  await expect(page.getByTestId('map-canvas')).toHaveAttribute('data-layout-ready', 'true', { timeout: 20_000 })
  const edges = () => page.locator('.react-flow__edge').count()
  const before = await edges()
  const entry = page.getByTestId('map-legend').locator('[data-legend-key="routes_to"]')
  await expect(entry).toHaveAttribute('aria-pressed', 'true')
  await entry.click()
  await expect(page).toHaveURL(/hideType=routes_to/)
  await expect(entry).toHaveAttribute('aria-pressed', 'false')
  await expect.poll(edges).toBeLessThan(before)
  await expect(page.getByTestId('map-visibility-toggle')).toContainText('Hidden 1')
  await entry.click()
  await expect(page).not.toHaveURL(/hideType=/)
  await expect.poll(edges).toBe(before)
})

test('show / hide panel: kinds and relationship types', async ({ page }) => {
  await page.goto(`/map?focus=${entityId('service/billing-api')}&mode=neighborhood&depth=2`)
  const canvas = page.getByTestId('map-canvas')
  await expect(canvas).toHaveAttribute('data-layout-ready', 'true', { timeout: 20_000 })
  await canvas.focus()
  await page.keyboard.press('v')
  const panel = page.getByTestId('map-visibility')
  await expect(panel).toBeVisible()
  const kinds = panel.getByTestId('map-visibility-kinds')
  const nodes = () => page.locator('.react-flow__node[data-id^="ent_"]').count()
  const before = await nodes()
  // Hide one kind: its entities leave the map (the focus stays), the entry stays listed to show it again.
  const box = kinds.locator('input[type="checkbox"]').nth(1)
  const kind = await box.getAttribute('data-key')
  await box.click()
  await expect(page).toHaveURL(new RegExp(`hideKind=${kind}`))
  await expect.poll(nodes).toBeLessThan(before)
  await expect(kinds.locator(`input[data-key="${kind}"]`)).not.toBeChecked()
  await expectAccessible(page, 'map show / hide panel')
  await kinds.getByRole('button', { name: 'Show all' }).click()
  await expect.poll(nodes).toBe(before)
  await expect(page).not.toHaveURL(/hideKind=/)
})

test('copy image: the whole map at 1:1, on the clipboard', async ({ page }) => {
  await page.context().grantPermissions(['clipboard-read', 'clipboard-write'])
  await page.goto('/map?mode=overview')
  const canvas = page.getByTestId('map-canvas')
  await expect(canvas).toHaveAttribute('data-layout-ready', 'true', { timeout: 20_000 })
  const zoom = () =>
    canvas.locator('.react-flow__viewport').evaluate((el) => new DOMMatrixReadOnly(getComputedStyle(el).transform).a)
  // The view once the initial fit has finished animating.
  let shown = await zoom()
  for (let prev = -1; Math.abs(prev - shown) > 1e-6;) {
    prev = shown
    await page.waitForTimeout(300)
    shown = await zoom()
  }
  await page.getByTestId('map-copy-image').click()
  await expect(page.getByText(/Map copied to the clipboard \(\d+ × \d+ px, 1:1\)/)).toBeVisible({ timeout: 20_000 })
  const img = await page.evaluate(async () => {
    const items = await navigator.clipboard.read()
    const blob = await items[0]?.getType('image/png')
    if (!blob) return null
    const bmp = await createImageBitmap(blob)
    const bytes = new Uint8Array(await blob.arrayBuffer())
    let bin = ''
    for (const b of bytes) bin += String.fromCharCode(b)
    return { width: bmp.width, height: bmp.height, png: btoa(bin) }
  })
  if (!img) throw new Error('no image on the clipboard')
  const box = await canvas.boundingBox()
  // Drawn at zoom 1: larger than the canvas whenever the map was zoomed out to fit, never a copy of the screen.
  if (shown < 1 && box) expect(img.width * img.height).toBeGreaterThan(box.width * box.height)
  if (process.env.DEED_E2E_SCREENSHOTS)
    (await import('node:fs')).writeFileSync(
      `${process.env.DEED_E2E_SCREENSHOTS}/map-copy.png`,
      Buffer.from(img.png, 'base64'),
    )
  // The view the user had comes back.
  await expect
    .poll(() =>
      canvas.locator('.react-flow__viewport').evaluate((el) => new DOMMatrixReadOnly(getComputedStyle(el).transform).a),
    )
    .toBeCloseTo(shown, 2)
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

test('map: group the overview by location and region, and expand a location', async ({ page }) => {
  await page.goto('/map?group=kind')
  await expect(page.getByTestId('map-canvas')).toHaveAttribute('data-layout-ready', 'true', { timeout: 20_000 })
  const ids = await page.evaluate(async () => {
    const me = (await (await fetch('/api/v1/me')).json()) as { csrf_token: string }
    const post = async (path: string, body: unknown) => {
      const r = await fetch(`/api/v1${path}`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'X-Deed-CSRF': me.csrf_token },
        body: JSON.stringify(body),
      })
      return (await r.json()) as { entity_id?: string }
    }
    await post('/projects', { name: 'e2e-places', synthetic: true })
    const out: Record<string, string> = {}
    for (const [kind, name] of [
      ['region', 'e2e-eu'],
      ['location', 'e2e-site'],
      ['location', 'e2e-rack'],
      ['server', 'e2e-srv-1'],
      ['server', 'e2e-srv-2'],
      ['server', 'e2e-srv-3'],
      ['vm', 'e2e-vm-1'],
    ] as const) {
      out[name] = (await post('/entities', { project: 'e2e-places', kind, name, source: 'api' })).entity_id ?? ''
    }
    for (const [type, from, to] of [
      ['located_in', 'e2e-site', 'e2e-eu'],
      ['located_in', 'e2e-rack', 'e2e-site'],
      ['located_in', 'e2e-srv-1', 'e2e-rack'],
      ['located_in', 'e2e-srv-2', 'e2e-rack'],
      ['located_in', 'e2e-srv-3', 'e2e-site'],
      ['runs_on', 'e2e-vm-1', 'e2e-srv-1'],
    ] as const) {
      await post('/relations', { type, from: { id: out[from] }, to: { id: out[to] }, source: 'api' })
    }
    return out
  })
  // Group by location: the rack holds itself, two servers and the VM on one of them.
  await page.getByTestId('map-group-by').click()
  await page.getByRole('option', { name: 'Group by location' }).click()
  await expect(page).toHaveURL(/group=location/)
  await expect(page.getByTestId('map-canvas')).toHaveAttribute('data-layout-ready', 'true', { timeout: 20_000 })
  const rack = page.locator(`[data-cluster-key="location|${ids['e2e-rack']}"]`)
  await expect(rack).toContainText('e2e-rack')
  await expect(rack).toContainText('4')
  await rack.click()
  await expect(page.getByTestId('map-canvas')).toHaveAttribute('data-layout-ready', 'true', { timeout: 20_000 })
  for (const n of ['e2e-rack', 'e2e-srv-1', 'e2e-srv-2', 'e2e-vm-1'])
    await expect(page.locator(`[data-entity-id="${ids[n]}"]`)).toBeVisible()
  // Group by region: everything placed is in e2e-eu.
  await page.goto('/map?group=region')
  await expect(page.getByTestId('map-canvas')).toHaveAttribute('data-layout-ready', 'true', { timeout: 20_000 })
  await expect(page.locator(`[data-cluster-key="region|${ids['e2e-eu']}"]`)).toContainText('7')
})
