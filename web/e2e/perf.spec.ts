import { expect, test, type Page } from '@playwright/test'
import { mkdirSync, writeFileSync } from 'node:fs'
import { cpus, totalmem } from 'node:os'
import { join } from 'node:path'
import { entityId } from './helpers'
import { STATE_DIR } from './paths'

/**
 * Map budget measurements (ADR-011): time to first neighbourhood (fetch → layout → first rendered frame) and frame
 * times while panning and zooming, on the seeded data and — with DEED_E2E_LARGE=2000, which seeds the "scale" project
 * — on a ~500-node neighbourhood (dmz-net, the size the 60 fps budget is stated for) and a ~2,000-node one (core-net,
 * the render cap), each in all three layouts, plus an in-app revisit (served from the in-memory layout cache). Run with
 * `DEED_E2E_LARGE=2000 pnpm e2e perf.spec.ts`; results go to e2e/.state/perf.json.
 */

interface MapStats {
  nodes: number
  edges: number
  layoutMs: number
  layoutCached?: boolean
  fetchToRenderMs: number
  signature: string
}

interface Frames {
  frames: number
  p50: number
  p95: number
  max: number
  fps: number
}

const LAYOUTS = ['layered', 'force', 'radial'] as const

async function openMap(page: Page, url: string): Promise<MapStats & { navToRenderMs: number }> {
  await page.goto(url)
  await expect(page.getByTestId('map-canvas')).toHaveAttribute('data-layout-ready', 'true', { timeout: 60_000 })
  await page.waitForFunction(
    () => (window as unknown as { __deedMap?: { renders: number } }).__deedMap?.renders !== undefined,
    null,
    {
      timeout: 60_000,
    },
  )
  // Let the fit animation (250 ms) settle before anything is measured.
  await page.waitForTimeout(400)
  return page.evaluate(() => {
    const m = (window as unknown as { __deedMap: MapStats }).__deedMap
    const rendered = performance.getEntriesByName('deed-map-rendered')[0]
    return { ...m, navToRenderMs: Math.round(rendered?.startTime ?? 0) }
  })
}

/**
 * Records requestAnimationFrame intervals for about `ms` while the mouse pans the viewport (drags in circles, starting
 * on empty canvas so it never drags a node) or zooms it (wheel ~13× in and back out around the view, twice).
 */
async function frameTimes(page: Page, kind: 'pan' | 'zoom', ms = 3000): Promise<Frames> {
  const box = await page.getByTestId('map-canvas').boundingBox()
  if (!box) throw new Error('no canvas')
  await page.evaluate(() => {
    const w = window as unknown as { __frames: number[]; __stop: boolean }
    w.__frames = []
    w.__stop = false
    let last = performance.now()
    const tick = (t: number) => {
      w.__frames.push(t - last)
      last = t
      if (!w.__stop) requestAnimationFrame(tick)
    }
    requestAnimationFrame(tick)
  })
  const { cx, cy } = await page.evaluate((b) => {
    for (let r = 0; r < Math.min(b.width, b.height) / 2; r += 8)
      for (let a = 0; a < 2 * Math.PI; a += 0.3) {
        const px = b.x + b.width / 2 + Math.cos(a) * r
        const py = b.y + b.height / 2 + Math.sin(a) * r
        if (document.elementFromPoint(px, py)?.classList.contains('react-flow__pane')) return { cx: px, cy: py }
      }
    return { cx: b.x + b.width / 2, cy: b.y + b.height / 2 }
  }, box)
  await page.mouse.move(cx, cy)
  const steps = Math.round(ms / 16)
  if (kind === 'pan') {
    await page.mouse.down()
    for (let i = 0; i < steps; i++) {
      const a = (i / steps) * Math.PI * 4
      await page.mouse.move(cx + Math.cos(a) * 200, cy + Math.sin(a) * 120)
    }
    await page.mouse.up()
  } else {
    const quarter = Math.max(1, Math.round(steps / 4))
    for (let i = 0; i < steps; i++) {
      const dir = Math.floor(i / quarter) % 2 === 0 ? -1 : 1
      await page.mouse.wheel(0, dir * 40)
      await page.waitForTimeout(8)
    }
  }
  const frames = await page.evaluate(() => {
    const w = window as unknown as { __frames: number[]; __stop: boolean }
    w.__stop = true
    return w.__frames.slice(2)
  })
  const sorted = [...frames].sort((a, b) => a - b)
  const q = (p: number) =>
    Math.round((sorted[Math.min(sorted.length - 1, Math.floor(p * sorted.length))] ?? 0) * 10) / 10
  const total = frames.reduce((s, f) => s + f, 0)
  return {
    frames: frames.length,
    p50: q(0.5),
    p95: q(0.95),
    max: q(1),
    fps: Math.round((frames.length / total) * 1000),
  }
}

/** Fits the view again (keyboard `f`) so the zoom measurement starts where the pan measurement did. */
async function refit(page: Page) {
  await page.getByTestId('map-canvas').focus()
  await page.keyboard.press('f')
  await page.waitForTimeout(400)
}

async function network(page: Page, name: string): Promise<string> {
  const res = await page.request.get(`/api/v1/entities/lookup?project=scale&kind=network&name=${name}`)
  expect(res.ok()).toBe(true)
  return ((await res.json()) as { metadata: { id: string } }).metadata.id
}

/**
 * In-app round trip to the overview and back (history.back(), no reload), so the second render can be served from the
 * in-memory layout cache: the time from history.back() to the rendered frame.
 */
async function revisit(page: Page): Promise<MapStats & { backToRenderMs: number }> {
  const sig = await page.evaluate(() => (window as unknown as { __deedMap: MapStats }).__deedMap.signature)
  await page.getByRole('button', { name: 'Overview', exact: true }).click()
  await page.waitForFunction(
    (s) => {
      const m = (window as unknown as { __deedMap?: MapStats }).__deedMap
      return !!m && m.signature !== s
    },
    sig,
    { timeout: 60_000 },
  )
  await page.evaluate(() => {
    ;(window as unknown as { __back: number }).__back = performance.now()
    history.back()
  })
  await page.waitForFunction((s) => (window as unknown as { __deedMap?: MapStats }).__deedMap?.signature === s, sig, {
    timeout: 60_000,
  })
  return page.evaluate(() => {
    const w = window as unknown as { __deedMap: MapStats; __back: number }
    const marks = performance.getEntriesByName('deed-map-rendered')
    const last = marks[marks.length - 1]
    return { ...w.__deedMap, backToRenderMs: Math.round((last?.startTime ?? 0) - w.__back) }
  })
}

test.describe('map budgets', () => {
  test.setTimeout(600_000)

  test('time to first neighbourhood and frame times', async ({ page }) => {
    const results: Record<string, unknown> = {
      machine: `${cpus()[0]?.model ?? 'unknown CPU'} × ${cpus().length}, ${Math.round(totalmem() / 2 ** 30)} GiB RAM, ${process.platform}`,
      browser: 'Chromium (Playwright), headless, 1440×900',
      at: new Date().toISOString(),
    }
    // Warm-up (code-split chunks, kinds and relationship types cached).
    await openMap(page, `/map?focus=${entityId('service/billing-api')}&mode=neighborhood&depth=2`)

    const runs: number[] = []
    let small: (MapStats & { navToRenderMs: number }) | null = null
    for (let i = 0; i < 5; i++) {
      small = await openMap(
        page,
        `/map?focus=${entityId('service/api-gateway')}&mode=neighborhood&depth=${(i % 2) + 2}`,
      )
      runs.push(small.fetchToRenderMs)
    }
    results.seededNeighbourhood = { ...small, fetchToRenderRunsMs: runs }
    results.seededFrames = { pan: await frameTimes(page, 'pan'), zoom: await frameTimes(page, 'zoom') }

    if (process.env.DEED_E2E_LARGE) {
      for (const [label, hub] of [
        ['medium', 'dmz-net'],
        ['large', 'core-net'],
      ] as const) {
        const id = await network(page, hub)
        for (const layout of LAYOUTS) {
          const stats = await openMap(page, `/map?focus=${id}&mode=neighborhood&depth=2&layout=${layout}&cluster=none`)
          expect(stats.nodes).toBeLessThanOrEqual(2000)
          expect(stats.edges).toBeLessThanOrEqual(4000)
          // Fitted, these drawings are zoomed far out: level-of-detail blocks and lines, and the UI says so.
          const lod = await page.locator('.deed-node-lod').count()
          await expect(page.getByTestId('map-lod-note')).toBeVisible()
          await expect(page.getByTestId('map-legend')).toContainText('without arrowheads, labels or animation')
          const pan = await frameTimes(page, 'pan')
          await refit(page)
          const zoom = await frameTimes(page, 'zoom')
          const entry: Record<string, unknown> = { ...stats, lodNodes: lod, frames: { pan, zoom } }
          if (layout !== 'radial') entry.revisit = await revisit(page)
          results[`${label}-${layout}`] = entry
        }
      }
    }
    mkdirSync(STATE_DIR, { recursive: true })
    writeFileSync(join(STATE_DIR, 'perf.json'), JSON.stringify(results, null, 2))
    console.log(JSON.stringify(results, null, 2))
  })
})
