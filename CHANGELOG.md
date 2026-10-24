# Changelog

All notable changes are documented here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
and the project uses [Semantic Versioning](https://semver.org/) (docs/VERSIONING.md).

## [Unreleased]

### Added
- Source-aware catalog: projects and bridges, built-in and custom kinds (JSON Schema 2020-12 with compatibility
  checks), relationship types, layered facets with deterministic field-level merge, provenance, conflicts, change
  log and history.
- Authentication (local Argon2id + TOTP + recovery codes, OIDC SSO with JSON-path group claims, bootstrap and
  break-glass recovery), project and installation roles, user and source tokens, CSRF, audit hash chain.
- Ingest protocol (incremental items, snapshots with mass-removal guard, dry-run, observations), pluggable source
  providers: Terraform state (push, S3, HTTP backend), Pulse admin, Pulse `evt.>` tail, GitHub, HallPass, Porch.
- Pulse integration: outbox relay, ingest consumer, tail rollups; SSE live updates.
- Graph API (neighborhood, expand, path, impact, overview), saved views, custom icons.
- Telemetry integrations and named bindings (Prometheus/Mimir, Loki, NATS monitoring, OpenSearch, Grafana, Pulse
  rollups), traffic overlays, telemetry stream.
- Findings, alerts, version intelligence, dashboard summary.
- Encrypted backups (age, zstd, signed manifests, GFS retention) and offline restore.
- Go SDK (`pkg/deedclient`), `deed-agent` (Docker), synthetic demo seed.
- Deployment assets (distroless image, compose, systemd), Grafana dashboard and alert rules, Cloudflare guide.
