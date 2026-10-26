# Compatibility

## Application ↔ Terraform provider
The application (`github.com/aurimasniekis/go-deed`) and the provider (`github.com/aurimasniekis/terraform-provider-deed`,
registry address `registry.terraform.io/aurimasniekis/deed`, source address `aurimasniekis/deed`) are versioned and
released independently. The provider talks only to `/api/v1`, which changes additively (see VERSIONING.md), so a
provider works with every Deed release at or above its minimum version.

| Provider | Minimum Deed | Tested against (CI pin) | Terraform |
|---|---|---|---|
| 0.3.x | 0.1.0; 0.2.0 for the service account resources; 0.3.0 for `deed_kind.grouping_relationship_types` | go-deed `v0.3.0` (`DEED_COMMIT` in the provider `Makefile` and `test.yml`) |
| 0.2.x | 0.1.0; 0.2.0 for `deed_service_account` and `deed_service_account_token` | go-deed `v0.2.0` |
| 0.1.x | 0.1.0 | go-deed `v0.1.0` | ≥ 1.11 (tested with 1.11.4) |

Recorded validation pairs: the provider acceptance suite (`make testacc`) and its scenario passed against go-deed
`569e2db` (provider `5c8537a`) and against go-deed `v0.1.0` (provider `v0.1.0`); the §18 end-to-end run in
`docs/VALIDATION.md` names the exact go-deed and provider commits it used. Published releases: go-deed v0.1.0, v0.2.0 and v0.3.0,
provider v0.1.0, v0.2.0 and v0.3.1 (2026-10-26; provider v0.3.0 was built from the 0.2.0 commit and must not be used); the provider acceptance suite passed against go-deed `v0.2.0` (provider `v0.2.0`).

The provider reads `GET /api/v1/system/info` during configure and warns when the server is older than its minimum
version or reports an API major version it does not know.

## Runtime dependencies
| Component | Supported | Notes |
|---|---|---|
| Go toolchain (build) | 1.27.3 | `go.mod` |
| Node (build) | 24.x, pnpm 10 | web UI |
| Pulse | go-pulse pseudo-version pinned in `go.mod` | NATS 2.10+ JetStream |
| OIDC providers | any OpenID Connect Core 1.0 OP with PKCE S256 | Porch is the primary documented provider |
| S3 | AWS S3, MinIO, SeaweedFS, Garage, Versity S3 Gateway, Cloudflare R2, Backblaze B2 (S3 API) | backups and Terraform state; validated locally against MinIO, SeaweedFS 4.47, Garage v2.4.1 and Versity S3 Gateway v1.8.0 (`scripts/validate/s3.sh`); AWS S3, R2 and B2 not validated. Leave `region` empty or set the server's (Garage: `garage`); `backup.s3.sse: false` where SSE-S3 is unsupported (docs/BACKUP.md) |
| Terraform state | state `version: 4`; `terraform show -json` `format_version` 1.x | Terraform 0.12 – 1.x, OpenTofu |
| Prometheus API | Prometheus 2.x/3.x, Mimir | `X-Scope-OrgID` supported; validated against Mimir 3.2.1 (`scripts/validate/telemetry.sh`) |
| Loki | 2.9+ / 3.x | |
| OpenSearch | 2.x, 3.x | validated against 3.8.0 with the security plugin (`scripts/validate/telemetry.sh`) |
| GitHub REST API | `X-GitHub-Api-Version: 2022-11-28`; github.com and GitHub Enterprise Server (`/api/v3`) | fine-grained read-only token |
| HallPass | `/api/v1` as described by HallPass `api/openapi.yaml` | auditor `hallpass_at_` token |
| Porch | `/api/v1` as described by Porch `api/openapi.json`; validated against go-porch `9940962` | tenant `porch_at_` token with `app:manage` + `service_account:manage` (Porch has no read-only permission for them) |
