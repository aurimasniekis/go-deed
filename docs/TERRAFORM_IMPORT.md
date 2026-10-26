# Terraform state import

The `terraform_state` source provider (ADR-006 A) turns Terraform state into catalog facts in the `imported` layer.
State is treated as highly sensitive: it is parsed in memory with a size cap, never stored, never logged and never
echoed in errors. Only attribute paths allowlisted by a mapping are read, and resource types without a mapping are
counted in diagnostics and otherwise ignored.

## Backends
A `terraform_state` source has one backend (`config.backend`, default `push`):

| Backend | How state arrives | Schedule |
|---|---|---|
| `push` | CI or `deed import terraform` uploads to `POST /api/v1/sources/{id}/terraform/state` | none (push only) |
| `s3` | Deed reads one object from an S3-compatible backend | default every 900 s |
| `http` | Deed sends `GET` to a Terraform HTTP backend URL and expects `200` | default every 900 s |

Pull backends (`s3`, `http`) run on the schedule, on `POST /sources/{id}/sync`, and as a dry run on
`POST /sources/{id}/preview`. They go through the SSRF-safe client: only the configured origin is reachable (for S3,
the endpoint and, unless `lookup: path`, `<bucket>.<endpoint>`), private ranges need `allow_cidrs`, `http://` needs
`insecure_http`, and each fetch times out after 2 minutes. `POST /sources/{id}/test` downloads and parses the state
and reports the format, Terraform version, serial and how many managed resources are mapped, without importing.

## Configuration
| Key | Default | Meaning |
|---|---|---|
| `backend` | `push` | `push`, `s3` or `http` |
| `s3.endpoint` | — | `host[:port]` of the S3 API (a value containing `/` is refused) |
| `s3.bucket`, `s3.key` | — | object location, e.g. `env:/prod/core.tfstate` |
| `s3.region` | — | bucket region; empty asks the bucket (GetBucketLocation). If set it must be the region the server signs with (Garage: its `s3_region`, default `garage`), or the test reports `the S3 server rejected the request region` / `answered 400 Bad Request (check s3.region …)` |
| `s3.use_ssl` | `true` | `false` uses `http://` |
| `s3.lookup` | `auto` | `auto`, `path` or `virtual` bucket addressing |
| `http.url` | — | Terraform HTTP backend state URL |
| `workspace` | — | up to 63 characters; labels entities `terraform/workspace` and prefixes external IDs so several states can share a project |
| `infer_dependencies` | `false` | turn untyped Terraform dependencies into inferred `depends_on` relationships |
| `mappings` | `[]` | custom mappings (up to 200), checked before the built-in catalog |
| `allow_cidrs` | `[]` | private CIDRs this source may reach (e.g. a LAN MinIO) |
| `insecure_http` | `false` | allow `http://` (development or trusted LAN only) |
| `max_bytes` | 64 MiB | state size cap, 1024 B to 512 MiB |

Credentials go in the source's write-only `secrets` (sealed, never returned by the API): `s3_access_key_id` and
`s3_secret_access_key`, or `http_token` (sent as `Authorization: Bearer`) or `http_username` / `http_password`
(basic auth; used only when `http_token` is empty). Give them read access to the state object only.

```sh
curl -s -X POST https://deed.example.com/api/v1/sources \
  -H "Authorization: Bearer $DEED_TOKEN" -H 'Content-Type: application/json' -d '{
    "project": "infra", "name": "tf-core", "provider": "terraform_state",
    "config": {"backend": "s3", "workspace": "prod", "allow_cidrs": ["10.0.0.0/8"],
      "s3": {"endpoint": "minio.example.com:9000", "bucket": "tfstate", "key": "env:/prod/core.tfstate", "region": "eu-central-1"}},
    "secrets": {"s3_access_key_id": "EXAMPLE", "s3_secret_access_key": "EXAMPLE"}
  }'
```

## Supported formats
- Raw state, `"version": 4` (`terraform.tfstate`, what backends store).
- `terraform show -json` output with `format_version` `1.x` (walks the root module and child modules).

Anything else fails with ``not a supported Terraform state: expected state version 4 or `terraform show -json`
format_version 1.x``. Oversized input fails with `state exceeds N bytes`; malformed input with `state is not valid
JSON`. Managed resources are read; a data source is read only when a mapping names it (`resource: data.<type>`, for
example a Cloudflare zone or an ESXi host that a configuration only looks up), and unmapped data sources are not
reported as unsupported. Outputs are never read.

## Pushing state
```sh
export DEED_ENDPOINT=https://deed.example.com
export DEED_SOURCE_TOKEN=deed_st_EXAMPLE…
terraform state pull | deed import terraform -source src_EXAMPLE -dry-run
deed import terraform -source src_EXAMPLE -file terraform.tfstate
```

| Flag | Default | Meaning |
|---|---|---|
| `-endpoint` | `$DEED_ENDPOINT` | Deed base URL |
| `-source` | — | source ID (`src_…`) of a `terraform_state` source |
| `-file` | `-` | state file or `terraform show -json` output; `-` reads stdin |
| `-dry-run` | `false` | preview without writing |
| `-partial` | `false` | upsert only; never remove resources missing from this state |
| `-allow-mass-removal` | `false` | allow a commit that removes more than the source's guard |
| `-ca-file` | `$DEED_CA_FILE` | extra CA bundle (PEM) |
| `-json` | `false` | print the server's JSON response |

The token comes from `$DEED_SOURCE_TOKEN`, falling back to `$DEED_TOKEN`; endpoint, source and token are required
(exit code 2 otherwise). The file is streamed to the server and never printed; redirects are not followed and the
request times out after 5 minutes. The CLI prints a summary (`created`, `updated`, `unchanged`, `removed`,
`errors`, `unresolved`) and each diagnostic.

The endpoint accepts a source token issued for that source (`POST /sources/{id}/tokens`, shown once) or a project
editor's credentials. The body is capped at 512 MiB by the HTTP layer and at the source's `max_bytes` by the parser.
The source must be enabled and of provider `terraform_state`.

## Snapshots, dry runs and partial imports
Every upload and every pull sync is a snapshot of the source. By default it is **complete**: facts this source
wrote earlier that are absent from the new state are handled by the source's `policy.on_absent` (`tombstone` by
default, or `orphan` / `keep`). With `partial=true` (`-partial`) the upload only upserts; nothing is removed.
Pull syncs are always complete.

The mass-removal guard refuses a complete snapshot that would remove more than `policy.max_removal_percent` of the
source's live facts (default `limits.max_removal_percent`, 25) when more than 3 facts would go. The push endpoint
answers `409 mass_removal_refused` with `commit would remove N of M live facts (…); set allow_mass_removal to
proceed`. Pull syncs have no override: raise the source's policy instead.

`dry_run=true` (`-dry-run`) and `POST /sources/{id}/preview` return the commit result with `dry_run: true` and a
list of changes (capped at 500) without writing anything.

## Allowlist-only extraction
For each mapped resource Deed reads only the attribute paths named in the mapping's `fields`, `join` and relation
attributes, plus `id`. Every path is checked individually:
- paths Terraform marks sensitive are skipped even when allowlisted, including parents and children of a sensitive
  path; an unrecognised `sensitive_attributes` shape marks the whole resource sensitive;
- values that look like key material are refused: PEM `BEGIN … PRIVATE KEY` / `ENCRYPTED` blocks, AWS access key
  IDs (`AKIA…`, `ASIA…`), GitHub tokens (`ghp_`, `gho_`, …) and Slack tokens (`xox…`), and any string over 4096
  characters; a list or map containing such a value is refused as a whole;
- lists are truncated to 100 elements.

`join` attributes are held in memory for relationship matching and never stored. Diagnostics carry counts and
resource types only: `state` (format, version, serial, resource count), `unsupported_resource` (no mapping, per
type), `sensitive_dropped`, `secret_like_dropped` and `mapping` warnings such as name collisions.

Each entity gets `metadata.labels` `terraform/type` (and `terraform/workspace`), `metadata.annotations`
`terraform/address`, the mapping's icon, and the external ID `<workspace>:<address>` (or the address alone).
Names come from the mapping's `name` template, else the resource name with index and module prefixed; a name that
collides within a kind gets a `-2`, `-3`, … suffix.

## Built-in mappings
Mappings are embedded from `internal/tfimport/mappings/*.yaml`:

| File | Resource type | Kind | Inferred relationships |
|---|---|---|---|
| `aws.yaml` | `aws_instance` | vm | |
| | `aws_route53_zone` | dns_zone | |
| | `aws_route53_record` | dns_record | zone `contains` record (`zone_id`); record `resolves_to` vm/server (`/spec/ipv4`) |
| | `aws_s3_bucket` | storage | |
| | `aws_db_instance` | database | |
| `certificates.yaml` | `acme_certificate`, `tls_self_signed_cert`, `tls_locally_signed_cert` | certificate | |
| `cloudflare.yaml` | `cloudflare_zone`, `data.cloudflare_zone` | dns_zone | |
| | `cloudflare_record` (v4), `cloudflare_dns_record` (v5) | dns_record | zone `contains` record (`zone_id` = zone `id`); record `resolves_to` server/vm/hypervisor/docker_host (`/spec/ipv4`, `/spec/ipv6`) |
| | `cloudflare_ruleset` | firewall_rule | zone `protected_by` ruleset (`zone_id`) |
| | `cloudflare_r2_bucket` | storage | |
| | `cloudflare_origin_ca_certificate` | certificate | record `secured_by_certificate` certificate (`hostnames` = record `/spec/fqdn`) |
| `deed-family.yaml` | `hallpass_server` | hallpass_server | server/vm/hypervisor `protected_by` it (`address` = `/spec/ipv4` or `/spec/hostname`) |
| | `hallpass_role` | hallpass_role | server `contains` role (`server_id`) |
| | `porch_application` | porch_app | |
| `digitalocean.yaml` | `digitalocean_droplet` | vm | |
| | `digitalocean_domain` | dns_zone | |
| | `digitalocean_record` | dns_record | domain `contains` record; record `resolves_to` server/vm/docker_host |
| `docker.yaml` | `docker_container` | container | container `member_of` network (first network name) |
| | `docker_network` | network | |
| | `docker_hub_repository` | repository | |
| `github.yaml` | `github_repository` | repository | |
| | `github_repository_pages` | application | `deployed_from` repository (`repository` = `name`); DNS record `resolves_to` it (`cname` = `/spec/fqdn`) |
| `google.yaml` | `googleworkspace_domain` | domain | |
| `grafana.yaml` | `grafana_dashboard` | grafana_dashboard | |
| `hetzner.yaml` | `hcloud_server` | vm | |
| | `hcloud_network` | network | |
| | `hcloud_volume` | storage | server `stored_in` volume (`server_id` = server `id`) |
| `proxmox.yaml` | `proxmox_vm_qemu`, `proxmox_virtual_environment_vm` | vm | `runs_on` hypervisor (`target_node` / `node_name` = `/spec/hostname`, confidence 0.9) |
| | `proxmox_lxc` | container | `runs_on` hypervisor (`target_node`) |
| `unifi.yaml` | `unifi_network`, `unifi_wan` | network | |
| | `unifi_wlan` | network (`type: wlan`) | `connects_to` its network (`network_id`) |
| | `unifi_device` | network_device | |
| | `unifi_client` | network_client | `connects_to` its network (`network_id`) |
| | `unifi_firewall_policy` | firewall_rule | `references` source and destination networks (`network_ids`) |
| | `unifi_port_forward` | firewall_rule (`type: port_forward`) | `routes_to` the client/server/vm with the forward IP (`/spec/ipv4`) |
| | `unifi_vpn_server` | service (`type: vpn`) | |
| | `unifi_wireguard_peer` | network_client (`type: vpn_peer`) | `connects_to` its VPN server (`network_id`) |
| `vsphere.yaml` | `data.vsphere_host` | hypervisor | |
| | `vsphere_virtual_machine` | vm | `runs_on` hypervisor (`host_system_id`); `stored_in` datastore (`datastore_id`) |
| | `vsphere_vmfs_datastore` | storage | `hosted_on` hypervisor |
| | `vsphere_host_virtual_switch` | network_device (`type: virtual_switch`) | `hosted_on` hypervisor; `contains` its port groups (`virtual_switch_name`) |
| | `vsphere_host_port_group` | network (`type: port_group`) | |

Not mapped on purpose: secrets and key material (`onepassword_item`, `tls_private_key`, `random_*`, `gpg_private_key`,
`*_access_token`, `github_actions_secret`, HallPass login and host keys) and per-setting resources without inventory
value (`cloudflare_zone_setting`).

Certificate mappings read public metadata only (names, SANs, validity), never key or certificate PEM.
`GET /api/v1/providers` lists the kinds the provider can produce.

## Inference joins
A relation rule matches an attribute of one resource against either a raw attribute of another mapped resource in
the same state (`target_attribute`) or a field of its entity (`target_pointer`), restricted to `target_kinds`.
Values are compared lower-cased with a trailing `.` removed; list attributes match element-wise.
- Rules with `confidence: 1` (id joins) produce **structural** relationships; others are **inferred** (default
  confidence 0.9). Evidence records the rule, attribute, matched value and both Terraform addresses.
- A `target_pointer` rule with no match in the state is looked up among existing entities of the source's project
  (confidence 0.8, inferred), so a DNS record can point at a server another source owns.
- A dependency on a `count`/`for_each` resource links to every one of its instances.
- Terraform dependencies become relationships only through a mapping's `dependencies` rules (structural, none of
  the built-in mappings define any) or, with `infer_dependencies`, as `depends_on` (inferred, confidence 0.6).

## Custom mappings
`config.mappings` takes the same shape as the built-in YAML and is checked before it, so it can add a type or
override one:

```json
{"resource": "example_server", "kind": "server", "icon": "tabler:server",
 "name": "{{ default .name .attr.hostname | slug }}",
 "fields": {"/spec/hostname": "hostname", "/spec/ipv4": "ipv4|private_ip", "/spec/provider": "const:example"},
 "labels": {"team": "platform"},
 "relations": [{"type": "runs_on", "attribute": "host", "target_kinds": ["hypervisor"], "target_pointer": "/spec/hostname"}]}
```

- `resource` is a type name or glob (`[a-z0-9_*?]`); `kind` is required.
- `fields` map JSON pointers under `/spec/` or `/metadata/` to expressions: dotted paths with numeric indexes
  (`network_data.0.ip_address`), `a|b` fallbacks, `first:list`, `lower:attr`, `json:attr.key` (a key inside a
  JSON-encoded string attribute) and `const:value`.
- `name` and `display_name` are Go templates (≤ 512 characters) over `.name`, `.index`, `.module`, `.address`,
  `.type` and `.attr` (allowlisted attributes only), with `slug`, `lower`, `trimSuffix` and `default`.
- `join`, `relations` (`type`, `direction` `out`/`in`, `attribute`, `target_kinds`, `target_attribute` or
  `target_pointer`, `confidence`), `dependencies` (`resource`, `type`, `direction`) and static `labels`.

Invalid mappings — and unknown keys anywhere in the source config — are refused when the source is saved
(`mapping <resource>: …`, `invalid terraform_state config: unknown field …`).

## Validation status
`internal/tfimport` tests cover the built-in catalog, v4 and show-json fixtures with planted secrets
(`internal/tfimport/testdata`) and parser rejections; `FuzzParse` fuzzes the parser. `TestS3Backend` reads a state
object (sync, test, missing key, wrong secret, wrong region) from a real S3 server: MinIO in CI, and SeaweedFS 4.47,
Garage v2.4.1 and Versity S3 Gateway v1.8.0 through `scripts/validate/s3.sh`. Push is verified via the CLI.
