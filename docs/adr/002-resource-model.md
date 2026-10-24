# ADR-002: Resource model, kinds and relationship types as data

## Decision
Every catalog object uses an envelope:

```yaml
apiVersion: deed.dev/v1
kind: server                    # built-in or custom kind name
metadata:
  id: ent_01jb…                 # server-assigned, stable
  project: home
  name: web-1                   # unique per (project, kind); renameable (old name kept as alias)
  displayName: Web server 1
  description: …
  environment: production
  owner: grp_…                  # reference to a group/team entity
  labels: {tier: web}
  annotations: {…}
  icon: thesvg:hetzner          # optional override
  links: [{type: repository|dashboard|logs|docs|runbook|other, title, url}]
  tags: [edge]
  version: {declared: 1.4.2, observed: 1.4.1}
  lifecycle: active|planned|deprecated|retired
spec: {…}                       # validated by the kind's JSON Schema version
status:                         # computed, never written by declarations
  health: {state: ok|warning|failed|unknown, reason, observed_at, source}
  freshness: {state: fresh|aging|stale|unknown, last_seen_at}
  findings: {critical: 0, warning: 1, …}
  observed: {…}                 # observation-layer fields
```

- IDs: `<prefix>_<26 Crockford base32>` encoding a UUIDv7 (time-sortable, ULID-compatible ordering), from Porch's
  `internal/ids`. Prefixes: `prj`, `knd`, `rty`, `ent`, `rel`, `src`, `usr`, …
- External identities: `entity_external_ids(source_id, external_id)` unique per source. Aliases: `entity_aliases`.
- **Built-in kinds are data**: an embedded YAML catalog (`internal/catalog/builtin/kinds.yaml`) loaded at start-up
  into `kind_definitions` with `builtin = 1`. Custom kinds use the same table and API.
- **Kind definition**: `group` (e.g. `infra`, `software`, `custom.example.com`), `name`, `versions[]` each with a JSON
  Schema 2020-12 document, `identity_keys`, `display` (title, plural, icon, color), `allowed relationship endpoints`,
  `support` levels (modeling/terraform/runtime/native) and a `served`/`storage` version flag.
- **Validation**: santhosh-tekuri/jsonschema/v6, no remote `$ref` (a loader that refuses every URL), schema ≤ 64 KiB,
  depth ≤ 16, ≤ 512 properties, regexes ≤ 512 chars (Go RE2, linear time).
- **Evolution**: a new version must be backward-compatible with the storage version (only additions of optional
  properties, relaxations of constraints, new enum values). A breaking version must set `breaking: true` and choose
  `migration: revalidate|mark_invalid`. `revalidate` validates every entity against the new schema inside the
  publish transaction and refuses publication if any fails; `mark_invalid` publishes and raises an `invalid_schema`
  finding per failing entity. Entities are never dropped.
- **Relationship types are data** (`relationship_types`): name, inverse name, directionality, allowed from/to kinds
  (empty = any), `default_evidence` (structural/observed), cardinality hint, attribute JSON Schema, custom flag.
- Secrets are never stored: spec values may be `{"secretRef": {...}}` references only.

## Consequences
New kinds need no Go rebuild and no server-side code execution. Typed columns exist only for envelope fields used in
filters (project, kind, name, environment, owner, lifecycle, health, freshness).
