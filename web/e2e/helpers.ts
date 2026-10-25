import AxeBuilder from '@axe-core/playwright'
import { expect, type Page } from '@playwright/test'
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { OWNER_STATE, SCREENSHOTS } from './paths'
import type { OwnerState, SeedResult } from './seed.ts'

export type E2EState = OwnerState & { seed: SeedResult; mocks: string }

export function readState(): E2EState {
  return JSON.parse(readFileSync(OWNER_STATE, 'utf8')) as E2EState
}

export function writeState(s: E2EState) {
  writeFileSync(OWNER_STATE, JSON.stringify(s, null, 2), { mode: 0o600 })
}

export function entityId(key: string): string {
  const id = readState().seed.entities[key]
  if (!id) throw new Error(`seeded entity ${key} not found`)
  return id
}

/** Waits for the app shell (signed in, navigation rendered) and for skeletons to disappear. */
export async function ready(page: Page) {
  await expect(page.getByRole('navigation', { name: 'Main navigation' })).toBeVisible()
  await expect(page.locator('[aria-busy="true"]')).toHaveCount(0, { timeout: 15_000 })
}

/** axe-core scan: no serious or critical violations (WCAG 2.x A/AA rules). */
export async function expectAccessible(page: Page, name: string) {
  const results = await new AxeBuilder({ page })
    .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa'])
    .analyze()
  const bad = results.violations.filter((v) => v.impact === 'serious' || v.impact === 'critical')
  const summary = bad.map(
    (v) =>
      `${v.id} (${v.impact}): ${v.help}\n    ${v.nodes
        .slice(0, 5)
        .map((n) => n.target.join(' '))
        .join('\n    ')}`,
  )
  expect(summary, `${name}: axe violations`).toEqual([])
}

export async function screenshot(page: Page, name: string) {
  mkdirSync(SCREENSHOTS, { recursive: true })
  await page.screenshot({ path: join(SCREENSHOTS, `${name}.png`) })
}

/** Forces the colour scheme (Mantine follows the system preference with defaultColorScheme="auto"). */
export async function useScheme(page: Page, scheme: 'light' | 'dark') {
  await page.emulateMedia({ colorScheme: scheme, reducedMotion: 'reduce' })
  await page.evaluate((s) => {
    try {
      window.localStorage.setItem('mantine-color-scheme-value', s)
    } catch {
      // storage unavailable: the media query still applies
    }
  }, scheme)
}
