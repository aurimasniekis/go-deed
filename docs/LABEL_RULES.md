# Label rules and overrides

Most entities belong to their sources: a Terraform state, a connector, an agent. Two tools change them without taking
them over, so an entity still lives exactly as long as its sources describe it.

| | Label rules | Overrides |
|---|---|---|
| For | the same labels on many entities (`group=github` on everything the GitHub connector imports) | one-off fixes on one entity (display name, icon, owner, a wrong imported value) |
| Precedence | **below** every source: a source's own label for the key wins | **above** every source (layer `override`) |
| Writes | labels only | any metadata field and spec values |
| Keeps an entity alive | never | never |
| API | `/projects/{project}/label-rules` | `PUT /entities/{id}/declarations/overrides` |
| Terraform | `deed_label_rule` | `deed_entity_override` |

## Label rules
A rule adds labels to the project's entities its `match` selects:

```json
{"name": "cloudflare-rulesets",
 "match": {"kinds": ["firewall_rule"], "sources": ["tf-cloudflare-*"], "labels": {"terraform/type": "cloudflare_ruleset"}},
 "labels": {"group": "cloudflare"}}
```

- **Match:** every given criterion must hold (an empty match selects every entity); within `kinds` and `sources` any
  entry may match. Kinds, source names and label values are globs (`*`, `?`, `[…]`). `sources` are the names of the
  sources that currently describe the entity; `labels` are the labels those sources set — never labels added by other
  rules, so the order of rules does not chain.
- **Precedence:** rules are a layer of their own (`rule`, below `discovered`). A source that sets the same key wins;
  between two rules that set one key, the older rule wins (no conflict is recorded).
- **Lifetime:** a rule never keeps an entity alive, and when its sources describe the entity again the rule applies
  again. Creating, changing or deleting a rule re-merges the project's entities before the request returns (in
  batches of 200; entities whose labels do not change emit nothing).
- **Provenance** shows the rule as the source of the label (*Label rule* layer), e.g. `label rule cloudflare-rulesets`.
- **Limits:** 200 rules per project, 32 labels per rule, 50 entries per match list; keys follow the label key format,
  values are 1–256 bytes, and keys that look like secrets are refused.
- **Who:** viewers read rules; project admins change them. They are managed under *Settings → Projects → (project) →
  Label rules*, through the API, or with Terraform:

```hcl
resource "deed_label_rule" "github" {
  project = "infra"
  name    = "github"
  match   = { sources = ["github"] }
  labels  = { group = "github" }
}
```

`GET /entities/label-keys` lists the label keys in use with entity and value counts.

### On the map
- **Group by label:** the overview's *Group by* lists the label keys in use (`Group by label group`): one group per
  value (entities without the key form a `(none)` group), expandable like any other group (`group=label:<key>` in the
  URL; `group_by=label:<key>` in `/graph/overview`).
- **Show / hide** (`v`): the *Labels* section picks a key and lists its values with counts; untick a value to hide its
  entities (and its overview group), *only* keeps one value, *Show all* resets. Hidden values are the
  `hideLabel=key:value` URL filter, saved with views. Entities without the key are never hidden by it.

## Overrides
An override is the project's `overrides` source (layer `override`, priority 100: above declarations and imports)
declaring fields of an entity other sources describe:

```http
PUT /entities/{id}/declarations/overrides
{"metadata": {"displayName": "App", "icon": "simple-icons:github", "owner": "platform",
              "labels": {"team": "platform"}}, "spec": {"visibility": "internal"}}
```

- **Fields** merge per field like every facet: labels and annotations per key, `links` and `tags` as unions, spec
  values per pointer. What the override does not set keeps coming from the sources.
- **Lifetime:** annotations and overrides only change entities other sources describe; alone they never keep one
  alive. When the last source lets go, the entity is tombstoned and its override goes dormant: reading it returns
  404 (so Terraform plans to create it again) and writing it returns 409 (*the entity is gone*), until a source
  describes the entity again — then the dormant override applies again as it was.
- **Who:** project admins (the override layer). Provenance shows the `overrides` source; the audit log and history
  show who changed it.
- **Terraform:** `deed_entity_override` targets an entity by `entity_id`, or by `project`, `kind` and `name`, and
  reads back only what the override sets, so values from other sources never cause a diff; destroying it removes only
  the override.
