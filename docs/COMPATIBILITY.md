# Compatibility

## Application ↔ Terraform provider
The application (`github.com/aurimasniekis/go-deed`) and the provider (`github.com/aurimasniekis/terraform-provider-deed`,
registry address `registry.terraform.io/aurimasniekis/deed`, source address `aurimasniekis/deed`) are versioned and
released independently. The provider talks only to `/api/v1`, which changes additively (see VERSIONING.md), so a
provider works with every Deed release at or above its minimum version.

| Provider | Minimum Deed | Tested against (CI pin) | Terraform |
|---|---|---|---|
| 0.1.x | 0.1.0 | see provider `Makefile` `DEED_COMMIT` | ≥ 1.11 |

The provider reads `GET /api/v1/system/info` during configure and warns when the server is older than its minimum
version or reports an API major version it does not know.

## Runtime dependencies
| Component | Supported | Notes |
|---|---|---|
| Go toolchain (build) | 1.27.3 | `go.mod` |
| Node (build) | 24.x, pnpm 10 | web UI |
| Pulse | go-pulse pseudo-version pinned in `go.mod` | NATS 2.10+ JetStream |
| OIDC providers | any OpenID Connect Core 1.0 OP with PKCE S256 | Porch is the primary documented provider |
| S3 | AWS S3, MinIO, Cloudflare R2, Backblaze B2 (S3 API) | backups and Terraform state |
| Terraform state | state `version: 4`; `terraform show -json` `format_version` 1.x | Terraform 0.12 – 1.x, OpenTofu |
| Prometheus API | Prometheus 2.x/3.x, Mimir | `X-Scope-OrgID` supported |
| Loki | 2.9+ / 3.x | |
| OpenSearch | 2.x | |
| GitHub REST API | `X-GitHub-Api-Version: 2022-11-28`; github.com and GitHub Enterprise Server (`/api/v3`) | fine-grained read-only token |
| HallPass | `/api/v1` as described by HallPass `api/openapi.yaml` | auditor `hallpass_at_` token |
| Porch | `/api/v1` as described by Porch `api/openapi.json`; validated against go-porch `9940962` | tenant `porch_at_` token with `app:manage` + `service_account:manage` (Porch has no read-only permission for them) |
