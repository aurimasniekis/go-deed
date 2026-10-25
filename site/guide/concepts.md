# Concepts

## Facets, layers and merging
Every source writes its own **facet** of an entity. Facets are merged field by field:

| Layer | Default priority | Typical writer |
|---|---|---|
| override | 100 | an admin correcting a value |
| declared | 80 | Terraform provider, API |
| annotation | 70 | UI notes, ownership |
| imported | 60 | Terraform state, connectors |
| discovered | 40 | Pulse tail, agents, push sources |

`status.observed` and `status.health` come from observation facets and never overwrite declared spec. Within a layer,
two sources that disagree produce a **conflict**; the older claim wins deterministically until someone resolves it.
`GET /api/v1/entities/{id}/provenance` explains every effective field.

## Snapshots, staleness and removal
Sources send **complete snapshots** (absent facts are tombstoned per the source's policy, behind a mass-removal guard)
or **incremental items** with revisions; stale revisions are refused, so a late update can never resurrect deleted
data. Removing a Terraform declaration removes only that facet: if a tail or agent still observes the entity, it stays
— undeclared, but visible.

## Freshness and evidence
Every source has an expected interval; entities and relationships carry `last_seen_at`, and freshness is reported as
fresh, aging, stale or unknown. Relationships carry an evidence class — declared, structural, observed or inferred —
and inferred ones keep the rule and matched value that produced them.

The architecture decision records explain the reasoning behind each of these: see
[Architecture decisions](/reference/decisions).
