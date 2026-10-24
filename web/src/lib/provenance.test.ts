import { describe, expect, it } from 'vitest'
import {
  compareFacets,
  escapeToken,
  facetPointers,
  flatten,
  getPointer,
  pointerLabel,
  provenanceFor,
  provenanceRows,
  sameValue,
  type FacetLike,
} from './provenance'

const facet = (over: Partial<FacetLike>): FacetLike => ({
  id: 'f',
  source: 'api',
  source_id: 'src_api',
  layer: 'declared',
  fields: {},
  observed_at: '2026-10-24T00:00:00Z',
  live: true,
  revision: 1,
  ...over,
})

const effective = {
  metadata: { owner: 'platform', labels: { 'deed.dev/synthetic': 'true' } },
  spec: { ipv4: '10.0.0.1', ports: [22, 443] },
  status: { observed: { rps: 5 } },
}

describe('JSON pointers', () => {
  it('escapes ~ and / and reads values', () => {
    expect(escapeToken('a/b~c')).toBe('a~1b~0c')
    expect(getPointer(effective, '/metadata/labels/deed.dev~1synthetic')).toBe('true')
    expect(getPointer(effective, '/spec/ports/1')).toBe(443)
    expect(getPointer(effective, '/spec/missing')).toBeUndefined()
    expect(pointerLabel('/metadata/labels/deed.dev~1synthetic')).toBe('metadata › labels › deed.dev/synthetic')
  })

  it('flattens objects to leaves; arrays are leaves', () => {
    expect([...flatten({ a: { b: 1, c: [1, 2] }, d: {} }, '')]).toEqual([
      ['/a/b', 1],
      ['/a/c', [1, 2]],
      ['/d', {}],
    ])
  })

  it('maps observation facets under /status', () => {
    const p = facetPointers(facet({ layer: 'observation', fields: { observed: { rps: 5 }, health: { state: 'ok' } } }))
    expect([...p.keys()]).toEqual(['/status/observed/rps', '/status/health'])
  })

  it('compares values structurally', () => {
    expect(sameValue({ a: 1, b: [1] }, { b: [1], a: 1 })).toBe(true)
    expect(sameValue([1, 2], [2, 1])).toBe(false)
  })
})

describe('declared vs observed', () => {
  it('lines up declared and observed values per field, differences first', () => {
    const rows = compareFacets(
      [
        facet({ fields: { metadata: { owner: 'platform' }, spec: { ipv4: '10.0.0.1' } } }),
        facet({
          id: 'f2',
          source: 'agent',
          source_id: 'src_agent',
          layer: 'discovered',
          fields: { spec: { ipv4: '10.0.0.9', ports: [22, 443] } },
        }),
        facet({
          id: 'f3',
          source: 'old',
          source_id: 'src_old',
          layer: 'discovered',
          live: false,
          fields: { spec: { ipv4: 'x' } },
        }),
        facet({
          id: 'f4',
          source: 'metrics',
          source_id: 'src_m',
          layer: 'observation',
          fields: { observed: { rps: 5 } },
        }),
      ],
      effective,
    )
    expect(rows[0]).toMatchObject({ pointer: '/spec/ipv4', state: 'differs', effective: '10.0.0.1' })
    expect(rows[0]?.observed).toEqual([
      { source: 'agent', sourceId: 'src_agent', layer: 'discovered', value: '10.0.0.9' },
    ])
    const byPointer = new Map(rows.map((r) => [r.pointer, r]))
    expect(byPointer.get('/spec/ports')?.state).toBe('observed-only')
    expect(byPointer.get('/metadata/owner')?.state).toBe('declared-only')
    expect(byPointer.get('/status/observed/rps')?.state).toBe('observed-only')
    // Tombstoned facets do not count.
    expect(rows.flatMap((r) => r.observed).some((o) => o.source === 'old')).toBe(false)
  })

  it('reports matches when every observation equals a declaration', () => {
    const rows = compareFacets(
      [
        facet({ fields: { spec: { ipv4: '10.0.0.1' } } }),
        facet({ id: 'f2', source: 'scan', source_id: 's2', layer: 'imported', fields: { spec: { ipv4: '10.0.0.1' } } }),
      ],
      effective,
    )
    expect(rows).toHaveLength(1)
    expect(rows[0]?.state).toBe('match')
  })
})

describe('provenance', () => {
  const prov = {
    '/spec/ipv4': {
      source_id: 'src_api',
      source: 'api',
      layer: 'declared',
      revision: 1,
      observed_at: '2026-10-24T00:00:00Z',
      facet_id: 'f',
      candidates: [
        { source_id: 'src_a', source: 'agent', layer: 'discovered', value: '10.0.0.9' },
        { source_id: 'src_s', source: 'scan', layer: 'discovered', value: '10.0.0.1' },
      ],
    },
    '/metadata/labels/deed.dev~1synthetic': {
      source_id: 'src_api',
      source: 'api',
      layer: 'declared',
      revision: 1,
      observed_at: '',
      facet_id: 'f',
    },
  }

  it('joins winners with effective values and marks losing candidates', () => {
    const rows = provenanceRows(prov, effective)
    const ipv4 = rows.find((r) => r.pointer === '/spec/ipv4')
    expect(ipv4?.value).toBe('10.0.0.1')
    expect(ipv4?.losing).toEqual([
      { source: 'agent', layer: 'discovered', value: '10.0.0.9', sameValue: false },
      { source: 'scan', layer: 'discovered', value: '10.0.0.1', sameValue: true },
    ])
  })

  it('finds the entry for a field, its nearest ancestor, or a descendant', () => {
    expect(provenanceFor(prov, '/spec/ipv4')?.pointer).toBe('/spec/ipv4')
    expect(provenanceFor({ '/metadata/links': prov['/spec/ipv4'] }, '/metadata/links/0/url')?.pointer).toBe(
      '/metadata/links',
    )
    expect(provenanceFor(prov, '/metadata/labels')?.pointer).toBe('/metadata/labels/deed.dev~1synthetic')
    expect(provenanceFor(prov, '/spec/nothing')).toBeNull()
    expect(provenanceFor(undefined, '/spec/ipv4')).toBeNull()
  })
})
