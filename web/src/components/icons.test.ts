import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join } from 'node:path'
import { isBundledIcon, parseIconRef } from './icons'

// Built-in kinds, Terraform mappings, connectors and the demo seed set icons on the server; each one must be in the
// bundle or the UI silently shows the fallback cube.
function serverIconRefs(dir: string, out = new Set<string>()): Set<string> {
  for (const entry of readdirSync(dir)) {
    const path = join(dir, entry)
    if (statSync(path).isDirectory()) {
      serverIconRefs(path, out)
    } else if (/\.(go|ya?ml)$/.test(entry) && !entry.endsWith('_test.go')) {
      for (const m of readFileSync(path, 'utf8').matchAll(/\b(?:tabler|simple-icons):[a-z0-9][a-z0-9._-]*/g)) {
        out.add(m[0])
      }
    }
  }
  return out
}

describe('icons', () => {
  it('parses references', () => {
    expect(parseIconRef('simple-icons:docker')).toEqual({ pack: 'simple-icons', name: 'docker' })
    expect(parseIconRef('server')).toEqual({ pack: 'tabler', name: 'server' })
    expect(parseIconRef(undefined)).toEqual({ pack: 'tabler', name: 'cube' })
  })

  it('bundles every icon the server refers to', () => {
    const refs = [...serverIconRefs(join(__dirname, '../../../internal'))]
    expect(refs.length).toBeGreaterThan(20)
    expect(refs.filter((r) => !isBundledIcon(r))).toEqual([])
  })
})
