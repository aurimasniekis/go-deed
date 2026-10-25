import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join } from 'node:path'
import { iconChoices, isBundledIcon, loadTheSvgIcons, parseIconRef, type TheSvgIcon } from './icons'
import manifest from './thesvg-manifest.json'

// Built-in kinds, Terraform mappings, connectors and the demo seed set icons on the server; each one must be in the
// bundle or the UI silently shows the fallback cube.
function serverIconRefs(dir: string, out = new Set<string>()): Set<string> {
  for (const entry of readdirSync(dir)) {
    const path = join(dir, entry)
    if (statSync(path).isDirectory()) {
      serverIconRefs(path, out)
    } else if (/\.(go|ya?ml)$/.test(entry) && !entry.endsWith('_test.go')) {
      for (const m of readFileSync(path, 'utf8').matchAll(/\b(?:tabler|simple-icons|thesvg):[a-z0-9][a-z0-9._-]*/g)) {
        out.add(m[0])
      }
    }
  }
  return out
}

// Must match LICENSES and UNMODIFIED in web/scripts/build-icons.mjs (ADR-011b).
const PERMISSIVE = ['CC0-1.0', 'MIT', 'Apache-2.0', 'BSD-2-Clause', 'BSD-3-Clause', 'ISC', 'Unlicense']
const NO_DERIVATIVES_ALLOWLIST = [
  'aws-amazon-ec2',
  'aws-amazon-rds',
  'aws-amazon-route-53',
  'aws-amazon-simple-storage-service',
]
const icons = manifest.icons as TheSvgIcon[]

describe('icons', () => {
  beforeAll(async () => {
    await loadTheSvgIcons()
  })

  it('parses references', () => {
    expect(parseIconRef('simple-icons:docker')).toEqual({ pack: 'simple-icons', name: 'docker' })
    expect(parseIconRef('thesvg:proxmox')).toEqual({ pack: 'thesvg', name: 'proxmox' })
    expect(parseIconRef('server')).toEqual({ pack: 'tabler', name: 'server' })
    expect(parseIconRef(undefined)).toEqual({ pack: 'tabler', name: 'cube' })
  })

  it('bundles every icon the server refers to', () => {
    const refs = [...serverIconRefs(join(__dirname, '../../../internal'))]
    expect(refs.length).toBeGreaterThan(20)
    expect(refs.some((r) => r.startsWith('thesvg:'))).toBe(true)
    expect(refs.filter((r) => !isBundledIcon(r))).toEqual([])
  })

  it('resolves thesvg references from the manifest', () => {
    expect(isBundledIcon('thesvg:proxmox')).toBe(true)
    expect(isBundledIcon('thesvg:aws')).toBe(true)
    expect(isBundledIcon('thesvg:not-bundled')).toBe(false)
    for (const i of icons) expect(isBundledIcon(`thesvg:${i.name}`)).toBe(true)
    expect(iconChoices().filter((c) => c.pack === 'thesvg')).toHaveLength(icons.length)
  })

  it('offers each icon once in pickers', () => {
    const refs = iconChoices(['core-router']).map((c) => c.ref)
    expect(new Set(refs).size).toBe(refs.length)
    expect(refs).toContain('tabler:cube')
    expect(refs).toContain('thesvg:aws')
    expect(refs).toContain('custom:core-router')
  })

  it('bundles only theSVG icons with an allowed license', () => {
    expect(manifest.package).toBe('@thesvg/icons')
    expect(icons.length).toBeGreaterThan(0)
    for (const i of icons) {
      expect(i.name).toMatch(/^[a-z0-9][a-z0-9._-]{0,127}$/)
      expect(i.title).not.toBe('')
      expect(i.source).toMatch(/^https?:\/\//)
      if (PERMISSIVE.includes(i.license)) {
        expect(i.unmodified).toBeUndefined()
      } else {
        // No-derivatives icons only by explicit allowlist, published bytes, never recoloured or masked.
        expect(i.license).toMatch(/^CC-BY-ND-/)
        expect(NO_DERIVATIVES_ALLOWLIST).toContain(i.name)
        expect(i.unmodified).toBeTruthy()
        expect(i.render).toBe('image')
      }
    }
  })

  it('bundles static vector markup only', () => {
    for (const i of icons) {
      expect(i.svg).toMatch(/^(<\?xml[^>]*\?>\s*)?<svg\b/)
      expect(i.svg).not.toMatch(/<(script|style|foreignObject|image|a|iframe)\b|\son[a-z]+\s*=|javascript:|data:/i)
      expect(i.svg).not.toMatch(/href="(?!#)/)
      if (i.render === 'image') {
        expect(i.contrast?.light).toBeGreaterThanOrEqual(3)
        expect(i.contrast?.dark).toBeGreaterThanOrEqual(3)
      }
    }
  })
})
