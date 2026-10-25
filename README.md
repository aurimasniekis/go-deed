<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/assets/deed-wordmark-dark.svg">
    <img src="docs/assets/deed-wordmark-light.svg" alt="Deed" height="64">
  </picture>
</p>

# Deed

**Declarative infrastructure catalog and live relationship map, powered by Go, Terraform, and React Flow.**

Deed is a source-aware graph of infrastructure, software, identities and operational relationships. It answers what
you have, where it runs, who owns it, how it is connected, what depends on it, which services publish or consume
which Pulse/NATS events, and — for every value — where it came from and how fresh it is.

> **Status:** under active development — not released, not externally security reviewed. The capability matrix is
> [docs/ACCEPTANCE.md](docs/ACCEPTANCE.md); what has actually been run end to end (and what is still pending against
> real services) is in [docs/VALIDATION.md](docs/VALIDATION.md). Documentation site: https://aurimasniekis.github.io/go-deed/

## What it does
- **Catalog** of entities (built-in kinds plus custom kinds with versioned JSON Schemas) in projects, connected by
  typed relationships with evidence (declared, structural, observed, inferred).
- **Layered facts:** every source writes its own facet; a deterministic field-level merge with provenance answers
  "why does this value exist?", and disagreements surface as conflicts instead of silent overwrites.
- **Sources:** the [Terraform provider](https://github.com/aurimasniekis/terraform-provider-deed) (`aurimasniekis/deed`),
  Terraform state import (push, S3, HTTP backend; allowlisted attributes only), GitHub, HallPass and Porch connectors,
  Pulse (admin API, read-only `evt.>` tail, ingest consumer), and any program with a source token
  (`pkg/deedclient`, `deed-agent` for Docker).
- **Map:** server-side neighborhoods, impact and path queries with node caps and fan clustering, saved views, live
  updates over SSE, traffic overlays only where a measurement exists.
- **Operations:** findings and alerts (expiring certificates, drift, stale sources, end-of-life versions,
  unreachable integrations) delivered as Pulse events; named telemetry bindings for Prometheus/Mimir, Loki, NATS
  monitoring, OpenSearch and Grafana; Prometheus metrics, Grafana dashboard and alert rules.
- **Security and durability:** Argon2id + TOTP + recovery codes, OIDC SSO (Porch first), project roles and scoped
  tokens, CSRF, SSRF-guarded outbound calls, hash-chained audit log, age-encrypted backups with signed manifests and
  tested restore.

## Quick start
```sh
make build
bin/deed migrate -config deploy/dev/deed.yaml
bin/deed admin bootstrap -config deploy/dev/deed.yaml   # single-use setup link for the first owner
bin/deed serve -config deploy/dev/deed.yaml             # http://127.0.0.1:8787
```
`deploy/compose.dev.yaml` starts NATS, MinIO, Prometheus, Loki and Grafana; `scripts/e2e.sh` runs the full acceptance
scenario against them. Production: [docs/DEPLOYMENT.md](docs/DEPLOYMENT.md), behind Cloudflare:
[docs/CLOUDFLARE.md](docs/CLOUDFLARE.md).

## Documentation
| Topic | Document |
|---|---|
| Decisions | [Discovery](docs/DISCOVERY.md), [ADRs](docs/adr/README.md), [Threat model](docs/THREAT_MODEL.md) |
| Operating | [Deployment](docs/DEPLOYMENT.md), [Cloudflare](docs/CLOUDFLARE.md), [SSO](docs/SSO.md), [Backups](docs/BACKUP.md), [Operations](docs/OPERATIONS.md) |
| Integrating | [Ingest API](docs/INGEST_API.md), [Terraform import](docs/TERRAFORM_IMPORT.md), [Connectors](docs/CONNECTORS.md), [Pulse](docs/PULSE.md), [Telemetry](docs/TELEMETRY.md), [Agent and SDK](docs/AGENT.md) |
| Modelling | [Custom kinds](docs/CUSTOM_KINDS.md), [Findings and alerts](docs/FINDINGS.md), [Icons](docs/ICONS.md) |
| Quality | [Testing](docs/TESTING.md), [Validation](docs/VALIDATION.md), [Performance](docs/PERFORMANCE.md) |
| Releases | [Versioning](docs/VERSIONING.md), [Compatibility](docs/COMPATIBILITY.md), [Releasing](docs/RELEASING.md), [Changelog](CHANGELOG.md) |

Security reports: see [SECURITY.md](SECURITY.md). Contributions: [CONTRIBUTING.md](CONTRIBUTING.md).

## License
Apache License 2.0. Copyright 2026 Aurimas Niekis. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
