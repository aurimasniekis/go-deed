import { expect, test, type Page } from '@playwright/test'
import { mkdirSync, writeFileSync } from 'node:fs'
import { cpus, totalmem } from 'node:os'
import { join } from 'node:path'
import { entityId } from './helpers'
import { STATE_DIR } from './paths'

/**
 * Map budget measurements (ADR-011): time to first neighbourhood (fetch → layout → first rendered frame) and frame
 * times while panning, on the seeded data and on a ~2,000-node neighbourhood (DEED_E2E_LARGE=2000 seeds the "scale"
 * project). Run with `DEED_E2E_LARGE=2000 pnpm e2e perf.spec.ts`; results go to e2e/.state/perf.json.
 */

interface MapStats {
  nodes: number
  edges: number
  layoutMs: number
  fetchToRenderMs: number
}

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
  return page.evaluate(() => {
    const m = (window as unknown as { __deedMap: MapStats }).__deedMap
    const rendered = performance.getEntriesByName('deed-map-rendered')[0]
    return { ...m, navToRenderMs: Math.round(rendered?.startTime ?? 0) }
  })
}

/** Pans the viewport with the mouse for `ms` while recording requestAnimationFrame intervals. */
async function frameTimes(
  page: Page,
  ms = 3000,
): Promise<{ frames: number; p50: number; p95: number; max: number; fps: number }> {
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
  const cx = box.x + box.width / 2
  const cy = box.y + box.height / 2
  await page.mouse.move(cx, cy)
  await page.mouse.down()
  const steps = Math.round(ms / 16)
  for (let i = 0; i < steps; i++) {
    const a = (i / steps) * Math.PI * 4
    await page.mouse.move(cx + Math.cos(a) * 200, cy + Math.sin(a) * 120)
  }
  await page.mouse.up()
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

test.describe('map budgets', () => {
  test.setTimeout(300_000)

  test('time to first neighbourhood and frame times', async ({ page, request }) => {
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
    results.seededFrames = await frameTimes(page)

    if (process.env.DEED_E2E_LARGE) {
      const res = await request.get('/api/v1/entities/lookup?project=scale&kind=network&name=core-net')
      expect(res.ok()).toBe(true)
      const core = ((await res.json()) as { metadata: { id: string } }).metadata.id
      for (const layout of ['layered', 'force', 'radial'] as const) {
        const stats = await openMap(page, `/map?focus=${core}&mode=neighborhood&depth=2&layout=${layout}&cluster=none`)
        expect(stats.nodes).toBeLessThanOrEqual(2000)
        expect(stats.edges).toBeLessThanOrEqual(4000)
        const frames = await frameTimes(page)
        results[`large-${layout}`] = { ...stats, frames }
      }
    }
    mkdirSync(STATE_DIR, { recursive: true })
    writeFileSync(join(STATE_DIR, 'perf.json'), JSON.stringify(results, null, 2))
    console.log(JSON.stringify(results, null, 2))
  })
})
