import { describe, expect, it } from 'vitest'
import {
  compact,
  decodeBool,
  decodeInt,
  decodeLabels,
  decodeList,
  encodeLabels,
  encodeList,
  inventoryQuery,
  validateFindingsSearch,
  validateInventorySearch,
  validateMapSearch,
} from './urlState'

describe('URL state codecs', () => {
  it('round-trips lists as comma-separated strings', () => {
    expect(encodeList(['server', 'vm'])).toBe('server,vm')
    expect(encodeList([])).toBeUndefined()
    expect(decodeList('server, vm,,')).toEqual(['server', 'vm'])
    expect(decodeList(['a', 1, ''])).toEqual(['a'])
    expect(decodeList(42)).toEqual(['42'])
    expect(decodeList(undefined)).toEqual([])
  })

  it('decodes booleans and clamps integers', () => {
    expect(decodeBool('true')).toBe(true)
    expect(decodeBool(false)).toBe(false)
    expect(decodeBool('yes')).toBeUndefined()
    expect(decodeInt('9', 1, 6)).toBe(6)
    expect(decodeInt(0, 1, 6)).toBe(1)
    expect(decodeInt('x', 1, 6)).toBeUndefined()
  })

  it('keeps labels with commas in values', () => {
    const l = ['team=a,b', 'tier=core']
    expect(decodeLabels(encodeLabels(l))).toEqual(l)
    expect(encodeLabels(['bad'])).toBeUndefined()
  })

  it('drops empty values', () => {
    expect(compact({ a: undefined, b: '', c: [], d: 'x', e: 0, f: false })).toEqual({ d: 'x', e: 0, f: false })
  })

  it('validates inventory search, rejecting unknown sorts and bounding sizes', () => {
    const s = validateInventorySearch({
      q: 'db',
      kind: ['server', 'vm'],
      sort: '-updated_at',
      declared: 'false',
      limit: 9999,
      junk: 1,
    })
    expect(s).toEqual({ q: 'db', kind: 'server,vm', sort: '-updated_at', declared: false, limit: 500 })
    expect(validateInventorySearch({ sort: 'password' }).sort).toBeUndefined()
  })

  it('builds the entities query, falling back to the global project filter', () => {
    const q = inventoryQuery(validateInventorySearch({ health: 'failed,warning', label: 'tier=core', valid: 'true' }), [
      'prj_1',
    ])
    expect(q).toMatchObject({
      health: ['failed', 'warning'],
      label: ['tier=core'],
      valid: 'true',
      project: ['prj_1'],
      limit: 100,
    })
    expect(inventoryQuery({ project: 'home' }, ['prj_1']).project).toEqual(['home'])
  })

  it('validates map state: enums, depth bounds, trail length', () => {
    const s = validateMapSearch({
      focus: 'ent_1',
      mode: 'impact',
      depth: 12,
      layout: 'circle',
      cluster: 'host',
      trail: Array.from({ length: 20 }, (_, i) => `e${i}`).join(','),
    })
    expect(s.mode).toBe('impact')
    expect(s.depth).toBe(6)
    expect(s.layout).toBeUndefined()
    expect(s.cluster).toBe('host')
    expect(decodeList(s.trail)).toHaveLength(12)
  })

  it('validates findings filters', () => {
    expect(validateFindingsSearch({ severity: 'critical', state: 'bogus' })).toEqual({ severity: 'critical' })
  })
})
