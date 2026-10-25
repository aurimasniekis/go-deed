# Custom kinds and relationship types

Every entity has a **kind** (an entity type with a versioned JSON Schema for `spec`) and every relationship has a
**relationship type** (ADR-002). Deed ships 40 built-in kinds (`server`, `vm`, `service`, `repository`,
`dns_record`, …) and 23 built-in relationship types (`runs_on`, `depends_on`, `contains`, `publishes`, …) from
`internal/catalog/builtin/*.yaml`. Built-in definitions are owned by the binary: they are re-seeded on start-up and
cannot be changed or deleted (`403`). Installation owners and admins add their own.

All paths below are under `/api/v1`. Any signed-in user can read kinds and relationship types; creating, changing,
publishing and deleting needs the installation `owner` or `admin` role. Source tokens cannot manage them.

## Kinds
| Field | Rules |
|---|---|
| `name` | lowercase identifier (`[a-z][a-z0-9_]{0,62}`), optionally prefixed by a domain: `example.com/ups`. Fixed after creation. |
| `group` | catalog group, `[a-z][a-z0-9.-]{0,62}` (default `custom`) |
| `title` | required, one line, at most 100 bytes |
| `plural` | at most 100 bytes (default: `title` + `s`) |
| `description` | at most 2000 bytes |
| `icon` | `pack:name` with pack `tabler`, `simple-icons`, `thesvg` or `custom` (see `docs/ICONS.md`) |
| `color` | `#rrggbb` |
| `identity_keys` | at most 8 keys; each key lists 1–4 JSON pointers starting with `/spec/` or `/metadata/` |
| `deprecated` | flag |

Names with a domain contain `/`, so escape it in URLs: `/api/v1/kinds/example.com%2Fups`.

```sh
curl -s -X POST https://deed.example.com/api/v1/kinds -H "Authorization: Bearer $DEED_ADMIN_TOKEN" \
  -H 'Content-Type: application/json' -d @- <<'JSON'
{
  "name": "example.com/ups", "title": "UPS", "icon": "tabler:battery-charging", "color": "#2f9e44",
  "identity_keys": [["/spec/serial"]],
  "schema": {
    "$schema": "https://json-schema.org/draft/2020-12/schema",
    "type": "object", "additionalProperties": false, "required": ["capacity_va"],
    "properties": {"capacity_va": {"type": "integer", "minimum": 0}, "serial": {"type": "string"}, "model": {"type": "string"}}
  }
}
JSON
```

The first schema is stored as version `v1`; without `schema` it is an open object
(`{"type": "object", "additionalProperties": true}`). Responses carry an `ETag` (the resource version); send it as
`If-Match` on updates to get `412` instead of overwriting a concurrent change.

`PATCH /kinds/{name}` changes the display fields, `identity_keys` and `deprecated`. It writes the whole set: send every
field you want to keep, because omitted ones fall back to their defaults (`title` is always required).
`GET /kinds` and `GET /kinds/{name}` include `support`: whether Terraform mappings, runtime ingestion paths or native
providers produce the kind.

### Identity keys
When a source writes an entity that does not resolve by ID, external ID or kind + name, Deed computes the item's
identity values (`pointer=value`, lowercased, all pointers of a key present) and converges on an existing entity when
**exactly one** entity of the kind in the project has the same value. Otherwise it creates a new entity. Identity
values are indexed whenever an entity is recomputed; changing a kind's keys does not re-index existing entities.

## Schema rules
Kind schemas (and relationship `attributes_schema`) are JSON Schema **draft 2020-12**, checked on publish:

| Rule | Limit |
|---|---|
| document size | 64 KiB |
| top level | a JSON object; `type`, if present, must be `"object"` |
| `$schema` | if present, must name draft 2020-12 |
| nesting depth | 16 |
| `properties` + `patternProperties` entries (whole document) | 512 |
| `pattern` length | 512 characters, compiled with Go RE2 (linear time) |
| `format` | only `date-time`, `date`, `time`, `duration`, `email`, `hostname`, `ipv4`, `ipv6`, `uri`, `uri-reference`, `uuid`, `regex`, `json-pointer`; formats are asserted |
| `$ref`, `$dynamicRef` | local only (`#…`); remote references are refused |
| `$id` | not allowed |

Independently of the schema, spec values whose keys look like secrets must be `{"secretRef": {"store": …, "key": …}}`
objects, and values that look like key material (PEM private keys, well-known token prefixes) are refused.

`POST /kinds/{name}/schema-check` with `{"schema": {...}, "spec": {...}}` dry-runs a schema: it returns `valid` (with
`error` when it is not), `compatibility_problems` against the kind's storage version, and `sample_errors` for the
optional sample spec. It writes nothing.

## Versions
`POST /kinds/{name}/versions` with `{"schema": {...}, "breaking": false, "migration": ""}` publishes the next version
(`v2`, `v3`, …), which becomes the **storage version**. Every live entity of the kind is revalidated in the same
transaction, and the response reports `version`, `compatibility_problems`, `revalidated` and `invalid`.

| Mode | Publishes when | Otherwise |
|---|---|---|
| `breaking: false` (no `migration`) | the schema passes the compatibility check **and** every entity validates | `409`, with each compatibility problem as a `fields` entry |
| `breaking: true, migration: revalidate` | every entity validates (compatibility problems are allowed) | `409 … do not validate against vN` |
| `breaking: true, migration: mark_invalid` | always | — failing entities are marked invalid, never dropped |

`migration` without `breaking` is refused, and so is `breaking` without a migration mode.

The compatibility check compares the new schema to the storage version, recursively through `properties` and
`items`, and reports changes that could reject data valid under the old version:
- a type no longer accepted (`integer` → `number` is a widening and allowed), or a type constraint added;
- an enum value removed or an enum added;
- `minimum`, `exclusiveMinimum`, `minLength`, `minItems`, `minProperties` raised or added; the matching maximums
  lowered or added;
- a property that became `required`;
- a property removed while `additionalProperties` is `false`, or a property added with constraints while old data
  could already contain it (`additionalProperties` allowed);
- `additionalProperties` restricted, or an `items` constraint added;
- `pattern`, `const`, `format`, `$ref`, `allOf`, `anyOf`, `oneOf`, `not`, `if`/`then`/`else`, `dependentRequired`,
  `dependentSchemas`, `propertyNames`, `contains`, `uniqueItems`, `multipleOf` or `patternProperties` added or changed
  (these are compared for equality, not analyzed).

## Validation and findings
Entities are validated against the kind's storage version whenever they are recomputed:
- declarations written through the API, the Terraform provider or the UI are **rejected** with `422 spec does not
  validate against kind …` and per-field errors (`spec/<pointer>`, at most 50);
- facts from sources (imports, push, Pulse, connectors) are **never** rejected for schema reasons: the entity is
  stored, marked `valid: false` with its validation errors, and an `invalid_schema` finding (warning, confirmed) is
  raised until it validates again. `mark_invalid` publications produce the same findings.

## Relationship types
| Field | Rules |
|---|---|
| `name` | same rules as kind names (`example.com/powers`); fixed after creation |
| `inverse_name` | optional identifier shown on the target, e.g. `powered_by` |
| `title` | required, at most 100 bytes; `description` at most 2000 bytes |
| `directed` | default `true` |
| `from_kinds`, `to_kinds` | existing kinds allowed as endpoints (empty = any); writes with other kinds fail |
| `default_evidence` | `structural` (default) or `observed` |
| `cardinality` | `many` (default) or `one`: an entity may have at most one live relationship of the type; a second target is refused with `409` |
| `impact` | `forward` (the `from` entity depends on `to`; default), `reverse` (`to` depends on `from`), `both`, `none` |
| `attributes_schema` | optional schema for relationship `attributes`, same rules as kind schemas; violations are reported under `attributes/…` |
| `color` | `#rrggbb` |

`POST /relationship-types` creates one; `PUT /relationship-types/{name}` replaces it (send every field; `If-Match`
supported). Narrowing `from_kinds` or `to_kinds` is refused with `409 N existing relations would violate the new
endpoint constraint` while live relations use other kinds.

## Deleting
- `DELETE /kinds/{name}` fails with `409 N live entities still use kind …`. Tombstoned entities of the kind do not
  block it: they are removed immediately, so a later kind with the same name starts clean. The kind's schema
  versions are deleted with it.
- `DELETE /relationship-types/{name}` fails with `409 N live relations still use …`; retracted relations of the type
  are removed with it.

Changes are audited (`kind.created`, `kind.updated`, `kind.version_published`, `kind.deleted`, `reltype.created`,
`reltype.updated`, `reltype.deleted`) and published through the outbox as `evt.deed.catalog.kind.<action>` and
`evt.deed.catalog.reltype.<action>`.

## Terraform
The [Terraform provider](https://github.com/aurimasniekis/terraform-provider-deed) manages both with an
installation admin (or owner) token:

```terraform
resource "deed_kind" "ups" {
  name          = "example.com/ups"
  title         = "UPS"
  icon          = "tabler:battery-charging"
  identity_keys = [["/spec/serial"]]

  schema = jsonencode({
    type                 = "object"
    additionalProperties = false
    required             = ["capacity_va"]
    properties = {
      capacity_va = { type = "integer", minimum = 0 }
      serial      = { type = "string" }
    }
  })

  # Incompatible schema changes:
  # breaking  = true
  # migration = "mark_invalid"
}

resource "deed_relationship_type" "powers" {
  name         = "example.com/powers"
  inverse_name = "powered_by"
  title        = "Powers"
  from_kinds   = [deed_kind.ups.name]
  to_kinds     = ["server", "storage", "network"]
  impact       = "reverse" # what is powered depends on the UPS
}
```

- Changing `schema` publishes a new version; the apply fails with Deed's compatibility problems unless the change is
  compatible or `breaking` and `migration` are set. `schema` is compared semantically, so Deed's re-encoding never
  causes a diff. `storage_version` is exported.
- `breaking` and `migration` are used only when `schema` changes and are not read back.
- Changing `name` forces a new resource. Import by name: `terraform import deed_kind.ups example.com/ups`,
  `terraform import deed_relationship_type.powers example.com/powers`.
- Built-in kinds and relationship types cannot be managed; read them with the `deed_kind` and
  `deed_relationship_type` data sources.

## Validation status
Covered by `internal/catalog` tests (schema limits, compatibility rules, strict declarations, identity-key
convergence, relationship endpoint constraints) and `internal/httpapi/provider_regressions_test.go` (deleting a kind
and relationship type after their users are destroyed). Schema publication modes are exercised by the Terraform
provider's acceptance tests (`TestAccKind_schemaVersions`) against a real Deed server; the Deed repository has no unit
test for `PublishVersion` yet.
