import { describe, expect, it } from 'vitest'
import { alias, maskEnvironment } from '@/lib/mask'
import { maskModel } from './mask'
import { emptyModel, type MapEdge, type MapNode } from './types'

const node = (id: string, extra: Partial<MapNode> = {}): MapNode => ({
  id,
  kind: 'service',
  name: 'billing-api',
  displayName: 'Billing API',
  projectId: 'prj_home',
  health: 'ok',
  freshness: 'fresh',
  environment: 'acme-prod-eu',
  owner: 'team-payments',
  labels: { customer: 'acme' },
  declared: true,
  valid: true,
  findings: 0,
  depth: 0,
  ...extra,
})

describe('mask mode', () => {
  it('replaces identifying text with stable aliases and keeps the shape', () => {
    const m = emptyModel()
    m.nodes.set('ent_1', node('ent_1'))
    m.nodes.set('ent_2', node('ent_2', { environment: 'production', kind: 'example.com/ups' }))
    m.nodes.set(
      'c1',
      node('c1', {
        kind: 'cluster',
        name: 'prj_home',
        cluster: { key: 'project|prj_home', count: 3, groupBy: 'project', value: 'prj_home' },
      }),
    )
    m.nodes.set(
      'c2',
      node('c2', {
        kind: 'cluster',
        name: 'service',
        cluster: { key: 'kind|service', count: 3, groupBy: 'kind', value: 'service' },
      }),
    )
    const edge: MapEdge = {
      id: 'rel_1',
      from: 'ent_1',
      to: 'ent_2',
      type: 'depends_on',
      evidence: 'declared',
      inferred: false,
      crossProject: false,
      count: 1,
      sources: [
        {
          id: 'src_a',
          name: 'github-acme',
          displayName: 'GitHub ACME',
          layer: 'imported',
          projectId: 'prj_home',
          relations: 1,
        },
        { id: 'src_b', name: 'terraform', layer: 'declared', projectId: 'prj_home', relations: 1 },
      ],
    }
    m.edges.set('rel_1', edge)
    m.expanded = [{ key: 'project|prj_home', label: 'Home', members: ['ent_1'] }]
    const out = maskModel(m)
    const a = out.nodes.get('ent_1')
    expect(a?.name).toBe(alias('service', 'ent_1'))
    expect(a?.name).toMatch(/^service-[0-9a-z]{4}$/)
    expect(a?.displayName).toBeUndefined()
    expect(a?.labels).toBeUndefined()
    expect(a?.owner).toBe(alias('owner', 'team-payments'))
    expect(a?.environment).toBe(maskEnvironment('acme-prod-eu'))
    expect(a?.environment).not.toContain('acme')
    // Common environments, kinds without their domain, kind clusters and system sources stay readable.
    expect(out.nodes.get('ent_2')?.environment).toBe('production')
    expect(out.nodes.get('ent_2')?.name).toMatch(/^ups-/)
    expect(out.nodes.get('c2')?.name).toBe('service')
    expect(out.nodes.get('c1')?.name).toBe(alias('project', 'prj_home'))
    const sources = out.edges.get('rel_1')?.sources ?? []
    expect(sources.map((s) => s.name)).toEqual([alias('source', 'src_a'), 'terraform'])
    expect(sources[0]?.displayName).toBeUndefined()
    expect(out.expanded?.[0]?.label).toBe(alias('project', 'prj_home'))
    // Stable, and nothing original survives anywhere in the masked model.
    expect(maskModel(m).nodes.get('ent_1')?.name).toBe(a?.name)
    const text = JSON.stringify([...out.nodes.values(), ...out.edges.values(), out.expanded])
    for (const secret of ['billing', 'Billing', 'acme', 'ACME', 'payments', 'Home']) expect(text).not.toContain(secret)
  })
})
