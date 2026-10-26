import { alias, maskEnvironment, maskProject } from '@/lib/mask'
import type { GraphModel, MapEdge, MapNode } from './types'

/**
 * Mask mode (for screenshots and screen sharing): entity names, display names, owners, labels, project and source names
 * are replaced by stable aliases derived from the IDs (`service-k3f9`), so the drawing keeps its shape and the same
 * entity keeps the same alias across reloads, but nothing identifying is drawn. Kinds, health, freshness, relationship
 * types and common environment names stay: they describe the shape, not the installation.
 */

/** Deed's per-project system sources: generic names that reveal nothing. */
const SYSTEM_SOURCES = new Set(['api', 'terraform', 'ui', 'overrides'])

function maskNode(n: MapNode): MapNode {
  if (n.redacted) return n
  const { displayName: _d, labels: _l, owner, environment, ...rest } = n
  const base: MapNode = {
    ...rest,
    ...(owner ? { owner: alias('owner', owner) } : {}),
    ...(environment ? { environment: maskEnvironment(environment) } : {}),
  }
  if (!n.cluster) return { ...base, name: alias(n.kind, n.id) }
  // Kind and relationship clusters are named after the kind or type (and collapsed groups after an already masked
  // label); project, environment and owner groups after their value.
  const { groupBy, value } = n.cluster
  if (groupBy === 'kind' || groupBy === 'relationship' || n.kind === 'group') return base
  const name =
    groupBy === 'environment'
      ? maskEnvironment(value)
      : groupBy === 'project'
        ? maskProject(value)
        : alias(groupBy || 'group', value)
  return { ...base, name, cluster: { ...n.cluster, value: name } }
}

function maskEdge(e: MapEdge): MapEdge {
  return {
    ...e,
    sources: e.sources.map(({ displayName: _d, ...s }) => ({
      ...s,
      name: SYSTEM_SOURCES.has(s.name) ? s.name : alias('source', s.id),
    })),
  }
}

/** The model with every identifying string replaced (IDs, and so the layout, are unchanged). */
export function maskModel(model: GraphModel): GraphModel {
  return {
    ...model,
    nodes: new Map([...model.nodes].map(([id, n]) => [id, maskNode(n)])),
    edges: new Map([...model.edges].map(([id, e]) => [id, maskEdge(e)])),
    notes: model.notes.length ? ['details hidden (mask mode)'] : [],
    ...(model.expanded
      ? {
          expanded: model.expanded.map((x) => {
            const [by = '', value = ''] = x.key.split('|', 2)
            if (by === 'kind' || by === 'fan') return x
            return {
              ...x,
              label:
                by === 'project'
                  ? maskProject(value)
                  : by === 'environment'
                    ? maskEnvironment(value)
                    : alias(by, value),
            }
          }),
        }
      : {}),
  }
}
