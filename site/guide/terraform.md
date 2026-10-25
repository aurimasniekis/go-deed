# Terraform provider

The provider lives in its own repository:
[aurimasniekis/terraform-provider-deed](https://github.com/aurimasniekis/terraform-provider-deed) (registry address
`registry.terraform.io/aurimasniekis/deed`; not published to the Registry yet — build it from source and use
`dev_overrides` until then).

```hcl
terraform {
  required_providers {
    deed = { source = "aurimasniekis/deed" }
  }
}

provider "deed" {
  endpoint = "https://deed.example.com" # or DEED_ENDPOINT
  # token  = DEED_TOKEN: a deed_at_ user API token (installation admin for kinds, project editor for entities)
}

resource "deed_entity" "nas" {
  project      = "home"
  kind         = "server"
  name         = "nas-01"
  display_name = "NAS"
  owner        = "platform"
  spec         = jsonencode({ hostname = "nas-01.example.com", ipv4 = "192.0.2.20" })
}

resource "deed_relationship" "media_on_nas" {
  type = "stored_in"
  from = { kind = "service", name = "media" }
  to   = { id = deed_entity.nas.id }
  project = "home"
}
```

Resources: `deed_project`, `deed_project_member`, `deed_kind`, `deed_relationship_type`, `deed_entity`,
`deed_relationship`. Data sources: `deed_project`, `deed_kind`, `deed_relationship_type`, `deed_entity`,
`deed_entities`, `deed_relationship`.

The provider manages only its own declaration: destroying a resource removes the Terraform facet, and an entity
other sources still report stays in Deed. A second plan after apply is empty; imports, refresh after out-of-band
deletes, ETag-guarded updates and idempotent retries are covered by its acceptance tests against a real Deed binary.
